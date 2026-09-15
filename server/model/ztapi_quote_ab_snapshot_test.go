package model

import (
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
