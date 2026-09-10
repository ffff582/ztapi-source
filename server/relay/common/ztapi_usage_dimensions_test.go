package common

import (
	"math"
	"net/http/httptest"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIUsageDimensionsRejectOverflowBeforePricing(t *testing.T) {
	info := quotedUsageInfo()
	for _, name := range []string{"cache_write", "cache_write_5m", "cache_write_1h"} {
		info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, name)
		info.ZTAPIPublicationSnapshot.SaleUSD[name] = "0"
	}
	usage := &dto.Usage{UsageSemantic: "anthropic", PromptTokens: 10, CompletionTokens: 1, ClaudeCacheCreation5mTokens: math.MaxInt, ClaudeCacheCreation1hTokens: math.MaxInt}
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.True(t, result.Pending, "overflowing cache totals cannot be accepted as a priced result")
	require.NotEmpty(t, result.InvalidDimensions)
}

func quotedUsageInfo() *RelayInfo {
	return &RelayInfo{ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{
		PriceSourceID: 17, PriceSourceVersion: 3, Modality: "text",
		BillingDimensions:    []string{"input_tokens", "output_tokens"},
		SaleUSD:              map[string]string{"input_tokens": "1.2345678901", "output_tokens": "2.5"},
		InputPricePerMillion: 1.23456789, OutputPricePerMillion: 2.5,
		CacheReadRatio: 1, CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25,
		CacheCreation1hRatio: 2, ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1,
	}}
}

func usageDimension(t *testing.T, result ZTAPIUsageDimensionResult, name string) ZTAPIUsageDimension {
	t.Helper()
	for _, dimension := range result.Dimensions {
		if dimension.Dimension == name {
			return dimension
		}
	}
	t.Fatalf("dimension %s missing from %+v", name, result.Dimensions)
	return ZTAPIUsageDimension{}
}

func TestZTAPIUsageDimensionsMissingQuoteNotInferredFromRatios(t *testing.T) {
	info := quotedUsageInfo()
	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 20,
		PromptTokensDetails:         dto.InputTokenDetails{CachedTokens: 5, CachedCreationTokens: 15},
		ClaudeCacheCreation5mTokens: 7, ClaudeCacheCreation1hTokens: 3}
	result := ValidateZTAPIUsageDimensions(nil, info, usage)
	require.True(t, result.Managed)
	require.True(t, result.Pending)
	require.Equal(t, []string{"cache_read", "cache_write", "cache_write_1h", "cache_write_5m"}, result.MissingDimensions)
	input := usageDimension(t, result, "input_tokens")
	require.Equal(t, int64(80), input.Quantity)
	require.Equal(t, "token", input.Unit)
	require.Equal(t, "0.0000012345678901", input.UnitPriceUSD)
	require.Equal(t, ZTAPIQuoteStateQuoted, input.QuoteState)
	require.Equal(t, int64(5), usageDimension(t, result, "cache_write").Quantity)
	require.Empty(t, usageDimension(t, result, "cache_read").UnitPriceUSD)
}

func TestZTAPIUsageDimensionsDistinguishQuotedFreeAndMissing(t *testing.T) {
	for _, tc := range []struct {
		name, price string
		listed      bool
		state       ZTAPIQuoteState
		pending     bool
	}{
		{"quoted", "0.25", true, ZTAPIQuoteStateQuoted, false},
		{"explicit_free", "0", true, ZTAPIQuoteStateFree, false},
		{"unlisted_zero", "0", false, ZTAPIQuoteStateMissing, true},
		{"unlisted_positive", "0.25", false, ZTAPIQuoteStateMissing, true},
		{"empty", "", true, ZTAPIQuoteStateMissing, true},
		{"negative", "-1", true, ZTAPIQuoteStateMissing, true},
		{"malformed", "NaN", true, ZTAPIQuoteStateMissing, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := quotedUsageInfo()
			info.ZTAPIPublicationSnapshot.SaleUSD["cache_read"] = tc.price
			if tc.listed {
				info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "cache_read")
			}
			result := ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{PromptTokens: 10, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 4}})
			require.Equal(t, tc.pending, result.Pending)
			require.Equal(t, tc.state, usageDimension(t, result, "cache_read").QuoteState)
			if tc.state == ZTAPIQuoteStateFree {
				require.Equal(t, "0", usageDimension(t, result, "cache_read").UnitPriceUSD)
			}
		})
	}
}

func TestZTAPIUsageDimensionsRequireProvenance(t *testing.T) {
	for _, missing := range []string{"id", "version", "dimensions", "prices"} {
		t.Run(missing, func(t *testing.T) {
			info := quotedUsageInfo()
			s := info.ZTAPIPublicationSnapshot
			switch missing {
			case "id":
				s.PriceSourceID = 0
			case "version":
				s.PriceSourceVersion = 0
			case "dimensions":
				s.BillingDimensions = nil
			case "prices":
				s.SaleUSD = nil
			}
			result := ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{PromptTokens: 2})
			require.True(t, result.Pending)
			require.Equal(t, []string{"input_tokens"}, result.MissingDimensions)
		})
	}
}

func TestZTAPIUsageDimensionsNilAndZeroUsagePreserveLegacy(t *testing.T) {
	for _, info := range []*RelayInfo{nil, {}} {
		require.Equal(t, ZTAPIUsageDimensionResult{}, ValidateZTAPIUsageDimensions(nil, info, nil))
	}
	info := quotedUsageInfo()
	nilResult := ValidateZTAPIUsageDimensions(nil, info, nil)
	require.True(t, nilResult.Pending)
	require.True(t, nilResult.UsageMissing)
	require.Empty(t, nilResult.Dimensions, "must not manufacture estimated usage")
	zeroResult := ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{})
	require.True(t, zeroResult.Pending)
	require.Equal(t, []string{"usage_zero_unconfirmed"}, zeroResult.InvalidDimensions)
	require.Empty(t, zeroResult.MissingDimensions)
	require.False(t, ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{PromptTokens: 5, CompletionTokens: 1}).Pending)
	labelledZero := ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{UsageSource: "upstream"})
	require.True(t, labelledZero.Pending, "a source label is not a supplier zero-billing receipt")
}

func TestZTAPIUsageDimensionsCacheInputSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, semantic string
		format         types.RelayFormat
		prompt, want   int
	}{
		{"openai_inclusive", "openai", types.RelayFormatOpenAI, 100, 65},
		{"anthropic_exclusive", "anthropic", types.RelayFormatOpenAI, 65, 65},
		{"claude_format", "", types.RelayFormatClaude, 65, 65},
		{"explicit_overrides_format", "openai", types.RelayFormatClaude, 100, 65},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := quotedUsageInfo()
			info.RelayFormat = tc.format
			usage := &dto.Usage{PromptTokens: tc.prompt, UsageSemantic: tc.semantic,
				PromptTokensDetails:         dto.InputTokenDetails{CachedTokens: 20, CachedCreationTokens: 15},
				ClaudeCacheCreation5mTokens: 10, ClaudeCacheCreation1hTokens: 5}
			result := ValidateZTAPIUsageDimensions(nil, info, usage)
			require.Equal(t, int64(tc.want), usageDimension(t, result, "input_tokens").Quantity)
			require.NotContains(t, result.MissingDimensions, "cache_write", "split totals must not require a duplicate generic write quote")
		})
	}
}

func TestZTAPIUsageDimensionsResponsesUsageAndReasoning(t *testing.T) {
	result := ValidateZTAPIUsageDimensions(nil, quotedUsageInfo(), &dto.Usage{
		InputTokens: 100, OutputTokens: 20,
		InputTokensDetails:     &dto.InputTokenDetails{CachedTokens: 30},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 10},
	})
	require.Equal(t, int64(70), usageDimension(t, result, "input_tokens").Quantity)
	require.Equal(t, int64(20), usageDimension(t, result, "output_tokens").Quantity, "reasoning is included in completion, not an extra charge")
	require.Equal(t, []string{"cache_read"}, result.MissingDimensions)
}

func TestZTAPIUsageDimensionsImageAudioAndToolsCannotBorrowTokenQuotes(t *testing.T) {
	info := quotedUsageInfo()
	info.ResponsesUsageInfo = &ResponsesUsageInfo{BuiltInTools: map[string]*BuildInToolInfo{
		"web_search_preview": {CallCount: 2}, "file_search": {CallCount: 0}, "unknown_tool": {CallCount: 1}, "nil_tool": nil,
	}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("claude_web_search_requests", 1)
	c.Set("image_generation_call", true)
	c.Set("image_generation_call_quality", "high")
	c.Set("image_generation_call_size", "1024x1024")
	result := ValidateZTAPIUsageDimensions(c, info, &dto.Usage{PromptTokens: 100, CompletionTokens: 20,
		PromptTokensDetails:    dto.InputTokenDetails{ImageTokens: 10, AudioTokens: 5},
		CompletionTokenDetails: dto.OutputTokenDetails{ImageTokens: 2, AudioTokens: 3},
	})
	require.True(t, result.Pending)
	require.Equal(t, []string{"audio_input_tokens", "audio_output_tokens", "image_generation:high:1024x1024", "image_input_tokens", "image_output_tokens", "unknown_tool", "web_search", "web_search_preview"}, result.MissingDimensions)
	require.Equal(t, int64(85), usageDimension(t, result, "input_tokens").Quantity)
	require.Equal(t, int64(15), usageDimension(t, result, "output_tokens").Quantity)
	require.Equal(t, "call", usageDimension(t, result, "web_search_preview").Unit)
	require.Equal(t, int64(2), usageDimension(t, result, "web_search_preview").Quantity)
}

func TestZTAPIUsageDimensionsEmbeddingIsInputOnly(t *testing.T) {
	info := quotedUsageInfo()
	info.ZTAPIPublicationSnapshot.Modality = "embedding"
	info.ZTAPIPublicationSnapshot.BillingDimensions = []string{"input_tokens"}
	delete(info.ZTAPIPublicationSnapshot.SaleUSD, "output_tokens")
	info.ZTAPIPublicationSnapshot.OutputPricePerMillion = 0
	result := ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{PromptTokens: 10})
	require.False(t, result.Pending)
	require.Len(t, result.Dimensions, 1)
	require.Equal(t, []string{"output_tokens"}, ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{PromptTokens: 10, CompletionTokens: 1}).MissingDimensions)
}

func TestZTAPIUsageDimensionsSnapshotCopiesAreFrozen(t *testing.T) {
	info := quotedUsageInfo()
	snapshot := info.ZTAPIPublicationSnapshot
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetZTAPIPublicationSnapshot(c, snapshot)
	snapshot.SaleUSD["input_tokens"] = "99"
	snapshot.BillingDimensions[0] = "wrong"
	first := GetZTAPIPublicationSnapshot(c)
	first.SaleUSD["input_tokens"] = "88"
	first.BillingDimensions[0] = "wrong_again"
	encoded, err := basecommon.Marshal(GetZTAPIPublicationSnapshot(c))
	require.NoError(t, err)
	var restored ZTAPIPublicationSnapshot
	require.NoError(t, basecommon.Unmarshal(encoded, &restored))
	info.ZTAPIPublicationSnapshot = &restored
	result := ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{PromptTokens: 5})
	require.False(t, result.Pending)
	require.Equal(t, "0.0000012345678901", usageDimension(t, result, "input_tokens").UnitPriceUSD)
	require.Equal(t, int64(17), restored.PriceSourceID)
	require.Equal(t, uint64(3), restored.PriceSourceVersion)
}

func TestZTAPIUsageDimensionsMalformedUsageCannotSettle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   dto.Usage
		invalid string
	}{
		{"negative_cache", dto.Usage{PromptTokens: 10, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: -1}}, "cache_read"},
		{"negative_alias_cache", dto.Usage{PromptTokens: 10, InputTokensDetails: &dto.InputTokenDetails{CachedTokens: -1}}, "cache_read"},
		{"negative_total", dto.Usage{TotalTokens: -1}, "total_tokens"},
		{"unexplained_total", dto.Usage{TotalTokens: 10}, "total_tokens"},
		{"reasoning_exceeds_output", dto.Usage{CompletionTokens: 1, CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 2}}, "output_tokens"},
		{"negative_reasoning", dto.Usage{CompletionTokens: 1, CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: -1}}, "output_tokens"},
		{"unknown_semantic", dto.Usage{PromptTokens: 10, UsageSemantic: "guess"}, "usage_semantic"},
		{"conflicting_input_alias", dto.Usage{PromptTokens: 10, InputTokens: 20}, "input_tokens"},
		{"cache_exceeds_input", dto.Usage{PromptTokens: 2, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 3}}, "input_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := ValidateZTAPIUsageDimensions(nil, quotedUsageInfo(), &tc.usage)
			require.True(t, result.Pending)
			require.Contains(t, result.InvalidDimensions, tc.invalid)
		})
	}
}

func TestZTAPIUsageDimensionsClaudeAllCachedDoesNotUseInclusiveAliasAsFreshInput(t *testing.T) {
	result := ValidateZTAPIUsageDimensions(nil, quotedUsageInfo(), &dto.Usage{
		UsageSemantic: "anthropic", InputTokens: 30,
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 30},
	})
	require.Equal(t, []string{"cache_read"}, result.MissingDimensions)
	require.Len(t, result.Dimensions, 1)
	require.Empty(t, result.InvalidDimensions)
}

func TestZTAPIUsageDimensionsNilUsageStillRetainsObservedTools(t *testing.T) {
	info := quotedUsageInfo()
	info.ResponsesUsageInfo = &ResponsesUsageInfo{BuiltInTools: map[string]*BuildInToolInfo{"file_search": {CallCount: 2}}}
	result := ValidateZTAPIUsageDimensions(nil, info, nil)
	require.True(t, result.UsageMissing)
	require.True(t, result.Pending)
	require.Equal(t, []string{"file_search"}, result.MissingDimensions)
	require.Equal(t, int64(2), usageDimension(t, result, "file_search").Quantity)
}
