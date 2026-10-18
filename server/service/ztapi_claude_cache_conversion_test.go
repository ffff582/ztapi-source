package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestZTAPIClaudeCacheForwardingBounded(t *testing.T) {
	for _, tc := range []struct {
		name         string
		managed      bool
		channelType  int
		upstream     string
		forwardCache bool
		systemBlocks bool
	}{
		{"managed_claude", true, constant.ChannelTypeOpenAI, "claude-opus-5-hc", true, true},
		{"legacy_claude", false, constant.ChannelTypeOpenAI, "claude-opus-5-hc", false, false},
		{"legacy_openrouter_claude", false, constant.ChannelTypeOpenRouter, "anthropic/claude-opus-5", false, true},
		{"managed_openrouter_claude", true, constant.ChannelTypeOpenRouter, "anthropic/claude-opus-5", true, true},
		{"managed_other_upstream", true, constant.ChannelTypeOpenAI, "gpt-5", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request dto.ClaudeRequest
			require.NoError(t, common.UnmarshalJsonStr(`{"model":"claude-opus-5-hc","cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"one"},{"type":"text","text":"two","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"read_file","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}},{"type":"text","text":"uncached"}]}]}`, &request))
			before, err := common.Marshal(request)
			require.NoError(t, err)
			info := &relaycommon.RelayInfo{
				OriginModelName: "zt-claude-opus-5",
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: tc.channelType, UpstreamModelName: tc.upstream},
			}
			if tc.managed {
				info.ZTAPIPublicationSnapshot = &relaycommon.ZTAPIPublicationSnapshot{PublicName: info.OriginModelName}
			}
			converted, err := ClaudeToOpenAIRequest(request, info)
			require.NoError(t, err)
			body, err := common.Marshal(converted)
			require.NoError(t, err)
			body, err = relaycommon.RemoveDisabledFields(body, dto.ChannelOtherSettings{}, false)
			require.NoError(t, err)
			if tc.forwardCache {
				assert.JSONEq(t, `{"type":"ephemeral"}`, gjson.GetBytes(body, "cache_control").Raw)
				assert.JSONEq(t, `{"type":"ephemeral","ttl":"5m"}`, gjson.GetBytes(body, "tools.0.cache_control").Raw)
			} else {
				assert.False(t, gjson.GetBytes(body, "cache_control").Exists())
				assert.False(t, gjson.GetBytes(body, "tools.0.cache_control").Exists())
			}
			if tc.systemBlocks {
				assert.Equal(t, "one", gjson.GetBytes(body, "messages.0.content.0.text").String())
				assert.Equal(t, "two", gjson.GetBytes(body, "messages.0.content.1.text").String())
				assert.JSONEq(t, `{"type":"ephemeral","ttl":"1h"}`, gjson.GetBytes(body, "messages.0.content.1.cache_control").Raw)
				assert.False(t, gjson.GetBytes(body, "messages.0.content.0.cache_control").Exists())
			} else {
				assert.Equal(t, "onetwo", gjson.GetBytes(body, "messages.0.content").String())
			}
			assert.JSONEq(t, `{"type":"ephemeral"}`, gjson.GetBytes(body, "messages.1.content.0.cache_control").Raw)
			assert.False(t, gjson.GetBytes(body, "messages.1.content.1.cache_control").Exists())
			after, err := common.Marshal(request)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "conversion must not mutate the client request")
		})
	}
}

func TestZTAPIClaudeCacheForwardingDoesNotInventControls(t *testing.T) {
	for _, system := range []string{
		`"plain system"`,
		`[{"type":"text","text":"one"},{"type":"text","text":"two"}]`,
	} {
		t.Run(system, func(t *testing.T) {
			var request dto.ClaudeRequest
			require.NoError(t, common.UnmarshalJsonStr(`{"model":"claude-opus-5-hc","system":`+system+`,"tools":[{"name":"read_file","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`, &request))
			info := &relaycommon.RelayInfo{
				ChannelMeta:              &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: request.Model},
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{PublicName: "zt-claude-opus-5"},
			}
			converted, err := ClaudeToOpenAIRequest(request, info)
			require.NoError(t, err)
			body, err := common.Marshal(converted)
			require.NoError(t, err)
			assert.NotContains(t, string(body), "cache_control")
			assert.True(t, gjson.GetBytes(body, "messages.0.content").Type == gjson.String)
			assert.Equal(t, "hello", gjson.GetBytes(body, "messages.1.content").String())
		})
	}
}

func TestZTAPIClaudeClientCacheUsage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		usage    dto.Usage
		want     dto.ClaudeUsage
		creation *dto.ClaudeCacheCreationUsage
	}{
		{
			name:  "openai_inclusive_input_alias_zero",
			usage: dto.Usage{PromptTokens: 100, CompletionTokens: 7, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80}},
			want:  dto.ClaudeUsage{InputTokens: 20, OutputTokens: 7, CacheReadInputTokens: 80},
		},
		{
			name:  "openai_inclusive_with_input_alias",
			usage: dto.Usage{PromptTokens: 100, InputTokens: 100, UsageSemantic: "openai", PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80}},
			want:  dto.ClaudeUsage{InputTokens: 20, CacheReadInputTokens: 80},
		},
		{
			name:  "openai_semantic_overrides_anthropic_source",
			usage: dto.Usage{PromptTokens: 100, UsageSemantic: "openai", UsageSource: "anthropic", PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80}},
			want:  dto.ClaudeUsage{InputTokens: 20, CacheReadInputTokens: 80},
		},
		{
			name:  "openai_all_cached",
			usage: dto.Usage{PromptTokens: 80, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80}},
			want:  dto.ClaudeUsage{CacheReadInputTokens: 80},
		},
		{
			name:     "openai_aggregate_write_remainder_is_5m",
			usage:    dto.Usage{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 40, CachedCreationTokens: 30}, ClaudeCacheCreation5mTokens: 5, ClaudeCacheCreation1hTokens: 10},
			want:     dto.ClaudeUsage{InputTokens: 30, CacheReadInputTokens: 40, CacheCreationInputTokens: 30},
			creation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 20, Ephemeral1hInputTokens: 10},
		},
		{
			name:     "openai_split_only_write_is_not_lost",
			usage:    dto.Usage{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 40}, ClaudeCacheCreation5mTokens: 10, ClaudeCacheCreation1hTokens: 20},
			want:     dto.ClaudeUsage{InputTokens: 30, CacheReadInputTokens: 40, CacheCreationInputTokens: 30},
			creation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 10, Ephemeral1hInputTokens: 20},
		},
		{
			name:     "openai_split_larger_than_aggregate",
			usage:    dto.Usage{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 40, CachedCreationTokens: 15}, ClaudeCacheCreation5mTokens: 10, ClaudeCacheCreation1hTokens: 20},
			want:     dto.ClaudeUsage{InputTokens: 30, CacheReadInputTokens: 40, CacheCreationInputTokens: 30},
			creation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 10, Ephemeral1hInputTokens: 20},
		},
		{
			name:     "anthropic_exclusive_input_is_not_subtracted_again",
			usage:    dto.Usage{PromptTokens: 20, UsageSemantic: "anthropic", PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80, CachedCreationTokens: 30}},
			want:     dto.ClaudeUsage{InputTokens: 20, CacheReadInputTokens: 80, CacheCreationInputTokens: 30},
			creation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 30},
		},
		{
			name:  "anthropic_zero_uncached_does_not_fall_back_to_inclusive_alias",
			usage: dto.Usage{InputTokens: 80, UsageSemantic: "anthropic", PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80}},
			want:  dto.ClaudeUsage{CacheReadInputTokens: 80},
		},
		{
			name:  "cache_cannot_make_uncached_input_negative",
			usage: dto.Usage{PromptTokens: 10, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 20}},
			want:  dto.ClaudeUsage{CacheReadInputTokens: 20},
		},
		{
			name:  "negative_counts_are_not_sent_to_client",
			usage: dto.Usage{PromptTokens: -10, CompletionTokens: -2, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: -3, CachedCreationTokens: -4}, ClaudeCacheCreation5mTokens: -5, ClaudeCacheCreation1hTokens: -6},
			want:  dto.ClaudeUsage{},
		},
		{
			name:     "negative_split_does_not_cancel_positive_write",
			usage:    dto.Usage{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedCreationTokens: 30}, ClaudeCacheCreation5mTokens: -5, ClaudeCacheCreation1hTokens: 10},
			want:     dto.ClaudeUsage{InputTokens: 70, CacheCreationInputTokens: 30},
			creation: &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 20, Ephemeral1hInputTokens: 10},
		},
		{
			name:  "no_cache_preserves_input_and_output",
			usage: dto.Usage{PromptTokens: 100, CompletionTokens: 7},
			want:  dto.ClaudeUsage{InputTokens: 100, OutputTokens: 7},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.usage
			got := buildClaudeUsageFromOpenAIUsage(&tc.usage)
			require.NotNil(t, got)
			assert.Equal(t, tc.want.InputTokens, got.InputTokens, "Claude input_tokens excludes cached read/write tokens")
			assert.Equal(t, tc.want.OutputTokens, got.OutputTokens)
			assert.Equal(t, tc.want.CacheReadInputTokens, got.CacheReadInputTokens)
			assert.Equal(t, tc.want.CacheCreationInputTokens, got.CacheCreationInputTokens)
			assert.Equal(t, tc.creation, got.CacheCreation)
			assert.Equal(t, original, tc.usage, "billing usage must not be mutated by client conversion")
		})
	}
	t.Run("nil_usage", func(t *testing.T) {
		assert.Nil(t, buildClaudeUsageFromOpenAIUsage(nil))
	})
}

func TestZTAPIClaudeClientCacheUsageInResponseAndStream(t *testing.T) {
	usage := dto.Usage{PromptTokens: 100, CompletionTokens: 7, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80}}
	original := usage
	response := ResponseOpenAI2Claude(&dto.OpenAITextResponse{Usage: usage}, &relaycommon.RelayInfo{})
	require.NotNil(t, response.Usage)
	assert.Equal(t, 20, response.Usage.InputTokens)
	assert.Equal(t, 80, response.Usage.CacheReadInputTokens)

	info := &relaycommon.RelayInfo{SendResponseCount: 2, ClaudeConvertInfo: &relaycommon.ClaudeConvertInfo{Usage: &usage}}
	for _, frameUsage := range []*dto.Usage{&usage, nil} {
		info.ClaudeConvertInfo.Done = false
		frames := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{Usage: frameUsage}, info)
		require.NotEmpty(t, frames)
		require.Equal(t, "message_delta", frames[0].Type)
		require.NotNil(t, frames[0].Usage)
		assert.Equal(t, 20, frames[0].Usage.InputTokens)
		assert.Equal(t, 80, frames[0].Usage.CacheReadInputTokens)
		assert.Equal(t, original, usage)
	}
}
