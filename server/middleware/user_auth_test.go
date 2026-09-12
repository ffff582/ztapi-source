package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestUserAuthAcceptsLoginJWTWithoutLegacyHeadersAndLoadsCurrentUser(t *testing.T) {
	db, user := setupZTAPIUserAuthTest(t)
	token, err := service.IssueZTAPIAccessToken(user.Id, time.Now().UTC())
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	if err := db.Model(user).Updates(map[string]any{
		"role":   common.RoleAdminUser,
		"group":  "updated-group",
		"status": common.UserStatusEnabled,
	}).Error; err != nil {
		t.Fatalf("update current user: %v", err)
	}

	var contextValues map[string]any
	engine := gin.New()
	engine.GET("/protected", ZTAPIUserAuth(), func(c *gin.Context) {
		contextValues = make(map[string]any)
		for _, key := range []string{
			"id",
			"username",
			"role",
			"status",
			"group",
			"user_group",
			"use_access_token",
		} {
			value, exists := c.Get(key)
			if !exists {
				t.Fatalf("context key %q is missing", key)
			}
			contextValues[key] = value
		}
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := contextValues["id"]; got != user.Id {
		t.Fatalf("context id = %#v, want %d", got, user.Id)
	}
	if got := contextValues["username"]; got != user.Username {
		t.Fatalf("context username = %#v, want %q", got, user.Username)
	}
	if got := contextValues["role"]; got != common.RoleAdminUser {
		t.Fatalf("context role = %#v, want %d", got, common.RoleAdminUser)
	}
	if got := contextValues["status"]; got != common.UserStatusEnabled {
		t.Fatalf("context status = %#v, want %d", got, common.UserStatusEnabled)
	}
	if got := contextValues["group"]; got != "updated-group" {
		t.Fatalf("context group = %#v, want updated-group", got)
	}
	if got := contextValues["user_group"]; got != "updated-group" {
		t.Fatalf("context user_group = %#v, want updated-group", got)
	}
	if got := contextValues["use_access_token"]; got != true {
		t.Fatalf("context use_access_token = %#v, want true", got)
	}
}

func TestUserAuthRejectsMissingOrMalformedBearerAuthorization(t *testing.T) {
	setupZTAPIUserAuthTest(t)
	tests := []string{
		"",
		"Token abc",
		"Bearer",
		"Bearer abc extra",
		"bearer abc",
	}

	for _, authorization := range tests {
		t.Run(authorization, func(t *testing.T) {
			nextCalled := false
			engine := gin.New()
			engine.GET("/protected", ZTAPIUserAuth(), func(c *gin.Context) {
				nextCalled = true
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}
			recorder := httptest.NewRecorder()

			engine.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", recorder.Code, recorder.Body.String())
			}
			if nextCalled {
				t.Fatal("protected handler ran for malformed authorization")
			}
			if strings.Contains(recorder.Body.String(), "abc") {
				t.Fatalf("response leaked presented credential: %s", recorder.Body.String())
			}
		})
	}
}

func TestUserAuthRejectsDisabledDeletedAndMissingUsers(t *testing.T) {
	db, user := setupZTAPIUserAuthTest(t)
	now := time.Now().UTC()
	validToken, err := service.IssueZTAPIAccessToken(user.Id, now)
	if err != nil {
		t.Fatalf("issue valid access token: %v", err)
	}
	missingToken, err := service.IssueZTAPIAccessToken(user.Id+1000, now)
	if err != nil {
		t.Fatalf("issue missing-user access token: %v", err)
	}

	assertZTAPIUserAuthStatus(t, validToken, http.StatusNoContent)
	if err := db.Model(user).Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}
	assertZTAPIUserAuthStatus(t, validToken, http.StatusUnauthorized)
	if err := db.Model(user).Update("status", common.UserStatusEnabled).Error; err != nil {
		t.Fatalf("enable user: %v", err)
	}
	if err := db.Delete(user).Error; err != nil {
		t.Fatalf("delete user: %v", err)
	}
	assertZTAPIUserAuthStatus(t, validToken, http.StatusUnauthorized)
	assertZTAPIUserAuthStatus(t, missingToken, http.StatusUnauthorized)
}

func setupZTAPIUserAuthTest(t *testing.T) (*gorm.DB, *model.User) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalSQLite := common.UsingSQLite
	originalMySQL := common.UsingMySQL
	originalPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access sqlite connection: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.UsingSQLite = originalSQLite
		common.UsingMySQL = originalMySQL
		common.UsingPostgreSQL = originalPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})
	if err := service.ConfigureZTAPISessionSigningKey("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("configure session key: %v", err)
	}
	user := &model.User{
		Username: "auth-user-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Password: "not-used",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return db, user
}

func assertZTAPIUserAuthStatus(t *testing.T, token string, wantStatus int) {
	t.Helper()
	engine := gin.New()
	engine.GET("/protected", ZTAPIUserAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
}
