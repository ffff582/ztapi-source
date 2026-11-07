package model

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type ZTAPIReferralWithdrawalRequest struct {
	UserID                  int
	AmountMicros            int64
	Address, IdempotencyKey string
	Now                     time.Time
}

type ZTAPIReferralWithdrawalAction struct {
	WithdrawalID, ExpectedVersion int64
	UserID, OperatorID            int
	FeeMicros                     int64
	IdempotencyKey, TxID, Note    string
	Now                           time.Time
}

// Trusted internal evidence from the read-only SolidityNode verifier, never a
// client-bound DTO. No model function accepts a hash as proof of payment.
type ZTAPIReferralPaymentEvidence struct {
	TxID                                        string
	LogIndex                                    int
	Network, ContractAddress, From, To          string
	AmountMicros, BlockTimestampMS, BlockNumber int64
	ReceiptSHA256                               string
}

type ZTAPIReferralTransferEvidence = ZTAPIReferralPaymentEvidence

type ZTAPIReferralPaymentConfirmation struct {
	WithdrawalID, ExpectedVersion int64
	OperatorID                    int
	IdempotencyKey                string
	Evidence                      ZTAPIReferralPaymentEvidence
	Now                           time.Time
}

func RequestZTAPIReferralWithdrawal(ctx context.Context, in ZTAPIReferralWithdrawalRequest) (*ZTAPIReferralWithdrawal, error) {
	if in.UserID <= 0 || in.AmountMicros < ZTAPIReferralMinimumMicros || !referralValidKey(in.IdempotencyKey) || referralNow(in.Now) != nil || strings.TrimSpace(in.Address) != in.Address || ValidateTronBase58Address(in.Address) != nil {
		return nil, ErrZTAPIReferralInvalid
	}
	identity, payload, err := referralIdentity(struct {
		UserID       int
		AmountMicros int64
		Address      string
	}{in.UserID, in.AmountMicros, in.Address})
	if err != nil {
		return nil, err
	}
	requestKey := referralHash(fmt.Sprintf("%d:%s", in.UserID, in.IdempotencyKey))
	key := "request:" + requestKey
	var result *ZTAPIReferralWithdrawal
	err = referralTransaction(ctx, func(tx *gorm.DB) error {
		if err := referralLockAccount(tx, in.UserID); err != nil {
			return err
		}
		a, err := referralAuditReplay(tx, key, identity)
		if err != nil {
			return err
		}
		if a != nil {
			result, err = referralLoadWithdrawal(tx, a.RelatedID)
			return err
		}
		var user User
		if err = tx.Select("id", "status").First(&user, in.UserID).Error; err != nil {
			return ErrZTAPIReferralNotFound
		}
		if user.Status != common.UserStatusEnabled {
			return ErrZTAPIReferralInvalid
		}
		var commissions []ZTAPIReferralCommission
		if err = tx.Where("user_id = ? AND available_at <= ?", in.UserID, in.Now.Unix()).Order("available_at ASC, id ASC").Find(&commissions).Error; err != nil {
			return err
		}
		remaining := in.AmountMicros
		allocations := []ZTAPIReferralAllocation{}
		for _, c := range commissions {
			free, err := referralCommissionFree(tx, c)
			if err != nil {
				return err
			}
			if free > remaining {
				free = remaining
			}
			if free > 0 {
				allocations = append(allocations, ZTAPIReferralAllocation{UserID: in.UserID, CommissionID: c.ID, AmountMicros: free, State: "reserved"})
				remaining -= free
			}
			if remaining == 0 {
				break
			}
		}
		if remaining != 0 {
			return ErrZTAPIReferralInsufficient
		}
		w := ZTAPIReferralWithdrawal{Version: 1, UserID: in.UserID, AmountMicros: in.AmountMicros, Address: in.Address, Status: "requested", CreatedAt: in.Now.Unix(), UpdatedAt: in.Now.Unix(), IdempotencyKey: requestKey, RequestIdentity: identity}
		if err = tx.Create(&w).Error; err != nil {
			return err
		}
		for i := range allocations {
			allocations[i].WithdrawalID = w.ID
		}
		if err = tx.Create(&allocations).Error; err != nil {
			return err
		}
		if err = referralAudit(tx, in.UserID, 0, w.ID, "request", key, identity, payload, in.Now.Unix()); err != nil {
			return err
		}
		result = &w
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func referralPrepayment(w *ZTAPIReferralWithdrawal) bool {
	return w.HolderID == 0 && (w.Status == "requested" || w.Status == "fee_quoted" || w.Status == "confirmed" || w.Status == "manual_review")
}

func referralAllocationCheck(tx *gorm.DB, w *ZTAPIReferralWithdrawal, allowInvalid bool) ([]ZTAPIReferralAllocation, error) {
	var allocations []ZTAPIReferralAllocation
	if err := tx.Where("withdrawal_id = ? AND state = ?", w.ID, "reserved").Order("id").Find(&allocations).Error; err != nil {
		return nil, err
	}
	var total int64
	for _, a := range allocations {
		if a.UserID != w.UserID || a.AmountMicros <= 0 || a.InvalidatedMicros < 0 || a.InvalidatedMicros > a.AmountMicros || (!allowInvalid && a.InvalidatedMicros != 0) {
			return nil, ErrZTAPIReferralConflict
		}
		var c ZTAPIReferralCommission
		if err := tx.First(&c, a.CommissionID).Error; err != nil {
			return nil, err
		}
		if c.UserID != w.UserID || c.AvailableAt > w.CreatedAt {
			return nil, ErrZTAPIReferralConflict
		}
		if _, err := referralCommissionFree(tx, c); err != nil {
			return nil, err
		}
		var err error
		total, err = referralAdd(total, a.AmountMicros)
		if err != nil {
			return nil, err
		}
	}
	if total != w.AmountMicros {
		return nil, ErrZTAPIReferralConflict
	}
	return allocations, nil
}

func referralWithdrawalAction(ctx context.Context, action string, in ZTAPIReferralWithdrawalAction) (*ZTAPIReferralWithdrawal, error) {
	if in.WithdrawalID <= 0 || in.ExpectedVersion <= 0 || referralNow(in.Now) != nil || len(in.Note) > 500 {
		return nil, ErrZTAPIReferralInvalid
	}
	userAction := action == "confirm" || action == "cancel"
	actor := in.OperatorID
	if userAction {
		actor = in.UserID
	}
	if actor <= 0 {
		return nil, ErrZTAPIReferralInvalid
	}
	key, err := referralKey(action, in.WithdrawalID, in.ExpectedVersion, actor, in.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	stable := in
	stable.Now = time.Time{}
	stable.IdempotencyKey = ""
	identity, payload, err := referralIdentity(struct {
		Action string
		Input  ZTAPIReferralWithdrawalAction
	}{action, stable})
	if err != nil {
		return nil, err
	}
	owner, err := referralWithdrawalOwner(ctx, in.WithdrawalID)
	if err != nil {
		return nil, err
	}
	var result *ZTAPIReferralWithdrawal
	err = referralTransaction(ctx, func(tx *gorm.DB) error {
		if err := referralLockAccount(tx, owner); err != nil {
			return err
		}
		w, err := referralLoadWithdrawal(tx, in.WithdrawalID)
		if err != nil {
			return err
		}
		if w.UserID != owner {
			return ErrZTAPIReferralConflict
		}
		if userAction && w.UserID != in.UserID {
			return ErrZTAPIReferralNotFound
		}
		a, err := referralAuditReplay(tx, key, identity)
		if err != nil {
			return err
		}
		if a != nil {
			if a.RelatedID != w.ID {
				return ErrZTAPIReferralConflict
			}
			result = w
			return nil
		}
		if w.Version != in.ExpectedVersion || in.Now.Unix() < w.UpdatedAt {
			return ErrZTAPIReferralConflict
		}
		switch action {
		case "quote":
			if !referralPrepayment(w) || w.Status == "manual_review" || in.FeeMicros < 0 || in.FeeMicros >= w.AmountMicros || (in.FeeMicros == 0 && strings.TrimSpace(in.Note) == "") {
				return ErrZTAPIReferralConflict
			}
			if _, err = referralAllocationCheck(tx, w, false); err != nil {
				return err
			}
			w.FeeMicros = in.FeeMicros
			w.NetAmountMicros = w.AmountMicros - in.FeeMicros
			w.FeeExpiresAt = in.Now.Unix() + 1800
			w.Status = "fee_quoted"
			w.Note = in.Note
		case "confirm":
			if w.Status != "fee_quoted" || w.HolderID != 0 || in.Now.Unix() >= w.FeeExpiresAt {
				return ErrZTAPIReferralConflict
			}
			if _, err = referralAllocationCheck(tx, w, false); err != nil {
				return err
			}
			w.Status = "confirmed"
		case "claim":
			if w.Status != "confirmed" || w.HolderID != 0 || in.Now.Unix() >= w.FeeExpiresAt {
				return ErrZTAPIReferralConflict
			}
			if _, err = referralAllocationCheck(tx, w, false); err != nil {
				return err
			}
			w.HolderID = in.OperatorID
			w.Status = "paying"
		case "submitted":
			if (w.Status != "paying" && w.Status != "submitted_unknown") || w.HolderID != in.OperatorID || !referralHex64(in.TxID) {
				return ErrZTAPIReferralConflict
			}
			txID := strings.ToLower(in.TxID)
			if w.TxID != "" && w.TxID != txID {
				return ErrZTAPIReferralConflict
			}
			w.TxID = txID
			w.Status = "submitted_unknown"
			w.Note = in.Note
		case "cancel", "reject":
			if !referralPrepayment(w) {
				return ErrZTAPIReferralConflict
			}
			if action == "reject" && strings.TrimSpace(in.Note) == "" {
				return ErrZTAPIReferralInvalid
			}
			if _, err = referralAllocationCheck(tx, w, true); err != nil {
				return err
			}
			if err = tx.Model(&ZTAPIReferralAllocation{}).Where("withdrawal_id = ? AND state = ?", w.ID, "reserved").Update("state", "released").Error; err != nil {
				return err
			}
			if action == "cancel" {
				w.Status = "cancelled"
			} else {
				w.Status = "rejected"
			}
			w.Note = in.Note
		default:
			return ErrZTAPIReferralInvalid
		}
		if err = referralSaveWithdrawal(tx, w, in.ExpectedVersion, in.Now); err != nil {
			return err
		}
		if err = referralAudit(tx, w.UserID, in.OperatorID, w.ID, action, key, identity, payload, in.Now.Unix()); err != nil {
			return err
		}
		result = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func QuoteZTAPIReferralWithdrawal(ctx context.Context, in ZTAPIReferralWithdrawalAction) (*ZTAPIReferralWithdrawal, error) {
	return referralWithdrawalAction(ctx, "quote", in)
}
func ConfirmZTAPIReferralWithdrawal(ctx context.Context, in ZTAPIReferralWithdrawalAction) (*ZTAPIReferralWithdrawal, error) {
	return referralWithdrawalAction(ctx, "confirm", in)
}
func CancelZTAPIReferralWithdrawal(ctx context.Context, in ZTAPIReferralWithdrawalAction) (*ZTAPIReferralWithdrawal, error) {
	return referralWithdrawalAction(ctx, "cancel", in)
}
func RejectZTAPIReferralWithdrawal(ctx context.Context, in ZTAPIReferralWithdrawalAction) (*ZTAPIReferralWithdrawal, error) {
	return referralWithdrawalAction(ctx, "reject", in)
}
func ClaimZTAPIReferralPaymentIntent(ctx context.Context, in ZTAPIReferralWithdrawalAction) (*ZTAPIReferralWithdrawal, error) {
	return referralWithdrawalAction(ctx, "claim", in)
}
func SubmitZTAPIReferralManualPayment(ctx context.Context, in ZTAPIReferralWithdrawalAction) (*ZTAPIReferralWithdrawal, error) {
	return referralWithdrawalAction(ctx, "submitted", in)
}

func referralHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func referralValidatePayment(w *ZTAPIReferralWithdrawal, in ZTAPIReferralPaymentConfirmation) error {
	e := in.Evidence
	if w.HolderID != in.OperatorID || (w.Status != "paying" && w.Status != "submitted_unknown") {
		return ErrZTAPIReferralConflict
	}
	if !referralHex64(e.TxID) || !referralHex64(e.ReceiptSHA256) || e.LogIndex < 0 || e.BlockNumber <= 0 || e.BlockTimestampMS <= 0 || e.BlockTimestampMS/1000 < w.CreatedAt || e.BlockTimestampMS/1000 > in.Now.Unix() {
		return ErrZTAPIReferralInvalid
	}
	if e.Network != USDTTopUpNetworkTronMainnet || e.ContractAddress != USDTTopUpContractAddress || e.To != w.Address || ValidateTronBase58Address(e.From) != nil || e.AmountMicros != w.NetAmountMicros || e.AmountMicros <= 0 || w.FeeMicros < 0 || w.FeeMicros+w.NetAmountMicros != w.AmountMicros {
		return ErrZTAPIReferralInvalid
	}
	if w.TxID != "" && w.TxID != strings.ToLower(e.TxID) {
		return ErrZTAPIReferralConflict
	}
	return nil
}

func ConfirmZTAPIReferralManualPayment(ctx context.Context, in ZTAPIReferralPaymentConfirmation) (*ZTAPIReferralWithdrawal, error) {
	if in.WithdrawalID <= 0 || in.ExpectedVersion <= 0 || in.OperatorID <= 0 || referralNow(in.Now) != nil {
		return nil, ErrZTAPIReferralInvalid
	}
	in.Evidence.TxID = strings.ToLower(in.Evidence.TxID)
	in.Evidence.ReceiptSHA256 = strings.ToLower(in.Evidence.ReceiptSHA256)
	stable := in
	stable.Now = time.Time{}
	stable.IdempotencyKey = ""
	identity, payload, err := referralIdentity(stable)
	if err != nil {
		return nil, err
	}
	key, err := referralKey("paid", in.WithdrawalID, in.ExpectedVersion, in.OperatorID, in.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	owner, err := referralWithdrawalOwner(ctx, in.WithdrawalID)
	if err != nil {
		return nil, err
	}
	var result *ZTAPIReferralWithdrawal
	err = referralTransaction(ctx, func(tx *gorm.DB) error {
		if err := referralLockAccount(tx, owner); err != nil {
			return err
		}
		w, err := referralLoadWithdrawal(tx, in.WithdrawalID)
		if err != nil {
			return err
		}
		if w.UserID != owner {
			return ErrZTAPIReferralConflict
		}
		a, err := referralAuditReplay(tx, key, identity)
		if err != nil {
			return err
		}
		if a != nil {
			if a.RelatedID != w.ID || w.HolderID != in.OperatorID {
				return ErrZTAPIReferralConflict
			}
			result = w
			return nil
		}
		if w.Version != in.ExpectedVersion || in.Now.Unix() < w.UpdatedAt {
			return ErrZTAPIReferralConflict
		}
		if err = referralValidatePayment(w, in); err != nil {
			return err
		}
		allocations, err := referralAllocationCheck(tx, w, true)
		if err != nil {
			return err
		}
		transferKey := fmt.Sprintf("%s:%d", in.Evidence.TxID, in.Evidence.LogIndex)
		var count int64
		if err = tx.Model(&ZTAPIReferralWithdrawal{}).Where("transfer_key = ?", transferKey).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrZTAPIReferralConflict
		}
		for _, a := range allocations {
			if a.InvalidatedMicros > 0 {
				var c ZTAPIReferralCommission
				if err = tx.First(&c, a.CommissionID).Error; err != nil {
					return err
				}
				c.ManualLossMicros, err = referralAdd(c.ManualLossMicros, a.InvalidatedMicros)
				if err != nil {
					return err
				}
				if c.ManualLossMicros > c.ReversedMicros {
					return ErrZTAPIReferralConflict
				}
				if err = tx.Model(&ZTAPIReferralCommission{}).Where("id = ?", c.ID).Update("manual_loss_micros", c.ManualLossMicros).Error; err != nil {
					return err
				}
				if err = referralAppend(tx, &c, w.ID, "manual_loss", a.InvalidatedMicros, fmt.Sprintf("loss:paid:%d:%d", w.ID, c.ID), identity, in.Now.Unix()); err != nil {
					return err
				}
			}
		}
		if err = tx.Model(&ZTAPIReferralAllocation{}).Where("withdrawal_id = ? AND state = ?", w.ID, "reserved").Update("state", "paid").Error; err != nil {
			return err
		}
		w.Status = "paid"
		w.PaidAt = in.Evidence.BlockTimestampMS / 1000
		w.TxID = in.Evidence.TxID
		w.TransferKey = &transferKey
		w.PaymentEvidence = payload
		if err = referralSaveWithdrawal(tx, w, in.ExpectedVersion, in.Now); err != nil {
			return err
		}
		if err = referralAudit(tx, w.UserID, in.OperatorID, w.ID, "paid", key, identity, payload, in.Now.Unix()); err != nil {
			return err
		}
		result = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
