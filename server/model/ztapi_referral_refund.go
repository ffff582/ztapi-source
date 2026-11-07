package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// This internal input is accepted only after a caller verifies an actual refund
// and its cumulative paid principal. It is not an API balance/refund instruction.
type ZTAPIReferralRefundInput struct {
	OrderID, CumulativePrincipalMicros        int64
	OperatorID                                int
	IdempotencyKey, EvidenceReference, Reason string
	At                                        time.Time
}

func ReverseZTAPIReferralRechargePrincipal(ctx context.Context, in ZTAPIReferralRefundInput) (*ZTAPIReferralCommission, error) {
	if in.OrderID <= 0 || in.CumulativePrincipalMicros < 0 || in.OperatorID <= 0 || !referralValidKey(in.IdempotencyKey) || strings.TrimSpace(in.EvidenceReference) == "" || len(in.EvidenceReference) > 500 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 || referralNow(in.At) != nil {
		return nil, ErrZTAPIReferralInvalid
	}
	stable := in
	stable.At = time.Time{}
	stable.IdempotencyKey = ""
	identity, payload, err := referralIdentity(stable)
	if err != nil {
		return nil, err
	}
	key := "refund:" + referralHash(in.IdempotencyKey)
	db, err := referralDB(ctx)
	if err != nil {
		return nil, err
	}
	var route ZTAPIReferralCommission
	if err = db.Select("id", "user_id").Where("order_id = ?", in.OrderID).First(&route).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrZTAPIReferralNotFound
		}
		return nil, err
	}
	evidenceKey := "refund:" + referralHash(in.EvidenceReference)
	var result *ZTAPIReferralCommission
	err = referralTransaction(ctx, func(tx *gorm.DB) error {
		if err := referralLockAccount(tx, route.UserID); err != nil {
			return err
		}
		var c ZTAPIReferralCommission
		if err := tx.Where("order_id = ?", in.OrderID).First(&c).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrZTAPIReferralNotFound
			}
			return err
		}
		if c.UserID != route.UserID {
			return ErrZTAPIReferralConflict
		}
		a, err := referralAuditReplay(tx, key, identity)
		if err != nil {
			return err
		}
		if a != nil {
			if a.RelatedID != c.ID {
				return ErrZTAPIReferralConflict
			}
			result = &c
			return nil
		}
		var sameEvidence ZTAPIReferralAudit
		err = tx.Where("evidence_key = ?", evidenceKey).First(&sameEvidence).Error
		if err == nil {
			if sameEvidence.Identity != identity || sameEvidence.RelatedID != c.ID {
				return ErrZTAPIReferralConflict
			}
			if err = referralAudit(tx, c.UserID, in.OperatorID, c.ID, "refund_replay", key, identity, payload, in.At.Unix()); err != nil {
				return err
			}
			result = &c
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if in.CumulativePrincipalMicros < c.RefundedPrincipalMicros || in.CumulativePrincipalMicros > c.PrincipalMicros || in.At.Unix() < c.EarnedAt {
			return ErrZTAPIReferralConflict
		}
		free, err := referralCommissionFree(tx, c)
		if err != nil {
			return err
		}
		target := in.CumulativePrincipalMicros / 20
		delta := target - c.ReversedMicros
		if delta < 0 {
			return ErrZTAPIReferralConflict
		}
		// The cumulative formula, not sum(each partial / 20), preserves rounding.
		invalidate := delta - free
		if invalidate < 0 {
			invalidate = 0
		}
		var allocations []ZTAPIReferralAllocation
		if err = tx.Where("commission_id = ? AND state IN ?", c.ID, []string{"reserved", "paid"}).Order("id ASC").Find(&allocations).Error; err != nil {
			return err
		}
		withdrawals := make(map[int64]*ZTAPIReferralWithdrawal)
		for _, a := range allocations {
			w, err := referralLoadWithdrawal(tx, a.WithdrawalID)
			if err != nil {
				return err
			}
			withdrawals[w.ID] = w
		}
		priority := func(a ZTAPIReferralAllocation) int {
			if a.State == "paid" {
				return 2
			}
			if referralPrepayment(withdrawals[a.WithdrawalID]) {
				return 0
			}
			return 1
		}
		// Preserve already-discharged valid entitlement whenever an unpaid
		// allocation can absorb reversal; otherwise cancellation could re-expose
		// the same valid commission for a second withdrawal.
		sort.SliceStable(allocations, func(i, j int) bool { return priority(allocations[i]) < priority(allocations[j]) })
		for _, a := range allocations {
			if invalidate == 0 {
				break
			}
			portion := a.AmountMicros - a.InvalidatedMicros
			if portion > invalidate {
				portion = invalidate
			}
			if portion == 0 {
				continue
			}
			w := withdrawals[a.WithdrawalID]
			a.InvalidatedMicros += portion
			invalidate -= portion
			if err = tx.Model(&ZTAPIReferralAllocation{}).Where("id = ?", a.ID).Update("invalidated_micros", a.InvalidatedMicros).Error; err != nil {
				return err
			}
			if a.State == "paid" {
				if w.Status != "paid" {
					return ErrZTAPIReferralConflict
				}
				c.ManualLossMicros, err = referralAdd(c.ManualLossMicros, portion)
				if err != nil {
					return err
				}
				if err = referralAppend(tx, &c, w.ID, "manual_loss", portion, fmt.Sprintf("loss:%s:%d", key, a.ID), identity, in.At.Unix()); err != nil {
					return err
				}
			} else if referralPrepayment(w) {
				previous := w.Version
				w.Status = "manual_review"
				w.Note = "refunded commission allocation; cancellation or rejection required"
				if in.At.Unix() < w.UpdatedAt {
					return ErrZTAPIReferralConflict
				}
				if err = referralSaveWithdrawal(tx, w, previous, in.At); err != nil {
					return err
				}
			} else if w.Status != "paying" && w.Status != "submitted_unknown" {
				return ErrZTAPIReferralConflict
			}
		}
		if invalidate != 0 {
			return ErrZTAPIReferralConflict
		}
		c.RefundedPrincipalMicros = in.CumulativePrincipalMicros
		c.ReversedMicros = target
		if c.ManualLossMicros > c.ReversedMicros {
			return ErrZTAPIReferralConflict
		}
		if err = tx.Model(&ZTAPIReferralCommission{}).Where("id = ?", c.ID).Updates(map[string]any{"refunded_principal_micros": c.RefundedPrincipalMicros, "reversed_micros": c.ReversedMicros, "manual_loss_micros": c.ManualLossMicros}).Error; err != nil {
			return err
		}
		if err = referralAppend(tx, &c, 0, "refund", -delta, key, identity, in.At.Unix()); err != nil {
			return err
		}
		if err = tx.Create(&ZTAPIReferralAudit{UserID: c.UserID, OperatorID: in.OperatorID, RelatedID: c.ID, Action: "refund", IdempotencyKey: key, EvidenceKey: &evidenceKey, Identity: identity, Evidence: payload, CreatedAt: in.At.Unix()}).Error; err != nil {
			return err
		}
		result = &c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
