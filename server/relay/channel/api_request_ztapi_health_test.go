package channel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	base "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type healthRoundTripper func(*http.Request) (*http.Response, error)

func (f healthRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestZTAPIHealthDoRequestObservesRetryOnceWithoutNetwork(t *testing.T) {
	for _, test := range []string{"retry", "truncated", "transport_error", "cancelled", "admission_denied"} {
		t.Run(test, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("private inbound")).WithContext(ctx)
			c.Set("channel_id", 7)
			c.Set("original_model", "public-model")
			c.Set(base.RequestIdKey, "client-supplied-repeat")
			c.Request.Header.Set("X-ZTAPI-Health-Source", "probe")
			info := &relaycommon.RelayInfo{OriginModelName: "public-model", UserId: 2, IsStream: test == "truncated", ChannelMeta: &relaycommon.ChannelMeta{}}
			var outcome types.ZTAPIHealthOutcome
			var recorded, admitted, sends int
			circuit := errors.New("circuit open")
			s, err := relaycommon.StartZTAPIHealthRequest(c, info, relaycommon.ZTAPIHealthBackend{
				AdmitRequest: func(ctx context.Context, requested, id, requestID string, user int, stream bool) (*types.ZTAPIHealthTicket, error) {
					if id == requestID || len(id) != 36 || requestID != "client-supplied-repeat" {
						t.Fatalf("bad execution identity %q %q", id, requestID)
					}
					return &types.ZTAPIHealthTicket{ExecutionID: id, RequestID: requestID, Source: "real", Stream: stream}, nil
				},
				AdmitAttempt: func(ctx context.Context, ticket *types.ZTAPIHealthTicket, id int, protocol string) error {
					admitted++
					if protocol != "responses" || id != 7 {
						t.Fatalf("outbound identity %s %d", protocol, id)
					}
					if test == "admission_denied" {
						return circuit
					}
					return nil
				},
				RecordOutcome: func(ctx context.Context, ticket *types.ZTAPIHealthTicket, o types.ZTAPIHealthOutcome) error {
					recorded++
					outcome = o
					return nil
				},
				CircuitOpen: circuit,
			})
			if err != nil {
				t.Fatal(err)
			}
			if service.GetHttpClient() == nil {
				service.InitHttpClient()
			}
			client := service.GetHttpClient()
			old := client.Transport
			t.Cleanup(func() { client.Transport = old })
			client.Transport = healthRoundTripper(func(r *http.Request) (*http.Response, error) {
				sends++
				if test == "transport_error" {
					return nil, io.ErrUnexpectedEOF
				}
				status := 200
				body := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"customer answer"}]}]}`
				headers := http.Header{"X-Request-Id": {"upstream-id"}}
				if test == "retry" && sends == 1 {
					status = 502
					body = `{"error":{"code":"server_error"}}`
				}
				if test == "truncated" {
					headers.Set("Content-Type", "text/event-stream")
					body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			if test == "cancelled" {
				cancel()
			}
			attempts := 1
			if test == "retry" {
				attempts = 2
			}
			var requestErr error
			for i := 0; i < attempts; i++ {
				s.PrepareRelayAttempt()
				req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/responses", strings.NewReader("private upstream"))
				resp, e := DoRequest(c, req, info)
				requestErr = e
				if resp != nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			}
			if err := s.Finalize(c.Request.Context(), requestErr != nil); err != nil {
				t.Fatal(err)
			}
			_ = s.Finalize(c.Request.Context(), false)
			if recorded != 1 {
				t.Fatalf("recorded %d times", recorded)
			}
			switch test {
			case "retry":
				if outcome.Result != "success" || len(outcome.Attempts) != 2 || admitted != 2 || outcome.Attempts[0].HTTPStatus != 502 || outcome.HTTPStatus != 200 {
					t.Fatalf("retry outcome %+v", outcome)
				}
			case "truncated", "transport_error":
				if outcome.Result != "failure" {
					t.Fatalf("failure became %+v", outcome)
				}
			case "cancelled":
				if sends != 0 || outcome.Result != "excluded" || outcome.Dispatched {
					t.Fatalf("cancel dispatched=%d %+v", sends, outcome)
				}
			case "admission_denied":
				var e *types.NewAPIError
				if sends != 0 || !errors.As(requestErr, &e) || e.StatusCode != 503 {
					t.Fatalf("gate sends=%d err=%v", sends, requestErr)
				}
			}
		})
	}
}
