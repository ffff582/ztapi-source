package controller

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ztapiHealthVerificationProjection struct {
	ID             string `json:"id"`
	ChannelID      int    `json:"channel_id"`
	EntryProtocol  string `json:"entry_protocol"`
	Protocol       string `json:"protocol"`
	Stream         bool   `json:"stream"`
	Generation     uint64 `json:"generation"`
	SourceEventID  int64  `json:"source_event_id"`
	State          string `json:"state"`
	Attempts       int    `json:"attempts"`
	ProbeRequestID string `json:"probe_request_id"`
	Result         string `json:"result"`
	CreatedAt      int64  `json:"created_at"`
	CompletedAt    int64  `json:"completed_at"`
	CancelledAt    int64  `json:"cancelled_at"`
}

type ztapiHealthRouteProjection struct {
	ID                  int64  `json:"id"`
	ChannelID           int    `json:"channel_id"`
	EntryProtocol       string `json:"entry_protocol"`
	Protocol            string `json:"protocol"`
	Stream              bool   `json:"stream"`
	Generation          uint64 `json:"generation"`
	IndependentFailures int64  `json:"independent_failures"`
	Open                bool   `json:"open"`
	LastProbeRequestID  string `json:"last_probe_request_id"`
	LastResult          string `json:"last_result"`
	OpenedAt            int64  `json:"opened_at"`
	UpdatedAt           int64  `json:"updated_at"`
}

type ztapiHealthEventProjection struct {
	ID                  int64  `json:"id"`
	RequestID           string `json:"request_id"`
	ModelID             int    `json:"model_id"`
	ConfigVersion       uint64 `json:"config_version"`
	Generation          uint64 `json:"generation"`
	PublicModel         string `json:"public_model"`
	Modality            string `json:"modality"`
	Operation           string `json:"operation"`
	EntryProtocol       string `json:"entry_protocol"`
	CompletionSequence  uint64 `json:"completion_sequence"`
	CompletedAt         int64  `json:"completed_at"`
	Stream              bool   `json:"stream"`
	Source              string `json:"source"`
	Result              string `json:"result"`
	Reason              string `json:"reason"`
	Counted             bool   `json:"counted"`
	StaleGeneration     bool   `json:"stale_generation"`
	ChannelID           int    `json:"channel_id"`
	UpstreamProtocol    string `json:"upstream_protocol"`
	HTTPStatus          int    `json:"http_status"`
	UpstreamRequestID   string `json:"upstream_request_id"`
	UpstreamTaskID      string `json:"upstream_task_id"`
	ProviderErrorCode   string `json:"provider_error_code"`
	LatencyMilliseconds int64  `json:"latency_milliseconds"`
	ResultValid         bool   `json:"result_valid"`
}

func ztapiHealthAdminModel(c *gin.Context, permission common.AdminPermission) (int, bool) {
	if c.GetInt("id") <= 0 || !common.HasAdminPermission(c.GetInt("role"), permission) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
		return 0, false
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid model ID"})
		return 0, false
	}
	var count int64
	if err = model.DB.WithContext(c.Request.Context()).Model(&model.ZTAPIModelConfig{}).Where("id = ?", id).Count(&count).Error; err != nil {
		ztapiHealthAdminError(c, err)
		return 0, false
	}
	if count != 1 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "model not found"})
		return 0, false
	}
	return id, true
}

func ztapiHealthAdminError(c *gin.Context, err error) {
	common.SysError("ztapi health admin operation failed: " + err.Error())
	c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "health state temporarily unavailable"})
}

func ztapiHealthTestAlertResponse(c *gin.Context, job *model.ZTAPIHealthOutbox, err error) {
	switch {
	case errors.Is(err, model.ErrZTAPIHealthTestAlertInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "operation_id must be a nonzero UUID"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "test alert not found"})
	case err != nil:
		ztapiHealthAdminError(c, err)
	default:
		common.ApiSuccess(c, gin.H{"id": job.ID, "operation_id": strings.TrimPrefix(job.DedupKey, "alert-test:"), "kind": job.Kind,
			"status": job.Status, "attempts": job.Attempts, "last_error": job.LastError, "delivery_receipt": job.DeliveryReceipt,
			"next_attempt_at": job.NextAttemptAt, "created_at": job.CreatedAt, "delivered_at": job.DeliveredAt})
	}
}

func CreateZTAPIHealthTestAlert(c *gin.Context) {
	if c.GetInt("id") <= 0 || !common.HasAdminPermission(c.GetInt("role"), common.PermissionModelWrite) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
		return
	}
	var req struct {
		OperationID string `json:"operation_id"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil || common.Unmarshal(body, &req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "operation_id must be a nonzero UUID"})
		return
	}
	job, err := model.NewZTAPIHealthStore(model.DB).EnqueueTestAlert(c.Request.Context(), req.OperationID, c.GetInt("id"))
	ztapiHealthTestAlertResponse(c, job, err)
}

func GetZTAPIHealthTestAlert(c *gin.Context) {
	if c.GetInt("id") <= 0 || !common.HasAdminPermission(c.GetInt("role"), common.PermissionModelRead) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
		return
	}
	job, err := model.NewZTAPIHealthStore(model.DB).GetTestAlert(c.Request.Context(), c.Param("operation_id"))
	ztapiHealthTestAlertResponse(c, job, err)
}

func GetZTAPIHealthWorkerStatus(c *gin.Context) {
	if c.GetInt("id") <= 0 || !common.HasAdminPermission(c.GetInt("role"), common.PermissionModelRead) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "permission denied"})
		return
	}
	store := &model.ZTAPIProbeStore{DB: model.DB}
	statuses, err := store.Statuses(c.Request.Context())
	if err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	budget, err := store.Budget(c.Request.Context())
	var projection any
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		ztapiHealthAdminError(c, err)
		return
	}
	if err == nil {
		projection = gin.H{"allocated_nano_usd": budget.AllocatedNanoUSD, "accounted_nano_usd": budget.AccountedNanoUSD}
	}
	common.ApiSuccess(c, gin.H{"enabled": model.ZTAPIHealthEnabled(), "worker_status": statuses, "probe_budget": projection})
}

// The read projection excludes lease tokens and does not create a health state.
func GetZTAPIModelHealth(c *gin.Context) {
	id, ok := ztapiHealthAdminModel(c, common.PermissionModelRead)
	if !ok {
		return
	}
	after, err := strconv.ParseUint(c.DefaultQuery("after_sequence", "0"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid event cursor"})
		return
	}
	store := model.NewZTAPIHealthStore(model.DB)
	ctx := c.Request.Context()
	state, err := store.GetState(ctx, id)
	observed := true
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state = nil
		observed = false
	} else if err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	window, err := store.Window(ctx, id)
	if err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	coverage, err := store.Coverage(ctx, id, time.Now().Unix()-3600)
	if err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	var events []ztapiHealthEventProjection
	eventQuery := model.DB.WithContext(ctx).Model(&model.ZTAPIHealthEvent{}).
		Select("id", "request_id", "model_id", "config_version", "generation", "public_model", "modality", "operation", "entry_protocol", "completion_sequence", "completed_at", "stream", "source", "result", "reason", "counted", "stale_generation", "channel_id", "upstream_protocol", "http_status", "upstream_request_id", "upstream_task_id", "provider_error_code", "latency_milliseconds", "result_valid").
		Where("model_id = ?", id)
	if _, explicitCursor := c.GetQuery("after_sequence"); explicitCursor {
		err = eventQuery.Where("completion_sequence > ?", after).Order("completion_sequence").Limit(100).Find(&events).Error
	} else {
		err = eventQuery.Order("completion_sequence DESC").Limit(100).Find(&events).Error
	}
	if err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	// Recent incident and delivery status are bounded independently of event pagination.
	var incidents []model.ZTAPIHealthIncident
	if err = model.DB.WithContext(ctx).Where("model_id = ?", id).Order("id DESC").Limit(20).Find(&incidents).Error; err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	var jobs []model.ZTAPIHealthOutbox
	if err = model.DB.WithContext(ctx).Select("id, kind, incident_id, event_id, status, attempts, next_attempt_at, last_error, created_at, delivered_at").Where("model_id = ?", id).Order("id DESC").Limit(50).Find(&jobs).Error; err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	outbox := make([]gin.H, 0, len(jobs))
	for _, j := range jobs {
		outbox = append(outbox, gin.H{"id": j.ID, "kind": j.Kind, "incident_id": j.IncidentID, "event_id": j.EventID, "status": j.Status, "attempts": j.Attempts, "next_attempt_at": j.NextAttemptAt, "last_error": j.LastError, "created_at": j.CreatedAt, "delivered_at": j.DeliveredAt})
	}
	var verificationCases []ztapiHealthVerificationProjection
	if err = model.DB.WithContext(ctx).Model(&model.ZTAPIHealthVerificationCase{}).
		Select("id", "channel_id", "entry_protocol", "protocol", "stream", "generation", "source_event_id", "state", "attempts", "probe_request_id", "result", "created_at", "completed_at", "cancelled_at").
		Where("model_id = ?", id).Order("created_at DESC, id DESC").Limit(100).Find(&verificationCases).Error; err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	var routeStates []ztapiHealthRouteProjection
	if err = model.DB.WithContext(ctx).Model(&model.ZTAPIHealthRouteState{}).
		Select("id", "channel_id", "entry_protocol", "protocol", "stream", "generation", "independent_failures", "open", "last_probe_request_id", "last_result", "opened_at", "updated_at").
		Where("model_id = ?", id).Order("open DESC, updated_at DESC, id DESC").Limit(100).Find(&routeStates).Error; err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"model_id": id, "enabled": model.ZTAPIHealthEnabled(), "observed": observed, "state": state, "window": window, "coverage": coverage, "events": events, "incidents": incidents, "outbox": outbox, "verification_cases": verificationCases, "route_states": routeStates, "recovery_requires_publication": true})
}

func RecoverZTAPIModelHealth(c *gin.Context) {
	id, ok := ztapiHealthAdminModel(c, common.PermissionModelWrite)
	if !ok {
		return
	}
	var req struct {
		Generation uint64 `json:"generation"`
		Evidence   string `json:"evidence"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	if err := c.ShouldBindJSON(&req); err != nil || req.Generation == 0 || strings.TrimSpace(req.Evidence) == "" || len(req.Evidence) > 4096 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "generation and evidence reference are required"})
		return
	}
	err := model.NewZTAPIHealthStore(model.DB).ManualRecover(c.Request.Context(), id, req.Generation, c.GetInt("id"), req.Evidence)
	if errors.Is(err, model.ErrZTAPIHealthGenerationConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "health generation changed; refresh before recovery"})
		return
	}
	if err != nil {
		ztapiHealthAdminError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"model_id": id, "recovered": true, "published": false, "publication_required": true})
}
