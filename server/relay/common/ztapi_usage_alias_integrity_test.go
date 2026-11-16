package common

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestResponsesReceiptKeepsUsefulDiagnosticsWithoutTrustingRejectedUsage(t *testing.T) {
	const counts = `"input_tokens":102161,"output_tokens":93,"total_tokens":102254,"input_tokens_details":{"cached_tokens":98688,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}`
	info := uniformCacheWriteInfo()
	usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{`+counts+`}}`))
	require.NotNil(t, usage)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.False(t, result.Pending)
	require.Equal(t, int64(3473), usageDimension(t, result, "input_tokens").Quantity)
	require.Equal(t, int64(98688), usageDimension(t, result, "cache_read").Quantity)
	require.Equal(t, int64(93), usageDimension(t, result, "output_tokens").Quantity)
	require.Len(t, result.Dimensions, 3)

	for _, tt := range []struct{ fields, reason string }{
		{counts + `,"cache_private_provider_field":"private provider data"`, "usage_unsupported_cache_field"},
		{counts + `,"cache_write_5m_tokens":0`, "usage_missing_cache_split"},
		{`"input_tokens":102161`, "usage_missing_token_counts"},
	} {
		info := uniformCacheWriteInfo()
		info.RelayFormat = types.RelayFormatOpenAIResponses
		require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{`+tt.fields+`}}`)))
		evidence := info.ZTAPIMetering.Sanitized()
		require.Equal(t, []string{tt.reason}, evidence.UsageIntegrityReasons)
		require.Equal(t, tt.reason, evidence.UnverifiedUsage.Reason)
		require.NotContains(t, string(evidence.UnverifiedUsage.RawUsage), "private")
		require.True(t, ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{}).Pending)
	}
}

func TestUsageIntegrityDimensionAliasesConflict(t *testing.T) {
	for _, usage := range []dto.Usage{
		{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 20}, InputTokensDetails: &dto.InputTokenDetails{CachedTokens: 80}},
		{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 20}, PromptCacheHitTokens: 80},
		{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedCreationTokens: 20}, InputTokensDetails: &dto.InputTokenDetails{CachedCreationTokens: 80}},
	} {
		info := quotedUsageInfo()
		info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "cache_read", "cache_write")
		info.ZTAPIPublicationSnapshot.SaleUSD["cache_read"] = "0"
		info.ZTAPIPublicationSnapshot.SaleUSD["cache_write"] = "0"
		result := ValidateZTAPIUsageDimensions(nil, info, &usage)
		require.True(t, result.Pending, "conflicting aliases must not be resolved by taking their maximum")
		require.NotEmpty(t, result.InvalidDimensions)
	}
}

func TestUsageIntegrityZeroRequiresCurrentReportedReceipt(t *testing.T) {
	info := quotedUsageInfo()
	info.ZTAPIMetering = &ZTAPIMeteringEvidence{Version: 1, UsageIntegrityComplete: true}
	for _, usage := range []dto.Usage{
		{},
		{UsageSource: "upstream_reported", UsageSemantic: "anthropic"},
	} {
		require.True(t, ValidateZTAPIUsageDimensions(nil, info, &usage).Pending, "a previous OpenAI receipt must not authorize an unverified/native zero")
	}
}
