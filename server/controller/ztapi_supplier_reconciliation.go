package controller

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const ztapiSupplierReconciliationHTTPMaxBytes = 8*service.ZTAPISupplierReconciliationMaxFileBytes + 65536

func ztapiSupplierReconciliationHTTPError(c *gin.Context, err error) {
	status, message := http.StatusServiceUnavailable, "supplier reconciliation temporarily unavailable"
	switch {
	case errors.Is(err, model.ErrZTAPISupplierRefundUnauthorized):
		status, message = http.StatusForbidden, "permission denied"
	case errors.Is(err, service.ErrZTAPISupplierReconciliationChecksum), errors.Is(err, model.ErrZTAPISupplierReconciliationInvalid):
		status, message = http.StatusBadRequest, "invalid supplier reconciliation import"
	case errors.Is(err, model.ErrZTAPISupplierReconciliationConflict), errors.Is(err, model.ErrZTAPISupplierReconciliationLineage), errors.Is(err, model.ErrZTAPISupplierReconciliationOverRefund):
		status, message = http.StatusConflict, "supplier reconciliation evidence conflicts with recorded lineage"
	case errors.Is(err, model.ErrZTAPISupplierReconciliationOrphanRefund):
		status, message = http.StatusUnprocessableEntity, "supplier reversal is missing its original debit"
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, message = http.StatusNotFound, "supplier reconciliation import not found"
	}
	c.JSON(status, gin.H{"success": false, "message": message})
}

func readZTAPISupplierReconciliationRequest(c *gin.Context) (service.ZTAPISupplierReconciliationRequest, error) {
	var request service.ZTAPISupplierReconciliationRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, ztapiSupplierReconciliationHTTPMaxBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil || len(body) == 0 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(body)) != nil || common.DecodeJsonStrict(bytes.NewReader(body), &request) != nil {
		return request, model.ErrZTAPISupplierReconciliationInvalid
	}
	if len(request.Content) > service.ZTAPISupplierReconciliationMaxFileBytes {
		return request, errZTAPISupplierReconciliationTooLarge
	}
	return request, nil
}

var errZTAPISupplierReconciliationTooLarge = errors.New("supplier reconciliation import too large")

func ztapiSupplierReconciliationProjection(result *model.ZTAPISupplierReconciliationResult) gin.H {
	entries := make([]gin.H, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, gin.H{
			"id": entry.ID, "batch_id": entry.BatchID, "supplier_record_id": entry.SupplierRecordID,
			"original_record_id": entry.OriginalRecordID, "settlement_id": entry.SettlementID, "user_id": entry.UserID,
			"attempt": entry.Attempt, "channel_id": entry.ChannelID, "charge_id": entry.ChargeID,
			"action_kind": entry.ActionKind, "match_reason": entry.MatchReason, "created_at": entry.CreatedAt,
		})
	}
	actions := make([]gin.H, 0, len(result.Actions))
	for _, action := range result.Actions {
		actions = append(actions, gin.H{
			"id": action.ID, "entry_id": action.EntryID, "kind": action.Kind, "status": action.Status,
			"billing_proof_id": action.BillingProofID, "refund_proof_id": action.RefundProofID,
			"last_error_code": action.LastErrorCode, "created_at": action.CreatedAt, "updated_at": action.UpdatedAt,
		})
	}
	batch := result.Batch
	return gin.H{
		"batch":   gin.H{"id": batch.ID, "supplier": batch.Supplier, "idempotency_key": batch.IdempotencyKey, "file_checksum": batch.FileChecksum, "format": batch.Format, "status": batch.Status, "row_count": batch.RowCount, "matched_count": batch.MatchedCount, "unmatched_count": batch.UnmatchedCount, "reversal_count": batch.ReversalCount, "created_at": batch.CreatedAt, "updated_at": batch.UpdatedAt},
		"entries": entries, "actions": actions, "matched": result.Matched, "unmatched": result.Unmatched, "reversals": result.Reversals,
	}
}

func ImportZTAPISupplierReconciliation(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceWrite) {
		return
	}
	request, err := readZTAPISupplierReconciliationRequest(c)
	if errors.Is(err, errZTAPISupplierReconciliationTooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"success": false, "message": "supplier reconciliation import exceeds the file limit"})
		return
	}
	if err != nil {
		ztapiSupplierReconciliationHTTPError(c, err)
		return
	}
	result, err := service.ImportZTAPISupplierReconciliation(c.Request.Context(), c.GetInt("id"), request)
	if err != nil {
		ztapiSupplierReconciliationHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, ztapiSupplierReconciliationProjection(result))
}

func GetZTAPISupplierReconciliationImport(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceRead) {
		return
	}
	supplier := strings.TrimSpace(c.Query("supplier"))
	idempotencyKey := strings.TrimSpace(c.Query("idempotency_key"))
	if supplier == "" || len(supplier) > 128 || idempotencyKey == "" || len(idempotencyKey) > 128 || strings.ContainsAny(supplier+idempotencyKey, "\r\n\x00") {
		ztapiSupplierReconciliationHTTPError(c, model.ErrZTAPISupplierReconciliationInvalid)
		return
	}
	result, err := model.GetZTAPISupplierReconciliationBatch(model.DB.WithContext(c.Request.Context()), supplier, idempotencyKey)
	if err != nil {
		ztapiSupplierReconciliationHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, ztapiSupplierReconciliationProjection(result))
}
