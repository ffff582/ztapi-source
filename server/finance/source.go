// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"context"
	"database/sql"
	"errors"
	"gorm.io/gorm"
	"sync"
	"time"
)

const snapshotLimit = 50000

type Source interface {
	Load(context.Context, time.Time) (Snapshot, error)
	Identity(context.Context, int) (User, error)
}
type Reader struct {
	SourceCommit string
	DB           *gorm.DB
	QuotaPerUnit int64
	mu           sync.Mutex
	cached       Snapshot
	cachedAt     time.Time
	before       time.Time
}

func (r *Reader) Identity(ctx context.Context, id int) (User, error) {
	var u User
	e := r.DB.WithContext(ctx).Table("users").Select("id,username,role,status,deleted_at").Where("id = ? AND deleted_at IS NULL", id).Take(&u).Error
	return u, e
}
func bounded(tx *gorm.DB, table, columns string, dest any, where string, args ...any) error {
	query := tx.Table(table).Select(columns).Limit(snapshotLimit + 1)
	if where != "" {
		query = query.Where(where, args...)
	}
	result := query.Find(dest)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > snapshotLimit {
		return errors.New("finance snapshot limit exceeded")
	}
	return nil
}
func (r *Reader) Load(ctx context.Context, before time.Time) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cachedAt.IsZero() && time.Since(r.cachedAt) < 15*time.Second && r.before.Equal(before) {
		return r.cached, nil
	}
	s := Snapshot{QuotaPerUnit: r.QuotaPerUnit, SourceCommit: r.SourceCommit}
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := bounded(tx, "users", "id,username", &s.Users, ""); e != nil {
			return e
		}
		if e := bounded(tx, "balance_ledgers", "id,user_id,delta,balance_before,balance_after,source_type,request_id,idempotency_key,created_at", &s.Ledgers, "created_at < ?", before); e != nil {
			return e
		}
		if e := bounded(tx, "ztapi_request_settlements s LEFT JOIN ztapi_settlement_log_outboxes o ON o.operation_id = s.operation_id", "s.id,s.user_id,s.request_id,s.operation_id,s.status,s.public_model,s.price_snapshot_json,s.charge_dimensions_json,s.missing_dimensions_json,s.reserved_quota,s.charged_quota,s.final_attempt,s.created_at,COALESCE(o.created_at,0) AS completed_at", &s.Settlements, "s.created_at < ?", before); e != nil {
			return e
		}
		if e := bounded(tx, "ztapi_request_attempts", "settlement_id,attempt,channel_id,created_at", &s.Attempts, "created_at < ?", before); e != nil {
			return e
		}
		if e := bounded(tx, "top_ups t JOIN usdt_top_up_orders u ON u.top_up_id = t.id", "t.id,t.user_id,t.trade_no,t.status,u.credit_units,u.pay_amount_micros,t.complete_time", &s.Payments, "t.status = ? AND u.status = ? AND t.complete_time < ?", "success", "settled", before.Unix()); e != nil {
			return e
		}
		if e := bounded(tx, "channels", "id,name,base_url", &s.Channels, ""); e != nil {
			return e
		}
		if e := bounded(tx, "ztapi_supplier_refund_charges c LEFT JOIN ztapi_settlement_log_outboxes o ON o.operation_id=c.billing_operation_id", "c.id,c.request_id,c.user_id,c.original_ledger_id,c.charged_quota,c.billing_proof_id,c.channel_id,COALESCE(o.created_at,0) AS posted_at", &s.Charges, "c.created_at < ?", before); e != nil {
			return e
		}
		return bounded(tx, "ztapi_supplier_refunds", "ledger_id,charge_id,refunded_quota", &s.Refunds, "status IN ? AND ledger_id > 0", []string{"applied", "completed"})
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return Snapshot{}, err
	}
	r.cached = s
	r.cachedAt = time.Now()
	r.before = before
	return s, nil
}
