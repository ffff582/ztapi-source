package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIPrices20261107CatalogTracksExactTier(t *testing.T) {
	p := ZTAPIRuntimePublication{
		SourceModel: "gpt-6.1-sol", PublicName: "zt-gpt-6.1-sol",
		ProviderFamily: ZTAPIProviderOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
		Groups: []string{"default"}, SnapshotID: 9001,
		BillingDimensions:   []string{"input_tokens", "output_tokens", "cache_read", "cache_write"},
		TokenPriceRulesJSON: `[{"conditions":["输入长度≤272K"],"sale":{"input_tokens":"0.7013","output_tokens":"3.5062","cache_read":"0.0351","cache_write":"0.8766"}},{"conditions":["输入长度>272K"],"sale":{"input_tokens":"1.4025","output_tokens":"5.2593","cache_read":"0.0702","cache_write":"1.7531"}}]`,
	}
	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{p})
	require.Len(t, items, 1)
	require.Equal(t, "2", items[0].TokenPriceRules[0].OfficialUSD["input_tokens"])
	require.Equal(t, "15", items[0].TokenPriceRules[1].OfficialUSD["output_tokens"])
	require.Equal(t, "0.7013", items[0].TokenPriceRules[0].SaleUSD["input_tokens"], "comparison must not alter billing")

	p.TokenPriceRulesJSON = `[{"conditions":["输入长度>272K"],"sale":{"input_tokens":"1","output_tokens":"2","cache_read":"0.1","cache_write":"2"}},{"conditions":["输入长度≤272K"],"sale":{"input_tokens":"1","output_tokens":"2","cache_read":"0.1","cache_write":"2"}}]`
	items = buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{p})
	require.Empty(t, items[0].TokenPriceRules[0].OfficialUSD, "a tier count match alone is not comparable evidence")
}

func TestZTAPIPrices20261107KeepCacheAndMediaComparisonsDistinct(t *testing.T) {
	fable := ZTAPIPublicCatalogItem{ModelName: "zt-claude-fable-5", TokenPriceRules: []ZTAPIPublicTokenPriceRule{
		{Conditions: []string{}, SaleUSD: map[string]string{"cache_write_5m": "16.2618", "cache_write_1h": "16.2618", "input_tokens": "8.1309"}},
	}}
	applyZTAPIOfficialComparison20261107("claude-fable-5", &fable)
	require.Equal(t, "12.5", fable.TokenPriceRules[0].OfficialUSD["cache_write_5m"])
	require.Equal(t, "20", fable.TokenPriceRules[0].OfficialUSD["cache_write_1h"])
	require.Equal(t, "16.2618", fable.TokenPriceRules[0].SaleUSD["cache_write_5m"])
	require.Equal(t, "2026-11-07", fable.OfficialReference.CheckedAt)

	image := ZTAPIPublicCatalogItem{ModelName: "zt-gp-image-2", PricingRules: []ZTAPIPublicPricingRule{
		{ID: "generation_image_input", Conditions: map[string]string{"image_operation": "generation", "token_bucket": "image_input"}, BillingUnit: "usd_per_million_tokens", SaleUSD: map[string]string{"image_input": "7.8057"}},
		{ID: "generation_image_output", Conditions: map[string]string{"image_operation": "generation", "token_bucket": "image_output"}, BillingUnit: "usd_per_million_tokens", SaleUSD: map[string]string{"image_output": "29.2713"}},
	}}
	applyZTAPIOfficialComparison20261107("gpt-image-2", &image)
	require.Equal(t, "8", image.PricingRules[0].OfficialUSD["image_input"])
	require.Equal(t, "30", image.PricingRules[1].OfficialUSD["image_output"])
	image.PricingRules[0].Conditions["token_bucket"] = "text_input"
	applyZTAPIOfficialComparison20261107("gpt-image-2", &image)
	require.Nil(t, image.OfficialReference)
	require.Empty(t, image.PricingRules[1].OfficialUSD, "incompatible buckets must not retain partial stale comparisons")
}

func TestZTAPIPrices20261107FableRuntimeRetainsComparableDimensions(t *testing.T) {
	p := ZTAPIRuntimePublication{
		SourceModel: "claude-fable-5", PublicName: "zt-claude-fable-5", ProviderFamily: ZTAPIProviderAnthropic,
		Protocol: ZTAPIProtocolOpenAICompatible, Groups: []string{"default"}, SnapshotID: 9002, PriceSourceID: 9003,
		PricePolicy:         string(ZTAPIPricePolicyReduction20261107),
		BillingDimensions:   []string{"input_tokens", "output_tokens", "cache_read", "cache_write_5m", "cache_write_1h"},
		SaleUSD:             map[string]string{"input_tokens": "8.1309", "output_tokens": "40.6545", "cache_read": "0.8131", "cache_write_5m": "16.2618", "cache_write_1h": "16.2618"},
		TokenPriceRulesJSON: `[{"conditions":[],"sale":{"input_tokens":"8.1309","output_tokens":"40.6545","cache_read":"0.8131","cache_write_5m":"16.2618","cache_write_1h":"16.2618"}}]`,
	}
	require.NoError(t, applyZTAPICacheWritePricingMode(&p, ZTAPIFableCashFloorCacheWriteMode))
	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{p})
	require.Len(t, items, 1)
	prices := items[0].TokenPriceRules[0]
	require.Equal(t, "16.2618", prices.SaleUSD["cache_write"])
	require.Equal(t, "10", prices.OfficialUSD["input_tokens"])
	require.Equal(t, "50", prices.OfficialUSD["output_tokens"])
	require.Equal(t, "1", prices.OfficialUSD["cache_read"])
	require.NotContains(t, prices.OfficialUSD, "cache_write", "unknown write TTL has no single comparable official rate")
	require.NotNil(t, items[0].OfficialReference)
}

func TestZTAPIPrices20261107ReferenceIsPublicAndQuotationIsLabeled(t *testing.T) {
	kimi := ZTAPIPublicCatalogItem{ModelName: "zt-kimi-k2.5", TokenPriceRules: []ZTAPIPublicTokenPriceRule{
		{Conditions: []string{"输入长度≤262K"}, SaleUSD: map[string]string{"input_tokens": "0.5256", "output_tokens": "2.7589", "cache_read": "0.0920"}},
	}}
	applyZTAPIOfficialComparison20261107("kimi-k2.5", &kimi)
	require.Equal(t, "supplier_quoted_official", kimi.OfficialReference.Kind)
	require.Contains(t, kimi.OfficialReference.Note, "非实时官网")
	encoded, err := common.Marshal(kimi)
	require.NoError(t, err)
	for _, forbidden := range []string{"cost_ceiling", "primary_cost", "channel_id", "source_id", "cost_usd", "quotation_fraction"} {
		require.NotContains(t, string(encoded), forbidden)
	}
	flash := ZTAPIPublicCatalogItem{ModelName: "zt-deepseek-v4-flash", OfficialUSD: map[string]string{"input_tokens": "999"}}
	applyZTAPIOfficialComparison20261107("deepseek-v4-flash", &flash)
	require.Empty(t, flash.OfficialUSD)
	require.Nil(t, flash.OfficialReference)
}

func TestZTAPIPrices20261107RuntimeRequiresPersistedParentAndRoute(t *testing.T) {
	for _, corruption := range []string{"parent", "route"} {
		t.Run(corruption, func(t *testing.T) {
			db := reductionFixture20261107(t)
			var selected ZTAPIPriceReductionSelection20261107
			for _, candidate := range ApprovedZTAPIPriceReductionSelections20261107() {
				if candidate.ModelID == 53 {
					selected = candidate
				}
			}
			_, err := ApplyZTAPIPriceReduction20261107(9, []ZTAPIPriceReductionSelection20261107{selected}, false)
			require.NoError(t, err)
			publications, err := loadZTAPIActivePublications(db)
			require.NoError(t, err)
			var active ZTAPIRuntimePublication
			for _, publication := range publications {
				if publication.ModelConfigID == selected.ModelID {
					active = publication
				}
			}
			require.Equal(t, selected.ModelID, active.ModelConfigID)
			if corruption == "parent" {
				require.NoError(t, db.Exec("UPDATE ztapi_model_price_sources SET input_per_million = ? WHERE id = ?", "0.01", selected.SourceID).Error)
			} else {
				require.NoError(t, db.Exec("UPDATE ztapi_model_publication_snapshots SET allowed_channel_ids = ? WHERE id = ?", "[1]", active.SnapshotID).Error)
			}
			publications, err = loadZTAPIActivePublications(db)
			require.NoError(t, err)
			for _, publication := range publications {
				require.NotEqual(t, selected.ModelID, publication.ModelConfigID, "new policies must not bypass persisted evidence and route validation")
			}
		})
	}
}
