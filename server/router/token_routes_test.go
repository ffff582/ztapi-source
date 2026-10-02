package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

func TestTokenRevealRoutesAreNotRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.RedisEnabled = false

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("token-route-test"))))
	SetApiRouter(engine)

	for _, path := range []string{
		"/api/token/123/key",
		"/api/token/batch/keys",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, nil)
			recorder := httptest.NewRecorder()

			engine.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("POST %s status = %d, want 404; body=%s", path, recorder.Code, recorder.Body.String())
			}
		})
	}
}
