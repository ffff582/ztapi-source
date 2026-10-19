package common

import (
	"fmt"
	"math"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func cacheUsageInfo() *RelayInfo {
	info := quotedUsageInfo()
	info.ChannelMeta = &ChannelMeta{UpstreamModelName: "kimi-k3"}
	for _, dimension := range []string{"cache_read", "cache_write", "cache_write_5m", "cache_write_1h"} {
		info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, dimension)
		info.ZTAPIPublicationSnapshot.SaleUSD[dimension] = "1"
	}
	return info
}

func TestOpenAICacheUsageNormalizesDocumentedWriteFields(t *testing.T) {
	for _, fields := range []string{
		`"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}`,
		`"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}`,
		`"prompt_tokens_details":{"cached_tokens":20,"cached_creation_tokens":30}`,
		`"cached_tokens":20,"cached_creation_tokens":30`,
		`"prompt_cache_hit_tokens":20,"prompt_tokens_details":{"cache_write_tokens":30}`,
		`"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30,"cached_creation_tokens":30},"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}`,
	} {
		t.Run(fields, func(t *testing.T) {
			info := cacheUsageInfo()
			usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"input_tokens":100,"output_tokens":2,`+fields+`}}`))
			require.NotNil(t, usage)
			require.Equal(t, 20, usage.PromptTokensDetails.CachedTokens)
			require.Equal(t, 30, usage.PromptTokensDetails.CachedCreationTokens)
			result := ValidateZTAPIUsageDimensions(nil, info, usage)
			require.False(t, result.Pending, "%+v", result)
			require.Equal(t, int64(50), usageDimension(t, result, "input_tokens").Quantity)
			require.Equal(t, int64(20), usageDimension(t, result, "cache_read").Quantity)
			require.Equal(t, int64(30), usageDimension(t, result, "cache_write").Quantity)
			require.Len(t, result.Dimensions, 4, "aliases must not create additional charges")
		})
	}
}

func TestOpenAICacheUsageTTLSplitIsNotAnExtraCharge(t *testing.T) {
	info := cacheUsageInfo()
	usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"response":{"usage":{"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}}`))
	require.NotNil(t, usage)
	require.Equal(t, 10, usage.ClaudeCacheCreation5mTokens)
	require.Equal(t, 20, usage.ClaudeCacheCreation1hTokens)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.False(t, result.Pending, "%+v", result)
	require.Equal(t, int64(50), usageDimension(t, result, "input_tokens").Quantity)
	require.Equal(t, int64(10), usageDimension(t, result, "cache_write_5m").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_write_1h").Quantity)
	for _, dimension := range result.Dimensions {
		require.NotEqual(t, "cache_write", dimension.Dimension)
	}
}

func TestOpenAICacheUsageInvalidEvidenceIsSticky(t *testing.T) {
	for _, tc := range []struct{ name, fields, reason string }{
		{"zero_read_alias", `"prompt_tokens_details":{"cached_tokens":0},"cached_tokens":20`, "usage_alias_conflict"},
		{"zero_write_alias", `"prompt_tokens_details":{"cached_creation_tokens":0,"cache_write_tokens":30}`, "usage_alias_conflict"},
		{"write_alias", `"prompt_tokens_details":{"cache_write_tokens":30},"input_tokens_details":{"cache_write_tokens":31}`, "usage_alias_conflict"},
		{"zero_aggregate", `"prompt_tokens_details":{"cache_write_tokens":0},"claude_cache_creation_5_m_tokens":10,"claude_cache_creation_1_h_tokens":0`, "usage_alias_conflict"},
		{"aggregate_below_ttls", `"prompt_tokens_details":{"cache_write_tokens":9},"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":0}`, "usage_alias_conflict"},
		{"ttl_alias", `"claude_cache_creation_5_m_tokens":0,"claude_cache_creation_1_h_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":0}`, "usage_alias_conflict"},
		{"negative_write", `"prompt_tokens_details":{"cache_write_tokens":-1}`, "usage_invalid_quantity"},
		{"write_overflow", `"prompt_tokens_details":{"cache_write_tokens":9223372036854775808}`, "usage_invalid_quantity"},
		{"write_limit", fmt.Sprintf(`"prompt_tokens_details":{"cache_write_tokens":%d}`, int64(math.MaxInt64/8)+1), "usage_invalid_quantity"},
		{"write_string", `"prompt_tokens_details":{"cache_write_tokens":"30"}`, "usage_invalid_type"},
		{"write_null", `"prompt_tokens_details":{"cache_write_tokens":null}`, "usage_invalid_type"},
		{"write_fraction", `"prompt_tokens_details":{"cache_write_tokens":0.5}`, "usage_invalid_type"},
		{"cache_exceeds_input", `"prompt_tokens_details":{"cached_tokens":80,"cache_write_tokens":30}`, "usage_invalid_quantity"},
		{"missing_ttl", `"prompt_tokens_details":{"cache_write_tokens":10},"cache_creation":{"ephemeral_5m_input_tokens":10}`, "usage_incomplete"},
		{"unknown_ttl", `"prompt_tokens_details":{"cache_write_tokens":10},"cache_creation":{"ephemeral_2h_input_tokens":10}`, "usage_incomplete"},
		{"unknown_cache_field", `"prompt_tokens_details":{"cache_write_tokens_5m":10}`, "usage_incomplete"},
		{"null_split", `"cache_creation":null`, "usage_invalid_type"},
		{"native_anthropic_usage", `"cache_read_input_tokens":20,"cache_creation_input_tokens":30`, "usage_semantic_conflict"},
		{"duplicate_write_member", `"prompt_tokens_details":{"cache_write_tokens":0,"cache_write_tokens":30}`, "usage_invalid_json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := cacheUsageInfo()
			raw := []byte(`{"usage":{"input_tokens":100,"output_tokens":2,` + tc.fields + `}}`)
			require.Nil(t, ObserveZTAPIOpenAIUsage(c, info, raw))
			require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, tc.reason)
			valid := ObserveZTAPIOpenAIUsage(c, info, []byte(`{"usage":{"input_tokens":100,"output_tokens":3,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}}`))
			require.NotNil(t, valid)
			require.True(t, ValidateZTAPIUsageDimensions(c, info, valid).Pending)
			frozen := &RelayInfo{ZTAPIPublicationSnapshot: info.ZTAPIPublicationSnapshot}
			require.True(t, ValidateZTAPIUsageDimensions(c, frozen, valid).Pending)
		})
	}
}

func TestOpenAICacheUsageSnapshotsCannotLoseOrReclassifyBuckets(t *testing.T) {
	first := `{"usage":{"input_tokens":100,"output_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	for _, next := range []string{
		`{"usage":{"input_tokens":100,"output_tokens":3,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":29},"cache_creation":{"ephemeral_5m_input_tokens":9,"ephemeral_1h_input_tokens":20}}}`,
		`{"usage":{"input_tokens":100,"output_tokens":3,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}}`,
		`{"usage":{"input_tokens":100,"output_tokens":3,"prompt_tokens_details":{"cached_tokens":21,"cache_write_tokens":30},"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`,
	} {
		t.Run(next, func(t *testing.T) {
			info := cacheUsageInfo()
			info.IsStream = true
			require.NotNil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(first)))
			previous := info.ZTAPIUsageLastSnapshot
			require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(next)))
			require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_snapshot_conflict")
			require.Equal(t, previous, info.ZTAPIUsageLastSnapshot)
		})
	}
}

func TestOpenAICacheUsageIdenticalSnapshotsAreIdempotent(t *testing.T) {
	info := cacheUsageInfo()
	raw := []byte(`{"usage":{"input_tokens":100,"output_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}}`)
	for i := 0; i < 3; i++ {
		usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
		require.NotNil(t, usage)
		result := ValidateZTAPIUsageDimensions(nil, info, usage)
		require.False(t, result.Pending)
		require.Equal(t, int64(50), usageDimension(t, result, "input_tokens").Quantity)
		require.Equal(t, int64(30), usageDimension(t, result, "cache_write").Quantity)
	}
}

func TestOpenAICacheUsageMissingTTLQuoteDoesNotGuessDuration(t *testing.T) {
	for _, write := range []string{`"cache_write_tokens":30`, `"cache_write_tokens":0`, ""} {
		info := cacheUsageInfo()
		delete(info.ZTAPIPublicationSnapshot.SaleUSD, "cache_write")
		fields := `"cached_tokens":20`
		if write != "" {
			fields += "," + write
		}
		usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"input_tokens":100,"output_tokens":2,"input_tokens_details":{`+fields+`}}}`))
		result := ValidateZTAPIUsageDimensions(nil, info, usage)
		if write == `"cache_write_tokens":0` {
			require.NotNil(t, usage)
			require.False(t, result.Pending, "explicit zero writes do not need a TTL price")
		} else {
			require.True(t, result.Pending, "missing write or TTL evidence cannot authorize billing")
		}
	}
}

func TestOpenAICacheUsageDimensionsRejectContradictoryWriteTotal(t *testing.T) {
	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 2,
		PromptTokensDetails:         dto.InputTokenDetails{CachedCreationTokens: 9},
		ClaudeCacheCreation5mTokens: 10}
	result := ValidateZTAPIUsageDimensions(nil, cacheUsageInfo(), usage)
	require.True(t, result.Pending)
	require.Contains(t, result.InvalidDimensions, "cache_write")
}

func TestOpenAICacheUsageRejectsDuplicateEnvelopes(t *testing.T) {
	for _, raw := range []string{
		`{"usage":{"input_tokens":1,"output_tokens":1},"usage":{"input_tokens":100,"output_tokens":2}}`,
		`{"response":{"usage":{"input_tokens":1,"output_tokens":1},"usage":{"input_tokens":100,"output_tokens":2}}}`,
	} {
		info := quotedUsageInfo()
		require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(raw)))
		require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_invalid_json")
	}
}

func TestOpenAICacheUsageCacheMissCounterMustConserveInput(t *testing.T) {
	info := cacheUsageInfo()
	raw := []byte(`{"usage":{"input_tokens":100,"output_tokens":2,"prompt_cache_hit_tokens":20,"prompt_cache_miss_tokens":90,"prompt_tokens_details":{"cache_write_tokens":30}}}`)
	require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, raw))
	require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_total_mismatch")
}

func TestOpenAICacheUsageTTLTotalCanLeaveGenericWriteRemainder(t *testing.T) {
	info := cacheUsageInfo()
	usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"input_tokens":100,"output_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":35},"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`))
	require.NotNil(t, usage)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.False(t, result.Pending)
	require.Equal(t, int64(45), usageDimension(t, result, "input_tokens").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_read").Quantity)
	require.Equal(t, int64(5), usageDimension(t, result, "cache_write").Quantity)
	require.Equal(t, int64(10), usageDimension(t, result, "cache_write_5m").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_write_1h").Quantity)
}

func TestOpenAICacheUsageLegacyOmittedZeroCacheCountersRemainValid(t *testing.T) {
	for _, model := range []string{"gpt-5.4", "claude-opus-4.8", "kimi-k2.7"} {
		t.Run(model, func(t *testing.T) {
			info := cacheUsageInfo()
			info.ChannelMeta.UpstreamModelName = model
			usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"input_tokens":100,"output_tokens":2}}`))
			require.NotNil(t, usage, "pricing dimensions are not a presence contract")
			result := ValidateZTAPIUsageDimensions(nil, info, usage)
			require.False(t, result.Pending)
			require.Equal(t, int64(100), usageDimension(t, result, "input_tokens").Quantity)
		})
	}
}

func TestOpenAICacheUsageLegacyOmittedZeroTTLRemainsValid(t *testing.T) {
	for _, fields := range []string{
		`"prompt_tokens_details":{"cached_creation_tokens":30},"claude_cache_creation_5_m_tokens":30`,
		`"prompt_tokens_details":{"cached_creation_tokens":30},"claude_cache_creation_1_h_tokens":30`,
		`"claude_cache_creation_5_m_tokens":30`,
	} {
		t.Run(fields, func(t *testing.T) {
			info := cacheUsageInfo()
			info.ChannelMeta.UpstreamModelName = "gpt-5.4"
			usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"input_tokens":100,"output_tokens":2,`+fields+`}}`))
			require.NotNil(t, usage)
			result := ValidateZTAPIUsageDimensions(nil, info, usage)
			require.False(t, result.Pending)
			require.Equal(t, int64(70), usageDimension(t, result, "input_tokens").Quantity)
			require.Equal(t, int64(30), info.ZTAPIUsageLastSnapshot["cache_write"])
			require.Equal(t, int64(70), info.ZTAPIUsageLastSnapshot["input_uncached"])
		})
	}
}
