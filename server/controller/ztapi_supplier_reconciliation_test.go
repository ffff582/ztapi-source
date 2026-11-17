package controller_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPISupplierReconciliationControllerTest(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	db, engine := setupBalanceLedgerControllerTest(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.Channel{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPIMediaTask{}, &model.ZTAPISupplierRefundCharge{}, &model.ZTAPIAttemptBillingReview{}, &model.ZTAPIAttemptBillingProof{}, &model.ZTAPISupplierRefund{}))
	require.NoError(t, model.MigrateZTAPISupplierReconciliation(db))
	return db, engine
}

func ztapiSupplierReconciliationHTTPBody(t *testing.T, key string, dryRun bool) string {
	t.Helper()
	billable := true
	records := []model.ZTAPISupplierLedgerRecord{{
		SupplierRecordID: "http-bill-1", RequestID: "unmatched-upstream", CredentialRef: "cred-v1", ProviderModel: "provider-model", ResourceType: "enterprise",
		OccurredAt: time.Now().UTC().Unix(), Billable: &billable, DimensionsJSON: `{"usage_semantic":"openai","usage":[{"dimension":"input_tokens","quantity":1}]}`,
		DebitAmount: "0.001", Currency: "USD", RawEvidenceHash: strings.Repeat("a", 64),
	}}
	content, err := common.Marshal(records)
	require.NoError(t, err)
	request := service.ZTAPISupplierReconciliationRequest{Supplier: "yunxin", IdempotencyKey: key, FileChecksum: fmt.Sprintf("%x", sha256.Sum256(content)), Format: "json", Content: string(content), DryRun: dryRun}
	body, err := common.Marshal(request)
	require.NoError(t, err)
	return string(body)
}

func TestZTAPISupplierReconciliationHTTPRequiresFinanceWriteAndSupportsReview(t *testing.T) {
	db, engine := setupZTAPISupplierReconciliationControllerTest(t)
	finance, financeKey := createBalanceLedgerOperator(t, db, "supplier-recon-finance", common.RoleFinanceUser)
	support, supportKey := createBalanceLedgerOperator(t, db, "supplier-recon-support", common.RoleSupportUser)
	body := ztapiSupplierReconciliationHTTPBody(t, "http-import", true)

	denied := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/supplier-reconciliation/imports", support, supportKey, body)
	assertBalanceLedgerDenied(t, denied)

	preview := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/supplier-reconciliation/imports", finance, financeKey, body)
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Contains(t, preview.Body.String(), `"status":"previewed"`)
	require.NotContains(t, preview.Body.String(), "cred-v1", "credential references are internal evidence and must not be echoed")

	review := performBalanceLedgerRequest(t, engine, http.MethodGet, "/api/admin/supplier-reconciliation/imports?supplier=yunxin&idempotency_key=http-import", finance, financeKey, "")
	require.Equal(t, http.StatusOK, review.Code, review.Body.String())
	require.Contains(t, review.Body.String(), `"status":"previewed"`)

	deniedRead := performBalanceLedgerRequest(t, engine, http.MethodGet, "/api/admin/supplier-reconciliation/imports?supplier=yunxin&idempotency_key=http-import", support, supportKey, "")
	assertBalanceLedgerDenied(t, deniedRead)
}

func TestZTAPISupplierReconciliationHTTPRequiresPreviewAndExactChecksum(t *testing.T) {
	db, engine := setupZTAPISupplierReconciliationControllerTest(t)
	finance, key := createBalanceLedgerOperator(t, db, "supplier-recon-validation", common.RoleFinanceUser)

	applyWithoutPreview := ztapiSupplierReconciliationHTTPBody(t, "missing-preview", false)
	response := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/supplier-reconciliation/imports", finance, key, applyWithoutPreview)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())

	var request service.ZTAPISupplierReconciliationRequest
	require.NoError(t, common.Unmarshal([]byte(ztapiSupplierReconciliationHTTPBody(t, "bad-checksum", true)), &request))
	request.FileChecksum = strings.Repeat("f", 64)
	body, err := common.Marshal(request)
	require.NoError(t, err)
	response = performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/supplier-reconciliation/imports", finance, key, string(body))
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), request.Content, "raw import content must not be echoed")
}

func TestZTAPISupplierReconciliationHTTPRejectsUnknownFieldsAndOversizedFiles(t *testing.T) {
	db, engine := setupZTAPISupplierReconciliationControllerTest(t)
	finance, key := createBalanceLedgerOperator(t, db, "supplier-recon-limits", common.RoleFinanceUser)

	unknown := `{"supplier":"yunxin","idempotency_key":"unknown","file_checksum":"` + strings.Repeat("a", 64) + `","format":"json","content":"[]","dry_run":true,"api_key":"SECRET"}`
	response := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/supplier-reconciliation/imports", finance, key, unknown)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())

	oversized := service.ZTAPISupplierReconciliationRequest{Supplier: "yunxin", IdempotencyKey: "oversized", FileChecksum: strings.Repeat("a", 64), Format: "json", Content: strings.Repeat("x", service.ZTAPISupplierReconciliationMaxFileBytes+1), DryRun: true}
	body, err := common.Marshal(oversized)
	require.NoError(t, err)
	response = performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/supplier-reconciliation/imports", finance, key, string(body))
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code, response.Body.String())
}
