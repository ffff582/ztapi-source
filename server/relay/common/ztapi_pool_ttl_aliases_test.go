package common

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func poolTTLUsageInfo() *RelayInfo {
	info := cacheUsageInfo()
	info.ChannelMeta.UpstreamModelName = "gpt-6-luna-hc"
	info.ZTAPIPublicationSnapshot.SourceModel = "gpt-6-luna"
	return info
}

func TestPoolTTLCacheAliasesAcceptObservedZeroUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			info := poolTTLUsageInfo()
			info.IsStream = stream
			raw := []byte(`{"usage":{"completion_tokens":5,"prompt_tokens":20,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0},"cache_write_5m_tokens":0,"cache_write_1h_tokens":0}}`)
			usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
			require.NotNil(t, usage)
			require.Equal(t, "upstream_reported", usage.UsageSource)
			require.Equal(t, 20, usage.PromptTokens)
			require.Equal(t, 5, usage.CompletionTokens)
			require.False(t, ValidateZTAPIUsageDimensions(nil, info, usage).Pending)
		})
	}
}

func TestPoolTTLCacheAliasesConserveAndDoNotDoubleCount(t *testing.T) {
	info := poolTTLUsageInfo()
	raw := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":35},"cache_write_5m_tokens":10,"cache_write_1h_tokens":20,"claude_cache_creation_5_m_tokens":10,"cache_creation":{"ephemeral_1h_input_tokens":20}}}`)
	usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
	require.NotNil(t, usage)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.False(t, result.Pending)
	require.Equal(t, int64(45), usageDimension(t, result, "input_tokens").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_read").Quantity)
	require.Equal(t, int64(5), usageDimension(t, result, "cache_write").Quantity)
	require.Equal(t, int64(10), usageDimension(t, result, "cache_write_5m").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_write_1h").Quantity)
}

func TestPoolTTLCacheAliasesInvalidEvidenceRemainsPending(t *testing.T) {
	for _, tc := range []struct{ name, fields, reason string }{
		{"conflict_zero", `"cache_write_5m_tokens":0,"cache_write_1h_tokens":0,"claude_cache_creation_5_m_tokens":10`, "usage_alias_conflict"},
		{"missing_pair", `"cache_write_5m_tokens":10`, "usage_incomplete"},
		{"negative", `"cache_write_5m_tokens":-1,"cache_write_1h_tokens":0`, "usage_invalid_quantity"},
		{"overflow", `"cache_write_5m_tokens":9223372036854775808,"cache_write_1h_tokens":0`, "usage_invalid_quantity"},
		{"string", `"cache_write_5m_tokens":"0","cache_write_1h_tokens":0`, "usage_invalid_type"},
		{"boolean", `"cache_write_5m_tokens":false,"cache_write_1h_tokens":0`, "usage_invalid_type"},
		{"fraction", `"cache_write_5m_tokens":0.5,"cache_write_1h_tokens":0`, "usage_invalid_type"},
		{"duplicate", `"cache_write_5m_tokens":0,"cache_write_5m_tokens":10,"cache_write_1h_tokens":0`, "usage_invalid_json"},
		{"unrecognized_ttl", `"cache_write_2h_tokens":0`, "usage_incomplete"},
		{"wrong_path", `"prompt_tokens_details":{"cache_write_5m_tokens":0,"cache_write_1h_tokens":0}`, "usage_incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := poolTTLUsageInfo()
			require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":2,`+tc.fields+`}}`)))
			require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, tc.reason)
			require.True(t, ValidateZTAPIUsageDimensions(nil, info, nil).Pending)
		})
	}
}

func TestPoolTTLCacheAliasesRequireTheirOwnPositiveQuote(t *testing.T) {
	info := poolTTLUsageInfo()
	delete(info.ZTAPIPublicationSnapshot.SaleUSD, "cache_write_5m")
	usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":2,"cache_write_5m_tokens":10,"cache_write_1h_tokens":0}}`))
	require.NotNil(t, usage)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.True(t, result.Pending)
	require.Contains(t, result.MissingDimensions, "cache_write_5m")
}

func TestPoolTTLCacheAliasStreamErrorsCannotBeCleared(t *testing.T) {
	for _, invalid := range []string{
		`{"usage":{"prompt_tokens":100,"completion_tokens":2,"cache_write_5m_tokens":0,"cache_write_1h_tokens":0,"claude_cache_creation_5_m_tokens":10}}`,
		`{"usage":{"prompt_tokens":100,"completion_tokens":2,"cache_write_5m_tokens":10}}`,
	} {
		info := poolTTLUsageInfo()
		info.IsStream = true
		require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(invalid)))
		valid := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":3,"cache_write_5m_tokens":10,"cache_write_1h_tokens":20}}`))
		require.NotNil(t, valid)
		require.True(t, ValidateZTAPIUsageDimensions(nil, info, valid).Pending)
	}
	info := poolTTLUsageInfo()
	info.IsStream = true
	require.NotNil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":2,"cache_write_5m_tokens":10,"cache_write_1h_tokens":20}}`)))
	require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":3,"cache_write_5m_tokens":9,"cache_write_1h_tokens":21}}`)))
	require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_snapshot_conflict")
}
