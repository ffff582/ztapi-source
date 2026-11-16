package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPIMeteringResponsesCompatibilityCapturesProviderCounters(t *testing.T) {
	chat := `{"id":"offline-chat","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":2,"total_tokens":102}}`
	responses := `{"id":"offline-response","object":"response","model":"test","status":"completed","output":[{"id":"msg_offline","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":100,"output_tokens":2,"total_tokens":102}}`
	for _, source := range []string{"chat", "responses"} {
		t.Run(source, func(t *testing.T) {
			c, _, info := usageRegressionContext(types.RelayFormatOpenAI, true)
			body := chat
			if source == "responses" {
				body = responses
			}
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
			if source == "chat" {
				usage, apiErr := OaiChatToResponsesHandler(c, info, resp, nil)
				require.Nil(t, apiErr)
				require.Equal(t, "upstream_reported", usage.UsageSource)
			} else {
				usage, apiErr := OaiResponsesToChatHandler(c, info, resp)
				require.Nil(t, apiErr)
				require.Equal(t, "upstream_reported", usage.UsageSource)
			}
			require.NotNil(t, info.ZTAPIMetering)
			require.NotEmpty(t, info.ZTAPIMetering.RawUsage)
		})
	}
}

func TestZTAPIMeteringChatToResponsesStreamCapturesLastUsage(t *testing.T) {
	c, _, info := usageRegressionContext(types.RelayFormatOpenAI, true)
	body := "data: " + `{"id":"offline-chat","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"input_tokens":100,"output_tokens":2,"total_tokens":102}}` + "\n\ndata: [DONE]\n\n"
	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil)
	require.Nil(t, apiErr)
	require.Equal(t, 100, usage.PromptTokens)
	require.Equal(t, "upstream_reported", usage.UsageSource)
	require.NotNil(t, info.ZTAPIMetering)
}

func TestZTAPIMeteringResponsesToChatStreamCapturesUsage(t *testing.T) {
	c, _, info := usageRegressionContext(types.RelayFormatOpenAI, true)
	body := "data: " + `{"type":"response.completed","response":{"id":"offline-response","model":"test","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":2,"total_tokens":102}}}` + "\n\ndata: [DONE]\n\n"
	usage, apiErr := OaiResponsesToChatStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, apiErr)
	require.Equal(t, 100, usage.PromptTokens)
	require.Equal(t, "upstream_reported", usage.UsageSource)
	require.NotNil(t, info.ZTAPIMetering)
	require.Contains(t, string(info.ZTAPIMetering.RawUsage), `"input_tokens":100`)
}
