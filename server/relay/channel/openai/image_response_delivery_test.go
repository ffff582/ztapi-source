package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageDeliveryFailureWriter struct {
	*httptest.ResponseRecorder
	short    bool
	writeErr error
	flushErr error
	writes   int
	flushes  int
}

func (w *imageDeliveryFailureWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short {
		return w.ResponseRecorder.Write(data[:len(data)/2])
	}
	return w.ResponseRecorder.Write(data)
}

func (w *imageDeliveryFailureWriter) FlushError() error {
	w.flushes++
	return w.flushErr
}

func (w *imageDeliveryFailureWriter) Flush() {
	w.flushes++
	w.ResponseRecorder.Flush()
}

func TestOpenaiImageManagedDeliveryFailureKeepsUpstreamEvidence(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"c3ludGhldGljLWltYWdl"}],"usage":` + observedGPTImageUsage + `}`)
	for _, tt := range []struct {
		name     string
		short    bool
		writeErr error
		flushErr error
		canceled bool
		want     error
	}{
		{name: "short write", short: true, want: io.ErrShortWrite},
		{name: "downstream disconnected", writeErr: io.ErrClosedPipe, want: io.ErrClosedPipe},
		{name: "flush failed", flushErr: io.ErrClosedPipe, want: io.ErrClosedPipe},
		{name: "request canceled", canceled: true, want: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writer := &imageDeliveryFailureWriter{ResponseRecorder: httptest.NewRecorder(), short: tt.short, writeErr: tt.writeErr, flushErr: tt.flushErr}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			if tt.canceled {
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			c.Header("X-Request-ID", "local-billing-id")
			info := gptImageEvidenceInfo(t, body)
			tracked := &trackingOpenAIImageBody{reader: bytes.NewReader(body)}
			usage, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: http.Header{
				"Content-Type": {"application/json"}, "X-Request-ID": {"provider-id"},
				"Authorization": {"Bearer synthetic-private-value"}, "Set-Cookie": {"private=value"},
			}, Body: tracked})
			require.Nil(t, apiErr, "delivery failure must not enter API-error retry/refund/JSON handling")
			require.NotNil(t, usage, "preserve the upstream usage handoff")
			require.True(t, tracked.closed.Load())
			require.Len(t, c.Errors, 1, "a failed client write must not be reported as successful delivery")
			require.ErrorIs(t, c.Errors.Last().Err, tt.want)
			require.True(t, c.Errors.Last().IsType(gin.ErrorTypePrivate))
			require.True(t, c.IsAborted(), "no later Gin handler may append another response")
			require.True(t, info.ZTAPIImageDeliveryFailed(), "failed delivery must not be finalized as a completed customer image sale")
			validated, evidence := info.GetZTAPIImageSettlementEvidence()
			require.NotNil(t, validated, "client failure is not proof of no supplier charge")
			require.NotNil(t, evidence)
			require.False(t, evidence.Pending)
			require.Equal(t, "synthetic-header-id", evidence.UpstreamRequestID)
			require.Equal(t, 1, evidence.ResultCount)
			require.True(t, evidence.ResultAvailable)
			require.Equal(t, observedGPTImageUsage, string(evidence.GetRawUsageJSON()))
			require.Len(t, evidence.GetDimensions(), 3)
			require.Zero(t, info.CurrentZTAPIImageResponseAttemptID())
			require.LessOrEqual(t, writer.writes, 1, "no write retry or appended JSON error")
			require.True(t, bytes.HasPrefix(body, writer.Body.Bytes()))
			require.Equal(t, "local-billing-id", writer.Header().Get("X-Request-ID"))
			require.Empty(t, writer.Header().Get("Authorization"))
			require.Empty(t, writer.Header().Get("Set-Cookie"))
			if !tt.canceled {
				require.Equal(t, "provider-id", c.GetString(basecommon.UpstreamRequestIdKey))
			}
		})
	}
}

func TestOpenaiImageManagedDeliveryFailureDoesNotInventUsage(t *testing.T) {
	body := []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/synthetic"}]}`)
	writer := &imageDeliveryFailureWriter{ResponseRecorder: httptest.NewRecorder(), writeErr: io.ErrClosedPipe}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	info := newOpenAIManagedImageResponseInfo(t, body)
	usage, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Len(t, c.Errors, 1)
	_, evidence := info.GetZTAPIImageSettlementEvidence()
	require.NotNil(t, evidence)
	require.True(t, evidence.Pending, "missing upstream usage must remain pending, not zero-cost")
	require.Empty(t, evidence.GetDimensions())
	require.Empty(t, writer.Body.Bytes())
}

func TestOpenaiImageManagedSaleContractDeliversPendingNativeUsage(t *testing.T) {
	for _, rawUsage := range []string{"", `{"input_tokens":18,"output_tokens":196,"total_tokens":214}`} {
		name := "missing native usage"
		if rawUsage != "" {
			name = "native usage missing dimensions"
		}
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"data":[{"b64_json":"c3ludGhldGljLWltYWdl"}]`)
			if rawUsage != "" {
				body = append(body, []byte(`,"usage":`+rawUsage)...)
			}
			body = append(body, '}')
			info := gptImageEvidenceInfo(t, body)
			info.ZTAPIPublicationSnapshot.ImageSaleContractJSON = types.ZTAPIImageSaleContract20261109JSON()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			usage, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))})
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			require.Zero(t, usage.TotalTokens, "do not substitute invented token counts for missing native usage")
			require.Equal(t, body, recorder.Body.Bytes())
			require.Empty(t, c.Errors)
			require.False(t, c.IsAborted())
			validated, evidence := info.GetZTAPIImageSettlementEvidence()
			require.NotNil(t, validated)
			require.Equal(t, 1, validated.ResultCount)
			require.NotNil(t, evidence)
			require.True(t, evidence.Pending)
			require.True(t, evidence.ResultAvailable)
			require.Equal(t, 1, evidence.ResultCount)
			require.Equal(t, "synthetic-header-id", evidence.UpstreamRequestID)
			require.Equal(t, rawUsage, string(evidence.GetRawUsageJSON()))
			require.Empty(t, evidence.GetDimensions())
		})
	}
}

type imageDeliveryTruncatingWriter struct{ http.ResponseWriter }

func (w imageDeliveryTruncatingWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w imageDeliveryTruncatingWriter) Write(data []byte) (int, error) {
	return w.ResponseWriter.Write(data[:len(data)/2])
}

func TestOpenaiImageManagedLargeHTTPDelivery(t *testing.T) {
	body := append([]byte(`{"data":[{"b64_json":"`), bytes.Repeat([]byte("AAAA"), 2<<20)...)
	body = append(body, []byte(`"}],"usage":`+observedGPTImageUsage+`}`)...)
	for _, truncated := range []bool{false, true} {
		name := "complete"
		if truncated {
			name = "truncated after HTTP 200"
		}
		t.Run(name, func(t *testing.T) {
			info := gptImageEvidenceInfo(t, body)
			result := make(chan *gin.Context, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if truncated {
					w = imageDeliveryTruncatingWriter{w}
				}
				c, _ := gin.CreateTestContext(w)
				c.Request = r
				_, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: http.Header{
					"Content-Type": {"application/json"}, "Content-Length": {"7"},
				}, Body: io.NopCloser(bytes.NewReader(body))})
				if apiErr != nil {
					c.Status(apiErr.StatusCode)
				}
				result <- c
			}))
			defer server.Close()
			transport := &http.Transport{DisableCompression: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			resp, err := client.Post(server.URL+"/v1/images/generations", "application/json", nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			received, readErr := io.ReadAll(resp.Body)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.EqualValues(t, len(body), resp.ContentLength)
			c := <-result
			if truncated {
				require.ErrorIs(t, readErr, io.ErrUnexpectedEOF)
				require.Len(t, received, len(body)/2)
				require.Len(t, c.Errors, 1)
				require.ErrorIs(t, c.Errors.Last().Err, io.ErrShortWrite)
				require.True(t, c.IsAborted())
			} else {
				require.NoError(t, readErr)
				require.True(t, bytes.Equal(body, received))
				require.Empty(t, c.Errors)
				require.False(t, c.IsAborted())
			}
			validated, evidence := info.GetZTAPIImageSettlementEvidence()
			require.NotNil(t, validated)
			require.NotNil(t, evidence)
			require.False(t, evidence.Pending)
			require.Equal(t, observedGPTImageUsage, string(evidence.GetRawUsageJSON()))
			t.Logf("HTTP status=%d declared=%d received=%d body_error=%v", resp.StatusCode, resp.ContentLength, len(received), readErr)
		})
	}
}
