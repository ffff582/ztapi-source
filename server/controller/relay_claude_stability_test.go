package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func claudeStabilityChatToolReply(t *testing.T, round int) ztapiE2EReply {
	t.Helper()
	calls := make([]any, 0, 2)
	for index := 0; index < 2; index++ {
		calls = append(calls, map[string]any{
			"index": index, "id": fmt.Sprintf("call_%d_%d", round, index), "type": "function",
			"function": map[string]any{"name": "lookup", "arguments": fmt.Sprintf(`{"value":%d}`, round*10+index)},
		})
	}
	chunk, err := common.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": calls}}}})
	require.NoError(t, err)
	return ztapiE2EReply{status: 200, stream: true, body: "data: " + string(chunk) + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}}` + "\n\ndata: [DONE]\n\n"}
}

func TestClaudeStabilityContinuousRelay(t *testing.T) {
	for _, messages := range []bool{false, true} {
		name, path := "responses_via_chat", "/v1/responses"
		if messages {
			name, path = "messages_via_chat", "/v1/messages"
		}
		t.Run(name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
			if messages {
				f.router.POST(path, func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
			}
			history := []any{map[string]any{"role": "user", "content": "Complete the synthetic task using lookup twice in parallel, then repeat."}}
			for i := 0; i < 12; i++ {
				history = append(history, map[string]any{"role": "user", "content": strings.Repeat("synthetic history ", 80)},
					map[string]any{"role": "assistant", "content": "Acknowledged."})
			}
			schema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}, "required": []string{"value"}}
			tool := map[string]any{"type": "function", "name": "lookup", "parameters": schema}
			if messages {
				tool = map[string]any{"name": "lookup", "input_schema": schema}
			}
			request := func() map[string]any {
				body := map[string]any{"model": f.publicName, "stream": true, "tools": []any{tool}}
				if messages {
					body["messages"], body["max_tokens"] = history, 1024
				} else {
					body["input"], body["max_output_tokens"], body["store"] = history, 1024, false
				}
				return body
			}
			executed := map[string]bool{}
			for round := 1; round <= 2; round++ {
				f.queue(claudeStabilityChatToolReply(t, round))
				w := f.postJSON(t, path, request())
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.EqualValues(t, round, f.calls.Load(), "one upstream dispatch per client turn")
				var output []any
				if messages {
					byIndex := map[int]map[string]any{}
					for _, event := range parseZTAPISSE(t, w.Body.String()) {
						if event.name == "content_block_start" {
							block := event.data["content_block"].(map[string]any)
							if block["type"] == "tool_use" {
								byIndex[int(event.data["index"].(float64))] = block
							}
						}
						if event.name == "content_block_delta" {
							delta := event.data["delta"].(map[string]any)
							if delta["type"] == "input_json_delta" {
								block := byIndex[int(event.data["index"].(float64))]
								var input map[string]any
								require.NoError(t, common.UnmarshalJsonStr(delta["partial_json"].(string), &input))
								block["input"] = input
							}
						}
					}
					require.Contains(t, w.Body.String(), "event: message_stop")
					for index := 0; index < len(byIndex); index++ {
						output = append(output, byIndex[index])
					}
				} else {
					for _, event := range parseZTAPISSE(t, w.Body.String()) {
						if event.name == "response.completed" {
							output = event.data["response"].(map[string]any)["output"].([]any)
						}
					}
				}
				require.Len(t, output, 2, "both parallel calls must survive the real relay")
				results := make([]any, 0, 2)
				for index, item := range output {
					call := item.(map[string]any)
					id, _ := call["call_id"].(string)
					var input map[string]any
					if messages {
						id = call["id"].(string)
						input = call["input"].(map[string]any)
					} else {
						require.NoError(t, common.UnmarshalJsonStr(call["arguments"].(string), &input))
					}
					require.Equal(t, fmt.Sprintf("call_%d_%d", round, index), id)
					require.False(t, executed[id], "a local tool must not execute twice")
					executed[id] = true
					value := strconv.Itoa(int(input["value"].(float64)) + 1)
					if messages {
						results = append(results, map[string]any{"type": "tool_result", "tool_use_id": id, "content": value})
					} else {
						results = append(results, map[string]any{"type": "function_call_output", "call_id": id, "output": value})
					}
				}
				if messages {
					history = append(history, map[string]any{"role": "assistant", "content": output}, map[string]any{"role": "user", "content": results})
				} else {
					history = append(history, output...)
					history = append(history, results...)
				}
			}
			f.queue(ztapiE2EReply{status: 200, stream: true, body: `data: {"choices":[{"index":0,"delta":{"content":"ZTAPI_CONTINUOUS_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}` + "\n\ndata: [DONE]\n\n"})
			w := f.postJSON(t, path, request())
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "ZTAPI_CONTINUOUS_OK")
			require.Len(t, executed, 4)
			require.EqualValues(t, 3, f.calls.Load())
			var sent struct {
				Messages []struct {
					Role, Content string
					ToolCallID    string `json:"tool_call_id"`
				} `json:"messages"`
			}
			// Decode only the tool-result messages; user history may use content arrays.
			var raw map[string]any
			require.NoError(t, common.Unmarshal(f.upstreamBodies[2], &raw))
			toolResults := make([]any, 0, 4)
			for _, message := range raw["messages"].([]any) {
				if message.(map[string]any)["role"] == "tool" {
					toolResults = append(toolResults, message)
				}
			}
			encoded, err := common.Marshal(map[string]any{"messages": toolResults})
			require.NoError(t, err)
			require.NoError(t, common.Unmarshal(encoded, &sent))
			require.Len(t, sent.Messages, 4)
			for _, result := range sent.Messages {
				require.True(t, executed[result.ToolCallID])
				require.NotEmpty(t, result.Content)
			}
			if !messages {
				before := f.calls.Load()
				f.queue(ztapiE2EReply{status: 200, stream: true, body: `data: {"choices":[{"index":0,"delta":{"content":"partial after tools"}}]}` + "\n\n"},
					ztapiE2EReply{status: 200, stream: true, body: "data: [DONE]\n\n"})
				failed := f.postJSON(t, path, request())
				require.Contains(t, failed.Body.String(), "response.failed")
				require.NotContains(t, failed.Body.String(), "response.completed")
				require.Equal(t, before+1, f.calls.Load(), "partial output after tools must never redispatch")
				require.Len(t, executed, 4)
			}
		})
	}
}
