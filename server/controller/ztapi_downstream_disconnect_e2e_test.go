package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestZTAPIDownstreamFailureAfterConfirmedUsageBillsExactlyOnce(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		name := "write_failure"
		if cancelRequest {
			name = "write_failure_and_cancel"
		}
		t.Run(name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
			before := f.balances(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := &ztapiDownstreamFailureWriter{ResponseRecorder: httptest.NewRecorder()}
			if cancelRequest {
				writer.cancel = cancel
			}
			f.queue(ztapiE2EReply{status: http.StatusOK, stream: true, body: `data: {"choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}` + "\n\ndata: [DONE]\n\n"})
			serveCodexDownstreamRequest(t, f, ctx, writer, map[string]any{"model": f.publicName, "input": "hi", "stream": true, "max_output_tokens": 1024})
			require.Positive(t, writer.failures, "the real Relay response writer must actually fail")
			if cancelRequest {
				require.ErrorIs(t, ctx.Err(), context.Canceled)
			} else {
				require.NoError(t, ctx.Err(), "write failure must also be tested independently of cancellation")
			}
			var terminal struct {
				Type     string `json:"type"`
				Response struct {
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
						TotalTokens  int `json:"total_tokens"`
					} `json:"usage"`
				} `json:"response"`
			}
			require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(writer.failedData, "data: "), &terminal))
			require.Equal(t, "response.completed", terminal.Type)
			require.Equal(t, 20, terminal.Response.Usage.InputTokens, "usage must already be parsed before the injected failure")
			require.Equal(t, 2, terminal.Response.Usage.OutputTokens)
			require.Equal(t, 22, terminal.Response.Usage.TotalTokens)
			require.Contains(t, writer.Body.String(), `"type":"response.output_text.delta"`, "some output must be delivered before the terminal write fails")
			require.NotContains(t, writer.Body.String(), `"type":"response.completed"`, "a terminal frame rejected by the writer was not delivered")
			settlement := assertCodexConfirmedUsageChargedOnce(t, f, before)
			assertCodexConfirmedUsageReplayUnchanged(t, f, before, settlement)
		})
	}
}

func TestZTAPINativeResponsesFailureAfterConfirmedUsageBillsExactlyOnce(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			before := f.balances(t)
			body := `{"id":"resp_native","object":"response","status":"completed","output":[],"usage":{"input_tokens":20,"output_tokens":2,"total_tokens":22}}`
			if stream {
				body = "data: {\"type\":\"response.completed\",\"response\":" + body + "}\n\n"
			}
			f.queue(ztapiE2EReply{status: http.StatusOK, stream: stream, body: body})
			writer := &ztapiNativeDownstreamFailureWriter{ResponseRecorder: httptest.NewRecorder()}
			serveCodexDownstreamRequest(t, f, context.Background(), writer, map[string]any{"model": f.publicName, "input": "hi", "stream": stream, "max_output_tokens": 1024})
			require.Positive(t, writer.failures)
			settlement := assertCodexConfirmedUsageChargedOnce(t, f, before)
			assertCodexConfirmedUsageReplayUnchanged(t, f, before, settlement)
		})
	}
}

type ztapiNativeDownstreamFailureWriter struct {
	*httptest.ResponseRecorder
	failures int
}

func TestZTAPINativeResponsesFailedHostedCallWithoutQuoteStaysPending(t *testing.T) {
	f := newZTAPIHealthE2EFixture(t)
	before := f.balances(t)
	f.queue(ztapiE2EReply{status: http.StatusOK, body: `{"id":"resp_hosted","object":"response","status":"incomplete","output":[{"id":"fs_1","type":"file_search_call","status":"completed"}],"usage":{"input_tokens":20,"output_tokens":2,"total_tokens":22}}`})
	writer := httptest.NewRecorder()
	serveCodexDownstreamRequest(t, f, context.Background(), writer, map[string]any{"model": f.publicName, "input": "hi", "max_output_tokens": 1024})
	var row model.ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&row).Error)
	require.Equal(t, model.ZTAPISettlementPending, row.Status)
	require.Contains(t, row.MissingDimensionsJSON, "file_search")
	require.Zero(t, row.ChargedQuota)
	require.Equal(t, before.Logs, f.balances(t).Logs)
}

func (writer *ztapiNativeDownstreamFailureWriter) Write(data []byte) (int, error) {
	writer.failures++
	return 0, io.ErrClosedPipe
}

func (writer *ztapiNativeDownstreamFailureWriter) WriteString(data string) (int, error) {
	return writer.Write([]byte(data))
}

func TestZTAPIDownstreamFailureInvalidRequestNeverDispatchesOrRetainsDebit(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	before := f.balances(t)
	writer := &ztapiDownstreamFailureWriter{ResponseRecorder: httptest.NewRecorder()}
	f.queue(ztapiE2EReply{status: http.StatusOK, stream: true, body: `data: {"choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}` + "\n\ndata: [DONE]\n\n"})
	serveCodexDownstreamRequest(t, f, context.Background(), writer, map[string]any{
		"model": f.publicName, "input": "hi", "stream": true, "max_output_tokens": 1024,
		"tools": []any{map[string]any{"type": "web_search"}},
	})
	require.Equal(t, http.StatusBadRequest, writer.Code, writer.Body.String())
	require.Contains(t, writer.Body.String(), "tools:")
	require.Zero(t, writer.failures, "invalid semantics must fail before any completed response is attempted")
	after := f.balances(t)
	require.Equal(t, before.Calls, after.Calls)
	require.Equal(t, before.Attempts, after.Attempts)
	require.Equal(t, before.UserQuota, after.UserQuota)
	require.Equal(t, before.TokenRemain, after.TokenRemain)
	require.Equal(t, before.TokenUsed, after.TokenUsed)
	require.Equal(t, before.Logs, after.Logs)
	var settlements []model.ZTAPIRequestSettlement
	require.NoError(t, f.db.Find(&settlements).Error)
	for _, row := range settlements {
		require.Equal(t, model.ZTAPISettlementReleased, row.Status)
		require.False(t, row.Dispatched)
		require.Zero(t, row.ChargedQuota)
		require.Zero(t, row.TokenChargedQuota)
	}
	var ledger []model.BalanceLedger
	require.NoError(t, f.db.Find(&ledger).Error)
	var net int64
	for _, entry := range ledger {
		net += entry.Delta
	}
	require.Zero(t, net)
}

func serveCodexDownstreamRequest(t *testing.T, f *ztapiE2EFixture, ctx context.Context, writer http.ResponseWriter, body any) {
	t.Helper()
	encoded, err := common.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(encoded))).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+f.key)
	f.router.ServeHTTP(writer, request)
}

type ztapiDownstreamFailureWriter struct {
	*httptest.ResponseRecorder
	cancel     context.CancelFunc
	failedData string
	failures   int
}

func (writer *ztapiDownstreamFailureWriter) Write(data []byte) (int, error) {
	// Fail only on the terminal payload, after the real scanner has parsed usage.
	// Sticky failure models a closed downstream; no timing or sleeps are involved.
	if writer.failedData == "" && strings.HasPrefix(string(data), "data: ") {
		var event struct {
			Type string `json:"type"`
		}
		if common.Unmarshal(data[len("data: "):], &event) == nil && event.Type == "response.completed" {
			writer.failedData = string(data)
			if writer.cancel != nil {
				writer.cancel()
			}
		}
	}
	if writer.failedData != "" {
		writer.failures++
		return 0, io.ErrClosedPipe
	}
	return writer.ResponseRecorder.Write(data)
}

func (writer *ztapiDownstreamFailureWriter) WriteString(data string) (int, error) {
	return writer.Write([]byte(data))
}
