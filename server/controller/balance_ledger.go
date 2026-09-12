package controller

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type balanceAdjustmentRequest struct {
	Delta          int64  `json:"delta"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

type balanceAdjustmentResponse struct {
	model.BalanceLedger
	Applied bool `json:"applied"`
}

func CreateBalanceAdjustment(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		common.ApiErrorMsg(c, "invalid user id")
		return
	}
	var request balanceAdjustmentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}

	entry, applied, err := model.AdjustUserBalance(model.BalanceAdjustment{
		UserID:         userID,
		OperatorID:     c.GetInt("id"),
		Delta:          request.Delta,
		Reason:         request.Reason,
		IdempotencyKey: request.IdempotencyKey,
		RequestID:      c.GetString(common.RequestIdKey),
		SourceType:     model.BalanceLedgerSourceAdmin,
	})
	if err != nil {
		if entry != nil && errors.Is(err, model.ErrBalanceLedgerCacheSync) {
			recordBalanceAdjustmentAudit(c, entry, "committed_cache_sync_failed")
		}
		common.ApiError(c, err)
		return
	}
	result := "duplicate"
	if applied {
		result = "applied"
	}
	recordBalanceAdjustmentAudit(c, entry, result)
	common.ApiSuccess(c, balanceAdjustmentResponse{BalanceLedger: *entry, Applied: applied})
}

func recordBalanceAdjustmentAudit(c *gin.Context, entry *model.BalanceLedger, result string) {
	recordManageAuditFor(c, entry.UserID, "balance.adjustment", map[string]interface{}{
		"target_user_id": entry.UserID,
		"delta":          entry.Delta,
		"reason":         entry.Reason,
		"ledger_id":      entry.ID,
		"result":         result,
	})
}

func GetAllBalanceLedger(c *gin.Context) {
	writeBalanceLedgerPage(c, nil)
}

func GetUserBalanceLedger(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		common.ApiErrorMsg(c, "invalid user id")
		return
	}
	writeBalanceLedgerPage(c, &userID)
}

func writeBalanceLedgerPage(c *gin.Context, userID *int) {
	pageInfo := common.GetPageQuery(c)
	entries, total, err := model.GetBalanceLedger(userID, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(entries)
	common.ApiSuccess(c, pageInfo)
}
