package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

func newLegacyUserAuthEngine() *gin.Engine {
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("legacy-auth-test"))))
	engine.GET("/protected", UserAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	return engine
}

// The console only refreshes its session when a request answers 401. An
// expired bearer token that answered 200 left pages showing a load failure the
// visitor could not clear without signing in again.
func TestLegacyUserAuthAnswers401ForAnUnusableBearerToken(t *testing.T) {
	setupZTAPIUserAuthTest(t)
	engine := newLegacyUserAuthEngine()

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer expired.or.rotated.token")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired bearer status = %d, want 401; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestLegacyUserAuthKeepsItsStatusForNonBearerCredentials(t *testing.T) {
	setupZTAPIUserAuthTest(t)
	engine := newLegacyUserAuthEngine()

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "legacy-access-token-value")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("legacy access token status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}

	missing := httptest.NewRequest(http.MethodGet, "/protected", nil)
	missingRecorder := httptest.NewRecorder()
	engine.ServeHTTP(missingRecorder, missing)
	if missingRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing credential status = %d, want 401; body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}
}
