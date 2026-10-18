package service

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZTAPIClaudeCacheProjectionAliases(t *testing.T) {
	for _, tc := range []struct {
		name     string
		usage    dto.Usage
		input    int
		read     int
		creation int
		write5m  int
		write1h  int
	}{
		{
			name:  "input_details_read",
			usage: dto.Usage{PromptTokens: 100, InputTokensDetails: &dto.InputTokenDetails{CachedTokens: 80}},
			input: 20, read: 80,
		},
		{
			name:  "prompt_cache_hit_read",
			usage: dto.Usage{PromptTokens: 100, PromptCacheHitTokens: 80},
			input: 20, read: 80,
		},
		{
			name: "duplicate_read_aliases_are_not_added",
			usage: dto.Usage{PromptTokens: 100, PromptCacheHitTokens: 80,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 80},
				InputTokensDetails:  &dto.InputTokenDetails{CachedTokens: 80}},
			input: 20, read: 80,
		},
		{
			name: "input_details_creation_with_split_remainder",
			usage: dto.Usage{PromptTokens: 100,
				InputTokensDetails:          &dto.InputTokenDetails{CachedTokens: 40, CachedCreationTokens: 30},
				ClaudeCacheCreation5mTokens: 10, ClaudeCacheCreation1hTokens: 5},
			input: 30, read: 40, creation: 30, write5m: 25, write1h: 5,
		},
		{
			name: "duplicate_creation_aliases_are_not_added",
			usage: dto.Usage{PromptTokens: 100,
				PromptTokensDetails:         dto.InputTokenDetails{CachedTokens: 40, CachedCreationTokens: 30},
				InputTokensDetails:          &dto.InputTokenDetails{CachedTokens: 40, CachedCreationTokens: 30},
				ClaudeCacheCreation5mTokens: 10, ClaudeCacheCreation1hTokens: 20},
			input: 30, read: 40, creation: 30, write5m: 10, write1h: 20,
		},
		{
			name: "native_anthropic_aliases_preserve_exclusive_input",
			usage: dto.Usage{PromptTokens: 20, UsageSemantic: "anthropic", PromptCacheHitTokens: 80,
				InputTokensDetails:          &dto.InputTokenDetails{CachedTokens: 80, CachedCreationTokens: 30},
				ClaudeCacheCreation5mTokens: 10, ClaudeCacheCreation1hTokens: 20},
			input: 20, read: 80, creation: 30, write5m: 10, write1h: 20,
		},
		{
			name: "negative_aliases_do_not_create_client_cache_counts",
			usage: dto.Usage{PromptTokens: 100, PromptCacheHitTokens: -3,
				InputTokensDetails: &dto.InputTokenDetails{CachedTokens: -80, CachedCreationTokens: -30}},
			input: 100,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := common.Marshal(tc.usage)
			require.NoError(t, err)
			got := buildClaudeUsageFromOpenAIUsage(&tc.usage)
			require.NotNil(t, got)
			assert.Equal(t, tc.input, got.InputTokens)
			assert.Equal(t, tc.read, got.CacheReadInputTokens)
			assert.Equal(t, tc.creation, got.CacheCreationInputTokens)
			if tc.creation == 0 {
				assert.Nil(t, got.CacheCreation)
			} else {
				require.NotNil(t, got.CacheCreation)
				assert.Equal(t, tc.write5m, got.CacheCreation.Ephemeral5mInputTokens)
				assert.Equal(t, tc.write1h, got.CacheCreation.Ephemeral1hInputTokens)
			}
			after, err := common.Marshal(tc.usage)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "including pointed-to alias details, billing evidence must remain unchanged")
		})
	}
}

func TestZTAPIClaudeCacheProjectionOverflow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		write5m      int
		write1h      int
		want5m       int
		want1h       int
		native       bool
		wantUncached int
	}{
		{"5m_max_plus_1h_one", math.MaxInt, 1, math.MaxInt - 1, 1, false, 0},
		{"5m_one_plus_1h_max", 1, math.MaxInt, 0, math.MaxInt, false, 0},
		{"both_max", math.MaxInt, math.MaxInt, 0, math.MaxInt, false, 0},
		{"exact_max_sum", math.MaxInt - 1, 1, math.MaxInt - 1, 1, false, 0},
		{"native_exclusive_input", math.MaxInt, 1, math.MaxInt - 1, 1, true, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := dto.Usage{PromptTokens: 20, CompletionTokens: 2,
				ClaudeCacheCreation5mTokens: tc.write5m, ClaudeCacheCreation1hTokens: tc.write1h}
			if tc.native {
				usage.UsageSemantic = "anthropic"
			}
			before := usage
			got := buildClaudeUsageFromOpenAIUsage(&usage)
			require.NotNil(t, got)
			require.NotNil(t, got.CacheCreation)
			assert.Equal(t, tc.wantUncached, got.InputTokens)
			assert.Equal(t, math.MaxInt, got.CacheCreationInputTokens)
			assert.Equal(t, tc.want5m, got.CacheCreation.Ephemeral5mInputTokens)
			assert.Equal(t, tc.want1h, got.CacheCreation.Ephemeral1hInputTokens)
			assert.Equal(t, got.CacheCreationInputTokens, got.CacheCreation.Ephemeral5mInputTokens+got.CacheCreation.Ephemeral1hInputTokens)
			assert.Equal(t, before, usage, "client saturation must not normalize the raw billing usage")
		})
	}
}
