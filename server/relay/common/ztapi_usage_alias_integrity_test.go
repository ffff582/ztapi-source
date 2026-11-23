package common

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

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
