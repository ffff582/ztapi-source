package openai

import (
	"fmt"
	"net/http"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestPoolTTLCacheAliasesReachChatAndResponsesBilling(t *testing.T) {
	raw := `{"completion_tokens":5,"prompt_tokens":20,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0},"cache_write_5m_tokens":0,"cache_write_1h_tokens":0}`
	for _, route := range []string{"chat", "chat_to_responses", "responses_to_chat"} {
		for _, stream := range []bool{false, true} {
			t.Run(route+"/"+fmt.Sprint(stream), func(t *testing.T) {
				usage, c, info := runKimiCacheHeaderFixture(t, route, stream, http.Header{}, []string{raw}, "gpt-6-luna", true)
				require.Equal(t, "upstream_reported", usage.UsageSource)
				require.Equal(t, 20, usage.PromptTokens)
				require.Equal(t, 5, usage.CompletionTokens)
				require.False(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
				require.Contains(t, string(info.ZTAPIMetering.RawUsage), `"cache_write_5m_tokens":0`)
			})
		}
	}
}

func TestPoolTTLCachePositiveAliasesReachPricedDimensions(t *testing.T) {
	raw := `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":35},"cache_write_5m_tokens":10,"cache_write_1h_tokens":20}`
	for _, route := range []string{"chat", "chat_to_responses", "responses_to_chat"} {
		for _, stream := range []bool{false, true} {
			t.Run(route+"/"+fmt.Sprint(stream), func(t *testing.T) {
				usage, c, info := runKimiCacheHeaderFixture(t, route, stream, http.Header{}, []string{raw}, "gpt-6-luna", true)
				result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
				require.False(t, result.Pending)
				require.Equal(t, map[string]int64{"input_tokens": 45, "output_tokens": 2, "cache_read": 20, "cache_write": 5, "cache_write_5m": 10, "cache_write_1h": 20}, kimiUsageDimensions(result))
				delete(info.ZTAPIPublicationSnapshot.SaleUSD, "cache_write_5m")
				result = relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
				require.True(t, result.Pending)
				require.Contains(t, result.MissingDimensions, "cache_write_5m")
			})
		}
	}
}

func TestPoolTTLCacheAliasesPreserveKimiHeaderAuthority(t *testing.T) {
	valid := `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"cache_write_5m_tokens":10,"cache_write_1h_tokens":20}`
	for _, tc := range []struct {
		name, raw string
		header    http.Header
		pending   bool
	}{
		{"matching", valid, kimiCacheHeaders("10", "20"), false},
		{"partial_backed_by_headers", `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"cache_write_5m_tokens":10}`, kimiCacheHeaders("10", "20"), false},
		{"missing_read", `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cache_write_tokens":30},"cache_write_5m_tokens":10,"cache_write_1h_tokens":20}`, kimiCacheHeaders("10", "20"), true},
		{"missing_aggregate", `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20},"cache_write_5m_tokens":10,"cache_write_1h_tokens":20}`, kimiCacheHeaders("10", "20"), true},
		{"header_body_conflict", valid, kimiCacheHeaders("0", "30"), true},
	} {
		for _, route := range []string{"chat", "chat_to_responses"} {
			for _, stream := range []bool{false, true} {
				t.Run(tc.name+"/"+route+"/"+fmt.Sprint(stream), func(t *testing.T) {
					usage, c, info := runKimiCacheHeaderFixture(t, route, stream, tc.header, []string{tc.raw}, "kimi-k3", false)
					require.Equal(t, tc.pending, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
				})
			}
		}
	}
}
