package relay

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPIImageBillingChain(t *testing.T, c interface{ Set(string, any) }, info *relaycommon.RelayInfo, quota, reserve int) *gorm.DB {
	t.Helper()
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
	oldQuotaPerUnit := common.QuotaPerUnit
	common.RedisEnabled, common.BatchUpdateEnabled = false, false
	common.QuotaPerUnit = 500000
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.BalanceLedger{}, &model.BillingRefundPending{},
		&model.Channel{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{},
		&model.ZTAPISettlementFinalizationIntent{}, &model.Log{},
	))
	require.NoError(t, model.MigrateZTAPISupplierRefund(db))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	require.NoError(t, model.MigrateZTAPISettlementLogOutbox(db))
	require.NoError(t, model.MigrateZTAPIFinanceAlerts(db))
	user := model.User{Username: "image-chain-user", Password: "unused", Status: common.UserStatusEnabled, Quota: quota, Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	_, keyHash, keyPrefix, err := common.GenerateZTAPIKey()
	require.NoError(t, err)
	token := model.Token{UserId: user.Id, KeyHash: keyHash, KeyPrefix: keyPrefix, Status: common.TokenStatusEnabled, Name: "image-chain", ExpiredTime: -1, RemainQuota: quota, UnlimitedQuota: true}
	require.NoError(t, db.Create(&token).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 41, Name: "image-chain-channel", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI}).Error)
	info.UserId, info.TokenId, info.TokenUnlimited = user.Id, token.Id, true
	info.RequestId = common.GetUUID()
	c.Set(string(constant.ContextKeyChannelId), 41)
	c.Set(string(constant.ContextKeyChannelType), constant.ChannelTypeOpenAI)
	c.Set(string(constant.ContextKeyChannelKey), "synthetic-image-key")
	c.Set(string(constant.ContextKeyOriginalModel), "verified-provider-image")
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch
		common.QuotaPerUnit = oldQuotaPerUnit
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	if reserve >= 0 {
		apiErr := service.PreConsumeBilling(nil, reserve, info)
		require.Nil(t, apiErr)
	}
	return db
}

func TestZTAPIImageSettlementRealChainTrustedUsageSettlesExactly(t *testing.T) {
	service.InitHttpClient()
	var upstreamCalls atomic.Int32
	const responseBody = `{"request_id":"req-image-settle","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":10,"output_tokens":7,"total_tokens":17}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.Close)
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	info.RelayMode = relayconstant.RelayModeImagesGenerations
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	db := setupZTAPIImageBillingChain(t, c, info, 100, 20)

	apiErr := ImageHelper(c, info)
	require.Nil(t, apiErr)
	require.Equal(t, int32(1), upstreamCalls.Load())
	var row model.ZTAPIRequestSettlement
	require.NoError(t, db.Where("request_id = ?", info.RequestId).Take(&row).Error)
	require.Equal(t, model.ZTAPISettlementSettled, row.Status)
	require.EqualValues(t, 9, row.ChargedQuota)
	require.Equal(t, 1, row.FinalAttempt)
	var ledgers, logs int64
	require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ?", info.RequestId).Count(&ledgers).Error)
	require.NoError(t, db.Model(&model.ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID).Count(&logs).Error)
	require.EqualValues(t, 2, ledgers)
	require.EqualValues(t, 1, logs)
}

func TestZTAPIImageSettlementRealChainPendingDeliversOnceWithoutRefundOrFallback(t *testing.T) {
	service.InitHttpClient()
	var upstreamCalls atomic.Int32
	const responseBody = `{"request_id":"req-image-pending","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1e3,"output_tokens":1,"total_tokens":1001}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req-image-pending")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.Close)
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	info.RelayMode = relayconstant.RelayModeImagesGenerations
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	db := setupZTAPIImageBillingChain(t, c, info, 100, 20)

	apiErr := ImageHelper(c, info)
	require.Nil(t, apiErr)
	require.Equal(t, int32(1), upstreamCalls.Load(), "delivered pending image must not fallback")
	recorder := c.MustGet("ztapi_image_test_recorder").(*httptest.ResponseRecorder)
	require.JSONEq(t, responseBody, recorder.Body.String())
	var row model.ZTAPIRequestSettlement
	require.NoError(t, db.Where("request_id = ?", info.RequestId).Take(&row).Error)
	require.Equal(t, model.ZTAPISettlementPending, row.Status)
	var ledgers, alerts int64
	require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ?", info.RequestId).Count(&ledgers).Error)
	require.NoError(t, db.Model(&model.ZTAPIFinanceAlertOutbox{}).Where("source_record_id = ?", row.ID).Count(&alerts).Error)
	require.EqualValues(t, 1, ledgers)
	require.EqualValues(t, 1, alerts)
}

func TestZTAPIImageSettlementRealChainInsufficientBalanceStopsBeforeIO(t *testing.T) {
	var upstreamCalls atomic.Int32
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	setupZTAPIImageBillingChain(t, c, info, 10, -1)
	apiErr := WithZTAPIImageAdmission(c, info, func() *types.NewAPIError {
		apiErr := service.PreConsumeBilling(c, 20, info)
		if apiErr == nil {
			upstreamCalls.Add(1)
		}
		return apiErr
	})
	require.NotNil(t, apiErr)
	require.Zero(t, upstreamCalls.Load())
}
