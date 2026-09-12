package common

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func healthFixture(t *testing.T, protocol, source, wire string, stream bool, status int) ZTAPIHealthObservation {
	t.Helper()
	c := NewZTAPIHealthCollector(source)
	a := c.BeginAttempt(7, protocol)
	a.Dispatched()
	resp := &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"upstream-123"}}, Body: io.NopCloser(strings.NewReader(wire))}
	if stream {
		resp.Header.Set("Content-Type", "text/event-stream")
	}
	a.WrapResponse(resp)
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != wire {
		t.Fatalf("observer altered body: %q, %v", got, err)
	}
	_ = resp.Body.Close()
	out, first := c.Seal(false, false)
	if !first {
		t.Fatal("first seal rejected")
	}
	return out
}

func TestZTAPIHealthWireFixtures(t *testing.T) {
	cases := []struct {
		name, protocol, source, wire, result string
		stream                               bool
	}{
		{"chat", "chat", "real", `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`, "success", false},
		{"empty", "chat", "real", `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`, "failure", false},
		{"tool", "chat", "real", `{"choices":[{"message":{"tool_calls":[{"id":"call1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, "success", false},
		{"invalid tool", "chat", "real", `{"choices":[{"message":{"tool_calls":[{}]},"finish_reason":"tool_calls"}]}`, "failure", false},
		{"refusal", "chat", "real", `{"choices":[{"message":{"refusal":"no"},"finish_reason":"stop"}]}`, "excluded", false},
		{"filter", "chat", "real", `{"choices":[{"message":{},"finish_reason":"content_filter"}]}`, "excluded", false},
		{"length text", "chat", "real", `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`, "success", false},
		{"length reasoning", "chat", "real", `{"choices":[{"message":{"reasoning_content":"private"},"finish_reason":"length"}]}`, "unknown", false},
		{"unknown terminal", "chat", "real", `{"choices":[{"message":{"content":"ok"},"finish_reason":"alien"}]}`, "unknown", false},
		{"missing finish", "chat", "real", `{"choices":[{"message":{"content":"ok"}}]}`, "failure", false},
		{"stream", "chat", "real", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "success", true},
		{"truncated", "chat", "real", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n", "failure", true},
		{"done without finish", "chat", "real", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", "failure", true},
		{"finish without done", "chat", "real", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n", "failure", true},
		{"responses", "responses", "real", `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, "success", false},
		{"responses stream", "responses", "real", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "success", true},
		{"responses failed", "responses", "real", `{"status":"failed","error":{"code":"server_error"}}`, "failure", false},
		{"responses incomplete", "responses", "real", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`, "success", false},
		{"responses incomplete reasoning", "responses", "real", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning","summary":[]}]}`, "unknown", false},
		{"claude", "claude", "real", `{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, "success", false},
		{"claude stream", "claude", "real", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "success", true},
		{"claude truncated", "claude", "real", "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n", "failure", true},
		{"gemini", "gemini", "real", `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`, "success", false},
		{"gemini tool", "gemini", "real", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}`, "success", false},
		{"gemini media", "gemini", "real", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"YWJj"}}]},"finishReason":"STOP"}]}`, "success", false},
		{"image generation", "images", "real", `{"created":1,"data":[{"url":"https://cdn.example.test/result.png"}],"usage":{"image_output":1}}`, "success", false},
		{"image generation empty", "images", "real", `{"created":1,"data":[]}`, "failure", false},
		{"image generation probe", "images", "probe", `{"created":1,"data":[{"b64_json":"YWJj"}]}`, "success", false},
		{"gemini thoughts", "gemini", "real", `{"candidates":[{"content":{"parts":[{"text":"private","thought":true}]},"finishReason":"MAX_TOKENS"}]}`, "unknown", false},
		{"gemini safety", "gemini", "real", `{"promptFeedback":{"blockReason":"SAFETY"}}`, "excluded", false},
		{"malformed", "chat", "real", `{"choices":`, "failure", false},
		{"unsupported", "unsupported", "real", `{"text":"ok"}`, "unknown", false},
		{"probe correct", "chat", "probe", `{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}]}`, "success", false},
		{"probe wrong", "chat", "probe", `{"choices":[{"message":{"content":"78"},"finish_reason":"stop"}]}`, "failure", false},
		{"probe refused", "chat", "probe", `{"choices":[{"message":{"refusal":"no"},"finish_reason":"stop"}]}`, "failure", false},
		{"probe length", "chat", "probe", `{"choices":[{"message":{"content":"77"},"finish_reason":"length"}]}`, "failure", false},
		{"200 safety error", "chat", "real", `{"error":{"code":"content_policy_violation"}}`, "excluded", false},
		{"200 quota error", "chat", "real", `{"error":{"type":"rate_limit_error"}}`, "failure", false},
		{"200 unclassified error", "chat", "real", `{"error":{"code":"custom_problem"}}`, "unknown", false},
		{"responses streamed tool", "responses", "real", "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc1\",\"type\":\"function_call\",\"name\":\"lookup\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc1\",\"delta\":\"{}\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "success", true},
		{"responses probe no duplicate done text", "responses", "probe", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"77\"}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"77\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", "success", true},
		{"claude malformed tool", "claude", "real", "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call\",\"name\":\"lookup\",\"input\":{}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{broken\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "failure", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			wire := tt.wire
			out := healthFixture(t, tt.protocol, tt.source, wire, tt.stream, 200)
			if out.Result != tt.result {
				t.Fatalf("got %s (%s), want %s: %+v", out.Result, out.Reason, tt.result, out)
			}
			if out.UpstreamRequestID != "upstream-123" {
				t.Fatal("request ID lost")
			}
		})
	}
}

func TestZTAPIHealthLocalProcessingErrorIsNotUpstreamTruncation(t *testing.T) {
	c := NewZTAPIHealthCollector("real")
	a := c.BeginAttempt(1, "chat")
	a.Dispatched()
	r := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("not consumed"))}
	a.WrapResponse(r)
	_ = r.Body.Close()
	out, _ := c.Seal(false, true)
	if out.Result != "unknown" {
		t.Fatalf("unread body blamed on upstream: %+v", out)
	}
}

func TestZTAPIHealthEarlyCloseIncompleteJSONDoesNotFakeEOF(t *testing.T) {
	c := NewZTAPIHealthCollector("real")
	a := c.BeginAttempt(1, "chat")
	a.Dispatched()
	r := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))}
	a.WrapResponse(r)
	p := make([]byte, 5)
	_, _ = r.Body.Read(p)
	_ = r.Body.Close()
	out, _ := c.Seal(false, false)
	if out.Result != "failure" || out.TransportComplete {
		t.Fatalf("early close: %+v", out)
	}
}

func TestZTAPIHealthWrappedProviderError(t *testing.T) {
	out := healthFixture(t, "chat", "real", `{"error":{"code":"wrapped_error","provider_specific_fields":{"status_code":502,"request_id":"provider-502","code":"upstream_unavailable"}}}`, false, 422)
	if out.Result != "failure" || out.HTTPStatus != 422 || out.ProviderErrorCode != "upstream_unavailable" || out.UpstreamRequestID != "provider-502" {
		t.Fatalf("lost attribution: %+v", out)
	}
	for _, status := range []int{400, 422} {
		out := healthFixture(t, "chat", "real", `{"error":{"code":"invalid_request"}}`, false, status)
		if out.Result != "excluded" {
			t.Fatalf("customer status %d: %+v", status, out)
		}
	}
}

func TestZTAPIHealthDispatched4xxAttribution(t *testing.T) {
	cases := []struct {
		name, source, code, result, reason string
		status                             int
	}{
		{"real invalid upstream key", "real", "invalid_api_key", "failure", "upstream_auth", 401},
		{"real generic upstream 401", "real", "invalid_request", "failure", "upstream_auth", 401},
		{"real upstream payment", "real", "payment_required", "failure", "upstream_quota", 402},
		{"real upstream rate limit", "real", "rate_limit_exceeded", "failure", "upstream_rate_limit", 429},
		{"real upstream quota in 200", "real", "insufficient_quota", "failure", "upstream_quota", 200},
		{"real known key failure in 403", "real", "authentication_error", "failure", "upstream_auth", 403},
		{"real ambiguous forbidden", "real", "permission_denied", "unknown", "ambiguous_upstream_4xx", 403},
		{"real generic forbidden", "real", "invalid_request_error", "unknown", "ambiguous_upstream_4xx", 403},
		{"real invalid input", "real", "invalid_argument", "excluded", "invalid_input", 400},
		{"real context limit", "real", "context_length_exceeded", "excluded", "invalid_input", 400},
		{"real refusal", "real", "content_policy_violation", "excluded", "safety_refusal", 403},
		{"probe key failure", "probe", "invalid_api_key", "failure", "upstream_auth", 401},
		{"probe payment", "probe", "payment_required", "failure", "upstream_quota", 402},
		{"probe rate limit", "probe", "rate_limit_error", "failure", "upstream_rate_limit", 429},
		{"probe ambiguous forbidden", "probe", "permission_denied", "failure", "probe_upstream_4xx", 403},
		{"probe other 4xx", "probe", "model_not_found", "failure", "probe_upstream_4xx", 404},
		{"probe generic 400", "probe", "unknown_error", "failure", "probe_upstream_4xx", 400},
		{"probe generic invalid request", "probe", "invalid_request_error", "failure", "probe_upstream_4xx", 400},
		{"probe generic invalid request forbidden", "probe", "invalid_request", "failure", "probe_upstream_4xx", 403},
		{"probe invalid parameter", "probe", "invalid_argument", "excluded", "invalid_input", 400},
		{"probe refusal", "probe", "content_policy_violation", "failure", "probe_refusal", 403},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out := healthFixture(t, "chat", tt.source, fmt.Sprintf(`{"error":{"code":%q}}`, tt.code), false, tt.status)
			if out.Result != tt.result || out.Reason != tt.reason {
				t.Fatalf("got %s/%s, want %s/%s: %+v", out.Result, out.Reason, tt.result, tt.reason, out)
			}
			if out.HTTPStatus != tt.status || out.ProviderErrorCode != tt.code {
				t.Fatalf("lost original upstream attribution: %+v", out)
			}
		})
	}
}

func TestZTAPIHealthAttemptResetAndSeal(t *testing.T) {
	c := NewZTAPIHealthCollector("real")
	a := c.BeginAttempt(1, "chat")
	a.Dispatched()
	a.TransportError(errors.New("broken"))
	b := c.BeginAttempt(2, "chat")
	b.Dispatched()
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))}
	b.WrapResponse(resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	a.TransportError(errors.New("late callback"))
	out, first := c.Seal(false, false)
	if !first || out.Result != "success" || out.ChannelID != 2 || len(out.Attempts) != 2 {
		t.Fatalf("retry contaminated: %+v", out)
	}
	if out.Attempts[0].Index != 1 || out.Attempts[1].Index != 2 {
		t.Fatal("attempt order lost")
	}
	if _, first = c.Seal(false, true); first {
		t.Fatal("duplicate finalization")
	}
	if c.BeginAttempt(3, "chat") != nil {
		t.Fatal("attempt after sealing")
	}
}

func TestZTAPIHealthCollectorRaceAndCancellation(t *testing.T) {
	c := NewZTAPIHealthCollector("real")
	a := c.BeginAttempt(1, "chat")
	a.Dispatched()
	var wg sync.WaitGroup
	var seals atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.TransportError(io.ErrUnexpectedEOF)
			if out, first := c.Seal(true, false); first {
				seals.Add(1)
				if out.Result != "excluded" || !out.ClientCancelled {
					t.Errorf("cancel: %+v", out)
				}
			}
		}()
	}
	wg.Wait()
	if seals.Load() != 1 {
		t.Fatalf("seals=%d", seals.Load())
	}
}

func TestZTAPIHealthBoundDoesNotLimitCustomerBody(t *testing.T) {
	wire := `{"choices":[{"message":{"content":"` + strings.Repeat("x", ztapiHealthMaxBody+1) + `"},"finish_reason":"stop"}]}`
	out := healthFixture(t, "chat", "real", wire, false, 200)
	if out.Result != "unknown" || out.Reason != "observation_limit" {
		t.Fatalf("oversize declared healthy: %+v", out)
	}
}

type healthChunkReader struct{ data string }

func (r *healthChunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}
func (r *healthChunkReader) Close() error { return nil }

func TestZTAPIHealthIncrementalSSE(t *testing.T) {
	c := NewZTAPIHealthCollector("real")
	a := c.BeginAttempt(1, "chat")
	a.Dispatched()
	wire := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\n" + "data: \"finish_reason\":\"stop\"}]}\r\n\r\ndata: [DONE]\r\n\r\n"
	r := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &healthChunkReader{data: wire}}
	a.WrapResponse(r)
	got, _ := io.ReadAll(r.Body)
	out, _ := c.Seal(false, false)
	if string(got) != wire || out.Result != "success" {
		t.Fatalf("fragmentation: %+v", out)
	}
}

func TestZTAPIHealthResponsesToolKeepsCanonicalIdentity(t *testing.T) {
	c := NewZTAPIHealthCollector("real")
	a := c.BeginAttempt(1, "responses")
	a.Dispatched()
	wire := "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc1\",\"call_id\":\"call1\",\"type\":\"function_call\",\"name\":\"lookup\",\"arguments\":\"\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc1\",\"delta\":\"{}\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc1\",\"call_id\":\"call1\",\"type\":\"function_call\",\"name\":\"lookup\",\"arguments\":\"{}\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"fc1\",\"call_id\":\"call1\",\"type\":\"function_call\",\"name\":\"lookup\",\"arguments\":\"{}\"}]}}\n\n"
	r := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
	a.WrapResponse(r)
	_, _ = io.Copy(io.Discard, r.Body)
	if len(a.tools) != 1 {
		t.Fatalf("one tool became %d collector entries", len(a.tools))
	}
	out, _ := c.Seal(false, false)
	if out.Result != "success" || !out.HasTool {
		t.Fatalf("canonical tool lost: %+v", out)
	}
}
