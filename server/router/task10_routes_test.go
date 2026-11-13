/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

func TestTask10AdminBackendRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	routes := make(map[string]struct{})
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}
	for _, expected := range []string{
		http.MethodPost + " /api/user/topup/usdt-trc20/orders",
		http.MethodGet + " /api/user/topup/usdt-trc20/orders/:trade_no",
		http.MethodPost + " /api/user/topup/usdt-trc20/orders/:trade_no/cancel",
		http.MethodGet + " /api/admin/topups",
		http.MethodGet + " /api/admin/topups/export",
		http.MethodPost + " /api/admin/topups/:id/complete",
		http.MethodPost + " /api/admin/topups/:id/reject",
		http.MethodGet + " /api/admin/request-logs",
		http.MethodGet + " /api/admin/request-logs/export",
		http.MethodGet + " /api/admin/audit-logs",
		http.MethodGet + " /api/admin/audit-logs/export",
		http.MethodGet + " /api/admin/settings",
		http.MethodPatch + " /api/admin/settings/:key",
		http.MethodGet + " /api/admin/balance-ledger/export",
	} {
		if _, ok := routes[expected]; !ok {
			t.Errorf("missing route %s", expected)
		}
	}
}

func TestUSDTTopUpRoutesRequireZTAPIBearerAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := newZTAPIRealRouter(t, false)
	request, err := http.NewRequest(http.MethodPost, "/api/user/topup/usdt-trc20/orders", bytes.NewBufferString(`{"amount":10}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUSDTTopUpRoutesAcceptExistingAuthenticatedUserSession(t *testing.T) {
	db := setupZTAPIAuthRouterTestDB(t)
	user := model.User{
		Username: "session-customer",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	engine := newZTAPIRealRouter(t, false)
	engine.GET("/__test/session", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("id", user.Id)
		session.Set("username", user.Username)
		session.Set("role", user.Role)
		session.Set("status", user.Status)
		session.Set("group", user.Group)
		if err := session.Save(); err != nil {
			t.Fatalf("save session: %v", err)
		}
		c.Status(http.StatusNoContent)
	})

	seed := httptest.NewRecorder()
	engine.ServeHTTP(seed, httptest.NewRequest(http.MethodGet, "/__test/session", nil))
	cookies := seed.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("test session did not set a cookie")
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/user/topup/usdt-trc20/orders",
		bytes.NewBufferString(`{"amount":10}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("New-API-User", strconv.Itoa(user.Id))
	request.AddCookie(cookies[0])
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code == http.StatusUnauthorized {
		t.Fatalf("authenticated user session was rejected: %s", recorder.Body.String())
	}
}
