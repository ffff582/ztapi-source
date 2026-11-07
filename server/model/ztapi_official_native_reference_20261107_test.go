package model

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func nativeReferenceJSON20261107(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := common.Marshal(value)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, common.Unmarshal(raw, &result))
	return result
}

func TestZTAPIPrices20261107NativeReferenceFrozenMetadata(t *testing.T) {
	cny := map[string]bool{"deepseek-v4-pro": true, "glm-5": true, "glm-5.1": true,
		"kimi-k2.5": true, "kimi-k2.6": true, "kimi-k2.7-code": true, "doubao-seedance-2-5": true}
	entries := ztapiOfficialComparisons20261107()
	require.Len(t, entries, 15)
	count := 0
	for source, entry := range entries {
		ref := nativeReferenceJSON20261107(t, entry.Reference)
		currency, units := "USD", "1"
		if cny[source] {
			currency, units = "CNY", "6.63"
			count++
		}
		require.Equal(t, currency, ref["original_currency"], source)
		require.Equal(t, units, ref["original_units_per_usd"], source)
	}
	require.Equal(t, 7, count)
	require.Equal(t, "9", decimal.RequireFromString(entries["deepseek-v4-pro"].OfficialUSD["input_tokens"]).Mul(decimal.RequireFromString("6.63")).Round(10).String())
	for _, rule := range entries["doubao-seedance-2-5"].MediaRules {
		if rule.ID == "1080p_video_false" {
			require.Equal(t, "77", decimal.RequireFromString(rule.OfficialUSD["input_tokens"]).Mul(decimal.RequireFromString("6.63")).Round(10).String())
		}
	}
}

func TestZTAPIPrices20261107NativeReferencePreservesFrozenDocument(t *testing.T) {
	var document map[string]any
	require.NoError(t, common.Unmarshal(ztapiOfficialComparisonJSON20261107, &document))
	for _, model := range document["models"].([]any) {
		ref := model.(map[string]any)["reference"].(map[string]any)
		delete(ref, "original_currency")
		delete(ref, "original_units_per_usd")
	}
	raw, err := common.Marshal(document)
	require.NoError(t, err)
	require.Equal(t, "ce434bd8f58c5ba763ed18d317c1a9b2aaaea8e98b855b196deaa2beb4e8d56c", fmt.Sprintf("%x", sha256.Sum256(raw)), "all prior prices, conditions, quotations and reference fields must stay unchanged")
}

func TestZTAPIPrices20261107NativeReferenceInvalidPairsStayCompatible(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
		valid  bool
	}{
		{"legacy", map[string]any{}, false},
		{"CNY", map[string]any{"original_currency": "CNY", "original_units_per_usd": "6.63"}, true},
		{"USD", map[string]any{"original_currency": "USD", "original_units_per_usd": "1"}, true},
		{"positive historical CNY rate", map[string]any{"original_currency": "CNY", "original_units_per_usd": "7.3"}, true},
		{"missing rate", map[string]any{"original_currency": "CNY"}, false},
		{"missing currency", map[string]any{"original_units_per_usd": "6.63"}, false},
		{"unsupported currency", map[string]any{"original_currency": "EUR", "original_units_per_usd": "1"}, false},
		{"lowercase currency", map[string]any{"original_currency": "cny", "original_units_per_usd": "6.63"}, false},
		{"zero", map[string]any{"original_currency": "CNY", "original_units_per_usd": "0"}, false},
		{"negative", map[string]any{"original_currency": "CNY", "original_units_per_usd": "-6.63"}, false},
		{"empty", map[string]any{"original_currency": "CNY", "original_units_per_usd": ""}, false},
		{"malformed", map[string]any{"original_currency": "CNY", "original_units_per_usd": "NaN"}, false},
		{"exponent", map[string]any{"original_currency": "CNY", "original_units_per_usd": "1e3"}, false},
		{"whitespace", map[string]any{"original_currency": "CNY", "original_units_per_usd": " 6.63"}, false},
		{"USD must be identity", map[string]any{"original_currency": "USD", "original_units_per_usd": "6.63"}, false},
		{"numeric rate is not a decimal string", map[string]any{"original_currency": "CNY", "original_units_per_usd": 6.63}, false},
		{"non-string currency", map[string]any{"original_currency": true, "original_units_per_usd": "6.63"}, false},
		{"null pair", map[string]any{"original_currency": nil, "original_units_per_usd": nil}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"url": "https://example.test/frozen", "checked_at": "2026-11-07", "kind": "official_snapshot", "note": "CNY 6.63 in a note is not metadata"}
			for key, value := range tc.fields {
				input[key] = value
			}
			raw, err := common.Marshal(input)
			require.NoError(t, err)
			var ref ZTAPIPublicOfficialReference
			require.NoError(t, common.Unmarshal(raw, &ref), "bad optional metadata must not invalidate the existing reference")
			output := nativeReferenceJSON20261107(t, ref)
			for _, key := range []string{"url", "checked_at", "kind", "note"} {
				require.Equal(t, input[key], output[key])
			}
			if tc.valid {
				require.Equal(t, input["original_currency"], output["original_currency"])
				require.Equal(t, input["original_units_per_usd"], output["original_units_per_usd"])
			} else {
				require.NotContains(t, output, "original_currency")
				require.NotContains(t, output, "original_units_per_usd")
			}
		})
	}
}

func TestZTAPIPrices20261107NativeReferenceLegacyAndInvalidMetadataKeepBenchmarks(t *testing.T) {
	original := ztapiOfficialComparisonJSON20261107
	t.Cleanup(func() { ztapiOfficialComparisonJSON20261107 = original })
	for _, invalid := range []bool{false, true} {
		var document map[string]any
		require.NoError(t, common.Unmarshal(original, &document))
		for _, model := range document["models"].([]any) {
			ref := model.(map[string]any)["reference"].(map[string]any)
			delete(ref, "original_currency")
			delete(ref, "original_units_per_usd")
			if invalid {
				ref["original_currency"] = "CNY"
				ref["original_units_per_usd"] = false
			}
		}
		raw, err := common.Marshal(document)
		require.NoError(t, err)
		ztapiOfficialComparisonJSON20261107 = raw
		entries := ztapiOfficialComparisons20261107()
		require.Len(t, entries, 15)
		for source, entry := range entries {
			require.Equal(t, ztapiOfficialBenchmarks20261107[source].OfficialUSD, entry.OfficialUSD)
			require.Equal(t, ztapiOfficialBenchmarks20261107[source].TokenRules, entry.TokenRules)
			require.Equal(t, ztapiOfficialBenchmarks20261107[source].MediaRules, entry.MediaRules)
			ref := nativeReferenceJSON20261107(t, entry.Reference)
			require.NotContains(t, ref, "original_currency")
			require.NotContains(t, ref, "original_units_per_usd")
		}
	}
}

func TestZTAPIPrices20261107NativeReferencePublicProjectionDoesNotConvertPrices(t *testing.T) {
	for _, source := range []string{"deepseek-v4-pro", "claude-fable-5"} {
		entry := ztapiOfficialBenchmarks20261107[source]
		item := ZTAPIPublicCatalogItem{ModelName: entry.PublicName, SaleUSD: map[string]string{"input_tokens": "1.6198"}, OfficialUSD: map[string]string{"input_tokens": "999"}}
		if source == "claude-fable-5" {
			item.TokenPriceRules = []ZTAPIPublicTokenPriceRule{{Conditions: []string{}, SaleUSD: map[string]string{"input_tokens": "8.1309", "cache_read": "0.8131", "cache_write": "16.2618", "output_tokens": "40.6545"}}}
		}
		before := nativeReferenceJSON20261107(t, item)
		applyZTAPIOfficialComparison20261107(source, &item)
		require.NotNil(t, item.OfficialReference)
		ref := nativeReferenceJSON20261107(t, item.OfficialReference)
		require.NotEmpty(t, ref["original_currency"])
		require.NotEmpty(t, ref["original_units_per_usd"])
		require.Equal(t, before["sale_usd"], nativeReferenceJSON20261107(t, item)["sale_usd"])
		if source == "deepseek-v4-pro" {
			require.Equal(t, entry.OfficialUSD["input_tokens"], item.OfficialUSD["input_tokens"])
		} else {
			require.Equal(t, "8.1309", item.TokenPriceRules[0].SaleUSD["input_tokens"])
			require.Equal(t, "10", item.TokenPriceRules[0].OfficialUSD["input_tokens"])
			require.NotContains(t, item.TokenPriceRules[0].OfficialUSD, "cache_write")
		}
	}
}
