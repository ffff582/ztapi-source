package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ztapiYunxinCloseCase struct {
	id             uint
	lineageHash    string
	model, missing string
	hold, actual   int64
	httpStatus     int
}

// These cases are frozen from the approved preflight, not supplied by the CLI.
var ztapiYunxinCloseCases = [...]ztapiYunxinCloseCase{
	{530, "f7ed48a6ce14686a61efe04a4bd53e5896d434aeaf1ebbabaca7e8b0052c0c24", "zt-claude-haiku-4.5", ZTAPIMissingUpstreamBillingUnconfirmed, 219, 0, 502},
	{531, "73067560f3ac911086ac4806008a90c6b813810a9f3fc3b74900e2210903430e", "zt-claude-haiku-4.5", ZTAPIMissingUpstreamBillingUnconfirmed, 207, 0, 502},
	{532, "915eabc89e0f766b21a6ad8041dd446a293af50a51e3bd1274507403f04cedde", "zt-claude-haiku-4.5", ZTAPIMissingUpstreamBillingUnconfirmed, 294, 0, 502},
	{533, "88088b9c8e6510b44fdad3ca20f143bdd37a735f664a042a23804199bcdc3f13", "zt-claude-haiku-4.5", ZTAPIMissingUpstreamBillingUnconfirmed, 294, 0, 502},
	{534, "d8d80fbca83885b3beb635a3af7d912cc6ad45d14562d2c2cd5e86c26bda195b", "zt-claude-haiku-4.5", ZTAPIMissingUpstreamBillingUnconfirmed, 219, 0, 502},
	{1599, "6e37ab09f4096d163654335f2d28c0c63e4f0c9d6af48eeed80ae5d0f5ebe459", "zt-deepseek-v4-flash", `["function"]`, 143, 84, 200},
}

type ZTAPIYunxinCloseReceipt struct {
	ID       uint   `json:"id"`
	Status   string `json:"status"`
	Reserved int64  `json:"reserved_quota"`
	Charged  int64  `json:"charged_quota"`
	Returned int64  `json:"returned_quota"`
	LedgerID int    `json:"ledger_id,omitempty"`
}

// ApplyZTAPIYunxinClose records customer decisions only. It never creates
// supplier proofs and never declares the supplier's cost to be zero.
func ApplyZTAPIYunxinClose(operatorID int, apply bool, price ZTAPIAttemptBillingPricer) ([]ZTAPIYunxinCloseReceipt, error) {
	return applyZTAPIYunxinCloseCases(operatorID, apply, price, ztapiYunxinCloseCases[:])
}

func applyZTAPIYunxinCloseCases(operatorID int, apply bool, price ZTAPIAttemptBillingPricer, cases []ztapiYunxinCloseCase) ([]ZTAPIYunxinCloseReceipt, error) {
	if operatorID <= 0 || price == nil || common.QuotaPerUnit != 500000 {
		return nil, ErrZTAPISettlementInvalid
	}
	var receipts []ZTAPIYunxinCloseReceipt
	var changed []*ZTAPIRequestSettlement
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		var operator User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&operator, operatorID).Error; err != nil ||
			operator.Status != common.UserStatusEnabled || !common.HasAdminPermission(operator.Role, common.PermissionFinanceWrite) {
			return ErrZTAPISupplierRefundUnauthorized
		}
		for _, item := range cases {
			receipt, row, err := closeZTAPIYunxinCaseTx(tx, item, operatorID, apply, price)
			if err != nil {
				return fmt.Errorf("settlement %d: %w", item.id, err)
			}
			receipts = append(receipts, receipt)
			if row != nil {
				changed = append(changed, row)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, row := range changed {
		syncZTAPISettlementCaches(row)
	}
	return receipts, nil
}

func closeZTAPIYunxinCaseTx(tx *gorm.DB, item ztapiYunxinCloseCase, operatorID int, apply bool, price ZTAPIAttemptBillingPricer) (ZTAPIYunxinCloseReceipt, *ZTAPIRequestSettlement, error) {
	status, reviewStatus, source := ZTAPISettlementReleased, "platform_waived", BalanceLedgerSourceBillingRefund
	reason := "2026-11-13 Yunxin batch: platform waiver; supplier cost unconfirmed"
	if item.actual > 0 {
		status, reviewStatus, source = ZTAPISettlementSettled, "customer_manual_settled", BalanceLedgerSourceUsageSettlement
		reason = "2026-11-13 Yunxin batch: manual customer token charge; no tool fee; supplier cost unconfirmed"
	}
	receipt := ZTAPIYunxinCloseReceipt{ID: item.id, Status: status, Reserved: item.hold, Charged: item.actual, Returned: item.hold - item.actual}
	var row ZTAPIRequestSettlement
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, item.id).Error; err != nil {
		return receipt, nil, err
	}
	if row.PublicModel != item.model || row.ReservedQuota != item.hold ||
		row.RefundedQuota != 0 || !row.Dispatched {
		return receipt, nil, ErrZTAPISettlementConflict
	}
	var attempts []ZTAPIRequestAttempt
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("settlement_id = ?", row.ID).Find(&attempts).Error; err != nil {
		return receipt, nil, err
	}
	if len(attempts) != 1 || attempts[0].Attempt != 1 || attempts[0].ChannelID != 2 ||
		ztapiSupplierRefundHash(row.RequestID+"|"+attempts[0].UpstreamRequestID) != item.lineageHash ||
		attempts[0].HTTPStatus != item.httpStatus || attempts[0].SafetyOutcome {
		return receipt, nil, ErrZTAPISettlementConflict
	}
	key := fmt.Sprintf("ztapi:yunxin-manual-20261113:%d", item.id)
	var old BalanceLedger
	err := tx.Where("idempotency_key = ?", key).Take(&old).Error
	if err == nil {
		if old.UserID != row.UserID || old.RequestID != row.RequestID || old.OperatorID != operatorID ||
			old.Reason != reason || old.Delta != receipt.Returned || old.SourceType != source ||
			row.Status != status || row.ChargedQuota != item.actual || row.LastLedgerID != old.ID {
			return receipt, nil, ErrZTAPISettlementConflict
		}
		receipt.LedgerID = old.ID
		return receipt, nil, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return receipt, nil, err
	}
	if row.Status != ZTAPISettlementPending || row.MissingDimensionsJSON != item.missing ||
		row.ChargedQuota != 0 || row.TokenChargedQuota != 0 {
		return receipt, nil, ErrZTAPISettlementConflict
	}
	for _, lookup := range []*gorm.DB{
		tx.Model(&ZTAPIAttemptBillingProof{}).Where("request_id = ?", row.RequestID),
		tx.Model(&ZTAPISettlementFinalizationIntent{}).Where("request_id = ?", row.RequestID),
		tx.Model(&ZTAPISupplierRefundCharge{}).Where("request_id = ?", row.RequestID),
		tx.Model(&ZTAPIPendingResolution{}).Where("settlement_id = ?", row.ID),
		tx.Model(&ZTAPISupplierReconciliationEntry{}).Where("settlement_id = ?", row.ID),
		tx.Model(&ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID),
	} {
		var n int64
		if err = lookup.Count(&n).Error; err != nil {
			return receipt, nil, err
		}
		if n != 0 {
			return receipt, nil, ErrZTAPISettlementConflict
		}
	}
	user, token, err := ztapiSettlementOwners(tx, &row)
	if err != nil {
		return receipt, nil, err
	}
	if user.Role != common.RoleCommonUser || user.DeletedAt.Valid || token.DeletedAt.Valid ||
		(!row.TokenUnlimited && row.TokenReservedQuota != item.hold) ||
		(row.TokenUnlimited && row.TokenReservedQuota != 0) {
		return receipt, nil, ErrZTAPISettlementConflict
	}
	var reserve BalanceLedger
	if err = tx.First(&reserve, row.LastLedgerID).Error; err != nil {
		return receipt, nil, err
	}
	if reserve.IdempotencyKey != "ztapi:"+row.OperationID+":begin" || reserve.SourceType != BalanceLedgerSourceUsageReservation ||
		reserve.Delta != -item.hold || reserve.UserID != row.UserID || reserve.RequestID != row.RequestID {
		return receipt, nil, ErrZTAPISettlementConflict
	}
	var review ZTAPIAttemptBillingReview
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ? AND attempt = ?", row.RequestID, 1).Take(&review).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return receipt, nil, err
	}
	if err == nil && (review.SettlementID != row.ID || review.Status != "pending" || review.AppliedProofID != 0 || review.ChargeID != 0) {
		return receipt, nil, ErrZTAPISettlementConflict
	}
	var priced ZTAPIAttemptBillingPriced
	if item.actual > 0 {
		var u struct {
			Input  int `json:"prompt_tokens"`
			Output int `json:"completion_tokens"`
		}
		if common.UnmarshalJsonStr(row.UsageJSON, &u) != nil || u.Input != 297 || u.Output != 11 {
			return receipt, nil, ErrZTAPISettlementConflict
		}
		submission := ZTAPIAttemptBillingSubmission{RequestID: row.RequestID, UserID: row.UserID,
			Attempt: 1, ChannelID: 2, UpstreamRequestID: attempts[0].UpstreamRequestID, Kind: "billed", UsageSemantic: "openai",
			Usage: []ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 297}, {Dimension: "output_tokens", Quantity: 11}}}
		priced, err = price(row, submission)
		if err != nil {
			return receipt, nil, err
		}
		actual, err := validateZTAPIAttemptPrice(&row, submission, &priced)
		if err != nil || actual != item.actual {
			return receipt, nil, ErrZTAPISettlementConflict
		}
	}
	if !apply {
		return receipt, nil, nil
	}
	entry, err := ztapiSettlementWalletDeltaWithOperatorTx(tx, user, receipt.Returned, row.RequestID, key, reason, source, true, operatorID)
	if err != nil {
		return receipt, nil, err
	}
	if !row.TokenUnlimited {
		if _, err = ztapiSettlementTokenDeltaTx(tx, token, receipt.Returned, false); err != nil {
			return receipt, nil, err
		}
		row.TokenChargedQuota = item.actual
	}
	row.Status, row.ChargedQuota, row.LastLedgerID = status, item.actual, entry.ID
	row.FinalAttempt, row.MissingDimensionsJSON, row.CacheSyncPending = 1, "[]", true
	if item.actual > 0 {
		raw, err := common.Marshal(priced.Dimensions)
		if err != nil {
			return receipt, nil, err
		}
		row.ChargeDimensionsJSON, err = normalizeZTAPIChargeDimensions(string(raw), item.actual, row.TokenChargedQuota)
		if err != nil {
			return receipt, nil, err
		}
		if int64(user.UsedQuota)+item.actual > maxBalanceLedgerQuota {
			return receipt, nil, ErrBalanceLedgerOverflow
		}
		result := tx.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]any{
			"used_quota": gorm.Expr("used_quota + ?", item.actual), "request_count": gorm.Expr("request_count + 1"),
		})
		if result.Error != nil || result.RowsAffected != 1 {
			return receipt, nil, ErrZTAPISettlementConflict
		}
	}
	if err = tx.Save(&row).Error; err != nil {
		return receipt, nil, err
	}
	if item.actual > 0 {
		if _, err = RecordZTAPISupplierRefundChargeTx(tx, ZTAPISupplierRefundCharge{RequestID: row.RequestID, Attempt: 1,
			ChannelID: 2, CredentialVersion: attempts[0].CredentialVersion, UpstreamRequestID: attempts[0].UpstreamRequestID}); err != nil {
			return receipt, nil, err
		}
		if err = EnqueueZTAPISettlementLogTx(tx, &row, priced.ConsumeLog); err != nil {
			return receipt, nil, err
		}
	}
	if review.ID == 0 {
		review = ZTAPIAttemptBillingReview{SettlementID: row.ID, RequestID: row.RequestID, Attempt: 1}
	}
	review.Status, review.PendingReason = reviewStatus, "supplier_cost_unconfirmed"
	if err = tx.Save(&review).Error; err != nil {
		return receipt, nil, err
	}
	receipt.LedgerID = entry.ID
	return receipt, &row, nil
}
