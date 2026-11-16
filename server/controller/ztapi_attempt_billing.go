package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ztapiAttemptBillingHTTPError(c *gin.Context, err error) {
	status, message := http.StatusServiceUnavailable, "attempt billing temporarily unavailable"
	switch {
	case errors.Is(err, model.ErrZTAPISupplierRefundUnauthorized):
		status, message = http.StatusForbidden, "permission denied"
	case errors.Is(err, model.ErrZTAPIAttemptBillingInvalid), errors.Is(err, model.ErrZTAPISettlementInvalid), errors.Is(err, model.ErrZTAPISupplierRefundEvidence):
		status, message = http.StatusBadRequest, "invalid attempt billing evidence"
	case errors.Is(err, model.ErrZTAPIAttemptBillingConflict), errors.Is(err, model.ErrZTAPISettlementConflict), errors.Is(err, model.ErrZTAPISupplierRefundConflict):
		status, message = http.StatusConflict, "billing proof conflicts with recorded evidence"
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, message = http.StatusNotFound, "attempt billing evidence not found"
	}
	c.JSON(status, gin.H{"success": false, "message": message})
}

func ztapiAttemptBillingProofProjection(row model.ZTAPIAttemptBillingProof) (gin.H, error) {
	var submission model.ZTAPIAttemptBillingSubmission
	if err := common.UnmarshalJsonStr(row.SubmissionJSON, &submission); err != nil {
		return nil, err
	}
	return gin.H{"id": row.ID, "source": row.Source, "proof_id": row.ProofID, "request_id": row.RequestID, "attempt": row.Attempt, "status": row.Status, "pending_reason": row.PendingReason, "charge_id": row.ChargeID, "submission": submission, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt}, nil
}

func GetZTAPIAttemptBillingReviews(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceRead) {
		return
	}
	after, limit, ok := ztapiSettlementQueueCursor(c)
	if !ok {
		return
	}
	rows, err := model.ListZTAPIAttemptBillingReviews(c.DefaultQuery("status", "pending"), after, limit)
	if err != nil {
		ztapiAttemptBillingHTTPError(c, err)
		return
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SettlementID)
	}
	var parents []model.ZTAPIRequestSettlement
	var attempts []model.ZTAPIRequestAttempt
	if len(ids) > 0 {
		if err = model.DB.WithContext(c.Request.Context()).Where("id IN ?", ids).Find(&parents).Error; err == nil {
			err = model.DB.WithContext(c.Request.Context()).Where("settlement_id IN ?", ids).Find(&attempts).Error
		}
		if err != nil {
			ztapiAttemptBillingHTTPError(c, err)
			return
		}
	}
	owners := make(map[uint]model.ZTAPIRequestSettlement, len(parents))
	for _, parent := range parents {
		owners[parent.ID] = parent
	}
	type attemptKey struct{ settlement, attempt uint }
	wires := make(map[attemptKey]model.ZTAPIRequestAttempt, len(attempts))
	for _, attempt := range attempts {
		wires[attemptKey{attempt.SettlementID, uint(attempt.Attempt)}] = attempt
	}
	items := make([]gin.H, 0, len(rows))
	next := after
	for _, row := range rows {
		parent, ownerFound := owners[row.SettlementID]
		attempt, wireFound := wires[attemptKey{row.SettlementID, uint(row.Attempt)}]
		if !ownerFound || !wireFound || parent.RequestID != row.RequestID {
			ztapiAttemptBillingHTTPError(c, model.ErrZTAPIAttemptBillingConflict)
			return
		}
		items = append(items, gin.H{"id": row.ID, "settlement_id": row.SettlementID, "request_id": row.RequestID, "attempt": row.Attempt, "status": row.Status, "pending_reason": row.PendingReason, "applied_proof_id": row.AppliedProofID, "charge_id": row.ChargeID, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt, "user_id": parent.UserID, "model": parent.PublicModel, "channel_id": attempt.ChannelID, "credential_version": attempt.CredentialVersion, "upstream_request_id": attempt.UpstreamRequestID})
		next = row.ID
	}
	common.ApiSuccess(c, gin.H{"items": items, "next_after_id": next})
}

func GetZTAPIAttemptBillingProofs(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceRead) {
		return
	}
	after, limit, ok := ztapiSettlementQueueCursor(c)
	if !ok {
		return
	}
	status := c.DefaultQuery("status", "pending")
	if status != "pending" && status != "approved" && status != "applied" && status != "completed" && status != "all" {
		ztapiAttemptBillingHTTPError(c, model.ErrZTAPIAttemptBillingInvalid)
		return
	}
	query := model.DB.WithContext(c.Request.Context()).Where("id > ?", after)
	if status != "all" {
		query = query.Where("status = ?", status)
	}
	var rows []model.ZTAPIAttemptBillingProof
	if err := query.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		ztapiAttemptBillingHTTPError(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	next := after
	for _, row := range rows {
		item, err := ztapiAttemptBillingProofProjection(row)
		if err != nil {
			ztapiAttemptBillingHTTPError(c, err)
			return
		}
		items, next = append(items, item), row.ID
	}
	common.ApiSuccess(c, gin.H{"items": items, "next_after_id": next})
}

func SubmitZTAPIAttemptBilling(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceWrite) {
		return
	}
	body, ok := ztapiSupplierRefundReadBody(c, "source|proof_id|request_id|user_id|attempt|channel_id|credential_version|upstream_request_id|upstream_task_id|upstream_bill_id|kind|usage_semantic|usage|selected_rule_id|price_rule_ids|raw_usage_json|evidence_reference|distinct_usage_reference")
	var input model.ZTAPIAttemptBillingSubmission
	if !ok || common.Unmarshal(body, &input) != nil || (input.Kind != "billed" && input.Kind != "nocharge") {
		ztapiAttemptBillingHTTPError(c, model.ErrZTAPIAttemptBillingInvalid)
		return
	}
	var raw struct {
		Usage []map[string]json.RawMessage `json:"usage"`
	}
	if common.Unmarshal(body, &raw) != nil {
		ztapiAttemptBillingHTTPError(c, model.ErrZTAPIAttemptBillingInvalid)
		return
	}
	for _, line := range raw.Usage {
		for field := range line {
			if field != "dimension" && field != "quantity" {
				ztapiAttemptBillingHTTPError(c, model.ErrZTAPIAttemptBillingInvalid)
				return
			}
		}
	}
	row, err := model.SubmitZTAPIAttemptBilling(input)
	if err != nil {
		ztapiAttemptBillingHTTPError(c, err)
		return
	}
	item, err := ztapiAttemptBillingProofProjection(*row)
	if err != nil {
		ztapiAttemptBillingHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, item)
}

func ApproveZTAPIAttemptBilling(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceWrite) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		ztapiAttemptBillingHTTPError(c, model.ErrZTAPIAttemptBillingInvalid)
		return
	}
	body, ok := ztapiSupplierRefundReadBody(c, "verification_reference")
	var input struct {
		VerificationReference string `json:"verification_reference"`
	}
	if !ok || common.Unmarshal(body, &input) != nil || strings.TrimSpace(input.VerificationReference) == "" || len(input.VerificationReference) > 512 {
		ztapiAttemptBillingHTTPError(c, model.ErrZTAPIAttemptBillingInvalid)
		return
	}
	err = model.ApproveZTAPIAttemptBilling(uint(id), c.GetInt("id"), input.VerificationReference, service.PriceZTAPIAttemptBilling)
	if err == nil {
		err = model.ProcessZTAPIAttemptBilling(uint(id), service.EnqueueZTAPIAttemptBillingLogTx)
	}
	pending := errors.Is(err, model.ErrZTAPIAttemptBillingPending) || errors.Is(err, model.ErrBalanceLedgerCacheSync)
	if err != nil && !pending {
		ztapiAttemptBillingHTTPError(c, err)
		return
	}
	var row model.ZTAPIAttemptBillingProof
	if err = model.DB.WithContext(c.Request.Context()).First(&row, uint(id)).Error; err != nil {
		ztapiAttemptBillingHTTPError(c, err)
		return
	}
	item, err := ztapiAttemptBillingProofProjection(row)
	if err != nil {
		ztapiAttemptBillingHTTPError(c, err)
		return
	}
	if pending || row.Status != "completed" {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "data": item})
		return
	}
	common.ApiSuccess(c, item)
}
