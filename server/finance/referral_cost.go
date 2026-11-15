// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"context"
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ReferralLedgerEntry struct {
	ID, AmountMicros       int64
	UserID                 int
	Kind                   string
	AvailableAt, CreatedAt int64
}

func (ReferralLedgerEntry) TableName() string { return "ztapi_referral_ledger_entries" }

// Financial projections deliberately exclude wallet, hash and user identity data.
type ReferralWithdrawal struct {
	ID, AmountMicros, FeeMicros, NetAmountMicros int64
	Status                                       string
	CreatedAt, PaidAt                            int64
}

func (ReferralWithdrawal) TableName() string { return "ztapi_referral_withdrawals" }

type ReferralSnapshot struct {
	Supported      bool
	Status, Reason string
	AsOf           time.Time
	Entries        []ReferralLedgerEntry
	Withdrawals    []ReferralWithdrawal
}

type ReferralReport struct {
	Supported            bool      `json:"supported"`
	Status               string    `json:"status"`
	Reason               string    `json:"reason"`
	Currency             string    `json:"currency"`
	AsOf                 time.Time `json:"as_of"`
	Accrued              *string   `json:"accrued"`
	RefundReversal       *string   `json:"refund_reversal"`
	ManualLoss           *string   `json:"manual_loss"`
	NetCommissionExpense *string   `json:"net_commission_expense"`
	FrozenPayable        *string   `json:"frozen_payable"`
	WithdrawalReserved   *string   `json:"withdrawal_reserved"`
	Paid                 *string   `json:"paid"`
	PlatformNetFee       *string   `json:"platform_net_fee"`
	NetworkFeeStatus     string    `json:"network_fee_status"`
}

// Optional read failures must not abort the core snapshot on PostgreSQL.
func (r *Reader) loadReferrals(ctx context.Context, before time.Time) *ReferralSnapshot {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s := &ReferralSnapshot{Status: "unsupported", Reason: "referral_schema_missing", AsOf: before}
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range []struct {
			projection any
			columns    []string
		}{
			{&ReferralLedgerEntry{}, []string{"id", "user_id", "kind", "amount_micros", "available_at", "created_at"}},
			{&ReferralWithdrawal{}, []string{"id", "amount_micros", "fee_micros", "net_amount_micros", "status", "created_at", "paid_at"}},
		} {
			if !tx.Migrator().HasTable(table.projection) {
				return nil
			}
			for _, c := range table.columns {
				if !tx.Migrator().HasColumn(table.projection, c) {
					s.Reason = "referral_schema_outdated"
					return nil
				}
			}
		}
		s.Supported = true
		if err := bounded(tx, "ztapi_referral_ledger_entries", referralLedgerColumns, &s.Entries, "created_at < ?", before.Unix()); err != nil {
			return err
		}
		if err := bounded(tx, "ztapi_referral_withdrawals", referralWithdrawalColumns, &s.Withdrawals, "created_at < ?", before.Unix()); err != nil {
			return err
		}
		s.Status, s.Reason = "available", ""
		return nil
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		s.Status, s.Reason = "unavailable", "referral_read_failed"
		s.Entries, s.Withdrawals = nil, nil
	}
	return s
}

func (r *replay) buildReferral(s *ReferralSnapshot) (decimal.Decimal, bool) {
	out := &ReferralReport{Currency: "USDT", Status: "unsupported", Reason: "referral_source_missing", NetworkFeeStatus: "unknown"}
	r.report.Referral = out
	if s != nil {
		out.Supported, out.Status, out.Reason, out.AsOf = s.Supported, s.Status, s.Reason, s.AsOf
	}
	unknown := func(reason string) (decimal.Decimal, bool) {
		out.Reason = reason
		r.issue(reason, "referral", 0)
		return zero, false
	}
	if s == nil || !s.Supported || s.Status != "available" {
		if out.Reason == "" {
			out.Reason = "referral_source_unavailable"
		}
		return unknown(out.Reason)
	}
	invalid := func(reason string) (decimal.Decimal, bool) { out.Status = "unavailable"; return unknown(reason) }
	if s.AsOf.IsZero() || s.AsOf.After(r.report.AsOf) {
		return invalid("referral_snapshot_invalid")
	}
	accrued, reversal, loss, frozen, reserved, paid := zero, zero, zero, zero, zero, zero
	seen := map[int64]bool{}
	for _, e := range s.Entries {
		if e.ID <= 0 || seen[e.ID] || e.CreatedAt <= 0 || e.CreatedAt >= s.AsOf.Unix() || e.UserID <= 0 {
			return invalid("referral_ledger_invalid")
		}
		seen[e.ID] = true
		value := decimal.New(e.AmountMicros, -6)
		if (e.Kind == "commission" || e.Kind == "manual_loss") && !value.IsPositive() || e.Kind == "refund" && !value.IsNegative() {
			return invalid("referral_ledger_invalid")
		}
		switch e.Kind {
		case "commission", "refund":
			if e.AvailableAt <= 0 {
				return invalid("referral_availability_unknown")
			}
			if e.AvailableAt > s.AsOf.Unix() {
				frozen = frozen.Add(value)
			}
			if r.inMonth(time.Unix(e.CreatedAt, 0)) {
				if e.Kind == "commission" {
					accrued = accrued.Add(value)
				} else {
					reversal = reversal.Add(value)
				}
			}
		case "manual_loss":
			if r.inMonth(time.Unix(e.CreatedAt, 0)) {
				loss = loss.Add(value)
			}
		default:
			return invalid("referral_ledger_invalid")
		}
	}
	seen = map[int64]bool{}
	for _, w := range s.Withdrawals {
		if w.ID <= 0 || seen[w.ID] || w.CreatedAt <= 0 || w.CreatedAt >= s.AsOf.Unix() || w.AmountMicros <= 0 || w.FeeMicros < 0 || w.NetAmountMicros < 0 {
			return invalid("referral_withdrawal_invalid")
		}
		seen[w.ID] = true
		value := decimal.New(w.AmountMicros, -6)
		switch w.Status {
		case "requested", "fee_quoted", "confirmed", "paying", "submitted_unknown", "manual_review":
			reserved = reserved.Add(value)
		case "paid":
			if w.PaidAt <= 0 || w.PaidAt < w.CreatedAt || w.PaidAt >= s.AsOf.Unix() || w.FeeMicros >= w.AmountMicros || w.NetAmountMicros != w.AmountMicros-w.FeeMicros {
				return invalid("referral_payment_invalid")
			}
			paid = paid.Add(value)
		case "rejected", "cancelled":
		default:
			return invalid("referral_withdrawal_status_unknown")
		}
	}
	if frozen.IsNegative() {
		return invalid("referral_payable_invalid")
	}
	// Refunds reverse even paid commissions; unrecoverable paid portions are
	// restored by manual_loss once. Paying a withdrawal is never another expense.
	expense := accrued.Add(reversal).Add(loss)
	out.Accrued, out.RefundReversal, out.ManualLoss = stringPtr(fixed(accrued)), stringPtr(fixed(reversal)), stringPtr(fixed(loss))
	out.NetCommissionExpense = stringPtr(fixed(expense))
	out.FrozenPayable, out.WithdrawalReserved, out.Paid = stringPtr(fixed(frozen)), stringPtr(fixed(reserved)), stringPtr(fixed(paid))
	return expense, true
}
