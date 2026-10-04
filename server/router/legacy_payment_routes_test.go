package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

var legacyPaymentRouteContracts = []string{
	http.MethodPost + " /api/stripe/webhook",
	http.MethodPost + " /api/creem/webhook",
	http.MethodPost + " /api/waffo/webhook",
	http.MethodPost + " /api/waffo-pancake/webhook/:env",
	http.MethodPost + " /api/user/epay/notify",
	http.MethodGet + " /api/user/epay/notify",
	http.MethodPost + " /api/user/topup",
	http.MethodPost + " /api/user/pay",
	http.MethodPost + " /api/user/amount",
	http.MethodPost + " /api/user/stripe/pay",
	http.MethodPost + " /api/user/stripe/amount",
	http.MethodPost + " /api/user/creem/pay",
	http.MethodPost + " /api/user/waffo/amount",
	http.MethodPost + " /api/user/waffo/pay",
	http.MethodPost + " /api/user/waffo-pancake/amount",
	http.MethodPost + " /api/user/waffo-pancake/pay",
	http.MethodPost + " /api/subscription/balance/pay",
	http.MethodPost + " /api/subscription/epay/pay",
	http.MethodPost + " /api/subscription/stripe/pay",
	http.MethodPost + " /api/subscription/creem/pay",
	http.MethodPost + " /api/subscription/waffo-pancake/pay",
	http.MethodPost + " /api/subscription/epay/notify",
	http.MethodGet + " /api/subscription/epay/notify",
	http.MethodGet + " /api/subscription/epay/return",
	http.MethodPost + " /api/subscription/epay/return",
}

var governedAdminTopUpRouteContracts = []string{
	http.MethodPost + " /api/admin/topups/:id/complete",
	http.MethodPost + " /api/admin/topups/:id/reject",
}

func TestLegacyPaymentRoutesAreAbsentByDefault(t *testing.T) {
	t.Setenv(legacyPaymentEnabledEnv, "")
	routes := registeredAPIRoutes()

	for _, route := range legacyPaymentRouteContracts {
		if _, exists := routes[route]; exists {
			t.Errorf("legacy payment route must be absent by default: %s", route)
		}
	}
	assertGovernedAdminTopUpRoutes(t, routes)
}

func TestLegacyPaymentRoutesCanBeExplicitlyEnabled(t *testing.T) {
	t.Setenv(legacyPaymentEnabledEnv, "true")
	routes := registeredAPIRoutes()

	for _, route := range legacyPaymentRouteContracts {
		if _, exists := routes[route]; !exists {
			t.Errorf("legacy payment route missing with explicit opt-in: %s", route)
		}
	}
	assertGovernedAdminTopUpRoutes(t, routes)
}

func registeredAPIRoutes() map[string]struct{} {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	routes := make(map[string]struct{}, len(engine.Routes()))
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}
	return routes
}

func assertGovernedAdminTopUpRoutes(t *testing.T, routes map[string]struct{}) {
	t.Helper()
	for _, route := range governedAdminTopUpRouteContracts {
		if _, exists := routes[route]; !exists {
			t.Errorf("governed admin top-up route must remain registered: %s", route)
		}
	}
}
