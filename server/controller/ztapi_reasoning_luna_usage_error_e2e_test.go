package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningLunaConfirmedUsageErrorsSettleExactlyOnce(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, outcome := range []string{"disconnect", "failed", "incomplete"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", protocol, stream, outcome), func(t *testing.T) {
					f := newZTAPIHealthE2EFixtureForModel(t, "zt-gpt-5.6-luna", "gpt-5.6-luna")
					f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
					before := f.balances(t)
					status := outcome
					if outcome == "disconnect" {
						status = "completed"
					}
					response := gin.H{
						"id": "resp_luna_usage", "object": "response", "status": status,
						"output": []any{gin.H{"id": "msg_luna_usage", "type": "message", "role": "assistant", "status": "completed", "content": []any{gin.H{"type": "output_text", "text": "partial", "annotations": []any{}}}}},
						"usage":  gin.H{"input_tokens": 20, "output_tokens": 2, "total_tokens": 22, "input_tokens_details": gin.H{"cached_tokens": 0}},
					}
					if outcome == "failed" {
						response["error"] = gin.H{"type": "server_error", "code": "fixture_failure", "message": "local fixture failure"}
					}
					if outcome == "incomplete" {
						response["incomplete_details"] = gin.H{"reason": "max_output_tokens"}
					}
					encoded, err := common.Marshal(response)
					require.NoError(t, err)
					reply := ztapiE2EReply{status: http.StatusOK, body: string(encoded), stream: stream}
					if stream {
						terminal, marshalErr := common.Marshal(gin.H{"type": "response." + status, "response": response})
						require.NoError(t, marshalErr)
						reply.body = "data: " + string(terminal) + "\n\n"
						if outcome != "disconnect" {
							reply.body = "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_luna_usage\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial\"}\n\n" + reply.body
						}
					}
					f.queue(reply)

					body := gin.H{"model": f.publicName, "stream": stream, "max_tokens": 1024, "messages": []any{gin.H{"role": "user", "content": "Call audit_echo with text OK."}}}
					path := "/v1/chat/completions"
					if protocol == "chat" {
						body["reasoning_effort"] = "high"
						body["tools"] = []any{gin.H{"type": "function", "function": gin.H{"name": "audit_echo", "parameters": gin.H{"type": "object"}}}}
						body["tool_choice"] = gin.H{"type": "function", "function": gin.H{"name": "audit_echo"}}
					} else {
						path = "/v1/messages"
						body["output_config"] = gin.H{"effort": "high"}
						body["tools"] = []any{gin.H{"name": "audit_echo", "input_schema": gin.H{"type": "object"}}}
						body["tool_choice"] = gin.H{"type": "tool", "name": "audit_echo"}
					}
					requestBody, err := common.Marshal(body)
					require.NoError(t, err)
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(requestBody)))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Authorization", "Bearer "+f.key)
					recorder := httptest.NewRecorder()
					var writer http.ResponseWriter = recorder
					var failedWriter *ztapiNativeDownstreamFailureWriter
					if outcome == "disconnect" {
						failedWriter = &ztapiNativeDownstreamFailureWriter{ResponseRecorder: recorder}
						writer = failedWriter
					}
					f.router.ServeHTTP(writer, request)

					require.Equal(t, []string{"/v1/responses"}, f.upstreamPaths)
					require.EqualValues(t, 1, f.calls.Load(), "an error after a confirmed receipt must not redispatch")
					var sent map[string]any
					require.NoError(t, common.Unmarshal(f.upstreamBodies[0], &sent))
					require.Equal(t, f.sourceModel, sent["model"])
					require.Equal(t, "high", sent["reasoning"].(map[string]any)["effort"])
					require.Equal(t, "audit_echo", sent["tool_choice"].(map[string]any)["name"])
					settlement := assertCodexConfirmedUsageChargedOnce(t, f, before)
					assertCodexConfirmedUsageReplayUnchanged(t, f, before, settlement)
					f.assertPublishedPrice(t)

					if outcome == "disconnect" {
						require.Positive(t, failedWriter.failures, "the real downstream writer must reject delivery after the terminal usage receipt was observed")
					} else if !stream {
						require.NotEqual(t, http.StatusOK, recorder.Code, recorder.Body.String())
					}
					require.False(t, ztapiLunaNormalSuccess(recorder.Body.String()), "failed, incomplete or undelivered output must not be reported as a normal completion")
				})
			}
		}
	}
}

func TestZTAPIReasoningLunaMessagesStreamIncludesTerminalUsage(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-gpt-5.6-luna", "gpt-5.6-luna")
	f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
	before := f.balances(t)
	f.queue(ztapiE2EReply{status: http.StatusOK, stream: true, body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_luna_complete\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_luna_complete\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":20,\"output_tokens\":2,\"total_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n"})
	response := f.postJSON(t, "/v1/messages", gin.H{
		"model": f.publicName, "stream": true, "max_tokens": 1024,
		"messages":      []any{gin.H{"role": "user", "content": "Reply exactly OK."}},
		"output_config": gin.H{"effort": "high"},
		"tools":         []any{gin.H{"name": "audit_echo", "input_schema": gin.H{"type": "object"}}},
		"tool_choice":   gin.H{"type": "auto"},
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, []string{"/v1/responses"}, f.upstreamPaths)
	settlement := assertCodexConfirmedUsageChargedOnce(t, f, before)
	assertCodexConfirmedUsageReplayUnchanged(t, f, before, settlement)
	require.Contains(t, response.Body.String(), `"text":"OK"`)
	require.Contains(t, response.Body.String(), `"type":"message_delta"`, "confirmed usage must be delivered with the normal Messages terminal")
	require.Contains(t, response.Body.String(), `"type":"message_stop"`, "a settled successful stream must actually complete for the client")
}

func TestZTAPIReasoningLunaMixedTextToolStreamKeepsToolFinishReason(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-gpt-5.6-luna", "gpt-5.6-luna")
	before := f.balances(t)
	item := gin.H{"id": "fc_luna_echo", "type": "function_call", "call_id": "call_luna_echo", "name": "audit_echo", "arguments": `{"text":"OK"}`, "status": "completed"}
	frames := []gin.H{
		{"type": "response.output_text.delta", "delta": "Calling the tool."},
		{"type": "response.output_item.added", "output_index": 1, "item": gin.H{"id": "fc_luna_echo", "type": "function_call", "call_id": "call_luna_echo", "name": "audit_echo", "arguments": ""}},
		{"type": "response.function_call_arguments.delta", "item_id": "fc_luna_echo", "output_index": 1, "delta": `{"text":"OK"}`},
		{"type": "response.output_item.done", "output_index": 1, "item": item},
		{"type": "response.completed", "response": gin.H{
			"id": "resp_luna_mixed", "status": "completed",
			"output": []any{gin.H{"id": "msg_luna_preface", "type": "message", "role": "assistant", "status": "completed", "content": []any{gin.H{"type": "output_text", "text": "Calling the tool.", "annotations": []any{}}}}, item},
			"usage":  gin.H{"input_tokens": 20, "output_tokens": 2, "total_tokens": 22, "input_tokens_details": gin.H{"cached_tokens": 0}},
		}},
	}
	var body strings.Builder
	for _, frame := range frames {
		encoded, err := common.Marshal(frame)
		require.NoError(t, err)
		body.WriteString("data: " + string(encoded) + "\n\n")
	}
	f.queue(ztapiE2EReply{status: http.StatusOK, stream: true, body: body.String()})
	response := f.postJSON(t, "/v1/chat/completions", gin.H{
		"model": f.publicName, "stream": true, "max_tokens": 1024, "reasoning_effort": "high",
		"messages":    []any{gin.H{"role": "user", "content": "Call audit_echo with text OK."}},
		"tools":       []any{gin.H{"type": "function", "function": gin.H{"name": "audit_echo", "parameters": gin.H{"type": "object"}}}},
		"tool_choice": gin.H{"type": "function", "function": gin.H{"name": "audit_echo"}},
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, []string{"/v1/responses"}, f.upstreamPaths)
	settlement := assertCodexConfirmedUsageChargedOnce(t, f, before)
	assertCodexConfirmedUsageReplayUnchanged(t, f, before, settlement)
	require.Contains(t, response.Body.String(), `"content":"Calling the tool."`)
	require.Contains(t, response.Body.String(), `"name":"audit_echo"`)
	require.Contains(t, response.Body.String(), `"finish_reason":"tool_calls"`, "a text preface must not hide the pending function call from the client")
	require.NotContains(t, response.Body.String(), `"finish_reason":"stop"`)
}

func TestZTAPIReasoningLunaNonStreamMixedTextAndToolsPreservesBothProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			f := newZTAPIHealthE2EFixtureForModel(t, "zt-gpt-5.6-luna", "gpt-5.6-luna")
			f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
			before := f.balances(t)
			encoded, err := common.Marshal(gin.H{
				"id": "resp_luna_nonstream_mixed", "object": "response", "status": "completed",
				"output": []any{
					gin.H{"id": "msg_luna_preface", "type": "message", "role": "assistant", "status": "completed", "content": []any{gin.H{"type": "output_text", "text": "Calling the tool.", "annotations": []any{}}}},
					gin.H{"id": "fc_luna_echo", "type": "function_call", "call_id": "call_luna_echo", "name": "audit_echo", "arguments": `{"text":"OK"}`, "status": "completed"},
				},
				"usage": gin.H{"input_tokens": 20, "output_tokens": 2, "total_tokens": 22, "input_tokens_details": gin.H{"cached_tokens": 0}},
			})
			require.NoError(t, err)
			f.queue(ztapiE2EReply{status: http.StatusOK, body: string(encoded)})
			body := gin.H{"model": f.publicName, "max_tokens": 1024, "messages": []any{gin.H{"role": "user", "content": "Call audit_echo with text OK."}}}
			path := "/v1/chat/completions"
			if protocol == "chat" {
				body["reasoning_effort"] = "high"
				body["tools"] = []any{gin.H{"type": "function", "function": gin.H{"name": "audit_echo", "parameters": gin.H{"type": "object"}}}}
				body["tool_choice"] = gin.H{"type": "function", "function": gin.H{"name": "audit_echo"}}
			} else {
				path = "/v1/messages"
				body["output_config"] = gin.H{"effort": "high"}
				body["tools"] = []any{gin.H{"name": "audit_echo", "input_schema": gin.H{"type": "object"}}}
				body["tool_choice"] = gin.H{"type": "tool", "name": "audit_echo"}
			}
			response := f.postJSON(t, path, body)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, []string{"/v1/responses"}, f.upstreamPaths)
			settlement := assertCodexConfirmedUsageChargedOnce(t, f, before)
			assertCodexConfirmedUsageReplayUnchanged(t, f, before, settlement)
			require.Contains(t, response.Body.String(), `"name":"audit_echo"`)
			if protocol == "chat" {
				require.Contains(t, response.Body.String(), `"content":"Calling the tool."`)
				require.Contains(t, response.Body.String(), `"finish_reason":"tool_calls"`)
			} else {
				require.Contains(t, response.Body.String(), `"text":"Calling the tool."`, "Messages conversion must preserve the assistant text beside tool_use")
				require.Contains(t, response.Body.String(), `"type":"tool_use"`)
				require.Contains(t, response.Body.String(), `"stop_reason":"tool_use"`)
			}
		})
	}
}

func ztapiLunaNormalSuccess(data string) bool {
	for _, line := range strings.Split(data, "\n") {
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "event: message_delta" || payload == "event: message_stop" {
			return true
		}
		if payload == "[DONE]" {
			return true
		}
		var response struct {
			Type       string  `json:"type"`
			StopReason *string `json:"stop_reason"`
			Delta      struct {
				StopReason *string `json:"stop_reason"`
			} `json:"delta"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if common.UnmarshalJsonStr(payload, &response) != nil {
			continue
		}
		if response.Type == "message_stop" || response.StopReason != nil || response.Delta.StopReason != nil {
			return true
		}
		for _, choice := range response.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				return true
			}
		}
	}
	return false
}
