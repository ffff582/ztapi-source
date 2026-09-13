package common

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	base "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func TestZTAPIHealthFinalizeDetachedBoundedOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	s := &ZTAPIHealthSession{ticket: &types.ZTAPIHealthTicket{ExecutionID: "server-id"}, collector: NewZTAPIHealthCollector("real"), record: func(ctx context.Context, ticket *types.ZTAPIHealthTicket, out types.ZTAPIHealthOutcome) error {
		calls.Add(1)
		if ctx.Err() != nil {
			t.Error("persistence inherited cancellation")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 6*time.Second {
			t.Error("unbounded persistence context")
		}
		if out.Result != "excluded" || !out.ClientCancelled {
			t.Errorf("cancel outcome: %+v", out)
		}
		return errors.New("storage unavailable")
	}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Finalize(ctx, false) }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("persisted %d times", calls.Load())
	}
}

func TestZTAPIHealthWriterSuppressesOnlySyntheticDone(t *testing.T) {
	for _, complete := range []bool{false, true} {
		c := NewZTAPIHealthCollector("real")
		a := c.BeginAttempt(1, "chat")
		a.Dispatched()
		wire := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"stop\"}]}\n\n"
		if complete {
			wire += "data: [DONE]\n\n"
		}
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
		a.WrapResponse(resp)
		_, _ = io.Copy(io.Discard, resp.Body)
		r := httptest.NewRecorder()
		gc, _ := gin.CreateTestContext(r)
		gc.Writer = NewZTAPIHealthResponseWriter(gc.Writer, c)
		_, _ = gc.Writer.WriteString("data: partial\n\n")
		n, err := gc.Writer.Write([]byte("data: [DONE]\n\n"))
		if err != nil || n != len("data: [DONE]\n\n") {
			t.Fatal("suppression changed handler error/billing path")
		}
		if strings.Contains(r.Body.String(), "[DONE]") != complete {
			t.Fatalf("complete=%t body=%s", complete, r.Body.String())
		}
	}
}

func TestZTAPIHealthProtocolUsesActualOutboundURL(t *testing.T) {
	for path, want := range map[string]string{"/v1/responses": "responses", "/v1/messages": "claude", "/v1/chat/completions": "chat", "/v1beta/models/m:streamGenerateContent": "gemini", "/v1/embeddings": "embeddings", "/v1/responses/compact": "unsupported"} {
		if got := ztapiHealthProtocol(path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}

func TestZTAPIOpenRouteAdmissionIsRetryableAndExcludedFromNextSelection(t *testing.T) {
	routeOpen := errors.New("route open")
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	session := &ZTAPIHealthSession{
		ticket:    &types.ZTAPIHealthTicket{ExecutionID: "route-open", Source: "real"},
		collector: NewZTAPIHealthCollector("real"),
		backend: ZTAPIHealthBackend{
			AdmitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string, string) error { return routeOpen },
			RouteOpen:    routeOpen,
		},
	}
	gc.Set(ztapiHealthSessionKey, session)

	_, err := BeginZTAPIHealthUpstream(gc, 7, "/v1/chat/completions", "open-route-key")
	if err == nil {
		t.Fatal("expected route-open admission error")
	}
	apiErr, ok := err.(*types.NewAPIError)
	if !ok {
		t.Fatalf("unexpected error type %T", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable || types.IsSkipRetryError(apiErr) {
		t.Fatalf("route-open error must be retryable 503: %+v", apiErr)
	}
	fingerprint, fingerprintErr := ZTAPIHealthCredentialVersion("open-route-key")
	if fingerprintErr != nil {
		t.Fatal(fingerprintErr)
	}
	excluded := base.GetContextKeyStringSlice(gc, constant.ContextKeyZTAPIHealthExcludedCredentials)
	if len(excluded) != 1 || excluded[0] != ZTAPIHealthRouteCredentialExclusion(7, fingerprint) {
		t.Fatalf("excluded credentials = %v, want channel-scoped fingerprint", excluded)
	}
	if got := ZTAPIHealthExcludedCredentialVersions(gc, 7); len(got) != 1 || got[0] != fingerprint {
		t.Fatalf("channel 7 exclusions = %v, want %s", got, fingerprint)
	}
	if got := ZTAPIHealthExcludedCredentialVersions(gc, 8); len(got) != 0 {
		t.Fatalf("same key on another channel must remain eligible: %v", got)
	}
	if len(session.collector.attempts) != 0 {
		t.Fatalf("locally rejected route must not be recorded as dispatched attempt: %+v", session.collector.attempts)
	}
}

func TestZTAPIHealthWriterDoesNotSynthesizeConvertedTerminal(t *testing.T) {
	c := NewZTAPIHealthCollector("real")
	a := c.BeginAttempt(1, "responses")
	a.Dispatched()
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))}
	a.WrapResponse(resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	r := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(r)
	gc.Writer = NewZTAPIHealthResponseWriter(gc.Writer, c)
	for _, frame := range []string{
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"event: message_stop\n",
		`data: {"type":"message_stop"}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
	} {
		if _, err := gc.Writer.WriteString(frame); err != nil {
			t.Fatal(err)
		}
	}
	if r.Body.Len() != 0 {
		t.Fatalf("synthesized completion survived: %s", r.Body.String())
	}
	content := `data: {"choices":[{"delta":{"content":"actual partial"},"finish_reason":null}]}`
	_, _ = gc.Writer.WriteString(content)
	if r.Body.String() != content {
		t.Fatalf("content changed: %s", r.Body.String())
	}
}

func TestZTAPIHealthWriterConcurrentWrites(t *testing.T) {
	c := NewZTAPIHealthCollector("real")
	r := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(r)
	w := NewZTAPIHealthResponseWriter(gc.Writer, c)
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = w.WriteString(": ping\n\n")
			w.Flush()
			_ = w.Written()
			_ = w.Status()
		}()
	}
	wg.Wait()
	if strings.Count(r.Body.String(), ": ping") != 25 {
		t.Fatal("lost concurrent write")
	}
}

func TestZTAPIHealthSessionExecutionIDAndSourceAreServerOwned(t *testing.T) {
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		gc, _ := gin.CreateTestContext(httptest.NewRecorder())
		gc.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		gc.Request.Header.Set("X-Request-ID", "repeat")
		gc.Request.Header.Set("X-ZTAPI-Health-Source", "probe")
		s, err := StartZTAPIHealthRequest(gc, &RelayInfo{OriginModelName: "public", UserId: 42}, ZTAPIHealthBackend{AdmitRequest: func(ctx context.Context, model, id, requestID string, user int, stream bool) (*types.ZTAPIHealthTicket, error) {
			if ids[id] || id == "repeat" || len(id) != 36 || user != 42 {
				t.Fatalf("untrusted identity %s", id)
			}
			ids[id] = true
			return &types.ZTAPIHealthTicket{ExecutionID: id, Source: "real"}, nil
		}})
		if err != nil || s.collector.source != "real" {
			t.Fatalf("client forged source: %v", err)
		}
	}
}

func TestZTAPIHealthSessionRecordsImageOperationLatencyAndResultValidity(t *testing.T) {
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	var recorded types.ZTAPIHealthOutcome
	s, err := StartZTAPIHealthRequest(gc, &RelayInfo{OriginModelName: "zt-image", UserId: 42, ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{Modality: "image"}}, ZTAPIHealthBackend{
		AdmitRequest: func(context.Context, string, string, string, int, bool) (*types.ZTAPIHealthTicket, error) {
			t.Fatal("image request used text health admission")
			return nil, nil
		},
		AdmitMediaRequest: func(_ context.Context, _, executionID, _ string, _ int, operation string, allowUnavailable bool) (*types.ZTAPIHealthTicket, error) {
			if operation != types.ZTAPIHealthOperationImageGenerate || allowUnavailable {
				t.Fatalf("bad image admission: %q %t", operation, allowUnavailable)
			}
			return &types.ZTAPIHealthTicket{ExecutionID: executionID, Source: "real", Modality: "image", Operation: operation}, nil
		},
		AdmitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string, string) error { return nil },
		RecordOutcome: func(_ context.Context, _ *types.ZTAPIHealthTicket, outcome types.ZTAPIHealthOutcome) error {
			recorded = outcome
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := BeginZTAPIHealthUpstream(gc, 7, "/v1/images/generations", "image-test-key")
	if err != nil {
		t.Fatal(err)
	}
	a.Dispatched()
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://cdn.example.test/result.png"}]}`))}
	a.WrapResponse(resp)
	if _, err = io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if err = s.Finalize(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if recorded.Operation != types.ZTAPIHealthOperationImageGenerate || !recorded.ResultValid || recorded.LatencyMilliseconds < 0 {
		t.Fatalf("image health metadata missing: %+v", recorded)
	}
}

func TestZTAPIHealthSessionRecordsAcceptedVideoSubmission(t *testing.T) {
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	var recorded types.ZTAPIHealthOutcome
	s, err := StartZTAPIHealthRequest(gc, &RelayInfo{OriginModelName: "zt-video", UserId: 42, ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{Modality: "video"}}, ZTAPIHealthBackend{
		AdmitMediaRequest: func(_ context.Context, _, executionID, _ string, _ int, operation string, allowUnavailable bool) (*types.ZTAPIHealthTicket, error) {
			if operation != types.ZTAPIHealthOperationVideoSubmit || allowUnavailable {
				t.Fatalf("bad video admission: %q %t", operation, allowUnavailable)
			}
			return &types.ZTAPIHealthTicket{ExecutionID: executionID, Source: "real", Modality: "video", Operation: operation}, nil
		},
		AdmitAttempt: func(_ context.Context, _ *types.ZTAPIHealthTicket, channelID int, protocol, credentialVersion string) error {
			if channelID != 9 || protocol != "video-tasks" {
				t.Fatalf("bad video attempt: %d %q", channelID, protocol)
			}
			expected, err := ZTAPIHealthCredentialVersion("video-test-key")
			if err != nil || credentialVersion != expected {
				t.Fatalf("bad video credential fingerprint: %q", credentialVersion)
			}
			return nil
		},
		RecordOutcome: func(_ context.Context, _ *types.ZTAPIHealthTicket, outcome types.ZTAPIHealthOutcome) error {
			recorded = outcome
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := BeginZTAPIHealthUpstream(gc, 9, "/provider/tasks/create", "video-test-key")
	if err != nil {
		t.Fatal(err)
	}
	a.Dispatched()
	s.MarkVideoSubmissionAccepted("provider-task-7")
	if err = s.Finalize(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if recorded.Result != "success" || recorded.Reason != "valid_output" || recorded.Operation != types.ZTAPIHealthOperationVideoSubmit ||
		recorded.UpstreamProtocol != "video-tasks" || recorded.UpstreamTaskID != "provider-task-7" || !recorded.ResultValid {
		t.Fatalf("video submission health metadata missing: %+v", recorded)
	}
}
