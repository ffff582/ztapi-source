package openai

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func runUsageIntegrityFixture(t *testing.T, route string, stream bool, usages []string) (*dto.Usage, *gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	c, _, info := usageRegressionContext(types.RelayFormatOpenAI, true)
	info.IsStream = stream
	var frames []string
	for _, usage := range usages {
		frame := `{"choices":[{"index":0,"delta":{},"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]`
		if route == "responses_to_chat" {
			frame = `{"status":"completed","output":[]`
		}
		if usage != "absent" {
			frame += `,"usage":` + usage
		}
		frame += `}`
		if stream && route == "responses_to_chat" {
			frame = `{"type":"response.completed","response":` + frame + `}`
		}
		frames = append(frames, frame)
	}
	body := frames[0]
	if stream {
		body = "data: " + strings.Join(frames, "\n\ndata: ") + "\n\ndata: [DONE]\n\n"
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	var usage *dto.Usage
	var apiErr *types.NewAPIError
	switch route {
	case "generic":
		if stream {
			usage, apiErr = OaiStreamHandler(c, info, resp)
		} else {
			usage, apiErr = OpenaiHandler(c, info, resp)
		}
	case "chat_to_responses":
		if stream {
			usage, apiErr = OaiChatToResponsesStreamHandler(c, info, resp, nil)
		} else {
			usage, apiErr = OaiChatToResponsesHandler(c, info, resp, nil)
		}
	case "responses_to_chat":
		if stream {
			usage, apiErr = OaiResponsesToChatStreamHandler(c, info, resp)
		} else {
			usage, apiErr = OaiResponsesToChatHandler(c, info, resp)
		}
	}
	require.Nil(t, apiErr)
	return usage, c, info
}

func usageIntegrityEvidence(t *testing.T, info *relaycommon.RelayInfo) []byte {
	t.Helper()
	require.NotNil(t, info.ZTAPIMetering)
	encoded, err := common.Marshal(info.ZTAPIMetering.Sanitized())
	require.NoError(t, err)
	return encoded
}

func TestUsageIntegrityStickyAcrossAllStreamEntrypoints(t *testing.T) {
	for _, route := range []string{"generic", "chat_to_responses", "responses_to_chat"} {
		for _, poison := range []struct{ name, raw, reason string }{
			{"negative", `{"input_tokens":-1,"output_tokens":2,"total_tokens":1}`, "usage_invalid_quantity"},
			{"wrong_type", `{"input_tokens":"PRIVATE_PROVIDER_TEXT","output_tokens":2}`, "usage_invalid_type"},
			{"alias_conflict", `{"prompt_tokens":100,"input_tokens":101,"completion_tokens":2,"output_tokens":2}`, "usage_alias_conflict"},
			{"partial", `{"output_tokens":3,"total_tokens":103}`, "usage_incomplete"},
			{"total_conflict", `{"input_tokens":100,"output_tokens":2,"total_tokens":999}`, "usage_total_mismatch"},
			{"semantic_conflict", `{"input_tokens":100,"output_tokens":2,"usage_semantic":"anthropic"}`, "usage_semantic_conflict"},
			{"regressing_snapshot", `{"input_tokens":99,"output_tokens":2,"total_tokens":101}`, "usage_snapshot_conflict"},
			{"cache_alias_conflict", `{"input_tokens":100,"output_tokens":2,"prompt_tokens_details":{"cached_tokens":20},"input_tokens_details":{"cached_tokens":80}}`, "usage_alias_conflict"},
			{"explicit_zero_cache_conflict", `{"input_tokens":100,"output_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"input_tokens_details":{"cached_tokens":80}}`, "usage_alias_conflict"},
		} {
			t.Run(route+"/"+poison.name, func(t *testing.T) {
				usage, c, info := runUsageIntegrityFixture(t, route, true, []string{
					`{"input_tokens":100,"output_tokens":2,"total_tokens":102}`,
					poison.raw,
					`{"input_tokens":100,"output_tokens":3,"total_tokens":103}`,
				})
				require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending, "a later valid snapshot cannot erase an anomaly")
				// Durable settlement validates with a new RelayInfo containing the frozen quote.
				frozenInfo := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: info.ZTAPIPublicationSnapshot}
				require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, frozenInfo, usage).Pending)
				evidence := usageIntegrityEvidence(t, info)
				require.Contains(t, gjson.GetBytes(evidence, "usage_integrity_reasons").Raw, poison.reason)
				require.NotContains(t, string(evidence), "PRIVATE_PROVIDER_TEXT")
				relaycommon.CaptureZTAPIOutboundRequestMetering(info, 123)
				require.Contains(t, string(usageIntegrityEvidence(t, info)), poison.reason, "dispatch must not reset request-level reasons")
			})
		}
	}
}

func TestUsageIntegrityPresenceAndEmptyStops(t *testing.T) {
	for _, route := range []string{"generic", "chat_to_responses", "responses_to_chat"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, raw string
				pending   bool
				prompt    int
			}{
				{"missing_input", `{"output_tokens":3,"total_tokens":103}`, true, 0},
				{"missing_output", `{"input_tokens":100}`, true, 0},
				{"explicit_zero_input", `{"input_tokens":0,"output_tokens":3,"total_tokens":3}`, false, 0},
				{"explicit_zero_output", `{"input_tokens":100,"output_tokens":0,"total_tokens":100}`, false, 100},
				{"explicit_all_zero", `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, false, 0},
				{"explicit_zero_alias_conflict", `{"prompt_tokens":0,"input_tokens":100,"completion_tokens":3}`, true, 0},
			} {
				name := route + "/nonstream/" + tc.name
				if stream {
					name = route + "/stream/" + tc.name
				}
				t.Run(name, func(t *testing.T) {
					usage, c, info := runUsageIntegrityFixture(t, route, stream, []string{tc.raw})
					result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
					require.Equal(t, tc.pending, result.Pending)
					if !tc.pending {
						require.Equal(t, tc.prompt, usage.PromptTokens)
						require.Equal(t, "upstream_reported", usage.UsageSource)
					}
				})
			}
		}
		for _, stop := range []string{"absent", "null", `{}`} {
			t.Run(route+"/empty_stop/"+stop, func(t *testing.T) {
				usage, c, info := runUsageIntegrityFixture(t, route, true, []string{`{"input_tokens":100,"output_tokens":2,"total_tokens":102}`, stop})
				require.False(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
				require.Equal(t, 100, usage.PromptTokens)
			})
		}
	}
}

func TestUsageIntegrityChatSemanticCannotBeProviderControlled(t *testing.T) {
	for _, route := range []string{"generic", "chat_to_responses", "responses_to_chat"} {
		t.Run(route, func(t *testing.T) {
			usage, c, info := runUsageIntegrityFixture(t, route, false, []string{`{"input_tokens":100,"output_tokens":2,"total_tokens":102,"prompt_tokens_details":{"cached_tokens":80},"usage_semantic":"anthropic"}`})
			info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "cache_read")
			info.ZTAPIPublicationSnapshot.SaleUSD["cache_read"] = "0.5"
			result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
			require.True(t, usage.UsageSemantic == "openai" || result.Pending, "Chat adapter must not authorize provider-selected Anthropic accounting")
			require.Contains(t, result.InvalidDimensions, "usage_semantic_conflict")
		})
	}
}

func TestUsageIntegrityNormalizeAuthoritativeChatSemantic(t *testing.T) {
	_, _, info := usageRegressionContext(types.RelayFormatOpenAI, true)
	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 2, UsageSemantic: "anthropic"}
	normalizeChatUsage(info, usage)
	require.Equal(t, "openai", usage.UsageSemantic)
}

func TestUsageIntegrityUnverifiedObservationSurvivesLaterValidUsage(t *testing.T) {
	for _, route := range []string{"generic", "chat_to_responses", "responses_to_chat"} {
		for _, validFirst := range []bool{false, true} {
			name := route + "/first_invalid"
			if validFirst {
				name = route + "/valid_then_invalid"
			}
			t.Run(name, func(t *testing.T) {
				frames := []string{`{"prompt_tokens":355000,"provider_note":"PRIVATE_PROVIDER_TEXT"}`, `{"input_tokens":100,"output_tokens":2,"total_tokens":102}`}
				if validFirst {
					frames = append([]string{`{"input_tokens":100,"output_tokens":1,"total_tokens":101}`}, frames...)
				}
				usage, c, info := runUsageIntegrityFixture(t, route, true, frames)
				require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
				evidence := usageIntegrityEvidence(t, info)
				observation := gjson.GetBytes(evidence, "unverified_usage_observation")
				require.True(t, observation.Exists(), "numeric evidence from an incomplete first snapshot must survive")
				require.Equal(t, "usage_incomplete", observation.Get("reason").String())
				require.Equal(t, "stream", observation.Get("location").String())
				raw := observation.Get("raw_numeric_usage").Raw
				require.JSONEq(t, `{"prompt_tokens":355000}`, raw)
				require.LessOrEqual(t, len(raw), 2048)
				require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(`{"prompt_tokens":355000}`))), observation.Get("raw_numeric_usage_sha256").String())
				require.NotContains(t, string(evidence), "PRIVATE_PROVIDER_TEXT")
				require.Contains(t, string(info.ZTAPIMetering.RawUsage), `"input_tokens":100`, "selected billing evidence must remain separate")
				relaycommon.CaptureZTAPIOutboundRequestMetering(info, 123)
				require.Equal(t, int64(355000), gjson.GetBytes(usageIntegrityEvidence(t, info), "unverified_usage_observation.raw_numeric_usage.prompt_tokens").Int())
			})
		}
	}
}
