package finance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSettlementUsageDimensionsAcceptsUnitsAndLegacyQuantity(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      int64
	}{
		{"persisted units", `[{"dimension":"input_tokens","units":"16","unit_quota":"4.70","charged_quota":75,"price_source":"frozen"}]`, 16},
		{"legacy quantity", `[{"dimension":"input_tokens","quantity":16}]`, 16},
		{"matching formats", `[{"dimension":"input_tokens","units":"16","quantity":16}]`, 16},
		{"units zero", `[{"dimension":"input_tokens","units":"0"}]`, 0},
		{"legacy zero", `[{"dimension":"input_tokens","quantity":0}]`, 0},
		{"matching zero", `[{"dimension":"input_tokens","units":"0","quantity":0}]`, 0},
		{"exact beyond float precision", `[{"dimension":"input_tokens","units":"9007199254740993"}]`, 9007199254740993},
		{"maximum units", `[{"dimension":"input_tokens","units":"9223372036854775807"}]`, 9223372036854775807},
		{"maximum legacy", `[{"dimension":"input_tokens","quantity":9223372036854775807}]`, 9223372036854775807},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dimensions, err := parseSettlementUsageDimensions(tc.raw)
			require.NoError(t, err)
			require.Equal(t, []UsageDimension{{Dimension: "input_tokens", Quantity: tc.want}}, dimensions)
		})
	}
}

func TestSettlementUsageDimensionsRejectsInvalidOrAmbiguousCounts(t *testing.T) {
	invalid := map[string]string{
		"empty":                          "",
		"empty array":                    `[]`,
		"null":                           `null`,
		"object":                         `{}`,
		"malformed":                      `[{`,
		"trailing value":                 `[{"dimension":"input_tokens","units":"16"}] []`,
		"null row":                       `[null]`,
		"missing count":                  `[{"dimension":"input_tokens"}]`,
		"missing dimension":              `[{"units":"16"}]`,
		"empty dimension":                `[{"dimension":"","units":"16"}]`,
		"padded dimension":               `[{"dimension":" cache_read ","units":"16"}]`,
		"null quantity":                  `[{"dimension":"input_tokens","quantity":null}]`,
		"negative quantity":              `[{"dimension":"input_tokens","quantity":-1}]`,
		"fractional quantity":            `[{"dimension":"input_tokens","quantity":1.5}]`,
		"string quantity":                `[{"dimension":"input_tokens","quantity":"16"}]`,
		"overflow quantity":              `[{"dimension":"input_tokens","quantity":9223372036854775808}]`,
		"units quantity conflict":        `[{"dimension":"input_tokens","units":"16","quantity":15}]`,
		"zero units conflict":            `[{"dimension":"input_tokens","units":"0","quantity":1}]`,
		"zero quantity conflict":         `[{"dimension":"input_tokens","units":"1","quantity":0}]`,
		"null units no fallback":         `[{"dimension":"input_tokens","units":null,"quantity":0}]`,
		"null quantity no fallback":      `[{"dimension":"input_tokens","units":"0","quantity":null}]`,
		"invalid units no fallback":      `[{"dimension":"input_tokens","units":"bad","quantity":16}]`,
		"duplicate units member":         `[{"dimension":"input_tokens","units":"16","units":"17"}]`,
		"duplicate quantity member":      `[{"dimension":"input_tokens","quantity":16,"quantity":16}]`,
		"duplicate dimension member":     `[{"dimension":"input_tokens","dimension":"cache_read","units":"16"}]`,
		"case ambiguous units":           `[{"dimension":"input_tokens","units":"16","Units":"17"}]`,
		"case ambiguous quantity":        `[{"dimension":"input_tokens","quantity":16,"Quantity":17}]`,
		"duplicate dimension rows":       `[{"dimension":"cache_read","units":"16"},{"dimension":"cache_read","quantity":16}]`,
		"duplicate zero dimension rows":  `[{"dimension":"cache_read","units":"0"},{"dimension":"cache_read","quantity":0}]`,
		"generic plus five minute write": `[{"dimension":"cache_write","units":"16"},{"dimension":"cache_write_5m","units":"16"}]`,
		"hourly plus generic write":      `[{"dimension":"cache_write_1h","units":"16"},{"dimension":"cache_write","units":"16"}]`,
		"zero overlapping writes":        `[{"dimension":"cache_write","units":"0"},{"dimension":"cache_write_1h","units":"0"}]`,
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			dimensions, err := parseSettlementUsageDimensions(raw)
			require.Error(t, err)
			require.Nil(t, dimensions, "invalid input cannot expose partially accepted quantities")
		})
	}
	for _, raw := range []string{`""`, `"-1"`, `"-0"`, `"1.5"`, `"16.0"`, `"1e3"`, `"+16"`, `" 16"`, `"16 "`,
		`"NaN"`, `"Infinity"`, `"9223372036854775808"`, `"18446744073709551616"`, `16`, `null`, `true`, `{}`, `[]`} {
		t.Run("units/"+raw, func(t *testing.T) {
			dimensions, err := parseSettlementUsageDimensions(`[{"dimension":"input_tokens","units":` + raw + `}]`)
			require.Error(t, err)
			require.Nil(t, dimensions)
		})
	}
}

func TestSettlementUsageDimensionsKeepsCacheDimensionsExclusive(t *testing.T) {
	dimensions, err := parseSettlementUsageDimensions(`[
		{"dimension":"input_tokens","units":"1000000"},
		{"dimension":"output_tokens","quantity":1000000},
		{"dimension":"cache_read","units":"1000000"},
		{"dimension":"cache_write_5m","units":"1000000"},
		{"dimension":"cache_write_1h","units":"1000000"}
	]`)
	require.NoError(t, err)
	require.Equal(t, []UsageDimension{{"input_tokens", 1000000}, {"output_tokens", 1000000}, {"cache_read", 1000000},
		{"cache_write_5m", 1000000}, {"cache_write_1h", 1000000}}, dimensions)
	// An explicitly supplied group/tier is only a unit-test pricing fixture,
	// never a substitute for missing historical routing evidence.
	estimate, err := EstimateXcloudUsageForGroup("claude-fable-5", "claude-max", "base", dimensions)
	require.NoError(t, err)
	require.Equal(t, "46.875", estimate.CostUSDT, "each exclusive dimension is priced exactly once")
	_, err = EstimateXcloudUsage("claude-fable-5", dimensions)
	require.ErrorIs(t, err, ErrXcloudQuoteMissing)
}

func TestSettlementUsageDimensionsReportPreservesUnitsAndHistoricalFailClosed(t *testing.T) {
	s := fixture()
	s.Channels = []Channel{{ID: 2, Name: "Xcloud", BaseURL: "https://www.xcloudtoken.com"}}
	s.Settlements[0].PriceSnapshotJSON = `{"source_model":"claude-fable-5"}`
	s.Settlements[0].ChargeDimensionsJSON = `[{"dimension":"input_tokens","units":"16","unit_quota":"4.70","charged_quota":75},{"dimension":"cache_read","units":"3"}]`
	s.Settlements[0].MissingDimensionsJSON = `[]`
	r := calculate(t, s, facts()[:1])
	require.Len(t, r.Estimates, 1)
	require.Equal(t, map[string]int64{"input_tokens": 16, "cache_read": 3}, r.Estimates[0].Dimensions)
	require.False(t, r.Estimates[0].Complete)
	require.False(t, r.EstimateComplete)
	require.Nil(t, r.EstimatedCost)
	require.Nil(t, r.EstimatedGrossProfit)
	require.Nil(t, r.ActualCost)
	require.False(t, r.Confirmed)
	require.Contains(t, r.EstimateIssues, Issue{Code: "xcloud_quote_missing", Ref: "r1", UserID: 1})

	s.Settlements[0].ChargeDimensionsJSON = `[{"dimension":"input_tokens","units":"16"},{"dimension":"cache_read","units":"-1"}]`
	r = calculate(t, s, facts()[:1])
	require.Len(t, r.Estimates, 1)
	require.Empty(t, r.Estimates[0].Dimensions, "a malformed later dimension must not leave a partial rollup")
	require.Nil(t, r.EstimatedCost)
	require.Contains(t, r.EstimateIssues, Issue{Code: "xcloud_usage_missing", Ref: "r1", UserID: 1})
}
