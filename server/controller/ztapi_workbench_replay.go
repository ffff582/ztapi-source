package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	WorkbenchReplayModel  = "zt-workbench-replay"
	workbenchReplayLimit  = 24000
	workbenchReplayInput  = 4096
	workbenchReplayOutput = 512
)

var workbenchReplayUUID = regexp.MustCompile(`^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)
var workbenchReplayHold = regexp.MustCompile(`^[a-f0-9]{64}$`)

type WorkbenchReplayReceipt struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

type workbenchReplayBody struct {
	Input  string `json:"input"`
	Output int    `json:"max_output_tokens"`
}

type workbenchReplayReservation struct {
	Task    string `json:"task"`
	Request string `json:"request"`
	Legs    []struct {
		Stage string              `json:"stage"`
		Body  workbenchReplayBody `json:"body"`
	} `json:"legs"`
}

type workbenchReplayDispatch struct {
	Task string              `json:"task"`
	Hold string              `json:"hold"`
	Body workbenchReplayBody `json:"body"`
}

// MountWorkbenchReplay is only mounted by the isolated replay harness after
// ZTAPIUserAuth. It is not registered by the application or a live relay.
func MountWorkbenchReplay(group *gin.RouterGroup, replay func(context.Context, string, int) (WorkbenchReplayReceipt, error)) {
	if replay == nil {
		return
	}
	group.POST("/reserve", func(c *gin.Context) {
		key := workbenchReplayKey(c)
		if key == nil {
			return
		}
		var req workbenchReplayReservation
		if !readWorkbenchReplay(c, &req) || !workbenchReplayUUID.MatchString(req.Task) || !workbenchReplayUUID.MatchString(req.Request) || len(req.Legs) < 1 || len(req.Legs) > 2 {
			c.Status(http.StatusBadRequest)
			return
		}
		legs := make([]model.ZTAPIWorkbenchLeg, 0, len(req.Legs))
		for index, leg := range req.Legs {
			snapshot, cap, ok := workbenchReplayQuote(leg.Body)
			if !ok {
				c.Status(http.StatusBadRequest)
				return
			}
			op := common.HashZTAPIKey(fmt.Sprintf("workbench-replay/%d/%s/%s/%d", key.UserId, req.Task, req.Request, index))
			legs = append(legs, model.ZTAPIWorkbenchLeg{Stage: leg.Stage, Settlement: model.ZTAPIRequestSettlement{
				OperationID: op, RequestID: op, UserID: key.UserId, TokenID: key.Id, PublicModel: WorkbenchReplayModel,
				PriceSnapshotJSON: snapshot, ReservedQuota: cap,
			}})
		}
		rows, err := model.BeginZTAPIWorkbenchBatch(req.Task, workbenchReplayLimit, legs)
		if err != nil {
			if errors.Is(err, model.ErrZTAPISettlementInvalid) && key.Status == common.TokenStatusExhausted {
				c.Status(http.StatusPaymentRequired)
				return
			}
			writeWorkbenchReplayError(c, err)
			return
		}
		holds := make([]gin.H, 0, len(rows))
		for _, row := range rows {
			holds = append(holds, gin.H{"id": row.OperationID, "stage": row.WorkbenchStage})
		}
		c.JSON(http.StatusOK, gin.H{"holds": holds})
	})
	group.POST("/dispatch", func(c *gin.Context) {
		key := workbenchReplayKey(c)
		if key == nil {
			return
		}
		var req workbenchReplayDispatch
		if !readWorkbenchReplay(c, &req) || !workbenchReplayUUID.MatchString(req.Task) || !workbenchReplayHold.MatchString(req.Hold) {
			c.Status(http.StatusBadRequest)
			return
		}
		snapshot, _, ok := workbenchReplayQuote(req.Body)
		if !ok {
			c.Status(http.StatusBadRequest)
			return
		}
		var row model.ZTAPIRequestSettlement
		err := model.DB.Where("operation_id = ? AND workbench_task_id = ? AND user_id = ? AND token_id = ? AND public_model = ?", req.Hold, req.Task, key.UserId, key.Id, WorkbenchReplayModel).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && row.PriceSnapshotJSON != snapshot {
			c.Status(http.StatusConflict)
			return
		}
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		if err = model.ClaimZTAPIWorkbenchDispatch(req.Task, key.UserId, key.Id, req.Hold); err != nil {
			writeWorkbenchReplayError(c, err)
			return
		}
		settled := false
		defer func() {
			if !settled {
				// If pending cannot be saved, the durable claim still prevents release and retry.
				_, _ = model.PendZTAPIRequestSettlement(req.Hold, "{}", model.ZTAPIMissingUpstreamBillingUnconfirmed)
			}
		}()
		receipt, err := replay(c.Request.Context(), req.Body.Input, req.Body.Output)
		if err != nil || receipt.InputTokens < 1 || receipt.InputTokens > len(req.Body.Input) || receipt.OutputTokens < 1 || receipt.OutputTokens > req.Body.Output || len(receipt.Text) == 0 || len(receipt.Text) > workbenchReplayInput {
			c.Status(http.StatusBadGateway)
			return
		}
		actual, err := settleWorkbenchReplayTokens(req.Hold, receipt)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		settled = true
		c.JSON(http.StatusOK, gin.H{"text": receipt.Text, "usage_quota": actual, "mode": "replay"})
	})
}

func workbenchReplayKey(c *gin.Context) *model.Token {
	owner := c.GetInt("id")
	if owner < 1 {
		c.Status(http.StatusForbidden)
		return nil
	}
	return lookupWorkbenchReplayKey(c, c.GetHeader("X-ZTAPI-Workbench-Key"), owner)
}

func lookupWorkbenchReplayKey(c *gin.Context, secret string, owner int) *model.Token {
	if !common.IsValidZTAPIKey(secret) {
		c.Status(http.StatusUnauthorized)
		return nil
	}
	key, err := model.GetTokenByHash(common.HashZTAPIKey(secret), true)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.Status(http.StatusForbidden)
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return nil
	}
	now := time.Now().Unix()
	if (owner > 0 && key.UserId != owner) || key.UnlimitedQuota || !key.ModelLimitsEnabled || key.ModelLimits != WorkbenchReplayModel ||
		key.ExpiredTime <= now || key.ExpiredTime > now+1800 || (key.Status != common.TokenStatusEnabled && key.Status != common.TokenStatusExhausted) ||
		len(key.GetIpLimits()) != 0 || key.Group != "" || key.CrossGroupRetry {
		c.Status(http.StatusForbidden)
		return nil
	}
	return key
}

func settleWorkbenchReplayTokens(hold string, receipt WorkbenchReplayReceipt) (int64, error) {
	actual := int64(receipt.InputTokens + 2*receipt.OutputTokens)
	usage := fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d}`, receipt.InputTokens, receipt.OutputTokens)
	dimensions := fmt.Sprintf(`[{"dimension":"input_tokens","units":%q,"unit_quota":"1","charged_quota":%d},{"dimension":"output_tokens","units":%q,"unit_quota":"2","charged_quota":%d}]`, strconv.Itoa(receipt.InputTokens), receipt.InputTokens, strconv.Itoa(receipt.OutputTokens), 2*receipt.OutputTokens)
	_, err := model.FinalizeZTAPIRequestSettlement(hold, actual, usage, dimensions)
	return actual, err
}

func readWorkbenchReplay(c *gin.Context, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 48*1024))
	return err == nil && common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) == nil && common.DecodeJsonStrict(bytes.NewReader(raw), dst) == nil
}

func workbenchReplayQuote(body workbenchReplayBody) (string, int64, bool) {
	if len(body.Input) < 1 || len(body.Input) > workbenchReplayInput || body.Output < 1 || body.Output > workbenchReplayOutput {
		return "", 0, false
	}
	encoded, err := common.Marshal(body)
	if err != nil {
		return "", 0, false
	}
	snapshot := fmt.Sprintf(`{"version":"offline-replay-v1","body_sha256":%q,"input_unit_quota":1,"output_unit_quota":2}`, common.HashZTAPIKey(string(encoded)))
	return snapshot, int64(len(body.Input) + 2*body.Output), true
}

func writeWorkbenchReplayError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrZTAPIWorkbenchBudget), errors.Is(err, model.ErrInsufficientTokenQuota), errors.Is(err, model.ErrBalanceLedgerNegativeBalance):
		c.Status(http.StatusPaymentRequired)
	case errors.Is(err, model.ErrZTAPISettlementConflict), errors.Is(err, model.ErrZTAPISettlementPending):
		c.Status(http.StatusConflict)
	case errors.Is(err, model.ErrZTAPISettlementInvalid):
		c.Status(http.StatusBadRequest)
	default:
		c.Status(http.StatusInternalServerError)
	}
}
