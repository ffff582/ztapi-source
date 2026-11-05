package openai

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const translatedCompleted = `{"type":"response.completed","response":{"id":"resp_ok","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`
const translatedDelta = `{"type":"response.output_text.delta","delta":"OK"}`

func translatedFrames(frames ...string) string {
	return "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
}

type translatedBrokenWriter struct{ gin.ResponseWriter }

func (w translatedBrokenWriter) Write([]byte) (int, error)       { return 0, io.ErrClosedPipe }
func (w translatedBrokenWriter) WriteString(string) (int, error) { return 0, io.ErrClosedPipe }

type translatedScannerFailure struct{ *strings.Reader }

func (r translatedScannerFailure) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, errors.New("supplier connection lost")
	}
	return n, err
}

func TestZTAPIReasoningTranslatedResponsesRequiresValidTerminal(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		for name, frames := range map[string][]string{
			"bare_eof":                     {translatedDelta},
			"done_without_completion":      {translatedDelta, "[DONE]"},
			"malformed_then_completed":     {translatedDelta, "not-json", translatedCompleted},
			"incomplete":                   {translatedDelta, `{"type":"response.incomplete","response":{"status":"incomplete","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`},
			"failed":                       {translatedDelta, `{"type":"response.failed","response":{"status":"failed","error":{"type":"server_error","message":"failed"},"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`},
			"error":                        {translatedDelta, `{"type":"error","error":{"type":"server_error","message":"failed"}}`},
			"missing_completed_response":   {translatedDelta, `{"type":"response.completed"}`},
			"completed_with_failed_status": {translatedDelta, `{"type":"response.completed","response":{"status":"failed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`},
		} {
			t.Run(string(format)+"/"+name, func(t *testing.T) {
				c, recorder, info := usageRegressionContext(format, true)
				_, apiErr := OaiResponsesToChatStreamHandler(c, info, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(translatedFrames(frames...)))})
				require.NotNil(t, apiErr)
				require.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
				require.NotContains(t, recorder.Body.String(), `"finish_reason":"tool_calls"`)
				require.NotContains(t, recorder.Body.String(), "[DONE]")
				require.NotContains(t, recorder.Body.String(), `"type":"message_stop"`)
			})
		}
	}
}

func TestZTAPIReasoningTranslatedResponsesDrainsConfirmedUsageAfterWriteFailure(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		t.Run(string(format), func(t *testing.T) {
			c, recorder, info := usageRegressionContext(format, true)
			c.Writer = translatedBrokenWriter{c.Writer}
			usage, apiErr := OaiResponsesToChatStreamHandler(c, info, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(translatedFrames(translatedDelta, translatedCompleted)))})
			require.NotNil(t, apiErr)
			require.NotNil(t, usage)
			require.Equal(t, 10, usage.PromptTokens)
			require.Equal(t, 5, usage.CompletionTokens)
			require.Equal(t, "upstream_reported", usage.UsageSource)
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestZTAPIReasoningTranslatedResponsesScannerErrorNeverCompletes(t *testing.T) {
	c, recorder, info := usageRegressionContext(types.RelayFormatOpenAI, true)
	_, apiErr := OaiResponsesToChatStreamHandler(c, info, &http.Response{StatusCode: 200, Body: io.NopCloser(translatedScannerFailure{strings.NewReader(translatedFrames(translatedDelta))})})
	require.NotNil(t, apiErr)
	require.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestZTAPIReasoningTranslatedResponsesNonStreamRetainsUsageOnFailure(t *testing.T) {
	for _, brokenWriter := range []bool{false, true} {
		c, _, info := usageRegressionContext(types.RelayFormatOpenAI, true)
		status := "incomplete"
		if brokenWriter {
			c.Writer = translatedBrokenWriter{c.Writer}
			status = "completed"
		}
		body := `{"id":"resp_test","status":"` + status + `","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
		usage, apiErr := OaiResponsesToChatHandler(c, info, &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))})
		require.NotNil(t, apiErr)
		require.NotNil(t, usage)
		require.Equal(t, 10, usage.PromptTokens)
		require.Equal(t, 5, usage.CompletionTokens)
	}
}

func TestZTAPIReasoningTranslatedResponsesToolIdentityIsNotRepeatedInDeltas(t *testing.T) {
	c, recorder, info := usageRegressionContext(types.RelayFormatOpenAI, true)
	frames := []string{
		`{"type":"response.output_item.added","item":{"id":"fc_echo","type":"function_call","call_id":"call_echo","name":"audit_echo","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_echo","delta":"{\"text\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_echo","delta":"\"OK\"}"}`,
		`{"type":"response.output_item.done","item":{"id":"fc_echo","type":"function_call","call_id":"call_echo","name":"audit_echo","arguments":"{\"text\":\"OK\"}"}}`,
		translatedCompleted,
	}
	usage, apiErr := OaiResponsesToChatStreamHandler(c, info, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(translatedFrames(frames...)))})
	require.Nil(t, apiErr)
	require.Equal(t, 10, usage.PromptTokens)
	var id, name, arguments strings.Builder
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk dto.ChatCompletionsStreamResponse
		require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(line, "data: "), &chunk))
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				id.WriteString(call.ID)
				name.WriteString(call.Function.Name)
				arguments.WriteString(call.Function.Arguments)
			}
		}
	}
	require.Equal(t, "call_echo", id.String(), "clients concatenate delta strings; ID belongs only to the initial chunk")
	require.Equal(t, "audit_echo", name.String())
	require.JSONEq(t, `{"text":"OK"}`, arguments.String())
}
