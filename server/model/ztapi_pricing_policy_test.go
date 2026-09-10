package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestZTAPIPricePolicyCalculatesExplicitMargins(t *testing.T) {
	tests := []struct {
		name   string
		cost   string
		policy ZTAPIPricePolicy
		want   string
	}{
		{name: "enterprise 40 percent margin", cost: "3.90", policy: ZTAPIPricePolicyEnterprise40Margin, want: "6.50"},
		{name: "pool 60 percent margin", cost: "6.50", policy: ZTAPIPricePolicyPool60Margin, want: "16.25"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CalculateZTAPISalePriceForPolicy(decimal.RequireFromString(test.cost), test.policy)
			require.NoError(t, err)
			require.Equal(t, test.want, got.StringFixed(2))
		})
	}
}

func TestZTAPIPricePolicyFailsClosed(t *testing.T) {
	_, err := CalculateZTAPISalePriceForPolicy(decimal.NewFromInt(1), ZTAPIPricePolicy("unknown"))
	require.Error(t, err)

	for _, test := range []struct {
		name     string
		resource string
		policy   ZTAPIPricePolicy
	}{
		{name: "pool cannot use enterprise policy", resource: "pool", policy: ZTAPIPricePolicyEnterprise40Margin},
		{name: "enterprise cannot use pool policy", resource: "enterprise", policy: ZTAPIPricePolicyPool60Margin},
		{name: "unknown policy", resource: "enterprise", policy: ZTAPIPricePolicy("unknown")},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, ValidateZTAPIPricePolicy(test.resource, test.policy))
		})
	}
}

func TestZTAPIPricePolicyIsRequiredOnPriceSources(t *testing.T) {
	for _, policy := range []string{"", "unknown"} {
		source := validZTAPIPriceSourceForTest()
		source.PricePolicy = policy
		require.Error(t, ValidateZTAPIModelPriceSource(&source))
	}
}

func TestZTAPIPricePolicyPreservesEnterpriseCompatibilityWrapper(t *testing.T) {
	cost := decimal.RequireFromString("3.9000000000")
	want, err := CalculateZTAPISalePriceForPolicy(cost, ZTAPIPricePolicyEnterprise40Margin)
	require.NoError(t, err)
	require.True(t, want.Equal(CalculateZTAPISalePrice(cost)))
}

func setZTAPIPriceSourceCostForTest(t *testing.T, source *ZTAPIModelPriceSource, dimension string, value decimal.Decimal) {
	t.Helper()
	raw := value.String()
	switch dimension {
	case ZTAPIBillingDimensionInputTokens:
		source.InputPerMillion = raw
	case ZTAPIBillingDimensionOutputTokens:
		source.OutputPerMillion = raw
	case ZTAPIBillingDimensionCacheRead:
		source.CacheReadPerMillion = raw
	case ZTAPIBillingDimensionCacheWrite5m:
		source.CacheWrite5mPerMillion = raw
	case ZTAPIBillingDimensionCacheWrite1h:
		source.CacheWrite1hPerMillion = raw
	default:
		t.Fatalf("unsupported pool test dimension %q", dimension)
	}
}

func validZTAPIPoolPriceSourceForTest(t *testing.T, sourceModel string) (ZTAPIModelPriceSource, ZTAPIQuotationRow) {
	t.Helper()
	entries, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	var rows []ZTAPIQuotationRow
	for _, entry := range entries {
		if entry.SourceModel == sourceModel {
			rows = entry.QuoteRows
			break
		}
	}
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, string(ZTAPIPricePolicyPool60Margin), row.PricePolicy)
	require.Len(t, row.RawPriceUSDPerMillion, 5)

	source := validZTAPIPriceSourceForTest()
	source.SourceModel = sourceModel
	source.ResourceType = "pool"
	source.PricePolicy = string(ZTAPIPricePolicyPool60Margin)
	source.SourceDocumentChecksum = ZTAPIQuotationSHA256
	source.BillingDimensions = `["input_tokens","output_tokens","cache_read","cache_write_5m","cache_write_1h"]`
	discount, err := decimal.NewFromString(row.DiscountPercent)
	require.NoError(t, err)
	discount = discount.Div(decimal.NewFromInt(100))
	for dimension, raw := range row.RawPriceUSDPerMillion {
		price, err := decimal.NewFromString(raw)
		require.NoError(t, err)
		setZTAPIPriceSourceCostForTest(t, &source, dimension, price.Mul(discount))
	}
	return source, row
}

func TestZTAPIFableOpusPoolPricing(t *testing.T) {
	tests := []struct {
		sourceModel string
		cell        string
		discount    string
		raw         map[string]string
		sale        map[string]string
	}{
		{
			sourceModel: "claude-fable-5", cell: "C8", discount: "65",
			raw: map[string]string{
				ZTAPIBillingDimensionInputTokens: "10", ZTAPIBillingDimensionCacheWrite5m: "12.5",
				ZTAPIBillingDimensionCacheWrite1h: "20", ZTAPIBillingDimensionCacheRead: "1",
				ZTAPIBillingDimensionOutputTokens: "50",
			},
			sale: map[string]string{
				ZTAPIBillingDimensionInputTokens: "16.25", ZTAPIBillingDimensionCacheWrite5m: "20.3125",
				ZTAPIBillingDimensionCacheWrite1h: "32.50", ZTAPIBillingDimensionCacheRead: "1.625",
				ZTAPIBillingDimensionOutputTokens: "81.25",
			},
		},
		{
			sourceModel: "claude-opus-5", cell: "C9", discount: "44",
			raw: map[string]string{
				ZTAPIBillingDimensionInputTokens: "5", ZTAPIBillingDimensionCacheWrite5m: "6.25",
				ZTAPIBillingDimensionCacheWrite1h: "10", ZTAPIBillingDimensionCacheRead: "0.5",
				ZTAPIBillingDimensionOutputTokens: "25",
			},
			sale: map[string]string{
				ZTAPIBillingDimensionInputTokens: "5.50", ZTAPIBillingDimensionCacheWrite5m: "6.875",
				ZTAPIBillingDimensionCacheWrite1h: "11.00", ZTAPIBillingDimensionCacheRead: "0.55",
				ZTAPIBillingDimensionOutputTokens: "27.50",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.sourceModel, func(t *testing.T) {
			source, row := validZTAPIPoolPriceSourceForTest(t, test.sourceModel)
			require.Equal(t, test.cell, row.Cell)
			require.Equal(t, "号池资源", row.Resource)
			require.Equal(t, test.discount, row.DiscountPercent)
			require.Equal(t, test.raw, row.RawPriceUSDPerMillion)

			preview, err := BuildZTAPIModelPricePreview(&source)
			require.NoError(t, err)
			require.Equal(t, string(ZTAPIPricePolicyPool60Margin), preview.PricePolicy)
			for dimension, want := range test.sale {
				got := decimal.RequireFromString(preview.SaleUSD[dimension])
				require.Truef(t, got.Equal(decimal.RequireFromString(want)), "%s: got %s want %s", dimension, got, want)
			}
		})
	}
}

func TestZTAPIFableOpusPoolPricingRejectsEveryMutatedCostDimension(t *testing.T) {
	dimensions := []string{
		ZTAPIBillingDimensionInputTokens,
		ZTAPIBillingDimensionCacheWrite5m,
		ZTAPIBillingDimensionCacheWrite1h,
		ZTAPIBillingDimensionCacheRead,
		ZTAPIBillingDimensionOutputTokens,
	}
	for _, sourceModel := range []string{"claude-fable-5", "claude-opus-5"} {
		for _, dimension := range dimensions {
			t.Run(sourceModel+"/"+dimension, func(t *testing.T) {
				source, _ := validZTAPIPoolPriceSourceForTest(t, sourceModel)
				cost := decimal.RequireFromString(ztapiPriceSourceValues(&source)[dimension])
				setZTAPIPriceSourceCostForTest(t, &source, dimension, cost.Add(decimal.RequireFromString("0.0000000001")))
				require.Error(t, ValidateZTAPIModelPriceSource(&source))
			})
		}
	}
}

func TestZTAPIFableOpusPoolPricingRejectsUnlistedPoolModel(t *testing.T) {
	source := validZTAPIPriceSourceForTest()
	source.SourceModel = "claude-sonnet-5"
	source.ResourceType = "pool"
	source.PricePolicy = string(ZTAPIPricePolicyPool60Margin)
	require.Error(t, ValidateZTAPIModelPriceSource(&source))
}

func TestZTAPIPricePolicyManifestRequiresUniqueExactRows(t *testing.T) {
	manifestForTest := func(t *testing.T) ztapiQuotationManifest {
		t.Helper()
		var manifest ztapiQuotationManifest
		require.NoError(t, common.Unmarshal(ztapiQuotationManifestJSON, &manifest))
		return manifest
	}
	entryIndex := func(t *testing.T, manifest *ztapiQuotationManifest, sourceModel string) int {
		t.Helper()
		for i := range manifest.Entries {
			if manifest.Entries[i].SourceModel == sourceModel {
				return i
			}
		}
		t.Fatalf("missing test entry %q", sourceModel)
		return -1
	}

	tests := []struct {
		name   string
		mutate func(*testing.T, *ztapiQuotationManifest)
	}{
		{name: "missing", mutate: func(t *testing.T, manifest *ztapiQuotationManifest) {
			i := entryIndex(t, manifest, "claude-opus-5")
			manifest.Entries[i].QuoteRows = nil
		}},
		{name: "duplicate fable without opus", mutate: func(t *testing.T, manifest *ztapiQuotationManifest) {
			fable := entryIndex(t, manifest, "claude-fable-5")
			opus := entryIndex(t, manifest, "claude-opus-5")
			duplicate := manifest.Entries[fable].QuoteRows[0]
			duplicate.Sheet = "重复国外模型"
			manifest.Entries[fable].QuoteRows = append(manifest.Entries[fable].QuoteRows, duplicate)
			manifest.Entries[opus].QuoteRows[0].DiscountPercent = ""
			manifest.Entries[opus].QuoteRows[0].PricePolicy = ""
			manifest.Entries[opus].QuoteRows[0].RawPriceUSDPerMillion = nil
		}},
		{name: "swapped", mutate: func(t *testing.T, manifest *ztapiQuotationManifest) {
			fable := entryIndex(t, manifest, "claude-fable-5")
			opus := entryIndex(t, manifest, "claude-opus-5")
			manifest.Entries[fable].QuoteRows, manifest.Entries[opus].QuoteRows =
				manifest.Entries[opus].QuoteRows, manifest.Entries[fable].QuoteRows
		}},
		{name: "mismatched identity", mutate: func(t *testing.T, manifest *ztapiQuotationManifest) {
			i := entryIndex(t, manifest, "claude-fable-5")
			manifest.Entries[i].QuoteRows[0].Label = "wrong-product-label"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := manifestForTest(t)
			test.mutate(t, &manifest)
			raw, err := common.Marshal(manifest)
			require.NoError(t, err)
			_, err = parseZTAPIQuotationManifest(raw)
			require.ErrorIs(t, err, ErrZTAPIQuotationManifestInvalid)
		})
	}
}
