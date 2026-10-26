package finance

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testSource struct {
	s    Snapshot
	role int
}

func (s *testSource) Load(context.Context, time.Time) (Snapshot, error) { return s.s, nil }
func (s *testSource) Identity(context.Context, int) (User, error) {
	return User{ID: 1, Role: s.role, Status: 1}, nil
}
func api(t *testing.T, role int) (http.Handler, string) {
	t.Helper()
	require.NoError(t, service.ConfigureZTAPISessionSigningKey(strings.Repeat("test-only-finance-key", 3)))
	token, e := service.IssueZTAPIAccessToken(1, moment("2026-10-26T00:00:00Z"))
	require.NoError(t, e)
	h := NewHandler(&testSource{fixture(), role}, newStore(t), "https://admin.example.test", func() time.Time { return moment("2026-10-26T00:01:00Z") })
	return h, token
}
func request(h http.Handler, token, method, path, body, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("New-Api-User", "1")
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestFinancePermissionAndNoAuthenticationFailClosed(t *testing.T) {
	for _, role := range []int{1, common.RoleSupportUser, 999} {
		h, token := api(t, role)
		require.Equal(t, 403, request(h, token, "GET", "/api/admin/finance-reporting/report?month=2026-09", "", "").Code)
	}
	h, _ := api(t, common.RoleRootUser)
	require.Equal(t, 401, request(h, "", "GET", "/api/admin/finance-reporting/report?month=2026-09", "", "").Code)
}
func TestFinanceReadReportAndSameOriginMutation(t *testing.T) {
	h, token := api(t, common.RoleFinanceUser)
	w := request(h, token, "GET", "/api/admin/finance-reporting/report?month=2026-09", "", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"gross_profit":null`)
	body := `{"kind":"funding","ref":"ledger:1","funding":{"kind":"cash","amount":"100","currency":"USDT","rate":"1","received_at":1789516800,"evidence":"bank123"},"previous_id":0,"idempotency_key":"http-test-key"}`
	require.Equal(t, 403, request(h, token, "POST", "/api/admin/finance-reporting/facts", body, "https://evil.example").Code)
	require.Equal(t, 201, request(h, token, "POST", "/api/admin/finance-reporting/facts", body, "https://admin.example.test").Code)
	w = request(h, token, "GET", "/api/admin/finance-reporting/report?month=2026-09", "", "")
	require.Contains(t, w.Body.String(), `"revenue":"20.000000"`)
	require.Equal(t, 400, request(h, token, "POST", "/api/admin/finance-reporting/facts", strings.Replace(body, "ledger:1", "ledger:99999", 1), "https://admin.example.test").Code)
}
func TestCSVFormulaNeutralization(t *testing.T) {
	require.Equal(t, "'=cmd", csvSafe("=cmd"))
	require.Equal(t, "'\t=cmd", csvSafe("\t=cmd"))
	require.Equal(t, "safe", csvSafe("safe"))
}
