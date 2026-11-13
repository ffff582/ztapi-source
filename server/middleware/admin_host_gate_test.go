package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

func TestZTAPIAdminHostGateRejectsCommonJWTAndAllowsStaff(t *testing.T) {
	db, user := setupZTAPIUserAuthTest(t)
	issueToken := func(role int) string {
		t.Helper()
		if err := db.Model(&model.User{}).Where("id = ?", user.Id).Update("role", role).Error; err != nil {
			t.Fatalf("update role %d: %v", role, err)
		}
		token, err := service.IssueZTAPIAccessToken(user.Id, time.Now().UTC())
		if err != nil {
			t.Fatalf("issue role %d JWT: %v", role, err)
		}
		return token
	}

	nextCalls := 0
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("admin-host-gate-test"))))
	engine.Use(ZTAPIAdminHostGate())
	engine.GET("/api/user/models", func(c *gin.Context) {
		nextCalls++
		c.Status(http.StatusNoContent)
	})

	request := func(host, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/user/models", nil)
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("New-API-User", strconv.Itoa(user.Id))
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, req)
		return recorder
	}

	commonToken := issueToken(common.RoleCommonUser)
	commonResponse := request("admin.ztapi.vip", commonToken)
	if commonResponse.Code != http.StatusForbidden || nextCalls != 0 {
		t.Fatalf("common admin-host request = status:%d calls:%d body:%s", commonResponse.Code, nextCalls, commonResponse.Body.String())
	}

	publicResponse := request("ztapi.vip", commonToken)
	if publicResponse.Code != http.StatusNoContent || nextCalls != 1 {
		t.Fatalf("common non-admin-host request = status:%d calls:%d body:%s", publicResponse.Code, nextCalls, publicResponse.Body.String())
	}

	for _, role := range []int{
		common.RoleSupportUser,
		common.RoleFinanceUser,
		common.RoleAdminUser,
		common.RoleRootUser,
	} {
		response := request("admin.ztapi.vip:443", issueToken(role))
		if response.Code != http.StatusNoContent {
			t.Fatalf("role %d admin-host request status = %d, want 204; body=%s", role, response.Code, response.Body.String())
		}
	}
}

func TestZTAPIAdminHostGateAllowsOnlySessionBootstrapWithoutStaffIdentity(t *testing.T) {
	setupZTAPIUserAuthTest(t)
	nextCalls := 0
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("admin-host-bootstrap-test"))))
	engine.Use(ZTAPIAdminHostGate())
	for _, path := range []string{
		"/api/auth/login",
		"/api/auth/refresh",
		"/api/auth/logout",
	} {
		engine.POST(path, func(c *gin.Context) {
			nextCalls++
			c.Status(http.StatusNoContent)
		})
	}
	engine.GET("/api/status", func(c *gin.Context) {
		nextCalls++
		c.Status(http.StatusNoContent)
	})

	for _, path := range []string{
		"/api/auth/login",
		"/api/auth/refresh",
		"/api/auth/logout",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Host = "admin.ztapi.vip"
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("bootstrap path %s status = %d, want 204; body=%s", path, recorder.Code, recorder.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "admin.ztapi.vip"
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized || nextCalls != 3 {
		t.Fatalf("unauthenticated admin API = status:%d calls:%d body:%s", recorder.Code, nextCalls, recorder.Body.String())
	}
}
