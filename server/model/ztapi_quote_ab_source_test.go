package model

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestZTAPIABTextPriceSourceUsesEnterpriseLongTierAndFrozenCells(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSource(quote, "GPT 5.6 Sol", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", source.SourceModel)
	require.Equal(t, "A", source.QuotationGrade)
	require.Equal(t, quote.WorkbookSHA256, source.SourceDocumentChecksum)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise20Margin), source.PricePolicy)
	require.Equal(t, "6.2400000000", source.InputPerMillion)
	require.Equal(t, "23.4000000000", source.OutputPerMillion)
	var rules []struct {
		Conditions []string          `json:"conditions"`
		Sale       map[string]string `json:"sale"`
	}
	require.NoError(t, json.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	require.Len(t, rules, 2)
	require.Equal(t, "3.9000000000", rules[0].Sale[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "7.8000000000", rules[1].Sale[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "29.2500000000", rules[1].Sale[ZTAPIBillingDimensionOutputTokens])
}

func TestZTAPIABPoolOfficial78UsesCurrentFirstPartySolPrice(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABPoolOfficial78TextPriceSource(quote, "GPT 5.6 Sol", 11, 7, 1_790_640_000)
	require.NoError(t, err)
	require.Equal(t, "pool", source.ResourceType)
	require.Equal(t, "B", source.QuotationGrade)
	require.Equal(t, "D77", source.QuotationCell)
	var rules []struct {
		Sale map[string]string `json:"sale"`
	}
	require.NoError(t, json.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	require.Len(t, rules, 2)
	require.Equal(t, "3.1200000000", rules[0].Sale[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "15.6000000000", rules[0].Sale[ZTAPIBillingDimensionOutputTokens])
	require.Equal(t, "6.2400000000", rules[1].Sale[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "23.4000000000", rules[1].Sale[ZTAPIBillingDimensionOutputTokens])
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "3.3000000000", preview.CostUSD[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "14.8500000000", preview.CostUSD[ZTAPIBillingDimensionOutputTokens])
	require.Equal(t, "23.4000000000", preview.SaleUSD[ZTAPIBillingDimensionOutputTokens])
	require.NoError(t, validateZTAPIABPriceSource(&source))
	source.QuotationCell = "D84"
	require.Error(t, validateZTAPIABPriceSource(&source))
}

func TestZTAPIABPoolOfficial78RejectsNonPoolAndInactiveRows(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	_, err = BuildZTAPIABPoolOfficial78TextPriceSource(quote, "GLM 5.2", 11, 7, 1_790_640_000)
	require.Error(t, err)
	_, err = BuildZTAPIABPoolOfficial78TextPriceSource(quote, "GPT 5.4 Mini", 11, 7, 1_790_640_000)
	require.Error(t, err)
}

func TestZTAPIABPoolOfficial78CoversEveryActiveQuotedTextPoolDimension(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	count := 0
	for _, row := range quote.Entries {
		if !row.Active || row.Grade != "B" || row.ResourceType != "号池" || row.Modality != "text" {
			continue
		}
		count++
		t.Run(row.ModelName, func(t *testing.T) {
			source, err := BuildZTAPIABPoolOfficial78TextPriceSource(quote, row.ModelName, 11, 7, 1_790_640_000)
			require.NoError(t, err)
			require.NoError(t, validateZTAPIABPriceSource(&source))
			var frozen []ztapiABFrozenSaleRule
			require.NoError(t, json.Unmarshal([]byte(source.TokenPriceRulesJSON), &frozen))
			require.Len(t, frozen, len(row.TokenPriceRules))
			for tier, quoted := range row.TokenPriceRules {
				for dimension := range quoted.Cost {
					want, err := ztapiOfficial78Sale(source.SourceModel, tier, dimension)
					require.NoError(t, err)
					require.Equal(t, want.StringFixed(10), frozen[tier].Sale[dimension])
				}
			}
		})
	}
	require.Equal(t, 14, count)
}

func TestZTAPIABPoolOfficial78CurrentFXAndRechargeBonusRemainProfitable(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	platform := decimal.RequireFromString("6.63")
	upstream := decimal.RequireFromString("6.7411")
	fx := ZTAPIABFXParams{Mode: ZTAPIFXModePlatformV1, PlatformRate: platform, UpstreamUSDRate: upstream}
	bonus := decimal.RequireFromString("1.05")
	for _, row := range quote.Entries {
		if !row.Active || row.Grade != "B" || row.ResourceType != "号池" || row.Modality != "text" {
			continue
		}
		t.Run(row.ModelName, func(t *testing.T) {
			source, err := BuildZTAPIABPoolOfficial78TextPriceSourceWithFX(quote, row.ModelName, 11, 7, 1_790_640_000, fx)
			require.NoError(t, err)
			var frozen []ztapiABFrozenSaleRule
			require.NoError(t, json.Unmarshal([]byte(source.TokenPriceRulesJSON), &frozen))
			for tier, quoted := range row.TokenPriceRules {
				for dimension, raw := range quoted.Cost {
					cost := decimal.RequireFromString(raw).Mul(upstream).Div(platform)
					cashSale := decimal.RequireFromString(frozen[tier].Sale[dimension]).Div(bonus)
					require.True(t, cashSale.GreaterThan(cost), "%s tier %d %s does not cover upstream cost", row.ModelName, tier, dimension)
				}
			}
			t.Logf("source=%s standard_input=%s standard_output=%s", source.SourceModel,
				frozen[0].Sale[ZTAPIBillingDimensionInputTokens], frozen[0].Sale[ZTAPIBillingDimensionOutputTokens])
		})
	}
}

func TestZTAPIABPoolOfficial78DoesNotRaisePriceOrEraseRechargeBonusMargin(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	old, err := BuildZTAPIABTextPriceSource(quote, "GPT 5.6 Sol", 11, 7, 1_790_640_000)
	require.NoError(t, err)
	old.SaleMultiplier = "0.90"
	target, err := BuildZTAPIABPoolOfficial78TextPriceSource(quote, "GPT 5.6 Sol", 11, 7, 1_790_640_000)
	require.NoError(t, err)
	preview, err := BuildZTAPIModelPricePreview(&target)
	require.NoError(t, err)
	require.NoError(t, ztapiPool78KeepsOldPriceAndBonusMargin(&old, &target, preview))
	old.SaleMultiplier = "0.70"
	require.ErrorContains(t, ztapiPool78KeepsOldPriceAndBonusMargin(&old, &target, preview), "increase")
	old.SaleMultiplier = "0.90"
	preview.SaleUSD[ZTAPIBillingDimensionOutputTokens] = preview.CostUSD[ZTAPIBillingDimensionOutputTokens]
	require.ErrorContains(t, ztapiPool78KeepsOldPriceAndBonusMargin(&old, &target, preview), "bonus")
}

func TestZTAPIABEnterprise15DiscountUsesCostAndNeverRaisesExistingPrice(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	old, err := BuildZTAPIABTextPriceSource(quote, "GLM 5.3", 11, 7, 1_790_640_000)
	require.NoError(t, err)
	target := old
	target.SaleMultiplier = "0.92"
	preview, err := BuildZTAPIModelPricePreview(&target)
	require.NoError(t, err)
	require.NoError(t, ztapiRepriceDoesNotRaise(&old, &target))
	cost := decimal.RequireFromString(preview.CostUSD[ZTAPIBillingDimensionInputTokens])
	sale := decimal.RequireFromString(preview.SaleUSD[ZTAPIBillingDimensionInputTokens])
	require.True(t, sale.Sub(cost.Mul(decimal.RequireFromString("1.15"))).Abs().LessThan(decimal.RequireFromString("0.0000000002")))
	old.SaleMultiplier = "0.90"
	require.ErrorContains(t, ztapiRepriceDoesNotRaise(&old, &target), "increase")
}

func TestZTAPIABEnterprise15CoversEmbeddingWithoutTierRules(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	old, err := BuildZTAPIABTextPriceSource(quote, "Text Embedding 3 Small", 11, 7, 1_790_640_000)
	require.NoError(t, err)
	require.Empty(t, old.TokenPriceRulesJSON)
	target := old
	target.SaleMultiplier = "0.92"
	require.NoError(t, ztapiRepriceDoesNotRaise(&old, &target))
}

func TestZTAPIABTextPriceSourceConvertsCNYBeforeFrozenCharge(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSource(quote, "GLM 5.2", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.Equal(t, "CNY", source.Currency)
	require.Equal(t, "7.2000000000", source.CNYPerUSD)
	var rules []struct {
		Sale map[string]string `json:"sale"`
	}
	require.NoError(t, json.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	require.Len(t, rules, 1)
	require.Equal(t, "1.1771428571", rules[0].Sale[ZTAPIBillingDimensionInputTokens])
}

func TestZTAPIABEmbeddingUsesFlatInputOnlyPrice(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSource(quote, "Text Embedding 3 Small", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.Empty(t, source.TokenPriceRulesJSON)
	require.Equal(t, `["input_tokens"]`, source.BillingDimensions)
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "0.0195000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "0.0000000000", preview.OutputSaleUSDPerMillion)
}

func TestZTAPIABTextPriceSourceRejectsUnmappedAndIncompleteQuotes(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	_, err = BuildZTAPIABTextPriceSource(quote, "Gemini 3.8 Flash", 11, 7, 1_789_000_000)
	require.Error(t, err)
	_, err = BuildZTAPIABTextPriceSource(quote, "GPT6 Astra", 11, 7, 1_789_000_000)
	require.Error(t, err)
	_, err = BuildZTAPIABTextPriceSource(quote, "DeepSeek V4 Pro", 11, 7, 1_789_000_000)
	require.ErrorContains(t, err, "unsupported quotation condition")
}

func TestZTAPIABSourceEvidenceRejectsChangedQuoteCellAndIdentity(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSource(quote, "GPT 5.6 Sol", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.NoError(t, validateZTAPIABPriceSource(&source))
	require.NoError(t, ValidateZTAPIQuotationIdentity(source.SourceModel, "zt-gpt-5.6-sol", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, quote.WorkbookSHA256))
	source.QuotationCell = "D999"
	require.Error(t, validateZTAPIABPriceSource(&source))
	require.Error(t, ValidateZTAPIQuotationIdentity(source.SourceModel, "zt-gpt-5.6-luna", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, quote.WorkbookSHA256))
}

func TestZTAPIABExactIdentityAndTextPriceReadinessCounts(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.NoError(t, err)
	require.Len(t, bridge.Mapped, 47)
	require.Len(t, bridge.Unmatched, 3)
	ready, blocked := 0, 0
	for _, identity := range bridge.Mapped {
		if _, err := BuildZTAPIABTextPriceSource(quote, identity.ModelName, 1, 7, 1_789_000_000); err == nil {
			ready++
		} else {
			blocked++
		}
	}
	require.Equal(t, 39, ready)
	require.Equal(t, 8, blocked)
}
