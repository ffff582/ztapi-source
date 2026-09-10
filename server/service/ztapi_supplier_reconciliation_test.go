package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPISupplierReconciliationServiceTest(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := model.DB
	originalSQLite := common.UsingSQLite
	common.UsingSQLite = true
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "supplier-reconciliation.db")), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.BalanceLedger{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPIMediaTask{}, &model.ZTAPISupplierRefundCharge{}, &model.ZTAPIAttemptBillingReview{}, &model.ZTAPIAttemptBillingProof{}, &model.ZTAPISupplierRefund{}))
	require.NoError(t, db.Create(&model.User{Id: 7, Username: "recon-service-finance", Password: "unused", Status: common.UserStatusEnabled, Role: common.RoleFinanceUser, AffCode: "recon-service-finance-aff"}).Error)
	require.NoError(t, model.MigrateZTAPISupplierReconciliation(db))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB = originalDB
		common.UsingSQLite = originalSQLite
	})
	return db
}

func ztapiSupplierServiceRecord(id string) model.ZTAPISupplierLedgerRecord {
	billable := true
	return model.ZTAPISupplierLedgerRecord{
		SupplierRecordID: id, RequestID: "supplier-wire-1", CredentialRef: "cred-v1",
		ProviderModel: "provider-model", ResourceType: "enterprise", OccurredAt: time.Now().UTC().Unix(), Billable: &billable,
		DimensionsJSON: `{"usage_semantic":"openai","usage":[{"dimension":"input_tokens","quantity":40}]}`,
		DebitAmount:    "0.004", Currency: "USD", RawEvidenceHash: strings.Repeat("a", 64),
	}
}

func ztapiSupplierImportRequest(t *testing.T, key string, records ...model.ZTAPISupplierLedgerRecord) ZTAPISupplierReconciliationRequest {
	t.Helper()
	content, err := common.Marshal(records)
	require.NoError(t, err)
	sum := fmt.Sprintf("%x", sha256.Sum256(content))
	return ZTAPISupplierReconciliationRequest{Supplier: "yunxin", IdempotencyKey: key, FileChecksum: sum, Format: "json", Content: string(content), DryRun: true}
}

func seedZTAPISupplierServiceAttempt(t *testing.T, db *gorm.DB, withCharge bool) (model.User, model.ZTAPIRequestSettlement, model.ZTAPIRequestAttempt) {
	t.Helper()
	user := model.User{Username: "recon-service-user", Password: "unused", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: "recon-service-aff", Quota: 1000}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, KeyHash: "recon-service-token", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000}
	require.NoError(t, db.Create(&token).Error)
	settlement := model.ZTAPIRequestSettlement{OperationID: "recon-service-op", RequestID: "our-request", UserID: user.Id, TokenID: token.Id, PublicModel: "public-model", PriceSnapshotJSON: `{"source_model":"provider-model"}`, Status: model.ZTAPISettlementSettled, FinalAttempt: 2, ChargedQuota: 100, TokenChargedQuota: 100, UsageJSON: `{}`, ChargeDimensionsJSON: `[]`, MissingDimensionsJSON: `[]`}
	require.NoError(t, db.Create(&settlement).Error)
	attempt := model.ZTAPIRequestAttempt{SettlementID: settlement.ID, Attempt: 1, ChannelID: 11, CredentialVersion: "cred-v1", Protocol: "chat", UpstreamRequestID: "supplier-wire-1", HTTPStatus: 502}
	require.NoError(t, db.Create(&attempt).Error)
	require.NoError(t, db.Create(&model.ZTAPIRequestAttempt{SettlementID: settlement.ID, Attempt: 2, ChannelID: 12, CredentialVersion: "cred-v2", Protocol: "chat", UpstreamRequestID: "supplier-wire-2", HTTPStatus: 200}).Error)
	if withCharge {
		require.NoError(t, db.Create(&model.ZTAPISupplierRefundCharge{RequestID: settlement.RequestID, SettlementID: settlement.ID, UserID: user.Id, TokenID: token.Id, Attempt: attempt.Attempt, ChannelID: attempt.ChannelID, CredentialVersion: attempt.CredentialVersion, UpstreamRequestID: attempt.UpstreamRequestID, UpstreamBillID: "", PriceSnapshotJSON: settlement.PriceSnapshotJSON, DimensionsJSON: `[{"dimension":"input_tokens","units":"40","unit_quota":"1","charged_quota":40,"token_charged_quota":40}]`, ChargedQuota: 40, TokenChargedQuota: 40, ProgressJSON: `[]`}).Error)
	}
	return user, settlement, attempt
}

func TestZTAPISupplierReconciliationParsesStrictJSONAndCSVAndChecksFileChecksum(t *testing.T) {
	setupZTAPISupplierReconciliationServiceTest(t)
	record := ztapiSupplierServiceRecord("parse-1")

	valid := ztapiSupplierImportRequest(t, "parse-json", record)
	result, err := ImportZTAPISupplierReconciliation(context.Background(), 7, valid)
	require.NoError(t, err)
	require.Equal(t, model.ZTAPISupplierReconciliationPreviewed, result.Batch.Status)

	badChecksum := valid
	badChecksum.IdempotencyKey = "bad-checksum"
	badChecksum.FileChecksum = strings.Repeat("f", 64)
	_, err = ImportZTAPISupplierReconciliation(context.Background(), 7, badChecksum)
	require.ErrorIs(t, err, ErrZTAPISupplierReconciliationChecksum)

	unknown := valid
	unknown.IdempotencyKey = "unknown-field"
	unknown.Content = strings.Replace(unknown.Content, `"supplier_record_id"`, `"raw_evidence":"SECRET","supplier_record_id"`, 1)
	unknown.FileChecksum = fmt.Sprintf("%x", sha256.Sum256([]byte(unknown.Content)))
	_, err = ImportZTAPISupplierReconciliation(context.Background(), 7, unknown)
	require.ErrorIs(t, err, model.ErrZTAPISupplierReconciliationInvalid)

	csvDimensions := `"` + strings.ReplaceAll(record.DimensionsJSON, `"`, `""`) + `"`
	csvContent := "supplier_record_id,request_id,task_id,credential_ref,provider_model,resource_type,occurred_at,billable,dimensions_json,debit_amount,currency,reversal_id,original_record_id,reversal_amount,raw_evidence_hash\n" +
		fmt.Sprintf("parse-csv,supplier-wire-1,,cred-v1,provider-model,enterprise,%d,true,%s,0.004,USD,,,,%s\n", record.OccurredAt, csvDimensions, record.RawEvidenceHash)
	csvRequest := ZTAPISupplierReconciliationRequest{Supplier: "yunxin", IdempotencyKey: "parse-csv", Format: "csv", Content: csvContent, FileChecksum: fmt.Sprintf("%x", sha256.Sum256([]byte(csvContent))), DryRun: true}
	result, err = ImportZTAPISupplierReconciliation(context.Background(), 7, csvRequest)
	require.NoError(t, err)
	require.Equal(t, 1, result.Batch.RowCount)
}

func TestZTAPISupplierReconciliationApplyCreatesReviewProofWithoutChangingCustomerBalance(t *testing.T) {
	db := setupZTAPISupplierReconciliationServiceTest(t)
	user, settlement, attempt := seedZTAPISupplierServiceAttempt(t, db, false)
	request := ztapiSupplierImportRequest(t, "apply-billing", ztapiSupplierServiceRecord("supplier-bill-1"))
	_, err := ImportZTAPISupplierReconciliation(context.Background(), 7, request)
	require.NoError(t, err)
	request.DryRun = false
	result, err := ImportZTAPISupplierReconciliation(context.Background(), 7, request)
	require.NoError(t, err)
	require.Len(t, result.Actions, 1)
	require.Equal(t, "submitted", result.Actions[0].Status)
	require.NotZero(t, result.Actions[0].BillingProofID)

	var proof model.ZTAPIAttemptBillingProof
	require.NoError(t, db.First(&proof, result.Actions[0].BillingProofID).Error)
	var submission model.ZTAPIAttemptBillingSubmission
	require.NoError(t, common.UnmarshalJsonStr(proof.SubmissionJSON, &submission))
	require.Equal(t, settlement.RequestID, submission.RequestID)
	require.Equal(t, user.Id, submission.UserID)
	require.Equal(t, attempt.Attempt, submission.Attempt)
	require.Equal(t, attempt.ChannelID, submission.ChannelID)
	require.Equal(t, "supplier-bill-1", submission.UpstreamBillID)
	require.Equal(t, []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 40}}, submission.Usage)

	var persisted model.User
	require.NoError(t, db.First(&persisted, user.Id).Error)
	require.Equal(t, 1000, persisted.Quota, "import and proof submission must not approve or mutate customer money")

	replayed, err := ImportZTAPISupplierReconciliation(context.Background(), 7, request)
	require.NoError(t, err)
	require.Equal(t, result.Actions[0].BillingProofID, replayed.Actions[0].BillingProofID)
}

func TestZTAPISupplierReconciliationRefundCreatesExactCustomerProofWithoutApplyingIt(t *testing.T) {
	db := setupZTAPISupplierReconciliationServiceTest(t)
	user, _, _ := seedZTAPISupplierServiceAttempt(t, db, true)
	original := ztapiSupplierServiceRecord("supplier-original")
	first := ztapiSupplierImportRequest(t, "original-import", original)
	_, err := ImportZTAPISupplierReconciliation(context.Background(), 7, first)
	require.NoError(t, err)
	first.DryRun = false
	_, err = ImportZTAPISupplierReconciliation(context.Background(), 7, first)
	require.NoError(t, err)

	refund := original
	refund.SupplierRecordID, refund.ReversalID, refund.OriginalRecordID = "supplier-refund", "supplier-refund", original.SupplierRecordID
	refund.DebitAmount, refund.ReversalAmount = "", original.DebitAmount
	refund.DimensionsJSON = `{"refund_mode":"full"}`
	second := ztapiSupplierImportRequest(t, "refund-import", refund)
	_, err = ImportZTAPISupplierReconciliation(context.Background(), 7, second)
	require.NoError(t, err)
	second.DryRun = false
	result, err := ImportZTAPISupplierReconciliation(context.Background(), 7, second)
	require.NoError(t, err)
	require.Len(t, result.Actions, 1)
	require.Equal(t, "submitted", result.Actions[0].Status)
	require.NotZero(t, result.Actions[0].RefundProofID)

	var proof model.ZTAPISupplierRefund
	require.NoError(t, db.First(&proof, result.Actions[0].RefundProofID).Error)
	var submission model.ZTAPISupplierRefundSubmission
	require.NoError(t, common.UnmarshalJsonStr(proof.SubmissionJSON, &submission))
	require.Equal(t, user.Id, submission.UserID)
	require.Equal(t, "full", submission.Mode)
	require.Empty(t, submission.Units)

	var persisted model.User
	require.NoError(t, db.First(&persisted, user.Id).Error)
	require.Equal(t, 1000, persisted.Quota, "refund import must await finance approval")
}
