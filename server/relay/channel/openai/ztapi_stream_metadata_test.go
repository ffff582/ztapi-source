package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runZTAPIStreamFixture(t *testing.T, frames []string, managed, includeUsage bool) (string, *dto.Usage) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		StartTime: time.Unix(1790852400, 0), OriginModelName: "zt-claude-sonnet-5",
		RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI,
		IsStream: true, DisablePing: true, ShouldIncludeUsage: includeUsage,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-5-hc"},
	}
	if managed {
		info.ZTAPIPublicationSnapshot = &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: info.OriginModelName, Modality: "text",
		}
	}
	var body strings.Builder
	for _, frame := range frames {
		body.WriteString("data: " + frame + "\n\n")
	}
	body.WriteString("data: [DONE]\n\n")
	usage, apiErr := OaiStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body.String())),
	})
	require.Nil(t, apiErr)
	return recorder.Body.String(), usage
}

func streamFixtureFrames(t *testing.T, body string) []map[string]interface{} {
	t.Helper()
	var frames []map[string]interface{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var frame map[string]interface{}
		require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(line, "data: "), &frame))
		frames = append(frames, frame)
	}
	return frames
}

func TestZTAPIStreamMetadataMissingProviderFields(t *testing.T) {
	// Captured Sonnet wire shape, replayed offline: no upstream identity fields.
	frames := []string{
		`{"choices":[{"delta":{"role":"assistant"},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}`,
		`{"choices":[{"delta":{"content":"**"},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}`,
		`{"choices":[{"delta":{"content":"35 + 42 = "},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}`,
		`{"choices":[{"delta":{"content":"77**"},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}`,
		`{"choices":[{"delta":{},"finish_reason":"stop","index":0}],"object":"chat.completion.chunk"}`,
		`{"choices":[],"object":"chat.completion.chunk","usage":{"completion_tokens":13,"prompt_tokens":53,"prompt_tokens_details":{"cached_tokens":0},"total_tokens":66}}`,
	}
	body, usage := runZTAPIStreamFixture(t, frames, true, true)
	got := streamFixtureFrames(t, body)
	require.Len(t, got, 6)
	for i, frame := range got {
		id, ok := frame["id"].(string)
		require.True(t, ok && strings.HasPrefix(id, "chatcmpl-ztapi-"), "frame %d lacks a client response id", i)
		require.Equal(t, got[0]["id"], frame["id"])
		require.Equal(t, "zt-claude-sonnet-5", frame["model"])
		require.Equal(t, float64(1790852400), frame["created"])
		require.Equal(t, "chat.completion.chunk", frame["object"])
	}
	require.Contains(t, body, "35 + 42 = ")
	require.Contains(t, body, "77**")
	require.Equal(t, 53, usage.PromptTokens)
	require.Equal(t, 13, usage.CompletionTokens)
	require.Equal(t, 66, usage.TotalTokens)
	require.Equal(t, 1, strings.Count(body, "data: [DONE]"))
}

func TestZTAPIStreamMetadataPreservesProviderFields(t *testing.T) {
	frame := `{"id":"provider-id","model":"provider-model","created":1234,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"apply_patch","arguments":"{}"}}],"vendor_delta":{"x":1}},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":53,"completion_tokens":13,"total_tokens":66},"provider_specific_fields":{"request_id":"upstream-1","integer":9007199254740993}}`
	body, usage := runZTAPIStreamFixture(t, []string{frame}, true, true)
	require.Contains(t, body, "data: "+frame+"\n")
	require.Equal(t, 66, usage.TotalTokens)
}

func TestZTAPIStreamMetadataPreservesUnknownFieldsWhileFilling(t *testing.T) {
	frame := `{"id":null,"model":"","created":0,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"apply_patch","arguments":"{}"}}],"vendor_delta":{"x":1}},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":53,"completion_tokens":13,"total_tokens":66},"provider_specific_fields":{"request_id":"upstream-1","integer":9007199254740993}}`
	body, usage := runZTAPIStreamFixture(t, []string{frame}, true, true)
	got := streamFixtureFrames(t, body)
	require.Len(t, got, 1)
	require.NotEmpty(t, got[0]["id"])
	require.Equal(t, "zt-claude-sonnet-5", got[0]["model"])
	require.Contains(t, body, `9007199254740993`)
	require.Contains(t, body, `"vendor_delta":{"x":1}`)
	require.Contains(t, body, `"name":"apply_patch"`)
	require.Contains(t, body, `"request_id":"upstream-1"`)
	require.Equal(t, 66, usage.TotalTokens)
}

func TestZTAPIStreamMetadataKeepsUnmanagedPassthrough(t *testing.T) {
	frame := `{"choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"object":"chat.completion.chunk","usage":{"prompt_tokens":53,"completion_tokens":13,"total_tokens":66}}`
	body, _ := runZTAPIStreamFixture(t, []string{frame}, false, true)
	require.Contains(t, body, "data: "+frame+"\n")
	require.NotContains(t, body, "chatcmpl-ztapi-")
}

func TestZTAPIStreamMetadataReusesProviderIdentityForUsage(t *testing.T) {
	frames := []string{
		`{"id":"provider-id","model":"provider-model","created":1234,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}]}`,
		`{"choices":[],"object":"chat.completion.chunk","usage":{"prompt_tokens":53,"completion_tokens":13,"total_tokens":66}}`,
	}
	body, _ := runZTAPIStreamFixture(t, frames, true, true)
	got := streamFixtureFrames(t, body)
	require.Len(t, got, 2)
	require.Equal(t, "provider-id", got[1]["id"])
	require.Equal(t, "provider-model", got[1]["model"])
	require.Equal(t, float64(1234), got[1]["created"])
}

func TestZTAPIStreamMetadataScopeAndNonChatFrames(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "zt-claude-sonnet-5", RelayMode: relayconstant.RelayModeChatCompletions,
		RelayFormat:              types.RelayFormatOpenAI,
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "text"},
	}
	for _, frame := range []string{`{"error":{"message":"unavailable","code":"provider_error"}}`, `{"choices":null}`} {
		got, err := newZTAPIStreamMetadata(info).normalize(frame)
		require.NoError(t, err)
		require.Equal(t, frame, got)
	}
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatGemini} {
		info.RelayFormat = format
		require.Nil(t, newZTAPIStreamMetadata(info))
	}
	info.RelayFormat = types.RelayFormatOpenAI
	info.RelayMode = relayconstant.RelayModeCompletions
	require.Nil(t, newZTAPIStreamMetadata(info))
	require.Nil(t, newZTAPIStreamMetadata(nil))
}
