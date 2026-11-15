package controller

import (
	"bytes"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var ztapiImageSalePublisher = model.PublishZTAPIImageSale

func PublishZTAPIImageSale(c *gin.Context) {
	if c.GetInt("role") != common.RoleRootUser || c.GetInt("id") <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Root authorization required."})
		return
	}
	var request model.ZTAPIImageSalePublicationRequest
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 4097))
	if err != nil || len(raw) > 4096 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil ||
		common.DecodeJsonStrict(bytes.NewReader(raw), &request) != nil || request.Validate() != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Explicit action approval and publication IDs/version required; custom prices are not accepted."})
		return
	}
	snapshot, err := ztapiImageSalePublisher(c.GetInt("id"), request)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "No image sale publication committed."})
		return
	}
	action := request.Action
	if action == "" {
		action = model.ZTAPIImageSaleActionEnable
	}
	common.ApiSuccess(c, gin.H{"snapshot_id": snapshot.ID, "version": snapshot.ModelVersion, "action": action})
}
