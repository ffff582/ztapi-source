package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func xcloudSnapshot20261128(t *testing.T) (ZTAPIModelPriceSource, ZTAPIModelPublicationSnapshot) {
	t.Helper()
	config := xcloudConfig20261128("gpt-6.2-sol", "zt-gpt-6.2-sol", ZTAPIProviderOpenAI)
	source, err := BuildZTAPIXcloudPriceSource20261128(config, "gpt-plus", 9, "")
	require.NoError(t, err)
	source.ID, source.Version = 9901, 1
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.NoError(t, applyZTAPICommercialPreview(&config, preview))
	snapshot := ZTAPIModelPublicationSnapshot{ModelConfigID: config.ID, SourceModel: config.SourceModel,
		PublicName: config.PublicNameValue(), Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		PriceSourceID: source.ID, PricePolicy: source.PricePolicy, SaleMultiplier: "1", TokenPriceRulesJSON: source.TokenPriceRulesJSON,
		AllowedChannelIDs: "[7]", InputPricePerMillion: config.InputPricePerMillion, OutputPricePerMillion: config.OutputPricePerMillion,
		CacheReadRatio: config.CacheReadRatio, CacheCreationRatio: config.CacheCreationRatio,
		CacheCreation5mRatio: config.CacheCreation5mRatio, CacheCreation1hRatio: config.CacheCreation1hRatio}
	return source, snapshot
}

func TestZTAPIXcloud20261128QuotationIdentityUsesExactDatedSKU(t *testing.T) {
	require.NoError(t, ValidateZTAPIQuotationIdentity("gpt-6.2-sol", "zt-gpt-6.2-sol", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, ZTAPIXcloudQuoteSHA25620261128))
	require.Error(t, ValidateZTAPIQuotationIdentity("gpt-6.2-sol", "zt-gpt-6.1-sol", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, ZTAPIXcloudQuoteSHA25620261128))
	require.Error(t, ValidateZTAPIQuotationIdentity("gpt-6.2-sol", "zt-gpt-6.2-sol", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, ztapiXcloudEvidence20261114))
}

func TestZTAPIXcloud20261128FrozenSnapshotRejectsCrossSupplierOrRepricing(t *testing.T) {
	source, snapshot := xcloudSnapshot20261128(t)
	require.NoError(t, ztapiValidateXcloudSnapshot20261128(&source, &snapshot))
	for _, mutate := range []func(*ZTAPIModelPublicationSnapshot){
		func(s *ZTAPIModelPublicationSnapshot) { s.AllowedChannelIDs = "[2,7]" },
		func(s *ZTAPIModelPublicationSnapshot) { s.AllowedChannelIDs = "[6]" },
		func(s *ZTAPIModelPublicationSnapshot) { s.SourceModel = "gpt-6.1-sol" },
		func(s *ZTAPIModelPublicationSnapshot) { s.InputPricePerMillion = 0.01 },
		func(s *ZTAPIModelPublicationSnapshot) { s.TokenPriceRulesJSON = "[]" },
	} {
		bad := snapshot
		mutate(&bad)
		require.Error(t, ztapiValidateXcloudSnapshot20261128(&source, &bad))
	}
	keep, err := ztapiLegacyRepricingPreservesSource(nil, &source)
	require.NoError(t, err)
	require.True(t, keep, "legacy FX/repricing must not replace a new dedicated quotation")
	require.True(t, ztapiPublicationPriceBasisPermittedTx(nil, &source))
}

func TestZTAPIXcloud20261128RouteDoesNotRenameOrFallback(t *testing.T) {
	require.NoError(t, ValidateZTAPIXcloudRoute20261128("claude-haiku-4-5-20251001", 5, "https://www.xcloudtoken.com", "claude-haiku-4-5-20251001"))
	require.Error(t, ValidateZTAPIXcloudRoute20261128("claude-haiku-4-5-20251001", 4, "https://www.xcloudtoken.com", "claude-haiku-4-5-20251001"))
	require.Error(t, ValidateZTAPIXcloudRoute20261128("gpt-6.2-sol", 7, "https://www.xcloudtoken.com", "gpt-6.1-sol"))
	require.Error(t, ValidateZTAPIXcloudRoute20261128("gpt-6.2-sol", 2, "https://ai.yunxinapi.com/hub", "gpt-6.2-sol"))
	require.Error(t, ValidateZTAPIXcloudRoute20261128("gemini-4-neon", 8, "https://www.xcloudtoken.com", "gemini-4-neon"))
}
