package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func TestIOCopyBytesGracefullyPreservesLocalBillingRequestID(t *testing.T) {
	const localRequestID = "local-billing-request-id"
	const upstreamRequestID = "provider-request-id"

	for _, upstreamHeader := range []string{"X-Request-ID", common.RequestIdKey} {
		t.Run(upstreamHeader, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Header("X-Request-ID", localRequestID)
			ctx.Header(common.RequestIdKey, localRequestID)

			response := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{upstreamHeader: []string{upstreamRequestID}},
			}
			IOCopyBytesGracefully(ctx, response, []byte(`{"ok":true}`))

			if got := recorder.Header().Get("X-Request-ID"); got != localRequestID {
				t.Fatalf("X-Request-ID = %q, want local billing ID %q", got, localRequestID)
			}
			if got := recorder.Header().Get(common.RequestIdKey); got != localRequestID {
				t.Fatalf("%s = %q, want local billing ID %q", common.RequestIdKey, got, localRequestID)
			}
			if got := ctx.GetString(common.UpstreamRequestIdKey); got != upstreamRequestID {
				t.Fatalf("upstream request ID = %q, want %q", got, upstreamRequestID)
			}
		})
	}
}
