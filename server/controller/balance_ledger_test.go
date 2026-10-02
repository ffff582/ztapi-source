package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/router"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

func TestBalanceAdjustmentRoutePersistsAllowlistedAuditData(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	admin, token := createBalanceLedgerOperator(t, db, "ledger-admin", common.RoleAdminUser)
	target := createBalanceLedgerTarget(t, db, "ledger-target", 1_000)

	body := `{"delta":500,"reason":"manual recharge verification","idempotency_key":"admin-ui-uuid","password":"PASSWORD_SECRET","api_key":"API_KEY_SECRET","payment_credentials":"PAYMENT_SECRET"}`
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/users/%d/balance-adjustments", target.Id), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-API-User", fmt.Sprintf("%d", admin.Id))
	request.Header.Set("X-Request-ID", "ledger-request-1")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST adjustment status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			ID             int    `json:"id"`
			UserID         int    `json:"user_id"`
			OperatorID     int    `json:"operator_id"`
			Delta          int64  `json:"delta"`
			BalanceBefore  int64  `json:"balance_before"`
			BalanceAfter   int64  `json:"balance_after"`
			Reason         string `json:"reason"`
			IdempotencyKey string `json:"idempotency_key"`
			RequestID      string `json:"request_id"`
			SourceType     string `json:"source_type"`
			Applied        bool   `json:"applied"`
		} `json:"data"`
	}
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	if !response.Success || !response.Data.Applied || response.Data.ID == 0 {
		t.Fatalf("unexpected adjustment response: %#v", response)
	}
	if response.Data.UserID != target.Id || response.Data.OperatorID != admin.Id || response.Data.Delta != 500 || response.Data.BalanceBefore != 1_000 || response.Data.BalanceAfter != 1_500 || response.Data.RequestID != "ledger-request-1" {
		t.Fatalf("unexpected ledger response: %#v", response.Data)
	}

	var audit model.Log
	if err := db.Where("user_id = ? AND type = ?", target.Id, model.LogTypeManage).Order("id DESC").First(&audit).Error; err != nil {
		t.Fatalf("load adjustment audit: %v", err)
	}
	for _, required := range []string{"balance.adjustment", fmt.Sprintf("\"target_user_id\":%d", target.Id), "\"delta\":500", "manual recharge verification", fmt.Sprintf("\"ledger_id\":%d", response.Data.ID), "\"result\":\"applied\""} {
		if !strings.Contains(audit.Other, required) {
			t.Fatalf("audit data missing %q: %s", required, audit.Other)
		}
	}
	for _, secret := range []string{"PASSWORD_SECRET", "API_KEY_SECRET", "PAYMENT_SECRET"} {
		if strings.Contains(audit.Content, secret) || strings.Contains(audit.Other, secret) {
			t.Fatalf("audit contains secret %q: content=%q other=%s", secret, audit.Content, audit.Other)
		}
	}
}

func TestBalanceLedgerRoutesUseExactNamedPermissionsAndUserFilter(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	admin, adminToken := createBalanceLedgerOperator(t, db, "ledger-admin-rbac", common.RoleAdminUser)
	finance, financeToken := createBalanceLedgerOperator(t, db, "ledger-finance", common.RoleFinanceUser)
	support, supportToken := createBalanceLedgerOperator(t, db, "ledger-support", common.RoleSupportUser)
	first := createBalanceLedgerTarget(t, db, "ledger-first", 100)
	second := createBalanceLedgerTarget(t, db, "ledger-second", 200)

	postBalanceAdjustment(t, engine, admin, adminToken, first.Id, "first-entry")
	postBalanceAdjustment(t, engine, admin, adminToken, second.Id, "second-entry")

	global := performBalanceLedgerRequest(t, engine, http.MethodGet, "/api/admin/balance-ledger?p=1&page_size=10", finance, financeToken, "")
	assertBalanceLedgerPage(t, global, 2, 2, 0)
	filtered := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/users/%d/balance-ledger?p=1&page_size=10", first.Id), finance, financeToken, "")
	assertBalanceLedgerPage(t, filtered, 1, 1, first.Id)

	deniedRead := performBalanceLedgerRequest(t, engine, http.MethodGet, "/api/admin/balance-ledger", support, supportToken, "")
	assertBalanceLedgerDenied(t, deniedRead)
	deniedWrite := performBalanceLedgerRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/balance-adjustments", first.Id), finance, financeToken, `{"delta":1,"reason":"denied","idempotency_key":"denied"}`)
	assertBalanceLedgerDenied(t, deniedWrite)
}

func TestBalanceAdjustmentCacheFailureIsRetrySafeAndAudited(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	admin, token := createBalanceLedgerOperator(t, db, "ledger-cache-admin", common.RoleAdminUser)
	target := createBalanceLedgerTarget(t, db, "ledger-cache-target", 1_000)

	originalRedisClient := common.RDB
	failingRedis := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  10 * time.Millisecond,
		WriteTimeout: 10 * time.Millisecond,
		MaxRetries:   0,
	})
	common.RDB = failingRedis
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = failingRedis.Close()
		common.RDB = originalRedisClient
	})

	body := `{"delta":500,"reason":"cache synchronization retry","idempotency_key":"cache-sync-route"}`
	first := performBalanceLedgerRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/balance-adjustments", target.Id), admin, token, body)
	var failedResponse struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := common.Unmarshal(first.Body.Bytes(), &failedResponse); err != nil {
		t.Fatalf("decode cache failure response: %v; body=%s", err, first.Body.String())
	}
	if failedResponse.Success || !strings.Contains(failedResponse.Message, "retry with the same idempotency key") {
		t.Fatalf("cache failure response = %#v", failedResponse)
	}

	var entry model.BalanceLedger
	if err := db.Where("idempotency_key = ?", "cache-sync-route").First(&entry).Error; err != nil {
		t.Fatalf("load committed cache-failure ledger: %v", err)
	}
	var persistedTarget model.User
	if err := db.First(&persistedTarget, target.Id).Error; err != nil {
		t.Fatalf("reload cache-failure target: %v", err)
	}
	if persistedTarget.Quota != 1_500 {
		t.Fatalf("committed quota = %d, want 1500", persistedTarget.Quota)
	}
	var audit model.Log
	auditNeedle := "%committed_cache_sync_failed%"
	if err := db.Where("user_id = ? AND type = ? AND other LIKE ?", target.Id, model.LogTypeManage, auditNeedle).First(&audit).Error; err != nil {
		t.Fatalf("load committed cache-failure audit: %v", err)
	}
	for _, required := range []string{fmt.Sprintf("\"ledger_id\":%d", entry.ID), "\"result\":\"committed_cache_sync_failed\""} {
		if !strings.Contains(audit.Other, required) {
			t.Fatalf("cache-failure audit missing %q: %s", required, audit.Other)
		}
	}

	common.RedisEnabled = false
	retry := performBalanceLedgerRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/balance-adjustments", target.Id), admin, token, body)
	var retryResponse struct {
		Success bool `json:"success"`
		Data    struct {
			ID      int  `json:"id"`
			Applied bool `json:"applied"`
		} `json:"data"`
	}
	if err := common.Unmarshal(retry.Body.Bytes(), &retryResponse); err != nil {
		t.Fatalf("decode cache retry response: %v; body=%s", err, retry.Body.String())
	}
	if !retryResponse.Success || retryResponse.Data.Applied || retryResponse.Data.ID != entry.ID {
		t.Fatalf("cache retry response = %#v; body=%s", retryResponse, retry.Body.String())
	}
	if err := db.First(&persistedTarget, target.Id).Error; err != nil {
		t.Fatalf("reload target after cache retry: %v", err)
	}
	if persistedTarget.Quota != 1_500 {
		t.Fatalf("quota after cache retry = %d, want 1500", persistedTarget.Quota)
	}
}

func setupBalanceLedgerControllerTest(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalSQLite := common.UsingSQLite
	originalMySQL := common.UsingMySQL
	originalPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalGlobalRateLimit := common.GlobalApiRateLimitEnable
	originalCriticalRateLimit := common.CriticalRateLimitEnable
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.GlobalApiRateLimitEnable = false
	common.CriticalRateLimitEnable = false
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access sqlite: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.User{}, &model.Log{}, &model.BalanceLedger{}); err != nil {
		t.Fatalf("migrate controller tables: %v", err)
	}

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("balance-ledger-test"))))
	engine.Use(middleware.RequestId())
	router.SetApiRouter(engine)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.UsingSQLite = originalSQLite
		common.UsingMySQL = originalMySQL
		common.UsingPostgreSQL = originalPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.GlobalApiRateLimitEnable = originalGlobalRateLimit
		common.CriticalRateLimitEnable = originalCriticalRateLimit
	})
	return db, engine
}

func createBalanceLedgerOperator(t *testing.T, db *gorm.DB, username string, role int) (model.User, string) {
	t.Helper()
	token := username + "-access-token"
	user := model.User{Username: username, Password: "not-used", Role: role, Status: common.UserStatusEnabled, AffCode: username + "-aff"}
	user.SetAccessToken(token)
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}
	return user, token
}

func createBalanceLedgerTarget(t *testing.T, db *gorm.DB, username string, quota int) model.User {
	t.Helper()
	user := model.User{Username: username, Password: "not-used", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: quota, AffCode: username + "-aff"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	return user
}

func postBalanceAdjustment(t *testing.T, engine *gin.Engine, operator model.User, token string, userID int, key string) {
	t.Helper()
	body := fmt.Sprintf(`{"delta":10,"reason":"seed ledger","idempotency_key":%q}`, key)
	recorder := performBalanceLedgerRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/balance-adjustments", userID), operator, token, body)
	var response struct {
		Success bool `json:"success"`
	}
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil || !response.Success {
		t.Fatalf("seed adjustment failed: err=%v body=%s", err, recorder.Body.String())
	}
}

func performBalanceLedgerRequest(t *testing.T, engine *gin.Engine, method, target string, operator model.User, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-API-User", fmt.Sprintf("%d", operator.Id))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func assertBalanceLedgerDenied(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	var response struct {
		Success bool `json:"success"`
	}
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode denied response: %v; body=%s", err, recorder.Body.String())
	}
	if response.Success {
		t.Fatalf("request unexpectedly succeeded: %s", recorder.Body.String())
	}
}

func assertBalanceLedgerPage(t *testing.T, recorder *httptest.ResponseRecorder, total, itemCount, expectedUserID int) {
	t.Helper()
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Total int `json:"total"`
			Items []struct {
				UserID int `json:"user_id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode ledger page: %v; body=%s", err, recorder.Body.String())
	}
	if !response.Success || response.Data.Total != total || len(response.Data.Items) != itemCount {
		t.Fatalf("unexpected ledger page: %#v; body=%s", response, recorder.Body.String())
	}
	if expectedUserID != 0 {
		for _, item := range response.Data.Items {
			if item.UserID != expectedUserID {
				t.Fatalf("filtered item user_id = %d, want %d", item.UserID, expectedUserID)
			}
		}
	}
}
