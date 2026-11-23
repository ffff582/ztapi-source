package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const workbenchReplayStreamMaxBody = 196608
const workbenchReplayStreamMaxBytes = 1024 * 1024

type workbenchReplayEnvelope struct {
	Version string `json:"version"`
	Bytes   int    `json:"bytes"`
	Output  int    `json:"output"`
}

func (e workbenchReplayEnvelope) valid() bool {
	return e.Version == "offline-responses-v1" && e.Bytes > 0 && e.Bytes <= workbenchReplayStreamMaxBody && e.Output > 0 && e.Output <= 4096
}

func (e workbenchReplayEnvelope) quota() int64 { return int64(e.Bytes + 2*e.Output) }

// MountWorkbenchStreamReplay implements the R35 wire shape with a synthetic
// model only. It is not registered by SetApiRouter or SetRelayRouter.
func MountWorkbenchStreamReplay(group *gin.RouterGroup, provider func(context.Context, []byte) (io.ReadCloser, WorkbenchReplayReceipt, error)) {
	if provider == nil {
		return
	}
	group.POST("/__workbench_budget/reserve", reserveWorkbenchStreamReplay)
	group.POST("/__workbench_budget/release", releaseWorkbenchStreamReplay)
	group.POST("/v1/responses", func(c *gin.Context) { dispatchWorkbenchStreamReplay(c, provider) })
}

func reserveWorkbenchStreamReplay(c *gin.Context) {
	key := workbenchStreamReplayKey(c)
	if key == nil {
		return
	}
	var req struct {
		Task    string `json:"task"`
		Request string `json:"request"`
		Model   string `json:"model"`
		Legs    []struct {
			Stage  string `json:"stage"`
			Bytes  int    `json:"bytes"`
			Output int    `json:"output"`
		} `json:"legs"`
	}
	if _, ok := readWorkbenchStreamReplay(c, &req, 8192); !ok {
		return
	}
	if !workbenchReplayUUID.MatchString(req.Task) || !workbenchReplayUUID.MatchString(req.Request) || len(req.Legs) < 1 || len(req.Legs) > 2 {
		c.Status(http.StatusBadRequest)
		return
	}
	if req.Model != WorkbenchReplayModel {
		c.Status(http.StatusForbidden)
		return
	}
	legs := make([]model.ZTAPIWorkbenchLeg, 0, len(req.Legs))
	for i, leg := range req.Legs {
		envelope := workbenchReplayEnvelope{Version: "offline-responses-v1", Bytes: leg.Bytes, Output: leg.Output}
		if !envelope.valid() {
			c.Status(http.StatusBadRequest)
			return
		}
		snapshot, err := common.Marshal(envelope)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		name := fmt.Sprintf("workbench-stream-replay/%d/%s/%s/%d", key.UserId, req.Task, req.Request, i)
		hold := uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
		legs = append(legs, model.ZTAPIWorkbenchLeg{Stage: leg.Stage, Settlement: model.ZTAPIRequestSettlement{
			OperationID: hold, RequestID: hold, UserID: key.UserId, TokenID: key.Id, PublicModel: req.Model,
			PriceSnapshotJSON: string(snapshot), ReservedQuota: envelope.quota(),
		}})
	}
	rows, err := model.BeginZTAPIWorkbenchBatch(req.Task, workbenchReplayLimit, legs)
	if err != nil {
		if errors.Is(err, model.ErrZTAPISettlementInvalid) && key.Status == common.TokenStatusExhausted {
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

func releaseWorkbenchStreamReplay(c *gin.Context) {
	key := workbenchStreamReplayKey(c)
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
	if _, _, ok := readWorkbenchStreamHold(c, key, req.Task, req.Hold); !ok {
		return
	}
	if _, err := model.ReleaseZTAPIRequestSettlement(req.Hold); err != nil {
		writeWorkbenchReplayError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"released": true})
}

func dispatchWorkbenchStreamReplay(c *gin.Context, provider func(context.Context, []byte) (io.ReadCloser, WorkbenchReplayReceipt, error)) {
	key := workbenchStreamReplayKey(c)
	if key == nil {
		return
	}
	task, hold := c.GetHeader("X-ZTAPI-Task"), c.GetHeader("X-ZTAPI-Hold")
	if len(c.Request.Header.Values("X-ZTAPI-Task")) != 1 || len(c.Request.Header.Values("X-ZTAPI-Hold")) != 1 {
		c.Status(http.StatusBadRequest)
		return
	}
	row, envelope, ok := readWorkbenchStreamHold(c, key, task, hold)
	if !ok {
		return
	}
	if row.Dispatched || row.Status != model.ZTAPISettlementReserved {
		c.Status(http.StatusConflict)
		return
	}
	var req struct {
		Model        string            `json:"model"`
		Input        []json.RawMessage `json:"input"`
		Instructions string            `json:"instructions"`
		Stream       bool              `json:"stream"`
		Store        *bool             `json:"store"`
		Output       int               `json:"max_output_tokens"`
	}
	body, ok := readWorkbenchStreamReplay(c, &req, workbenchReplayStreamMaxBody)
	if !ok {
		return
	}
	if len(body) > envelope.Bytes || req.Output > envelope.Output {
		c.Status(http.StatusPaymentRequired)
		return
	}
	if req.Model != row.PublicModel || !req.Stream || req.Store == nil || *req.Store || req.Output < 1 || len(req.Input) < 1 || len(req.Input) > 500 {
		c.Status(http.StatusBadRequest)
		return
	}
	for _, item := range req.Input {
		if common.GetJsonType(item) != "object" {
			c.Status(http.StatusBadRequest)
			return
		}
	}
	if c.Request.Context().Err() != nil {
		c.Status(http.StatusRequestTimeout)
		return
	}
	if err := model.ClaimZTAPIWorkbenchDispatch(task, key.UserId, key.Id, hold); err != nil {
		writeWorkbenchReplayError(c, err)
		return
	}
	settled := false
	defer func() {
		if !settled {
			_, _ = model.PendZTAPIRequestSettlement(hold, "{}", model.ZTAPIMissingUpstreamBillingUnconfirmed)
		}
	}()
	stream, receipt, err := provider(c.Request.Context(), body)
	if stream != nil {
		defer stream.Close()
	}
	if err != nil || stream == nil {
		c.Status(http.StatusBadGateway)
		return
	}
	sse, err := io.ReadAll(io.LimitReader(stream, workbenchReplayStreamMaxBytes+1))
	if err != nil || len(sse) > workbenchReplayStreamMaxBytes || !validWorkbenchStreamReceipt(sse, row.PublicModel, receipt, len(body), req.Output) {
		c.Status(http.StatusBadGateway)
		return
	}
	if _, err = settleWorkbenchReplayTokens(hold, receipt); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	settled = true
	c.Header("Cache-Control", "no-store")
	c.Header("X-ZTAPI-Budget-Hold", hold)
	c.Data(http.StatusOK, "text/event-stream", sse)
}

func workbenchStreamReplayKey(c *gin.Context) *model.Token {
	scheme, secret, ok := strings.Cut(c.GetHeader("Authorization"), " ")
	if !ok || scheme != "Bearer" || len(c.Request.Header.Values("Authorization")) != 1 {
		c.Status(http.StatusUnauthorized)
		return nil
	}
	return lookupWorkbenchReplayKey(c, secret, 0)
}

func readWorkbenchStreamReplay(c *gin.Context, dst any, limit int64) ([]byte, bool) {
	if c.GetHeader("Content-Type") != "application/json" {
		c.Status(http.StatusUnsupportedMediaType)
		return nil, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, limit))
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		c.Status(http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if err != nil || !utf8.Valid(raw) || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), dst) != nil {
		c.Status(http.StatusBadRequest)
		return nil, false
	}
	return raw, true
}

func readWorkbenchStreamHold(c *gin.Context, key *model.Token, task, hold string) (model.ZTAPIRequestSettlement, workbenchReplayEnvelope, bool) {
	var row model.ZTAPIRequestSettlement
	var envelope workbenchReplayEnvelope
	if !workbenchReplayUUID.MatchString(task) || !workbenchReplayUUID.MatchString(hold) {
		c.Status(http.StatusBadRequest)
		return row, envelope, false
	}
	err := model.DB.Where("operation_id = ? AND workbench_task_id = ? AND user_id = ? AND token_id = ? AND public_model = ?", hold, task, key.UserId, key.Id, WorkbenchReplayModel).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.Status(http.StatusConflict)
		return row, envelope, false
	}
	if err != nil || common.DecodeJsonStrict(strings.NewReader(row.PriceSnapshotJSON), &envelope) != nil || !envelope.valid() || row.ReservedQuota != envelope.quota() {
		c.Status(http.StatusInternalServerError)
		return row, envelope, false
	}
	return row, envelope, true
}

// The replay accepts one JSON data line per frame, like the local Responses
// fixture. It buffers the stream so incomplete evidence cannot look successful.
func validWorkbenchStreamReceipt(sse []byte, publicModel string, receipt WorkbenchReplayReceipt, inputCap, outputCap int) bool {
	if !utf8.Valid(sse) || receipt.InputTokens < 1 || receipt.InputTokens > inputCap || receipt.OutputTokens < 1 || receipt.OutputTokens > outputCap {
		return false
	}
	frames := strings.Split(strings.ReplaceAll(string(sse), "\r\n", "\n"), "\n\n")
	if len(frames) < 3 || frames[len(frames)-1] != "" {
		return false
	}
	var id string
	for i, frame := range frames[:len(frames)-1] {
		lines := strings.Split(frame, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			return false
		}
		data := strings.TrimPrefix(lines[1], "data: ")
		var event struct {
			Type     string `json:"type"`
			Response struct {
				ID     string          `json:"id"`
				Model  string          `json:"model"`
				Status string          `json:"status"`
				Usage  json.RawMessage `json:"usage"`
			} `json:"response"`
		}
		if common.RejectDuplicateJsonObjectMembers(strings.NewReader(data)) != nil || common.UnmarshalJsonStr(data, &event) != nil || event.Type != strings.TrimPrefix(lines[0], "event: ") {
			return false
		}
		if i == 0 {
			if event.Type != "response.created" || event.Response.ID == "" {
				return false
			}
			id = event.Response.ID
			continue
		}
		if i == len(frames)-2 {
			var usage struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
				Total  int `json:"total_tokens"`
			}
			return event.Type == "response.completed" && event.Response.ID == id && event.Response.Model == publicModel && event.Response.Status == "completed" &&
				common.DecodeJsonStrict(bytes.NewReader(event.Response.Usage), &usage) == nil && usage.Input == receipt.InputTokens && usage.Output == receipt.OutputTokens && usage.Total == usage.Input+usage.Output
		}
		switch event.Type {
		case "response.output_item.added", "response.output_text.delta", "response.output_item.done", "response.content_part.added", "response.content_part.done", "response.output_text.done":
		default:
			return false
		}
	}
	return false
}
