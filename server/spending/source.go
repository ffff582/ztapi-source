// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package spending

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// DB must be a separate SELECT-only handle, never the global model DB.
type Reader struct {
	DB                   *gorm.DB
	VerifiedQuotaPerUnit string
	Limit                int
}
type settlement struct {
	ID                                                                                                                     uint
	UserID                                                                                                                 int
	OperationID, RequestID, Status, PublicModel, PriceSnapshotJSON, UsageJSON, MissingDimensionsJSON, ChargeDimensionsJSON string
	ReservedQuota, ChargedQuota, RefundedQuota, CompletedAt, LogID                                                         int64
}
type ledger struct {
	ID                                    int64
	UserID                                int
	RequestID, SourceType, IdempotencyKey string
	Delta                                 int64
	CreatedAt                             time.Time
}
type charge struct {
	ID, SettlementID                                                  uint
	UserID                                                            int
	RequestID, BillingOperationID, PriceSnapshotJSON                  string
	BillingProofID                                                    uint
	OriginalLedgerID, ChargedQuota, RefundedQuota, CompletedAt, LogID int64
}
type refund struct {
	ID, ChargeID            uint
	RequestID               string
	LedgerID, RefundedQuota int64
}
type legacyLog struct {
	ID, CreatedAt, Quota int64
	RequestID            string
}

const settlementProjection = "s.id,s.user_id,s.operation_id,s.request_id,s.status,s.public_model,CASE WHEN LENGTH(s.price_snapshot_json)<=262144 THEN s.price_snapshot_json ELSE '' END AS price_snapshot_json,CASE WHEN LENGTH(s.usage_json)<=65536 THEN s.usage_json ELSE '' END AS usage_json,CASE WHEN LENGTH(s.charge_dimensions_json)<=65536 THEN s.charge_dimensions_json ELSE '' END AS charge_dimensions_json,CASE WHEN LENGTH(s.missing_dimensions_json)<=8192 THEN s.missing_dimensions_json ELSE '' END AS missing_dimensions_json,s.reserved_quota,s.charged_quota,s.refunded_quota,COALESCE(o.created_at,0) AS completed_at,COALESCE(o.log_id,0) AS log_id"
const chargeProjection = "c.id,c.settlement_id,c.user_id,c.request_id,c.billing_proof_id,c.billing_operation_id,c.original_ledger_id,c.charged_quota,c.refunded_quota,CASE WHEN LENGTH(c.price_snapshot_json)<=262144 THEN c.price_snapshot_json ELSE '' END AS price_snapshot_json,COALESCE(o.created_at,0) AS completed_at,COALESCE(o.log_id,0) AS log_id"

func readRows[T any](q *gorm.DB, dest *[]T, limit int, budget *int) error {
	var page, all []T
	result := q.FindInBatches(&page, 32, func(_ *gorm.DB, _ int) error {
		if len(all)+len(page) > limit {
			return ErrUnavailable
		}
		for _, row := range page {
			switch v := any(row).(type) {
			case settlement:
				*budget += len(v.PriceSnapshotJSON) + len(v.UsageJSON) + len(v.ChargeDimensionsJSON) + len(v.MissingDimensionsJSON)
			case charge:
				*budget += len(v.PriceSnapshotJSON)
			}
			if *budget > 8<<20 {
				return ErrUnavailable
			}
		}
		all = append(all, page...)
		return nil
	})
	if result.Error != nil {
		return ErrUnavailable
	}
	*dest = all
	return nil
}
func readScoped[T any](q *gorm.DB, column string, keys []string, dest *[]T, limit int, budget *int) error {
	for start := 0; start < len(keys); start += 64 {
		end := start + 64
		if end > len(keys) {
			end = len(keys)
		}
		var page []T
		if err := readRows(q.Session(&gorm.Session{}).Where(column+" IN ?", keys[start:end]), &page, limit-len(*dest), budget); err != nil {
			return err
		}
		*dest = append(*dest, page...)
	}
	return nil
}
func (r *Reader) Preflight(ctx context.Context) error {
	if r == nil || r.DB == nil || r.VerifiedQuotaPerUnit != "500000" {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	metadata := RequiredSourceColumns()
	tables := make([]string, 0, len(metadata))
	for table := range metadata {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		where := "id = ?"
		var value any = -1
		if table == "ztapi_settlement_log_outboxes" {
			where = "operation_id = ?"
			value = ""
		}
		var rows []map[string]any
		if err := r.DB.WithContext(ctx).Table(table).Select(metadata[table]).Where(where, value).Limit(1).Find(&rows).Error; err != nil {
			return ErrUnavailable
		}
	}
	return nil
}
func (r *Reader) Identity(ctx context.Context, owner int) (Identity, error) {
	if r == nil || r.DB == nil || owner <= 0 {
		return Identity{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var user Identity
	if err := r.DB.WithContext(ctx).Table("users").Select("id,status,deleted_at,quota").Where("id = ?", owner).Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, ErrUnavailable
	}
	return user, nil
}

// column is a static server-owned identifier. SQLite binds time.Time as TEXT
// with its original offset; normalize both operands before comparing instants.
func dateTimeWindow(q *gorm.DB, column string, start, end time.Time) *gorm.DB {
	if q.Dialector.Name() == "sqlite" {
		return q.Where("julianday("+column+") >= julianday(?) AND julianday("+column+") < julianday(?)", start.UTC(), end.UTC())
	}
	return q.Where(column+" >= ? AND "+column+" < ?", start, end)
}

func (r *Reader) Load(ctx context.Context, owner int, now time.Time) (Snapshot, error) {
	out := Snapshot{Overview: emptyOverview(now)}
	if r == nil || r.DB == nil || owner <= 0 {
		return out, ErrUnavailable
	}
	unit, err := decimal.NewFromString(r.VerifiedQuotaPerUnit)
	if err != nil || r.VerifiedQuotaPerUnit != "500000" {
		return out, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	limit := r.Limit
	if limit <= 0 || limit > 2048 {
		limit = 2048
	}
	start := out.Overview.DayStart
	if hour := now.Add(-time.Hour); hour.Before(start) {
		start = hour
	}
	var user Identity
	var pending int64
	var primary, parents []settlement
	var selectedExtra, charges []charge
	var refunds []refund
	var ledgers []ledger
	var unknown, legacy []legacyLog
	err = r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		budget := 0
		if e := tx.Table("users").Select("id,status,deleted_at,quota").Where("id = ?", owner).Take(&user).Error; e != nil {
			return e
		}
		if user.ID != owner || user.Status != 1 || user.DeletedAt != nil {
			return ErrUnavailable
		}
		if e := tx.Table("ztapi_request_settlements").Select("id").Where("user_id = ? AND status IN ?", owner, []string{"pending", "reserved"}).Count(&pending).Error; e != nil {
			return e
		}
		// Confirmation time, not request age, selects spend. A late extra charge may
		// belong to a parent that is years old; its parent is loaded below by request.
		if e := readRows(tx.Table("ztapi_request_settlements s").Select(settlementProjection).Joins("JOIN ztapi_settlement_log_outboxes o ON o.operation_id=s.operation_id").Where("s.user_id = ? AND o.created_at >= ? AND o.created_at < ?", owner, start.Unix(), now.Unix()), &primary, limit, &budget); e != nil {
			return e
		}
		if e := readRows(tx.Table("ztapi_supplier_refund_charges c").Select(chargeProjection).Joins("JOIN ztapi_settlement_log_outboxes o ON o.operation_id=c.billing_operation_id").Where("c.user_id = ? AND c.billing_proof_id > ? AND o.created_at >= ? AND o.created_at < ?", owner, 0, start.Unix(), now.Unix()), &selectedExtra, limit, &budget); e != nil {
			return e
		}
		refundQuery := tx.Table("ztapi_supplier_refunds f").Select("f.id,f.charge_id,f.request_id,f.ledger_id,f.refunded_quota").Joins("JOIN ztapi_supplier_refund_charges c ON c.id=f.charge_id AND c.request_id=f.request_id").Joins("JOIN balance_ledgers l ON l.id=f.ledger_id").Where("c.user_id = ? AND l.user_id = ? AND f.status IN ?", owner, owner, []string{"applied", "completed"})
		if e := readRows(dateTimeWindow(refundQuery, "l.created_at", out.Overview.DayStart, now.In(Beijing)), &refunds, limit, &budget); e != nil {
			return e
		}

		// updated_at is only an anomaly hint. It is NEVER a charge posting time.
		missingQuery := tx.Table("ztapi_request_settlements s").Select("s.id,s.request_id").Joins("LEFT JOIN ztapi_settlement_log_outboxes o ON o.operation_id=s.operation_id").Where("s.user_id = ? AND s.status = ? AND (o.operation_id IS NULL OR o.created_at <= ?)", owner, "settled", 0)
		if e := readRows(dateTimeWindow(missingQuery, "s.updated_at", start, now.In(Beijing)), &unknown, limit, &budget); e != nil {
			return e
		}
		var orphan []legacyLog
		orphanQuery := tx.Table("balance_ledgers l").Select("l.id,l.request_id").Where("l.user_id = ?", owner).Where(unprovenLedgerSQL(tx))
		if e := readRows(dateTimeWindow(orphanQuery, "l.created_at", start, now.In(Beijing)), &orphan, limit, &budget); e != nil {
			return e
		}
		unknown = append(unknown, orphan...)
		// These anti-joins exclude all managed/extra projections, including delayed
		// delivery of old charges, without loading any owner's old settlements.
		if e := readRows(tx.Table("logs").Select("logs.id,logs.created_at,logs.request_id,logs.quota").Where("logs.user_id = ? AND logs.type = ? AND logs.created_at >= ? AND logs.created_at < ?", owner, 2, start.Unix(), now.Unix()).Where(`NOT EXISTS (SELECT 1 FROM ztapi_request_settlements s WHERE s.user_id=logs.user_id AND s.request_id=logs.request_id)`).Where(`NOT EXISTS (SELECT 1 FROM ztapi_settlement_log_outboxes o JOIN ztapi_request_settlements s ON s.operation_id=o.operation_id WHERE s.user_id=logs.user_id AND o.log_id=logs.id)`).Where(`NOT EXISTS (SELECT 1 FROM ztapi_settlement_log_outboxes o JOIN ztapi_supplier_refund_charges c ON c.billing_operation_id=o.operation_id WHERE c.user_id=logs.user_id AND c.billing_proof_id>0 AND o.log_id=logs.id)`), &legacy, limit, &budget); e != nil {
			return e
		}

		requests := map[string]bool{}
		for _, s := range primary {
			requests[s.RequestID] = true
		}
		for _, c := range selectedExtra {
			requests[c.RequestID] = true
		}
		for _, f := range refunds {
			requests[f.RequestID] = true
		}
		keys := make([]string, 0, len(requests))
		for request := range requests {
			keys = append(keys, request)
		}
		sort.Strings(keys)
		if len(keys) > limit {
			return ErrUnavailable
		}
		if e := readScoped(tx.Table("ztapi_request_settlements s").Select(settlementProjection).Joins("LEFT JOIN ztapi_settlement_log_outboxes o ON o.operation_id=s.operation_id").Where("s.user_id = ?", owner), "s.request_id", keys, &parents, limit, &budget); e != nil {
			return e
		}
		if e := readScoped(tx.Table("ztapi_supplier_refund_charges c").Select(chargeProjection).Joins("LEFT JOIN ztapi_settlement_log_outboxes o ON o.operation_id=c.billing_operation_id").Where("c.user_id = ?", owner), "c.request_id", keys, &charges, limit, &budget); e != nil {
			return e
		}
		return readScoped(tx.Table("balance_ledgers").Select("id,user_id,request_id,source_type,idempotency_key,delta,created_at").Where("user_id = ? AND source_type IN ?", owner, []string{"usage_reservation", "usage_settlement", "billing_refund", "supplier_refund"}), "request_id", keys, &ledgers, limit, &budget)
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if user.ID != owner || user.Status != 1 || user.DeletedAt != nil {
		return out, ErrUnavailable
	}
	balance := decimal.NewFromInt(user.Quota).Div(unit).String()
	out.Overview.AvailableBalanceU = &balance
	out.Overview.PendingRequests = int(pending)
	// Financial coverage can fail independently of a freshly verified wallet.
	if err != nil {
		out.Overview.IncompleteRequests = 1
		return out, nil
	}
	badRequests := map[string]bool{}
	bad := func(request string) { badRequests[request] = true }
	for _, v := range unknown {
		key := v.RequestID
		if key == "" {
			key = fmt.Sprint("ledger:", v.ID)
		}
		bad(key)
	}
	for _, v := range legacy {
		key := v.RequestID
		if key == "" {
			key = fmt.Sprint("log:", v.ID)
		}
		bad(key)
	}
	sm := map[string]settlement{}
	sid := map[uint]settlement{}
	ops := map[string]bool{}
	for _, s := range parents {
		if _, exists := sm[s.RequestID]; exists || s.RequestID == "" || s.OperationID == "" || s.UserID != owner || ops[s.OperationID] {
			bad(s.RequestID)
			continue
		}
		sm[s.RequestID] = s
		sid[s.ID] = s
		ops[s.OperationID] = true
	}
	lm := map[int64]ledger{}
	for _, l := range ledgers {
		if _, exists := lm[l.ID]; exists {
			bad(l.RequestID)
		}
		lm[l.ID] = l
	}
	cm := map[uint]charge{}
	extraLedgers := map[int64]bool{}
	primaryAnchors := map[uint]int{}
	primaryRequests := map[string]bool{}
	for _, s := range primary {
		primaryRequests[s.RequestID] = true
	}
	for _, c := range charges {
		cm[c.ID] = c
		if c.BillingProofID > 0 {
			extraLedgers[c.OriginalLedgerID] = true
			continue
		}
		if primaryRequests[c.RequestID] {
			primaryAnchors[c.SettlementID]++
			s, ok := sid[c.SettlementID]
			if !ok || primaryAnchors[c.SettlementID] > 1 || s.RequestID != c.RequestID || c.ChargedQuota != s.ChargedQuota {
				bad(c.RequestID)
			}
		}
	}
	walletTotals := map[string]decimal.Decimal{}
	for _, l := range ledgers {
		if !extraLedgers[l.ID] && (l.SourceType == "usage_reservation" || l.SourceType == "usage_settlement" || l.SourceType == "billing_refund") {
			walletTotals[l.RequestID] = walletTotals[l.RequestID].Add(decimal.NewFromInt(l.Delta))
		}
	}
	primaryWalletValid := func(s settlement) bool {
		return s.Status == "settled" && s.ChargedQuota >= 0 && s.ReservedQuota >= 0 && walletTotals[s.RequestID].Equal(decimal.NewFromInt(-s.ChargedQuota))
	}
	daily, hourly, refundDaily := decimal.Zero, decimal.Zero, decimal.Zero
	inWindow := func(at int64, from time.Time) bool { return at >= from.Unix() && at < now.Unix() }
	add := func(q int64, at int64, den decimal.Decimal) {
		value := decimal.NewFromInt(q).Div(den)
		if inWindow(at, out.Overview.DayStart) {
			daily = daily.Add(value)
		}
		if inWindow(at, now.Add(-time.Hour)) {
			hourly = hourly.Add(value)
		}
	}
	for _, s := range primary {
		parent, ok := sm[s.RequestID]
		if !ok || parent.ID != s.ID || s.Status != "settled" || s.CompletedAt <= 0 || s.ChargedQuota < 0 || s.ReservedQuota < 0 || s.MissingDimensionsJSON != "[]" {
			bad(s.RequestID)
			continue
		}
		den, e := snapshotUnit(s.PriceSnapshotJSON, unit)
		if e != nil {
			bad(s.RequestID)
			continue
		}
		if !primaryWalletValid(s) {
			bad(s.RequestID)
			continue
		}
		add(s.ChargedQuota, s.CompletedAt, den)
		if n, valid := normalizedInput(s.UsageJSON, s.ChargeDimensionsJSON); valid {
			out.Inputs = append(out.Inputs, Input{RequestID: s.RequestID, Model: s.PublicModel, Tokens: n, At: time.Unix(s.CompletedAt, 0).UTC()})
		}
	}
	seenExtraLedger := map[int64]bool{}
	for _, selected := range selectedExtra {
		c, ok := cm[selected.ID]
		s, parentExists := sid[c.SettlementID]
		if !ok || !parentExists || s.Status != "settled" || s.UserID != owner || s.RequestID != c.RequestID || c.UserID != owner || c.BillingProofID == 0 || c.ChargedQuota < 0 || c.BillingOperationID == "" || ops[c.BillingOperationID] || c.CompletedAt <= 0 {
			bad(selected.RequestID)
			continue
		}
		ops[c.BillingOperationID] = true
		den, e := snapshotUnit(c.PriceSnapshotJSON, unit)
		if e != nil {
			bad(c.RequestID)
			continue
		}
		if c.ChargedQuota > 0 {
			l, exists := lm[c.OriginalLedgerID]
			if !exists || seenExtraLedger[l.ID] || l.UserID != owner || l.RequestID != c.RequestID || l.SourceType != "usage_settlement" || !strings.HasPrefix(l.IdempotencyKey, "attempt-billing:") || l.Delta != -c.ChargedQuota {
				bad(c.RequestID)
				continue
			}
			seenExtraLedger[l.ID] = true
		}
		add(c.ChargedQuota, c.CompletedAt, den)
	}
	seenRefund := map[int64]bool{}
	refunded := map[uint]int64{}
	for _, f := range refunds {
		c, exists := cm[f.ChargeID]
		s, parentExists := sid[c.SettlementID]
		l, ledgerExists := lm[f.LedgerID]
		if !exists || !parentExists || s.Status != "settled" || s.UserID != owner || s.RequestID != c.RequestID || c.RequestID != f.RequestID || c.UserID != owner || !ledgerExists || seenRefund[f.LedgerID] || l.UserID != owner || l.RequestID != f.RequestID || l.SourceType != "supplier_refund" || f.RefundedQuota <= 0 || l.Delta != f.RefundedQuota || c.RefundedQuota < 0 || c.RefundedQuota > c.ChargedQuota || f.RefundedQuota > c.RefundedQuota-refunded[c.ID] {
			bad(f.RequestID)
			continue
		}
		if c.BillingProofID == 0 {
			if c.ChargedQuota != s.ChargedQuota || s.CompletedAt <= 0 || !primaryWalletValid(s) {
				bad(f.RequestID)
				continue
			}
		} else {
			original, ok := lm[c.OriginalLedgerID]
			if !ok || c.CompletedAt <= 0 || original.UserID != owner || original.RequestID != c.RequestID || original.SourceType != "usage_settlement" || !strings.HasPrefix(original.IdempotencyKey, "attempt-billing:") || original.Delta != -c.ChargedQuota {
				bad(f.RequestID)
				continue
			}
		}
		seenRefund[f.LedgerID] = true
		refunded[c.ID] += f.RefundedQuota
		den, e := snapshotUnit(c.PriceSnapshotJSON, unit)
		if e != nil {
			bad(f.RequestID)
			continue
		}
		if inWindow(l.CreatedAt.Unix(), out.Overview.DayStart) {
			refundDaily = refundDaily.Add(decimal.NewFromInt(f.RefundedQuota).Div(den))
		}
	}
	out.Overview.IncompleteRequests = len(badRequests)
	out.Overview.Complete = len(badRequests) == 0
	if out.Overview.Complete {
		d, h, f := daily.String(), hourly.String(), refundDaily.String()
		out.Overview.ConfirmedDailyU = &d
		out.Overview.ConfirmedHourlyU = &h
		out.Overview.ConfirmedRefundsDailyU = &f
	}
	return out, nil
}

// Only static SQL is composed here; owner, times and scope lists are parameters.
func unprovenLedgerSQL(db *gorm.DB) string {
	key := "'ztapi:' || s.operation_id || ':settle'"
	if db.Dialector.Name() == "mysql" {
		key = "CONCAT('ztapi:',s.operation_id,':settle')"
	}
	return `((l.source_type='usage_settlement' AND NOT EXISTS (SELECT 1 FROM ztapi_request_settlements s JOIN ztapi_settlement_log_outboxes o ON o.operation_id=s.operation_id WHERE s.user_id=l.user_id AND s.request_id=l.request_id AND s.status='settled' AND o.created_at>0 AND l.idempotency_key=` + key + `) AND NOT EXISTS (SELECT 1 FROM ztapi_supplier_refund_charges c JOIN ztapi_settlement_log_outboxes o ON o.operation_id=c.billing_operation_id WHERE c.user_id=l.user_id AND c.request_id=l.request_id AND c.original_ledger_id=l.id AND c.billing_proof_id>0 AND o.created_at>0)) OR (l.source_type IN ('usage_reservation','billing_refund') AND NOT EXISTS (SELECT 1 FROM ztapi_request_settlements s WHERE s.user_id=l.user_id AND s.request_id=l.request_id)) OR (l.source_type='supplier_refund' AND NOT EXISTS (SELECT 1 FROM ztapi_supplier_refunds f JOIN ztapi_supplier_refund_charges c ON c.id=f.charge_id AND c.request_id=f.request_id WHERE f.ledger_id=l.id AND f.request_id=l.request_id AND c.user_id=l.user_id AND f.status IN ('applied','completed') AND f.refunded_quota=l.delta)))`
}
func snapshotUnit(raw string, fallback decimal.Decimal) (decimal.Decimal, error) {
	if len(raw) > 262144 || common.RejectDuplicateJsonObjectMembers(bytes.NewBufferString(raw)) != nil {
		return decimal.Zero, ErrUnavailable
	}
	var obj map[string]json.RawMessage
	if common.UnmarshalJsonStr(raw, &obj) != nil || obj == nil {
		return decimal.Zero, ErrUnavailable
	}
	for _, key := range []string{"quota_per_unit", "ztapi_quota_per_unit"} {
		value, present := obj[key]
		if !present {
			continue
		}
		var text string
		if common.Unmarshal(value, &text) != nil {
			text = string(value)
		}
		d, e := decimal.NewFromString(text)
		if e != nil || d.String() != text || !d.IsPositive() || !d.Equal(d.Truncate(0)) || !d.Equal(fallback) {
			return decimal.Zero, ErrUnavailable
		}
	}
	return fallback, nil
}
func normalizedInput(raw string, dimensionJSON string) (int64, bool) {
	if len(raw) > 65536 || len(dimensionJSON) > 65536 {
		return 0, false
	}
	var usage struct {
		UsageSource string `json:"usage_source"`
		Evidence    *struct {
			UsageSource  string `json:"usage_source"`
			Observations int    `json:"usage_observations"`
		} `json:"ztapi_metering_evidence"`
	}
	if common.RejectDuplicateJsonObjectMembers(bytes.NewBufferString(raw)) != nil || common.UnmarshalJsonStr(raw, &usage) != nil || usage.UsageSource != "upstream_reported" {
		return 0, false
	}
	if usage.Evidence != nil && (usage.Evidence.UsageSource != "upstream_reported" || usage.Evidence.Observations <= 0) {
		return 0, false
	}
	var dimensions []model.ZTAPISupplierRefundDimension
	if common.RejectDuplicateJsonObjectMembers(bytes.NewBufferString(dimensionJSON)) != nil || common.UnmarshalJsonStr(dimensionJSON, &dimensions) != nil || len(dimensions) > 128 {
		return 0, false
	}
	var tokens int64
	found := false
	for _, d := range dimensions {
		if d.Dimension != "input_tokens" {
			continue
		}
		if found {
			return 0, false
		}
		value, e := strconv.ParseInt(d.Units, 10, 64)
		if e != nil || value <= 0 || strconv.FormatInt(value, 10) != d.Units {
			return 0, false
		}
		tokens = value
		found = true
	}
	return tokens, found
}
