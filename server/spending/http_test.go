package spending

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestHTTPAuthCASStrictJSONCSRFAndOwnerIsolation(t *testing.T) {
	r, now := sourceFixture(t)
	store, err := NewStore(fixture(t))
	require.NoError(t, err)
	require.NoError(t, service.ConfigureZTAPISessionSigningKey(strings.Repeat("s", 32)))
	handler := NewHandler(r, store, HandlerConfig{PublicOrigin: "https://ztapi.vip", Now: func() time.Time { return now }})
	token, err := service.IssueZTAPIAccessToken(1, now)
	require.NoError(t, err)
	call := func(method, path, body, auth, owner, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		req.Header.Set("New-Api-User", owner)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		return w
	}
	require.Equal(t, 200, call("GET", "/healthz", "", "", "", "").Code)
	require.Equal(t, 401, call("GET", "/api/spending/config", "", "", "1", "").Code)
	require.Equal(t, 401, call("GET", "/api/spending/config", "", token, "2", "").Code)
	require.Equal(t, 200, call("GET", "/api/spending/config", "", token, "1", "").Code)
	body := `{"enabled":true,"daily_spend_u":"1.000001","hourly_spend_u":"","low_balance_u":"","input_tokens":0,"telegram":false,"version":0}`
	require.Equal(t, 403, call("PUT", "/api/spending/config", body, token, "1", "https://evil.example").Code)
	require.Equal(t, 200, call("PUT", "/api/spending/config", body, token, "1", "https://ztapi.vip").Code)
	require.Equal(t, 409, call("PUT", "/api/spending/config", body, token, "1", "https://ztapi.vip").Code)
	for _, bad := range []string{strings.Replace(body, `"version":0`, `"version":1,"user_id":2`, 1), strings.Replace(body, `"version":0`, `"version":1,"version":1`, 1), strings.Replace(body, `"version":0`, `"version":null`, 1), strings.Replace(body, `"version":0`, `"version":1`, 1) + `{}`, `{}`, strings.Repeat("x", 9000)} {
		code := call("PUT", "/api/spending/config", bad, token, "1", "https://ztapi.vip").Code
		require.True(t, code == 400 || code == 413)
	}
	require.Equal(t, 400, call("GET", "/api/spending/alerts?limit=101", "", token, "1", "").Code)
	require.Equal(t, 404, call("POST", "/api/spending/alerts/999/read", `{}`, token, "1", "https://ztapi.vip").Code)
	require.NoError(t, r.DB.Exec(`UPDATE users SET status=2 WHERE id=1`).Error)
	require.Equal(t, 403, call("GET", "/api/spending/config", "", token, "1", "").Code)
	p, err := store.Preferences(context.Background(), 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, p.Version)
	_ = http.MethodGet
}
