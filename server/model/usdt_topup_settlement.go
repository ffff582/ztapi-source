package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const BalanceLedgerSourceUSDTTopUp = "usdt_topup_settlement"

var (
	ErrUSDTTopUpTransferMismatch  = errors.New("TRC-20 transfer does not match an open USDT topup")
	ErrUSDTTopUpTransferAmbiguous = errors.New("TRC-20 transfer matches multiple USDT topups")
	ErrUSDTTopUpCacheSync         = errors.New("USDT topup committed but user cache synchronization failed")
)

type TRC20Transfer struct {
	TxID             string
	From             string
	To               string
	ContractAddress  string
	AmountMicros     int64
	BlockTimestampMS int64
}

type USDTSettlementResult struct {
	Order      *USDTTopUpOrder
	TopUp      *TopUp
	Ledger     *BalanceLedger
	QuotaAdded int64
	Applied    bool
}

func SettleUSDTTopUp(transfer TRC20Transfer, settledAt time.Time) (*USDTSettlementResult, error) {
	if err := validateUSDTSettlementTransfer(transfer); err != nil {
		return nil, err
	}
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if DB.Dialector.Name() == "sqlite" {
		usdtTopUpSQLiteMu.Lock()
		balanceLedgerSQLiteMu.Lock()
		defer balanceLedgerSQLiteMu.Unlock()
		defer usdtTopUpSQLiteMu.Unlock()
	}

	settledAt = settledAt.UTC()
	var result *USDTSettlementResult
	err := DB.Transaction(func(tx *gorm.DB) error {
		existing, err := loadUSDTSettlementByTx(tx, transfer.TxID)
		if err == nil {
			if !settlementEvidenceMatches(existing.Order, transfer) {
				return ErrUSDTTopUpTransferMismatch
			}
			result = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var candidates []USDTTopUpOrder
		query := tx.Where(
			"status IN ? AND pay_amount_micros = ? AND receiving_address = ? AND contract_address = ? AND created_at * 1000 <= ? AND expires_at * 1000 >= ?",
			[]string{USDTTopUpStatusPending, USDTTopUpStatusConfirming},
			transfer.AmountMicros,
			transfer.To,
			transfer.ContractAddress,
			transfer.BlockTimestampMS,
			transfer.BlockTimestampMS,
		).Limit(2)
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Find(&candidates).Error; err != nil {
			return err
		}
		if len(candidates) == 0 {
			return ErrUSDTTopUpTransferMismatch
		}
		if len(candidates) > 1 {
			return ErrUSDTTopUpTransferAmbiguous
		}
		order := candidates[0]

		var topUp TopUp
		topUpQuery := tx.Where("id = ?", order.TopUpID)
		if tx.Dialector.Name() != "sqlite" {
			topUpQuery = topUpQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := topUpQuery.First(&topUp).Error; err != nil {
			return err
		}
		if topUp.Status != common.TopUpStatusPending || topUp.PaymentProvider != PaymentProviderUSDTTRC20 || topUp.UserId != order.UserID {
			return ErrUSDTTopUpStateConflict
		}

		var user User
		userQuery := tx.Where("id = ?", order.UserID)
		if tx.Dialector.Name() != "sqlite" {
			userQuery = userQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := userQuery.First(&user).Error; err != nil {
			return err
		}
		quotaAdded := adminTopUpQuota(&topUp)
		if quotaAdded <= 0 {
			return ErrUSDTTopUpInvalidAmount
		}
		nextBalance, err := checkedBalanceLedgerSum(int64(user.Quota), quotaAdded)
		if err != nil {
			return err
		}
		ledger := BalanceLedger{
			UserID:         user.Id,
			Delta:          quotaAdded,
			BalanceBefore:  int64(user.Quota),
			BalanceAfter:   nextBalance,
			Reason:         "confirmed USDT TRC-20 service-credit topup",
			IdempotencyKey: "usdt-trc20:" + transfer.TxID,
			RequestID:      order.TradeNo,
			SourceType:     BalanceLedgerSourceUSDTTopUp,
			CreatedAt:      settledAt,
		}
		if len(ledger.IdempotencyKey) > BalanceLedgerIdempotencyKeyMaxLength {
			return ErrBalanceLedgerIdempotencyKeyTooLong
		}
		if err := tx.Create(&ledger).Error; err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where("id = ?", user.Id).Updates(balanceLedgerUserUpdates(user, nextBalance, false)).Error; err != nil {
			return err
		}

		completeTime := settledAt.Unix()
		topUpUpdate := tx.Model(&TopUp{}).Where("id = ? AND status = ?", topUp.Id, common.TopUpStatusPending).Updates(map[string]interface{}{
			"status": common.TopUpStatusSuccess, "complete_time": completeTime,
		})
		if topUpUpdate.Error != nil {
			return topUpUpdate.Error
		}
		if topUpUpdate.RowsAffected != 1 {
			return ErrUSDTTopUpStateConflict
		}

		txID := transfer.TxID
		txFrom := transfer.From
		blockTimestamp := transfer.BlockTimestampMS
		orderUpdate := tx.Model(&USDTTopUpOrder{}).Where(
			"id = ? AND status IN ?",
			order.ID,
			[]string{USDTTopUpStatusPending, USDTTopUpStatusConfirming},
		).Updates(map[string]interface{}{
			"status":             USDTTopUpStatusSettled,
			"tx_id":              txID,
			"tx_from":            txFrom,
			"block_timestamp_ms": blockTimestamp,
			"settled_at":         completeTime,
			"updated_at":         completeTime,
		})
		if orderUpdate.Error != nil {
			return orderUpdate.Error
		}
		if orderUpdate.RowsAffected != 1 {
			return ErrUSDTTopUpStateConflict
		}

		order.Status = USDTTopUpStatusSettled
		order.TxID = &txID
		order.TxFrom = &txFrom
		order.BlockTimestampMS = &blockTimestamp
		order.SettledAt = &completeTime
		order.UpdatedAt = completeTime
		topUp.Status = common.TopUpStatusSuccess
		topUp.CompleteTime = completeTime
		result = &USDTSettlementResult{Order: &order, TopUp: &topUp, Ledger: &ledger, QuotaAdded: quotaAdded, Applied: true}
		return nil
	})
	if err != nil {
		if recovered, recoverErr := loadUSDTSettlementByTx(DB, transfer.TxID); recoverErr == nil {
			if !settlementEvidenceMatches(recovered.Order, transfer) {
				return nil, ErrUSDTTopUpTransferMismatch
			}
			result = recovered
		} else {
			return nil, err
		}
	}
	if result == nil || result.Ledger == nil {
		return nil, ErrUSDTTopUpStateConflict
	}
	if syncErr := balanceLedgerCacheSync(result.Order.UserID); syncErr != nil {
		return result, fmt.Errorf("%w: ledger %d is committed", ErrUSDTTopUpCacheSync, result.Ledger.ID)
	}
	return result, nil
}

func loadUSDTSettlementByTx(db *gorm.DB, txID string) (*USDTSettlementResult, error) {
	var order USDTTopUpOrder
	if err := db.Where("tx_id = ?", txID).First(&order).Error; err != nil {
		return nil, err
	}
	var topUp TopUp
	if err := db.First(&topUp, order.TopUpID).Error; err != nil {
		return nil, err
	}
	var ledger BalanceLedger
	if err := db.Where("idempotency_key = ?", "usdt-trc20:"+txID).First(&ledger).Error; err != nil {
		return nil, err
	}
	return &USDTSettlementResult{
		Order: &order, TopUp: &topUp, Ledger: &ledger, QuotaAdded: ledger.Delta, Applied: false,
	}, nil
}

func validateUSDTSettlementTransfer(transfer TRC20Transfer) error {
	if strings.TrimSpace(transfer.TxID) == "" || strings.TrimSpace(transfer.From) == "" || strings.TrimSpace(transfer.To) == "" || strings.TrimSpace(transfer.ContractAddress) == "" || transfer.AmountMicros <= 0 || transfer.BlockTimestampMS <= 0 {
		return ErrUSDTTopUpTransferMismatch
	}
	return nil
}

func settlementEvidenceMatches(order *USDTTopUpOrder, transfer TRC20Transfer) bool {
	if order == nil || order.TxID == nil || *order.TxID != transfer.TxID {
		return false
	}
	return order.PayAmountMicros == transfer.AmountMicros &&
		order.ReceivingAddress == transfer.To &&
		order.ContractAddress == transfer.ContractAddress &&
		order.BlockTimestampMS != nil && *order.BlockTimestampMS == transfer.BlockTimestampMS
}
