package router

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type ztAPIRealRouterResponse struct {
	Success bool `json:"success"`
	Data    struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		User        struct {
			ID       int    `json:"id"`
			Username string `json:"username"`
			Role     int    `json:"role"`
			Group    string `json:"group"`
		} `json:"user"`
	} `json:"data"`
	Message string `json:"message"`
}

func TestAuthRouterRealLoginJWTCallsTokenLogAndSessionRoutes(t *testing.T) {
	setupZTAPIAuthRouterTestDB(t)
	engine := newZTAPIRealRouter(t, false)

	register := performZTAPIRouterRequest(
		engine,
		http.MethodPost,
		"/api/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		"",
		"192.0.2.10:1000",
	)
	if register.Code != http.StatusOK {
		t.Fatalf("registration status = %d; body=%s", register.Code, register.Body.String())
	}
	login := performZTAPIRouterRequest(
		engine,
		http.MethodPost,
		"/api/auth/login",
		`{"username":"alice","password":"at-least-ten"}`,
		"",
		"192.0.2.11:1000",
	)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d; body=%s", login.Code, login.Body.String())
	}
	var loginResponse ztAPIRealRouterResponse
	if err := common.Unmarshal(login.Body.Bytes(), &loginResponse); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if !loginResponse.Success || loginResponse.Data.AccessToken == "" || loginResponse.Data.ExpiresIn != 900 {
		t.Fatalf("login response = %#v", loginResponse)
	}

	for _, target := range []string{
		"/api/token/",
		"/api/log/self",
		"/api/auth/session",
	} {
		t.Run(target, func(t *testing.T) {
			recorder := performZTAPIRouterRequest(
				engine,
				http.MethodGet,
				target,
				"",
				"Bearer "+loginResponse.Data.AccessToken,
				"192.0.2.12:1000",
			)
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%s", target, recorder.Code, recorder.Body.String())
			}
			var response struct {
				Success bool `json:"success"`
			}
			if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode GET %s response: %v; body=%s", target, err, recorder.Body.String())
			}
			if !response.Success {
				t.Fatalf("GET %s failed: %s", target, recorder.Body.String())
			}
			if strings.Contains(strings.ToLower(recorder.Body.String()), "password") {
				t.Fatalf("GET %s leaked a password field: %s", target, recorder.Body.String())
			}
		})
	}
}

func TestAuthRouterAdminHostRejectsOrdinaryLoginAndExistingJWT(t *testing.T) {
	setupZTAPIAuthRouterTestDB(t)
	engine := newZTAPIRealRouter(t, false)

	register := performZTAPIRouterRequest(
		engine,
		http.MethodPost,
		"/api/auth/register",
		`{"username":"alice","password":"at-least-ten"}`,
		"",
		"192.0.2.13:1000",
	)
	if register.Code != http.StatusOK {
		t.Fatalf("registration status = %d; body=%s", register.Code, register.Body.String())
	}
	var registration ztAPIRealRouterResponse
	if err := common.Unmarshal(register.Body.Bytes(), &registration); err != nil {
		t.Fatalf("decode registration response: %v", err)
	}

	loginRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/login",
		bytes.NewBufferString(`{"username":"alice","password":"at-least-ten"}`),
	)
	loginRequest.Host = "admin.ztapi.vip"
	loginRequest.RemoteAddr = "192.0.2.14:1000"
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	engine.ServeHTTP(loginRecorder, loginRequest)
	if loginRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary admin-host login status = %d, want 401; body=%s", loginRecorder.Code, loginRecorder.Body.String())
	}

	apiRequest := httptest.NewRequest(http.MethodGet, "/api/user/models", nil)
	apiRequest.Host = "admin.ztapi.vip"
	apiRequest.RemoteAddr = "192.0.2.15:1000"
	apiRequest.Header.Set("Authorization", "Bearer "+registration.Data.AccessToken)
	apiRequest.Header.Set("New-API-User", fmt.Sprint(registration.Data.User.ID))
	apiRecorder := httptest.NewRecorder()
	engine.ServeHTTP(apiRecorder, apiRequest)
	if apiRecorder.Code != http.StatusForbidden {
		t.Fatalf("ordinary admin-host API status = %d, want 403; body=%s", apiRecorder.Code, apiRecorder.Body.String())
	}
}

func TestAuthRouterLegacyPublicAuthRoutesReturnNotFound(t *testing.T) {
	engine := newZTAPIRealRouter(t, false)
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/user/login"},
		{method: http.MethodPost, path: "/api/user/register"},
		{method: http.MethodGet, path: "/api/user/logout"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			recorder := performZTAPIRouterRequest(
				engine,
				test.method,
				test.path,
				`{"username":"legacy","password":"legacy-password"}`,
				"",
				"192.0.2.20:1000",
			)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404; body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAuthRouterPublicEndpointsApplyAnonymousBodyLimits(t *testing.T) {
	originalLimit := constant.AnonymousRequestBodyLimitKB
	constant.AnonymousRequestBodyLimitKB = 1
	t.Cleanup(func() { constant.AnonymousRequestBodyLimitKB = originalLimit })
	engine := newZTAPIRealRouter(t, false)
	oversized := strings.Repeat("x", 1025)

	for _, path := range []string{
		"/api/auth/register",
		"/api/auth/login",
		"/api/auth/refresh",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := performZTAPIRouterRequest(
				engine,
				http.MethodPost,
				path,
				oversized,
				"",
				"192.0.2.30:1000",
			)
			if recorder.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("POST %s status = %d, want 413; body=%s", path, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAuthRouterPublicEndpointsApplyExplicitRateLimits(t *testing.T) {
	originalEnabled := common.CriticalRateLimitEnable
	originalNumber := common.CriticalRateLimitNum
	originalDuration := common.CriticalRateLimitDuration
	common.CriticalRateLimitEnable = true
	common.CriticalRateLimitNum = 1
	common.CriticalRateLimitDuration = 60
	t.Cleanup(func() {
		common.CriticalRateLimitEnable = originalEnabled
		common.CriticalRateLimitNum = originalNumber
		common.CriticalRateLimitDuration = originalDuration
	})
	engine := newZTAPIRealRouter(t, true)

	for index, path := range []string{
		"/api/auth/register",
		"/api/auth/login",
		"/api/auth/refresh",
	} {
		t.Run(path, func(t *testing.T) {
			remote := fmt.Sprintf("192.0.2.%d:1000", 40+index)
			first := performZTAPIRouterRequest(engine, http.MethodPost, path, `{}`, "", remote)
			if first.Code == http.StatusTooManyRequests {
				t.Fatalf("first POST %s was rate limited", path)
			}
			second := performZTAPIRouterRequest(engine, http.MethodPost, path, `{}`, "", remote)
			if second.Code != http.StatusTooManyRequests {
				t.Fatalf("second POST %s status = %d, want 429; body=%s", path, second.Code, second.Body.String())
			}
		})
	}
}

func setupZTAPIAuthRouterTestDB(t *testing.T) *gorm.DB {
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
	dsn := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)
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
	if err := db.AutoMigrate(
		&model.User{},
		&model.AuthSession{},
		&model.Token{},
		&model.Log{},
	); err != nil {
		t.Fatalf("migrate real-router tables: %v", err)
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
		t.Fatalf("configure signing key: %v", err)
	}
	return db
}

func newZTAPIRealRouter(t *testing.T, keepRateLimit bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	originalGlobalRateLimit := common.GlobalApiRateLimitEnable
	originalCriticalRateLimit := common.CriticalRateLimitEnable
	common.RedisEnabled = false
	common.GlobalApiRateLimitEnable = false
	if !keepRateLimit {
		common.CriticalRateLimitEnable = false
	}
	t.Cleanup(func() {
		common.RedisEnabled = originalRedisEnabled
		common.GlobalApiRateLimitEnable = originalGlobalRateLimit
		common.CriticalRateLimitEnable = originalCriticalRateLimit
	})

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("ztapi-auth-router-test"))))
	SetApiRouter(engine)
	return engine
}

func performZTAPIRouterRequest(
	engine *gin.Engine,
	method string,
	target string,
	body string,
	authorization string,
	remoteAddr string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.RemoteAddr = remoteAddr
	request.Header.Set("User-Agent", "ZTAPI Router Test")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}
