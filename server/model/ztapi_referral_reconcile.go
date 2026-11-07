package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ZTAPIReferralReconcileResult struct {
	Scanned int `json:"scanned"`
	Created int `json:"created"`
	Skipped int `json:"skipped"`
}

// Repeated, bounded, cyclic scans operate on committed source evidence, not on
// cache callbacks. The durable cursor wraps, so late settlements and repaired
// evidence below the cursor are retried and invalid evidence cannot starve work.
func ReconcileZTAPIReferralSettlements(ctx context.Context, limit int) (ZTAPIReferralReconcileResult, error) {
	r := ZTAPIReferralReconcileResult{}
	if limit <= 0 || limit > 1000 {
		return r, ErrZTAPIReferralInvalid
	}
	err := referralTransaction(ctx, func(tx *gorm.DB) error {
		seed := ZTAPIReferralCampaign{ID: 1, RuleVersion: ZTAPIReferralRuleVersion}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
			return err
		}
		var c ZTAPIReferralCampaign
		if err := referralForUpdate(tx).First(&c, 1).Error; err != nil {
			return err
		}
		query := func(cursor int64) ([]USDTTopUpOrder, error) {
			var orders []USDTTopUpOrder
			q := tx.Table("usdt_top_up_orders AS o").Select("o.*").
				Joins("JOIN ztapi_referral_bindings AS b ON b.user_id = o.user_id").
				Where("o.id > ? AND o.status = ? AND o.block_timestamp_ms > 0 AND o.settled_at > 0", cursor, USDTTopUpStatusSettled).
				Where("NOT EXISTS (SELECT 1 FROM ztapi_referral_commissions AS rc WHERE rc.order_id = o.id)").Order("o.id ASC").Limit(limit)
			err := q.Find(&orders).Error
			return orders, err
		}
		orders, err := query(c.ReconcileCursor)
		if err != nil {
			return err
		}
		if len(orders) == 0 && c.ReconcileCursor != 0 {
			orders, err = query(0)
			if err != nil {
				return err
			}
			c.ReconcileCursor = 0
		}
		for _, order := range orders {
			if err := ctx.Err(); err != nil {
				return err
			}
			r.Scanned++
			c.ReconcileCursor = order.ID
			applied, err := referralRecordSettlement(tx, order)
			if err != nil {
				return err
			}
			if applied {
				r.Created++
			} else {
				r.Skipped++
			}
		}
		return tx.Model(&ZTAPIReferralCampaign{}).Where("id = ?", 1).Update("reconcile_cursor", c.ReconcileCursor).Error
	})
	if err != nil {
		return ZTAPIReferralReconcileResult{}, err
	}
	return r, nil
}

func referralRecordSettlement(tx *gorm.DB, order USDTTopUpOrder) (bool, error) {
	var b ZTAPIReferralBinding
	if err := tx.Where("user_id = ?", order.UserID).First(&b).Error; err != nil {
		return false, err
	}
	if order.TxID == nil || *order.TxID == "" || order.BlockTimestampMS == nil || order.SettledAt == nil || *order.SettledAt <= 0 {
		return false, nil
	}
	block := *order.BlockTimestampMS
	if block/1000 < b.BoundAt || block/1000 < b.CampaignStartedAt || block/1000 >= b.ExpiresAt || block/1000 > *order.SettledAt {
		return false, nil
	}
	if order.Network != USDTTopUpNetworkTronMainnet || order.Asset != USDTTopUpAssetUSDT || order.ContractAddress != USDTTopUpContractAddress || order.PayAmountMicros != order.ExactPayAmountMicros() || order.ExactPayAmountMicros() == 0 {
		return false, nil
	}
	if block/1000 < order.CreatedAt || block/1000 > order.ExpiresAt || order.TxFrom == nil || *order.TxFrom == "" {
		return false, nil
	}
	var topUp TopUp
	err := tx.First(&topUp, order.TopUpID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if topUp.UserId != order.UserID || topUp.TradeNo != order.TradeNo || topUp.Status != common.TopUpStatusSuccess || topUp.PaymentProvider != PaymentProviderUSDTTRC20 || topUp.PaymentMethod != PaymentMethodUSDTTRC20 || topUp.Amount != order.CreditUnits || topUp.CompleteTime != *order.SettledAt {
		return false, nil
	}
	var ledger BalanceLedger
	err = tx.Where("idempotency_key = ?", "usdt-trc20:"+*order.TxID).First(&ledger).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// No current-price/quota conversion is used to reconstruct historical cash.
	// The order's integer base units, linked success and paid-only immutable ledger
	// establish principal; the bonus ledger and transfer suffix are never counted.
	if ledger.UserID != order.UserID || ledger.SourceType != BalanceLedgerSourceUSDTTopUp || ledger.RequestID != order.TradeNo || ledger.Delta <= 0 || ledger.CreatedAt.IsZero() || ledger.CreatedAt.Unix() != *order.SettledAt {
		return false, nil
	}
	end, err := referralAdd(ledger.BalanceBefore, ledger.Delta)
	if err != nil || end != ledger.BalanceAfter {
		return false, nil
	}
	if order.CreditUnits <= 0 || order.CreditUnits > math.MaxInt64/1_000_000 || *order.SettledAt > math.MaxInt64-ZTAPIReferralFreezeSeconds {
		return false, ErrZTAPIReferralOverflow
	}
	principal := order.CreditUnits * 1_000_000
	identity, payload, err := referralIdentity(struct {
		Order   USDTTopUpOrder
		TopUp   TopUp
		Ledger  BalanceLedger
		Binding ZTAPIReferralBinding
	}{order, topUp, ledger, b})
	if err != nil {
		return false, err
	}
	if err = referralLockAccount(tx, b.InviterID); err != nil {
		return false, err
	}
	var existing ZTAPIReferralCommission
	err = tx.Where("order_id = ?", order.ID).First(&existing).Error
	if err == nil {
		if existing.EvidenceIdentity != identity {
			return false, ErrZTAPIReferralConflict
		}
		return false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	commission := ZTAPIReferralCommission{UserID: b.InviterID, InviteeID: b.UserID, BindingID: b.ID, OrderID: order.ID, TopUpID: order.TopUpID, BalanceLedgerID: ledger.ID, PrincipalMicros: principal, CommissionMicros: principal / 20, BlockTimestampMS: block, EarnedAt: *order.SettledAt, AvailableAt: *order.SettledAt + ZTAPIReferralFreezeSeconds, RuleVersion: b.RuleVersion, EvidenceIdentity: identity}
	if err = tx.Create(&commission).Error; err != nil {
		return false, err
	}
	key := fmt.Sprintf("commission:%d", order.ID)
	if err = referralAppend(tx, &commission, 0, "commission", commission.CommissionMicros, key, identity, commission.EarnedAt); err != nil {
		return false, err
	}
	if err = referralAudit(tx, b.InviterID, 0, commission.ID, "commission", key, identity, payload, commission.EarnedAt); err != nil {
		return false, err
	}
	return true, nil
}

type ZTAPIReferralSummary struct {
	InvitedCount              int64 `json:"invited_count"`
	TotalCommissionMicros     int64 `json:"total_commission_micros"`
	ReversedCommissionMicros  int64 `json:"reversed_commission_micros"`
	FrozenCommissionMicros    int64 `json:"frozen_commission_micros"`
	AvailableCommissionMicros int64 `json:"available_commission_micros"`
	WithdrawalReservedMicros  int64 `json:"withdrawal_reserved_micros"`
	PaidCommissionMicros      int64 `json:"paid_commission_micros"`
	ManualLossMicros          int64 `json:"manual_loss_micros"`
}

// Spendability is evaluated per source commission. A paid/refunded source never
// becomes negative and therefore cannot charge unrelated or future earnings.
func referralCommissionFree(tx *gorm.DB, c ZTAPIReferralCommission) (int64, error) {
	if c.PrincipalMicros <= 0 || c.CommissionMicros != c.PrincipalMicros/20 || c.ReversedMicros < 0 || c.ReversedMicros > c.CommissionMicros || c.ManualLossMicros < 0 || c.ManualLossMicros > c.ReversedMicros {
		return 0, ErrZTAPIReferralConflict
	}
	var allocations []ZTAPIReferralAllocation
	if err := tx.Where("commission_id = ? AND state IN ?", c.ID, []string{"reserved", "paid"}).Find(&allocations).Error; err != nil {
		return 0, err
	}
	remaining := c.CommissionMicros - c.ReversedMicros
	for _, a := range allocations {
		if a.AmountMicros <= 0 || a.InvalidatedMicros < 0 || a.InvalidatedMicros > a.AmountMicros {
			return 0, ErrZTAPIReferralConflict
		}
		remaining -= a.AmountMicros - a.InvalidatedMicros
		if remaining < 0 {
			return 0, ErrZTAPIReferralConflict
		}
	}
	return remaining, nil
}

func GetZTAPIReferralSummary(ctx context.Context, userID int, at time.Time) (ZTAPIReferralSummary, error) {
	s := ZTAPIReferralSummary{}
	if userID < 0 || referralNow(at) != nil {
		return s, ErrZTAPIReferralInvalid
	}
	err := referralTransaction(ctx, func(tx *gorm.DB) error {
		// A write lock gives the three reads one serialized wallet snapshot even
		// on MySQL's READ COMMITTED deployments. userID=0 is administrator aggregate.
		if userID > 0 {
			if err := referralLockAccount(tx, userID); err != nil {
				return err
			}
		}
		bq := tx.Model(&ZTAPIReferralBinding{})
		cq := tx.Model(&ZTAPIReferralCommission{})
		wq := tx.Model(&ZTAPIReferralWithdrawal{})
		if userID > 0 {
			bq = bq.Where("inviter_id = ?", userID)
			cq = cq.Where("user_id = ?", userID)
			wq = wq.Where("user_id = ?", userID)
		}
		if err := bq.Count(&s.InvitedCount).Error; err != nil {
			return err
		}
		var cs []ZTAPIReferralCommission
		if err := cq.Order("id").Find(&cs).Error; err != nil {
			return err
		}
		for _, c := range cs {
			free, err := referralCommissionFree(tx, c)
			if err != nil {
				return err
			}
			for _, pair := range []struct {
				dest  *int64
				value int64
			}{{&s.TotalCommissionMicros, c.CommissionMicros}, {&s.ReversedCommissionMicros, c.ReversedMicros}, {&s.ManualLossMicros, c.ManualLossMicros}} {
				*pair.dest, err = referralAdd(*pair.dest, pair.value)
				if err != nil {
					return err
				}
			}
			if c.AvailableAt <= at.Unix() {
				s.AvailableCommissionMicros, err = referralAdd(s.AvailableCommissionMicros, free)
			} else {
				s.FrozenCommissionMicros, err = referralAdd(s.FrozenCommissionMicros, free)
			}
			if err != nil {
				return err
			}
		}
		var ws []ZTAPIReferralWithdrawal
		if err := wq.Find(&ws).Error; err != nil {
			return err
		}
		for _, w := range ws {
			var err error
			switch w.Status {
			case "paid":
				s.PaidCommissionMicros, err = referralAdd(s.PaidCommissionMicros, w.AmountMicros)
			case "requested", "fee_quoted", "confirmed", "paying", "submitted_unknown", "manual_review":
				s.WithdrawalReservedMicros, err = referralAdd(s.WithdrawalReservedMicros, w.AmountMicros)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ZTAPIReferralSummary{}, err
	}
	return s, nil
}

func referralPagination(page, size int) (int, int, error) {
	if page < 1 || size < 1 || size > 100 || page > math.MaxInt32/size {
		return 0, 0, ErrZTAPIReferralInvalid
	}
	return (page - 1) * size, size, nil
}

// userID=0 is explicitly the administrator scope. Controllers must never pass a
// client-provided user ID and must redact InviteeID using their presentation DTO.
func ListZTAPIReferralCommissions(ctx context.Context, userID, page, pageSize int) ([]ZTAPIReferralCommission, int64, error) {
	db, err := referralDB(ctx)
	if err != nil {
		return nil, 0, err
	}
	offset, size, err := referralPagination(page, pageSize)
	if err != nil || userID < 0 {
		return nil, 0, ErrZTAPIReferralInvalid
	}
	q := db.Model(&ZTAPIReferralCommission{})
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	var count int64
	if err = q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	items := []ZTAPIReferralCommission{}
	err = q.Order("id DESC").Offset(offset).Limit(size).Find(&items).Error
	return items, count, err
}

func ListZTAPIReferralWithdrawals(ctx context.Context, userID, page, pageSize int, status string) ([]ZTAPIReferralWithdrawal, int64, error) {
	db, err := referralDB(ctx)
	if err != nil {
		return nil, 0, err
	}
	offset, size, err := referralPagination(page, pageSize)
	if err != nil || userID < 0 {
		return nil, 0, ErrZTAPIReferralInvalid
	}
	q := db.Model(&ZTAPIReferralWithdrawal{})
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var count int64
	if err = q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	items := []ZTAPIReferralWithdrawal{}
	err = q.Order("id DESC").Offset(offset).Limit(size).Find(&items).Error
	return items, count, err
}
