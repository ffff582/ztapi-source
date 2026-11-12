package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
)

type ZTAPICNYReferralCursor struct {
	Paid, Refunded int64
}

// CNY accounting is retried independently and never runs in a payment callback.
func ReconcileZTAPICNYReferralSettlements(ctx context.Context, policy *ZTAPICNYReferralPolicy, cursor ZTAPICNYReferralCursor, limit int, at time.Time) (ZTAPICNYReferralCursor, error) {
	db, err := referralDB(ctx)
	if err != nil || limit < 1 || limit > 1000 || cursor.Paid < 0 || cursor.Refunded < 0 || referralNow(at) != nil {
		return cursor, ErrZTAPIReferralInvalid
	}
	var refunded []CNYTopUpOrder
	if err = db.Table("cny_top_up_orders AS o").Select("o.*").Joins("JOIN ztapi_referral_commissions AS r ON r.cny_order_id = o.id").
		Where("o.id > ? AND o.state = ? AND o.refunded_at > 0 AND o.refunded_at <= ? AND r.reversed_micros < r.commission_micros", cursor.Refunded, CNYTopUpRefunded, at.Unix()).Order("o.id").Limit(limit).Find(&refunded).Error; err != nil {
		return cursor, err
	}
	if len(refunded) < limit {
		cursor.Refunded = 0
	}
	for _, row := range refunded {
		if len(refunded) == limit {
			cursor.Refunded = row.ID
		}
		if reversalErr := reverseVerifiedCNYReferral(ctx, db, row, at); reversalErr != nil && err == nil {
			err = reversalErr
		}
	}
	if policy == nil || policy.EnabledAt <= 0 || at.Unix() < policy.EnabledAt {
		return cursor, err
	}
	var paid []CNYTopUpOrder
	if readErr := db.Table("cny_top_up_orders AS o").Select("o.*").Joins("JOIN ztapi_referral_bindings AS b ON b.user_id = o.user_id").
		Where("o.id > ? AND o.referral_policy_epoch > 0 AND o.created_at >= o.referral_policy_epoch AND o.state = ? AND o.paid_at > 0 AND o.paid_at <= ?", cursor.Paid, CNYTopUpPaid, at.Unix()).
		Where("NOT EXISTS (SELECT 1 FROM ztapi_referral_commissions AS r WHERE r.cny_order_id = o.id)").Order("o.id").Limit(limit).Find(&paid).Error; readErr != nil {
		return cursor, readErr
	}
	if len(paid) < limit {
		cursor.Paid = 0
	}
	for _, row := range paid {
		if len(paid) == limit {
			cursor.Paid = row.ID
		}
		if creditErr := creditVerifiedCNYReferral(ctx, db, row, *policy, at); creditErr != nil && err == nil {
			err = creditErr
		}
	}
	return cursor, err
}

func creditVerifiedCNYReferral(ctx context.Context, db *gorm.DB, candidate CNYTopUpOrder, policy ZTAPICNYReferralPolicy, at time.Time) error {
	var binding ZTAPIReferralBinding
	if err := db.Where("user_id = ?", candidate.UserID).First(&binding).Error; err != nil {
		return err
	}
	return referralTransaction(ctx, func(tx *gorm.DB) error {
		if err := referralLockAccount(tx, binding.InviterID); err != nil {
			return err
		}
		var order CNYTopUpOrder
		if err := cnyLock(tx).First(&order, candidate.ID).Error; err != nil {
			return err
		}
		_, knownMethod := policy.FeeBPS[order.Method]
		if order.State != CNYTopUpPaid || order.RefundNoticeAt != 0 || order.RefundHeldAt != 0 || order.RefundedAt != 0 || !knownMethod || (order.Method != "alipay" && order.Method != "wxpay") || order.MerchantNo != policy.MerchantNo || order.AppID != policy.AppID ||
			order.UserID != binding.UserID || order.UserID == binding.InviterID || order.ReferralPolicyEpoch <= 0 || order.CreatedAt < order.ReferralPolicyEpoch || order.CreatedAt < binding.BoundAt || order.CreatedAt < binding.CampaignStartedAt ||
			order.PaidAt < order.CreatedAt || order.PaidAt >= binding.ExpiresAt || order.PaidAt > at.Unix() || order.PaidAt > math.MaxInt64/1000 || order.ProviderTradeNo == nil || !ValidCNYPaymentID(*order.ProviderTradeNo) {
			return nil
		}
		var existing ZTAPIReferralCommission
		if err := tx.Where("cny_order_id = ?", order.ID).First(&existing).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		ledger, err := checkedCNYReferralLedger(tx, order, CNYCreditSource, order.Quota, order.PaidAt, 0)
		if err != nil {
			return err
		}
		principal, err := ztapiCNYReferralPrincipal(order, policy)
		if err != nil {
			return err
		}
		identity, payload, err := referralIdentity(struct {
			OrderID, BindingID, Principal, PaidAt int64
			LedgerID                              int
			Method, OrderNo, ProviderTrade        string
		}{order.ID, binding.ID, principal, order.PaidAt, ledger.ID, order.Method, order.OrderNo, *order.ProviderTradeNo})
		if err != nil {
			return err
		}
		c := ZTAPIReferralCommission{UserID: binding.InviterID, InviteeID: binding.UserID, BindingID: binding.ID, CNYOrderID: &order.ID, CNYMethod: order.Method,
			BalanceLedgerID: ledger.ID, PrincipalMicros: principal, CommissionMicros: principal / 20, BlockTimestampMS: order.PaidAt * 1000, EarnedAt: order.PaidAt,
			AvailableAt: order.PaidAt + ZTAPIReferralFreezeSeconds, RuleVersion: ZTAPICNYReferralRuleVersion, EvidenceIdentity: identity}
		if err = tx.Create(&c).Error; err != nil {
			return err
		}
		key := fmt.Sprintf("cny-commission:%d", order.ID)
		if err = referralAppend(tx, &c, 0, "commission", c.CommissionMicros, key, identity, c.EarnedAt); err != nil {
			return err
		}
		return referralAudit(tx, binding.InviterID, 0, c.ID, "commission", key, identity, payload, c.EarnedAt)
	})
}

func checkedCNYReferralLedger(db *gorm.DB, order CNYTopUpOrder, source string, delta int64, occurred int64, operator int) (BalanceLedger, error) {
	var row BalanceLedger
	err := db.Where("idempotency_key = ?", source+":"+order.OrderNo).First(&row).Error
	if err != nil {
		return row, err
	}
	after, err := referralAdd(row.BalanceBefore, row.Delta)
	operatorMatches := (operator >= 0 && row.OperatorID == operator) || (operator < 0 && row.OperatorID > 0)
	if err != nil || order.Quota <= 0 || row.RequestID != order.OrderNo || row.SourceType != source || row.UserID != order.UserID || !operatorMatches || row.Delta != delta || row.CreatedAt.Unix() != occurred || after != row.BalanceAfter {
		return row, ErrZTAPIReferralConflict
	}
	return row, nil
}

func reverseVerifiedCNYReferral(ctx context.Context, db *gorm.DB, order CNYTopUpOrder, at time.Time) error {
	if order.State != CNYTopUpRefunded || order.ProviderRefund == nil || !ValidCNYPaymentID(*order.ProviderRefund) || order.ProviderTradeNo == nil || !ValidCNYPaymentID(*order.ProviderTradeNo) || order.RefundHeldAt < order.PaidAt || order.RefundedAt < order.RefundHeldAt || order.RefundedAt > at.Unix() || order.RefundOperator <= 0 || order.Quota <= 0 {
		return ErrZTAPIReferralConflict
	}
	if _, err := checkedCNYReferralLedger(db, order, CNYRefundSource, -order.Quota, order.RefundHeldAt, -1); err != nil {
		return err
	}
	var c ZTAPIReferralCommission
	if err := db.Where("cny_order_id = ?", order.ID).First(&c).Error; err != nil {
		return err
	}
	if c.InviteeID != order.UserID || c.CNYMethod != order.Method || c.RuleVersion != ZTAPICNYReferralRuleVersion {
		return ErrZTAPIReferralConflict
	}
	_, err := reverseZTAPIReferralRechargePrincipal(ctx, ZTAPIReferralRefundInput{OrderID: order.ID, CumulativePrincipalMicros: c.PrincipalMicros,
		OperatorID: order.RefundOperator, IdempotencyKey: fmt.Sprintf("cny-refund-%d", order.ID), EvidenceReference: "blackcat:" + *order.ProviderRefund,
		Reason: "verified full CNY refund", At: at}, true)
	return err
}

func referralCNYCommissionPayable(tx *gorm.DB, c ZTAPIReferralCommission, lock bool) (bool, error) {
	if c.CNYOrderID == nil {
		return c.RuleVersion != ZTAPICNYReferralRuleVersion && c.CNYMethod == "", nil
	}
	if c.RuleVersion != ZTAPICNYReferralRuleVersion || c.OrderID != 0 || c.TopUpID != 0 || *c.CNYOrderID <= 0 || (c.CNYMethod != "alipay" && c.CNYMethod != "wxpay") {
		return false, nil
	}
	var order CNYTopUpOrder
	if lock {
		tx = cnyLock(tx)
	}
	err := tx.Select("id", "state", "refund_notice_at", "refund_held_at", "refunded_at", "method", "user_id").First(&order, *c.CNYOrderID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil && order.State == CNYTopUpPaid && order.RefundNoticeAt == 0 && order.RefundHeldAt == 0 && order.RefundedAt == 0 && order.Method == c.CNYMethod && order.UserID == c.InviteeID, err
}
