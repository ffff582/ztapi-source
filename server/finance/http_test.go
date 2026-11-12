package finance

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/support"
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

func TestFinanceSupportReaderIsMonthBoundedAndLeavesPrivateFactsUntouched(t *testing.T) {
	base, token := api(t, common.RoleRootUser)
	h := base.(*handler)
	reader := func(ctx context.Context, start, end time.Time) ([]support.FinancialUsage, error) {
		require.Equal(t, "2026-09-01T00:00:00+08:00", start.Format(time.RFC3339))
		require.Equal(t, "2026-10-01T00:00:00+08:00", end.Format(time.RFC3339))
		require.NotNil(t, ctx)
		return []support.FinancialUsage{supportUsageFixture()}, nil
	}
	withSupport := NewHandlerWithSupportReader(h.source, h.store, h.origin, h.now, reader)
	w := request(withSupport, token, "GET", prefix+"report?month=2026-09", "", "")
	require.Equal(t, 200, w.Code)
	var response struct {
		Data Report `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Data.CustomerServiceCosts, 1)
	require.Equal(t, "available", reportMap(t, response.Data)["support_source_status"])
	f, err := h.store.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, f)
	// The support projection is not a fact history or a writable settlement.
	w = request(withSupport, token, "GET", prefix+"history", "", "")
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "support-call-1")
}

func TestFinanceSupportOutageDoesNotReturn503OrLeakSourceErrors(t *testing.T) {
	base, token := api(t, common.RoleRootUser)
	h := base.(*handler)
	reader := func(context.Context, time.Time, time.Time) ([]support.FinancialUsage, error) {
		return []support.FinancialUsage{supportUsageFixture()}, errors.New("secret path or database credentials")
	}
	withSupport := NewHandlerWithSupportReader(h.source, h.store, h.origin, h.now, reader)
	for _, endpoint := range []string{"report", "export"} {
		w := request(withSupport, token, "GET", prefix+endpoint+"?month=2026-09", "", "")
		require.Equal(t, 200, w.Code)
		require.Contains(t, w.Body.String(), "support_source_unavailable")
		require.NotContains(t, w.Body.String(), "secret path")
		require.NotContains(t, w.Body.String(), "support-call-1", "discard partial reader data on errors")
		if endpoint == "report" {
			require.Contains(t, w.Body.String(), `"support_source_status":"unavailable"`)
			require.Contains(t, w.Body.String(), `"actual_cost":null`)
			require.Contains(t, w.Body.String(), `"gross_profit":null`)
		}
	}
}

func TestFinanceSupportReaderCurrentMonthStopsAtSnapshotCutoff(t *testing.T) {
	base, token := api(t, common.RoleRootUser)
	h := base.(*handler)
	reader := func(_ context.Context, start, end time.Time) ([]support.FinancialUsage, error) {
		require.Equal(t, "2026-10-01T00:00:00+08:00", start.Format(time.RFC3339))
		require.Equal(t, "2026-10-26T00:01:00Z", end.UTC().Format(time.RFC3339))
		return nil, nil
	}
	w := request(NewHandlerWithSupportReader(h.source, h.store, h.origin, h.now, reader), token, "GET", prefix+"report?month=2026-10", "", "")
	require.Equal(t, 200, w.Code)
}
