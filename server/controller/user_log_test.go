package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/router"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestUserLogRouteReturnsOnlyPublicAllowlistDTO(t *testing.T) {
	db := setupUserLogRouteTestDB(t)
	user := model.User{
		Username:    "user-log-alice",
		Password:    "not-used-by-this-test",
		DisplayName: "Alice",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		Setting:     "{}",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	const (
		channelSentinel    = "PRIVATE_CHANNEL_NAME_SENTINEL"
		upstreamSentinel   = "UPSTREAM_REQUEST_SENTINEL"
		groupSentinel      = "INTERNAL_GROUP_SENTINEL"
		credentialSentinel = "sk-upstream-secret-sentinel"
		otherSentinel      = "INTERNAL_OTHER_SENTINEL"
	)
	logEntry := model.Log{
		UserId:            user.Id,
		Username:          user.Username,
		CreatedAt:         1_900_000_123,
		Type:              model.LogTypeConsume,
		Content:           "private content " + channelSentinel,
		TokenName:         credentialSentinel,
		ModelName:         "gpt-4o",
		Quota:             1_250_000,
		PromptTokens:      11,
		CompletionTokens:  7,
		UseTime:           3,
		IsStream:          true,
		ChannelId:         987_654_321,
		ChannelName:       channelSentinel,
		TokenId:           765_432_109,
		Group:             groupSentinel,
		Ip:                "198.51.100.77",
		RequestId:         "ztapi-request-7",
		UpstreamRequestId: upstreamSentinel,
		Other: fmt.Sprintf(
			`{"credential":%q,"channel_name":%q,"internal":%q}`,
			credentialSentinel,
			channelSentinel,
			otherSentinel,
		),
	}
	if err := db.Create(&logEntry).Error; err != nil {
		t.Fatalf("create user log: %v", err)
	}

	accessToken, err := service.IssueZTAPIAccessToken(user.Id, time.Now().UTC())
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	engine := gin.New()
	router.SetApiRouter(engine)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/log/self?p=1&page_size=10&request_id=ztapi-request-7",
		nil,
	)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.RemoteAddr = "192.0.2.50:3000"
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET self logs status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Page     int `json:"page"`
			PageSize int `json:"page_size"`
			Total    int `json:"total"`
			Items    []struct {
				Timestamp        int64   `json:"timestamp"`
				RequestID        string  `json:"request_id"`
				Model            string  `json:"model"`
				Status           string  `json:"status"`
				Latency          int     `json:"latency"`
				PromptTokens     int     `json:"prompt_tokens"`
				CompletionTokens int     `json:"completion_tokens"`
				TotalTokens      int     `json:"total_tokens"`
				BilledAmount     float64 `json:"billed_amount"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	if !response.Success || response.Data.Page != 1 || response.Data.PageSize != 10 || response.Data.Total != 1 {
		t.Fatalf("unexpected page envelope: %#v", response)
	}
	if len(response.Data.Items) != 1 {
		t.Fatalf("items length = %d, want 1; body=%s", len(response.Data.Items), recorder.Body.String())
	}
	item := response.Data.Items[0]
	if item.Timestamp != logEntry.CreatedAt ||
		item.RequestID != logEntry.RequestId ||
		item.Model != logEntry.ModelName ||
		item.Status != "success" ||
		item.Latency != 3 ||
		item.PromptTokens != 11 ||
		item.CompletionTokens != 7 ||
		item.TotalTokens != 18 ||
		item.BilledAmount != 2.5 {
		t.Fatalf("public item = %#v, want stable allowlisted values", item)
	}

	raw := recorder.Body.String()
	for _, forbiddenField := range []string{
		`"id"`,
		`"user_id"`,
		`"created_at"`,
		`"type"`,
		`"content"`,
		`"username"`,
		`"token_name"`,
		`"model_name"`,
		`"quota"`,
		`"use_time"`,
		`"is_stream"`,
		`"channel"`,
		`"channel_name"`,
		`"token_id"`,
		`"group"`,
		`"ip"`,
		`"upstream_request_id"`,
		`"other"`,
	} {
		if strings.Contains(raw, forbiddenField) {
			t.Fatalf("response contains forbidden field %s: %s", forbiddenField, raw)
		}
	}
	for _, sentinel := range []string{
		channelSentinel,
		upstreamSentinel,
		groupSentinel,
		credentialSentinel,
		otherSentinel,
		"987654321",
		"765432109",
		"198.51.100.77",
	} {
		if strings.Contains(raw, sentinel) {
			t.Fatalf("response contains forbidden sentinel %q: %s", sentinel, raw)
		}
	}
}

func setupUserLogRouteTestDB(t *testing.T) *gorm.DB {
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
	originalQuotaPerUnit := common.QuotaPerUnit

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.GlobalApiRateLimitEnable = false
	common.CriticalRateLimitEnable = false
	common.QuotaPerUnit = 500_000

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
		t.Fatalf("access sqlite: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.User{}, &model.Log{}); err != nil {
		t.Fatalf("migrate user-log tables: %v", err)
	}
	if err := service.ConfigureZTAPISessionSigningKey(
		"0123456789abcdef0123456789abcdef",
	); err != nil {
		t.Fatalf("configure signing key: %v", err)
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
		common.GlobalApiRateLimitEnable = originalGlobalRateLimit
		common.CriticalRateLimitEnable = originalCriticalRateLimit
		common.QuotaPerUnit = originalQuotaPerUnit
	})
	return db
}
