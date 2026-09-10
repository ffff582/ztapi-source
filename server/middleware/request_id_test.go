package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func performRequestIDRequest(t *testing.T, inbound string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestId())
	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(common.RequestIdKey))
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if inbound != "" {
		request.Header.Set("X-Request-ID", inbound)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestRequestIDUsesTrustedProxyValueAndReturnsMatchingHeaders(t *testing.T) {
	recorder := performRequestIDRequest(t, "ztapi-edge-7f4a")

	if got := recorder.Header().Get("X-Request-ID"); got != "ztapi-edge-7f4a" {
		t.Fatalf("standard request ID = %q, want trusted edge value", got)
	}
	if got := recorder.Header().Get(common.RequestIdKey); got != "ztapi-edge-7f4a" {
		t.Fatalf("legacy request ID = %q, want trusted edge value", got)
	}
	if got := recorder.Body.String(); got != "ztapi-edge-7f4a" {
		t.Fatalf("context request ID = %q, want trusted edge value", got)
	}
}

func TestRequestIDRejectsMalformedInboundValue(t *testing.T) {
	recorder := performRequestIDRequest(t, "bad request id\r\ninjected")
	standard := recorder.Header().Get("X-Request-ID")
	legacy := recorder.Header().Get(common.RequestIdKey)

	if standard == "" {
		t.Fatal("generated request ID must not be empty")
	}
	if standard == "bad request id\r\ninjected" {
		t.Fatal("malformed inbound request ID must not be trusted")
	}
	if legacy != standard {
		t.Fatalf("request ID headers diverged: standard=%q legacy=%q", standard, legacy)
	}
}

func TestRequestIDGeneratesValueWhenProxyHeaderMissing(t *testing.T) {
	recorder := performRequestIDRequest(t, "")
	standard := recorder.Header().Get("X-Request-ID")

	if standard == "" {
		t.Fatal("generated request ID must not be empty")
	}
	if got := recorder.Header().Get(common.RequestIdKey); got != standard {
		t.Fatalf("request ID headers diverged: standard=%q legacy=%q", standard, got)
	}
}
