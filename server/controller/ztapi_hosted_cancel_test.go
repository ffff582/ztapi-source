package controller

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/hostedsearch"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHostedCancellationReturnsButRetainsBoundedWorkerLease(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			blocked := make(chan struct{})
			entered := make(chan struct{}, 8)
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(blocked) }) })
			router.Use(CaptureHostedResponsesCredential(), func(c *gin.Context) {
				c.Request.Header.Del("Authorization")
				c.Set("id", 1)
				c.Set("token_id", 2)
				c.Next()
			})
			options := HostedResponsesOptions{Provider: hostedControllerProvider{}, Authorize: func(*gin.Context, string) bool { return true }, Dispatch: func(*gin.Context, string) hostedsearch.ToolDispatch {
				return func(context.Context, hostedsearch.ToolRequest, func(context.Context) (hostedsearch.Result, error)) (hostedsearch.Result, error) {
					panic("no tool expected")
				}
			}}
			router.POST("/v1/responses", HostedResponsesBridge(router, options), func(c *gin.Context) {
				entered <- struct{}{}
				<-blocked
				c.JSON(500, gin.H{"error": "synthetic blocked storage"})
			})
			for i := 0; i < 8; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"zt-claude-opus-5","input":"Hi","tools":[{"type":"web_search"}],"stream":`+strconv.FormatBool(stream)+`}`)).WithContext(ctx)
				r.Header.Set("Authorization", "Bearer dummy-owned-key")
				r.RemoteAddr = "203.0.113.9:1234"
				done := make(chan struct{})
				go func() { defer close(done); router.ServeHTTP(httptest.NewRecorder(), r) }()
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("model did not enter")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("canceled request waited indefinitely on context-ignoring storage")
				}
			}
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"zt-claude-opus-5","input":"Hi","tools":[{"type":"web_search"}]}`))
			r.Header.Set("Authorization", "Bearer dummy-owned-key")
			r.RemoteAddr = "203.0.113.9:1234"
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			require.Equal(t, 429, w.Code, "canceled roots must not release leases while their native workers are still blocked")
			once.Do(func() { close(blocked) })
		})
	}
}
