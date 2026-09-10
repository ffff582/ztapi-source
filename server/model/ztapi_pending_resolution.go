package model

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ZTAPINoChargeAttempt struct {
	Attempt               int    `json:"attempt"`
	ChannelID             int    `json:"channel_id"`
	CredentialVersion     string `json:"credential_version"`
	UpstreamRequestID     string `json:"upstream_request_id"`
	VerificationReference string `json:"verification_reference"`
}

// No amount or destination is accepted. An authorized reviewer attests to
// supplier non-billing for every dispatched attempt, not just the last one.
type ZTAPINoChargeProof struct {
	Source                string                 `json:"source"`
	ProofID               string                 `json:"proof_id"`
	VerificationReference string                 `json:"verification_reference"`
	Attempts              []ZTAPINoChargeAttempt `json:"attempts"`
}

type ZTAPIPendingResolution struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	SettlementID uint      `gorm:"not null;uniqueIndex" json:"settlement_id"`
	ProofKey     string    `gorm:"type:char(64);not null;uniqueIndex" json:"proof_key"`
	OperatorID   int       `gorm:"not null" json:"operator_id"`
	ProofJSON    string    `gorm:"type:text;not null" json:"proof"`
	LedgerID     int       `gorm:"not null" json:"ledger_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func (ZTAPIPendingResolution) TableName() string            { return "ztapi_pending_resolutions" }
func (*ZTAPIPendingResolution) BeforeUpdate(*gorm.DB) error { return ErrZTAPISettlementConflict }
func (*ZTAPIPendingResolution) BeforeDelete(*gorm.DB) error { return ErrZTAPISettlementConflict }

func ResolveZTAPIPendingNoCharge(id uint, operatorID int, proof ZTAPINoChargeProof) (*ZTAPIRequestSettlement, error) {
	if id == 0 || operatorID <= 0 || strings.TrimSpace(proof.Source) == "" || len(proof.Source) > 128 || strings.TrimSpace(proof.ProofID) == "" || len(proof.ProofID) > 256 || strings.TrimSpace(proof.VerificationReference) == "" || len(proof.VerificationReference) > 512 || len(proof.Attempts) < 1 || len(proof.Attempts) > 2 {
		return nil, ErrZTAPISettlementInvalid
	}
	for _, a := range proof.Attempts {
		if strings.TrimSpace(a.VerificationReference) == "" || len(a.VerificationReference) > 512 || len(a.CredentialVersion) > 64 || len(a.UpstreamRequestID) > 200 {
			return nil, ErrZTAPISettlementInvalid
		}
	}
	raw, err := common.Marshal(proof)
	if err != nil {
		return nil, err
	}
	identity, _ := common.Marshal([]string{proof.Source, proof.ProofID})
	proofKey := ztapiSupplierRefundHash(string(identity))
	var row ZTAPIRequestSettlement
	err = ztapiSettlementTransaction(func(tx *gorm.DB) error {
		// Lock before any snapshot reads so concurrent dispatch evidence is current.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		var operator User
		if err := tx.First(&operator, operatorID).Error; err != nil {
			return err
		}
		if operator.Status != common.UserStatusEnabled || !common.HasAdminPermission(operator.Role, common.PermissionFinanceWrite) {
			return ErrZTAPISupplierRefundUnauthorized
		}
		var existing ZTAPIPendingResolution
		if err := tx.Where("settlement_id = ?", id).Take(&existing).Error; err == nil {
			if existing.ProofKey != proofKey || existing.ProofJSON != string(raw) || row.Status != ZTAPISettlementReleased {
				return ErrZTAPISettlementConflict
			}
			return SyncZTAPIAttemptBillingReviewsTx(tx, &row)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if row.Status != ZTAPISettlementPending {
			return ErrZTAPISettlementPending
		}
		if exists, err := HasZTAPISettlementFinalizationIntentTx(tx, row.RequestID); err != nil {
			return err
		} else if exists {
			return ErrZTAPISettlementConflict
		}
		var attempts []ZTAPIRequestAttempt
		if err := tx.Where("settlement_id = ?", id).Order("attempt").Find(&attempts).Error; err != nil {
			return err
		}
		if len(attempts) != len(proof.Attempts) {
			return ErrZTAPISettlementConflict
		}
		for i, a := range attempts {
			p := proof.Attempts[i]
			if p.Attempt != a.Attempt || p.ChannelID != a.ChannelID || p.CredentialVersion != a.CredentialVersion || p.UpstreamRequestID != a.UpstreamRequestID {
				return ErrZTAPISettlementConflict
			}
		}
		user, token, err := ztapiSettlementOwners(tx, &row)
		if err != nil {
			return err
		}
		entry, err := ztapiSettlementWalletDeltaTx(tx, user, row.ReservedQuota, row.RequestID, "ztapi:"+row.OperationID+":no-charge", "reviewed supplier non-billing releases original reservation", BalanceLedgerSourceBillingRefund, false)
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
		resolution := ZTAPIPendingResolution{SettlementID: row.ID, ProofKey: proofKey, OperatorID: operatorID, ProofJSON: string(raw), LedgerID: row.LastLedgerID}
		if err = tx.Create(&resolution).Error; err != nil {
			return err
		}
		row.Status = ZTAPISettlementReleased
		row.CacheSyncPending = true
		if err = tx.Save(&row).Error; err != nil {
			return err
		}
		return SyncZTAPIAttemptBillingReviewsTx(tx, &row)
	})
	if err != nil {
		return nil, err
	}
	syncZTAPISettlementCaches(&row)
	return &row, nil
}
