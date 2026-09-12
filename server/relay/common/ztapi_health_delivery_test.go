package common

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type healthFailedDelivery struct {
	gin.ResponseWriter
	short bool
}

func (w healthFailedDelivery) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestZTAPIHealthNonstreamDeliveryCancellationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, when, result, reason string
		status                     int
	}{
		{"late after full delivery", "after", "success", "valid_output", 200},
		{"before upstream completion", "before_eof", "excluded", "client_cancelled", 200},
		{"after EOF before delivery", "before_write", "excluded", "client_cancelled", 200},
		{"write error", "write_error", "excluded", "client_cancelled", 200},
		{"short write", "short", "excluded", "client_cancelled", 200},
		{"write error without cancellation", "no_cancel_write_error", "unknown", "relay_processing_error", 200},
		{"short write without cancellation", "no_cancel_short", "unknown", "relay_processing_error", 200},
		{"post-commit header mutation", "mutated_length", "excluded", "client_cancelled", 200},
		{"incomplete downstream", "partial", "excluded", "client_cancelled", 200},
		{"unknown downstream length", "no_length", "excluded", "client_cancelled", 200},
		{"late preserves empty failure", "empty", "failure", "empty_output", 200},
		{"late preserves upstream HTTP error", "http_error", "failure", "upstream_http_error", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gc, _ := gin.CreateTestContext(httptest.NewRecorder())
			gc.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			if strings.HasSuffix(tc.when, "write_error") || strings.HasSuffix(tc.when, "short") {
				gc.Writer = healthFailedDelivery{gc.Writer, strings.HasSuffix(tc.when, "short")}
			}
			var got types.ZTAPIHealthOutcome
			s, err := StartZTAPIHealthRequest(gc, &RelayInfo{OriginModelName: "public", UserId: 42}, ZTAPIHealthBackend{
				AdmitRequest: func(context.Context, string, string, string, int, bool) (*types.ZTAPIHealthTicket, error) {
					return &types.ZTAPIHealthTicket{Source: "real"}, nil
				},
				AdmitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string) error { return nil },
				RecordOutcome: func(_ context.Context, _ *types.ZTAPIHealthTicket, out types.ZTAPIHealthOutcome) error {
					got = out
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			a, err := BeginZTAPIHealthUpstream(gc, 1, "/v1/chat/completions")
			if err != nil {
				t.Fatal(err)
			}
			a.Dispatched()
			body := `{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}]}`
			if tc.when == "empty" {
				body = `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`
			}
			if tc.when == "http_error" {
				body = `{"error":{"code":"server_error"}}`
			}
			resp := &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			a.WrapResponse(resp)
			if tc.when == "before_eof" {
				cancel()
			}
			if _, err = io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatal(err)
			}
			if tc.when == "before_write" {
				cancel()
			}
			if tc.when != "no_length" {
				gc.Writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
			}
			gc.Writer.WriteHeader(tc.status)
			if tc.when == "partial" {
				_, _ = gc.Writer.Write([]byte(body[:5]))
			} else if tc.when == "mutated_length" {
				_, _ = gc.Writer.Write([]byte(body[:5]))
				gc.Writer.Header().Set("Content-Length", "10")
				_, _ = gc.Writer.Write([]byte(body[5:10]))
			} else {
				_, _ = gc.Writer.Write([]byte(body[:5]))
				_, _ = gc.Writer.Write([]byte(body[5:]))
			}
			if !strings.HasPrefix(tc.when, "no_cancel_") {
				cancel()
			}
			if err = s.Finalize(ctx, false); err != nil {
				t.Fatal(err)
			}
			if got.Result != tc.result || got.Reason != tc.reason || got.ClientCancelled != (tc.result == "excluded") {
				t.Fatalf("got %+v, want %s/%s", got, tc.result, tc.reason)
			}
		})
	}
}

func TestZTAPIHealthPartialStreamCancellationRemainsExcluded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := NewZTAPIHealthCollector("real")
	a := c.BeginAttempt(1, "chat")
	a.Dispatched()
	r := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))}
	a.WrapResponse(r)
	_, _ = io.Copy(io.Discard, r.Body)
	cancel()
	out, _ := c.Seal(errors.Is(ctx.Err(), context.Canceled), false)
	if out.Result != "excluded" || !out.ClientCancelled {
		t.Fatalf("partial stream counted: %+v", out)
	}
}

func TestZTAPIHealthHTTPCompleteThenCloseDuringSettlement(t *testing.T) {
	results := make(chan types.ZTAPIHealthOutcome, 1)
	errs := make(chan error, 1)
	engine := gin.New()
	engine.POST("/v1/chat/completions", func(gc *gin.Context) {
		_, _ = io.Copy(io.Discard, gc.Request.Body)
		_ = gc.Request.Body.Close()
		s, err := StartZTAPIHealthRequest(gc, &RelayInfo{OriginModelName: "public", UserId: 42}, ZTAPIHealthBackend{
			AdmitRequest: func(context.Context, string, string, string, int, bool) (*types.ZTAPIHealthTicket, error) {
				return &types.ZTAPIHealthTicket{Source: "real"}, nil
			},
			AdmitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string) error { return nil },
			RecordOutcome: func(_ context.Context, _ *types.ZTAPIHealthTicket, out types.ZTAPIHealthOutcome) error {
				results <- out
				return nil
			},
		})
		if err != nil {
			errs <- err
			return
		}
		a, err := BeginZTAPIHealthUpstream(gc, 1, "/v1/chat/completions")
		if err != nil {
			errs <- err
			return
		}
		a.Dispatched()
		body := `{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}]}`
		resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
		a.WrapResponse(resp)
		_, _ = io.Copy(io.Discard, resp.Body)
		gc.Writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if _, err = io.Copy(gc.Writer, strings.NewReader(body)); err != nil {
			errs <- err
			return
		}
		gc.Writer.Flush()
		select {
		case <-gc.Request.Context().Done():
		case <-time.After(3 * time.Second):
			errs <- errors.New("client did not close completed response")
			return
		}
		if err = s.Finalize(gc.Request.Context(), false); err != nil {
			errs <- err
		}
	})
	server := httptest.NewServer(engine)
	defer server.Close()
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-results:
		if out.Result != "success" || out.ClientCancelled {
			t.Fatalf("completed HTTP response misclassified: %+v", out)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("health finalization did not complete")
	}
}
