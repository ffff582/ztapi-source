package controller

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type workbenchReserveRequest struct {
	Task    string `json:"task"`
	Request string `json:"request"`
	Model   string `json:"model"`
	Legs    []struct {
		Stage  string `json:"stage"`
		Wire   string `json:"wire,omitempty"`
		Bytes  int    `json:"bytes,omitempty"`
		Output int    `json:"output,omitempty"`
	} `json:"legs"`
}

func workbenchNativeKey(c *gin.Context) *model.Token {
	key, err := model.GetTokenByIds(c.GetInt("token_id"), c.GetInt("id"))
	if err != nil {
		c.Status(http.StatusUnauthorized)
		return nil
	}
	now := time.Now()
	if key.UnlimitedQuota || !key.ModelLimitsEnabled || key.ExpiredTime <= now.Unix() || key.ExpiredTime > now.Add(30*time.Minute).Unix() ||
		(key.Status != common.TokenStatusEnabled && !(key.Status == common.TokenStatusExhausted && key.RemainQuota == 0)) {
		c.Status(http.StatusForbidden)
		return nil
	}
	return key
}

func ReserveZTAPIWorkbench(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := workbenchNativeKey(c)
		if key == nil {
			return
		}
		var req workbenchReserveRequest
		if _, ok := readWorkbenchStreamReplay(c, &req, 6*service.ZTAPIWorkbenchMaxWire+8192); !ok {
			return
		}
		if !workbenchReplayUUID.MatchString(req.Task) || !workbenchReplayUUID.MatchString(req.Request) || len(req.Legs) < 1 || len(req.Legs) > 2 {
			c.Status(http.StatusBadRequest)
			return
		}
		if req.Model != key.ModelLimits {
			c.Status(http.StatusForbidden)
			return
		}
		snapshot, err := middleware.LoadZTAPIWorkbenchPublication(c, req.Model)
		if err != nil {
			c.Status(http.StatusConflict)
			return
		}
		price, err := common.Marshal(snapshot)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		legs := make([]model.ZTAPIWorkbenchLeg, 0, len(req.Legs))
		for i, leg := range req.Legs {
			bytes, output, hash := leg.Bytes, leg.Output, ""
			if leg.Wire != "" && bytes == 0 && output == 0 {
				output, err = service.ZTAPIWorkbenchWireOutput([]byte(leg.Wire), req.Model)
				if err != nil {
					writeWorkbenchReplayError(c, err)
					return
				}
				bytes, hash = len(leg.Wire), common.HashZTAPIKey(leg.Wire)
			} else if leg.Wire != "" || leg.Stage != "continuation" {
				c.Status(http.StatusBadRequest)
				return
			}
			if bytes < 1 || bytes > service.ZTAPIWorkbenchMaxWire {
				c.Status(http.StatusBadRequest)
				return
			}
			outbound := min(service.ZTAPIWorkbenchMaxWire, 2*bytes+512)
			quota, err := service.ZTAPIWorkbenchQuote(snapshot, outbound, output)
			if err != nil {
				writeWorkbenchReplayError(c, err)
				return
			}
			name := fmt.Sprintf("workbench-native-v1/%d/%s/%s/%d", key.UserId, req.Task, req.Request, i)
			hold := uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
			legs = append(legs, model.ZTAPIWorkbenchLeg{Stage: leg.Stage,
				Native: &model.ZTAPIWorkbenchNativeBounds{BodySHA256: hash, OutboundBytes: outbound, MaxOutput: output},
				Settlement: model.ZTAPIRequestSettlement{OperationID: hold, RequestID: hold, UserID: key.UserId, TokenID: key.Id,
					PublicModel: req.Model, PriceSnapshotJSON: string(price), ReservedQuota: quota},
			})
		}
		rows, err := model.BeginZTAPIWorkbenchBatch(req.Task, limit, legs, service.ZTAPIWorkbenchPilotPolicies(c)...)
		if err != nil {
			if errors.Is(err, model.ErrZTAPIWorkbenchTaskLimit) {
				c.Header("X-ZTAPI-Workbench-Error", "task-limit")
				c.Status(http.StatusPaymentRequired)
			} else if key.Status == common.TokenStatusExhausted {
				c.Status(http.StatusPaymentRequired)
			} else {
				writeWorkbenchReplayError(c, err)
			}
			return
		}
		holds := make([]gin.H, 0, len(rows))
		for _, row := range rows {
			holds = append(holds, gin.H{"id": row.OperationID, "stage": row.WorkbenchStage})
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"holds": holds})
	}
}

func ReleaseZTAPIWorkbench(c *gin.Context) {
	key := workbenchNativeKey(c)
	if key == nil {
		return
	}
	var req struct {
		Task string `json:"task"`
		Hold string `json:"hold"`
	}
	if _, ok := readWorkbenchStreamReplay(c, &req, 8192); !ok {
		return
	}
	if !workbenchReplayUUID.MatchString(req.Task) || !workbenchReplayUUID.MatchString(req.Hold) {
		c.Status(http.StatusBadRequest)
		return
	}
	var row model.ZTAPIRequestSettlement
	if err := model.DB.Where("operation_id = ? AND workbench_task_id = ? AND user_id = ? AND token_id = ? AND workbench_outbound_max > 0", req.Hold, req.Task, key.UserId, key.Id).Take(&row).Error; err != nil {
		c.Status(http.StatusConflict)
		return
	}
	if _, err := model.ReleaseZTAPIRequestSettlement(req.Hold); err != nil {
		writeWorkbenchReplayError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"released": true})
}
