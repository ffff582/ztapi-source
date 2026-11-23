package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupZTAPIFinanceServiceTest(t *testing.T, count int) (*gorm.DB, ZTAPIHealthWorkerConfig, *time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "finance.db")+"?_pragma=busy_timeout(10000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPISupplierRefund{}, &model.ZTAPIAttemptBillingReview{}))
	require.NoError(t, model.MigrateZTAPIFinanceAlerts(db))
	c := telegramTestConfig(t)
	now := c.Now()
	c.Now = func() time.Time { return now }
	for i := 0; i < count; i++ {
		require.NoError(t, db.Create(&model.ZTAPIRequestSettlement{OperationID: fmt.Sprintf("finance-op-%d", i), RequestID: fmt.Sprintf("sk-private-request-%d", i), PublicModel: "private-model", Status: model.ZTAPISettlementPending,
			MissingDimensionsJSON: `["cache_write","https://api.telegram.org/bot123456:offline_test_token/sendMessage"]`, UsageJSON: `{"prompt":"private output"}`, UpdatedAt: now}).Error)
	}
	return db, c, &now
}

func TestZTAPIFinanceAlertsAttemptReviewRetriesAfterParentSettled(t *testing.T) {
	db, c, now := setupZTAPIFinanceServiceTest(t, 0)
	parent := model.ZTAPIRequestSettlement{OperationID: "attempt-alert-service", RequestID: "01a07c2d-1d46-44a1-b294-2c3bea4912fd",
		Status: model.ZTAPISettlementSettled, ChargedQuota: 75, FinalAttempt: 2, UsageJSON: `{"prompt":"private"}`, UpdatedAt: *now}
	require.NoError(t, db.Create(&parent).Error)
	review := model.ZTAPIAttemptBillingReview{SettlementID: parent.ID, RequestID: parent.RequestID, Attempt: 1,
		Status: "pending", PendingReason: "upstream_attempt_billing_unconfirmed", UpdatedAt: *now}
	require.NoError(t, db.Create(&review).Error)
	calls := 0
	c.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		text := body["text"].(string)
		require.Contains(t, text, "【ZTAPI 账单待核对】")
		require.Contains(t, text, "情况：号池请求结果不确定，备用线路可能已接管；需要确认这次号池请求是否扣费")
		require.Contains(t, text, "请求编号："+parent.RequestID)
		require.Contains(t, text, "第 1 次上游尝试")
		require.Contains(t, text, fmt.Sprintf("结算编号：%d", parent.ID))
		require.Contains(t, text, "建议处理：请把请求编号发给上游")
		require.Contains(t, text, "本通知不会自动修改客户余额")
		for _, secret := range []string{"private", "prompt", "http", "CIRCUIT", "offline_test_token"} {
			require.NotContains(t, text, secret)
		}
		if calls == 1 {
			return nil, errors.New("https://api.telegram.org/bot123456:offline_test_token/sendMessage")
		}
		return workerResponse(200, `{"ok":true,"result":{"message_id":9,"date":2000000000,"text":"private"}}`), nil
	})
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.Equal(t, 1, calls)
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.Equal(t, 1, calls)
	*now = now.Add(61 * time.Second)
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.Equal(t, 2, calls)
	var jobs []model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.Equal(t, "sent", jobs[0].Status)
	require.Equal(t, 2, jobs[0].Attempts)
	require.JSONEq(t, `{"ok":true,"result":{"message_id":9,"date":2000000000}}`, jobs[0].ReceiptJSON)
	var stored model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&stored, parent.ID).Error)
	require.Equal(t, model.ZTAPISettlementSettled, stored.Status)
	require.Equal(t, int64(75), stored.ChargedQuota)
}

func TestZTAPIFinanceAlertsAttemptReviewTwoWorkersSendOnce(t *testing.T) {
	db, c, now := setupZTAPIFinanceServiceTest(t, 0)
	require.NoError(t, db.Create(&model.ZTAPIAttemptBillingReview{SettlementID: 7,
		RequestID: "01a07c2d-1d46-44a1-b294-2c3bea4912fd", Attempt: 1, Status: "pending",
		PendingReason: "upstream_attempt_billing_unconfirmed", UpdatedAt: *now}).Error)
	var calls atomic.Int32
	c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		return workerResponse(200, `{"ok":true,"result":{"message_id":9,"date":2000000000}}`), nil
	})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- RunZTAPIFinanceAlertsOnce(context.Background(), db, c) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), calls.Load())
	var jobs []model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.Equal(t, "sent", jobs[0].Status)
	require.Equal(t, 1, jobs[0].Attempts)
}

func TestZTAPIFinanceAlertsFailedSendThenRetryUsesDurableSafeState(t *testing.T) {
	db, c, now := setupZTAPIFinanceServiceTest(t, 1)
	calls := 0
	c.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		text := body["text"].(string)
		require.Contains(t, text, "【ZTAPI 账单待核对】")
		require.Contains(t, text, "缓存写入用量")
		require.Contains(t, text, "建议处理：请让 Codex 检查我方计费证据")
		for _, forbidden := range []string{"CIRCUIT", "private", "offline_test_token", "http"} {
			require.NotContains(t, text, forbidden)
		}
		if calls == 1 {
			return nil, errors.New("https://api.telegram.org/bot123456:offline_test_token/sendMessage")
		}
		return workerResponse(200, `{"ok":true,"result":{"message_id":99,"date":2000000061,"chat":{"id":"private"},"text":"secret"}}`), nil
	})
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	var job model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, "alert_transport_error", job.LastReason)
	require.Equal(t, 1, job.Attempts)
	require.Greater(t, job.NextAttemptAt, now.Unix())
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.Equal(t, 1, calls)
	*now = now.Add(61 * time.Second)
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.Equal(t, 2, calls)
	require.NoError(t, db.First(&job, job.ID).Error)
	require.Equal(t, "sent", job.Status)
	require.Equal(t, 2, job.Attempts)
	require.JSONEq(t, `{"ok":true,"result":{"message_id":99,"date":2000000061}}`, job.ReceiptJSON)
	raw, err := common.Marshal(job)
	require.NoError(t, err)
	for _, forbidden := range []string{"private", "secret", "offline_test_token", "http"} {
		require.NotContains(t, string(raw), forbidden)
	}
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIFinanceAlertOutbox{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestZTAPIFinanceAlertsTwoWorkersClaimOneDelivery(t *testing.T) {
	db, c, _ := setupZTAPIFinanceServiceTest(t, 1)
	var calls atomic.Int32
	c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		return workerResponse(200, `{"ok":true,"result":{"message_id":9,"date":2000000000}}`), nil
	})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- RunZTAPIFinanceAlertsOnce(context.Background(), db, c) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), calls.Load())
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIFinanceAlertOutbox{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestZTAPIFinanceAlertsBoundedQueueAndTenSendsPerTick(t *testing.T) {
	db, c, _ := setupZTAPIFinanceServiceTest(t, 125)
	calls := 0
	c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return workerResponse(200, `{"ok":true,"result":{"message_id":9,"date":2000000000}}`), nil
	})
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.Equal(t, 10, calls)
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIFinanceAlertOutbox{}).Count(&count).Error)
	require.Equal(t, int64(100), count)
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.Equal(t, 20, calls)
	require.NoError(t, db.Model(&model.ZTAPIFinanceAlertOutbox{}).Count(&count).Error)
	require.Equal(t, int64(125), count)
}

func TestZTAPIFinanceAlertsInvalidConfigIsSilentAndPersisted(t *testing.T) {
	db, c, _ := setupZTAPIFinanceServiceTest(t, 1)
	c.TelegramBotToken = ""
	c.AlertWebhookURL = "https://example.invalid/do-not-fallback"
	c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid config must never send")
		return nil, nil
	})
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	var job model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, "alert_recipient_missing_or_invalid", job.LastReason)
	require.Equal(t, "pending", job.Status)
	require.Empty(t, job.ReceiptJSON)
}

func TestZTAPIFinanceMaintenanceUsesOnlyExistingTelegramEnvironment(t *testing.T) {
	db, _, _ := setupZTAPIFinanceServiceTest(t, 1)
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN", "")
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_CHAT_ID", "")
	t.Setenv("ZTAPI_HEALTH_PROBE_USER_ID", "not-a-probe-user")
	require.NoError(t, RunZTAPIFinanceMaintenance(context.Background()))
	var job model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, "alert_recipient_missing_or_invalid", job.LastReason)
	require.False(t, strings.Contains(job.Reason, "offline_test_token"))
}

func TestZTAPIFinanceAlertsStaleReservationSendsOnceWithoutReleasing(t *testing.T) {
	db, c, now := setupZTAPIFinanceServiceTest(t, 0)
	row := model.ZTAPIRequestSettlement{OperationID: "stale-service-op", RequestID: "stale-service-request", Status: model.ZTAPISettlementReserved,
		ReservedQuota: 91, TokenReservedQuota: 91, CreatedAt: now.Add(-25 * time.Hour), UpdatedAt: *now}
	require.NoError(t, db.Create(&row).Error)
	require.NoError(t, db.First(&row, row.ID).Error)
	calls := 0
	c.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		require.Contains(t, body["text"], "预留费用超过 24 小时仍未结算")
		require.Contains(t, body["text"], "【ZTAPI 账单待核对】")
		require.Contains(t, body["text"], "建议处理：请让 Codex 检查这笔请求为何一直未完成")
		return workerResponse(200, `{"ok":true,"result":{"message_id":9,"date":2000000000}}`), nil
	})
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	require.Equal(t, 1, calls)
	var stored model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&stored, row.ID).Error)
	require.Equal(t, row, stored)
}

func TestZTAPIFinanceAlertsResolvedSourcesCancelBeforeDelivery(t *testing.T) {
	for _, source := range []struct{ kind, terminal, initial string }{
		{"settlement", "settled", "pending"}, {"settlement", "released", "pending"},
		{"settlement", "settled", "reserved"}, {"settlement", "released", "reserved"},
		{"refund", "applied", "pending"}, {"refund", "completed", "pending"},
		{"attempt_review", "verified_billed", "pending"}, {"attempt_review", "verified_nocharge", "pending"},
	} {
		for _, failedFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/%s/failed_first_%t", source.kind, source.initial, source.terminal, failedFirst), func(t *testing.T) {
				db, c, now := setupZTAPIFinanceServiceTest(t, 0)
				var row any
				switch source.kind {
				case "settlement":
					row = &model.ZTAPIRequestSettlement{OperationID: "resolved-op", RequestID: "resolved-request", Status: "pending", MissingDimensionsJSON: `["cache_write"]`, UpdatedAt: *now}
				case "refund":
					row = &model.ZTAPISupplierRefund{ProofKey: "resolved-proof", RequestID: "resolved-request", Status: "pending", PendingReason: "awaiting_approval", UpdatedAt: *now}
				case "attempt_review":
					row = &model.ZTAPIAttemptBillingReview{SettlementID: 7, RequestID: "resolved-request", Attempt: 1, Status: "pending", PendingReason: "awaiting_approval", UpdatedAt: *now}
				}
				require.NoError(t, db.Create(row).Error)
				if source.initial == "reserved" {
					require.NoError(t, db.Model(row).Updates(map[string]any{"status": "reserved", "created_at": now.Add(-25 * time.Hour)}).Error)
				}
				calls := 0
				c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, errors.New("offline failure")
				})
				if failedFirst {
					require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
					require.Equal(t, 1, calls)
				} else {
					n, err := model.QueuePendingZTAPIFinanceAlerts(context.Background(), db, *now, 100)
					require.NoError(t, err)
					require.Equal(t, 1, n)
				}
				require.NoError(t, db.Model(row).Update("status", source.terminal).Error)
				*now = now.Add(61 * time.Second)
				require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
				var job model.ZTAPIFinanceAlertOutbox
				require.NoError(t, db.First(&job).Error)
				require.Equal(t, "cancelled", job.Status)
				require.Equal(t, "alert_source_resolved", job.LastReason)
				require.Empty(t, job.LeaseToken)
				require.Empty(t, job.ReceiptJSON)
				require.Zero(t, job.SentAt)
				wantCalls := 0
				if failedFirst {
					wantCalls = 1
				}
				require.Equal(t, wantCalls, calls, "resolved sources must not make a subsequent HTTP request")
				require.Equal(t, wantCalls, job.Attempts, "validation and cancellation are not delivery attempts")
				*now = now.Add(time.Hour)
				require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
				var stored model.ZTAPIFinanceAlertOutbox
				require.NoError(t, db.First(&stored, job.ID).Error)
				require.Equal(t, job, stored, "cancellation remains durable")
			})
		}
	}
}

func TestZTAPIFinanceAlertsLookupFailureDoesNotStrandOtherClaims(t *testing.T) {
	for _, missingTable := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_table_%t", missingTable), func(t *testing.T) {
			db, c, now := setupZTAPIFinanceServiceTest(t, 1)
			_, err := model.QueuePendingZTAPIFinanceAlerts(context.Background(), db, *now, 100)
			require.NoError(t, err)
			job := model.ZTAPIFinanceAlertOutbox{DedupKey: strings.Repeat("a", 64), SourceKind: "refund", SourceRecordID: 99,
				Reason: "awaiting_approval", Status: "pending", NextAttemptAt: now.Unix() - 1}
			require.NoError(t, db.Create(&job).Error)
			reason := "alert_source_missing"
			if missingTable {
				reason = "alert_source_lookup_failed"
				require.NoError(t, db.Migrator().DropTable(&model.ZTAPISupplierRefund{}))
			}
			calls := 0
			c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return workerResponse(200, `{"ok":true,"result":{"message_id":9,"date":2000000000}}`), nil
			})
			err = RunZTAPIFinanceAlertsOnce(context.Background(), db, c)
			require.ErrorContains(t, err, reason)
			require.Equal(t, 1, calls, "one unreadable source must not strand valid claimed warnings")
			require.NoError(t, db.First(&job, job.ID).Error)
			require.Equal(t, "pending", job.Status)
			require.Equal(t, reason, job.LastReason)
			require.Greater(t, job.NextAttemptAt, now.Unix())
			require.Empty(t, job.LeaseToken)
			var sent int64
			require.NoError(t, db.Model(&model.ZTAPIFinanceAlertOutbox{}).Where("status = ?", "sent").Count(&sent).Error)
			require.Equal(t, int64(1), sent)
		})
	}
}

func TestZTAPIFinanceAlertsResolutionDuringSendPreservesActualReceipt(t *testing.T) {
	db, c, now := setupZTAPIFinanceServiceTest(t, 1)
	calls := 0
	c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		calls++
		// Claim has committed before HTTP. A real concurrent resolution cannot
		// retract this accepted delivery or erase its historical receipt.
		require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Where("status = ?", "pending").Update("status", "settled").Error)
		return workerResponse(200, `{"ok":true,"result":{"message_id":9,"date":2000000000}}`), nil
	})
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	var job model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, "sent", job.Status)
	require.NotEmpty(t, job.ReceiptJSON)
	*now = now.Add(time.Hour)
	require.NoError(t, RunZTAPIFinanceAlertsOnce(context.Background(), db, c))
	var stored model.ZTAPIFinanceAlertOutbox
	require.NoError(t, db.First(&stored, job.ID).Error)
	require.Equal(t, job, stored)
	require.Equal(t, 1, calls)
}
