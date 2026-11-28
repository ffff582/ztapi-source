// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
)

const referralLedgerColumns = "id,user_id,kind,amount_micros,available_at,created_at"
const referralWithdrawalColumns = "id,amount_micros,fee_micros,net_amount_micros,status,created_at,paid_at"

// ReferralCapabilitySource is optional. Implementations must honor cancellation;
// legacy sources without this interface cannot advertise referral readiness.
type ReferralCapabilitySource interface {
	SupportsReferralV1(context.Context) bool
}

func (r *Reader) SupportsReferralV1(ctx context.Context) bool {
	if r == nil || r.DB == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Selecting the report's exact columns proves table/column read access,
		// including empty tables, without metadata SELECT * or snapshot replay.
		var entries []ReferralLedgerEntry
		if err := tx.Table("ztapi_referral_ledger_entries").Select(referralLedgerColumns).Limit(1).Find(&entries).Error; err != nil {
			return err
		}
		var withdrawals []ReferralWithdrawal
		return tx.Table("ztapi_referral_withdrawals").Select(referralWithdrawalColumns).Limit(1).Find(&withdrawals).Error
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	return err == nil && ctx.Err() == nil
}
