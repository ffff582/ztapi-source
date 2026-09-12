package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/google/uuid"
)

type ztapiMediaHealthBackend struct {
	admitMediaRequest func(context.Context, string, string, string, int, string, bool) (*types.ZTAPIHealthTicket, error)
	admitAttempt      func(context.Context, *types.ZTAPIHealthTicket, int, string) error
	recordOutcome     func(context.Context, *types.ZTAPIHealthTicket, types.ZTAPIHealthOutcome) error
}

var productionZTAPIMediaHealthBackend = ztapiMediaHealthBackend{
	admitMediaRequest: model.AdmitZTAPIMediaHealthRequest,
	admitAttempt:      model.AdmitZTAPIHealthAttempt,
	recordOutcome:     model.RecordZTAPIHealthOutcome,
}

type ztapiVideoFetchHealth struct {
	ctx      context.Context
	backend  ztapiMediaHealthBackend
	ticket   *types.ZTAPIHealthTicket
	media    *model.ZTAPIMediaTask
	started  time.Time
	finished sync.Once
}

func beginZTAPIVideoFetchHealth(ctx context.Context, backend ztapiMediaHealthBackend, media *model.ZTAPIMediaTask) *ztapiVideoFetchHealth {
	health := &ztapiVideoFetchHealth{ctx: ctx, backend: backend, media: media, started: time.Now()}
	if media == nil || backend.admitMediaRequest == nil || backend.admitAttempt == nil || backend.recordOutcome == nil {
		return health
	}
	ticket, err := backend.admitMediaRequest(ctx, media.PublicModel, uuid.NewString(), media.PublicTaskID, media.UserID, types.ZTAPIHealthOperationVideoFetch, true)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("ztapi_health_operational_fault stage=video_fetch_admission task_id=%s error_type=%T", media.PublicTaskID, err))
		return health
	}
	if ticket == nil {
		return health
	}
	if err = backend.admitAttempt(ctx, ticket, media.ChannelID, "video-tasks"); err != nil {
		logger.LogError(ctx, fmt.Sprintf("ztapi_health_operational_fault stage=video_fetch_attempt task_id=%s error_type=%T", media.PublicTaskID, err))
		return health
	}
	health.ticket = ticket
	return health
}

func (h *ztapiVideoFetchHealth) transportFailure() {
	h.record(types.ZTAPIHealthOutcome{Result: "failure", Reason: "upstream_transport_error", Dispatched: true})
}

func (h *ztapiVideoFetchHealth) finish(httpStatus int, result *relaycommon.TaskInfo) {
	if h == nil {
		return
	}
	outcome := types.ZTAPIHealthOutcome{Result: "failure", Reason: "unrecognized_terminal", HTTPStatus: httpStatus, Dispatched: true, TransportComplete: true}
	switch httpStatus {
	case http.StatusUnauthorized:
		outcome.Reason = "upstream_auth"
	case http.StatusPaymentRequired:
		outcome.Reason = "upstream_quota"
	case http.StatusForbidden:
		outcome.Reason = "upstream_model_permission"
	case http.StatusTooManyRequests:
		outcome.Reason = "upstream_rate_limit"
	default:
		if httpStatus >= 500 {
			outcome.Reason = "upstream_http_error"
		} else if httpStatus >= 400 {
			outcome.Reason = "ambiguous_upstream_4xx"
		} else if result != nil {
			outcome.UpstreamRequestID = ztapiMediaHealthMetadata(result.UpstreamRequestID, 255)
			outcome.TerminalStatus = ztapiMediaHealthMetadata(result.ProviderStatus, 128)
			switch model.TaskStatus(result.Status) {
			case model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusInProgress:
				outcome.Result, outcome.Reason, outcome.ResultValid = "success", "valid_output", true
			case model.TaskStatusSuccess:
				if strings.TrimSpace(result.Url) != "" {
					outcome.Result, outcome.Reason, outcome.ResultValid = "success", "valid_output", true
				} else {
					outcome.Reason = "invalid_media_result"
				}
			case model.TaskStatusFailure:
				outcome.Reason = "provider_task_failed"
				if h.ticket != nil && h.ticket.Source != "probe" {
					outcome.Result = "excluded"
				}
			}
		}
	}
	h.record(outcome)
}

func ztapiMediaHealthMetadata(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		return ""
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-/", r)) {
			return ""
		}
	}
	return value
}

func (h *ztapiVideoFetchHealth) record(outcome types.ZTAPIHealthOutcome) {
	if h == nil || h.ticket == nil || h.media == nil {
		return
	}
	h.finished.Do(func() {
		outcome.Operation = types.ZTAPIHealthOperationVideoFetch
		outcome.ChannelID = h.media.ChannelID
		outcome.UpstreamProtocol = "video-tasks"
		outcome.UpstreamTaskID = ztapiMediaHealthMetadata(h.media.UpstreamTaskID, 255)
		outcome.LatencyMilliseconds = time.Since(h.started).Milliseconds()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.backend.recordOutcome(ctx, h.ticket, outcome); err != nil {
			logger.LogError(h.ctx, fmt.Sprintf("ztapi_health_operational_fault stage=video_fetch_outcome task_id=%s error_type=%T", h.media.PublicTaskID, err))
		}
	})
}
