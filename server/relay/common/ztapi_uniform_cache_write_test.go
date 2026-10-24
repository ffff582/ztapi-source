package common

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func uniformCacheWriteInfo() *RelayInfo {
	info := quotedUsageInfo()
	info.ZTAPIPublicationSnapshot.BillingDimensions = []string{"input_tokens", "output_tokens", "cache_read", "cache_write"}
	info.ZTAPIPublicationSnapshot.SaleUSD["cache_read"] = "0.9432913166"
	info.ZTAPIPublicationSnapshot.SaleUSD["cache_write"] = "18.86582633"
	return info
}

func TestZTAPIUniformCacheWriteNestedInclusiveReceipt(t *testing.T) {
	info := uniformCacheWriteInfo()
	usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"prompt_tokens":7603,"completion_tokens":169,"total_tokens":7772,"prompt_tokens_details":{"cache_creation_input_tokens":7601,"cached_tokens":0}}}`))
	require.NotNil(t, usage)
	require.Equal(t, "upstream_reported", usage.UsageSource)
	require.Equal(t, 7601, usage.PromptTokensDetails.CachedCreationTokens)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.False(t, result.Pending, "%+v", result)
	require.Equal(t, int64(2), usageDimension(t, result, "input_tokens").Quantity)
	require.Equal(t, int64(7601), usageDimension(t, result, "cache_write").Quantity)
}

func TestZTAPIUniformCacheWriteTotalIsChargedOnce(t *testing.T) {
	for _, total := range []int{0, 40, 50} {
		info := uniformCacheWriteInfo()
		usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 2, UsageSemantic: "openai", UsageSource: "upstream_reported",
			PromptTokensDetails:         dto.InputTokenDetails{CachedTokens: 10, CachedCreationTokens: total},
			ClaudeCacheCreation5mTokens: 25, ClaudeCacheCreation1hTokens: 15}
		result := ValidateZTAPIUsageDimensions(nil, info, usage)
		require.False(t, result.Pending, "%+v", result)
		writes := total
		if writes == 0 {
			writes = 40
		}
		require.Equal(t, int64(writes), usageDimension(t, result, "cache_write").Quantity)
		require.Equal(t, "0.00001886582633", usageDimension(t, result, "cache_write").UnitPriceUSD)
		require.Equal(t, int64(90-writes), usageDimension(t, result, "input_tokens").Quantity)
		require.Len(t, result.Dimensions, 4)
	}
}

func TestZTAPIUniformCacheWriteInvalidReceiptsRemainBlocked(t *testing.T) {
	for _, raw := range []string{
		`{"usage":{"prompt_tokens":100,"completion_tokens":1,"cache_creation_input_tokens":5}}`,
		`{"usage":{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cache_creation_input_tokens":5,"cached_creation_tokens":0}}}`,
		`{"usage":{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cache_creation_input_tokens":-1}}}`,
		`{"usage":{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cache_creation_input_tokens":"5"}}}`,
		`{"usage":{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cache_creation_input_tokens":101}}}`,
	} {
		info := uniformCacheWriteInfo()
		require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(raw)), raw)
		require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons, raw)
	}
	legacy := quotedUsageInfo()
	legacy.ZTAPIPublicationSnapshot.BillingDimensions = append(legacy.ZTAPIPublicationSnapshot.BillingDimensions, "cache_write_5m", "cache_write_1h")
	legacy.ZTAPIPublicationSnapshot.SaleUSD["cache_write_5m"] = "11.7911414566"
	legacy.ZTAPIPublicationSnapshot.SaleUSD["cache_write_1h"] = "18.8658263306"
	usage := ObserveZTAPIOpenAIUsage(nil, legacy, []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cache_creation_input_tokens":5}}}`))
	require.NotNil(t, usage)
	result := ValidateZTAPIUsageDimensions(nil, legacy, usage)
	require.True(t, result.Pending)
	require.Contains(t, result.MissingDimensions, "cache_write")
}

func TestZTAPIUniformCacheWriteUsesFinalCumulativeReceiptOnly(t *testing.T) {
	info := uniformCacheWriteInfo()
	info.IsStream = true
	require.NotNil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"prompt_tokens":50,"completion_tokens":1,"prompt_tokens_details":{"cache_creation_input_tokens":25,"cached_tokens":10}}}`)))
	final := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cache_creation_input_tokens":50,"cached_tokens":20}}}`))
	require.NotNil(t, final)
	result := ValidateZTAPIUsageDimensions(nil, info, final)
	require.False(t, result.Pending)
	require.Equal(t, int64(50), usageDimension(t, result, "cache_write").Quantity)
	require.Equal(t, int64(30), usageDimension(t, result, "input_tokens").Quantity)
}
