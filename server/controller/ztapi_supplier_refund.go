package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ztapiSupplierRefundPermission(c *gin.Context, permission common.AdminPermission) bool {
	if c.GetInt("id") <= 0 || !common.HasAdminPermission(c.GetInt("role"), permission) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
		return false
	}
	var operator model.User
	if err := model.DB.WithContext(c.Request.Context()).First(&operator, c.GetInt("id")).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
		} else {
			ztapiSupplierRefundHTTPError(c, err)
		}
		return false
	}
	if operator.Status != common.UserStatusEnabled || !common.HasAdminPermission(operator.Role, permission) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
		return false
	}
	return true
}

func ztapiSupplierRefundHTTPError(c *gin.Context, err error) {
	status := http.StatusServiceUnavailable
	message := "supplier refund state temporarily unavailable"
	switch {
	case errors.Is(err, model.ErrZTAPISupplierRefundUnauthorized):
		status = http.StatusForbidden
		message = "permission denied"
	case errors.Is(err, model.ErrZTAPISupplierRefundConflict):
		status = http.StatusConflict
		message = "refund proof identity conflicts with existing evidence"
	case errors.Is(err, model.ErrZTAPISupplierRefundEvidence):
		status = http.StatusBadRequest
		message = "invalid supplier refund evidence"
	case errors.Is(err, gorm.ErrRecordNotFound):
		status = http.StatusNotFound
		message = "supplier refund not found"
	}
	c.JSON(status, gin.H{"success": false, "message": message})
}

func ztapiSupplierRefundReadBody(c *gin.Context, allowed string) ([]byte, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 65536)
	body, err := io.ReadAll(c.Request.Body)
	var fields map[string]json.RawMessage
	if err != nil || common.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, false
	}
	for field := range fields {
		if !strings.Contains("|"+allowed+"|", "|"+field+"|") {
			return nil, false
		}
	}
	return body, true
}

func ztapiSupplierRefundProjection(row model.ZTAPISupplierRefund) gin.H {
	var input model.ZTAPISupplierRefundSubmission
	_ = common.UnmarshalJsonStr(row.SubmissionJSON, &input)
	return gin.H{"id": row.ID, "source": row.Source, "proof_id": row.ProofID, "request_id": row.RequestID, "status": row.Status, "pending_reason": row.PendingReason, "charge_id": row.ChargeID, "ledger_id": row.LedgerID, "refunded_quota": row.RefundedQuota, "token_refunded_quota": row.TokenRefundedQuota, "submission": input, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt}
}

func GetZTAPISupplierRefunds(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceRead) {
		return
	}
	after, err := strconv.ParseUint(c.DefaultQuery("after_id", "0"), 10, 32)
	limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limitErr != nil {
		ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundEvidence)
		return
	}
	rows, err := model.ListZTAPISupplierRefunds(c.DefaultQuery("status", "pending"), uint(after), limit)
	if err != nil {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	next := uint(after)
	for _, row := range rows {
		items = append(items, ztapiSupplierRefundProjection(row))
		next = row.ID
	}
	common.ApiSuccess(c, gin.H{"items": items, "next_after_id": next})
}

func SubmitZTAPISupplierRefund(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceWrite) {
		return
	}
	body, ok := ztapiSupplierRefundReadBody(c, "source|proof_id|request_id|user_id|attempt|channel_id|credential_version|upstream_request_id|upstream_task_id|upstream_bill_id|mode|units|evidence_reference")
	var input model.ZTAPISupplierRefundSubmission
	if !ok || common.Unmarshal(body, &input) != nil || (input.Mode != "full" && input.Mode != "partial") {
		ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundEvidence)
		return
	}
	var raw struct {
		Units []map[string]json.RawMessage `json:"units"`
	}
	if common.Unmarshal(body, &raw) != nil {
		ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundEvidence)
		return
	}
	for _, line := range raw.Units {
		for key := range line {
			if key != "dimension" && key != "units" {
				ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundEvidence)
				return
			}
		}
	}
	row, err := model.SubmitZTAPISupplierRefund(input)
	if err != nil {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, ztapiSupplierRefundProjection(*row))
}

func ApproveZTAPISupplierRefund(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceWrite) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundEvidence)
		return
	}
	body, ok := ztapiSupplierRefundReadBody(c, "verification_reference")
	var request struct {
		VerificationReference string `json:"verification_reference"`
	}
	if !ok || common.Unmarshal(body, &request) != nil || strings.TrimSpace(request.VerificationReference) == "" || len(request.VerificationReference) > 512 {
		ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundEvidence)
		return
	}
	if err := model.ApproveZTAPISupplierRefund(uint(id), c.GetInt("id"), request.VerificationReference); err != nil {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	err = model.ProcessZTAPISupplierRefund(uint(id))
	if err != nil && !errors.Is(err, model.ErrZTAPISupplierRefundPending) && !errors.Is(err, model.ErrBalanceLedgerCacheSync) {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	var row model.ZTAPISupplierRefund
	if lookupErr := model.DB.WithContext(c.Request.Context()).First(&row, uint(id)).Error; lookupErr != nil {
		ztapiSupplierRefundHTTPError(c, lookupErr)
		return
	}
	if err != nil {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "data": ztapiSupplierRefundProjection(row)})
		return
	}
	common.ApiSuccess(c, ztapiSupplierRefundProjection(row))
}
