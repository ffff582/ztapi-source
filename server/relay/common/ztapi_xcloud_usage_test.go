package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIUsageRejectsEmbeddedClaudeReceiptWhenAliasesConflict(t *testing.T) {
	info := quotedUsageInfo()
	info.ChannelMeta = &ChannelMeta{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-5"}
	raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":16,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":4}},"input_tokens":16,"prompt_tokens":15,"output_tokens":0,"completion_tokens":4,"total_tokens":19,"prompt_tokens_details":{"cached_tokens":0},"claude_cache_creation_5_m_tokens":0,"claude_cache_creation_1_h_tokens":0}}`)
	usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
	require.Nil(t, usage)
	require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons)
	require.True(t, ValidateZTAPIUsageDimensions(nil, info, usage).Pending)
}

func TestEmbeddedClaudeUsageValidatesAllEvidence(t *testing.T) {
	const receipt = `"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	for _, tc := range []struct {
		name, extra string
		pending     bool
	}{
		{"exclusive", `,"prompt_tokens":50,"input_tokens":50,"output_tokens":2,"completion_tokens":2,"total_tokens":52`, false},
		{"inclusive", `,"prompt_tokens":100,"input_tokens":100,"output_tokens":2,"total_tokens":102,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"claude_cache_creation_5_m_tokens":10,"claude_cache_creation_1_h_tokens":20`, false},
		{"input_conflict", `,"prompt_tokens":51`, true},
		{"mixed_input_aliases", `,"prompt_tokens":50,"input_tokens":100`, true},
		{"output_conflict", `,"output_tokens":3`, true},
		{"total_conflict", `,"prompt_tokens":50,"total_tokens":102`, true},
		{"cache_read_conflict", `,"prompt_tokens_details":{"cached_tokens":19}`, true},
		{"cache_write_conflict", `,"input_tokens_details":{"cache_write_tokens":29}`, true},
		{"tier_conflict", `,"cache_creation":{"ephemeral_5m_input_tokens":11}`, true},
		{"unknown_tier", `,"cache_creation":{"ephemeral_2h_input_tokens":1}`, true},
		{"invalid_tier", `,"claude_cache_creation_5_m_tokens":null`, true},
		{"nested_details_conflict", `,"input_tokens_details":{"cache_creation":{"ephemeral_1h_input_tokens":19}}`, true},
		{"unknown_top", `,"future_tokens":1`, true},
		{"unknown_details", `,"prompt_tokens_details":{"future_tokens":1}`, true},
		{"image_input", `,"prompt_tokens_details":{"image_tokens":5}`, true},
		{"audio_output", `,"completion_tokens_details":{"audio_tokens":1}`, true},
		{"semantic_conflict", `,"usage_semantic":"anthropic"`, true},
		{"null_alias", `,"output_tokens":null`, true},
		{"negative_alias", `,"output_tokens":-1`, true},
		{"fractional_alias", `,"output_tokens":2.0`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := cacheUsageInfo()
			info.ChannelMeta = &ChannelMeta{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-4-8"}
			usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{`+receipt+tc.extra+`}}`))
			result := ValidateZTAPIUsageDimensions(nil, info, usage)
			require.Equal(t, tc.pending, result.Pending, "%+v", result)
			if !tc.pending {
				require.Equal(t, 100, usage.PromptTokens)
				require.Equal(t, int64(50), usageDimension(t, result, "input_tokens").Quantity)
				require.Equal(t, int64(20), usageDimension(t, result, "cache_read").Quantity)
				require.Equal(t, int64(10), usageDimension(t, result, "cache_write_5m").Quantity)
				require.Equal(t, int64(20), usageDimension(t, result, "cache_write_1h").Quantity)
			} else {
				require.Nil(t, usage)
				require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons)
			}
		})
	}
}

func TestEmbeddedClaudeUsageMatchesVerifiedXcloudReceiptShape(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		wantOK      bool
	}{
		{"verified", `"input_tokens":16,"prompt_tokens":16,"output_tokens":0,"completion_tokens":4,"total_tokens":20,"usage_semantic":"openai","usage_source":"upstream_reported","input_tokens_details":null,"prompt_tokens_details":{"cached_tokens":0,"text_tokens":0,"audio_tokens":0,"image_tokens":0},"completion_tokens_details":{"text_tokens":0,"audio_tokens":0,"image_tokens":0,"reasoning_tokens":0}`, true},
		{"wrong_completion", `"input_tokens":16,"output_tokens":0,"completion_tokens":3,"total_tokens":20`, false},
		{"missing_total", `"input_tokens":16,"output_tokens":0,"completion_tokens":4`, false},
		{"wrong_total", `"input_tokens":16,"output_tokens":0,"completion_tokens":4,"total_tokens":19`, false},
		{"reversed", `"input_tokens":16,"output_tokens":4,"completion_tokens":0,"total_tokens":20`, false},
		{"nonzero_audio", `"input_tokens":16,"output_tokens":0,"completion_tokens":4,"total_tokens":20,"completion_tokens_details":{"audio_tokens":1}`, false},
		{"unknown_dimension", `"input_tokens":16,"output_tokens":0,"completion_tokens":4,"total_tokens":20,"completion_tokens_details":{"future_tokens":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := quotedUsageInfo()
			info.ChannelMeta = &ChannelMeta{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-sonnet-5"}
			raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":16,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":4}},` + tc.usage + `}}`)
			usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
			if tc.wantOK {
				require.NotNil(t, usage)
				require.Equal(t, 16, usage.PromptTokens)
				require.Equal(t, 4, usage.CompletionTokens)
				return
			}
			require.Nil(t, usage)
			require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons)
		})
	}
}

func TestZeroOutputAliasDoesNotGeneralizeToUntestedRoute(t *testing.T) {
	for _, channel := range []*ChannelMeta{
		{ChannelId: 5, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-sonnet-5"},
		{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-unverified"},
	} {
		info := quotedUsageInfo()
		info.ChannelMeta = channel
		raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":16,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":4}},"input_tokens":16,"output_tokens":0,"completion_tokens":4,"total_tokens":20}}`)
		require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, raw))
		require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons)
	}
}

func TestEmbeddedClaudeUsagePreservesTopLevelCacheTiers(t *testing.T) {
	info := cacheUsageInfo()
	info.ChannelMeta = &ChannelMeta{ChannelId: 5, ChannelBaseUrl: "https://www.xcloudtoken.com/", UpstreamModelName: "claude-opus-4-8"}
	raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":2}},"input_tokens":100,"total_tokens":102,"cache_write_5m_tokens":10,"cache_write_1h_tokens":20}}`)
	usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
	require.NotNil(t, usage)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.False(t, result.Pending, "%+v", result)
	require.Equal(t, int64(10), usageDimension(t, result, "cache_write_5m").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_write_1h").Quantity)
}

func TestEmbeddedClaudeUsageInvalidStreamEvidenceIsSticky(t *testing.T) {
	info := quotedUsageInfo()
	info.ChannelMeta = &ChannelMeta{ChannelId: 6, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-5"}
	info.IsStream = true
	const receipt = `"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":5,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`
	require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{`+receipt+`,"prompt_tokens":4}}`)))
	usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{`+receipt+`,"prompt_tokens":5}}`))
	require.True(t, ValidateZTAPIUsageDimensions(nil, info, usage).Pending)
	require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_alias_conflict")
}

func TestEmbeddedClaudeUsageRejectsDuplicateKeys(t *testing.T) {
	info := quotedUsageInfo()
	info.ChannelMeta = &ChannelMeta{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-5"}
	raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":5,"input_tokens":6,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"prompt_tokens":6}}`)
	usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
	require.Nil(t, usage)
	require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_invalid_json")
	require.True(t, ValidateZTAPIUsageDimensions(nil, info, usage).Pending)
}

func TestEmbeddedClaudeUsageRejectsUnknownNestedEvidence(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown_claude":      `{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"image_tokens":1}}`,
		"unknown_billing":     `{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"future_charge":1}`,
		"invalid_semantic":    `{"source":"claude_messages","semantic":"openai","claude_usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`,
		"null_receipt":        `null`,
		"null_cache_creation": `{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			info := quotedUsageInfo()
			info.ChannelMeta = &ChannelMeta{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-5"}
			usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(`{"usage":{"billing_usage":`+raw+`,"input_tokens":1,"output_tokens":1}}`))
			require.Nil(t, usage)
			require.True(t, ValidateZTAPIUsageDimensions(nil, info, usage).Pending)
			require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons)
		})
	}
}

func TestEmbeddedClaudeUsageKeepsExclusiveCacheDimensions(t *testing.T) {
	info := cacheUsageInfo()
	info.ChannelMeta.ChannelId = 4
	info.ChannelMeta.ChannelBaseUrl = "https://www.xcloudtoken.com"
	info.ChannelMeta.UpstreamModelName = "claude-opus-4-8"
	raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}},"prompt_tokens":50,"completion_tokens":2,"total_tokens":52}}`)
	usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
	require.NotNil(t, usage)
	require.Equal(t, 100, usage.PromptTokens)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.False(t, result.Pending, "%+v", result)
	require.Equal(t, int64(50), usageDimension(t, result, "input_tokens").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_read").Quantity)
	require.Equal(t, int64(10), usageDimension(t, result, "cache_write_5m").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_write_1h").Quantity)
	for _, dimension := range result.Dimensions {
		require.NotEqual(t, "cache_write", dimension.Dimension)
	}
}

func TestEmbeddedClaudeUsageRejectsInvalidCacheEvidence(t *testing.T) {
	for _, cache := range []string{
		`"cache_read_input_tokens":-1,"cache_creation_input_tokens":0`,
		`"cache_read_input_tokens":0,"cache_creation_input_tokens":"bad"`,
		`"cache_read_input_tokens":0,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":11}`,
		`"cache_read_input_tokens":0,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_2h_input_tokens":10}`,
		`"cache_read_input_tokens":0,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":8},"claude_cache_creation_5_m_tokens":9`,
		`"cache_read_input_tokens":0,"cache_creation_input_tokens":10,"claude_cache_creation_5_m_tokens":"invalid"`,
	} {
		t.Run(cache, func(t *testing.T) {
			info := quotedUsageInfo()
			info.ChannelMeta = &ChannelMeta{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-5"}
			raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":50,"output_tokens":2,` + cache + `}},"input_tokens":50,"output_tokens":2,"total_tokens":52}}`)
			require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, raw))
			require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons)
		})
	}
}

func TestEmbeddedClaudeUsageRejectsNullRequiredQuantities(t *testing.T) {
	for _, usage := range []string{
		`{"input_tokens":null,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`,
		`{"input_tokens":50,"output_tokens":null,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`,
		`{"input_tokens":50,"output_tokens":2,"cache_read_input_tokens":null,"cache_creation_input_tokens":0}`,
		`{"input_tokens":50,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation_input_tokens":null}`,
	} {
		info := quotedUsageInfo()
		info.ChannelMeta = &ChannelMeta{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-5"}
		raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":` + usage + `}}}`)
		require.Nil(t, ObserveZTAPIOpenAIUsage(nil, info, raw), usage)
		require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons)
	}
}

func TestEmbeddedClaudeUsageRetainsDirectCacheTierAliases(t *testing.T) {
	info := cacheUsageInfo()
	info.ChannelMeta = &ChannelMeta{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-4-8"}
	raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":2,"claude_cache_creation_5_m_tokens":10,"claude_cache_creation_1_h_tokens":20}},"prompt_tokens":50,"completion_tokens":2,"total_tokens":52}}`)
	usage := ObserveZTAPIOpenAIUsage(nil, info, raw)
	require.NotNil(t, usage)
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.False(t, result.Pending, "%+v", result)
	require.Equal(t, int64(10), usageDimension(t, result, "cache_write_5m").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "cache_write_1h").Quantity)
}

func TestEmbeddedClaudeUsageDoesNotOverrideOtherProviders(t *testing.T) {
	for _, channel := range []*ChannelMeta{
		{ChannelId: 2, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "claude-opus-5"},
		{ChannelId: 4, ChannelBaseUrl: "https://other.example", UpstreamModelName: "claude-opus-5"},
		{ChannelId: 4, ChannelBaseUrl: "https://www.xcloudtoken.com", UpstreamModelName: "gpt-5"},
		nil,
	} {
		info := quotedUsageInfo()
		info.ChannelMeta = channel
		raw := []byte(`{"usage":{"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":50,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"input_tokens":50,"prompt_tokens":49,"output_tokens":2,"total_tokens":52}}`)
		ObserveZTAPIOpenAIUsage(nil, info, raw)
		require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_alias_conflict")
	}
}
