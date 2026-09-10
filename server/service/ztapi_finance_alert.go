package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// RunZTAPIFinanceMaintenance is a single bounded alert tick for the parent's
// existing maintenance timer. It does not resolve holds or move balances.
func RunZTAPIFinanceMaintenance(ctx context.Context) error {
	config := DefaultZTAPIHealthWorkerConfig()
	config.TelegramBotToken = strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN"))
	config.TelegramChatID = strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_TELEGRAM_CHAT_ID"))
	config.TelegramConfigured = true
	return RunZTAPIFinanceAlertsOnce(ctx, model.DB, config)
}

// RunZTAPIFinanceAlertsOnce queues at most 100 source revisions and attempts at
// most ten leased sends. Telegram provides no idempotency key: a crash after
// acceptance but before receipt persistence can require at-least-once replay.
// Source validation is atomic with claim, not with external delivery: a source
// can still resolve after claim commit while its notification is in flight.
func RunZTAPIFinanceAlertsOnce(ctx context.Context, db *gorm.DB, config ZTAPIHealthWorkerConfig) error {
	if ctx == nil || db == nil {
		return model.ErrZTAPIFinanceAlertInvalid
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.AlertTimeout == 0 {
		config.AlertTimeout = 10 * time.Second
	}
	if config.AlertTimeout < time.Second || config.AlertTimeout > 30*time.Second {
		return model.ErrZTAPIFinanceAlertInvalid
	}
	tickTimeout := 10*config.AlertTimeout + time.Minute
	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()
	var failures []error
	_, queueErr := model.QueuePendingZTAPIFinanceAlerts(ctx, db, config.Now(), 100)
	failures = append(failures, queueErr)
	// All leases cover the entire sequential batch, not just its first send.
	leaseSeconds := int64((tickTimeout + time.Minute) / time.Second)
	jobs, err := model.ClaimZTAPIFinanceAlerts(ctx, db, config.Now(), 10, leaseSeconds)
	failures = append(failures, err)
	for _, job := range jobs {
		kind := "unknown"
		if job.SourceKind == "settlement" || job.SourceKind == "refund" || job.SourceKind == "attempt_review" {
			kind = job.SourceKind
		}
		identity := ""
		if kind == "attempt_review" {
			identity = fmt.Sprintf("\nrequest_ref: %s\nsettlement_id: %d\nattempt: %d",
				model.ZTAPIFinanceAlertRequestReference(job.RequestReference), job.SourceSettlementID, job.SourceAttempt)
		}
		text := fmt.Sprintf("ZTAPI FINANCE RECONCILIATION PENDING\nsource: %s\nrecord_id: %d%s\nreason: %s\noutbox_id: %d\nqueued_at: %s\nFinance review is required. This alert does not change balances.",
			kind, job.SourceRecordID, identity, model.ZTAPIFinanceAlertSafeReason(job.Reason), job.ID, time.Unix(job.CreatedAt, 0).UTC().Format(time.RFC3339))
		ok, reason, receipt := sendZTAPITelegramText(ctx, config, text)
		// Invalid configuration and delivery failures remain quiet but durable.
		if err := model.FinishZTAPIFinanceAlert(ctx, db, job, config.Now(), ok, reason, receipt); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
