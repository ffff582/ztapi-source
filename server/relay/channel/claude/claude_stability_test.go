package claude

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type claudeStabilityInterruptedReader struct{}

func (claudeStabilityInterruptedReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func runClaudeStabilityStream(t *testing.T, format types.RelayFormat, body io.ReadCloser) (*dto.Usage, *types.NewAPIError, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: format,
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-local"},
	}
	usage, apiErr := ClaudeStreamHandler(c, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       body,
	}, info)
	return usage, apiErr, recorder, info
}

func TestClaudeStabilityTerminalAndUsage(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(format), func(t *testing.T) {
			for _, tc := range []struct {
				name, body string
				failed     bool
			}{
				{"missing_stop", claudeStabilityPartialSSE, true},
				{"normal_stop", claudeStabilityPartialSSE + "data: {\"type\":\"message_stop\"}\n\n", false},
				{"upstream_error", claudeStabilityPartialSSE + "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"synthetic failure\"}}\n\n", true},
				{"malformed_event", claudeStabilityPartialSSE + "data: not-json\n\n", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					usage, apiErr, recorder, _ := runClaudeStabilityStream(t, format, io.NopCloser(strings.NewReader(tc.body)))
					if tc.failed {
						require.NotNil(t, apiErr)
						require.True(t, types.IsSkipRetryError(apiErr))
						require.NotContains(t, recorder.Body.String(), "data: [DONE]")
					} else {
						require.Nil(t, apiErr)
						if format == types.RelayFormatOpenAI {
							require.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
						}
					}
					require.NotNil(t, usage, "reported usage must survive a transport/protocol failure")
					require.Equal(t, 2, usage.PromptTokens)
					require.Equal(t, 7, usage.CompletionTokens)
					require.Equal(t, 3, usage.PromptTokensDetails.CachedTokens)
					require.Equal(t, 4, usage.PromptTokensDetails.CachedCreationTokens)
				})
			}
		})
	}
}

func TestClaudeStabilityToolUseWithoutText(t *testing.T) {
	body := strings.Replace(claudeStabilityPartialSSE,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_local","name":"lookup","input":{}}}`, 1)
	body = strings.Replace(body, `"stop_reason":"end_turn"`, `"stop_reason":"tool_use"`, 1)
	body += "data: {\"type\":\"message_stop\"}\n\n"
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(format), func(t *testing.T) {
			usage, apiErr, recorder, _ := runClaudeStabilityStream(t, format, io.NopCloser(strings.NewReader(body)))
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			require.Equal(t, 7, usage.CompletionTokens)
			require.Contains(t, recorder.Body.String(), "tool_local")
			require.NotContains(t, recorder.Body.String(), "partial")
		})
	}
}

func TestClaudeStabilityBareEOFAndScannerFailure(t *testing.T) {
	for _, body := range []string{"", strings.Split(claudeStabilityPartialSSE, "data: {\"type\":\"message_delta\"")[0]} {
		_, apiErr, recorder, _ := runClaudeStabilityStream(t, types.RelayFormatOpenAI, io.NopCloser(strings.NewReader(body)))
		require.NotNil(t, apiErr)
		require.NotContains(t, recorder.Body.String(), "data: [DONE]")
	}
	usage, apiErr, recorder, info := runClaudeStabilityStream(t, types.RelayFormatOpenAI,
		io.NopCloser(io.MultiReader(strings.NewReader(claudeStabilityPartialSSE), claudeStabilityInterruptedReader{})))
	require.NotNil(t, apiErr)
	require.Equal(t, relaycommon.StreamEndReasonScannerErr, info.StreamStatus.EndReason)
	require.NotContains(t, recorder.Body.String(), "data: [DONE]")
	require.NotNil(t, usage)
	require.Equal(t, 7, usage.CompletionTokens)
}

func TestClaudeStabilityStopDoesNotWaitForEOF(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 1
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, claudeStabilityPartialSSE+"data: {\"type\":\"message_stop\"}\n\n")
		written <- err
	}()
	usage, apiErr, _, info := runClaudeStabilityStream(t, types.RelayFormatClaude, reader)
	require.Nil(t, apiErr, "message_stop must end the stream even when upstream leaves its body open")
	require.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	require.NoError(t, <-written)
	require.Equal(t, 7, usage.CompletionTokens)
}

func TestClaudeStabilityTimeoutRetainsReportedUsage(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 1
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, claudeStabilityPartialSSE)
		written <- err
	}()
	usage, apiErr, recorder, info := runClaudeStabilityStream(t, types.RelayFormatOpenAI, reader)
	require.NotNil(t, apiErr)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.Equal(t, relaycommon.StreamEndReasonTimeout, info.StreamStatus.EndReason)
	require.NoError(t, <-written)
	require.NotContains(t, recorder.Body.String(), "data: [DONE]")
	require.NotNil(t, usage)
	require.Equal(t, 7, usage.CompletionTokens)
}

type claudeStabilityCancelWriter struct {
	gin.ResponseWriter
	cancel context.CancelFunc
	marker string
}

func (w *claudeStabilityCancelWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if strings.Contains(string(data), w.marker) {
		w.cancel()
	}
	return n, err
}

func TestClaudeStabilityClientCancellation(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name, marker := "after_partial", `"type":"message_delta"`
		body := claudeStabilityPartialSSE
		if terminal {
			name, marker = "at_stop", `"type":"message_stop"`
			body += "data: {\"type\":\"message_stop\"}\n\n"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			c.Writer = &claudeStabilityCancelWriter{ResponseWriter: c.Writer, cancel: cancel, marker: marker}
			info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, DisablePing: true,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-local"}}
			usage, apiErr := ClaudeStreamHandler(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, info)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			require.NotNil(t, apiErr, "cancellation must override an earlier scanner EOF/done")
			require.True(t, types.IsSkipRetryError(apiErr))
			require.NotNil(t, usage)
			require.Equal(t, 7, usage.CompletionTokens)
		})
	}
}

const claudeStabilityPartialSSE = `data: {"type":"message_start","message":{"id":"msg_local","model":"claude-local","usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}

`

func TestClaudeStabilityHTTP200MessageDeltaWithoutStop(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "clean_eof"
		if interrupted {
			name = "unexpected_eof"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			var reader io.Reader = strings.NewReader(claudeStabilityPartialSSE)
			if interrupted {
				reader = io.MultiReader(reader, claudeStabilityInterruptedReader{})
			}
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       io.NopCloser(reader),
			}
			info := &relaycommon.RelayInfo{
				RelayFormat: types.RelayFormatClaude,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-local"},
			}
			_, apiErr := ClaudeStreamHandler(c, resp, info)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "partial")
			require.NotNil(t, apiErr, "HTTP 200 without message_stop must not succeed")
		})
	}
}
