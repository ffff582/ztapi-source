package model

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func setupSecretLogBoundaryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "log-boundary.db")), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	previousDB, previousLogDB := DB, LOG_DB
	previousRedis := common.RedisEnabled
	DB, LOG_DB, common.RedisEnabled = db, db, false
	t.Cleanup(func() {
		DB, LOG_DB, common.RedisEnabled = previousDB, previousLogDB, previousRedis
		_ = pool.Close()
	})
	require.NoError(t, db.AutoMigrate(&Log{}, &User{}))
	return db
}

func TestNewErrorLogStorageRedactsContentAndOther(t *testing.T) {
	db := setupSecretLogBoundaryDB(t)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("username", "customer")
	ctx.Set(common.RequestIdKey, "req-audit-17")
	ctx.Set(common.UpstreamRequestIdKey, "upstream-audit-19")
	other := map[string]interface{}{
		"api_key": "fictional-other-credential", "prompt_tokens": 123, "completion_tokens": 45,
		"cache_creation_input_tokens": 67, "cost": 0.125, "token_id": 19, "audit_id": "audit-31",
	}
	RecordErrorLog(ctx, 7, 11, "test-model", "test-token", "Authorization: Bearer fictional-content-credential", 19, 3, true, "default", other)
	var stored Log
	require.NoError(t, db.First(&stored).Error)
	require.NotContains(t, stored.Content, "fictional-content-credential")
	require.NotContains(t, stored.Other, "fictional-other-credential")
	for _, safe := range []string{`"prompt_tokens":123`, `"completion_tokens":45`, `"cache_creation_input_tokens":67`, `"cost":0.125`, `"token_id":19`, `"audit_id":"audit-31"`} {
		require.Contains(t, stored.Other, safe)
	}
	require.Equal(t, "req-audit-17", stored.RequestId)
	require.Equal(t, "upstream-audit-19", stored.UpstreamRequestId)
	require.Equal(t, 19, stored.TokenId)
	require.Equal(t, 11, stored.ChannelId)
	require.Zero(t, stored.Quota)
	require.Zero(t, stored.PromptTokens)
	require.Zero(t, stored.CompletionTokens)
	require.Equal(t, "fictional-other-credential", other["api_key"], "must not mutate caller evidence")
}

func TestErrorLogCreateBoundaryCoversAlternateWriters(t *testing.T) {
	db := setupSecretLogBoundaryDB(t)
	for _, create := range []func(){
		func() { RecordLog(7, LogTypeError, "OPENAI_API_KEY=fictional-alternate-credential") },
		func() {
			RecordLogWithAdminInfo(7, LogTypeError, "OPENAI_API_KEY=fictional-alternate-credential", map[string]interface{}{"api_key": "fictional-admin-credential"})
		},
		func() {
			RecordTaskBillingLog(RecordTaskBillingLogParams{UserId: 7, LogType: LogTypeError, Content: "OPENAI_API_KEY=fictional-alternate-credential", Other: map[string]interface{}{"api_key": "fictional-task-credential", "cost": 0.125}})
		},
	} {
		create()
	}
	var logs []Log
	require.NoError(t, db.Find(&logs).Error)
	require.Len(t, logs, 3)
	for _, row := range logs {
		require.NotContains(t, row.Content+row.Other, "fictional-")
	}
}

func TestCustomerLogProjectionRedactsOldRowsWithoutUpdatingHistory(t *testing.T) {
	db := setupSecretLogBoundaryDB(t)
	original := Log{UserId: 7, TokenId: 19, Type: LogTypeConsume, Content: "Authorization: Bearer fictional-old-content-credential",
		RequestId: "req-audit-17", UpstreamRequestId: "upstream-audit-19", Quota: 901, PromptTokens: 123, CompletionTokens: 45,
		Other: `{"api_key":"fictional-old-other-credential","prompt_tokens":123,"completion_tokens":45,"cost":0.125,"audit_id":"audit-31","settlement_id":9007199254740993,"admin_info":{"private":"admin-only"},"audit_info":{"route":"admin-only"},"stream_status":"admin-only"}`}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&original).Error)
	updates := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:no-history-updates", func(*gorm.DB) { updates++ }))
	for _, read := range []struct {
		name string
		load func() ([]*Log, error)
	}{
		{"user", func() ([]*Log, error) {
			rows, total, err := GetUserLogs(7, LogTypeUnknown, 0, 0, "", "", 0, 10, "", "", "")
			require.EqualValues(t, 1, total)
			return rows, err
		}},
		{"token", func() ([]*Log, error) { return GetLogByTokenId(19) }},
	} {
		t.Run(read.name, func(t *testing.T) {
			rows, err := read.load()
			require.NoError(t, err)
			require.Len(t, rows, 1)
			got := rows[0]
			require.NotContains(t, got.Content+got.Other, "fictional-old-")
			require.NotContains(t, got.Other, "admin-only")
			for _, safe := range []string{`"prompt_tokens":123`, `"completion_tokens":45`, `"cost":0.125`, `"audit_id":"audit-31"`, `"settlement_id":9007199254740993`} {
				require.Contains(t, got.Other, safe)
			}
			require.Equal(t, 901, got.Quota)
			require.Equal(t, 123, got.PromptTokens)
			require.Equal(t, 45, got.CompletionTokens)
			require.Equal(t, "req-audit-17", got.RequestId)
			require.Equal(t, "upstream-audit-19", got.UpstreamRequestId)
		})
	}
	var raw Log
	require.NoError(t, db.First(&raw, original.Id).Error)
	require.Equal(t, original, raw)
	require.Zero(t, updates)
}

func TestCustomerLogProjectionRedactsMalformedLegacyOther(t *testing.T) {
	rows := []*Log{{Content: "OPENAI_API_KEY=fictional-legacy-content", Other: "Authorization: Bearer fictional-legacy-other"}}
	formatUserLogs(rows, 0)
	require.NotContains(t, rows[0].Content+rows[0].Other, "fictional-legacy-")
	require.Equal(t, "null", rows[0].Other, "malformed admin metadata must remain hidden")
}

func TestErrorLogBoundaryDoesNotRewriteFinancialLogFields(t *testing.T) {
	db := setupSecretLogBoundaryDB(t)
	for _, logType := range []int{LogTypeTopup, LogTypeConsume, LogTypeRefund, LogTypeManage} {
		row := Log{Type: logType, Content: "immutable financial evidence", Quota: 901, PromptTokens: 123, CompletionTokens: 45,
			RequestId: "req-audit-17", UpstreamRequestId: "upstream-audit-19",
			Other: `{"cost":0.125,"settlement_id":9007199254740993,"audit_id":"audit-31"}`}
		require.NoError(t, db.Create(&row).Error)
		var stored Log
		require.NoError(t, db.First(&stored, row.Id).Error)
		require.Equal(t, row, stored)
	}
}
