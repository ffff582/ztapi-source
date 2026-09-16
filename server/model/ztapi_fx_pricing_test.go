package model

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func platformFXParamsForTest(platform, upstream string) ZTAPIABFXParams {
	params := ZTAPIABFXParams{Mode: ZTAPIFXModePlatformV1, PlatformRate: decimal.RequireFromString(platform)}
	if upstream != "" {
		params.UpstreamUSDRate = decimal.RequireFromString(upstream)
	}
	return params
}

func firstTierSaleForTest(t *testing.T, source ZTAPIModelPriceSource) map[string]string {
	t.Helper()
	var rules []ztapiABFrozenSaleRule
	require.NoError(t, json.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	require.NotEmpty(t, rules)
	return rules[0].Sale
}

func TestZTAPIABTextPriceBillsUSDQuotesThroughUpstreamAndPlatformRates(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	fx := platformFXParamsForTest("6.63", "6.7628")
	source, err := BuildZTAPIABTextPriceSourceWithFX(quote, "GPT 5.6 Sol", 11, 7, 1_789_000_000, fx)
	require.NoError(t, err)
	require.Equal(t, "CNY", source.Currency)
	require.Equal(t, ZTAPIFXModePlatformV1, source.FXMode)
	require.Equal(t, "6.6300000000", source.PlatformCNYPerUnit)
	require.Equal(t, "6.6300000000", source.CNYPerUSD)
	require.Equal(t, "6.7628000000", source.UpstreamCNYPerUSD)

	// The short-context tier is quoted at $3.12 per million input tokens.
	wantCost := decimal.RequireFromString("3.12").Mul(decimal.RequireFromString("6.7628")).Round(10)
	wantSale := wantCost.Div(decimal.RequireFromString("6.63")).Round(10).
		Div(decimal.RequireFromString("0.8")).Round(10)
	require.Equal(t, wantSale.StringFixed(10), firstTierSaleForTest(t, source)[ZTAPIBillingDimensionInputTokens])

	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	longCost := decimal.RequireFromString("6.24").Mul(decimal.RequireFromString("6.7628")).Round(10)
	require.Equal(t, longCost.StringFixed(10), source.InputPerMillion)
	require.Equal(t, longCost.Div(decimal.RequireFromString("6.63")).Round(10).StringFixed(10), preview.InputCostUSDPerMillion)
	require.NoError(t, ztapiPreviewKeepsMinimumMargin(preview))
	require.NoError(t, validateZTAPIABPriceSource(&source))
}

func TestZTAPIABTextPriceConvertsCNYQuotesAtThePlatformRateWithoutBuffer(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSourceWithFX(quote, "GLM 5.2", 11, 7, 1_789_000_000,
		platformFXParamsForTest("6.63", "6.7628"))
	require.NoError(t, err)
	require.Equal(t, "CNY", source.Currency)
	require.Equal(t, "0.0000000000", source.UpstreamCNYPerUSD)
	// GLM 5.2 costs ¥5.76 per million input tokens at the B-grade pool share.
	wantSale := decimal.RequireFromString("5.76").Div(decimal.RequireFromString("6.63")).Round(10).
		Div(decimal.RequireFromString("0.7")).Round(10)
	require.Equal(t, wantSale.StringFixed(10), firstTierSaleForTest(t, source)[ZTAPIBillingDimensionInputTokens])
	require.NoError(t, validateZTAPIABPriceSource(&source))
}

func TestZTAPIABTextPriceRefusesUSDQuotesUntilTheUpstreamRateIsSet(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	_, err = BuildZTAPIABTextPriceSourceWithFX(quote, "GPT 5.6 Sol", 11, 7, 1_789_000_000,
		platformFXParamsForTest("6.63", ""))
	require.ErrorIs(t, err, ErrZTAPIUpstreamUSDRateUnset)
	_, err = BuildZTAPIABTextPriceSourceWithFX(quote, "GLM 5.2", 11, 7, 1_789_000_000,
		platformFXParamsForTest("6.63", ""))
	require.NoError(t, err)
}

func TestZTAPIABSourceValidatesAgainstItsOwnFXSnapshot(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	legacy, err := BuildZTAPIABTextPriceSource(quote, "GLM 5.2", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.Equal(t, "7.2000000000", legacy.CNYPerUSD)
	require.Empty(t, legacy.FXMode)
	// A price published before the FX policy keeps validating after the change.
	require.NoError(t, validateZTAPIABPriceSource(&legacy))

	platform, err := BuildZTAPIABTextPriceSourceWithFX(quote, "GLM 5.2", 11, 7, 1_789_000_000,
		platformFXParamsForTest("6.63", ""))
	require.NoError(t, err)
	require.NoError(t, validateZTAPIABPriceSource(&platform))
	tampered := platform
	tampered.PlatformCNYPerUnit = "6.9000000000"
	require.Error(t, validateZTAPIABPriceSource(&tampered))
}

func TestZTAPIFXPolicyValidatesRatesAndSource(t *testing.T) {
	policy, err := buildZTAPIFXPolicy(ZTAPIFXPolicyInput{
		MarketCNYPerUSDT: "6.66", StopLossCNY: "0.03", UpstreamCNYPerUSD: "6.7628",
		MarketSource: ZTAPIFXMarketSourceOKXAlipay, Reason: "test", OperatorID: 1,
	})
	require.NoError(t, err)
	require.Equal(t, "6.6300000000", policy.PlatformCNYPerUSDT)
	require.Equal(t, ZTAPIFXUpstreamSourcePBOCMid, policy.UpstreamSource)
	params, err := policy.ABFXParams()
	require.NoError(t, err)
	require.Equal(t, ZTAPIFXModePlatformV1, params.Mode)

	for _, broken := range []ZTAPIFXPolicyInput{
		{MarketCNYPerUSDT: "12", StopLossCNY: "0.03", MarketSource: ZTAPIFXMarketSourceManual, Reason: "r", OperatorID: 1},
		{MarketCNYPerUSDT: "6.66", StopLossCNY: "2", MarketSource: ZTAPIFXMarketSourceManual, Reason: "r", OperatorID: 1},
		{MarketCNYPerUSDT: "6.66", StopLossCNY: "0.03", UpstreamCNYPerUSD: "1", MarketSource: ZTAPIFXMarketSourceManual, Reason: "r", OperatorID: 1},
		{MarketCNYPerUSDT: "6.66", StopLossCNY: "0.03", MarketSource: "bank", Reason: "r", OperatorID: 1},
		{MarketCNYPerUSDT: "6.66", StopLossCNY: "0.03", MarketSource: ZTAPIFXMarketSourceManual, OperatorID: 1},
	} {
		_, err := buildZTAPIFXPolicy(broken)
		require.Error(t, err)
	}
}

func seedZTAPIFXPolicyForTest(t *testing.T, db *gorm.DB, upstream string) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&ZTAPIFXPolicy{}))
	policy, err := buildZTAPIFXPolicy(ZTAPIFXPolicyInput{
		MarketCNYPerUSDT: "6.66", StopLossCNY: "0.03", UpstreamCNYPerUSD: upstream,
		MarketSource: ZTAPIFXMarketSourceOKXAlipay, Reason: "test policy", OperatorID: 7,
	})
	require.NoError(t, err)
	policy.CreatedAt = 1_789_000_000
	require.NoError(t, db.Create(&policy).Error)
}

func TestApplyZTAPIFXRepricingMovesQuotedModelsOntoThePlatformRate(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	seedZTAPIFXPolicyForTest(t, db, "6.7628")
	config := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	result, err := ApplyZTAPIFXRepricing(7)
	require.NoError(t, err)
	require.Equal(t, 1, result.Republished)

	var committed ZTAPIModelConfig
	require.NoError(t, db.First(&committed, config.ID).Error)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, committed.PublicationSnapshotID).Error)
	var source ZTAPIModelPriceSource
	require.NoError(t, db.First(&source, snapshot.PriceSourceID).Error)
	require.Equal(t, ZTAPIFXModePlatformV1, source.FXMode)
	// SQLite stores decimals without trailing zeros, so compare by value.
	require.True(t, decimal.RequireFromString(source.PlatformCNYPerUnit).
		Equal(decimal.RequireFromString("6.63")), "platform rate was %s", source.PlatformCNYPerUnit)
	require.NoError(t, validateZTAPIABPriceSource(&source))

	second, err := ApplyZTAPIFXRepricing(7)
	require.NoError(t, err)
	require.Equal(t, 1, second.Unchanged)
	require.Zero(t, second.Republished)
}

func TestApplyZTAPIFXRepricingSkipsUSDQuotesWithoutAnUpstreamRate(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	seedZTAPIFXPolicyForTest(t, db, "")
	seedZTAPIPublicCatalogRecord(t, db, "gpt-5.6-sol", "zt-gpt-5.6-sol", ZTAPIProviderOpenAI,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	result, err := ApplyZTAPIFXRepricing(7)
	require.NoError(t, err)
	require.Equal(t, []string{"zt-gpt-5.6-sol"}, result.Skipped)
	require.Zero(t, result.Republished)
}

func TestGPT54NanoQuotesTheOriginalVendorOfficialPrice(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	found := false
	for _, entry := range quote.Entries {
		if entry.ModelName != "GPT 5.4 Nano" || entry.Grade != "A" {
			continue
		}
		found = true
		require.Equal(t, "F63", entry.OfficialPriceCell)
		require.Len(t, entry.TokenPriceRules, 1)
		require.Equal(t, "0.195", entry.TokenPriceRules[0].Sale[ZTAPIBillingDimensionInputTokens])
		require.Equal(t, "1.21875", entry.TokenPriceRules[0].Sale[ZTAPIBillingDimensionOutputTokens])
	}
	require.True(t, found, "GPT 5.4 Nano must stay quotable")
}
