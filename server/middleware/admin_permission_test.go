package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

func TestAdminPermissionAuthUsesSharedAccessTokenIdentity(t *testing.T) {
	db, user := setupZTAPIUserAuthTest(t)
	token := "admin-permission-token"
	user.Role = common.RoleFinanceUser
	user.SetAccessToken(token)
	if err := db.Save(user).Error; err != nil {
		t.Fatalf("save finance user: %v", err)
	}

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("admin-permission-test"))))
	engine.GET("/protected", AdminPermissionAuth(common.PermissionFinanceWrite), func(c *gin.Context) {
		if common.GetContextKeyBool(c, constant.ContextKeyZTAPIJWTAuthenticated) {
			t.Fatal("static access token was marked as a ZTAPI JWT")
		}
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-API-User", "1")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminPermissionAuthAcceptsZTAPILoginJWT(t *testing.T) {
	db, user := setupZTAPIUserAuthTest(t)
	user.Role = common.RoleFinanceUser
	if err := db.Save(user).Error; err != nil {
		t.Fatalf("save finance user: %v", err)
	}
	token, err := service.IssueZTAPIAccessToken(user.Id, time.Now().UTC())
	if err != nil {
		t.Fatalf("issue ZTAPI access token: %v", err)
	}

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("admin-permission-jwt"))))
	engine.GET("/protected", AdminPermissionAuth(common.PermissionFinanceWrite), func(c *gin.Context) {
		if !common.GetContextKeyBool(c, constant.ContextKeyZTAPIJWTAuthenticated) {
			t.Fatal("ZTAPI JWT authentication marker is missing")
		}
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-API-User", strconv.Itoa(user.Id))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminPermissionAuthRevalidatesSessionRoleFromDatabase(t *testing.T) {
	db, user := setupZTAPIUserAuthTest(t)
	user.Role = common.RoleRootUser
	if err := db.Save(user).Error; err != nil {
		t.Fatalf("save root user: %v", err)
	}

	protectedCalls := 0
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("admin-permission-session-refresh"))))
	engine.GET("/establish", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("username", user.Username)
		session.Set("role", common.RoleRootUser)
		session.Set("id", user.Id)
		session.Set("status", common.UserStatusEnabled)
		session.Set("group", user.Group)
		if err := session.Save(); err != nil {
			t.Fatalf("save session: %v", err)
		}
		c.Status(http.StatusNoContent)
	})
	engine.GET("/protected", AdminPermissionAuth(common.PermissionRoleWrite), func(c *gin.Context) {
		protectedCalls++
		c.Status(http.StatusNoContent)
	})

	establish := httptest.NewRecorder()
	engine.ServeHTTP(establish, httptest.NewRequest(http.MethodGet, "/establish", nil))
	if len(establish.Result().Cookies()) == 0 {
		t.Fatal("session cookie was not issued")
	}
	cookieValue := establish.Result().Cookies()[0]

	requestProtected := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/protected", nil)
		request.AddCookie(cookieValue)
		request.Header.Set("New-API-User", strconv.Itoa(user.Id))
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	first := requestProtected()
	if first.Code != http.StatusNoContent || protectedCalls != 1 {
		t.Fatalf("root session denied before demotion: status=%d calls=%d body=%s", first.Code, protectedCalls, first.Body.String())
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.Id).Update("role", common.RoleCommonUser).Error; err != nil {
		t.Fatalf("demote root user: %v", err)
	}

	second := requestProtected()
	if second.Code == http.StatusNoContent || protectedCalls != 1 {
		t.Fatalf("stale root session retained role-write permission: status=%d calls=%d body=%s", second.Code, protectedCalls, second.Body.String())
	}
}

func TestAdminPermissionAuthRejectsNamedPermissionWithoutBypass(t *testing.T) {
	db, user := setupZTAPIUserAuthTest(t)
	token := "admin-permission-support-token"
	user.Role = common.RoleSupportUser
	user.SetAccessToken(token)
	if err := db.Save(user).Error; err != nil {
		t.Fatalf("save support user: %v", err)
	}

	nextCalled := false
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("admin-permission-test"))))
	engine.GET("/protected", AdminPermissionAuth(common.PermissionBalanceWrite), func(c *gin.Context) {
		nextCalled = true
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-API-User", "1")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if nextCalled {
		t.Fatal("handler ran without the named permission")
	}
}
