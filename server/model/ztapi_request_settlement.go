package model

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ZTAPISettlementReserved = "reserved"
	ZTAPISettlementPending  = "pending"
	ZTAPISettlementSettled  = "settled"
	ZTAPISettlementReleased = "released"
)

var (
	ErrZTAPISettlementConflict = errors.New("request settlement identity or final charge conflicts")
	ErrZTAPISettlementPending  = errors.New("request settlement requires reconciliation")
	ErrZTAPISettlementInvalid  = errors.New("invalid request settlement")
)

// The hold and both balance mutations commit together before any provider I/O.
// Pending rows retain financial responsibility across request/process failure.
type ZTAPIRequestSettlement struct {
	ID                    uint      `gorm:"primaryKey" json:"id"`
	OperationID           string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"operation_id"`
	RequestID             string    `gorm:"type:varchar(128);not null;uniqueIndex" json:"request_id"`
	UserID                int       `gorm:"not null;index" json:"user_id"`
	TokenID               int       `gorm:"not null" json:"token_id"`
	TokenUnlimited        bool      `gorm:"not null" json:"token_unlimited"`
	PublicModel           string    `gorm:"type:varchar(200);not null" json:"model"`
	PriceSnapshotJSON     string    `gorm:"type:text;not null" json:"price_snapshot"`
	Status                string    `gorm:"type:varchar(16);not null;index" json:"status"`
	ReservedQuota         int64     `gorm:"type:bigint;not null" json:"reserved_quota"`
	InitialReservedQuota  int64     `gorm:"type:bigint;not null" json:"initial_reserved_quota"`
	TokenReservedQuota    int64     `gorm:"type:bigint;not null" json:"token_reserved_quota"`
	ChargedQuota          int64     `gorm:"type:bigint;not null" json:"charged_quota"`
	TokenChargedQuota     int64     `gorm:"type:bigint;not null" json:"token_charged_quota"`
	RefundedQuota         int64     `gorm:"type:bigint;not null" json:"refunded_quota"`
	UsageJSON             string    `gorm:"type:text;not null" json:"usage"`
	ChargeDimensionsJSON  string    `gorm:"type:text;not null" json:"charge_dimensions"`
	MissingDimensionsJSON string    `gorm:"type:text;not null" json:"missing_dimensions"`
	LastLedgerID          int       `json:"last_ledger_id"`
	FinalAttempt          int       `json:"final_attempt"`
	Dispatched            bool      `gorm:"not null" json:"dispatched"`
	CacheSyncPending      bool      `gorm:"not null;index" json:"cache_sync_pending"`
	CacheLastAttemptAt    int64     `gorm:"not null;default:0;index" json:"-"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `gorm:"index" json:"updated_at"`
}

func (ZTAPIRequestSettlement) TableName() string { return "ztapi_request_settlements" }

func ztapiSettlementTransaction(fn func(*gorm.DB) error) error {
	if DB == nil {
		return ErrZTAPISettlementInvalid
	}
	if DB.Dialector.Name() == "sqlite" {
		balanceLedgerSQLiteMu.Lock()
		defer balanceLedgerSQLiteMu.Unlock()
	}
	return DB.Transaction(fn)
}

func ztapiSettlementLock(tx *gorm.DB, op string) (*ZTAPIRequestSettlement, error) {
	var row ZTAPIRequestSettlement
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", op).Take(&row).Error
	return &row, err
}

func ztapiSettlementOwners(tx *gorm.DB, row *ZTAPIRequestSettlement) (*User, *Token, error) {
	var user User
	var token Token
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, row.UserID).Error; err != nil {
		return nil, nil, err
	}
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&token, row.TokenID).Error; err != nil {
		return nil, nil, err
	}
	if token.UserId != user.Id {
		return nil, nil, ErrZTAPISettlementConflict
	}
	return &user, &token, nil
}

func BeginZTAPIRequestSettlement(input ZTAPIRequestSettlement) (*ZTAPIRequestSettlement, error) {
	if strings.TrimSpace(input.OperationID) == "" || len(input.OperationID) > 64 || strings.TrimSpace(input.RequestID) == "" || len(input.RequestID) > 64 || input.UserID <= 0 || input.TokenID <= 0 || input.ReservedQuota < 0 || input.ReservedQuota > maxBalanceLedgerQuota || input.PublicModel == "" || len(input.PriceSnapshotJSON) > 65536 || !validZTAPISettlementJSON(input.PriceSnapshotJSON) {
		return nil, ErrZTAPISettlementInvalid
	}
	var row *ZTAPIRequestSettlement
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		// An absent-row FOR UPDATE takes a MySQL gap lock before the owner
		// lock, deadlocking concurrent first reservations at the unique insert.
		var existing ZTAPIRequestSettlement
		e := tx.Where("operation_id = ?", input.OperationID).Take(&existing).Error
		if e == nil {
			row, e = ztapiSettlementLock(tx, input.OperationID)
			if e != nil {
				return e
			}
			return ztapiSettlementMatches(*row, input)
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		row = &ZTAPIRequestSettlement{OperationID: input.OperationID, RequestID: input.RequestID, UserID: input.UserID, TokenID: input.TokenID, TokenUnlimited: input.TokenUnlimited, PublicModel: input.PublicModel, PriceSnapshotJSON: input.PriceSnapshotJSON, Status: ZTAPISettlementReserved, ReservedQuota: input.ReservedQuota, InitialReservedQuota: input.ReservedQuota, CacheSyncPending: true, UsageJSON: "{}", ChargeDimensionsJSON: "[]", MissingDimensionsJSON: "[]"}
		user, token, e := ztapiSettlementOwners(tx, row)
		if e != nil {
			return e
		}
		if user.Status != common.UserStatusEnabled || user.DeletedAt.Valid || token.DeletedAt.Valid || token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime < time.Now().Unix()) || token.UnlimitedQuota != input.TokenUnlimited {
			return ErrZTAPISettlementInvalid
		}
		if e = tx.Create(row).Error; e != nil {
			return e
		}
		entry, e := ztapiSettlementWalletDeltaTx(tx, user, -row.ReservedQuota, row.RequestID, "ztapi:"+row.OperationID+":begin", "managed request reservation", BalanceLedgerSourceUsageReservation, false)
		if e != nil {
			return e
		}
		if entry != nil {
			row.LastLedgerID = entry.ID
		}
		if !row.TokenUnlimited {
			d, e := ztapiSettlementTokenDeltaTx(tx, token, -row.ReservedQuota, false)
			if e != nil {
				return e
			}
			row.TokenReservedQuota = -d
		}
		return tx.Save(row).Error
	})
	if err != nil {
		var existing ZTAPIRequestSettlement
		if lookupErr := DB.Where("operation_id = ?", input.OperationID).Take(&existing).Error; lookupErr != nil {
			return nil, err
		}
		if matchErr := ztapiSettlementMatches(existing, input); matchErr != nil {
			return nil, matchErr
		}
		row = &existing
	}
	syncZTAPISettlementCaches(row)
	return row, nil
}

func ztapiSettlementMatches(row, input ZTAPIRequestSettlement) error {
	if row.RequestID != input.RequestID || row.UserID != input.UserID || row.TokenID != input.TokenID || row.TokenUnlimited != input.TokenUnlimited || row.PublicModel != input.PublicModel || row.PriceSnapshotJSON != input.PriceSnapshotJSON || row.InitialReservedQuota != input.ReservedQuota {
		return ErrZTAPISettlementConflict
	}
	return nil
}

func ReserveZTAPIRequestSettlement(op string, target int64) (*ZTAPIRequestSettlement, error) {
	if target < 0 || target > maxBalanceLedgerQuota {
		return nil, ErrZTAPISettlementInvalid
	}
	return mutateZTAPISettlement(op, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		if row.Status != ZTAPISettlementReserved {
			return ErrZTAPISettlementPending
		}
		if target <= row.ReservedQuota {
			return nil
		}
		user, token, err := ztapiSettlementOwners(tx, row)
		if err != nil {
			return err
		}
		delta := target - row.ReservedQuota
		entry, err := ztapiSettlementWalletDeltaTx(tx, user, -delta, row.RequestID, fmt.Sprintf("ztapi:%s:reserve:%d", op, target), "managed request additional reservation", BalanceLedgerSourceUsageReservation, false)
		if err != nil {
			return err
		}
		if !row.TokenUnlimited {
			d, e := ztapiSettlementTokenDeltaTx(tx, token, -delta, false)
			if e != nil {
				return e
			}
			row.TokenReservedQuota -= d
		}
		row.ReservedQuota = target
		row.LastLedgerID = entry.ID
		return nil
	})
}

func MarkZTAPIRequestDispatched(op string) error {
	_, err := mutateZTAPISettlement(op, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		if row.Status != ZTAPISettlementReserved {
			return ErrZTAPISettlementPending
		}
		row.Dispatched = true
		return nil
	})
	return err
}

func PendZTAPIRequestSettlement(op, usage, missing string) (*ZTAPIRequestSettlement, error) {
	if len(usage) > 65536 || len(missing) > 8192 || !validZTAPISettlementJSON(usage) || !validZTAPISettlementJSON(missing) {
		return nil, ErrZTAPISettlementInvalid
	}
	return mutateZTAPISettlement(op, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		// Deferred HTTP cleanup has no new usage evidence. It must not erase
		// an already durable finalization after a failed debit/pending handoff.
		// Actual missing-dimension or conflicting-evidence holds remain blocked.
		if missing == `["upstream_billing_unconfirmed"]` && (row.Status == ZTAPISettlementReserved ||
			(row.Status == ZTAPISettlementPending && row.MissingDimensionsJSON == `["settlement_retry_required"]`)) {
			intent, _, err := loadZTAPISettlementFinalizationIntentTx(tx, row)
			if err == nil {
				usage, missing = intent.UsageJSON, `["settlement_retry_required"]`
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if row.Status == ZTAPISettlementPending {
			if row.UsageJSON != usage || row.MissingDimensionsJSON != missing {
				return ErrZTAPISettlementConflict
			}
			return nil
		}
		if row.Status != ZTAPISettlementReserved {
			return ErrZTAPISettlementConflict
		}
		row.Status = ZTAPISettlementPending
		row.UsageJSON = usage
		row.MissingDimensionsJSON = missing
		return nil
	})
}

type ZTAPIPendingSettlementLineage struct {
	FinalAttempt      int
	ChannelID         int
	UpstreamRequestID string
}

func PendZTAPIRequestSettlementWithLineage(op, usage, missing string, lineage ZTAPIPendingSettlementLineage) (*ZTAPIRequestSettlement, error) {
	return pendZTAPIRequestSettlementWithLineageAndHook(op, usage, missing, lineage, nil, nil)
}

func pendZTAPIRequestSettlementWithLineageAndHook(op, usage, missing string, lineage ZTAPIPendingSettlementLineage, precondition, hook ztapiSettlementMutationHook) (*ZTAPIRequestSettlement, error) {
	if len(usage) > 65536 || len(missing) > 8192 || !validZTAPISettlementJSON(usage) || !validZTAPISettlementJSON(missing) ||
		lineage.FinalAttempt < 1 || lineage.FinalAttempt > 2 || lineage.ChannelID <= 0 || len(lineage.UpstreamRequestID) > 200 ||
		strings.ContainsAny(lineage.UpstreamRequestID, "\r\n\x00") {
		return nil, ErrZTAPISettlementInvalid
	}
	return mutateZTAPISettlementWithHooks(op, precondition, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		var attempt ZTAPIRequestAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("settlement_id = ?", row.ID).Order("attempt DESC").Take(&attempt).Error; err != nil {
			return err
		}
		if attempt.Attempt != lineage.FinalAttempt || attempt.ChannelID != lineage.ChannelID || attempt.HTTPStatus < 200 || attempt.HTTPStatus >= 300 {
			return ErrZTAPISettlementConflict
		}
		if attempt.UpstreamRequestID != "" && lineage.UpstreamRequestID != "" && attempt.UpstreamRequestID != lineage.UpstreamRequestID {
			return ErrZTAPISettlementConflict
		}
		if attempt.UpstreamRequestID == "" && lineage.UpstreamRequestID != "" {
			attempt.UpstreamRequestID = lineage.UpstreamRequestID
			if err := tx.Save(&attempt).Error; err != nil {
				return err
			}
		}
		if row.Status == ZTAPISettlementPending {
			if row.UsageJSON != usage || row.MissingDimensionsJSON != missing || row.FinalAttempt != lineage.FinalAttempt {
				return ErrZTAPISettlementConflict
			}
			return nil
		}
		if row.Status != ZTAPISettlementReserved || row.FinalAttempt != 0 {
			return ErrZTAPISettlementConflict
		}
		row.Status = ZTAPISettlementPending
		row.UsageJSON = usage
		row.MissingDimensionsJSON = missing
		row.FinalAttempt = lineage.FinalAttempt
		return nil
	}, hook)
}

func releaseZTAPIDispatchedNoChargeWithLineageAndHook(op, usage, dimensions string, lineage ZTAPIPendingSettlementLineage, allowedPendingMissing string, precondition, hook ztapiSettlementMutationHook) (*ZTAPIRequestSettlement, error) {
	if len(usage) > 65536 || len(dimensions) > 65536 || !validZTAPISettlementJSON(usage) || !validZTAPISettlementJSON(dimensions) ||
		lineage.FinalAttempt < 1 || lineage.FinalAttempt > 2 || lineage.ChannelID <= 0 || len(lineage.UpstreamRequestID) > 200 || strings.ContainsAny(lineage.UpstreamRequestID, "\r\n\x00") {
		return nil, ErrZTAPISettlementInvalid
	}
	canonicalDimensions, err := normalizeZTAPIChargeDimensions(dimensions, 0, 0)
	if err != nil {
		return nil, err
	}
	return mutateZTAPISettlementWithHooks(op, precondition, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		var attempt ZTAPIRequestAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("settlement_id = ? AND attempt = ?", row.ID, lineage.FinalAttempt).Take(&attempt).Error; err != nil {
			return err
		}
		if attempt.ChannelID != lineage.ChannelID || attempt.HTTPStatus < 200 || attempt.HTTPStatus >= 300 ||
			(attempt.UpstreamRequestID != "" && lineage.UpstreamRequestID != "" && attempt.UpstreamRequestID != lineage.UpstreamRequestID) {
			return ErrZTAPISettlementConflict
		}
		if row.Status == ZTAPISettlementReleased {
			if row.ChargedQuota != 0 || row.UsageJSON != usage || row.ChargeDimensionsJSON != canonicalDimensions || row.MissingDimensionsJSON != "[]" || row.FinalAttempt != lineage.FinalAttempt {
				return ErrZTAPISettlementConflict
			}
			return nil
		}
		pendingRetry := allowedPendingMissing != "" && row.Status == ZTAPISettlementPending && row.MissingDimensionsJSON == allowedPendingMissing
		if row.Status != ZTAPISettlementReserved && !pendingRetry {
			return ErrZTAPISettlementPending
		}
		if exists, err := HasZTAPISettlementFinalizationIntentTx(tx, row.RequestID); err != nil {
			return err
		} else if exists {
			return ErrZTAPISettlementConflict
		}
		user, token, err := ztapiSettlementOwners(tx, row)
		if err != nil {
			return err
		}
		entry, err := ztapiSettlementWalletDeltaTx(tx, user, row.ReservedQuota, row.RequestID, "ztapi:"+op+":media-no-charge", "trusted media supplier non-billing releases original reservation", BalanceLedgerSourceBillingRefund, false)
		if err != nil {
			return err
		}
		if !row.TokenUnlimited {
			if _, err = ztapiSettlementTokenDeltaTx(tx, token, row.TokenReservedQuota, false); err != nil {
				return err
			}
		}
		if entry != nil {
			row.LastLedgerID = entry.ID
		}
		row.Status = ZTAPISettlementReleased
		row.ChargedQuota = 0
		row.TokenChargedQuota = 0
		row.UsageJSON = usage
		row.ChargeDimensionsJSON = canonicalDimensions
		row.MissingDimensionsJSON = "[]"
		row.FinalAttempt = lineage.FinalAttempt
		return nil
	}, hook)
}

func ReleaseZTAPIRequestSettlement(op string) (*ZTAPIRequestSettlement, error) {
	return mutateZTAPISettlement(op, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		if row.Status == ZTAPISettlementReleased {
			return nil
		}
		if row.Status == ZTAPISettlementSettled {
			return ErrZTAPISettlementConflict
		}
		if row.Status == ZTAPISettlementPending || row.Dispatched {
			return ErrZTAPISettlementPending
		}
		if exists, err := HasZTAPISettlementFinalizationIntentTx(tx, row.RequestID); err != nil {
			return err
		} else if exists {
			return ErrZTAPISettlementConflict
		}
		user, token, err := ztapiSettlementOwners(tx, row)
		if err != nil {
			return err
		}
		entry, err := ztapiSettlementWalletDeltaTx(tx, user, row.ReservedQuota, row.RequestID, "ztapi:"+op+":release", "undispatched request reservation release", BalanceLedgerSourceBillingRefund, false)
		if err != nil {
			return err
		}
		if !row.TokenUnlimited {
			if _, err = ztapiSettlementTokenDeltaTx(tx, token, row.TokenReservedQuota, false); err != nil {
				return err
			}
		}
		if entry != nil {
			row.LastLedgerID = entry.ID
		}
		row.Status = ZTAPISettlementReleased
		return nil
	})
}

type ZTAPISettlementEvidence struct {
	FinalAttempt   int
	UpstreamTaskID string
	ConsumeLog     Log
}

type ztapiSettlementMutationHook func(*gorm.DB, *ZTAPIRequestSettlement) error

type ztapiSettlementFinalizeOptions struct {
	directEvidence        bool
	allowedPendingMissing string
	precondition          ztapiSettlementMutationHook
	hook                  ztapiSettlementMutationHook
}

func FinalizeZTAPIRequestSettlement(op string, actual int64, usage, dimensions string) (*ZTAPIRequestSettlement, error) {
	return finalizeZTAPIRequestSettlement(op, actual, usage, dimensions, nil, false)
}

func FinalizeZTAPIRequestSettlementWithEvidence(op string, actual int64, usage, dimensions string, evidence ZTAPISettlementEvidence) (*ZTAPIRequestSettlement, error) {
	if err := saveZTAPISettlementFinalizationIntent(op, actual, usage, dimensions, evidence); err != nil {
		return nil, err
	}
	return finalizeZTAPIRequestSettlement(op, actual, usage, dimensions, &evidence, false)
}

func finalizeZTAPIRequestSettlement(op string, actual int64, usage, dimensions string, evidence *ZTAPISettlementEvidence, fromIntent bool) (*ZTAPIRequestSettlement, error) {
	return finalizeZTAPIRequestSettlementWithOptions(op, actual, usage, dimensions, evidence, fromIntent, ztapiSettlementFinalizeOptions{})
}

func finalizeZTAPIRequestSettlementWithOptions(op string, actual int64, usage, dimensions string, evidence *ZTAPISettlementEvidence, fromIntent bool, options ztapiSettlementFinalizeOptions) (*ZTAPIRequestSettlement, error) {
	if actual < 0 || actual > maxBalanceLedgerQuota || len(usage) > 65536 || len(dimensions) > 65536 || !validZTAPISettlementJSON(usage) || !validZTAPISettlementJSON(dimensions) {
		return nil, ErrZTAPISettlementInvalid
	}
	return mutateZTAPISettlementWithHooks(op, options.precondition, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		var intent *ZTAPISettlementFinalizationIntent
		if fromIntent || (!options.directEvidence && row.Status != ZTAPISettlementSettled && evidence != nil) {
			var savedEvidence ZTAPISettlementEvidence
			var err error
			intent, savedEvidence, err = loadZTAPISettlementFinalizationIntentTx(tx, row)
			if err != nil {
				return err
			}
			actual, usage, dimensions, evidence = intent.ActualQuota, intent.UsageJSON, intent.ChargeDimensionsJSON, &savedEvidence
		}
		if row.Status == ZTAPISettlementSettled {
			canonical, err := normalizeZTAPIChargeDimensions(dimensions, actual, row.TokenChargedQuota)
			if err != nil || row.ChargedQuota != actual || row.UsageJSON != usage || row.ChargeDimensionsJSON != canonical {
				return ErrZTAPISettlementConflict
			}
			if err := validateZTAPISettlementReplayEvidenceTx(tx, row, evidence); err != nil {
				return err
			}
			return completeZTAPISettlementFinalizationIntentTx(tx, intent)
		}
		pendingIntentRetry := intent != nil && row.Status == ZTAPISettlementPending && row.MissingDimensionsJSON == `["settlement_retry_required"]` && row.UsageJSON == usage
		pendingDirectRetry := options.directEvidence && options.allowedPendingMissing != "" && row.Status == ZTAPISettlementPending && row.MissingDimensionsJSON == options.allowedPendingMissing
		if row.Status != ZTAPISettlementReserved && !pendingIntentRetry && !pendingDirectRetry {
			return ErrZTAPISettlementPending
		}
		if intent == nil {
			if exists, err := HasZTAPISettlementFinalizationIntentTx(tx, row.RequestID); err != nil {
				return err
			} else if exists {
				return ErrZTAPISettlementConflict
			}
		}
		if evidence != nil {
			canonical, err := canonicalZTAPISettlementEvidenceTx(tx, row, actual, *evidence)
			if err != nil {
				return err
			}
			if options.directEvidence {
				evidence = &canonical
			} else if canonical.ConsumeLog.UpstreamRequestId != evidence.ConsumeLog.UpstreamRequestId {
				return ErrZTAPISettlementConflict
			}
		}
		if _, err := normalizeZTAPIChargeDimensions(dimensions, actual, 0); err != nil {
			return err
		}
		user, token, err := ztapiSettlementOwners(tx, row)
		if err != nil {
			return err
		}
		entry, err := ztapiSettlementWalletDeltaTx(tx, user, row.ReservedQuota-actual, row.RequestID, "ztapi:"+op+":settle", "managed request actual usage settlement", BalanceLedgerSourceUsageSettlement, true)
		if err != nil {
			return err
		}
		tokenCharged := int64(0)
		if !row.TokenUnlimited {
			d, e := ztapiSettlementTokenDeltaTx(tx, token, row.TokenReservedQuota-actual, true)
			if e != nil {
				return e
			}
			tokenCharged = row.TokenReservedQuota - d
		}
		if int64(user.UsedQuota)+actual > maxBalanceLedgerQuota {
			return ErrBalanceLedgerOverflow
		}
		update := tx.Unscoped().Model(&User{}).Where("id = ?", row.UserID).Updates(map[string]any{"used_quota": gorm.Expr("used_quota + ?", actual), "request_count": gorm.Expr("request_count + 1")})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrZTAPISettlementConflict
		}
		row.Status = ZTAPISettlementSettled
		row.MissingDimensionsJSON = "[]"
		row.ChargedQuota = actual
		row.TokenChargedQuota = tokenCharged
		row.UsageJSON = usage
		row.ChargeDimensionsJSON, err = normalizeZTAPIChargeDimensions(dimensions, actual, tokenCharged)
		if err != nil {
			return err
		}
		if entry != nil {
			row.LastLedgerID = entry.ID
		}
		if evidence != nil {
			if evidence.ConsumeLog.UserId != row.UserID || evidence.ConsumeLog.TokenId != row.TokenID || evidence.ConsumeLog.RequestId != row.RequestID || int64(evidence.ConsumeLog.Quota) != actual || evidence.ConsumeLog.ModelName != row.PublicModel || evidence.FinalAttempt < 1 || evidence.FinalAttempt > 2 {
				return ErrZTAPISettlementConflict
			}
			var attempt ZTAPIRequestAttempt
			if err = tx.Where("settlement_id = ? AND attempt = ?", row.ID, evidence.FinalAttempt).Take(&attempt).Error; err != nil {
				return err
			}
			var later int64
			if err = tx.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ? AND attempt > ?", row.ID, attempt.Attempt).Count(&later).Error; err != nil {
				return err
			}
			if later != 0 || attempt.ChannelID != evidence.ConsumeLog.ChannelId {
				return ErrZTAPISettlementConflict
			}
			row.FinalAttempt = attempt.Attempt
			if err = tx.Save(row).Error; err != nil {
				return err
			}
			if attempt.UpstreamRequestID != "" || evidence.UpstreamTaskID != "" {
				_, err = RecordZTAPISupplierRefundChargeTx(tx, ZTAPISupplierRefundCharge{RequestID: row.RequestID, Attempt: attempt.Attempt, ChannelID: attempt.ChannelID, CredentialVersion: attempt.CredentialVersion, UpstreamRequestID: attempt.UpstreamRequestID, UpstreamTaskID: evidence.UpstreamTaskID})
				if err != nil {
					return err
				}
			}
			if err = EnqueueZTAPISettlementLogTx(tx, row, evidence.ConsumeLog); err != nil {
				return err
			}
			if err = SyncZTAPIAttemptBillingReviewsTx(tx, row); err != nil {
				return err
			}
		}
		return completeZTAPISettlementFinalizationIntentTx(tx, intent)
	}, options.hook)
}

// Immutable known-usage intent survives a failed financial transaction. Only
// retry scheduling and completion fields may change after insertion.
type ZTAPISettlementFinalizationIntent struct {
	OperationID          string `gorm:"type:varchar(64);primaryKey"`
	RequestID            string `gorm:"type:varchar(128);not null;uniqueIndex"`
	ActualQuota          int64  `gorm:"type:bigint;not null"`
	UsageJSON            string `gorm:"type:text;not null"`
	ChargeDimensionsJSON string `gorm:"type:text;not null"`
	EvidenceJSON         string `gorm:"type:text;not null"`
	PayloadSHA256        string `gorm:"type:varchar(64);not null"`
	CreatedAt            int64  `gorm:"not null"`
	LastAttemptAt        int64  `gorm:"not null;default:0;index"`
	CompletedAt          int64  `gorm:"not null;default:0;index"`
}

func (ZTAPISettlementFinalizationIntent) TableName() string {
	return "ztapi_settlement_finalization_intents"
}

// Caller must hold the canonical request lock through its policy decision.
func HasZTAPISettlementFinalizationIntentTx(tx *gorm.DB, requestID string) (bool, error) {
	if tx == nil || tx.Statement == nil || requestID == "" {
		return false, ErrZTAPISettlementInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return false, ErrZTAPISettlementInvalid
	}
	var rows []ZTAPISettlementFinalizationIntent
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("operation_id").Where("request_id = ?", requestID).Limit(1).Find(&rows).Error
	return len(rows) != 0, err
}

func ztapiSettlementIntentDigest(intent *ZTAPISettlementFinalizationIntent) string {
	payload, _ := common.Marshal([]any{intent.OperationID, intent.RequestID, intent.ActualQuota, intent.UsageJSON, intent.ChargeDimensionsJSON, intent.EvidenceJSON})
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func loadZTAPISettlementFinalizationIntentTx(tx *gorm.DB, row *ZTAPIRequestSettlement) (*ZTAPISettlementFinalizationIntent, ZTAPISettlementEvidence, error) {
	var intent ZTAPISettlementFinalizationIntent
	var evidence ZTAPISettlementEvidence
	if err := tx.Where("operation_id = ?", row.OperationID).Take(&intent).Error; err != nil {
		return nil, evidence, err
	}
	if intent.RequestID != row.RequestID || intent.ActualQuota < 0 || intent.ActualQuota > maxBalanceLedgerQuota ||
		intent.PayloadSHA256 != ztapiSettlementIntentDigest(&intent) || common.UnmarshalJsonStr(intent.EvidenceJSON, &evidence) != nil ||
		evidence.ConsumeLog.UserId != row.UserID || evidence.ConsumeLog.TokenId != row.TokenID || evidence.ConsumeLog.RequestId != row.RequestID ||
		evidence.ConsumeLog.ModelName != row.PublicModel || int64(evidence.ConsumeLog.Quota) != intent.ActualQuota {
		return nil, evidence, ErrZTAPISettlementConflict
	}
	return &intent, evidence, nil
}

func canonicalZTAPISettlementEvidenceTx(tx *gorm.DB, row *ZTAPIRequestSettlement, actual int64, evidence ZTAPISettlementEvidence) (ZTAPISettlementEvidence, error) {
	log := &evidence.ConsumeLog
	if evidence.FinalAttempt < 1 || evidence.FinalAttempt > 2 || log.Id != 0 || log.Type != LogTypeConsume ||
		log.UserId != row.UserID || log.TokenId != row.TokenID || log.RequestId != row.RequestID || log.ModelName != row.PublicModel || int64(log.Quota) != actual ||
		log.ChannelId <= 0 || log.CreatedAt <= 0 || log.UseTime < 0 || log.PromptTokens < 0 || log.CompletionTokens < 0 ||
		(evidence.UpstreamTaskID != "" && !validZTAPIUpstreamTaskID(evidence.UpstreamTaskID)) {
		return evidence, ErrZTAPISettlementConflict
	}
	var attempts []ZTAPIRequestAttempt
	if err := tx.Where("settlement_id = ?", row.ID).Order("attempt DESC").Find(&attempts).Error; err != nil {
		return evidence, err
	}
	if len(attempts) == 0 || attempts[0].Attempt != evidence.FinalAttempt || attempts[0].ChannelID != log.ChannelId || len(attempts[0].UpstreamRequestID) > 200 ||
		(log.UpstreamRequestId != "" && log.UpstreamRequestId != attempts[0].UpstreamRequestID) {
		return evidence, ErrZTAPISettlementConflict
	}
	log.UpstreamRequestId = attempts[0].UpstreamRequestID
	if err := sanitizeZTAPISettlementLogMetadata(log); err != nil {
		return evidence, err
	}
	return evidence, nil
}

func saveZTAPISettlementFinalizationIntent(op string, actual int64, usage, dimensions string, evidence ZTAPISettlementEvidence) error {
	if actual < 0 || actual > maxBalanceLedgerQuota || len(usage) > 65536 || len(dimensions) > 65536 || !validZTAPISettlementJSON(usage) {
		return ErrZTAPISettlementInvalid
	}
	canonicalDims, err := normalizeZTAPIChargeDimensions(dimensions, actual, 0)
	if err != nil {
		return err
	}
	return ztapiSettlementTransaction(func(tx *gorm.DB) error {
		row, err := ztapiSettlementLock(tx, op)
		if err != nil {
			return err
		}
		if row.Status == ZTAPISettlementSettled {
			dims, err := normalizeZTAPIChargeDimensions(dimensions, actual, row.TokenChargedQuota)
			if err != nil || row.ChargedQuota != actual || row.UsageJSON != usage || row.ChargeDimensionsJSON != dims {
				return ErrZTAPISettlementConflict
			}
			return validateZTAPISettlementReplayEvidenceTx(tx, row, &evidence)
		}
		if row.Status != ZTAPISettlementReserved && !(row.Status == ZTAPISettlementPending && row.MissingDimensionsJSON == `["settlement_retry_required"]` && row.UsageJSON == usage) {
			return ErrZTAPISettlementPending
		}
		original, saved, err := loadZTAPISettlementFinalizationIntentTx(tx, row)
		if err == nil {
			if original.ActualQuota != actual || original.UsageJSON != usage || original.ChargeDimensionsJSON != canonicalDims || saved.FinalAttempt != evidence.FinalAttempt || saved.UpstreamTaskID != evidence.UpstreamTaskID {
				return ErrZTAPISettlementConflict
			}
			return matchZTAPISettlementLogEvidence(saved.ConsumeLog, evidence.ConsumeLog)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if row.Status != ZTAPISettlementReserved {
			return ErrZTAPISettlementPending
		}
		canonicalEvidence, err := canonicalZTAPISettlementEvidenceTx(tx, row, actual, evidence)
		if err != nil {
			return err
		}
		payload, err := common.Marshal(canonicalEvidence)
		if err != nil || len(payload) > 65536 {
			return ErrZTAPISettlementInvalid
		}
		intent := ZTAPISettlementFinalizationIntent{OperationID: op, RequestID: row.RequestID, ActualQuota: actual, UsageJSON: usage, ChargeDimensionsJSON: canonicalDims, EvidenceJSON: string(payload), CreatedAt: time.Now().Unix()}
		intent.PayloadSHA256 = ztapiSettlementIntentDigest(&intent)
		return tx.Create(&intent).Error
	})
}

func completeZTAPISettlementFinalizationIntentTx(tx *gorm.DB, intent *ZTAPISettlementFinalizationIntent) error {
	if intent == nil || intent.CompletedAt != 0 {
		return nil
	}
	return tx.Model(&ZTAPISettlementFinalizationIntent{}).Where("operation_id = ?", intent.OperationID).UpdateColumn("completed_at", time.Now().Unix()).Error
}

func RetryZTAPISettlementFinalizations(limit int) error {
	if limit < 1 || limit > 1000 || DB == nil {
		return ErrZTAPISettlementInvalid
	}
	var intents []ZTAPISettlementFinalizationIntent
	if err := DB.Where("completed_at = 0").Order("last_attempt_at, operation_id").Limit(limit).Find(&intents).Error; err != nil {
		return err
	}
	var failures []error
	for _, intent := range intents {
		// Commit scheduling before applying money so rolled-back attempts rotate.
		if err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
			row, err := ztapiSettlementLock(tx, intent.OperationID)
			if err != nil {
				return err
			}
			updates := map[string]any{"last_attempt_at": time.Now().UnixNano()}
			if row.Status == ZTAPISettlementReleased {
				updates["completed_at"] = time.Now().Unix()
			}
			return tx.Model(&ZTAPISettlementFinalizationIntent{}).Where("operation_id = ?", intent.OperationID).Updates(updates).Error
		}); err != nil {
			failures = append(failures, err)
			continue
		}
		var current ZTAPISettlementFinalizationIntent
		if err := DB.Where("operation_id = ?", intent.OperationID).Take(&current).Error; err != nil {
			failures = append(failures, err)
			continue
		}
		if current.CompletedAt != 0 {
			continue
		}
		if _, err := finalizeZTAPIRequestSettlement(intent.OperationID, 0, "{}", "[]", nil, true); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func validateZTAPISettlementReplayEvidenceTx(tx *gorm.DB, row *ZTAPIRequestSettlement, evidence *ZTAPISettlementEvidence) error {
	if evidence == nil {
		return nil
	}
	if evidence.FinalAttempt < 1 || evidence.FinalAttempt > 2 || evidence.FinalAttempt != row.FinalAttempt {
		return ErrZTAPISettlementConflict
	}
	var job ZTAPISettlementLogOutbox
	if err := tx.Where("operation_id = ?", row.OperationID).Take(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrZTAPISettlementConflict
		}
		return err
	}
	var saved Log
	if job.PayloadSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(job.PayloadJSON))) || common.UnmarshalJsonStr(job.PayloadJSON, &saved) != nil ||
		saved.UserId != row.UserID || saved.TokenId != row.TokenID || saved.RequestId != row.RequestID || saved.ModelName != row.PublicModel || int64(saved.Quota) != row.ChargedQuota {
		return ErrZTAPISettlementConflict
	}
	if err := matchZTAPISettlementLogEvidence(saved, evidence.ConsumeLog); err != nil {
		return err
	}
	if evidence.UpstreamTaskID == "" {
		return nil
	}
	var charge ZTAPISupplierRefundCharge
	if err := tx.Where("request_id = ? AND attempt = ? AND billing_proof_id = 0", row.RequestID, row.FinalAttempt).Take(&charge).Error; err != nil {
		return err
	}
	if charge.UpstreamTaskID != evidence.UpstreamTaskID {
		return ErrZTAPISettlementConflict
	}
	return nil
}

func matchZTAPISettlementLogEvidence(saved, incoming Log) error {
	// Retry timing and display metadata do not replace the original outbox.
	// An omitted wire ID is resolved by the original final attempt at enqueue.
	if incoming.Id != 0 || incoming.Type != LogTypeConsume || saved.Type != LogTypeConsume ||
		incoming.UserId != saved.UserId || incoming.TokenId != saved.TokenId || incoming.RequestId != saved.RequestId ||
		incoming.ModelName != saved.ModelName || incoming.Quota != saved.Quota || incoming.ChannelId != saved.ChannelId ||
		incoming.PromptTokens != saved.PromptTokens || incoming.CompletionTokens != saved.CompletionTokens ||
		incoming.IsStream != saved.IsStream || incoming.Group != saved.Group ||
		(incoming.UpstreamRequestId != "" && incoming.UpstreamRequestId != saved.UpstreamRequestId) {
		return ErrZTAPISettlementConflict
	}
	if err := sanitizeZTAPISettlementLogMetadata(&incoming); err != nil || incoming.Other != saved.Other {
		return ErrZTAPISettlementConflict
	}
	return nil
}

func normalizeZTAPIChargeDimensions(raw string, actual, tokenCharge int64) (string, error) {
	var dims []ZTAPISupplierRefundDimension
	if err := common.UnmarshalJsonStr(raw, &dims); err != nil || len(dims) > 128 {
		return "", ErrZTAPISettlementInvalid
	}
	remaining := tokenCharge
	for i := range dims {
		dims[i].TokenChargedQuota = min(remaining, dims[i].ChargedQuota)
		remaining -= dims[i].TokenChargedQuota
	}
	if remaining != 0 {
		return "", ErrZTAPISettlementInvalid
	}
	if _, err := planZTAPISupplierRefund(actual, tokenCharge, dims, nil, "full", nil); err != nil {
		return "", err
	}
	if dims == nil {
		dims = []ZTAPISupplierRefundDimension{}
	}
	encoded, err := common.Marshal(dims)
	return string(encoded), err
}

func validZTAPISettlementJSON(raw string) bool {
	var parsed json.RawMessage
	return common.UnmarshalJsonStr(raw, &parsed) == nil
}

func mutateZTAPISettlement(op string, fn func(*gorm.DB, *ZTAPIRequestSettlement) error) (*ZTAPIRequestSettlement, error) {
	return mutateZTAPISettlementWithHook(op, fn, nil)
}

func mutateZTAPISettlementWithHook(op string, fn func(*gorm.DB, *ZTAPIRequestSettlement) error, hook ztapiSettlementMutationHook) (*ZTAPIRequestSettlement, error) {
	return mutateZTAPISettlementWithHooks(op, nil, fn, hook)
}

func mutateZTAPISettlementWithHooks(op string, precondition ztapiSettlementMutationHook, fn func(*gorm.DB, *ZTAPIRequestSettlement) error, hook ztapiSettlementMutationHook) (*ZTAPIRequestSettlement, error) {
	var row *ZTAPIRequestSettlement
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		var e error
		row, e = ztapiSettlementLock(tx, op)
		if e != nil {
			return e
		}
		if precondition != nil {
			if e = precondition(tx, row); e != nil {
				return e
			}
		}
		if e = fn(tx, row); e != nil {
			return e
		}
		row.CacheSyncPending = true
		if e = tx.Save(row).Error; e != nil {
			return e
		}
		if hook != nil {
			return hook(tx, row)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	syncZTAPISettlementCaches(row)
	return row, nil
}

func ztapiSettlementWalletDeltaTx(tx *gorm.DB, user *User, delta int64, requestID, key, reason, source string, allowNegative bool) (*BalanceLedger, error) {
	if delta == 0 {
		return nil, nil
	}
	before := int64(user.Quota)
	after, err := checkedBalanceLedgerSum(before, delta)
	if allowNegative {
		after, err = checkedSettlementBalanceLedgerSum(before, delta)
	}
	if err != nil {
		return nil, err
	}
	updates := balanceLedgerUserUpdates(*user, after, allowNegative)
	if user.DeletedAt.Valid {
		delete(updates, "status")
	}
	update := tx.Unscoped().Model(&User{}).Where("id = ?", user.Id).Updates(updates)
	if update.Error != nil {
		return nil, update.Error
	}
	if update.RowsAffected != 1 {
		return nil, ErrZTAPISettlementConflict
	}
	row := &BalanceLedger{UserID: user.Id, Delta: delta, BalanceBefore: before, BalanceAfter: after, Reason: reason, IdempotencyKey: key, RequestID: requestID, SourceType: source, CreatedAt: time.Now().UTC()}
	if err = tx.Create(row).Error; err != nil {
		return nil, err
	}
	user.Quota = int(after)
	return row, nil
}

// delta is a credit; a final debit beyond a finite key's remaining limit exhausts
// that key while the wallet records the full actual debt.
func ztapiSettlementTokenDeltaTx(tx *gorm.DB, token *Token, delta int64, allowDebt bool) (int64, error) {
	if delta == 0 {
		return 0, nil
	}
	if delta < 0 && -delta > int64(token.RemainQuota) {
		if !allowDebt {
			return 0, ErrInsufficientTokenQuota
		}
		delta = -int64(token.RemainQuota)
	}
	if delta == 0 {
		return 0, nil
	}
	after, err := checkedBalanceLedgerSum(int64(token.RemainQuota), delta)
	if err != nil {
		return 0, err
	}
	used := int64(token.UsedQuota) - delta
	if used < 0 || used > maxBalanceLedgerQuota {
		return 0, ErrBalanceLedgerOverflow
	}
	updates := map[string]any{"remain_quota": after, "used_quota": used}
	if after == 0 && token.Status == common.TokenStatusEnabled {
		updates["status"] = common.TokenStatusExhausted
	}
	if after > 0 && token.Status == common.TokenStatusExhausted {
		updates["status"] = common.TokenStatusEnabled
	}
	update := tx.Unscoped().Model(&Token{}).Where("id = ? AND user_id = ?", token.Id, token.UserId).Updates(updates)
	if update.Error != nil {
		return 0, update.Error
	}
	if update.RowsAffected != 1 {
		return 0, ErrZTAPISettlementConflict
	}
	token.RemainQuota = int(after)
	token.UsedQuota = int(used)
	return delta, nil
}

func syncZTAPISettlementCaches(row *ZTAPIRequestSettlement) {
	if row == nil {
		return
	}
	if DB.Dialector.Name() == "sqlite" {
		balanceLedgerSQLiteMu.Lock()
		defer balanceLedgerSQLiteMu.Unlock()
	}
	// Failure is durable and retried, never reported as an uncommitted debit.
	if err := balanceLedgerCacheSync(row.UserID); err != nil {
		return
	}
	if common.RedisEnabled {
		var token Token
		if err := DB.Unscoped().First(&token, row.TokenID).Error; err != nil {
			return
		}
		if err := cacheDeleteToken(token.KeyHash); err != nil {
			return
		}
	}
	if result := DB.Model(&ZTAPIRequestSettlement{}).Where("id = ? AND updated_at = ?", row.ID, row.UpdatedAt).UpdateColumn("cache_sync_pending", false); result.Error == nil && result.RowsAffected == 1 {
		row.CacheSyncPending = false
	}
}

func RetryZTAPISettlementCaches(limit int) error {
	if limit < 1 || limit > 1000 {
		return ErrZTAPISettlementInvalid
	}
	var rows []ZTAPIRequestSettlement
	if err := DB.Where("cache_sync_pending = ?", true).Order("cache_last_attempt_at, id").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		if err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
			return tx.Model(&ZTAPIRequestSettlement{}).Where("id = ?", rows[i].ID).UpdateColumn("cache_last_attempt_at", time.Now().UnixNano()).Error
		}); err != nil {
			return err
		}
		syncZTAPISettlementCaches(&rows[i])
	}
	return nil
}
