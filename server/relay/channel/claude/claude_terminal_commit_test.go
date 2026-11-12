package claude

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func claudeStabilityOpenAITerminals(t *testing.T, body string) []string {
	t.Helper()
	var events []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			events = append(events, "DONE")
			continue
		}
		var frame struct {
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *dto.Usage `json:"usage"`
		}
		require.NoError(t, common.UnmarshalJsonStr(data, &frame))
		for _, choice := range frame.Choices {
			if choice.FinishReason != nil {
				events = append(events, "finish:"+*choice.FinishReason)
			}
		}
		if frame.Usage != nil {
			events = append(events, "usage")
		}
	}
	return events
}

func TestClaudeStabilityOpenAITerminalCommit(t *testing.T) {
	for _, tool := range []bool{false, true} {
		kind, finish, body := "text", "stop", claudeStabilityPartialSSE
		if tool {
			kind, finish = "tool", "tool_calls"
			body = strings.Replace(body,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_local","name":"lookup","input":{}}}`, 1)
			body = strings.Replace(body, `"stop_reason":"end_turn"`, `"stop_reason":"tool_use"`, 1)
		}
		for _, ending := range []string{"clean_eof", "unexpected_eof", "error_event", "stop_without_delta", "message_stop"} {
			t.Run(kind+"/"+ending, func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				upstream := body
				if ending == "message_stop" {
					upstream += "data: {\"type\":\"message_stop\"}\n\n"
				} else if ending == "stop_without_delta" {
					upstream = strings.Split(body, "data: {\"type\":\"message_delta\"")[0] + "data: {\"type\":\"message_stop\"}\n\n"
				} else if ending == "error_event" {
					upstream += "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"synthetic\"}}\n\n"
				}
				var reader io.Reader = strings.NewReader(upstream)
				if ending == "unexpected_eof" {
					reader = io.MultiReader(reader, claudeStabilityInterruptedReader{})
				}
				info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, IsStream: true,
					ShouldIncludeUsage: true, DisablePing: true,
					ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-local"}}
				_, apiErr := (&Adaptor{}).DoResponse(c, &http.Response{StatusCode: 200, Body: io.NopCloser(reader)}, info)
				if ending == "message_stop" {
					require.Nil(t, apiErr)
					require.Equal(t, []string{"finish:" + finish, "usage", "DONE"}, claudeStabilityOpenAITerminals(t, recorder.Body.String()))
				} else {
					require.NotNil(t, apiErr)
					require.True(t, types.IsSkipRetryError(apiErr))
					require.Empty(t, claudeStabilityOpenAITerminals(t, recorder.Body.String()), "incomplete streams must not expose any successful terminal frame")
				}
				if tool {
					require.Contains(t, recorder.Body.String(), "tool_local", "tool deltas must stay streaming")
				} else {
					require.Contains(t, recorder.Body.String(), "partial", "text deltas must stay streaming")
				}
			})
		}
	}
}

func TestClaudeStabilityAdaptorErrorRetainsMetering(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(format), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := strings.Replace(claudeStabilityPartialSSE, `"cache_creation_input_tokens":4`,
				`"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3},"provider_note":"SYNTHETIC_PRIVATE"`, 1)
			info := &relaycommon.RelayInfo{RelayFormat: format, IsStream: true, ShouldIncludeUsage: true, DisablePing: true,
				ChannelMeta:              &relaycommon.ChannelMeta{UpstreamModelName: "claude-local"},
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "text"}}
			_, apiErr := (&Adaptor{}).DoResponse(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, info)
			require.NotNil(t, apiErr)
			// Deliberately discard the returned usage, just as ClaudeHelper does on error.
			require.NotNil(t, info.ZTAPIMetering)
			require.Equal(t, 2, info.ZTAPIMetering.UsageObservations)
			require.Equal(t, string(types.RelayFormatClaude), info.ZTAPIMetering.UpstreamProtocol)
			require.Equal(t, "upstream_reported", info.ZTAPIMetering.UsageSource)
			require.False(t, info.ZTAPIMetering.UsageIntegrityComplete, "observation is not authorization to settle")
			require.NotEmpty(t, info.ZTAPIMetering.RawUsageSHA256)
			persistable := info.ZTAPIMetering.Sanitized()
			require.NotNil(t, persistable)
			var raw struct {
				CacheCreation map[string]int `json:"cache_creation"`
			}
			require.NoError(t, common.Unmarshal(persistable.RawUsage, &raw))
			var numeric map[string]any
			require.NoError(t, common.Unmarshal(persistable.RawUsage, &numeric))
			assert.EqualValues(t, 2, numeric["input_tokens"])
			assert.EqualValues(t, 7, numeric["output_tokens"])
			assert.EqualValues(t, 3, numeric["cache_read_input_tokens"])
			assert.EqualValues(t, 4, numeric["cache_creation_input_tokens"])
			assert.Equal(t, map[string]int{"ephemeral_5m_input_tokens": 1, "ephemeral_1h_input_tokens": 3}, raw.CacheCreation)
			require.NotContains(t, string(persistable.RawUsage), "SYNTHETIC_PRIVATE")
			if format == types.RelayFormatOpenAI {
				require.Empty(t, claudeStabilityOpenAITerminals(t, recorder.Body.String()))
			}
		})
	}
}

func TestClaudeStabilityBufferedUsagePreservesEarlierCacheTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	prior := &relaycommon.ZTAPIMeteringEvidence{Version: 1, Messages: 5, Tools: 2}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, IsStream: true, DisablePing: true,
		ChannelMeta:              &relaycommon.ChannelMeta{UpstreamModelName: "claude-local"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "text"}, ZTAPIMetering: prior}
	body := strings.Replace(claudeStabilityPartialSSE, `"cache_creation_input_tokens":4`,
		`"cache_creation_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3}`, 1)
	body = strings.Replace(body, `"usage":{"output_tokens":7}`, `"usage":{"output_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":1}}`, 1)
	_, apiErr := (&Adaptor{}).DoResponse(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, info)
	require.NotNil(t, apiErr)
	require.Same(t, prior, info.ZTAPIMetering, "do not replace request or provider evidence on failure")
	require.Equal(t, 5, prior.Messages)
	require.Equal(t, 2, prior.Tools)
	var numeric map[string]any
	require.NoError(t, common.Unmarshal(prior.RawUsage, &numeric))
	require.Equal(t, map[string]any{"ephemeral_5m_input_tokens": float64(1), "ephemeral_1h_input_tokens": float64(3)}, numeric["cache_creation"])
}

func TestClaudeStabilityNativeEventOrder(t *testing.T) {
	_, apiErr, recorder, _ := runClaudeStabilityStream(t, types.RelayFormatClaude,
		io.NopCloser(strings.NewReader(claudeStabilityPartialSSE+"data: {\"type\":\"message_stop\"}\n\n")))
	require.Nil(t, apiErr)
	var events []string
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	require.Equal(t, []string{"message_start", "content_block_delta", "message_delta", "message_stop"}, events)
}

func TestClaudeStabilitySharedFinalPreservesSDKCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, ShouldIncludeUsage: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-local"}}
	state := &ClaudeResponseInfo{Usage: &dto.Usage{}}
	// AWS SDK streaming calls the shared Data/Final functions without ClaudeStreamHandler.
	body := claudeStabilityPartialSSE + "data: {\"type\":\"message_stop\"}\n\n"
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			require.Nil(t, HandleStreamResponseData(c, info, state, strings.TrimPrefix(line, "data: ")))
		}
	}
	require.Empty(t, claudeStabilityOpenAITerminals(t, recorder.Body.String()))
	require.Nil(t, HandleStreamFinalResponse(c, info, state))
	require.Equal(t, []string{"finish:stop", "usage", "DONE"}, claudeStabilityOpenAITerminals(t, recorder.Body.String()))
}
