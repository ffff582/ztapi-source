package controller

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestZTAPICodexAdditionalToolsRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, custom := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream_%t_custom_%t", stream, custom), func(t *testing.T) {
				f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
				name, kind, outputKind, arguments := "read", "function", "function_call_output", `{}`
				if custom {
					name, kind, outputKind, arguments = "apply_patch", "custom", "custom_tool_call_output", `{"input":"*** Begin Patch\n*** End Patch"}`
				}
				sum := sha256.Sum256([]byte("functions\x00" + name))
				alias := fmt.Sprintf("ns_%x_%s", sum[:8], name)
				call := map[string]any{"id": "call_ns", "type": "function", "function": map[string]any{"name": alias, "arguments": arguments}}
				var first map[string]any
				if stream {
					call["index"] = 0
					first = map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": "tool_calls"}},
						"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28}}
				} else {
					first = map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "tool_calls": []any{call}}, "finish_reason": "tool_calls"}},
						"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28}}
				}
				encoded, err := common.Marshal(first)
				require.NoError(t, err)
				body := string(encoded)
				if stream {
					body = "data: " + body + "\n\ndata: [DONE]\n\n"
				}
				f.queue(ztapiE2EReply{status: 200, stream: stream, body: body})
				declarations := map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
					map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": kind, "name": name, "parameters": map[string]any{"type": "object"}}}},
				}}
				user := map[string]any{"role": "user", "content": "Use the tool."}
				request := map[string]any{"model": f.publicName, "store": false, "stream": stream, "max_output_tokens": 1024,
					"input": []any{declarations, user}, "tool_choice": map[string]any{"type": kind, "namespace": "functions", "name": name}}
				w := f.postJSON(t, "/v1/responses", request)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var result map[string]any
				if stream {
					for _, event := range parseZTAPISSE(t, w.Body.String()) {
						if event.name == "response.completed" {
							result = event.data["response"].(map[string]any)
						}
					}
				} else {
					require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
				}
				require.NotNil(t, result)
				output := result["output"].([]any)
				require.Len(t, output, 1)
				item := output[0].(map[string]any)
				require.Equal(t, name, item["name"])
				require.Equal(t, "functions", item["namespace"])
				require.Equal(t, "call_ns", item["call_id"])
				f.queue(ztapiE2EReply{status: 200, body: `{"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":2,"total_tokens":32}}`})
				followUp := map[string]any{"model": f.publicName, "stream": false, "max_output_tokens": 1024, "store": false,
					"input": []any{declarations, user, item, map[string]any{"type": outputKind, "call_id": "call_ns", "output": "Tool succeeded."}}}
				continued := f.postJSON(t, "/v1/responses", followUp)
				require.Equal(t, http.StatusOK, continued.Code, continued.Body.String())
				require.Contains(t, continued.Body.String(), "OK")
				f.mu.Lock()
				defer f.mu.Unlock()
				var sent map[string]any
				require.NoError(t, common.Unmarshal(f.upstreamBodies[1], &sent))
				messages := sent["messages"].([]any)
				historyCall := messages[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
				require.Equal(t, alias, historyCall["function"].(map[string]any)["name"])
				require.Equal(t, "call_ns", messages[2].(map[string]any)["tool_call_id"])
				require.EqualValues(t, 2, f.calls.Load())
			})
		}
	}
}

func TestZTAPICodexConfirmedUsageSettlesAfterTrailingStreamFailure(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	client := service.GetHttpClient()
	originalTransport := client.Transport
	client.Transport = codexTrailingFailureTransport{base: originalTransport}
	t.Cleanup(func() { client.Transport = originalTransport })
	f.queue(ztapiE2EReply{status: 200, stream: true, body: `data: {"choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}` + "\n\n"})
	w := f.postJSON(t, "/v1/responses", map[string]any{"model": f.publicName, "input": "hi", "stream": true, "max_output_tokens": 1024})
	require.Contains(t, w.Body.String(), "response.failed")
	require.NotContains(t, w.Body.String(), "response.completed")
	var settlement model.ZTAPIRequestSettlement
	require.NoError(t, f.db.Order("id DESC").Take(&settlement).Error)
	require.Equal(t, model.ZTAPISettlementSettled, settlement.Status)
	require.Positive(t, settlement.ChargedQuota)
	require.EqualValues(t, 1, f.balances(t).Logs)
	require.EqualValues(t, 1, f.calls.Load())
}

type codexTrailingFailureTransport struct{ base http.RoundTripper }

func (transport codexTrailingFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil {
		response.Body = codexTrailingFailureBody{ReadCloser: response.Body}
	}
	return response, err
}

type codexTrailingFailureBody struct{ io.ReadCloser }

func (body codexTrailingFailureBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
