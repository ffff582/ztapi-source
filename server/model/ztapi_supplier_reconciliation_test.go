package model

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPISupplierReconciliationTest(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := DB
	originalSQLite := common.UsingSQLite
	common.UsingSQLite = true
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &Channel{}, &BalanceLedger{}, &ZTAPIRequestSettlement{}, &ZTAPIRequestAttempt{}, &ZTAPIMediaTask{}, &ZTAPISupplierRefundCharge{}, &ZTAPIAttemptBillingProof{}, &ZTAPISupplierRefund{}))
	require.NoError(t, db.Create(&User{Id: 7, Username: "recon-finance", Password: "unused", Status: common.UserStatusEnabled, Role: common.RoleFinanceUser, AffCode: "recon-finance-aff"}).Error)
	require.NoError(t, MigrateZTAPISupplierReconciliation(db))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		DB = originalDB
		common.UsingSQLite = originalSQLite
	})
	return db
}

func validZTAPISupplierLedgerRecord(id string) ZTAPISupplierLedgerRecord {
	billable := true
	return ZTAPISupplierLedgerRecord{
		SupplierRecordID: id,
		RequestID:        "upstream-request-1",
		CredentialRef:    "credential-v1",
		ProviderModel:    "provider-model",
		ResourceType:     "enterprise",
		OccurredAt:       time.Now().UTC().Unix(),
		Billable:         &billable,
		DimensionsJSON:   `{"usage_semantic":"openai","usage":[{"dimension":"input_tokens","quantity":40}]}`,
		DebitAmount:      "0.004",
		Currency:         "USD",
		RawEvidenceHash:  strings.Repeat("a", 64),
	}
}

func validZTAPISupplierImport(records ...ZTAPISupplierLedgerRecord) ZTAPISupplierReconciliationImport {
	return ZTAPISupplierReconciliationImport{
		Supplier:       "yunxin",
		IdempotencyKey: "import-2026-09-09",
		FileChecksum:   strings.Repeat("b", 64),
		PayloadHash:    strings.Repeat("c", 64),
		Format:         "json",
		OperatorID:     7,
		DryRun:         true,
		Records:        records,
	}
}

func TestZTAPISupplierReconciliationRejectsInvalidCanonicalRecords(t *testing.T) {
	db := setupZTAPISupplierReconciliationTest(t)
	base := validZTAPISupplierLedgerRecord("bill-1")
	negative := base
	negative.SupplierRecordID, negative.DebitAmount = "bill-negative", "-0.01"
	noLineage := base
	noLineage.SupplierRecordID, noLineage.RequestID = "bill-no-lineage", ""
	secretHash := base
	secretHash.SupplierRecordID, secretHash.RawEvidenceHash = "bill-secret", "sk-live-secret-must-never-be-stored"
	secretCredential := base
	secretCredential.SupplierRecordID, secretCredential.CredentialRef = "bill-secret-credential", "Bearer supplier-secret"

	for _, tc := range []struct {
		name   string
		record ZTAPISupplierLedgerRecord
	}{
		{"missing unique id", func() ZTAPISupplierLedgerRecord { r := base; r.SupplierRecordID = ""; return r }()},
		{"neither request nor task id", noLineage},
		{"negative debit", negative},
		{"raw evidence instead of hash", secretHash},
		{"credential secret instead of stable reference", secretCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validZTAPISupplierImport(tc.record)
			_, err := PrepareZTAPISupplierReconciliation(db, input)
			require.ErrorIs(t, err, ErrZTAPISupplierReconciliationInvalid)
			var count int64
			require.NoError(t, db.Model(&ZTAPISupplierReconciliationEntry{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestZTAPISupplierReconciliationRejectsOrphanOverRefundAndMismatchedLineage(t *testing.T) {
	db := setupZTAPISupplierReconciliationTest(t)
	base := validZTAPISupplierLedgerRecord("bill-original")
	refund := base
	refund.SupplierRecordID = "refund-1"
	refund.DebitAmount = ""
	refund.ReversalID = "reversal-1"
	refund.OriginalRecordID = base.SupplierRecordID
	refund.ReversalAmount = "0.003"
	refund.DimensionsJSON = `{"refund_mode":"full"}`

	t.Run("orphan refund", func(t *testing.T) {
		input := validZTAPISupplierImport(refund)
		_, err := PrepareZTAPISupplierReconciliation(db, input)
		require.ErrorIs(t, err, ErrZTAPISupplierReconciliationOrphanRefund)
	})

	t.Run("over refund", func(t *testing.T) {
		over := refund
		over.ReversalAmount = "0.005"
		over.DimensionsJSON = `{"refund_mode":"partial","refund_units":[{"dimension":"input_tokens","units":"40"}]}`
		input := validZTAPISupplierImport(base, over)
		input.IdempotencyKey = "over-refund"
		_, err := PrepareZTAPISupplierReconciliation(db, input)
		require.ErrorIs(t, err, ErrZTAPISupplierReconciliationOverRefund)
	})

	t.Run("mismatched customer lineage", func(t *testing.T) {
		mismatch := refund
		mismatch.RequestID = "another-customer-request"
		input := validZTAPISupplierImport(base, mismatch)
		input.IdempotencyKey = "mismatched-refund"
		_, err := PrepareZTAPISupplierReconciliation(db, input)
		require.ErrorIs(t, err, ErrZTAPISupplierReconciliationLineage)
	})
}

func TestZTAPISupplierReconciliationPreviewApplyMatchesExactAttemptAndIsIdempotent(t *testing.T) {
	db := setupZTAPISupplierReconciliationTest(t)
	user := User{Username: "recon-user", Password: "unused", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: "recon-user-aff"}
	require.NoError(t, db.Create(&user).Error)
	token := Token{UserId: user.Id, KeyHash: "recon-token", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000}
	require.NoError(t, db.Create(&token).Error)
	settlement := ZTAPIRequestSettlement{
		OperationID: "recon-operation", RequestID: "our-request-1", UserID: user.Id, TokenID: token.Id,
		PublicModel: "public-model", PriceSnapshotJSON: `{"source_model":"provider-model"}`,
		Status: ZTAPISettlementSettled, FinalAttempt: 2, ChargedQuota: 100, TokenChargedQuota: 100,
		UsageJSON: `{}`, ChargeDimensionsJSON: `[]`, MissingDimensionsJSON: `[]`,
	}
	require.NoError(t, db.Create(&settlement).Error)
	require.NoError(t, db.Create(&ZTAPIRequestAttempt{SettlementID: settlement.ID, Attempt: 1, ChannelID: 11, CredentialVersion: "credential-v1", Protocol: "chat", UpstreamRequestID: "upstream-request-1", HTTPStatus: 502}).Error)
	require.NoError(t, db.Create(&ZTAPIRequestAttempt{SettlementID: settlement.ID, Attempt: 2, ChannelID: 12, CredentialVersion: "credential-v2", Protocol: "chat", UpstreamRequestID: "upstream-request-2", HTTPStatus: 200}).Error)

	record := validZTAPISupplierLedgerRecord("bill-1")
	previewInput := validZTAPISupplierImport(record)
	preview, err := PrepareZTAPISupplierReconciliation(db, previewInput)
	require.NoError(t, err)
	require.Equal(t, ZTAPISupplierReconciliationPreviewed, preview.Batch.Status)
	require.Equal(t, 1, preview.Matched)
	require.Empty(t, preview.Entries)

	applyInput := previewInput
	applyInput.DryRun = false
	applied, err := PrepareZTAPISupplierReconciliation(db, applyInput)
	require.NoError(t, err)
	require.Equal(t, ZTAPISupplierReconciliationApplied, applied.Batch.Status)
	require.Len(t, applied.Entries, 1)
	require.Equal(t, settlement.ID, applied.Entries[0].SettlementID)
	require.Equal(t, user.Id, applied.Entries[0].UserID)
	require.Equal(t, 1, applied.Entries[0].Attempt)
	require.Equal(t, 11, applied.Entries[0].ChannelID)
	require.Equal(t, ZTAPISupplierReconciliationActionAttemptBilling, applied.Entries[0].ActionKind)

	replayed, err := PrepareZTAPISupplierReconciliation(db, applyInput)
	require.NoError(t, err)
	require.Equal(t, applied.Batch.ID, replayed.Batch.ID)
	require.Equal(t, applied.Entries[0].ID, replayed.Entries[0].ID)

	var entryCount int64
	require.NoError(t, db.Model(&ZTAPISupplierReconciliationEntry{}).Count(&entryCount).Error)
	require.EqualValues(t, 1, entryCount)
}

func TestZTAPISupplierReconciliationRejectsDuplicateRecordWithDifferentPayload(t *testing.T) {
	db := setupZTAPISupplierReconciliationTest(t)
	record := validZTAPISupplierLedgerRecord("same-supplier-record")
	first := validZTAPISupplierImport(record)
	_, err := PrepareZTAPISupplierReconciliation(db, first)
	require.NoError(t, err)
	first.DryRun = false
	_, err = PrepareZTAPISupplierReconciliation(db, first)
	require.NoError(t, err)

	changed := record
	changed.DebitAmount = "0.005"
	second := validZTAPISupplierImport(changed)
	second.IdempotencyKey = "different-import"
	second.FileChecksum = strings.Repeat("d", 64)
	second.PayloadHash = strings.Repeat("e", 64)
	_, err = PrepareZTAPISupplierReconciliation(db, second)
	require.ErrorIs(t, err, ErrZTAPISupplierReconciliationConflict)
}

func TestZTAPISupplierReconciliationExactDuplicateAcrossFilesLinksWithoutDuplicatingEvidence(t *testing.T) {
	db := setupZTAPISupplierReconciliationTest(t)
	record := validZTAPISupplierLedgerRecord("same-exact-record")
	first := validZTAPISupplierImport(record)
	first.IdempotencyKey = "first-exact-file"
	_, err := PrepareZTAPISupplierReconciliation(db, first)
	require.NoError(t, err)
	first.DryRun = false
	firstResult, err := PrepareZTAPISupplierReconciliation(db, first)
	require.NoError(t, err)
	require.Len(t, firstResult.Entries, 1)

	second := validZTAPISupplierImport(record)
	second.IdempotencyKey = "second-exact-file"
	second.FileChecksum = strings.Repeat("d", 64)
	second.PayloadHash = strings.Repeat("e", 64)
	_, err = PrepareZTAPISupplierReconciliation(db, second)
	require.NoError(t, err)
	second.DryRun = false
	secondResult, err := PrepareZTAPISupplierReconciliation(db, second)
	require.NoError(t, err)
	require.Len(t, secondResult.Entries, 1)
	require.Equal(t, firstResult.Entries[0].ID, secondResult.Entries[0].ID)

	var entries, links int64
	require.NoError(t, db.Model(&ZTAPISupplierReconciliationEntry{}).Count(&entries).Error)
	require.NoError(t, db.Model(&ZTAPISupplierReconciliationBatchEntry{}).Count(&links).Error)
	require.EqualValues(t, 1, entries)
	require.EqualValues(t, 2, links)
}

func TestZTAPISupplierReconciliationActionTransitionsAreTypedAndImmutable(t *testing.T) {
	db := setupZTAPISupplierReconciliationTest(t)
	entry := ZTAPISupplierReconciliationEntry{BatchID: 1, Supplier: "yunxin", SupplierRecordID: "action-record", PayloadHash: strings.Repeat("a", 64), RecordJSON: `{}`, ActionKind: ZTAPISupplierReconciliationActionAttemptBilling, MatchReason: "matched"}
	require.NoError(t, db.Create(&entry).Error)
	require.NoError(t, db.Create(&ZTAPISupplierReconciliationAction{EntryID: entry.ID, Kind: entry.ActionKind, Status: "pending"}).Error)

	require.ErrorIs(t, UpdateZTAPISupplierReconciliationAction(entry.ID, "submitted", 0, 0, ""), ErrZTAPISupplierReconciliationInvalid)
	require.ErrorIs(t, UpdateZTAPISupplierReconciliationAction(entry.ID, "submitted", 0, 9, ""), ErrZTAPISupplierReconciliationInvalid)
	require.NoError(t, UpdateZTAPISupplierReconciliationAction(entry.ID, "submitted", 9, 0, ""))
	require.NoError(t, UpdateZTAPISupplierReconciliationAction(entry.ID, "submitted", 9, 0, ""))
	require.ErrorIs(t, UpdateZTAPISupplierReconciliationAction(entry.ID, "failed", 0, 0, "retry"), ErrZTAPISupplierReconciliationConflict)
}

func TestZTAPISupplierReconciliationAcceptsCanonicalMediaMeteringButDoesNotInventTokenUsage(t *testing.T) {
	db := setupZTAPISupplierReconciliationTest(t)
	record := validZTAPISupplierLedgerRecord("media-metering")
	record.DimensionsJSON = `{"metering":[{"dimension":"images","units":"1"}]}`
	input := validZTAPISupplierImport(record)
	result, err := PrepareZTAPISupplierReconciliation(db, input)
	require.NoError(t, err)
	require.Equal(t, 0, result.Matched)
	require.Equal(t, 1, result.Unmatched)
}
