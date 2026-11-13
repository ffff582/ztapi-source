package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPITokenRulesFreezeFromPriceSourceIntoPublication(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	source := validZTAPIPriceSourceForTest()
	source.TokenPriceRulesJSON = `[{"conditions":["输入长度≤272K"],"sale":{"input_tokens":"3.9","output_tokens":"19.5"}},{"conditions":["输入长度>272K"],"sale":{"input_tokens":"7.8","output_tokens":"29.25"}}]`
	frozenRules := source.TokenPriceRulesJSON
	require.NoError(t, db.Create(&source).Error)
	snapshot := ZTAPIModelPublicationSnapshot{
		ModelConfigID: source.ModelConfigID, SourceModel: source.SourceModel,
		PriceSourceID: source.ID, PricePolicy: source.PricePolicy,
	}
	require.NoError(t, db.Create(&snapshot).Error)
	require.Equal(t, frozenRules, snapshot.TokenPriceRulesJSON)

	require.NoError(t, db.Model(&source).Update("token_price_rules_json", `[{"sale":{"input_tokens":"99"}}]`).Error)
	var frozen ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&frozen, snapshot.ID).Error)
	require.Equal(t, frozenRules, frozen.TokenPriceRulesJSON)
}

func TestZTAPIPublicCatalogShowsFrozenTokenTiers(t *testing.T) {
	rules := `[{"conditions":["输入长度≤272K"],"sale":{"input_tokens":"3.9","output_tokens":"19.5"}},{"conditions":["输入长度>272K"],"sale":{"input_tokens":"7.8","output_tokens":"29.25"}}]`
	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{{
		Modality: ZTAPIModalityText, PublicName: "zt-gpt-5.6-sol", SnapshotID: 42,
		TokenPriceRulesJSON: rules,
		InputPriceDisplay:   "7.8", OutputPriceDisplay: "29.25",
		BillingDimensions: []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens},
		SaleUSD:           map[string]string{ZTAPIBillingDimensionInputTokens: "7.8", ZTAPIBillingDimensionOutputTokens: "29.25"},
	}})
	require.Len(t, items, 1)
	require.Len(t, items[0].TokenPriceRules, 2)
	require.Equal(t, []string{"输入长度≤272K"}, items[0].TokenPriceRules[0].Conditions)
	require.Equal(t, "3.9", items[0].TokenPriceRules[0].SaleUSD[ZTAPIBillingDimensionInputTokens])
	require.Empty(t, items[0].InputPricePerMillion)
	require.Empty(t, items[0].OutputPricePerMillion)
}

// The public price list empties the single rate for a tiered model, so it must
// publish the tiers themselves; without them a reader sees no price at all.
func TestZTAPIPublicPricingPublishesFrozenTokenTiers(t *testing.T) {
	rules := `[{"conditions":["输入长度≤272K"],"sale":{"input_tokens":"3.9","output_tokens":"19.5"}},{"conditions":["输入长度>272K"],"sale":{"input_tokens":"7.8","output_tokens":"29.25"}}]`
	pricing := projectZTAPIPublicPricing([]ZTAPIRuntimePublication{{
		Modality: ZTAPIModalityText, PublicName: "zt-gpt-5.6-sol", SnapshotID: 42,
		ProviderFamily: ZTAPIProviderOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
		Groups: []string{"default"}, TokenPriceRulesJSON: rules,
		InputPricePerMillion: 7.8, OutputPricePerMillion: 29.25,
		InputPriceDisplay:    "7.8", OutputPriceDisplay: "29.25",
		BillingDimensions:    []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens},
		SaleUSD:              map[string]string{ZTAPIBillingDimensionInputTokens: "7.8", ZTAPIBillingDimensionOutputTokens: "29.25"},
	}})
	require.Len(t, pricing, 1)
	require.Empty(t, pricing[0].InputPricePerMillion)
	require.Empty(t, pricing[0].OutputPricePerMillion)
	require.Len(t, pricing[0].TokenPriceRules, 2)
	require.Equal(t, []string{"输入长度>272K"}, pricing[0].TokenPriceRules[1].Conditions)
	require.Equal(t, "29.25", pricing[0].TokenPriceRules[1].SaleUSD[ZTAPIBillingDimensionOutputTokens])

	encoded, err := json.Marshal(pricing[0])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(encoded, &payload))
	tiers, ok := payload["token_price_rules"].([]any)
	require.True(t, ok, "token_price_rules must reach the public payload")
	require.Len(t, tiers, 2)
}
