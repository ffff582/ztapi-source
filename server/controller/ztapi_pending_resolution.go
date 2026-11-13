package controller

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func ResolveZTAPIPendingNoCharge(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionFinanceWrite) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	raw, ok := ztapiSupplierRefundReadBody(c, "source|proof_id|verification_reference|attempts")
	var proof model.ZTAPINoChargeProof
	if !ok || err != nil || id == 0 || common.Unmarshal(raw, &proof) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid non-billing evidence"})
		return
	}
	var wire struct {
		Attempts []map[string]json.RawMessage `json:"attempts"`
	}
	if common.Unmarshal(raw, &wire) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid attempt evidence"})
		return
	}
	for _, attempt := range wire.Attempts {
		for field := range attempt {
			switch field {
			case "attempt", "channel_id", "credential_version", "upstream_request_id", "verification_reference":
			default:
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid attempt field"})
				return
			}
		}
	}
	row, err := model.ResolveZTAPIPendingNoCharge(uint(id), c.GetInt("id"), proof)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "evidence does not authorize releasing this reservation"})
		return
	}
	common.ApiSuccess(c, gin.H{"id": row.ID, "request_id": row.RequestID, "status": row.Status, "ledger_id": row.LastLedgerID, "cache_sync_pending": row.CacheSyncPending})
}
