package controller

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCashMargin20261030ReaderTierPrecisionAndExclusiveCache(t *testing.T) {
	db, _ := setupZTAPIModelEvidenceControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelPublicationSnapshot{}, &model.ZTAPIHealthState{}))
	quote, err := model.ZTAPIQuotationABEntries()
	require.NoError(t, err)
	var selected []model.ZTAPICashMarginSelection20261030
	for _, tc := range []struct {
		name, source, public, provider string
		channel                        int
	}{
		{"Gemini 3 Flash Preview", "gemini-3-flash-preview", "zt-gemini-3-flash-preview", model.ZTAPIProviderGoogle, 1},
		{"Gemini 3.1 Pro Preview", "gemini-3.1-pro-preview", "zt-gemini-3.1-pro-preview", model.ZTAPIProviderGoogle, 2},
		{"Kimi K2.7 Code", "kimi-k2.7-code", "zt-kimi-k2.7-code", model.ZTAPIProviderMoonshot, 2},
	} {
		public := tc.public
		c := model.ZTAPIModelConfig{SourceModel: tc.source, PublicName: &public, ProviderFamily: tc.provider, Protocol: model.ZTAPIProtocolOpenAICompatible,
			Family: model.ZTAPIModelFamilyOpenAI, EnabledGroups: `["default"]`, Version: 1, Published: true,
			CacheCreationRatio: 1, CacheCreation5mRatio: 1, CacheCreation1hRatio: 1, ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1}
		require.NoError(t, db.Create(&c).Error)
		fx := model.ZTAPIABFXParams{Mode: model.ZTAPIFXModePlatformV1, PlatformRate: decimal.RequireFromString("6.63"), UpstreamUSDRate: decimal.RequireFromString("6.7208")}
		anchor, err := model.BuildZTAPIABTextPriceSourceWithFX(quote, tc.name, c.ID, 7, 1_789_000_000, fx)
		require.NoError(t, err)
		anchor.Version = 1
		require.NoError(t, db.Create(&anchor).Error)
		base, err := model.BuildZTAPISupplierPriceSource20261019WithDB(db, &anchor, 7, 1_792_414_023, fx)
		require.NoError(t, err)
		base.Version = 2
		require.NoError(t, db.Create(&base).Error)
		preview, err := model.BuildZTAPIModelPricePreview(&base)
		require.NoError(t, err)
		c.InputPricePerMillion = decimal.RequireFromString(preview.InputSaleUSDPerMillion).InexactFloat64()
		c.OutputPricePerMillion = decimal.RequireFromString(preview.OutputSaleUSDPerMillion).InexactFloat64()
		c.CacheReadRatio = decimal.RequireFromString(preview.CostUSD[model.ZTAPIBillingDimensionCacheRead]).Div(decimal.RequireFromString(preview.InputCostUSDPerMillion)).Round(8).InexactFloat64()
		require.NoError(t, db.Save(&c).Error)
		p := model.ZTAPIModelPublicationSnapshot{ModelConfigID: c.ID, ModelVersion: c.Version, SourceModel: c.SourceModel, PublicName: public,
			Protocol: c.Protocol, ProviderFamily: c.ProviderFamily, EnabledGroups: c.EnabledGroups, AllowedChannelIDs: fmt.Sprintf("[%d]", tc.channel),
			PriceSourceID: base.ID, VerificationIDs: `[31]`, InputPricePerMillion: c.InputPricePerMillion, OutputPricePerMillion: c.OutputPricePerMillion, CacheReadRatio: c.CacheReadRatio,
			CacheCreationRatio: c.CacheCreationRatio, CacheCreation5mRatio: c.CacheCreation5mRatio, CacheCreation1hRatio: c.CacheCreation1hRatio,
			ImageRatio: c.ImageRatio, AudioRatio: c.AudioRatio, AudioCompletionRatio: c.AudioCompletionRatio}
		require.NoError(t, db.Create(&p).Error)
		require.NoError(t, db.Model(&c).Update("publication_snapshot_id", p.ID).Error)
		selected = append(selected, model.ZTAPICashMarginSelection20261030{ModelID: c.ID, Version: c.Version, SnapshotID: p.ID})
	}
	_, err = model.ApplyZTAPICashMargin20261030(9, selected, false)
	require.NoError(t, err)
	publications, err := model.ListZTAPIActiveRuntimePublications()
	require.NoError(t, err)
	require.Len(t, publications, 3)
	for _, p := range publications {
		t.Run(p.PublicName, func(t *testing.T) {
			frozen := &relaycommon.ZTAPIPublicationSnapshot{PricePolicy: p.PricePolicy, PublicName: p.PublicName, SourceModel: p.SourceModel,
				PriceSourceID: p.PriceSourceID, PriceSourceVersion: p.PriceSourceVersion, BillingDimensions: p.BillingDimensions, SaleUSD: p.SaleUSD, TokenPriceRulesJSON: p.TokenPriceRulesJSON,
				InputPricePerMillion: p.InputPricePerMillion, OutputPricePerMillion: p.OutputPricePerMillion}
			for _, dim := range []string{model.ZTAPIBillingDimensionInputTokens, model.ZTAPIBillingDimensionOutputTokens, model.ZTAPIBillingDimensionCacheRead} {
				require.NotEmpty(t, p.SaleUSD[dim])
				require.True(t, decimal.RequireFromString(p.SaleUSD[dim]).Equal(decimal.RequireFromString(p.SaleUSD[dim]).Round(8)))
			}
			for _, prompt := range []int{100, 200001} {
				if p.SourceModel == "kimi-k2.7-code" && prompt > 262000 {
					continue
				}
				info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: frozen.Clone(), ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: p.SourceModel}}
				usage := relaycommon.ObserveZTAPIOpenAIUsage(nil, info, []byte(fmt.Sprintf(`{"usage":{"input_tokens":%d,"output_tokens":10,"input_tokens_details":{"cached_tokens":20}}}`, prompt)))
				require.NotNil(t, usage)
				tier, _, err := relaycommon.SelectZTAPIFrozenTokenTier(info.ZTAPIPublicationSnapshot, usage)
				require.NoError(t, err)
				info.ZTAPIPublicationSnapshot = tier
				check := relaycommon.ValidateZTAPIUsageDimensions(nil, info, usage)
				require.False(t, check.Pending, "%+v", check)
				var input, cached int64
				for _, d := range check.Dimensions {
					if d.Dimension == "input_tokens" {
						input = d.Quantity
					}
					if d.Dimension == "cache_read" {
						cached = d.Quantity
					}
				}
				require.Equal(t, int64(prompt), input+cached)
				require.Equal(t, int64(20), cached)
			}
			if p.SourceModel == "gemini-3-flash-preview" {
				_, tier, err := relaycommon.SelectZTAPIFrozenTokenTier(frozen, &dto.Usage{PromptTokens: 100, CompletionTokens: 10, PromptTokensDetails: dto.InputTokenDetails{AudioTokens: 100}})
				require.NoError(t, err)
				require.Equal(t, "tier_1", tier, "Unicode audio conditions must survive round trips")
			}
		})
	}
}
