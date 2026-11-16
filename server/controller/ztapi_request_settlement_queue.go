package controller

import (
	"math"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func ztapiSettlementQueueCursor(c *gin.Context) (uint, int, bool) {
	after, err := strconv.ParseUint(c.DefaultQuery("after_id", "0"), 10, 32)
	limit, e := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || e != nil || limit < 1 || limit > 1000 {
		ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundEvidence)
		return 0, 0, false
	}
	return uint(after), limit, true
}

func ztapiSettlementHoldProjection(row model.ZTAPIRequestSettlement) gin.H {
	return gin.H{"id": row.ID, "request_id": row.RequestID, "model": row.PublicModel, "status": row.Status, "reserved_quota": row.ReservedQuota, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt}
}

func GetAdminZTAPIRequestSettlements(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceRead) {
		return
	}
	ztapiSettlementHoldList(c, true)
}

func GetSelfZTAPIRequestSettlements(c *gin.Context) {
	if c.GetInt("id") <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "authentication required"})
		return
	}
	ztapiSettlementHoldList(c, false)
}

func ztapiSettlementHoldList(c *gin.Context, admin bool) {
	after, limit, ok := ztapiSettlementQueueCursor(c)
	if !ok {
		return
	}
	var rows []model.ZTAPIRequestSettlement
	query := model.DB.WithContext(c.Request.Context()).Where("id > ? AND status IN ?", after, []string{model.ZTAPISettlementReserved, model.ZTAPISettlementPending})
	if !admin {
		query = query.Where("user_id = ?", c.GetInt("id"))
	}
	if err := query.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	next := after
	for _, row := range rows {
		item := ztapiSettlementHoldProjection(row)
		if admin {
			item["user_id"] = row.UserID
			item["dispatched"] = row.Dispatched
			item["token_reserved_quota"] = row.TokenReservedQuota
			item["missing_dimensions"] = row.MissingDimensionsJSON
		}
		items = append(items, item)
		next = row.ID
	}
	common.ApiSuccess(c, gin.H{"items": items, "next_after_id": next})
}

func GetAdminZTAPIRequestSettlement(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceRead) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundEvidence)
		return
	}
	var row model.ZTAPIRequestSettlement
	if err := model.DB.WithContext(c.Request.Context()).First(&row, uint(id)).Error; err != nil {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	var attempts []model.ZTAPIRequestAttempt
	if err := model.DB.WithContext(c.Request.Context()).Where("settlement_id = ?", row.ID).Order("attempt ASC").Limit(2).Find(&attempts).Error; err != nil {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	var health []model.ZTAPIHealthRequest
	if err := model.DB.WithContext(c.Request.Context()).Where("execution_id = ? AND user_id = ?", row.RequestID, row.UserID).Order("started_at ASC, execution_id ASC").Limit(100).Find(&health).Error; err != nil {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	evidence := make([]gin.H, 0, len(health))
	for _, h := range health {
		var admissions []types.ZTAPIHealthAttempt
		if common.UnmarshalJsonStr(h.Admissions, &admissions) != nil {
			ztapiSupplierRefundHTTPError(c, model.ErrZTAPISettlementInvalid)
			return
		}
		var events []model.ZTAPIHealthEvent
		if err := model.DB.WithContext(c.Request.Context()).Where("execution_id = ? AND request_id = ?", h.ExecutionID, h.RequestID).Limit(1).Find(&events).Error; err != nil {
			ztapiSupplierRefundHTTPError(c, err)
			return
		}
		item := gin.H{"execution_id": h.ExecutionID, "admissions": admissions, "completed": h.Completed}
		if len(events) > 0 {
			var outcome types.ZTAPIHealthOutcome
			if common.UnmarshalJsonStr(events[0].Outcome, &outcome) != nil {
				ztapiSupplierRefundHTTPError(c, model.ErrZTAPISettlementInvalid)
				return
			}
			item["final_outcome"] = outcome
		}
		evidence = append(evidence, item)
	}
	result := ztapiSettlementHoldProjection(row)
	result["user_id"] = row.UserID
	result["token_id"] = row.TokenID
	result["operation_id"] = row.OperationID
	result["price_snapshot"] = row.PriceSnapshotJSON
	result["usage"] = row.UsageJSON
	result["charge_dimensions"] = row.ChargeDimensionsJSON
	result["missing_dimensions"] = row.MissingDimensionsJSON
	result["charged_quota"] = row.ChargedQuota
	result["refunded_quota"] = row.RefundedQuota
	var charges []model.ZTAPISupplierRefundCharge
	if err := model.DB.WithContext(c.Request.Context()).Where("settlement_id = ?", row.ID).Order("attempt ASC").Limit(3).Find(&charges).Error; err != nil {
		ztapiSupplierRefundHTTPError(c, err)
		return
	}
	additional := int64(0)
	anchors := make([]gin.H, 0, len(charges))
	for _, charge := range charges {
		if len(charges) > 2 || charge.RequestID != row.RequestID || charge.UserID != row.UserID || charge.ChargedQuota < 0 || charge.ChargedQuota > math.MaxInt32 || charge.RefundedQuota < 0 || charge.RefundedQuota > charge.ChargedQuota {
			ztapiSupplierRefundHTTPError(c, model.ErrZTAPISupplierRefundConflict)
			return
		}
		if charge.BillingProofID != 0 {
			additional += charge.ChargedQuota
		}
		anchors = append(anchors, gin.H{"id": charge.ID, "attempt": charge.Attempt, "channel_id": charge.ChannelID, "charged_quota": charge.ChargedQuota, "refunded_quota": charge.RefundedQuota, "billing_proof_id": charge.BillingProofID, "original_ledger_id": charge.OriginalLedgerID, "dimensions": charge.DimensionsJSON})
	}
	result["additional_charged_quota"] = additional
	result["total_charged_quota"] = row.ChargedQuota + additional
	result["net_charged_quota"] = row.ChargedQuota + additional - row.RefundedQuota
	result["charge_anchors"] = anchors
	result["last_ledger_id"] = row.LastLedgerID
	result["dispatched"] = row.Dispatched
	result["attempt_evidence"] = evidence
	result["attempts"] = attempts
	result["attempt_evidence_available"] = len(evidence) > 0 || len(attempts) > 0
	common.ApiSuccess(c, result)
}
