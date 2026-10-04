package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestZTAPICommercialMaintenanceTimerQueuesPendingFinance(t *testing.T) {
	db, _, _ := setupZTAPIFinanceServiceTest(t, 1)
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN", "")
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_CHAT_ID", "")
	runBillingRefundRetryOnce()
	var rows []model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1, "the existing maintenance timer must execute the finance outbox path even when another queue fails")
	require.Equal(t, "pending", rows[0].Status)
	require.Equal(t, 1, rows[0].Attempts)
	require.NotEmpty(t, rows[0].LastReason)
}

func TestZTAPICommercialMaintenanceFindsEarlierAttemptAfterSuccess(t *testing.T) {
	db, _, _ := setupZTAPIFinanceServiceTest(t, 0)
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	require.NoError(t, db.AutoMigrate(&model.ZTAPIRequestAttempt{}, &model.ZTAPIPendingResolution{}))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	parent := model.ZTAPIRequestSettlement{OperationID: "maintenance-attempt", RequestID: "01a07c2d-1d46-44a1-b294-2c3bea4912fd", Status: model.ZTAPISettlementSettled, Dispatched: true, FinalAttempt: 2}
	require.NoError(t, db.Create(&parent).Error)
	for _, attempt := range []model.ZTAPIRequestAttempt{
		{SettlementID: parent.ID, Attempt: 1, ChannelID: 1, CredentialVersion: "pool-version", Protocol: "chat", HTTPStatus: 502},
		{SettlementID: parent.ID, Attempt: 2, ChannelID: 2, CredentialVersion: "enterprise-version", Protocol: "chat", HTTPStatus: 200},
	} {
		require.NoError(t, db.Create(&attempt).Error)
	}
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN", "")
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_CHAT_ID", "")
	runBillingRefundRetryOnce()
	var reviews []model.ZTAPIAttemptBillingReview
	require.NoError(t, db.Find(&reviews).Error)
	require.Len(t, reviews, 1, "the production timer must recover an earlier unknown attempt despite the parent being settled")
	require.Equal(t, "pending", reviews[0].Status)
	require.Equal(t, 1, reviews[0].Attempt)
	var alerts []model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.Find(&alerts).Error)
	require.Len(t, alerts, 1)
	require.Equal(t, "pending", alerts[0].Status, "unconfigured Telegram leaves a durable retry, never drops the review")
}
