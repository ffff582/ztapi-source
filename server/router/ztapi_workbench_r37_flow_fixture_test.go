package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func writeR37FlowFixture(t *testing.T, w http.ResponseWriter, body []byte, id, model, endpoint string) {
	t.Helper()
	var request struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Errorf("invalid fixture body: %v", err)
		w.WriteHeader(400)
		return
	}
	wire := string(body)
	var name, arguments, callID, text string
	if strings.Contains(wire, "This is a small synthetic addition bug.") || strings.Contains(wire, "r37-short-child-") {
		switch {
		case len(request.Tools) == 0 && strings.Contains(wire, "r37-short-child-test"):
			text = "The test requires addition."
		case len(request.Tools) == 0 && strings.Contains(wire, "r37-short-child-code"):
			text = "The implementation subtracts."
		case strings.Contains(wire, "r37-short-run-test"):
			text = "The addition is fixed and the test passed."
		case strings.Contains(wire, "r37-short-edit"):
			name, arguments, callID = "run_project_test", `{"path":"calc.test.mjs"}`, "r37-short-run-test"
		case strings.Contains(wire, "r37-short-read"):
			name, arguments, callID = "stage_project_edit", `{"path":"calc.mjs","content":"export const add = (a, b) => a + b;\n"}`, "r37-short-edit"
		case strings.Contains(wire, "The implementation subtracts.") && strings.Contains(wire, "The test requires addition."):
			name, arguments, callID = "read_project_file", `{"path":"calc.mjs"}`, "r37-short-read"
		default:
			name, arguments, callID = "investigate_in_parallel", `{"tasks":[{"question":"r37-short-child-code","paths":["calc.mjs"]},{"question":"r37-short-child-test","paths":["calc.test.mjs"]}]}`, "r37-short-plan"
		}
	} else {
		nativeLong := strings.Contains(wire, "This is a long synthetic addition task.")
		complete := "fixture test passed"
		edit := `{"path":"calc.mjs","content":"fixture edit"}`
		if nativeLong {
			complete = `\"exitCode\":0`
			edit = `{"path":"calc.mjs","content":"export const add = (a, b) => a + b;\n"}`
		}
		switch {
		case len(request.Tools) == 0 && strings.Contains(wire, "r37-child-fixture"):
			if strings.Contains(wire, "r37-child-fixture-first") {
				text = "first finding"
			} else {
				text = "second finding"
			}
		case len(request.Tools) == 0:
			text = "fixture checkpoint"
		case strings.Contains(wire, complete):
			text = "fixture complete"
		case strings.Contains(wire, "r37-edit-call"):
			name, arguments, callID = "run_project_test", `{"path":"calc.test.mjs"}`, "r37-test-call"
		case strings.Contains(wire, "first finding") && strings.Contains(wire, "second finding"):
			name, arguments, callID = "stage_project_edit", edit, "r37-edit-call"
		case strings.Contains(wire, "fixture checkpoint"):
			name, arguments, callID = "investigate_in_parallel", `{"tasks":[{"question":"r37-child-fixture-first","paths":["calc.mjs"]},{"question":"r37-child-fixture-second","paths":["calc.test.mjs"]}]}`, "r37-investigate-call"
		default:
			index := 1
			if strings.Contains(wire, "r37-read-call-2") {
				index = 3
			} else if strings.Contains(wire, "r37-read-call-1") {
				index = 2
			}
			name, arguments, callID = "read_project_file", fmt.Sprintf(`{"path":"sample-%d.mjs"}`, index), fmt.Sprintf("r37-read-call-%d", index)
		}
	}
	write := func(event string, value any) {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Errorf("marshal fixture event: %v", err)
			return
		}
		if event != "" {
			_, _ = fmt.Fprintf(w, "event: %s\n", event)
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
	if endpoint == "/v1/chat/completions" {
		delta := map[string]any{"role": "assistant", "content": text}
		finish := "stop"
		if name != "" {
			delete(delta, "content")
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": callID, "type": "function",
				"function": map[string]string{"name": name, "arguments": arguments}}}
			finish = "tool_calls"
		}
		chunk := func(choice map[string]any, usage map[string]int) {
			body := map[string]any{"id": id, "object": "chat.completion.chunk", "model": model, "choices": []any{choice}}
			if usage != nil {
				body["usage"] = usage
			}
			write("", body)
		}
		chunk(map[string]any{"index": 0, "delta": delta}, nil)
		chunk(map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}, map[string]int{"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	output := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}}
	if name != "" {
		output = map[string]any{"type": "function_call", "id": "fc_" + callID, "name": name, "call_id": callID, "arguments": arguments, "status": "completed"}
	}
	write("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
		"id": id, "model": model, "status": "completed", "output": []any{output},
		"usage": map[string]int{"input_tokens": 12, "output_tokens": 3, "total_tokens": 15},
	}})
}
