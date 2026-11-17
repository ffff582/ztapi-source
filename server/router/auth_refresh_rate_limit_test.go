package router

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var refreshRateLimitTestIP atomic.Uint32

func newRefreshRateLimitTestRouter(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	enabled, number, duration := common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration
	register, passwordRegister := common.RegisterEnabled, common.PasswordRegisterEnabled
	common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = true, 20, 20*60
	common.RegisterEnabled, common.PasswordRegisterEnabled = true, true
	t.Cleanup(func() {
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.CriticalRateLimitDuration = enabled, number, duration
		common.RegisterEnabled, common.PasswordRegisterEnabled = register, passwordRegister
	})
	ip := refreshRateLimitTestIP.Add(1)
	return newZTAPIRealRouter(t, true), fmt.Sprintf("198.18.%d.%d:1000", ip/250, ip%250+1)
}

func TestAuthRouterRefreshFloodDoesNotConsumeLoginRegisterBucket(t *testing.T) {
	engine, remote := newRefreshRateLimitTestRouter(t)
	for i := 0; i < 100; i++ {
		response := performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/refresh", `{}`, "", remote)
		want := http.StatusUnauthorized
		if i >= 20 {
			want = http.StatusTooManyRequests
		}
		require.Equal(t, want, response.Code, "refresh request %d", i+1)
	}
	// Login and registration still share exactly the original 20-request critical allowance.
	for i := 0; i < 20; i++ {
		path, want := "/api/auth/login", http.StatusUnauthorized
		if i%2 == 1 {
			path, want = "/api/auth/register", http.StatusBadRequest
		}
		response := performZTAPIRouterRequest(engine, http.MethodPost, path, `{}`, "", remote)
		require.Equal(t, want, response.Code, "critical request %d: %s", i+1, path)
	}
	for _, path := range []string{"/api/auth/login", "/api/auth/register", "/api/auth/refresh"} {
		response := performZTAPIRouterRequest(engine, http.MethodPost, path, `{}`, "", remote)
		require.Equal(t, http.StatusTooManyRequests, response.Code, path)
	}
	logout := performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/logout", "", "", remote)
	require.Equal(t, http.StatusOK, logout.Code)
	require.JSONEq(t, `{"success":true}`, logout.Body.String())
	cookies := logout.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, "ztapi_refresh", cookies[0].Name)
	require.Equal(t, "/api/auth", cookies[0].Path)
	require.True(t, cookies[0].HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
	require.Equal(t, -1, cookies[0].MaxAge)
}

func TestAuthRouterCriticalExhaustionDoesNotConsumeRefreshBucket(t *testing.T) {
	engine, remote := newRefreshRateLimitTestRouter(t)
	for i := 0; i < 20; i++ {
		response := performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/login", `{}`, "", remote)
		require.Equal(t, http.StatusUnauthorized, response.Code)
	}
	response := performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/register", `{}`, "", remote)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	for i := 0; i < 20; i++ {
		response = performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/refresh", `{}`, "", remote)
		require.Equal(t, http.StatusUnauthorized, response.Code, "refresh request %d", i+1)
	}
	response = performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/refresh", `{}`, "", remote)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	response = performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/refresh", `{}`, "", "198.19.0.1:1000")
	require.Equal(t, http.StatusUnauthorized, response.Code, "another IP retains its own allowance")
}

func TestAuthRouterLogoutDoesNotConsumeAuthenticationBuckets(t *testing.T) {
	engine, remote := newRefreshRateLimitTestRouter(t)
	for i := 0; i < 40; i++ {
		response := performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/logout", "", "", remote)
		require.Equal(t, http.StatusOK, response.Code)
	}
	for _, path := range []string{"/api/auth/login", "/api/auth/refresh"} {
		for i := 0; i < 20; i++ {
			response := performZTAPIRouterRequest(engine, http.MethodPost, path, `{}`, "", remote)
			require.Equal(t, http.StatusUnauthorized, response.Code, "%s request %d", path, i+1)
		}
	}
}

func TestAuthRouterRefreshPreservesCriticalLimitDisabledSetting(t *testing.T) {
	_, remote := newRefreshRateLimitTestRouter(t)
	engine := newZTAPIRealRouter(t, false)
	for _, path := range []string{"/api/auth/login", "/api/auth/register", "/api/auth/refresh"} {
		want := http.StatusUnauthorized
		if path == "/api/auth/register" {
			want = http.StatusBadRequest
		}
		for i := 0; i < 40; i++ {
			response := performZTAPIRouterRequest(engine, http.MethodPost, path, `{}`, "", remote)
			require.Equal(t, want, response.Code)
		}
	}
}
