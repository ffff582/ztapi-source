package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func TestWorkbenchReleaseDisabledByDefault(t *testing.T) {
	for _, config := range []string{"", t.TempDir()} {
		t.Setenv("ZTAPI_WORKBENCH_PILOT_CONFIG", config)
		engine := gin.New()
		mountConfiguredWorkbenchPilot(engine)
		for _, r := range []struct{ method, path string }{
			{http.MethodGet, "/.well-known/ztapi-workbench"},
			{http.MethodGet, "/api/workbench/models"},
			{http.MethodPost, "/__workbench_budget/responses"},
		} {
			out := httptest.NewRecorder()
			engine.ServeHTTP(out, httptest.NewRequest(r.method, r.path, nil))
			if out.Code != http.StatusNotFound {
				t.Fatalf("unexpected %s %s: %d", r.method, r.path, out.Code)
			}
		}
	}
}

func TestWorkbenchReleaseDisabledWithWebFallback(t *testing.T) {
	master := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = master })
	t.Setenv("FRONTEND_BASE_URL", "https://frontend.invalid")
	t.Setenv(workbenchPilotConfigEnv, "")
	engine := gin.New()
	SetRouter(engine, ThemeAssets{})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/.well-known/ztapi-workbench"},
		{http.MethodGet, "/api/workbench/models"},
		{http.MethodPost, "/__workbench_budget/reserve"},
	} {
		out := httptest.NewRecorder()
		engine.ServeHTTP(out, httptest.NewRequest(route.method, route.path, nil))
		if out.Code != http.StatusNotFound || out.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: got %d with cache policy %q", route.method, route.path, out.Code, out.Header().Get("Cache-Control"))
		}
	}
	out := httptest.NewRecorder()
	engine.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/unrelated-page", nil))
	if out.Code != http.StatusMovedPermanently || out.Header().Get("Location") != "https://frontend.invalid/unrelated-page" {
		t.Errorf("website fallback changed: %d %q", out.Code, out.Header().Get("Location"))
	}
}
