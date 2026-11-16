package model

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BillingRefundKindWallet = "wallet"
	BillingRefundKindToken  = "token"

	BillingRefundStatusPending    = "pending"
	BillingRefundStatusApplied    = "applied"
	BillingRefundStatusCompleted  = "completed"
	BillingRefundStatusDeadLetter = "dead_letter"
	BillingRefundMaxAttempts      = 10

	BalanceLedgerSourceBillingRefund = "billing_refund"
)

var (
	ErrBillingRefundInvalid             = errors.New("invalid billing refund")
	ErrBillingRefundIdempotencyConflict = errors.New("billing refund idempotency conflict")
	ErrBillingRefundDeadLetter          = errors.New("billing refund is in dead letter state")
)

// BillingRefundPending is the durable hand-off between request cleanup and the
// background compensator. The applied state separates the financial mutation
// from cache synchronization so retries cannot credit a balance twice.
type BillingRefundPending struct {
	ID             int        `json:"id"`
	UserID         int        `json:"user_id" gorm:"not null;index:idx_billing_refund_user"`
	TokenID        int        `json:"token_id" gorm:"not null;default:0"`
	TokenKeyHash   string     `json:"-" gorm:"type:varchar(64);not null;default:''"`
	Amount         int64      `json:"amount" gorm:"type:bigint;not null"`
	Kind           string     `json:"kind" gorm:"type:varchar(32);not null;index:idx_billing_refund_status"`
	Status         string     `json:"status" gorm:"type:varchar(16);not null;index:idx_billing_refund_status"`
	IdempotencyKey string     `json:"idempotency_key" gorm:"type:varchar(128);not null;uniqueIndex:idx_billing_refund_idempotency"`
	RequestID      string     `json:"request_id" gorm:"type:varchar(128);not null;default:''"`
	Attempts       int        `json:"attempts" gorm:"not null;default:0"`
	LastError      string     `json:"last_error" gorm:"type:text;not null"`
	CreatedAt      time.Time  `json:"created_at" gorm:"not null"`
	UpdatedAt      time.Time  `json:"updated_at" gorm:"not null"`
	CompletedAt    *time.Time `json:"completed_at"`
}

func EnsureBillingRefundPending(candidate BillingRefundPending) (*BillingRefundPending, error) {
	candidate.IdempotencyKey = strings.TrimSpace(candidate.IdempotencyKey)
	candidate.RequestID = strings.TrimSpace(candidate.RequestID)
	if candidate.IdempotencyKey == "" || candidate.RequestID == "" ||
		len(candidate.IdempotencyKey) > BalanceLedgerIdempotencyKeyMaxLength ||
		candidate.UserID <= 0 || candidate.Amount <= 0 || candidate.Amount > math.MaxInt32 ||
		(candidate.Kind != BillingRefundKindWallet && candidate.Kind != BillingRefundKindToken) ||
		(candidate.Kind == BillingRefundKindToken && (candidate.TokenID <= 0 || candidate.TokenKeyHash == "")) {
		return nil, ErrBillingRefundInvalid
	}

	now := time.Now().UTC()
	candidate.Status = BillingRefundStatusPending
	candidate.CreatedAt = now
	candidate.UpdatedAt = now
	var stored BillingRefundPending
	result := DB.Where("idempotency_key = ?", candidate.IdempotencyKey).
		Attrs(candidate).
		FirstOrCreate(&stored)
	if result.Error != nil {
		return nil, result.Error
	}
	if stored.UserID != candidate.UserID || stored.TokenID != candidate.TokenID ||
		stored.TokenKeyHash != candidate.TokenKeyHash || stored.Amount != candidate.Amount ||
		stored.Kind != candidate.Kind || stored.RequestID != candidate.RequestID {
		return nil, ErrBillingRefundIdempotencyConflict
	}
	return &stored, nil
}

func ProcessBillingRefundPending(id int) error {
	if id <= 0 {
		return ErrBillingRefundInvalid
	}

	var pending BillingRefundPending
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&pending, id).Error; err != nil {
			return err
		}
		if pending.Status == BillingRefundStatusCompleted || pending.Status == BillingRefundStatusApplied {
			return nil
		}
		if pending.Status == BillingRefundStatusDeadLetter {
			return ErrBillingRefundDeadLetter
		}
		if pending.Status != BillingRefundStatusPending {
			return fmt.Errorf("%w: unsupported status %q", ErrBillingRefundInvalid, pending.Status)
		}

		switch pending.Kind {
		case BillingRefundKindWallet:
			if err := applyWalletBillingRefund(tx, &pending); err != nil {
				return err
			}
		case BillingRefundKindToken:
			if err := applyTokenBillingRefund(tx, &pending); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unsupported kind %q", ErrBillingRefundInvalid, pending.Kind)
		}

		return tx.Model(&BillingRefundPending{}).
			Where("id = ? AND status = ?", pending.ID, BillingRefundStatusPending).
			Updates(map[string]any{
				"status":     BillingRefundStatusApplied,
				"last_error": "",
				"updated_at": time.Now().UTC(),
			}).Error
	})
	if err != nil {
		if !errors.Is(err, ErrBillingRefundDeadLetter) {
			recordBillingRefundFailure(id, err)
		}
		return err
	}

	if pending.Status == BillingRefundStatusCompleted {
		return nil
	}
	if err := syncBillingRefundCache(pending); err != nil {
		recordBillingRefundFailure(id, err)
		return err
	}

	now := time.Now().UTC()
	result := DB.Model(&BillingRefundPending{}).
		Where("id = ? AND status = ?", id, BillingRefundStatusApplied).
		Updates(map[string]any{
			"status":       BillingRefundStatusCompleted,
			"last_error":   "",
			"updated_at":   now,
			"completed_at": &now,
		})
	return result.Error
}

func applyWalletBillingRefund(tx *gorm.DB, pending *BillingRefundPending) error {
	var user User
	query := tx.Where("id = ?", pending.UserID)
	if tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&user).Error; err != nil {
		return err
	}
	before := int64(user.Quota)
	after, err := checkedBalanceLedgerSum(before, pending.Amount)
	if err != nil {
		return err
	}
	if err := tx.Model(&User{}).Where("id = ?", user.Id).Updates(balanceLedgerUserUpdates(user, after, false)).Error; err != nil {
		return err
	}
	return tx.Create(&BalanceLedger{
		UserID:         user.Id,
		OperatorID:     0,
		Delta:          pending.Amount,
		BalanceBefore:  before,
		BalanceAfter:   after,
		Reason:         "Automatic refund for failed relay request",
		IdempotencyKey: pending.IdempotencyKey,
		RequestID:      pending.RequestID,
		SourceType:     BalanceLedgerSourceBillingRefund,
		CreatedAt:      time.Now().UTC(),
	}).Error
}

func applyTokenBillingRefund(tx *gorm.DB, pending *BillingRefundPending) error {
	result := tx.Model(&Token{}).
		Where("id = ? AND key_hash = ? AND used_quota >= ?", pending.TokenID, pending.TokenKeyHash, pending.Amount).
		Updates(map[string]any{
			"remain_quota":  gorm.Expr("remain_quota + ?", pending.Amount),
			"used_quota":    gorm.Expr("used_quota - ?", pending.Amount),
			"accessed_time": common.GetTimestamp(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("refund token %d: %w", pending.TokenID, ErrBillingRefundInvalid)
	}
	return nil
}

func syncBillingRefundCache(pending BillingRefundPending) error {
	if !common.RedisEnabled {
		return nil
	}
	switch pending.Kind {
	case BillingRefundKindWallet:
		return InvalidateUserCache(pending.UserID)
	case BillingRefundKindToken:
		return cacheDeleteToken(pending.TokenKeyHash)
	default:
		return nil
	}
}

func recordBillingRefundFailure(id int, refundErr error) {
	if refundErr == nil {
		return
	}
	_ = DB.Transaction(func(tx *gorm.DB) error {
		var pending BillingRefundPending
		query := tx.Where("id = ?", id)
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&pending).Error; err != nil {
			return err
		}
		if (pending.Status != BillingRefundStatusPending && pending.Status != BillingRefundStatusApplied) ||
			pending.Attempts >= BillingRefundMaxAttempts {
			return nil
		}
		nextAttempts := pending.Attempts + 1
		updates := map[string]any{
			"attempts":   nextAttempts,
			"last_error": refundErr.Error(),
			"updated_at": time.Now().UTC(),
		}
		if nextAttempts >= BillingRefundMaxAttempts {
			updates["status"] = BillingRefundStatusDeadLetter
		}
		result := tx.Model(&BillingRefundPending{}).
			Where("id = ? AND status = ? AND attempts = ?", pending.ID, pending.Status, pending.Attempts).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrInvalidTransaction
		}
		return nil
	})
}

func ProcessPendingBillingRefunds(limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	var pending []BillingRefundPending
	if err := DB.Where("status IN ?", []string{BillingRefundStatusPending, BillingRefundStatusApplied}).
		Order("id ASC").
		Limit(limit).
		Find(&pending).Error; err != nil {
		return 0, err
	}

	processed := 0
	var firstErr error
	for _, refund := range pending {
		if err := ProcessBillingRefundPending(refund.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		processed++
	}
	return processed, firstErr
}

func CountPendingBillingRefunds() (int64, error) {
	var count int64
	err := DB.Model(&BillingRefundPending{}).
		Where("status IN ?", []string{BillingRefundStatusPending, BillingRefundStatusApplied}).
		Count(&count).Error
	return count, err
}

func CountDeadLetterBillingRefunds() (int64, error) {
	var count int64
	err := DB.Model(&BillingRefundPending{}).
		Where("status = ?", BillingRefundStatusDeadLetter).
		Count(&count).Error
	return count, err
}
