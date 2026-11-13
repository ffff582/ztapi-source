// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Reader uses an already-open, SELECT-only connection to the main API database.
// CatalogURL is the fixed, server-configured complete /api/pricing URL.
type Reader struct {
	DB         *gorm.DB
	CatalogURL string
	HTTPClient *http.Client
	Now        func() time.Time
}

var errSourceUnavailable = errors.New("support evidence unavailable")

func (r *Reader) Identity(ctx context.Context, userID int) (Identity, error) {
	if userID <= 0 {
		return Identity{}, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, sourceQueryTimeout)
	defer cancel()
	if r == nil || r.DB == nil {
		return Identity{}, errSourceUnavailable
	}
	var rows []Identity
	// A plain projection intentionally includes soft-deleted identities. Auth
	// must see the real role/status/deleted_at rather than a synthetic active user.
	err := r.DB.WithContext(ctx).Table("users").Select("id, role, status, deleted_at").Where("id = ?", userID).Limit(1).Find(&rows).Error
	if err != nil {
		return Identity{}, sourceError(ctx, err)
	}
	if len(rows) == 0 {
		return Identity{}, ErrNotFound
	}
	return rows[0], nil
}

func (r *Reader) RecentRequests(ctx context.Context, userID int) ([]Diagnosis, error) {
	if userID <= 0 {
		return nil, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, sourceQueryTimeout)
	defer cancel()
	if r == nil || r.DB == nil {
		return nil, errSourceUnavailable
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	start := now.Add(-30 * 24 * time.Hour)
	var health []struct {
		RequestID string
		StartedAt int64
	}
	// Group before limiting: repeated executions must not crowd distinct requests
	// out of the customer's latest twenty. Both candidate sources are owner-scoped.
	err := r.DB.WithContext(ctx).Table("ztapi_health_requests").
		Select("request_id, MAX(started_at) AS started_at").
		Where("user_id = ? AND started_at >= ? AND started_at <= ?", userID, start.Unix(), now.Unix()).
		Group("request_id").Order("started_at DESC, request_id DESC").Limit(20).Find(&health).Error
	if err != nil {
		return nil, sourceError(ctx, err)
	}
	var settlements []struct {
		RequestID string
		CreatedAt time.Time
	}
	err = r.DB.WithContext(ctx).Table("ztapi_request_settlements AS s").Select("s.request_id, s.created_at").
		Where("s.user_id = ? AND s.created_at >= ? AND s.created_at <= ?", userID, start, now).
		Where("NOT EXISTS (?)", r.DB.WithContext(ctx).Table("ztapi_health_requests").Select("1").
			Where("user_id = ? AND request_id = s.request_id", userID)).
		Order("s.created_at DESC, s.request_id DESC").Limit(20).Find(&settlements).Error
	if err != nil {
		return nil, sourceError(ctx, err)
	}
	type candidate struct {
		id string
		at time.Time
	}
	candidates := make(map[string]time.Time, len(health)+len(settlements))
	for _, h := range health {
		candidates[h.RequestID] = time.Unix(h.StartedAt, 0).UTC()
	}
	for _, s := range settlements {
		if _, exists := candidates[s.RequestID]; !exists {
			candidates[s.RequestID] = s.CreatedAt.UTC()
		}
	}
	ordered := make([]candidate, 0, len(candidates))
	for id, at := range candidates {
		ordered = append(ordered, candidate{id, at})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].at.Equal(ordered[j].at) {
			return ordered[i].id > ordered[j].id
		}
		return ordered[i].at.After(ordered[j].at)
	})
	if len(ordered) > 20 {
		ordered = ordered[:20]
	}
	result := make([]Diagnosis, 0, len(ordered))
	for _, c := range ordered {
		diagnosis, err := r.Diagnose(ctx, userID, c.id)
		if err != nil {
			return nil, err
		}
		result = append(result, diagnosis)
	}
	return result, nil
}

func (r *Reader) Diagnose(ctx context.Context, userID int, requestID string) (Diagnosis, error) {
	if userID <= 0 || requestID == "" || len(requestID) > 255 {
		return Diagnosis{}, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, sourceQueryTimeout)
	defer cancel()
	if r == nil || r.DB == nil {
		return Diagnosis{}, errSourceUnavailable
	}
	var health []sourceHealthRow
	// Events have no user_id: ownership is established by the admission, joined
	// on BOTH execution and request, never by a client-controlled request ID alone.
	err := r.DB.WithContext(ctx).Table("ztapi_health_requests AS h").Select(
		"h.request_id, h.public_model, h.stream, h.started_at, e.id AS event_id, e.reason, e.http_status, e.result_valid, e.outcome").
		Joins("LEFT JOIN ztapi_health_events AS e ON e.execution_id = h.execution_id AND e.request_id = h.request_id").
		Where("h.user_id = ? AND h.request_id = ?", userID, requestID).
		Order("h.started_at DESC, h.execution_id DESC, e.id DESC").Limit(1).Find(&health).Error
	if err != nil {
		return Diagnosis{}, sourceError(ctx, err)
	}
	var settlements []sourceSettlementRow
	err = r.DB.WithContext(ctx).Table("ztapi_request_settlements").Select(
		"id, request_id, public_model, status, reserved_quota, charged_quota, refunded_quota, dispatched, usage_json, missing_dimensions_json, created_at").
		Where("user_id = ? AND request_id = ?", userID, requestID).Limit(1).Find(&settlements).Error
	if err != nil {
		return Diagnosis{}, sourceError(ctx, err)
	}
	if len(health) == 0 && len(settlements) == 0 {
		return Diagnosis{}, ErrNotFound
	}
	d := Diagnosis{RequestID: redactSource(requestID), Outcome: "unknown", Code: "evidence_incomplete", NeedsHuman: true,
		Summary: "请求证据不完整，无法确认输出和最终费用，请提交人工核查。"}
	var settlement *sourceSettlementRow
	if len(settlements) != 0 {
		settlement = &settlements[0]
		d.Model, d.CreatedAt = redactSource(settlement.PublicModel), settlement.CreatedAt.UTC()
		d.Dispatched = settlement.Dispatched
		d.UsageReceived = sourceUsageReceived(settlement.UsageJSON, settlement.MissingDimensionsJSON)
	}
	if len(health) != 0 {
		h := health[0]
		d.Model, d.CreatedAt, d.Stream = redactSource(h.PublicModel), time.Unix(h.StartedAt, 0).UTC(), h.Stream
		if h.EventID != nil {
			d.HTTPStatus = h.HTTPStatus
			var signal sourceSignal
			valid := len(h.Outcome) <= 65536 && common.UnmarshalJsonStr(h.Outcome, &signal) == nil
			if valid && signal.Dispatched != nil {
				d.Dispatched = d.Dispatched || *signal.Dispatched
			}
			switch {
			case h.Reason == "client_cancelled" || (valid && signal.ClientCancelled):
				d.Outcome, d.Code, d.Summary = "cancelled", "client_cancelled", "已记录客户端取消或连接中断；不能据此判断上游未执行或未计费。"
			case h.Reason == "not_dispatched" && !d.Dispatched:
				d.Outcome, d.Code, d.Summary = "not_dispatched", "not_dispatched", "记录显示请求未向上游发出，未生成输出；费用仍须按结算记录核查。"
			case h.HTTPStatus == 401:
				d.Outcome, d.Code, d.Summary = "failed", "upstream_401", "上游返回 401，表示上游认证或授权失败；不代表你的平台密钥一定无效，请提交人工核查。"
			case h.HTTPStatus == 503:
				d.Outcome, d.Code, d.Summary = "failed", "upstream_503", "上游返回 503，服务暂时无法处理；不能据此承诺恢复时间或费用处理结果。"
			case h.Reason == "empty_output" || h.Reason == "length_without_visible_output" ||
				(valid && h.HTTPStatus == 200 && signal.TransportComplete && !signal.HasText && !signal.HasTool && !signal.HasMedia):
				d.Outcome, d.Code, d.Summary = "empty", "empty_output", "请求未记录到可见文本、工具或媒体输出；HTTP 200 不等于已成功交付内容。"
			case h.HTTPStatus >= 400 && h.HTTPStatus <= 599:
				d.Outcome, d.Code, d.Summary = "failed", "upstream_error", "上游返回错误状态，未确认成功输出，请携带请求编号核查。"
			case valid && signal.TransportComplete && (signal.HasText || signal.HasTool || signal.HasMedia) && h.ResultValid:
				if d.UsageReceived {
					d.Outcome, d.Code, d.Summary, d.NeedsHuman = "success", "valid_output", "已记录有效输出及上游用量；费用以独立结算记录为准。", false
				} else {
					d.Outcome, d.Code, d.Summary = "unconfirmed", "usage_missing", "已记录输出，但尚未确认收到上游用量，不能据此估算最终费用。"
				}
			}
		}
	}
	if !d.UsageReceived && d.Code != "usage_missing" {
		d.Summary += " 尚未确认收到上游用量。"
	}
	if settlement == nil {
		d.Summary += " 无可核实的结算记录，金额未知。"
		d.NeedsHuman = true
	} else {
		switch settlement.Status {
		case "pending", "reserved":
			d.ReservedU = sourceQuotaU(settlement.ReservedQuota)
			d.Summary += " 当前为预留或待结算，预留不等于最终扣款，最终费用尚未确认。"
			d.NeedsHuman = true
		case "settled", "released":
			d.ReservedU = "0"
			if settlement.Status == "released" && settlement.ChargedQuota != 0 {
				d.Summary += " 结算状态与金额证据不一致，最终费用未知，需人工核查。"
				d.NeedsHuman = true
				break
			}
			var note string
			d.ChargedU, note, err = r.netCharge(ctx, userID, *settlement)
			if err != nil {
				return Diagnosis{}, err
			}
			d.Summary += note
			if settlement.Status == "released" {
				d.Summary += " 主预留已释放不等于退款审批或到账凭证。"
			}
			if d.ChargedU == "" {
				d.NeedsHuman = true
			}
		default:
			d.Summary += " 结算状态无法确认，金额未知。"
			d.NeedsHuman = true
		}
	}
	return d, nil
}

const sourceQueryTimeout = 3 * time.Second

type sourceHealthRow struct {
	RequestID, PublicModel string
	Stream                 bool
	StartedAt              int64
	EventID                *int64
	Reason                 string
	HTTPStatus             int
	ResultValid            bool
	Outcome                string
}

type sourceSettlementRow struct {
	ID                                         uint
	RequestID, PublicModel, Status             string
	ReservedQuota, ChargedQuota, RefundedQuota int64
	Dispatched                                 bool
	UsageJSON, MissingDimensionsJSON           string
	CreatedAt                                  time.Time
}

type sourceChargeAnchor struct {
	ID                          uint
	Attempt                     int
	BillingProofID              uint
	OriginalLedgerID            int64
	ChargedQuota, RefundedQuota int64
}

type sourcePostedRefund struct {
	ChargeID      uint
	LedgerID      int64
	RefundedQuota int64
}

func (r *Reader) netCharge(ctx context.Context, userID int, settlement sourceSettlementRow) (string, string, error) {
	unknown := func(reason string) (string, string, error) {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		primary := sourceQuotaU(settlement.ChargedQuota)
		note := " 仅能核实主结算（primary）记录"
		if primary != "" {
			note += "金额 " + primary + " U"
		}
		return "", note + "；" + reason + "，重试补扣及已入账退款未完整核实，已结算净扣未知，需人工核查。", nil
	}
	if settlement.ID == 0 || settlement.ChargedQuota < 0 || settlement.RefundedQuota < 0 {
		return unknown("结算金额证据异常")
	}
	var charges []sourceChargeAnchor
	err := r.DB.WithContext(ctx).Table("ztapi_supplier_refund_charges").
		Select("id, attempt, billing_proof_id, original_ledger_id, charged_quota, refunded_quota").
		Where("user_id = ? AND settlement_id = ? AND request_id = ?", userID, settlement.ID, settlement.RequestID).
		Order("attempt ASC").Limit(3).Find(&charges).Error
	if err != nil {
		return unknown("扣款锚点证据不可用")
	}
	if len(charges) == 0 {
		return unknown("缺少扣款锚点，可能为历史记录")
	}
	if len(charges) > 2 {
		return unknown("扣款锚点数量异常")
	}
	primaryCount := 0
	gross, refunded := settlement.ChargedQuota, int64(0)
	byID := make(map[uint]sourceChargeAnchor, len(charges))
	ids := make([]uint, 0, len(charges))
	attempts := make(map[int]bool, len(charges))
	ledgers := make(map[int64]bool, len(charges))
	for _, charge := range charges {
		if charge.ID == 0 || charge.Attempt < 1 || charge.Attempt > 2 || attempts[charge.Attempt] ||
			charge.ChargedQuota < 0 || charge.RefundedQuota < 0 || charge.RefundedQuota > charge.ChargedQuota ||
			(charge.ChargedQuota > 0 && charge.OriginalLedgerID <= 0) ||
			(charge.OriginalLedgerID > 0 && ledgers[charge.OriginalLedgerID]) {
			return unknown("扣款锚点金额或入账标识不一致")
		}
		attempts[charge.Attempt] = true
		if charge.OriginalLedgerID > 0 {
			ledgers[charge.OriginalLedgerID] = true
		}
		if charge.BillingProofID == 0 {
			primaryCount++
			if charge.ChargedQuota != settlement.ChargedQuota {
				return unknown("primary 锚点与主结算金额不一致")
			}
		} else {
			if charge.ChargedQuota > math.MaxInt64-gross {
				return unknown("累计扣款超出可核实范围")
			}
			gross += charge.ChargedQuota
		}
		if charge.RefundedQuota > math.MaxInt64-refunded {
			return unknown("累计退款超出可核实范围")
		}
		refunded += charge.RefundedQuota
		ids = append(ids, charge.ID)
		byID[charge.ID] = charge
	}
	if primaryCount != 1 || refunded != settlement.RefundedQuota || refunded > gross {
		return unknown("primary 锚点缺失或累计退款与主结算不一致")
	}
	var refunds []sourcePostedRefund
	// Refunds have no user_id. Re-establish ownership through the exact charge
	// anchor, settlement and request, and never inspect their private submission.
	err = r.DB.WithContext(ctx).Table("ztapi_supplier_refunds AS f").
		Select("f.charge_id, f.ledger_id, f.refunded_quota").
		Joins("JOIN ztapi_supplier_refund_charges AS c ON c.id = f.charge_id AND c.request_id = f.request_id").
		Where("c.user_id = ? AND c.settlement_id = ? AND c.request_id = ?", userID, settlement.ID, settlement.RequestID).
		Where("f.request_id = ? AND f.charge_id IN ? AND f.status IN ? AND f.ledger_id > 0", settlement.RequestID, ids, []string{"applied", "completed"}).
		Order("f.ledger_id ASC").Limit(513).Find(&refunds).Error
	if err != nil {
		return unknown("已入账退款证据不可用")
	}
	if len(refunds) > 512 {
		return unknown("退款记录超出单次核查上限")
	}
	posted := make(map[uint]int64, len(charges))
	refundLedgers := make(map[int64]bool, len(refunds))
	for _, refund := range refunds {
		charge, ok := byID[refund.ChargeID]
		if !ok || refund.LedgerID <= 0 || refundLedgers[refund.LedgerID] || refund.RefundedQuota < 0 ||
			refund.RefundedQuota > charge.ChargedQuota-posted[refund.ChargeID] {
			return unknown("已入账退款金额或入账标识不一致")
		}
		refundLedgers[refund.LedgerID] = true
		posted[refund.ChargeID] += refund.RefundedQuota
	}
	for _, charge := range charges {
		if posted[charge.ID] != charge.RefundedQuota {
			return unknown("已入账退款与扣款锚点累计金额不一致")
		}
	}
	net := sourceQuotaU(gross - refunded)
	if net == "" {
		return unknown("金额单位无法核实")
	}
	return net, " 本次已核实的已结算净扣为 " + net + " U（主结算及重试补扣合计 " + sourceQuotaU(gross) + " U，减去已入账退款 " + sourceQuotaU(refunded) + " U）；不包含待入账退款，后续以结算记录为准。", nil
}

type sourceSignal struct {
	Dispatched                                                     *bool
	ClientCancelled, TransportComplete, HasText, HasTool, HasMedia bool
}

func sourceError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errSourceUnavailable
}

func sourceQuotaU(quota int64) string {
	if quota < 0 || common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return ""
	}
	return decimal.NewFromInt(quota).Div(decimal.NewFromFloat(common.QuotaPerUnit)).String()
}

func sourceUsageReceived(raw, missing string) bool {
	if len(raw) > 65536 || len(missing) > 8192 {
		return false
	}
	var usage struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		InputTokens      *int64 `json:"input_tokens"`
		OutputTokens     *int64 `json:"output_tokens"`
		TotalTokens      *int64 `json:"total_tokens"`
		Source           string `json:"usage_source"`
		Evidence         *struct {
			Source       string `json:"usage_source"`
			Observations int    `json:"usage_observations"`
		} `json:"ztapi_metering_evidence"`
	}
	if common.UnmarshalJsonStr(raw, &usage) != nil {
		return false
	}
	var absent []string
	if missing != "" && common.UnmarshalJsonStr(missing, &absent) != nil {
		return false
	}
	for _, code := range absent {
		if code == "upstream_usage_missing" || code == "usage_zero_unconfirmed" {
			return false
		}
	}
	source := usage.Source
	if usage.Evidence != nil {
		if usage.Evidence.Observations <= 0 || (source != "" && source != usage.Evidence.Source) {
			return false
		}
		source = usage.Evidence.Source
	}
	if source != "upstream_reported" {
		return false
	}
	found := false
	for _, value := range []*int64{usage.PromptTokens, usage.CompletionTokens, usage.InputTokens, usage.OutputTokens, usage.TotalTokens} {
		if value != nil {
			if *value < 0 {
				return false
			}
			found = true
		}
	}
	return found
}

var sourceSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bBearer\s+[^\s"'<>;,]+`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|authorization|password|secret|credentials)\s*["']?\s*[:=]\s*["']?[^\s"'<>;,}]+`),
	regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_-]+`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
}

func redactSource(value string) string {
	value = Redact(value)
	for _, pattern := range sourceSecrets {
		value = pattern.ReplaceAllString(value, "[REDACTED]")
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
}
