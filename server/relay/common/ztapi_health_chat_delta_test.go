package common

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

func TestZTAPIHealthChatStreamDeltaNotShadowedByMessage(t *testing.T) {
	for _, message := range []string{`null`, `{}`, `{"role":"assistant","content":""}`, `{"content":"77"}`} {
		t.Run(message, func(t *testing.T) {
			wire := fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"message\":%s,\"delta\":{\"content\":\"7\"}}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"message\":%s,\"delta\":{\"content\":\"7\"},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":14,\"completion_tokens\":13,\"total_tokens\":27}}\n\n"+
				"data: [DONE]\n\n", message, message)
			out := healthFixture(t, "chat", "probe", wire, true, 200)
			if out.Result != "success" || out.Reason != "probe_valid_output" || !out.HasText || !out.TransportComplete {
				t.Fatalf("complete delta answer 77 was misclassified: %+v", out)
			}
			if out.UpstreamRequestID != "upstream-123" {
				t.Fatal("upstream request ID lost")
			}
		})
	}
}

func TestZTAPIHealthChatStreamDeltaSafetyAndTerminal(t *testing.T) {
	frame := func(message, delta, finish string) string {
		return fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"message\":%s,\"delta\":%s,\"finish_reason\":%s}]}\n\n", message, delta, finish)
	}
	const done = "data: [DONE]\n\n"
	for _, tt := range []struct{ name, wire, reason string }{
		{"wrong delta cannot borrow correct snapshot", frame(`{"content":"77"}`, `{"content":"76"}`, `"stop"`) + done, "probe_incorrect_output"},
		{"empty delta cannot borrow correct snapshot", frame(`{"content":"77"}`, `{}`, `"stop"`) + done, "probe_incorrect_output"},
		{"delta refusal not hidden", frame(`{}`, `{"content":"77","refusal":"RAW_SECRET"}`, `"stop"`) + done, "probe_refusal"},
		{"message refusal still rejected", frame(`{"refusal":"RAW_SECRET"}`, `{"content":"77"}`, `"stop"`) + done, "probe_refusal"},
		{"tool delta not hidden", frame(`{}`, `{"content":"77","tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"{}"}}]}`, `"stop"`) + done, "probe_incorrect_output"},
		{"reasoning is not visible answer", frame(`{}`, `{"reasoning_content":"77"}`, `"stop"`) + done, "probe_incorrect_output"},
		{"length not accepted", frame(`{}`, `{"content":"77"}`, `"length"`) + done, "probe_invalid_terminal"},
		{"filter not accepted", frame(`{}`, `{"content":"77"}`, `"content_filter"`) + done, "probe_refusal"},
		{"stop without DONE", frame(`{}`, `{"content":"77"}`, `"stop"`), "missing_native_terminal"},
		{"DONE without stop", frame(`{}`, `{"content":"77"}`, `null`) + done, "missing_native_terminal"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := healthFixture(t, "chat", "probe", tt.wire, true, 200)
			if out.Result != "failure" || out.Reason != tt.reason {
				t.Fatalf("wrong failure classification: %+v", out)
			}
			if strings.Contains(fmt.Sprintf("%+v", out), "RAW_SECRET") {
				t.Fatal("raw response leaked into outcome")
			}
		})
	}
	// Non-streaming responses retain message as the authoritative text field.
	out := healthFixture(t, "chat", "probe", `{"choices":[{"message":{"content":"77"},"delta":{"content":"76"},"finish_reason":"stop"}]}`, false, 200)
	if out.Result != "success" || !out.HasText {
		t.Fatalf("nonstream message changed: %+v", out)
	}
}

func TestZTAPIHealthChatDeltaSurvivesReadBoundaries(t *testing.T) {
	wire := "data: {\"choices\":[{\"index\":0,\"message\":null,\"delta\":{\"content\":\"7\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"index\":0,\"message\":{},\"delta\":{\"content\":\"7\"},\"finish_reason\":\"stop\"}]}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	for _, source := range []string{"probe", "real"} {
		for _, fragmented := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/one_byte=%t", source, fragmented), func(t *testing.T) {
				c := NewZTAPIHealthCollector(source)
				a := c.BeginAttempt(2, "chat")
				a.Dispatched()
				var reader io.Reader = strings.NewReader(wire)
				if fragmented {
					reader = iotest.OneByteReader(reader)
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"upstream-delta-proof"}}, Body: io.NopCloser(reader)}
				a.WrapResponse(resp)
				body, err := io.ReadAll(resp.Body)
				if err != nil || string(body) != wire {
					t.Fatal("collector changed downstream response")
				}
				_ = resp.Body.Close()
				out, first := c.Seal(false, false)
				if !first || out.Result != "success" || !out.HasText || !out.TransportComplete || out.UpstreamRequestID != "upstream-delta-proof" || out.ChannelID != 2 || len(out.Attempts) != 1 {
					t.Fatalf("fragmented delta lost: %+v", out)
				}
				if a.probeText != "" || a.buffer != nil || a.event != nil {
					t.Fatal("transient content retained after seal")
				}
			})
		}
	}
}
