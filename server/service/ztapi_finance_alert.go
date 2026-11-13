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

func ztapiFinanceAlertMeaning(kind, reason string) (string, string) {
	reasons := strings.Split(model.ZTAPIFinanceAlertSafeReason(reason), ",")
	labels := make([]string, 0, len(reasons))
	for _, value := range reasons {
		switch strings.TrimSpace(value) {
		case "upstream_attempt_billing_unconfirmed":
			labels = append(labels, "号池请求结果不确定，备用线路可能已接管；需要确认这次号池请求是否扣费")
		case "stale_reserved_hold":
			labels = append(labels, "预留费用超过 24 小时仍未结算")
		case "cache_write":
			labels = append(labels, "缓存写入用量缺少可信计费证据")
		case "cache_read":
			labels = append(labels, "缓存读取用量缺少可信计费证据")
		case "input_tokens":
			labels = append(labels, "输入用量缺少可信计费证据")
		case "output_tokens":
			labels = append(labels, "输出用量缺少可信计费证据")
		case "upstream_billing_unconfirmed", "upstream_usage_missing", "usage_zero_unconfirmed":
			labels = append(labels, "上游用量或扣费情况尚未确认")
		case "awaiting_approval":
			labels = append(labels, "已有凭证，等待财务复核批准")
		case "evidence_conflict", "approval_conflict", "attempt_evidence_conflict":
			labels = append(labels, "现有凭证互相冲突，不能自动处理")
		case "unclassified_pending_reason":
			labels = append(labels, "系统发现一项无法自动分类的计费证据")
		default:
			labels = append(labels, "存在需要人工确认的计费或退款证据")
		}
	}
	meaning := strings.Join(labels, "；")
	switch {
	case kind == "attempt_review" && strings.Contains(reason, "upstream_attempt_billing_unconfirmed"):
		return meaning, "请把请求编号发给上游，确认该次请求是否扣费；收到回复后进入管理后台“财务对账 → 尝试对账”处理"
	case strings.Contains(reason, "stale_reserved_hold"):
		return meaning, "请让 Codex 检查这笔请求为何一直未完成；禁止直接修改客户余额"
	case kind == "refund":
		return meaning, "请先向上游取得退款凭证，再进入管理后台“财务对账”核准；禁止直接修改客户余额"
	default:
		return meaning, "请让 Codex 检查我方计费证据；如缺少上游扣费明细，再携带请求编号询问上游"
	}
}

func ztapiFinanceSourceLabel(kind string) string {
	switch kind {
	case "settlement":
		return "客户请求结算"
	case "refund":
		return "上游退款"
	case "attempt_review":
		return "上游尝试计费"
	default:
		return "未知记录"
	}
}

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
			identity = fmt.Sprintf("\n请求编号：%s\n结算编号：%d\n范围：第 %d 次上游尝试",
				model.ZTAPIFinanceAlertRequestReference(job.RequestReference), job.SourceSettlementID, job.SourceAttempt)
		}
		meaning, action := ztapiFinanceAlertMeaning(kind, job.Reason)
		text := fmt.Sprintf("【ZTAPI 账单待核对】\n类型：%s\n情况：%s%s\n时间（北京时间）：%s\n\n建议处理：%s\n重要：本通知不会自动修改客户余额，核验前不要手工改余额。\n\n记录编号：%d\n通知编号：%d",
			ztapiFinanceSourceLabel(kind), meaning, identity, time.Unix(job.CreatedAt, 0).In(ztapiBeijingTime).Format("2006-01-02 15:04:05"), action, job.SourceRecordID, job.ID)
		ok, reason, receipt := sendZTAPITelegramText(ctx, config, text)
		// Invalid configuration and delivery failures remain quiet but durable.
		if err := model.FinishZTAPIFinanceAlert(ctx, db, job, config.Now(), ok, reason, receipt); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
