package controller

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningAcceptanceLunaToolTransportPreservesIntentAndBilling(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				f := newZTAPIHealthE2EFixtureForModel(t, "zt-gpt-5.6-luna", "gpt-5.6-luna")
				f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
				item := gin.H{"id": "fc_echo", "type": "function_call", "call_id": "call_echo", "name": "audit_echo", "arguments": `{"text":"OK"}`, "status": "completed"}
				final := gin.H{"id": "resp_echo", "object": "response", "status": "completed", "output": []any{item}, "usage": gin.H{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15, "input_tokens_details": gin.H{"cached_tokens": 0}}}
				raw, err := common.Marshal(final)
				require.NoError(t, err)
				reply := ztapiE2EReply{status: 200, body: string(raw), stream: stream}
				if stream {
					event := func(kind string, payload gin.H) string {
						payload["type"] = kind
						encoded, marshalErr := common.Marshal(payload)
						require.NoError(t, marshalErr)
						return "event: " + kind + "\ndata: " + string(encoded) + "\n\n"
					}
					reply.body = event("response.output_item.added", gin.H{"output_index": 0, "item": gin.H{"id": "fc_echo", "type": "function_call", "call_id": "call_echo", "name": "audit_echo", "arguments": ""}}) +
						event("response.function_call_arguments.delta", gin.H{"item_id": "fc_echo", "output_index": 0, "delta": `{"text":"OK"}`}) +
						event("response.output_item.done", gin.H{"output_index": 0, "item": item}) + event("response.completed", gin.H{"response": final})
				}
				f.queue(reply)
				body := gin.H{"model": f.publicName, "stream": stream, "messages": []any{gin.H{"role": "user", "content": "Call audit_echo with text OK."}}}
				path := "/v1/chat/completions"
				if protocol == "chat" {
					body["reasoning_effort"] = "high"
					body["tools"] = []any{gin.H{"type": "function", "function": gin.H{"name": "audit_echo", "parameters": gin.H{"type": "object"}}}}
					body["tool_choice"] = gin.H{"type": "function", "function": gin.H{"name": "audit_echo"}}
				} else {
					path = "/v1/messages"
					body["max_tokens"] = 128
					body["output_config"] = gin.H{"effort": "high"}
					body["tools"] = []any{gin.H{"name": "audit_echo", "input_schema": gin.H{"type": "object"}}}
					body["tool_choice"] = gin.H{"type": "tool", "name": "audit_echo"}
				}
				response := f.postJSON(t, path, body)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				require.Equal(t, []string{"/v1/responses"}, f.upstreamPaths)
				require.EqualValues(t, 1, f.calls.Load())
				var sent map[string]any
				require.NoError(t, common.Unmarshal(f.upstreamBodies[0], &sent))
				require.Equal(t, f.sourceModel, sent["model"])
				require.Equal(t, "high", sent["reasoning"].(map[string]any)["effort"])
				require.Equal(t, "audit_echo", sent["tool_choice"].(map[string]any)["name"])
				f.assertSettlement(t, 0, model.ZTAPISettlementSettled, []int{1})
				f.assertPublishedPrice(t)
			})
		}
	}
}
