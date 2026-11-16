package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/openaicompat"
	"github.com/stretchr/testify/require"
)

// Exercise the real Responses ingress and adapter, not manually constructed
// RelayInfo. The fixture permits only its loopback supplier and real test ledger.
func TestZTAPIResponsesParallelToolHistoryReachesSupplierAsCompletePairs(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
			if stream {
				f.queue(ztapiE2EReply{status: 200, stream: true, body: `data: {"id":"parallel-ok","object":"chat.completion.chunk","model":"claude-sonnet-5","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}` + "\n\n" +
					`data: {"id":"parallel-ok","object":"chat.completion.chunk","model":"claude-sonnet-5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":2,"total_tokens":42}}` + "\n\ndata: [DONE]\n\n"})
			} else {
				f.queue(ztapiE2EReply{status: 200, body: `{"id":"parallel-ok","object":"chat.completion","model":"claude-sonnet-5","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":40,"completion_tokens":2,"total_tokens":42}}`})
			}
			input := []any{map[string]any{"role": "user", "content": "Use the three completed results."}}
			ids := []string{"call_search", "call_open", "call_find"}
			for _, id := range ids {
				input = append(input, map[string]any{"type": "function_call", "call_id": id, "name": "lookup", "arguments": `{"action":"` + id + `"}`})
			}
			// Completion arrival order need not match the assistant's call order.
			for _, id := range []string{ids[2], ids[0], ids[1]} {
				input = append(input, map[string]any{"type": "function_call_output", "call_id": id, "output": "evidence:" + id})
			}
			request := map[string]any{
				"model": f.publicName, "stream": stream, "store": false,
				"instructions": "Keep all tool evidence.", "max_output_tokens": 1024,
				"parallel_tool_calls": true, "tool_choice": "auto", "input": input,
				"tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}}}}},
			}
			w := f.postJSON(t, "/v1/responses", request)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			f.mu.Lock()
			paths := append([]string(nil), f.upstreamPaths...)
			bodies := append([][]byte(nil), f.upstreamBodies...)
			f.mu.Unlock()
			require.Equal(t, []string{"/v1/chat/completions"}, paths)
			require.Len(t, bodies, 1)
			var sent dto.GeneralOpenAIRequest
			require.NoError(t, common.Unmarshal(bodies[0], &sent))
			require.NoError(t, openaicompat.ValidateChatToolHistory(sent.Messages))
			require.Equal(t, f.sourceModel, sent.Model)
			require.NotNil(t, sent.ParallelTooCalls)
			require.True(t, *sent.ParallelTooCalls)
			require.Len(t, sent.Messages, 8)
			require.Equal(t, "system", sent.Messages[0].Role)
			require.Equal(t, "Keep all tool evidence.", sent.Messages[0].Content)
			require.Equal(t, "user", sent.Messages[1].Role)
			for i, id := range ids {
				assistant, result := sent.Messages[2+i*2], sent.Messages[3+i*2]
				require.Equal(t, "assistant", assistant.Role)
				var calls []dto.ToolCallRequest
				require.NoError(t, common.Unmarshal(assistant.ToolCalls, &calls))
				require.Len(t, calls, 1, "all results must remain immediately paired on the actual supplier wire")
				require.Equal(t, id, calls[0].ID)
				require.Equal(t, `{"action":"`+id+`"}`, calls[0].Function.Arguments)
				require.Equal(t, "tool", result.Role)
				require.Equal(t, id, result.ToolCallId)
				require.Equal(t, "evidence:"+id, result.Content)
			}
			require.Contains(t, w.Body.String(), "OK")
			if stream {
				events := parseZTAPISSE(t, w.Body.String())
				require.Equal(t, "response.completed", events[len(events)-1].name)
			}
			var settlements []model.ZTAPIRequestSettlement
			require.NoError(t, f.db.Find(&settlements).Error)
			require.Len(t, settlements, 1)
			require.Equal(t, model.ZTAPISettlementSettled, settlements[0].Status)
			require.Positive(t, settlements[0].ChargedQuota)
			require.EqualValues(t, 1, f.balances(t).Logs)
			require.NotContains(t, strings.ToLower(w.Body.String()), "tool_use_result_mismatch")
		})
	}
}
