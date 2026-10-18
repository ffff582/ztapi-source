package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

// ztapiCodexStyleRequest mirrors what Codex CLI sends: instructions, a
// developer turn, a prior shell call with its output, opaque reasoning, a
// freeform apply_patch tool and a hosted tool the supplier cannot run.
func ztapiCodexStyleRequest(modelName string, stream bool) map[string]any {
	text := func(kind, value string) []any { return []any{map[string]any{"type": kind, "text": value}} }
	return map[string]any{
		"model": modelName, "stream": stream, "store": false, "max_output_tokens": 256,
		"instructions": "You are Codex.", "include": []string{"reasoning.encrypted_content"},
		"prompt_cache_key": "local-session", "parallel_tool_calls": false, "tool_choice": "auto",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell", "description": "Run a command", "strict": false,
				"parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"command"}}},
			map[string]any{"type": "custom", "name": "apply_patch", "description": "Edit files",
				"format": map[string]any{"type": "grammar", "syntax": "lark", "definition": "start: begin_patch"}},
			map[string]any{"type": "web_search"},
		},
		"input": []any{
			map[string]any{"type": "message", "role": "developer", "content": text("input_text", "Sandbox: workspace-write.")},
			map[string]any{"type": "message", "role": "user", "content": text("input_text", "List the files.")},
			map[string]any{"type": "reasoning", "id": "rs_prior", "summary": []any{}, "encrypted_content": "opaque"},
			map[string]any{"type": "function_call", "call_id": "call_ls", "name": "shell", "arguments": `{"command":["ls"]}`},
			map[string]any{"type": "function_call_output", "call_id": "call_ls", "output": "main.go"},
			map[string]any{"type": "message", "role": "assistant", "content": text("output_text", "There is main.go.")},
			map[string]any{"type": "message", "role": "user", "content": text("input_text", "Fix it.")},
		},
	}
}

func (f *ztapiE2EFixture) postJSON(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := common.Marshal(body)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.key)
	r.Header.Set("User-Agent", "codex_cli_rs/0.99.0 (local e2e)")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

type ztapiSSEEvent struct {
	name string
	data map[string]any
}

func parseZTAPISSE(t *testing.T, body string) []ztapiSSEEvent {
	t.Helper()
	var events []ztapiSSEEvent
	for _, block := range strings.Split(body, "\n\n") {
		var event ztapiSSEEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(line, "data: "), &event.data), block)
			}
		}
		if event.name != "" {
			require.Equal(t, event.name, event.data["type"], "event name and payload type must agree")
			events = append(events, event)
		}
	}
	return events
}

func (f *ztapiE2EFixture) assertChatUpstreamRequest(t *testing.T, stream bool, maxTokens int) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, []string{"/v1/chat/completions"}, f.upstreamPaths, "a claude/kimi Responses request must reach the supplier as chat")
	var sent map[string]any
	require.NoError(t, common.Unmarshal(f.upstreamBodies[0], &sent))
	require.Equal(t, f.sourceModel, sent["model"])
	for _, field := range []string{"input", "instructions", "store", "include", "prompt_cache_key"} {
		require.NotContains(t, sent, field, "Responses-only field must not reach the chat supplier")
	}
	require.EqualValues(t, maxTokens, sent["max_tokens"])
	require.Equal(t, false, sent["parallel_tool_calls"])
	if stream {
		require.Equal(t, map[string]any{"include_usage": true}, sent["stream_options"], "managed billing needs supplier usage")
	}

	messages := sent["messages"].([]any)
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.(map[string]any)["role"].(string))
	}
	require.Equal(t, []string{"system", "system", "user", "assistant", "tool", "assistant", "user"}, roles)
	require.Equal(t, "You are Codex.", messages[0].(map[string]any)["content"])
	call := messages[3].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	require.Equal(t, "call_ls", call["id"])
	require.Equal(t, `{"command":["ls"]}`, call["function"].(map[string]any)["arguments"])
	require.Equal(t, map[string]any{"role": "tool", "tool_call_id": "call_ls", "content": "main.go"}, messages[4])

	tools := sent["tools"].([]any)
	require.Len(t, tools, 2, "the hosted web_search tool has no chat equivalent")
	patch := tools[1].(map[string]any)["function"].(map[string]any)
	require.Equal(t, "apply_patch", patch["name"])
	require.Contains(t, patch["description"], "start: begin_patch", "a freeform tool keeps its grammar")
	return sent
}

func TestZTAPIResponsesForChatOnlyModelStreamsThroughChat(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	chunk := func(delta string, finish string) string {
		finishJSON := "null"
		if finish != "" {
			finishJSON = `"` + finish + `"`
		}
		return `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"claude-sonnet-5","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finishJSON + `}]}` + "\n\n"
	}
	f.queue(ztapiE2EReply{status: 200, stream: true, body: chunk(`{"role":"assistant","reasoning_content":"Checking."}`, "") +
		chunk(`{"content":"Patching "}`, "") + chunk(`{"content":"now."}`, "") +
		chunk(`{"tool_calls":[{"index":0,"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":""}}]}`, "") +
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"*** Begin Patch"}}]}`, "") +
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\\n*** End Patch\"}"}}]}`, "") +
		chunk(`{}`, "tool_calls") +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"claude-sonnet-5","choices":[],"usage":{"prompt_tokens":40,"completion_tokens":12,"total_tokens":52}}` + "\n\n" +
		"data: [DONE]\n\n"})

	w := f.postJSON(t, "/v1/responses", ztapiCodexStyleRequest(f.publicName, true))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Header().Get("Content-Type"), "text/event-stream")
	f.assertChatUpstreamRequest(t, true, 1024)

	events := parseZTAPISSE(t, w.Body.String())
	require.GreaterOrEqual(t, len(events), 6)
	require.Equal(t, "response.created", events[0].name)
	require.Equal(t, "response.in_progress", events[1].name)
	require.Equal(t, "response.completed", events[len(events)-1].name)
	var deltas strings.Builder
	var done []map[string]any
	for i, event := range events {
		require.EqualValues(t, i, event.data["sequence_number"])
		switch event.name {
		case "response.output_text.delta":
			deltas.WriteString(event.data["delta"].(string))
		case "response.output_item.done":
			done = append(done, event.data["item"].(map[string]any))
		}
	}
	require.Equal(t, "Patching now.", deltas.String())
	require.Len(t, done, 3)
	require.Equal(t, "reasoning", done[0]["type"])
	require.Equal(t, "Checking.", done[0]["summary"].([]any)[0].(map[string]any)["text"])
	require.Equal(t, "message", done[1]["type"])
	require.Equal(t, "Patching now.", done[1]["content"].([]any)[0].(map[string]any)["text"])
	require.Equal(t, map[string]any{"type": "custom_tool_call", "id": done[2]["id"], "status": "completed",
		"call_id": "call_patch", "name": "apply_patch", "input": "*** Begin Patch\n*** End Patch"}, done[2])

	final := events[len(events)-1].data["response"].(map[string]any)
	require.Equal(t, "completed", final["status"])
	require.Equal(t, f.publicName, final["model"])
	require.Len(t, final["output"], 3)
	usage := final["usage"].(map[string]any)
	require.EqualValues(t, 40, usage["input_tokens"])
	require.EqualValues(t, 12, usage["output_tokens"])
	require.EqualValues(t, 52, usage["total_tokens"])

	var settlement model.ZTAPIRequestSettlement
	require.NoError(t, f.db.Order("id DESC").Take(&settlement).Error)
	require.Equal(t, model.ZTAPISettlementSettled, settlement.Status, "supplier usage must settle the managed charge")
	require.Positive(t, settlement.ChargedQuota)
	require.EqualValues(t, 1, f.balances(t).Logs)

	var consume model.Log
	require.NoError(t, f.db.Where("type = ?", model.LogTypeConsume).Take(&consume).Error)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(consume.Other, &other))
	require.Equal(t, "/v1/responses", other["request_path"], "the log must show which endpoint the client used")
	require.Equal(t, "codex_cli_rs/0.99.0 (local e2e)", other["client_user_agent"], "control characters are stripped")
}

func TestZTAPIResponsesForChatOnlyModelAnswersWithoutStreaming(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-kimi-k2.6", "kimi-k2.6")
	f.queue(ztapiE2EReply{status: 200, body: `{"id":"chatcmpl-2","object":"chat.completion","created":1,"model":"kimi-k2.6",` +
		`"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","reasoning_content":"Need a listing.",` +
		`"tool_calls":[{"id":"call_ls2","type":"function","function":{"name":"shell","arguments":"{\"command\":[\"ls\",\"-a\"]}"}}]}}],` +
		`"usage":{"prompt_tokens":30,"completion_tokens":9,"total_tokens":39}}`})

	w := f.postJSON(t, "/v1/responses", ztapiCodexStyleRequest(f.publicName, false))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	f.assertChatUpstreamRequest(t, false, 1024) // kimi reasons, so its small cap is raised

	var response map[string]any
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "response", response["object"])
	require.Equal(t, "completed", response["status"])
	output := response["output"].([]any)
	require.Len(t, output, 2)
	require.Equal(t, "reasoning", output[0].(map[string]any)["type"])
	call := output[1].(map[string]any)
	require.Equal(t, "function_call", call["type"])
	require.Equal(t, "call_ls2", call["call_id"])
	require.Equal(t, `{"command":["ls","-a"]}`, call["arguments"])
	require.EqualValues(t, 30, response["usage"].(map[string]any)["input_tokens"])

	var settlement model.ZTAPIRequestSettlement
	require.NoError(t, f.db.Order("id DESC").Take(&settlement).Error)
	require.Equal(t, model.ZTAPISettlementSettled, settlement.Status)
}

func TestZTAPIResponsesForChatOnlyModelRejectsServerSideStateBeforeDispatch(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	before := f.balances(t)
	body := ztapiCodexStyleRequest(f.publicName, false)
	body["previous_response_id"] = "resp_stored_elsewhere"

	w := f.postJSON(t, "/v1/responses", body)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "previous_response_id")
	require.Zero(t, f.calls.Load(), "an unservable request must not reach the supplier")
	after := f.balances(t)
	require.Equal(t, before.UserQuota, after.UserQuota, "the reservation must be returned")
	require.Equal(t, before.TokenRemain, after.TokenRemain)
}
