package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

const (
	ztapiHealthProbeBodyLimit            = 1 << 20
	ztapiHealthProbeFinalizationTimeout  = 2 * time.Second
	ztapiHealthProbeFinalizationInterval = 50 * time.Millisecond
)

var ErrZTAPIHealthWorkerBusy = errors.New("ztapi health worker already running")

type ZTAPIHealthWorkerConfig struct {
	ProbeKey               string
	ProbeUserID            int
	SyntheticProbesEnabled bool
	// Verification probes are event-driven and independent from legacy hourly
	// synthetic coverage. When idle they perform no paid preparation.
	VerificationProbesEnabled bool
	// Parent verifies the key belongs to the configured dedicated probe user,
	// whose authenticated ID is the engine's only source=probe authority.
	ProbeIdentityValidated bool
	RelayBaseURL           string
	AlertWebhookURL        string
	TelegramBotToken       string
	TelegramChatID         string
	TelegramConfigured     bool
	// Parent-only initialization request, NOT a resettable runtime allowance.
	// Runtime spending uses the persisted R7 allocation exclusively.
	InitialAllocationNanoUSD int64
	Interval                 time.Duration
	RequestTimeout           time.Duration
	AlertTimeout             time.Duration
	OutboxTimeout            time.Duration
	TickTimeout              time.Duration
	LeaseDuration            time.Duration
	BatchSize                int
	Now                      func() time.Time
	// Zero disables engine orphan sweeping. Parent must choose an age longer
	// than the longest supported REAL request, not merely the probe timeout.
	OrphanAfter time.Duration
	// Trusted injection points for offline tests; production should leave nil.
	ProbeTransport http.RoundTripper
	AlertTransport http.RoundTripper
}

func DefaultZTAPIHealthWorkerConfig() ZTAPIHealthWorkerConfig {
	return ZTAPIHealthWorkerConfig{RelayBaseURL: "http://127.0.0.1:3000", VerificationProbesEnabled: true, Interval: time.Minute, RequestTimeout: 120 * time.Second, AlertTimeout: 10 * time.Second, OutboxTimeout: 30 * time.Second, TickTimeout: 10 * time.Minute, LeaseDuration: 4 * time.Minute, BatchSize: 2, Now: time.Now}
}

// Loading configuration never allocates budget, credits quota, or authenticates
// a key. The parent must validate ProbeUserID/key and transfer R7 funds itself.
func ZTAPIHealthWorkerConfigFromEnv() (ZTAPIHealthWorkerConfig, error) {
	c := DefaultZTAPIHealthWorkerConfig()
	c.ProbeKey = strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_PROBE_KEY"))
	c.AlertWebhookURL = strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_ALERT_WEBHOOK_URL"))
	c.TelegramBotToken = strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN"))
	c.TelegramChatID = strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_TELEGRAM_CHAT_ID"))
	_, telegramTokenSet := os.LookupEnv("ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN")
	_, telegramChatSet := os.LookupEnv("ZTAPI_HEALTH_TELEGRAM_CHAT_ID")
	c.TelegramConfigured = telegramTokenSet || telegramChatSet
	if value := strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_PROBE_RELAY_URL")); value != "" {
		c.RelayBaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_PROBE_USER_ID")); value != "" {
		id, err := strconv.Atoi(value)
		if err != nil || id <= 0 {
			return c, errors.New("invalid probe user ID configuration")
		}
		c.ProbeUserID = id
	}
	if value := strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_SYNTHETIC_PROBES_ENABLED")); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return c, errors.New("invalid synthetic probe configuration")
		}
		c.SyntheticProbesEnabled = enabled
	}
	if value := strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_VERIFICATION_PROBES_ENABLED")); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return c, errors.New("invalid verification probe configuration")
		}
		c.VerificationProbesEnabled = enabled
	}
	value := strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_PROBE_BUDGET_USD"))
	if value == "" {
		value = "0"
	}
	// Decimal-only, exact nanoUSD. Reject fractions, exponents, negatives and
	// sub-nano precision instead of silently rounding a money allocation.
	if len(value) > 40 {
		return c, errors.New("invalid probe budget configuration")
	}
	dots, digits := 0, 0
	for _, ch := range value {
		if ch == '.' {
			dots++
			continue
		}
		if ch < '0' || ch > '9' {
			return c, errors.New("invalid probe budget configuration")
		}
		digits++
	}
	if dots > 1 || digits == 0 {
		return c, errors.New("invalid probe budget configuration")
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok {
		return c, errors.New("invalid probe budget configuration")
	}
	r.Mul(r, big.NewRat(1_000_000_000, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return c, errors.New("probe budget is not representable in nanoUSD")
	}
	c.InitialAllocationNanoUSD = r.Num().Int64()
	return c, nil
}

// Engine adapters must persist outbox leases, attempts and retry times. This is
// a projection of engine-owned records, never a duplicate incident/outbox table.
type ZTAPIHealthWorkItem struct {
	Test               bool
	Kind               string
	ID                 int64
	ModelID            int
	Generation         uint64
	IncidentID         int64
	Attempts           int
	LeaseToken         string
	LeaseUntil         time.Time
	DedupKey           string
	EventID            int64
	VerificationCaseID string
	Alert              ZTAPIHealthAlertMetadata
}

type ZTAPIHealthAlertMetadata struct {
	MetadataStatus           string   `json:"metadata_status"`
	Model                    string   `json:"model"`
	Rule                     string   `json:"rule"`
	WindowStart              *int64   `json:"window_start"`
	WindowEnd                *int64   `json:"window_end"`
	Failures                 *int64   `json:"fail_count"`
	ValidSamples             *int64   `json:"valid_count"`
	ConsecutiveFailures      *int64   `json:"consecutive_failures"`
	FinishReasons            []string `json:"finish_reasons"`
	UpstreamRequestID        string   `json:"upstream_request_id"`
	TriggerUpstreamRequestID string   `json:"trigger_upstream_request_id"`
	ProbeRequestID           string   `json:"probe_request_id"`
	TriggerRequestID         string   `json:"trigger_request_id"`
	UpstreamTaskID           string   `json:"upstream_task_id"`
	Modality                 string   `json:"modality"`
	Operation                string   `json:"operation"`
	LatencyMilliseconds      *int64   `json:"latency_milliseconds"`
	ResultValid              *bool    `json:"result_valid"`
	ErrorCode                string   `json:"error_code"`
	HTTPStatus               *int     `json:"http_status"`
	Unpublished              *bool    `json:"unpublished"`
	OpenedAt                 *int64   `json:"opened_at"`
	ObservedAt               int64    `json:"observed_at"`
	AdminModelHealthPath     string   `json:"admin_model_health_path"`
	AdminIncidentReference   string   `json:"admin_incident_reference"`
	ChannelID                int      `json:"channel_id"`
	EntryProtocol            string   `json:"entry_protocol"`
	UpstreamProtocol         string   `json:"upstream_protocol"`
	Stream                   bool     `json:"stream"`
}

func ztapiHealthSafeAlertMetadata(item ZTAPIHealthWorkItem, now time.Time) ZTAPIHealthAlertMetadata {
	d := item.Alert
	d.UpstreamRequestID = ztapiHealthSafeRequestID(d.UpstreamRequestID)
	d.TriggerUpstreamRequestID = ztapiHealthSafeRequestID(d.TriggerUpstreamRequestID)
	d.ProbeRequestID = ztapiHealthSafeRequestID(d.ProbeRequestID)
	d.TriggerRequestID = ztapiHealthSafeRequestID(d.TriggerRequestID)
	d.UpstreamTaskID = ztapiHealthSafeRequestID(d.UpstreamTaskID)
	switch d.Modality {
	case model.ZTAPIModalityText, model.ZTAPIModalityEmbedding, model.ZTAPIModalityImage, model.ZTAPIModalityVideo:
	default:
		d.Modality = "unknown"
	}
	switch d.Operation {
	case "", types.ZTAPIHealthOperationImageGenerate, types.ZTAPIHealthOperationVideoSubmit, types.ZTAPIHealthOperationVideoFetch:
	default:
		d.Operation = "unknown"
	}
	if d.Operation == "" {
		d.Operation = "not_applicable"
	}
	if d.LatencyMilliseconds != nil && (*d.LatencyMilliseconds < 0 || *d.LatencyMilliseconds > 86_400_000) {
		d.LatencyMilliseconds = nil
	}
	if d.MetadataStatus != "available" {
		d.MetadataStatus = "unknown"
	}
	if d.Model == "" || len(d.Model) > 128 || strings.HasPrefix(d.Model, "sk-") {
		d.Model = "unknown"
	}
	for _, ch := range d.Model {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("._:/-", ch)) {
			d.Model = "unknown"
			break
		}
	}
	if d.Rule != "consecutive_2" && d.Rule != "rolling_24h_gt_2pct" && d.Rule != "verified_all_routes" &&
		d.Rule != "verified_route_2" && d.Rule != "manual_verified_recovery" {
		d.Rule = "unknown"
	}
	switch d.ErrorCode {
	case "upstream_http_error", "upstream_auth", "upstream_model_permission", "upstream_quota", "upstream_rate_limit", "probe_refusal", "safety_refusal", "invalid_input", "probe_upstream_4xx", "upstream_error", "ambiguous_upstream_4xx", "upstream_transport_error", "unsupported_protocol", "observation_limit", "unclassified_upstream_error", "malformed_upstream_response", "relay_processing_error", "missing_native_terminal", "probe_invalid_terminal", "unrecognized_terminal", "probe_incorrect_output", "probe_valid_output", "valid_output", "invalid_media_result", "provider_task_failed", "length_without_visible_output", "empty_output", "client_cancelled", "orphaned_admission", "not_dispatched":
	default:
		d.ErrorCode = "unknown"
	}
	finishes := make([]string, 0, 8)
	for i, finish := range d.FinishReasons {
		if i == 8 {
			break
		}
		switch finish {
		case "stop", "tool_calls", "function_call", "end_turn", "stop_sequence", "tool_use", "STOP", "length", "max_tokens", "max_output_tokens", "MAX_TOKENS", "refusal", "content_filter", "SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "RECITATION", "SPII", "IMAGE_SAFETY":
		default:
			finish = "unknown"
		}
		finishes = append(finishes, finish)
	}
	if len(finishes) == 0 {
		finishes = []string{"unknown"}
	}
	d.FinishReasons, d.ObservedAt = finishes, now.Unix()
	d.AdminModelHealthPath = fmt.Sprintf("/api/models/ztapi/%d/health", item.ModelID)
	d.AdminIncidentReference = fmt.Sprintf("incident:%d", item.IncidentID)
	return d
}

func ztapiHealthEnrichVerificationMetadata(ctx context.Context, store *model.ZTAPIHealthStore, item ZTAPIHealthWorkItem, d ZTAPIHealthAlertMetadata) ZTAPIHealthAlertMetadata {
	var trigger model.ZTAPIHealthEvent
	if err := store.DB.WithContext(ctx).Select("id", "model_id", "generation", "request_id", "reason", "http_status", "outcome", "upstream_request_id", "upstream_task_id", "modality", "operation", "latency_milliseconds", "result_valid").First(&trigger, item.EventID).Error; err == nil && trigger.ModelID == item.ModelID && trigger.Generation == item.Generation {
		d.TriggerRequestID = trigger.RequestID
		d.TriggerUpstreamRequestID = trigger.UpstreamRequestID
		// Legacy incidents may predate route verification. Preserve their only
		// upstream reference until a diagnostic probe supplies stronger evidence.
		d.UpstreamRequestID = trigger.UpstreamRequestID
		d.ErrorCode, d.HTTPStatus = trigger.Reason, &trigger.HTTPStatus
		d.UpstreamTaskID = trigger.UpstreamTaskID
		d.Modality, d.Operation = trigger.Modality, trigger.Operation
		d.LatencyMilliseconds, d.ResultValid = &trigger.LatencyMilliseconds, &trigger.ResultValid
		if len(trigger.Outcome) <= 64*1024 {
			var outcome struct{ FinishReasons []string }
			if common.Unmarshal([]byte(trigger.Outcome), &outcome) == nil {
				d.FinishReasons = outcome.FinishReasons
			}
		}
	}
	var verification model.ZTAPIHealthVerificationCase
	query := store.DB.WithContext(ctx).Where("model_id = ? AND generation = ? AND state = ? AND result = ?", item.ModelID, item.Generation, "completed", "failure")
	if item.VerificationCaseID != "" {
		query = query.Where("id = ?", item.VerificationCaseID)
	} else {
		query = query.Where("source_event_id = ?", item.EventID).Order("completed_at DESC, id DESC")
	}
	err := query.First(&verification).Error
	if err != nil {
		return d
	}
	d.ChannelID = verification.ChannelID
	d.EntryProtocol = verification.EntryProtocol
	d.UpstreamProtocol = verification.Protocol
	d.Stream = verification.Stream
	d.ProbeRequestID = verification.ProbeRequestID
	if d.OpenedAt == nil && verification.CompletedAt > 0 {
		openedAt := verification.CompletedAt / 1000
		d.OpenedAt = &openedAt
	}
	var probe model.ZTAPIHealthEvent
	err = store.DB.WithContext(ctx).
		Select("model_id", "generation", "reason", "http_status", "outcome", "upstream_request_id", "upstream_task_id", "modality", "operation", "latency_milliseconds", "result_valid").
		Where("request_id = ? AND model_id = ? AND generation = ? AND source = ?", verification.ProbeRequestID, item.ModelID, item.Generation, "probe").
		Order("id DESC").First(&probe).Error
	if err != nil {
		return d
	}
	d.ErrorCode, d.HTTPStatus = probe.Reason, &probe.HTTPStatus
	d.UpstreamRequestID, d.UpstreamTaskID = probe.UpstreamRequestID, probe.UpstreamTaskID
	d.Modality, d.Operation = probe.Modality, probe.Operation
	d.LatencyMilliseconds, d.ResultValid = &probe.LatencyMilliseconds, &probe.ResultValid
	if len(probe.Outcome) <= 64*1024 {
		var outcome struct{ FinishReasons []string }
		if common.Unmarshal([]byte(probe.Outcome), &outcome) == nil {
			d.FinishReasons = outcome.FinishReasons
		}
	}
	return d
}

func ztapiHealthLoadAlertMetadata(ctx context.Context, store *model.ZTAPIHealthStore, item ZTAPIHealthWorkItem) ZTAPIHealthAlertMetadata {
	d := ZTAPIHealthAlertMetadata{MetadataStatus: "unknown"}
	var config model.ZTAPIModelConfig
	publicationKnown := store.DB.WithContext(ctx).Select("id", "public_name", "published").First(&config, item.ModelID).Error == nil
	if publicationKnown {
		d.Model = config.PublicNameValue()
		unpublished := !config.Published
		d.Unpublished = &unpublished
		d.MetadataStatus = "available"
	}
	if item.Kind == "route_alert" {
		d.Rule = "verified_route_2"
		failures := int64(2)
		d.ConsecutiveFailures = &failures
		return ztapiHealthEnrichVerificationMetadata(ctx, store, item, d)
	}
	var incident model.ZTAPIHealthIncident
	if err := store.DB.WithContext(ctx).Select("id", "model_id", "generation", "public_model", "rule", "window_start", "failures", "valid_samples", "consecutive_failures", "opened_at", "trigger_event_id", "verification_case_id", "recovered_at").First(&incident, item.IncidentID).Error; err != nil || incident.ModelID != item.ModelID || incident.Generation != item.Generation {
		return d
	}
	d.Model, d.Rule = incident.PublicModel, incident.Rule
	if item.Kind == "recovery_alert" {
		d.Rule = "manual_verified_recovery"
		if incident.RecoveredAt > 0 {
			d.OpenedAt = &incident.RecoveredAt
		}
	} else {
		d.OpenedAt = &incident.OpenedAt
	}
	d.Failures, d.ValidSamples, d.ConsecutiveFailures = &incident.Failures, &incident.ValidSamples, &incident.ConsecutiveFailures
	d.WindowStart, d.WindowEnd = &incident.WindowStart, &incident.OpenedAt
	item.EventID = incident.TriggerEventID
	if item.VerificationCaseID == "" {
		item.VerificationCaseID = incident.VerificationCaseID
	}
	return ztapiHealthEnrichVerificationMetadata(ctx, store, item, d)
}

type ZTAPIHealthDelivery struct {
	Receipt string
	// HTTP 2xx means accepted by the webhook, NOT verified recipient receipt.
	Accepted bool
	RetryAt  time.Time
	Code     string
}

// ZTAPIVerificationProbeEvidence is the bounded durable result projection used
// by the worker. It intentionally excludes prompts, response bodies and keys.
type ZTAPIVerificationProbeEvidence struct {
	RequestID         string
	UpstreamRequestID string
	Result            string
	Route             model.ZTAPIHealthRouteIdentity
	InputTokens       int64
	OutputTokens      int64
}

type ZTAPIHealthWorkerBackend struct {
	Probes *model.ZTAPIProbeStore
	// Verification cases are the only automatic paid-probe work source. The
	// legacy hourly probe store remains attached for status/outbox history only.
	Verifications             *model.ZTAPIHealthVerificationStore
	LoadVerificationTarget    func(context.Context, model.ZTAPIHealthVerificationCase) (model.ZTAPIProbeTarget, bool, error)
	CheckVerificationDispatch model.ZTAPIHealthVerificationDispatchCheck
	LoadVerificationEvidence  func(context.Context, model.ZTAPIHealthVerificationCase) (*ZTAPIVerificationProbeEvidence, error)
	// Bounded, fair/cursor-based enumeration of published model/mode targets.
	ListTargets           func(context.Context, int) ([]model.ZTAPIProbeTarget, error)
	CheckProbe            model.ZTAPIProbeCheck
	ValidateProbeIdentity func(context.Context) (bool, string, error)
	// Read-only collector-finalization check, correlated with the dedicated
	// authenticated user, job's X-Request-ID, model, mode, version and generation.
	// Missing or not-yet-final results are operational unknown, never success.
	ProbeFinalized func(context.Context, model.ZTAPIProbeJob, string) (bool, error)
	ClaimOutbox    func(context.Context, string, time.Time, time.Duration, int) ([]ZTAPIHealthWorkItem, error)
	// Must CAS the unexpired lease token; accepted=false persists retry state.
	FinishOutbox func(context.Context, ZTAPIHealthWorkItem, ZTAPIHealthDelivery) error
	// Separate idempotent engine callback; must preserve manual-only recovery.
	Unpublish      func(context.Context, ZTAPIHealthWorkItem) error
	SweepOrphans   func(context.Context, time.Time, int) (int, error)
	RecordCoverage func(context.Context, ZTAPIHealthWorkItem) error
	// Operational codes/IDs only; never forward errors, response text or keys.
	// Parent can persist/deduplicate these statuses independently of health events.
	OperationalStatus func(context.Context, string, string)
}

// AttachZTAPIHealthStore binds the stable engine outbox APIs and a read-only
// final-result lookup. Publication/price inspection, measured input samples,
// fair target enumeration and operational status persistence remain injected.
func AttachZTAPIHealthStore(backend ZTAPIHealthWorkerBackend, store *model.ZTAPIHealthStore, probeUserID int) (ZTAPIHealthWorkerBackend, error) {
	if store == nil || store.DB == nil {
		return backend, errors.New("health engine store unavailable")
	}
	if backend.Probes == nil {
		backend.Probes = &model.ZTAPIProbeStore{DB: store.DB}
	}
	if backend.Verifications == nil {
		backend.Verifications = model.NewZTAPIHealthVerificationStore(store.DB)
	}
	backend.ClaimOutbox = func(ctx context.Context, kind string, _ time.Time, lease time.Duration, limit int) ([]ZTAPIHealthWorkItem, error) {
		jobs, err := store.ClaimOutbox(ctx, kind, limit, int64((lease+time.Second-1)/time.Second))
		if err != nil {
			return nil, err
		}
		items := make([]ZTAPIHealthWorkItem, 0, len(jobs))
		for _, job := range jobs {
			item := ZTAPIHealthWorkItem{Kind: job.Kind, ID: job.ID, ModelID: job.ModelID, Generation: job.Generation, IncidentID: job.IncidentID, EventID: job.EventID, VerificationCaseID: job.VerificationCaseID, Attempts: job.Attempts, LeaseToken: job.LeaseToken, LeaseUntil: time.Unix(job.LeaseUntil, 0), DedupKey: job.DedupKey}
			item.Test = kind == "alert_test"
			if item.Test {
				item.Alert = ZTAPIHealthAlertMetadata{Model: "TEST-NO-MODEL-CHANGE", OpenedAt: &job.CreatedAt}
			}
			if kind == "alert" || kind == "route_alert" || kind == "recovery_alert" {
				item.Alert = ztapiHealthLoadAlertMetadata(ctx, store, item)
			}
			items = append(items, item)
		}
		return items, nil
	}
	backend.FinishOutbox = func(ctx context.Context, item ZTAPIHealthWorkItem, delivery ZTAPIHealthDelivery) error {
		code := delivery.Code
		if delivery.Accepted {
			code = ""
		}
		return store.FinishOutboxWithReceipt(ctx, item.ID, item.LeaseToken, code, delivery.RetryAt.Unix(), delivery.Receipt)
	}
	backend.Unpublish = func(ctx context.Context, item ZTAPIHealthWorkItem) error {
		return store.ProcessUnpublish(ctx, item.ID, item.LeaseToken)
	}
	backend.SweepOrphans = func(ctx context.Context, before time.Time, limit int) (int, error) {
		return store.MarkOrphansUnknown(ctx, before.Unix(), limit)
	}
	backend.RecordCoverage = func(ctx context.Context, item ZTAPIHealthWorkItem) error {
		return backend.Probes.RecordStatus(ctx, "coverage", "orphaned_admission_admin_only", strconv.FormatInt(item.EventID, 10), store.Now())
	}
	backend.ProbeFinalized = func(ctx context.Context, job model.ZTAPIProbeJob, requestID string) (bool, error) {
		if probeUserID <= 0 || requestID != "ztapi-health-probe-"+job.ID {
			return false, nil
		}
		var requests []model.ZTAPIHealthRequest
		requestQuery := store.DB.WithContext(ctx).Where("request_id = ? AND model_id = ? AND user_id = ? AND source = ? AND generation = ? AND config_version = ? AND public_model = ? AND completed = ?", requestID, job.ModelID, probeUserID, "probe", job.Generation, job.ConfigVersion, job.PublicModel, true)
		if job.Modality != "" {
			requestQuery = requestQuery.Where("modality = ? AND operation = ?", job.Modality, job.Operation)
		} else {
			requestQuery = requestQuery.Where("stream = ?", job.Stream)
		}
		err := requestQuery.Limit(2).Find(&requests).Error
		if err != nil || len(requests) != 1 {
			return false, err
		}
		var count int64
		eventQuery := store.DB.WithContext(ctx).Model(&model.ZTAPIHealthEvent{}).Where("execution_id = ? AND model_id = ? AND source = ? AND generation = ? AND config_version = ? AND stale_generation = ? AND result IN ?", requests[0].ExecutionID, job.ModelID, "probe", job.Generation, job.ConfigVersion, false, []string{"success", "failure"})
		if job.Modality != "" {
			eventQuery = eventQuery.Where("modality = ? AND operation = ?", job.Modality, job.Operation)
		} else {
			eventQuery = eventQuery.Where("stream = ?", job.Stream)
		}
		err = eventQuery.Count(&count).Error
		return count == 1 && err == nil, err
	}
	return backend, nil
}

// ZTAPIHealthStoreProbeCheck composes a parent's transaction-local publication,
// generation and cost inspector with the engine's real-coverage query. This
// wrapper never treats probe, unknown, excluded or stale-generation events as
// coverage. The inspector must preserve catalog -> health lock order.
func ZTAPIHealthStoreProbeCheck(store *model.ZTAPIHealthStore, inspect model.ZTAPIProbeCheck) model.ZTAPIProbeCheck {
	return func(ctx context.Context, tx *gorm.DB, target model.ZTAPIProbeTarget, since time.Time) (model.ZTAPIProbeAdmission, error) {
		if store == nil || tx == nil || inspect == nil {
			return model.ZTAPIProbeAdmission{}, errors.New("probe publication inspector missing")
		}
		admission, err := inspect(ctx, tx, target, since)
		if err != nil || !admission.Active || admission.Target != target {
			return admission, err
		}
		txStore := model.NewZTAPIHealthStore(tx.Where("generation = ? AND config_version = ?", target.Generation, target.ConfigVersion))
		txStore.Now = func() time.Time { return since.Add(time.Hour) }
		coverage, err := txStore.Coverage(ctx, target.ModelID, since.Unix())
		if err != nil {
			return admission, err
		}
		admission.RealCoverage = false
		for _, row := range coverage {
			matchesMode := row.Stream == target.Stream
			if target.Modality != "" {
				matchesMode = row.Modality == target.Modality && row.Operation == target.Operation
			}
			if row.Source == "real" && matchesMode && row.ValidSamples > 0 {
				admission.RealCoverage = true
			}
		}
		return admission, nil
	}
}

// StartProductionZTAPIHealthWorker never provisions identity/quota, transfers
// allocation, publishes a model or recovers a circuit. Migrate before calling.
func StartProductionZTAPIHealthWorker(ctx context.Context) (*ZTAPIHealthWorker, error) {
	config, err := ZTAPIHealthWorkerConfigFromEnv()
	if err != nil {
		return nil, err
	}
	backend, err := NewProductionZTAPIHealthWorkerBackend(config)
	if err != nil {
		return nil, err
	}
	return StartZTAPIHealthWorker(ctx, backend, config)
}

func GetProductionZTAPIHealthWorkerStatus(ctx context.Context) ([]model.ZTAPIHealthWorkerStatus, error) {
	if model.DB == nil {
		return nil, errors.New("health worker database unavailable")
	}
	return (&model.ZTAPIProbeStore{DB: model.DB}).Statuses(ctx)
}

// NewProductionZTAPIHealthWorkerBackend reads current catalog/price evidence,
// authenticated token ownership and bounded consumption samples from the two
// configured databases. No cached publication authorizes a paid dispatch.
func NewProductionZTAPIHealthWorkerBackend(config ZTAPIHealthWorkerConfig) (ZTAPIHealthWorkerBackend, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	db, logDB := model.DB, model.LOG_DB
	if db == nil {
		return ZTAPIHealthWorkerBackend{}, errors.New("health worker database unavailable")
	}
	store := model.NewZTAPIHealthStore(db)
	store.Now = config.Now
	backend, err := AttachZTAPIHealthStore(ZTAPIHealthWorkerBackend{}, store, config.ProbeUserID)
	if err != nil {
		return backend, err
	}
	backend.OperationalStatus = func(ctx context.Context, code, ref string) {
		component := "probe"
		switch {
		case strings.HasPrefix(code, "alert_") || code == "http_2xx_accepted" || code == "telegram_accepted":
			component = "alert"
		case strings.HasPrefix(code, "unpublish"):
			component = "unpublish"
		case strings.HasPrefix(code, "worker_") || code == "outbox_callbacks_missing":
			component = "worker"
		case strings.HasPrefix(code, "admin_"):
			component = "coverage"
		case code == "abandoned_dispatch" || code == "probe_final_result_missing" || code == "incomplete_response" || code == "probe_transport_error" || code == "probe_usage_invalid" || code == "invalid_response" || code == "wrong_answer" || code == "response_too_large" || code == "http_status":
			component = "coverage"
		}
		if err := backend.Probes.RecordStatus(ctx, component, code, ref, config.Now()); err != nil {
			common.SysError("ztapi health worker status persistence failed")
		}
	}
	backend.ValidateProbeIdentity = func(ctx context.Context) (bool, string, error) {
		return productionZTAPIProbeIdentity(ctx, db, config)
	}
	backend.LoadVerificationTarget = func(ctx context.Context, verificationCase model.ZTAPIHealthVerificationCase) (model.ZTAPIProbeTarget, bool, error) {
		target, admission, err := productionZTAPIVerificationTarget(ctx, db, logDB, verificationCase, config.Now())
		return target, admission.Active, err
	}
	backend.CheckVerificationDispatch = func(ctx context.Context, tx *gorm.DB, verificationCase model.ZTAPIHealthVerificationCase) (model.ZTAPIHealthVerificationDispatchAdmission, error) {
		_, admission, err := productionZTAPIVerificationTarget(ctx, tx, func() *gorm.DB {
			if logDB == db {
				return tx
			}
			return logDB
		}(), verificationCase, config.Now())
		return admission, err
	}
	backend.LoadVerificationEvidence = func(ctx context.Context, verificationCase model.ZTAPIHealthVerificationCase) (*ZTAPIVerificationProbeEvidence, error) {
		return productionZTAPIVerificationEvidence(ctx, db, logDB, config.ProbeUserID, verificationCase)
	}
	backend.ListTargets = func(ctx context.Context, limit int) ([]model.ZTAPIProbeTarget, error) {
		return productionZTAPIProbeTargets(ctx, db, limit, config.Now())
	}
	inspect := func(ctx context.Context, tx *gorm.DB, target model.ZTAPIProbeTarget, _ time.Time) (model.ZTAPIProbeAdmission, error) {
		current, active, err := productionZTAPIProbeTarget(ctx, tx, target.ModelID, target.Stream, true)
		admission := model.ZTAPIProbeAdmission{Target: current, Active: active}
		if err != nil || !active || current != target {
			return admission, err
		}
		// The dedicated key's ownership and engine source classification must
		// still agree immediately before reservation/dispatch, not only at boot.
		valid, code, err := productionZTAPIProbeIdentity(ctx, tx, config)
		if err != nil {
			return model.ZTAPIProbeAdmission{}, err
		}
		if !valid {
			statusErr := (&model.ZTAPIProbeStore{DB: tx}).RecordStatus(ctx, "probe", code, "", config.Now())
			return model.ZTAPIProbeAdmission{}, statusErr
		}
		logs := logDB
		if logDB == db {
			logs = tx
		}
		admission.Samples, err = productionZTAPIProbeSamples(ctx, tx, logs, current, config.Now())
		return admission, err
	}
	backend.CheckProbe = ZTAPIHealthStoreProbeCheck(store, inspect)
	// Database-only status initialization; do not inspect probe identity, catalog,
	// usage samples, or budget until a durable verification case is claimed.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probeCode := "verification_waiting_for_case"
	if !config.VerificationProbesEnabled {
		probeCode = "verification_probes_disabled"
	}
	alertCode := "alert_recipient_missing_or_invalid"
	if validZTAPIHealthAlertRecipient(config) {
		alertCode = "alert_configured_unverified"
	}
	for component, code := range map[string]string{"probe": probeCode, "alert": alertCode, "worker": "worker_waiting_first_tick"} {
		if err := backend.Probes.RecordStatus(ctx, component, code, "", config.Now()); err != nil {
			return backend, err
		}
	}
	return backend, nil
}

func sameZTAPIVerificationRoute(left, right model.ZTAPIHealthRouteIdentity) bool {
	return left.ModelID == right.ModelID && left.ChannelID == right.ChannelID &&
		ztapiVerificationEntryProtocol(left) == ztapiVerificationEntryProtocol(right) &&
		strings.TrimSpace(left.Protocol) == strings.TrimSpace(right.Protocol) && left.Stream == right.Stream &&
		left.CredentialVersion.String() == right.CredentialVersion.String() && left.Generation == right.Generation
}

func ztapiVerificationEntryProtocol(route model.ZTAPIHealthRouteIdentity) string {
	entryProtocol := strings.TrimSpace(route.EntryProtocol)
	if entryProtocol == "" {
		entryProtocol = strings.TrimSpace(route.Protocol)
	}
	return entryProtocol
}

func productionZTAPIVerificationTarget(ctx context.Context, db, logDB *gorm.DB, verificationCase model.ZTAPIHealthVerificationCase, now time.Time) (model.ZTAPIProbeTarget, model.ZTAPIHealthVerificationDispatchAdmission, error) {
	empty := model.ZTAPIHealthVerificationDispatchAdmission{}
	if db == nil || logDB == nil {
		return model.ZTAPIProbeTarget{}, empty, errors.New("verification database unavailable")
	}
	target, active, err := productionZTAPIProbeTargetForProtocol(ctx, db, verificationCase.ModelID, verificationCase.Protocol, verificationCase.Stream, false)
	if err != nil || !active {
		return target, empty, err
	}
	if target.ModelID != verificationCase.ModelID || target.Protocol != verificationCase.Protocol || target.Stream != verificationCase.Stream || target.Generation != verificationCase.Generation {
		return target, empty, nil
	}
	target.EntryProtocol = verificationCase.EntryProtocol
	var snapshot model.ZTAPIModelPublicationSnapshot
	if err := db.WithContext(ctx).First(&snapshot, target.PublicationSnapshotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return target, empty, nil
		}
		return target, empty, err
	}
	allowed := false
	for _, channelID := range snapshot.ChannelIDs() {
		if channelID == verificationCase.ChannelID {
			allowed = true
			break
		}
	}
	if !allowed {
		return target, empty, nil
	}
	var channel model.Channel
	if err := db.WithContext(ctx).First(&channel, verificationCase.ChannelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return target, empty, nil
		}
		return target, empty, err
	}
	if channel.Status != common.ChannelStatusEnabled || !channel.ZTAPIManaged {
		return target, empty, nil
	}
	modelAllowed := false
	for _, channelModel := range channel.GetModels() {
		if strings.TrimSpace(channelModel) == target.SourceModel || strings.TrimSpace(channelModel) == "*" {
			modelAllowed = true
			break
		}
	}
	if !modelAllowed {
		return target, empty, nil
	}
	credentialMatches := false
	for index, key := range channel.GetKeys() {
		if channel.ChannelInfo.IsMultiKey && channel.ChannelInfo.MultiKeyStatusList != nil {
			if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status != common.ChannelStatusEnabled {
				continue
			}
		}
		if model.ZTAPICredentialVersionMatchesKey(verificationCase.CredentialVersion, key) {
			credentialMatches = true
			break
		}
	}
	if !credentialMatches {
		return target, empty, nil
	}
	inputTokens := int64(0)
	if target.FixedCostNanoUSD == 0 {
		samples, sampleErr := productionZTAPIProbeSamples(ctx, db, logDB, target, now.UTC())
		if sampleErr != nil {
			return target, empty, sampleErr
		}
		inputTokens = model.ZTAPIProbeP95Input(target.ModelID, samples)
	}
	admission := model.ZTAPIHealthVerificationDispatchAdmission{
		Active: true, Route: verificationCase.RouteIdentity(), InputTokens: inputTokens,
		InputNanoUSDPerMillion: target.InputNanoUSDPerMillion, OutputNanoUSDPerMillion: target.OutputNanoUSDPerMillion,
		FixedCostNanoUSD: target.FixedCostNanoUSD,
	}
	if !sameZTAPIVerificationRoute(admission.Route, verificationCase.RouteIdentity()) {
		return target, empty, nil
	}
	return target, admission, nil
}

func productionZTAPIVerificationEvidence(ctx context.Context, db, logDB *gorm.DB, probeUserID int, verificationCase model.ZTAPIHealthVerificationCase) (*ZTAPIVerificationProbeEvidence, error) {
	if db == nil || logDB == nil || probeUserID <= 0 || verificationCase.ProbeRequestID == "" {
		return nil, nil
	}
	var requests []model.ZTAPIHealthRequest
	if err := db.WithContext(ctx).Where(
		"request_id = ? AND model_id = ? AND user_id = ? AND source = ? AND generation = ? AND stream = ? AND completed = ?",
		verificationCase.ProbeRequestID, verificationCase.ModelID, probeUserID, "probe", verificationCase.Generation, verificationCase.Stream, true,
	).Limit(2).Find(&requests).Error; err != nil || len(requests) != 1 {
		return nil, err
	}
	requestEntryProtocol := strings.TrimSpace(requests[0].EntryProtocol)
	if requestEntryProtocol != ztapiVerificationEntryProtocol(verificationCase.RouteIdentity()) {
		return nil, nil
	}
	var events []model.ZTAPIHealthEvent
	if err := db.WithContext(ctx).Where(
		"execution_id = ? AND model_id = ? AND source = ? AND generation = ? AND stale_generation = ?",
		requests[0].ExecutionID, verificationCase.ModelID, "probe", verificationCase.Generation, false,
	).Limit(2).Find(&events).Error; err != nil || len(events) != 1 {
		return nil, err
	}
	event := events[0]
	if event.RequestID != verificationCase.ProbeRequestID || event.ChannelID != verificationCase.ChannelID || event.Stream != verificationCase.Stream ||
		strings.TrimSpace(event.UpstreamProtocol) != strings.TrimSpace(verificationCase.Protocol) || event.CredentialVersion != verificationCase.CredentialVersion ||
		(event.Result != "success" && event.Result != "failure") {
		return nil, nil
	}
	if event.Result == "failure" {
		// A failed upstream request normally has no consume log because billing is
		// refunded. The exact durable health event is sufficient failure evidence;
		// supplier billing reconciliation remains an independent finance concern.
		return &ZTAPIVerificationProbeEvidence{
			RequestID: verificationCase.ProbeRequestID, UpstreamRequestID: event.UpstreamRequestID,
			Result: "failure", Route: verificationCase.RouteIdentity(),
		}, nil
	}
	var logs []model.Log
	if err := logDB.WithContext(ctx).Where("request_id = ? AND user_id = ? AND type = ?", verificationCase.ProbeRequestID, probeUserID, model.LogTypeConsume).Limit(2).Find(&logs).Error; err != nil || len(logs) != 1 {
		return nil, err
	}
	return &ZTAPIVerificationProbeEvidence{
		RequestID: verificationCase.ProbeRequestID, UpstreamRequestID: event.UpstreamRequestID,
		Result: "healthy", Route: verificationCase.RouteIdentity(), InputTokens: int64(logs[0].PromptTokens), OutputTokens: int64(logs[0].CompletionTokens),
	}, nil
}

func productionZTAPIProbeIdentity(ctx context.Context, db *gorm.DB, config ZTAPIHealthWorkerConfig) (bool, string, error) {
	if config.ProbeKey == "" || config.ProbeUserID <= 0 {
		return false, "probe_identity_missing", nil
	}
	engineUserID, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_PROBE_USER_ID")))
	if err != nil || engineUserID != config.ProbeUserID {
		return false, "probe_identity_source_mismatch", nil
	}
	if !model.ZTAPIHealthEnabled() {
		return false, "instrumentation_disabled", nil
	}
	// Query only non-secret columns and silence SQL logger for the key-hash
	// predicate. Do not use token validation helpers that update caches/status.
	safeDB := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	var token model.Token
	err = safeDB.Select("id", "user_id", "status", "expired_time", "remain_quota", "unlimited_quota").Where("key_hash = ?", common.HashZTAPIKey(config.ProbeKey)).Take(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, "probe_identity_invalid", nil
	}
	if err != nil {
		return false, "probe_identity_check_error", err
	}
	if token.UserId != config.ProbeUserID || token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime <= config.Now().Unix()) {
		return false, "probe_identity_invalid", nil
	}
	var user model.User
	err = safeDB.Select("id", "role", "status", "quota", "debt_suspended").First(&user, config.ProbeUserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, "probe_identity_invalid", nil
	}
	if err != nil {
		return false, "probe_identity_check_error", err
	}
	if user.Role != common.RoleCommonUser || user.Status != common.UserStatusEnabled || user.DebtSuspended {
		return false, "probe_identity_invalid", nil
	}
	if user.Quota <= 0 || (!token.UnlimitedQuota && token.RemainQuota <= 0) {
		return false, "probe_quota_missing", nil
	}
	return true, "probe_ready", nil
}

func productionZTAPIProbeTarget(ctx context.Context, db *gorm.DB, id int, stream, lock bool) (model.ZTAPIProbeTarget, bool, error) {
	return productionZTAPIProbeTargetForProtocol(ctx, db, id, "", stream, lock)
}

func productionZTAPIProbeTargetForProtocol(ctx context.Context, db *gorm.DB, id int, requestedProtocol string, stream, lock bool) (model.ZTAPIProbeTarget, bool, error) {
	target := model.ZTAPIProbeTarget{ModelID: id, Stream: stream}
	tx := db.WithContext(ctx)
	if lock {
		// Preserve the engine's catalog -> health -> config ordering. Budget
		// is already locked by Dispatch; no engine path acquires that budget.
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ZTAPICatalogLock{ID: 1}).Error; err != nil {
			return target, false, err
		}
		if err := tx.Model(&model.ZTAPICatalogLock{}).Where("id = ?", 1).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return target, false, err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.ZTAPIHealthState{ModelID: id, Generation: 1}).Error; err != nil {
			return target, false, err
		}
		if err := tx.Model(&model.ZTAPIHealthState{}).Where("model_id = ?", id).UpdateColumn("generation", gorm.Expr("generation")).Error; err != nil {
			return target, false, err
		}
		tx = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
	}
	state := model.ZTAPIHealthState{Generation: 1}
	err := tx.Take(&state, "model_id = ?", id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return target, false, err
	}
	if state.Open {
		return target, false, nil
	}
	var c model.ZTAPIModelConfig
	err = tx.First(&c, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return target, false, nil
	}
	if err != nil {
		return target, false, err
	}
	if !c.Published || c.PublicationSnapshotID <= 0 || c.PublicNameValue() == "" {
		return target, false, nil
	}
	var snapshot model.ZTAPIModelPublicationSnapshot
	err = tx.First(&snapshot, c.PublicationSnapshotID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return target, false, nil
	}
	if err != nil {
		return target, false, err
	}
	if snapshot.ModelConfigID != id || snapshot.ModelVersion != c.Version || snapshot.SourceModel != c.SourceModel || snapshot.PublicName != c.PublicNameValue() || len(snapshot.Groups()) == 0 || len(snapshot.ChannelIDs()) == 0 {
		return target, false, nil
	}
	modality := model.ZTAPIModelModality(c.SourceModel)
	var price model.ZTAPIModelPriceSource
	err = tx.First(&price, snapshot.PriceSourceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return target, false, nil
	}
	if err != nil {
		return target, false, err
	}
	if price.ModelConfigID != id || price.SourceModel != c.SourceModel || price.Version == 0 {
		return target, false, nil
	}
	if modality == model.ZTAPIModalityImage || modality == model.ZTAPIModalityVideo {
		mediaTarget, active := ztapiMediaProbeTargetFromEvidence(c, snapshot, price, state, stream)
		if !active || mediaTarget.Modality != modality {
			return target, false, nil
		}
		return mediaTarget, true, nil
	}
	embedding := modality == model.ZTAPIModalityEmbedding
	values := []float64{snapshot.InputPricePerMillion, snapshot.OutputPricePerMillion, snapshot.CacheReadRatio, snapshot.CacheCreationRatio, snapshot.CacheCreation5mRatio, snapshot.CacheCreation1hRatio, snapshot.ImageRatio, snapshot.AudioRatio, snapshot.AudioCompletionRatio}
	if embedding {
		if stream || snapshot.OutputPricePerMillion != 0 {
			return target, false, nil
		}
		values = []float64{snapshot.InputPricePerMillion}
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return target, false, nil
		}
	}
	preview, err := model.BuildZTAPIModelPricePreview(&price)
	if err != nil {
		return target, false, nil
	}
	for _, pair := range []struct {
		stored   float64
		expected string
	}{{c.InputCostPerMillion, preview.InputCostUSDPerMillion}, {c.OutputCostPerMillion, preview.OutputCostUSDPerMillion}, {snapshot.InputPricePerMillion, preview.InputSaleUSDPerMillion}, {snapshot.OutputPricePerMillion, preview.OutputSaleUSDPerMillion}} {
		value, err := decimal.NewFromString(pair.expected)
		if err != nil || math.IsNaN(pair.stored) || math.IsInf(pair.stored, 0) || !decimal.NewFromFloat(pair.stored).Round(8).Equal(value.Round(8)) {
			return target, false, nil
		}
	}
	input, err := ztapiHealthPriceNanoUSD(preview.InputCostUSDPerMillion)
	if err != nil {
		return target, false, nil
	}
	output, err := ztapiHealthPriceNanoUSD(preview.OutputCostUSDPerMillion)
	if embedding && preview.OutputCostUSDPerMillion == "0.0000000000" {
		output, err = 0, nil
	}
	if err != nil {
		return target, false, nil
	}
	protocol, validProtocol := ztapiHealthProbeProtocol(c.SourceModel, modality, requestedProtocol)
	if !validProtocol {
		return target, false, nil
	}
	target = model.ZTAPIProbeTarget{ModelID: id, Stream: stream, SourceModel: c.SourceModel, PublicModel: snapshot.PublicName, Protocol: protocol, Generation: state.Generation, ConfigVersion: c.Version, PublicationSnapshotID: snapshot.ID, PriceSourceID: price.ID, PriceSourceVersion: price.Version, InputNanoUSDPerMillion: input, OutputNanoUSDPerMillion: output}
	return target, true, nil
}

func ztapiHealthProbeProtocol(sourceModel, modality, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	defaultProtocol := "chat"
	if modality == model.ZTAPIModalityEmbedding {
		defaultProtocol = "embeddings"
	} else if common.IsOpenAIResponseOnlyModel(strings.ToLower(sourceModel)) {
		defaultProtocol = "responses"
	}
	if requested == "" {
		return defaultProtocol, true
	}
	if modality == model.ZTAPIModalityEmbedding {
		return requested, requested == "embeddings"
	}
	if modality == model.ZTAPIModalityText {
		switch requested {
		case "chat", "responses", "claude", "gemini":
			return requested, true
		}
	}
	return requested, false
}

func ztapiHealthPriceNanoUSD(raw string) (int64, error) {
	value, err := decimal.NewFromString(raw)
	if err != nil || !value.IsPositive() {
		return 0, errors.New("invalid probe cost")
	}
	n := value.Mul(decimal.NewFromInt(1_000_000_000)).Ceil().BigInt()
	if !n.IsInt64() {
		return 0, errors.New("probe cost overflow")
	}
	return n.Int64(), nil
}

type ztapiMediaProbeCandidate struct {
	payload string
	cost    int64
}

func ztapiMediaProbeTargetFromEvidence(c model.ZTAPIModelConfig, snapshot model.ZTAPIModelPublicationSnapshot, price model.ZTAPIModelPriceSource, state model.ZTAPIHealthState, stream bool) (model.ZTAPIProbeTarget, bool) {
	empty := model.ZTAPIProbeTarget{ModelID: c.ID, Stream: stream}
	if stream || snapshot.MediaPriceContractJSON == "" || snapshot.MediaPriceContractJSON != price.MediaPriceContractJSON {
		return empty, false
	}
	pricing, err := types.ParseZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
	if err != nil {
		return empty, false
	}
	modality := pricing.Modality
	var candidate ztapiMediaProbeCandidate
	probeProtocol := ""
	switch modality {
	case model.ZTAPIModalityImage:
		imageProtocol, canonical, parseErr := types.ParseZTAPIImageProtocolContract(snapshot.ImageProtocolContractJSON)
		if parseErr != nil || canonical != snapshot.ImageProtocolContractJSON || imageProtocol.ProviderModel != c.SourceModel || types.ValidateZTAPIImagePriceProtocolCompatibility(pricing, imageProtocol) != nil {
			return empty, false
		}
		candidate, err = cheapestZTAPIImageProbe(pricing, imageProtocol)
		switch imageProtocol.WireProtocol {
		case "", types.ZTAPIImageWireProtocolOpenAIImages:
			probeProtocol = "images"
		case types.ZTAPIImageWireProtocolGeminiGenerateContent:
			probeProtocol = "gemini"
		default:
			return empty, false
		}
	case model.ZTAPIModalityVideo:
		protocol, canonical, parseErr := types.ParseZTAPIVideoProtocolContract(snapshot.VideoProtocolContractJSON)
		if parseErr != nil || canonical != snapshot.VideoProtocolContractJSON || protocol.ProviderModel != c.SourceModel || types.ValidateZTAPIVideoPriceProtocolCompatibility(pricing, protocol) != nil {
			return empty, false
		}
		candidate, err = cheapestZTAPIVideoProbe(pricing, protocol)
		probeProtocol = "video-tasks"
	default:
		return empty, false
	}
	if err != nil || candidate.cost <= 0 || candidate.payload == "" {
		return empty, false
	}
	protocol, operation := probeProtocol, types.ZTAPIHealthOperationImageGenerate
	if modality == model.ZTAPIModalityVideo {
		protocol, operation = "video-tasks", types.ZTAPIHealthOperationVideoSubmit
	}
	return model.ZTAPIProbeTarget{
		ModelID: c.ID, Stream: false, SourceModel: c.SourceModel, PublicModel: snapshot.PublicName,
		Protocol: protocol, Modality: modality, Operation: operation, ProbePayloadJSON: candidate.payload,
		Generation: state.Generation, ConfigVersion: c.Version, PublicationSnapshotID: snapshot.ID,
		PriceSourceID: price.ID, PriceSourceVersion: price.Version, FixedCostNanoUSD: candidate.cost,
	}, true
}

func ztapiProbeMaximumCostNanoUSD(costs, maximum map[string]string) (int64, error) {
	total := decimal.Zero
	for dimension, rawCost := range costs {
		rawMaximum, ok := maximum[dimension]
		cost, costErr := decimal.NewFromString(rawCost)
		quantity, quantityErr := decimal.NewFromString(rawMaximum)
		if !ok || costErr != nil || quantityErr != nil || cost.IsNegative() || quantity.IsNegative() {
			return 0, errors.New("media probe cost evidence is invalid")
		}
		total = total.Add(cost.Mul(quantity).Mul(decimal.NewFromInt(1000)))
	}
	value := total.Ceil().BigInt()
	if !value.IsInt64() {
		return 0, errors.New("media probe cost exceeds supported range")
	}
	return value.Int64(), nil
}

func chooseZTAPIMediaProbeCandidate(current, next ztapiMediaProbeCandidate) ztapiMediaProbeCandidate {
	if current.cost == 0 || next.cost < current.cost || next.cost == current.cost && next.payload < current.payload {
		return next
	}
	return current
}

func cheapestZTAPIImageProbe(pricing types.ZTAPIMediaPriceContract, protocol types.ZTAPIImageProtocolContract) (ztapiMediaProbeCandidate, error) {
	best := ztapiMediaProbeCandidate{}
	for _, authority := range protocol.Reservations {
		costs := map[string]string{}
		if _, tiered := pricing.Rules[0].Conditions["prompt_tokens_tier"]; tiered {
			input, err := strconv.ParseInt(authority.MaximumDimensions["input_tokens"], 10, 64)
			if err != nil || input < 0 {
				return best, errors.New("image probe tier evidence is invalid")
			}
			tier := "lte_200k"
			if input > 200000 {
				tier = "gt_200k"
			}
			rule, err := types.SelectZTAPIMediaPriceRuleFromContract(pricing, types.ZTAPIMediaPriceSelector{Modality: model.ZTAPIModalityImage, Conditions: map[string]string{"prompt_tokens_tier": tier}})
			if err != nil {
				return best, err
			}
			costs = rule.CostUSD
		} else {
			for _, rule := range pricing.Rules {
				for dimension, cost := range rule.CostUSD {
					if _, reported := protocol.Usage.Fields[dimension]; reported {
						costs[dimension] = cost
					}
				}
			}
			if len(costs) != len(protocol.Usage.Fields) {
				return best, errors.New("image probe pricing does not cover reported dimensions")
			}
		}
		cost, err := ztapiProbeMaximumCostNanoUSD(costs, authority.MaximumDimensions)
		if err != nil {
			return best, err
		}
		payload, err := common.Marshal(map[string]any{"size": authority.Size, "quality": authority.Quality, "response_format": authority.ResponseFormat, "n": authority.N})
		if err != nil {
			return best, err
		}
		best = chooseZTAPIMediaProbeCandidate(best, ztapiMediaProbeCandidate{payload: string(payload), cost: cost})
	}
	return best, nil
}

func cheapestZTAPIVideoProbe(pricing types.ZTAPIMediaPriceContract, protocol types.ZTAPIVideoProtocolContract) (ztapiMediaProbeCandidate, error) {
	best := ztapiMediaProbeCandidate{}
	for _, authority := range protocol.Reservations {
		if authority.ContainsVideoInput {
			continue
		}
		conditions := map[string]string{"contains_video_input": "false"}
		if _, hasResolution := pricing.Rules[0].Conditions["resolution"]; hasResolution {
			conditions["resolution"] = authority.Resolution
		}
		rule, err := types.SelectZTAPIMediaPriceRuleFromContract(pricing, types.ZTAPIMediaPriceSelector{Modality: model.ZTAPIModalityVideo, Conditions: conditions})
		if err != nil {
			return best, err
		}
		cost, err := ztapiProbeMaximumCostNanoUSD(rule.CostUSD, authority.MaximumDimensions)
		if err != nil {
			return best, err
		}
		payload, err := common.Marshal(map[string]any{"size": authority.Resolution, "duration": authority.DurationSeconds})
		if err != nil {
			return best, err
		}
		best = chooseZTAPIMediaProbeCandidate(best, ztapiMediaProbeCandidate{payload: string(payload), cost: cost})
	}
	return best, nil
}

func productionZTAPIProbeTargets(ctx context.Context, db *gorm.DB, limit int, now time.Time) ([]model.ZTAPIProbeTarget, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid production probe batch")
	}
	var result []model.ZTAPIProbeTarget
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cursor := model.ZTAPIHealthWorkerStatus{Component: "scheduler", Code: "enumerating"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&cursor).Error; err != nil {
			return err
		}
		if err := tx.Model(&cursor).UpdateColumn("cursor_model_id", gorm.Expr("cursor_model_id")).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cursor, "component = ?", "scheduler").Error; err != nil {
			return err
		}
		var configs []model.ZTAPIModelConfig
		q := tx.Select("id").Where("published = ? AND publication_snapshot_id > ?", true, 0)
		if cursor.CursorStream {
			q = q.Where("id > ?", cursor.CursorModelID)
		} else {
			q = q.Where("id >= ?", cursor.CursorModelID)
		}
		if err := q.Order("id ASC").Limit(limit).Find(&configs).Error; err != nil {
			return err
		}
		if len(configs) == 0 {
			cursor.CursorModelID, cursor.CursorStream = 0, false
		}
		for _, c := range configs {
			for _, stream := range []bool{false, true} {
				if c.ID == cursor.CursorModelID && !stream {
					continue
				}
				target, active, err := productionZTAPIProbeTarget(ctx, tx, c.ID, stream, false)
				if err != nil {
					return err
				}
				cursor.CursorModelID, cursor.CursorStream = c.ID, stream
				if active {
					result = append(result, target)
				}
				if len(result) == limit {
					break
				}
			}
			if len(result) == limit {
				break
			}
		}
		cursor.Code, cursor.UpdatedAt = "enumerated", now.Unix()
		return tx.Save(&cursor).Error
	})
	return result, err
}

func productionZTAPIProbeSamples(ctx context.Context, db, logs *gorm.DB, target model.ZTAPIProbeTarget, now time.Time) ([]model.ZTAPIProbeInputSample, error) {
	if target.Modality == model.ZTAPIModalityImage || target.Modality == model.ZTAPIModalityVideo {
		return nil, nil
	}
	if logs == nil {
		return nil, errors.New("probe consumption database unavailable")
	}
	// Last 1000 positive consumptions over 24h, no prompts, keys, IPs or Other.
	var rows []model.Log
	err := logs.WithContext(ctx).Select("request_id", "user_id", "prompt_tokens").Where("type = ? AND quota > ? AND prompt_tokens > ? AND model_name IN ? AND created_at > ? AND created_at <= ?", model.LogTypeConsume, 0, 0, []string{target.PublicModel, target.SourceModel}, now.Add(-24*time.Hour).Unix(), now.Unix()).Order("id DESC").Limit(1000).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.RequestId != "" {
			ids = append(ids, row.RequestId)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var requests []model.ZTAPIHealthRequest
	err = db.WithContext(ctx).Select("request_id", "user_id").Where("model_id = ? AND source = ? AND completed = ? AND request_id IN ?", target.ModelID, "real", true, ids).Limit(2000).Find(&requests).Error
	if err != nil {
		return nil, err
	}
	type identity struct {
		request string
		user    int
	}
	real := make(map[identity]bool, len(requests))
	for _, request := range requests {
		real[identity{request.RequestID, request.UserID}] = true
	}
	var samples []model.ZTAPIProbeInputSample
	for _, row := range rows {
		if real[identity{row.RequestId, row.UserId}] {
			samples = append(samples, model.ZTAPIProbeInputSample{ModelID: target.ModelID, Source: "real", InputTokens: int64(row.PromptTokens), ConsumptionPositive: true})
		}
	}
	return samples, nil
}

type ZTAPIHealthWorker struct {
	backend ZTAPIHealthWorkerBackend
	config  ZTAPIHealthWorkerConfig
	running atomic.Bool
	done    chan struct{}
}

func NewZTAPIHealthWorker(backend ZTAPIHealthWorkerBackend, config ZTAPIHealthWorkerConfig) (*ZTAPIHealthWorker, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Interval < time.Second || config.Interval > time.Hour || config.RequestTimeout < time.Second || config.RequestTimeout > 3*time.Minute ||
		config.AlertTimeout < time.Second || config.AlertTimeout > 30*time.Second || config.OutboxTimeout < config.AlertTimeout || config.OutboxTimeout > time.Minute ||
		config.TickTimeout < config.RequestTimeout+4*config.OutboxTimeout || config.TickTimeout > 15*time.Minute || config.LeaseDuration <= config.RequestTimeout || config.LeaseDuration > 5*time.Minute ||
		config.BatchSize < 1 || config.BatchSize > 100 || config.InitialAllocationNanoUSD < 0 || config.OrphanAfter < 0 || (config.OrphanAfter > 0 && config.OrphanAfter <= config.LeaseDuration) {
		return nil, errors.New("invalid bounded health worker configuration")
	}
	return &ZTAPIHealthWorker{backend: backend, config: config, done: make(chan struct{})}, nil
}

// StartZTAPIHealthWorker is opt-in wiring for server/main.go's owner. One loop,
// no cron or overlap; cancellation stops the loop and Done confirms shutdown.
func StartZTAPIHealthWorker(ctx context.Context, backend ZTAPIHealthWorkerBackend, config ZTAPIHealthWorkerConfig) (*ZTAPIHealthWorker, error) {
	w, err := NewZTAPIHealthWorker(backend, config)
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.config.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if ctx.Err() != nil {
				return
			}
			if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
				common.SysError("ztapi health worker tick failed; pending durable jobs will be retried")
			}
		}
	}()
	return w, nil
}

func (w *ZTAPIHealthWorker) Done() <-chan struct{} { return w.done }

func (w *ZTAPIHealthWorker) status(ctx context.Context, code, id string) {
	if w.backend.OperationalStatus != nil {
		w.backend.OperationalStatus(ctx, code, id)
	}
}

func (w *ZTAPIHealthWorker) RunOnce(ctx context.Context) (resultErr error) {
	if !w.running.CompareAndSwap(false, true) {
		return ErrZTAPIHealthWorkerBusy
	}
	defer w.running.Store(false)
	ctx, cancel := context.WithTimeout(ctx, w.config.TickTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	w.status(ctx, "worker_tick_running", "")
	defer func() {
		// A terminal heartbeat is database-only and bounded even on shutdown.
		// It reports loop progress, never model health or alert receipt.
		statusCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		code := "worker_ready"
		if resultErr != nil {
			code = "worker_tick_error"
		}
		w.status(statusCtx, code, "")
	}()
	// Alert failures must not suppress the independent unpublish queue or probes.
	var failures []error
	if w.config.OrphanAfter > 0 && w.backend.SweepOrphans != nil {
		sweepCtx, stop := context.WithTimeout(ctx, w.config.OutboxTimeout)
		if _, err := w.backend.SweepOrphans(sweepCtx, w.config.Now().Add(-w.config.OrphanAfter), w.config.BatchSize); err != nil {
			failures = append(failures, err)
		}
		stop()
	}
	for _, kind := range []string{"unpublish", "route_alert", "alert", "recovery_alert", "alert_test", "coverage"} {
		outboxCtx, stop := context.WithTimeout(ctx, w.config.OutboxTimeout)
		if err := w.runOutbox(outboxCtx, kind); err != nil {
			failures = append(failures, err)
		}
		stop()
	}
	if err := w.runProbes(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (w *ZTAPIHealthWorker) runProbes(ctx context.Context) error {
	if w.backend.Verifications != nil && w.backend.Verifications.DB != nil {
		if !w.config.VerificationProbesEnabled {
			w.status(ctx, "verification_probes_disabled", "")
			return nil
		}
		expired, err := w.backend.Verifications.ExpireDispatches(ctx, w.config.Now(), w.config.BatchSize)
		if err != nil {
			return err
		}
		for _, verificationCase := range expired {
			w.status(ctx, "verification_unknown", verificationCase.ID)
		}
		verificationCase, err := w.backend.Verifications.Claim(ctx, w.config.Now(), w.config.LeaseDuration)
		if err != nil {
			return err
		}
		if verificationCase == nil {
			w.status(ctx, "verification_idle", "")
			return nil
		}
		return w.runVerificationCase(ctx, verificationCase)
	}
	if w.backend.Probes == nil || w.backend.Probes.DB == nil {
		w.status(ctx, "probe_storage_missing", "")
		return nil
	}
	expired, err := w.backend.Probes.ExpireDispatches(ctx, w.config.Now(), w.config.BatchSize)
	if err != nil {
		return err
	}
	for _, job := range expired {
		w.status(ctx, "abandoned_dispatch", job.ID)
	}
	if !w.config.SyntheticProbesEnabled {
		w.status(ctx, "synthetic_probes_disabled", "")
		return nil
	}
	identityValid := w.config.ProbeIdentityValidated
	if w.backend.ValidateProbeIdentity != nil {
		var code string
		identityValid, code, err = w.backend.ValidateProbeIdentity(ctx)
		if err != nil {
			w.status(ctx, "probe_identity_check_error", "")
			return err
		}
		if !identityValid {
			w.status(ctx, code, "")
			return nil
		}
	}
	if w.config.ProbeKey == "" || w.config.ProbeUserID <= 0 || !identityValid {
		w.status(ctx, "probe_identity_missing", "")
		return nil
	}
	if _, err := ztapiHealthRelayEndpoint(w.config.RelayBaseURL, "chat", "", false); err != nil {
		w.status(ctx, "relay_url_invalid", "")
		return nil
	}
	if w.backend.ListTargets == nil || w.backend.CheckProbe == nil {
		w.status(ctx, "probe_engine_callbacks_missing", "")
		return nil
	}
	budget, err := w.backend.Probes.Budget(ctx)
	if err != nil {
		w.status(ctx, "probe_allocation_missing", "")
		return err
	}
	if budget.AllocatedNanoUSD <= 0 || budget.AccountedNanoUSD >= budget.AllocatedNanoUSD {
		w.status(ctx, "probe_budget_exhausted", "")
		return nil
	}
	w.status(ctx, "probe_ready", "")
	targets, err := w.backend.ListTargets(ctx, w.config.BatchSize)
	if err != nil {
		return err
	}
	if len(targets) > w.config.BatchSize {
		return errors.New("probe target enumeration exceeded limit")
	}
	for _, target := range targets {
		if _, err := w.backend.Probes.Enqueue(ctx, target, w.config.Now()); err != nil {
			return err
		}
	}
	for i := 0; i < w.config.BatchSize; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < w.config.RequestTimeout+5*time.Second {
			w.status(ctx, "probe_deferred_tick_budget", "")
			break
		}
		job, err := w.backend.Probes.Claim(ctx, w.config.Now(), w.config.LeaseDuration)
		if err != nil {
			return err
		}
		if job == nil {
			break
		}
		dispatched, send, err := w.backend.Probes.Dispatch(ctx, job.ID, job.LeaseToken, w.config.Now(), w.config.LeaseDuration, w.backend.CheckProbe)
		if err != nil {
			return err
		}
		if !send {
			if dispatched.ResultCode == "budget_exhausted" {
				w.status(ctx, "probe_budget_exhausted", dispatched.ID)
			}
			continue
		}
		// From this point onward any interruption is ambiguous: never requeue.
		result := performZTAPIHealthProbe(ctx, w.config, dispatched)
		state, code := "unknown", result.Code
		if result.Complete {
			state = "completed"
		}
		finalized, err := w.waitProbeFinalized(ctx, dispatched, result.RequestID)
		if err != nil || !finalized {
			state, code = "unknown", "probe_final_result_missing"
		}
		observed, costErr := model.ZTAPIProbeEstimateNanoUSD(result.InputTokens, result.OutputTokens, dispatched.InputNanoUSDPerMillion, dispatched.OutputNanoUSDPerMillion)
		if costErr != nil {
			state, code, observed = "unknown", "probe_usage_invalid", math.MaxInt64
		}
		if err := w.backend.Probes.Finish(ctx, dispatched.ID, dispatched.LeaseToken, state, code, observed); err != nil {
			return err
		}
		if state == "unknown" || code != "functional_pass" {
			w.status(ctx, code, dispatched.ID)
		}
	}
	return nil
}

func (w *ZTAPIHealthWorker) runVerificationCase(ctx context.Context, verificationCase *model.ZTAPIHealthVerificationCase) error {
	if verificationCase == nil {
		return nil
	}
	deferClaim := func(code string) error {
		w.status(ctx, code, verificationCase.ID)
		return w.backend.Verifications.DeferClaim(ctx, verificationCase.ID, verificationCase.LeaseToken, w.config.Now(), time.Minute)
	}
	if !supportedZTAPIVerificationProtocol(verificationCase.Protocol) ||
		!supportedZTAPIVerificationProtocol(ztapiVerificationEntryProtocol(verificationCase.RouteIdentity())) {
		w.status(ctx, "verification_protocol_invalid", verificationCase.ID)
		return w.backend.Verifications.CancelClaim(ctx, verificationCase.ID, verificationCase.LeaseToken, verificationCase.Generation, "protocol_invalid", w.config.Now())
	}
	if w.backend.ValidateProbeIdentity == nil || w.backend.LoadVerificationTarget == nil || w.backend.CheckVerificationDispatch == nil || w.backend.LoadVerificationEvidence == nil {
		return deferClaim("verification_callbacks_missing")
	}
	identityValid, code, err := w.backend.ValidateProbeIdentity(ctx)
	if err != nil {
		if deferErr := deferClaim("probe_identity_check_error"); deferErr != nil {
			return errors.Join(err, deferErr)
		}
		return err
	}
	if !identityValid || w.config.ProbeKey == "" || w.config.ProbeUserID <= 0 {
		if code == "" {
			code = "probe_identity_missing"
		}
		return deferClaim(code)
	}
	target, active, err := w.backend.LoadVerificationTarget(ctx, *verificationCase)
	if err != nil {
		if deferErr := deferClaim("verification_target_error"); deferErr != nil {
			return errors.Join(err, deferErr)
		}
		return err
	}
	if !active || target.ModelID != verificationCase.ModelID || target.Protocol != verificationCase.Protocol || target.Stream != verificationCase.Stream || target.Generation != verificationCase.Generation {
		w.status(ctx, "verification_route_changed", verificationCase.ID)
		return w.backend.Verifications.CancelClaim(ctx, verificationCase.ID, verificationCase.LeaseToken, verificationCase.Generation, "route_changed", w.config.Now())
	}
	entryProtocol := strings.TrimSpace(target.EntryProtocol)
	if entryProtocol == "" {
		entryProtocol = target.Protocol
	}
	if _, err := ztapiHealthRelayEndpoint(w.config.RelayBaseURL, entryProtocol, target.PublicModel, target.Stream); err != nil {
		return deferClaim("relay_url_invalid")
	}
	dispatched, send, err := w.backend.Verifications.BeginDispatch(
		ctx, verificationCase.ID, verificationCase.LeaseToken, verificationCase.Generation,
		w.config.Now(), w.config.LeaseDuration, w.backend.CheckVerificationDispatch,
	)
	if err != nil {
		return err
	}
	if !send {
		code := dispatched.Result
		if code == "" {
			code = "verification_not_dispatched"
		}
		w.status(ctx, code, dispatched.ID)
		return nil
	}

	job := model.ZTAPIProbeJob{
		ID:               dispatched.ID,
		ZTAPIProbeTarget: target,
		Source:           "probe",
		State:            dispatched.State,
		LeaseToken:       dispatched.LeaseToken,
		LeaseUntil:       dispatched.LeaseUntil,
		DispatchAt:       dispatched.DispatchAt,
		ReservedNanoUSD:  dispatched.ReservedNanoUSD,
		EstimateNanoUSD:  dispatched.EstimateNanoUSD,
	}
	result := performZTAPIVerificationProbe(ctx, w.config, job, dispatched.ProbeRequestID)
	evidence, evidenceErr := w.waitVerificationEvidence(ctx, dispatched)
	if ctx.Err() != nil {
		// Dispatch may already have reached the provider. Leave the durable case
		// in dispatching; the next sweep will close it as unknown without resend.
		return ctx.Err()
	}
	if evidenceErr != nil || evidence == nil || !matchingZTAPIVerificationEvidence(dispatched, evidence) {
		w.status(ctx, "probe_final_result_missing", dispatched.ID)
		completion := model.ZTAPIHealthProbeCompletion{CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation, ProbeRequestID: dispatched.ProbeRequestID}
		return w.backend.Verifications.CompleteUnknown(ctx, completion, "probe_missing", w.config.Now())
	}
	observed := dispatched.EstimateNanoUSD
	if target.FixedCostNanoUSD == 0 {
		observed, err = model.ZTAPIProbeEstimateNanoUSD(evidence.InputTokens, evidence.OutputTokens, target.InputNanoUSDPerMillion, target.OutputNanoUSDPerMillion)
		if err != nil {
			w.status(ctx, "probe_usage_invalid", dispatched.ID)
			completion := model.ZTAPIHealthProbeCompletion{CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation, ProbeRequestID: dispatched.ProbeRequestID}
			return w.backend.Verifications.CompleteUnknown(ctx, completion, "usage_invalid", w.config.Now())
		}
	}
	_, err = w.backend.Verifications.CompleteProbe(ctx, model.ZTAPIHealthProbeCompletion{
		CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
		ProbeRequestID: dispatched.ProbeRequestID, Result: evidence.Result, ObservedNanoUSD: observed,
	}, w.config.Now())
	if err == nil && (result.Code != "functional_pass" || !result.Complete) {
		w.status(ctx, "probe_http_projection_disagreed", dispatched.ID)
	}
	return err
}

func supportedZTAPIVerificationProtocol(protocol string) bool {
	switch strings.TrimSpace(protocol) {
	case "chat", "responses", "claude", "gemini", "embeddings", "images", "video-tasks":
		return true
	default:
		return false
	}
}

func matchingZTAPIVerificationEvidence(verificationCase model.ZTAPIHealthVerificationCase, evidence *ZTAPIVerificationProbeEvidence) bool {
	if evidence == nil || evidence.RequestID != verificationCase.ProbeRequestID || (evidence.Result != "healthy" && evidence.Result != "failure") || evidence.InputTokens < 0 || evidence.OutputTokens < 0 {
		return false
	}
	route := verificationCase.RouteIdentity()
	return evidence.Route.ModelID == route.ModelID && evidence.Route.ChannelID == route.ChannelID &&
		ztapiVerificationEntryProtocol(evidence.Route) == ztapiVerificationEntryProtocol(route) &&
		strings.TrimSpace(evidence.Route.Protocol) == strings.TrimSpace(route.Protocol) && evidence.Route.Stream == route.Stream &&
		evidence.Route.CredentialVersion.String() == route.CredentialVersion.String() && evidence.Route.Generation == route.Generation
}

func (w *ZTAPIHealthWorker) waitVerificationEvidence(ctx context.Context, verificationCase model.ZTAPIHealthVerificationCase) (*ZTAPIVerificationProbeEvidence, error) {
	timeout := ztapiHealthProbeFinalizationTimeout
	if remaining := time.UnixMilli(verificationCase.LeaseUntil).Sub(w.config.Now()); remaining < timeout {
		timeout = remaining
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(ztapiHealthProbeFinalizationInterval)
	defer ticker.Stop()
	for {
		evidence, err := w.backend.LoadVerificationEvidence(readCtx, verificationCase)
		if err != nil || evidence != nil {
			return evidence, err
		}
		select {
		case <-readCtx.Done():
			return nil, readCtx.Err()
		case <-ticker.C:
		}
	}
}

// Relay finalization can commit after the HTTP body is consumed. Poll only its
// read projection, within the tick/lease budget; never repeat the paid request.
func (w *ZTAPIHealthWorker) waitProbeFinalized(ctx context.Context, job model.ZTAPIProbeJob, requestID string) (bool, error) {
	if w.backend.ProbeFinalized == nil {
		return false, nil
	}
	timeout := ztapiHealthProbeFinalizationTimeout
	if remaining := time.UnixMilli(job.LeaseUntil).Sub(w.config.Now()); remaining < timeout {
		timeout = remaining
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(ztapiHealthProbeFinalizationInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		finalized, err := w.backend.ProbeFinalized(ctx, job, requestID)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err != nil || finalized {
			return finalized, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func validZTAPIHealthWebhook(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func (w *ZTAPIHealthWorker) runOutbox(ctx context.Context, kind string) error {
	if (kind == "alert" || kind == "route_alert" || kind == "recovery_alert" || kind == "alert_test") && !validZTAPIHealthAlertRecipient(w.config) {
		w.status(ctx, "alert_recipient_missing_or_invalid", "")
		return nil
	}
	if kind == "coverage" && w.backend.RecordCoverage == nil {
		return nil
	}
	if w.backend.ClaimOutbox == nil || w.backend.FinishOutbox == nil || (kind == "unpublish" && w.backend.Unpublish == nil) {
		w.status(ctx, "outbox_callbacks_missing", kind)
		return nil
	}
	for i := 0; i < w.config.BatchSize; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := w.backend.ClaimOutbox(ctx, kind, w.config.Now(), w.config.LeaseDuration, 1)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		if len(items) != 1 {
			return errors.New("outbox claim exceeded limit")
		}
		item := items[0]
		delivery := ZTAPIHealthDelivery{}
		if kind == "coverage" {
			err := w.backend.RecordCoverage(ctx, item)
			delivery.Accepted, delivery.Code = err == nil, "admin_status_recorded"
			if err != nil {
				delivery.Code = "admin_status_retry"
			}
		} else if kind == "unpublish" {
			callCtx, cancel := context.WithTimeout(ctx, w.config.AlertTimeout)
			err := w.backend.Unpublish(callCtx, item)
			cancel()
			delivery.Accepted = err == nil
			if err == nil {
				// ProcessUnpublish already commits the catalog change and ACK.
				continue
			} else {
				delivery.Code = "unpublish_retry"
			}
		} else {
			delivery.Accepted, delivery.Code, delivery.Receipt = deliverZTAPIHealthAlert(ctx, w.config, item)
		}
		if !delivery.Accepted {
			delivery.RetryAt = w.config.Now().Add(ztapiHealthAlertBackoff(item.Attempts))
		}
		if err := w.backend.FinishOutbox(ctx, item, delivery); err != nil {
			return err
		}
		if !delivery.Accepted {
			w.status(ctx, delivery.Code, strconv.FormatInt(item.ID, 10))
			if kind == "alert" || kind == "route_alert" || kind == "recovery_alert" || kind == "alert_test" {
				common.SysError(fmt.Sprintf("ztapi health alert delivery pending: outbox=%d code=%s", item.ID, delivery.Code))
			}
		} else if kind == "alert" || kind == "route_alert" || kind == "recovery_alert" || kind == "alert_test" {
			w.status(ctx, delivery.Code, strconv.FormatInt(item.ID, 10))
		}
	}
	return nil
}

func ztapiHealthAlertBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		return time.Hour
	}
	d := 30 * time.Second * time.Duration(1<<uint(attempt-1))
	if d > time.Hour {
		return time.Hour
	}
	return d
}

func ztapiHealthHTTPClient(timeout time.Duration, transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: timeout}).DialContext, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, DisableKeepAlives: true}
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func sendZTAPIHealthAlert(ctx context.Context, config ZTAPIHealthWorkerConfig, item ZTAPIHealthWorkItem) (bool, string) {
	ok, code, _ := deliverZTAPIHealthAlert(ctx, config, item)
	return ok, code
}

func deliverZTAPIHealthAlert(ctx context.Context, config ZTAPIHealthWorkerConfig, item ZTAPIHealthWorkItem) (bool, string, string) {
	if config.TelegramConfigured || config.TelegramBotToken != "" || config.TelegramChatID != "" {
		return sendZTAPIHealthTelegram(ctx, config, item)
	}
	ok, code := sendZTAPIHealthWebhook(ctx, config, item)
	return ok, code, ""
}

func sendZTAPIHealthWebhook(ctx context.Context, config ZTAPIHealthWorkerConfig, item ZTAPIHealthWorkItem) (bool, string) {
	if !validZTAPIHealthWebhook(config.AlertWebhookURL) {
		return false, "alert_recipient_missing_or_invalid"
	}
	// Fixed allowlist: no free-form incident reason, prompts,
	// credentials, lease token, upstream bodies or personal contact data.
	payload := struct {
		Event      string                   `json:"event"`
		OutboxID   int64                    `json:"outbox_id"`
		IncidentID int64                    `json:"incident_id"`
		ModelID    int                      `json:"model_id"`
		Generation uint64                   `json:"generation"`
		EventID    int64                    `json:"event_id"`
		Details    ZTAPIHealthAlertMetadata `json:"details"`
	}{"ztapi.health.incident", item.ID, item.IncidentID, item.ModelID, item.Generation, item.EventID, ztapiHealthSafeAlertMetadata(item, config.Now())}
	switch item.Kind {
	case "route_alert":
		payload.Event = "ztapi.health.route_degraded"
	case "recovery_alert":
		payload.Event = "ztapi.health.recovered"
	case "alert_test":
		payload.Event = "ztapi.health.test"
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return false, "alert_payload_error"
	}
	ctx, cancel := context.WithTimeout(ctx, config.AlertTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.AlertWebhookURL, bytes.NewReader(body))
	if err != nil {
		return false, "alert_request_invalid"
	}
	req.Header.Set("Content-Type", "application/json")
	dedup := item.DedupKey
	if dedup == "" {
		dedup = fmt.Sprintf("outbox:%d", item.ID)
	}
	req.Header.Set("Idempotency-Key", fmt.Sprintf("ztapi-health-%x", sha256.Sum256([]byte(dedup))))
	resp, err := ztapiHealthHTTPClient(config.AlertTimeout, config.AlertTransport).Do(req)
	if err != nil {
		return false, "alert_transport_error"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, "http_2xx_accepted"
	}
	return false, "alert_http_status"
}

func ztapiHealthRelayEndpoint(base, protocol, publicModel string, stream bool) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid loopback relay URL")
	}
	// Literal loopback only: neither DNS rebinding nor environment proxies may
	// redirect the dedicated credential to a public upstream.
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("relay URL must be literal loopback")
	}
	path := strings.TrimRight(u.Path, "/")
	if path != "" && path != "/v1" {
		return "", errors.New("relay URL path invalid")
	}
	switch protocol {
	case "images":
		u.Path = "/v1/images/generations"
	case "video-tasks":
		u.Path = "/v1/videos"
	case "embeddings":
		u.Path = "/v1/embeddings"
	case "chat":
		u.Path = "/v1/chat/completions"
	case "responses":
		u.Path = "/v1/responses"
	case "claude":
		u.Path = "/v1/messages"
	case "gemini":
		if strings.TrimSpace(publicModel) == "" || strings.ContainsAny(publicModel, "/?#") {
			return "", errors.New("probe model invalid")
		}
		action := ":generateContent"
		if stream {
			action = ":streamGenerateContent"
			u.RawQuery = "alt=sse"
		}
		u.Path = "/v1/models/" + url.PathEscape(strings.TrimSpace(publicModel)) + action
	default:
		return "", errors.New("probe protocol invalid")
	}
	u.RawPath = ""
	return u.String(), nil
}

type ztapiHealthProbeResult struct {
	Code         string
	Complete     bool
	InputTokens  int64
	OutputTokens int64
	RequestID    string
}

type ztapiHealthWireUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

type ztapiHealthWireResponse struct {
	Status  string                `json:"status"`
	Usage   *ztapiHealthWireUsage `json:"usage"`
	Error   any                   `json:"error"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

type ztapiHealthClaudeResponse struct {
	Type       string `json:"type"`
	StopReason string `json:"stop_reason"`
	Error      any    `json:"error"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Message *struct {
		Usage struct {
			InputTokens int64 `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
}

type ztapiHealthGeminiResponse struct {
	Error      any `json:"error"`
	Candidates []struct {
		FinishReason string `json:"finishReason"`
		Content      struct {
			Parts []struct {
				Text       string `json:"text"`
				InlineData struct {
					MIMEType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

func ztapiHealthOutputText(w ztapiHealthWireResponse) string {
	var b strings.Builder
	for _, output := range w.Output {
		if output.Type != "message" {
			continue
		}
		for _, part := range output.Content {
			if part.Type == "output_text" {
				b.WriteString(part.Text)
			}
		}
	}
	return b.String()
}

func ztapiHealthUsage(result *ztapiHealthProbeResult, usage *ztapiHealthWireUsage, responses bool) bool {
	if usage == nil {
		return true
	}
	input, output := usage.PromptTokens, usage.CompletionTokens
	if responses {
		input, output = usage.InputTokens, usage.OutputTokens
	}
	if input < 0 || output < 0 {
		return false
	}
	if input > result.InputTokens {
		result.InputTokens = input
	}
	if output > result.OutputTokens {
		result.OutputTokens = output
	}
	return true
}

func performZTAPIHealthProbe(ctx context.Context, config ZTAPIHealthWorkerConfig, job model.ZTAPIProbeJob) ztapiHealthProbeResult {
	return performZTAPIHealthProbeRequest(ctx, config, job, "ztapi-health-probe-"+job.ID, "", "", 1024)
}

func performZTAPIVerificationProbe(ctx context.Context, config ZTAPIHealthWorkerConfig, job model.ZTAPIProbeJob, requestID string) ztapiHealthProbeResult {
	return performZTAPIHealthProbeRequest(ctx, config, job, requestID, job.ID, job.LeaseToken, model.ZTAPIVerificationMaxOutputTokens)
}

func performZTAPIHealthProbeRequest(ctx context.Context, config ZTAPIHealthWorkerConfig, job model.ZTAPIProbeJob, requestID, caseID, leaseToken string, maxOutputTokens int64) ztapiHealthProbeResult {
	result := ztapiHealthProbeResult{Code: "unknown", RequestID: requestID}
	entryProtocol := strings.TrimSpace(job.EntryProtocol)
	if entryProtocol == "" {
		entryProtocol = job.Protocol
	}
	endpoint, err := ztapiHealthRelayEndpoint(config.RelayBaseURL, entryProtocol, job.PublicModel, job.Stream)
	if err != nil {
		result.Code = "relay_url_invalid"
		return result
	}
	payload := map[string]any{"model": job.PublicModel, "stream": job.Stream}
	const question = "What is 35+42? Reply with only the integer answer."
	const system = "You are a concise assistant."
	if job.Modality == model.ZTAPIModalityImage || job.Protocol == "video-tasks" {
		payload, err = ztapiHealthMediaProbePayload(job)
		if err != nil {
			result.Code = "probe_payload_error"
			return result
		}
	} else if entryProtocol == "embeddings" {
		if job.Stream {
			result.Code = "probe_protocol_invalid"
			return result
		}
		payload = map[string]any{"model": job.PublicModel, "input": "ZTAPI embedding verification", "encoding_format": "float"}
	} else if entryProtocol == "responses" {
		payload["input"], payload["max_output_tokens"] = question, maxOutputTokens
		payload["instructions"] = system
	} else if entryProtocol == "claude" {
		payload["system"] = system
		payload["messages"] = []map[string]string{{"role": "user", "content": question}}
		payload["max_tokens"] = maxOutputTokens
	} else if entryProtocol == "gemini" {
		payload = map[string]any{
			"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": question}}}},
			"systemInstruction": map[string]any{"parts": []map[string]string{{"text": system}}},
			"generationConfig":  map[string]any{"maxOutputTokens": maxOutputTokens},
		}
	} else {
		payload["messages"] = []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": question}}
		payload["max_tokens"] = maxOutputTokens
		if job.Stream {
			payload["stream_options"] = map[string]bool{"include_usage": true}
		}
	}
	body, err := common.Marshal(payload)
	if err != nil {
		result.Code = "probe_payload_error"
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, config.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		result.Code = "probe_request_invalid"
		return result
	}
	req.Header.Set("Authorization", "Bearer "+config.ProbeKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", result.RequestID)
	if caseID != "" && leaseToken != "" {
		req.Header.Set("X-ZTAPI-Health-Case-ID", caseID)
		req.Header.Set("X-ZTAPI-Health-Lease-Token", leaseToken)
	}
	resp, err := ztapiHealthHTTPClient(config.RequestTimeout, config.ProbeTransport).Do(req)
	if err != nil {
		result.Code = "probe_transport_error"
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Code = "http_status"
		return result
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, ztapiHealthProbeBodyLimit+1))
	if len(body) > ztapiHealthProbeBodyLimit {
		result = parseZTAPIHealthProbeForTarget(body[:ztapiHealthProbeBodyLimit], entryProtocol, job.Stream, job.Modality, result)
		result.Complete = false
		result.Code = "response_too_large"
		return result
	}
	if err != nil {
		result = parseZTAPIHealthProbeForTarget(body, entryProtocol, job.Stream, job.Modality, result)
		result.Complete = false
		result.Code = "incomplete_response"
		return result
	}
	return parseZTAPIHealthProbeForTarget(body, entryProtocol, job.Stream, job.Modality, result)
}

func parseZTAPIHealthProbeForTarget(body []byte, protocol string, stream bool, modality string, result ztapiHealthProbeResult) ztapiHealthProbeResult {
	if protocol == "gemini" && modality == model.ZTAPIModalityImage {
		return parseZTAPIHealthGeminiImageProbe(body, stream, result)
	}
	return parseZTAPIHealthProbe(body, protocol, stream, result)
}

func parseZTAPIHealthProbe(body []byte, protocol string, stream bool, result ztapiHealthProbeResult) ztapiHealthProbeResult {
	if protocol == "images" || protocol == "video-tasks" {
		result.Complete = false
		result.Code = "invalid_response"
		if stream || !gjson.ValidBytes(body) {
			return result
		}
		parsed := gjson.ParseBytes(body)
		if parsed.Get("error").Exists() {
			return result
		}
		if protocol == "video-tasks" {
			for _, path := range []string{"id", "task_id", "data.id", "data.task_id"} {
				if ztapiMediaHealthMetadata(parsed.Get(path).String(), 255) != "" {
					result.Code, result.Complete = "functional_pass", true
					return result
				}
			}
			return result
		}
		results := parsed.Get("data")
		if !results.Exists() || !results.IsArray() || len(results.Array()) == 0 {
			return result
		}
		for _, item := range results.Array() {
			if !item.IsObject() || (strings.TrimSpace(item.Get("url").String()) == "" && strings.TrimSpace(item.Get("b64_json").String()) == "") {
				return result
			}
		}
		result.Code, result.Complete = "functional_pass", true
		return result
	}
	if protocol == "embeddings" {
		result.Complete = false
		result.Code = "invalid_response"
		if stream {
			return result
		}
		tokens, err := common.ValidateZTAPIEmbeddingResponse(body, 1, 1536)
		if err != nil {
			return result
		}
		result.InputTokens, result.OutputTokens = int64(tokens), 0
		result.Code, result.Complete = "functional_pass", true
		return result
	}
	if protocol == "claude" {
		return parseZTAPIHealthClaudeProbe(body, stream, result)
	}
	if protocol == "gemini" {
		return parseZTAPIHealthGeminiProbe(body, stream, result)
	}
	responses := protocol == "responses"
	text, complete, invalid := "", false, false
	if !stream {
		var wire ztapiHealthWireResponse
		if common.Unmarshal(body, &wire) != nil || wire.Error != nil {
			result.Code = "invalid_response"
			return result
		}
		if !ztapiHealthUsage(&result, wire.Usage, responses) {
			result.Code = "probe_usage_invalid"
			return result
		}
		if responses {
			text, complete = ztapiHealthOutputText(wire), wire.Status == "completed"
		} else if len(wire.Choices) == 1 {
			text, complete = wire.Choices[0].Message.Content, wire.Choices[0].FinishReason == "stop"
		}
	} else {
		var accumulated strings.Builder
		chatStop, terminal := false, false
		scanner := bufio.NewScanner(bytes.NewReader(body))
		scanner.Buffer(make([]byte, 4096), ztapiHealthProbeBodyLimit+1)
		var data []string
		consume := func() {
			if len(data) == 0 {
				return
			}
			payload := strings.Join(data, "\n")
			data = nil
			if terminal {
				invalid = true
				return
			}
			if !responses && payload == "[DONE]" {
				terminal = true
				complete = chatStop
				return
			}
			if responses {
				var event struct {
					Type     string                   `json:"type"`
					Delta    string                   `json:"delta"`
					Response *ztapiHealthWireResponse `json:"response"`
				}
				if common.Unmarshal([]byte(payload), &event) != nil {
					invalid = true
					return
				}
				switch event.Type {
				case "response.output_text.delta":
					accumulated.WriteString(event.Delta)
				case "response.completed":
					terminal = true
					if event.Response == nil {
						invalid = true
						return
					}
					if !ztapiHealthUsage(&result, event.Response.Usage, true) {
						invalid = true
						return
					}
					complete = event.Response.Status == "completed" && event.Response.Error == nil
					text = ztapiHealthOutputText(*event.Response)
				case "error", "response.failed", "response.incomplete":
					invalid = true
				}
			} else {
				var chunk ztapiHealthWireResponse
				if common.Unmarshal([]byte(payload), &chunk) != nil || chunk.Error != nil {
					invalid = true
					return
				}
				if !ztapiHealthUsage(&result, chunk.Usage, false) {
					invalid = true
					return
				}
				if len(chunk.Choices) > 1 {
					invalid = true
					return
				}
				for _, choice := range chunk.Choices {
					if choice.Index != 0 || (chatStop && choice.Delta.Content != "") {
						invalid = true
						return
					}
					accumulated.WriteString(choice.Delta.Content)
					if choice.FinishReason != "" {
						chatStop = choice.FinishReason == "stop"
						if !chatStop {
							invalid = true
						}
					}
				}
			}
		}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				consume()
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		// SSE dispatches only blank-line-terminated events; an EOF mid-event
		// cannot supply a valid final marker.
		if scanner.Err() != nil || len(data) != 0 {
			invalid = true
		}
		if text == "" {
			text = accumulated.String()
		}
	}
	if invalid {
		result.Code = "invalid_response"
		return result
	}
	if !complete {
		result.Code = "incomplete_response"
		return result
	}
	result.Complete = true
	if strings.TrimSpace(text) == "77" {
		result.Code = "functional_pass"
	} else {
		result.Code = "wrong_answer"
	}
	return result
}

func finishZTAPIHealthNativeProbe(result ztapiHealthProbeResult, text string, complete, invalid bool) ztapiHealthProbeResult {
	if invalid {
		result.Code = "invalid_response"
		return result
	}
	if !complete {
		result.Code = "incomplete_response"
		return result
	}
	result.Complete = true
	if strings.TrimSpace(text) == "77" {
		result.Code = "functional_pass"
	} else {
		result.Code = "wrong_answer"
	}
	return result
}

func parseZTAPIHealthClaudeProbe(body []byte, stream bool, result ztapiHealthProbeResult) ztapiHealthProbeResult {
	var text strings.Builder
	complete, invalid := false, false
	if !stream {
		var response ztapiHealthClaudeResponse
		if common.Unmarshal(body, &response) != nil || response.Error != nil || response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 {
			return finishZTAPIHealthNativeProbe(result, "", false, true)
		}
		for _, content := range response.Content {
			if content.Type == "text" {
				text.WriteString(content.Text)
			}
		}
		result.InputTokens, result.OutputTokens = response.Usage.InputTokens, response.Usage.OutputTokens
		complete = response.StopReason == "end_turn" || response.StopReason == "stop_sequence"
		return finishZTAPIHealthNativeProbe(result, text.String(), complete, false)
	}

	terminal, stopped := false, false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), ztapiHealthProbeBodyLimit+1)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var event ztapiHealthClaudeResponse
		if common.Unmarshal([]byte(payload), &event) != nil || event.Error != nil {
			invalid = true
			continue
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				result.InputTokens = event.Message.Usage.InputTokens
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" {
				text.WriteString(event.Delta.Text)
			}
		case "message_delta":
			result.OutputTokens = event.Usage.OutputTokens
			stopped = event.Delta.StopReason == "end_turn" || event.Delta.StopReason == "stop_sequence"
		case "message_stop":
			terminal = true
		case "error":
			invalid = true
		}
	}
	if scanner.Err() != nil || result.InputTokens < 0 || result.OutputTokens < 0 {
		invalid = true
	}
	complete = terminal && stopped
	return finishZTAPIHealthNativeProbe(result, text.String(), complete, invalid)
}

func parseZTAPIHealthGeminiProbe(body []byte, stream bool, result ztapiHealthProbeResult) ztapiHealthProbeResult {
	consume := func(payload []byte, text *strings.Builder) (complete bool, invalid bool) {
		var response ztapiHealthGeminiResponse
		if common.Unmarshal(payload, &response) != nil || response.Error != nil || response.UsageMetadata.PromptTokenCount < 0 || response.UsageMetadata.CandidatesTokenCount < 0 || len(response.Candidates) != 1 {
			return false, true
		}
		for _, part := range response.Candidates[0].Content.Parts {
			text.WriteString(part.Text)
		}
		if response.UsageMetadata.PromptTokenCount > result.InputTokens {
			result.InputTokens = response.UsageMetadata.PromptTokenCount
		}
		if response.UsageMetadata.CandidatesTokenCount > result.OutputTokens {
			result.OutputTokens = response.UsageMetadata.CandidatesTokenCount
		}
		return response.Candidates[0].FinishReason == "STOP", false
	}
	var text strings.Builder
	if !stream {
		complete, invalid := consume(body, &text)
		return finishZTAPIHealthNativeProbe(result, text.String(), complete, invalid)
	}
	complete, invalid := false, false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), ztapiHealthProbeBodyLimit+1)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		chunkComplete, chunkInvalid := consume([]byte(payload), &text)
		complete = complete || chunkComplete
		invalid = invalid || chunkInvalid
	}
	if scanner.Err() != nil {
		invalid = true
	}
	return finishZTAPIHealthNativeProbe(result, text.String(), complete, invalid)
}

func parseZTAPIHealthGeminiImageProbe(body []byte, stream bool, result ztapiHealthProbeResult) ztapiHealthProbeResult {
	result.Code = "invalid_response"
	if stream {
		return result
	}
	var response ztapiHealthGeminiResponse
	if common.Unmarshal(body, &response) != nil || response.Error != nil ||
		response.UsageMetadata.PromptTokenCount < 0 || response.UsageMetadata.CandidatesTokenCount < 0 ||
		len(response.Candidates) != 1 || response.Candidates[0].FinishReason != "STOP" {
		return result
	}
	hasImage := false
	for _, part := range response.Candidates[0].Content.Parts {
		if strings.TrimSpace(part.InlineData.MIMEType) != "" && strings.TrimSpace(part.InlineData.Data) != "" {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return result
	}
	result.InputTokens = response.UsageMetadata.PromptTokenCount
	result.OutputTokens = response.UsageMetadata.CandidatesTokenCount
	result.Code, result.Complete = "functional_pass", true
	return result
}

func ztapiHealthMediaProbePayload(job model.ZTAPIProbeJob) (map[string]any, error) {
	if job.Stream || strings.TrimSpace(job.ProbePayloadJSON) == "" {
		return nil, errors.New("media probe payload is missing")
	}
	entryProtocol := strings.TrimSpace(job.EntryProtocol)
	if entryProtocol == "" {
		entryProtocol = job.Protocol
	}
	if job.Modality == model.ZTAPIModalityImage && entryProtocol == "gemini" {
		return map[string]any{
			"contents": []any{map[string]any{
				"role": "user", "parts": []any{map[string]any{"text": "Generate a neutral blue circle."}},
			}},
			"generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}},
		}, nil
	}
	var frozen map[string]any
	if common.Unmarshal([]byte(job.ProbePayloadJSON), &frozen) != nil || len(frozen) == 0 {
		return nil, errors.New("media probe payload is invalid")
	}
	allowed := map[string]bool{}
	switch entryProtocol {
	case "images":
		allowed = map[string]bool{"size": true, "quality": true, "response_format": true, "n": true}
		if len(frozen) != len(allowed) || !ztapiHealthProbeStringOption(frozen["size"]) || !ztapiHealthProbeStringOption(frozen["quality"]) ||
			!ztapiHealthProbeStringOption(frozen["response_format"]) || !ztapiHealthProbeIntegerOption(frozen["n"], 1, 16) {
			return nil, errors.New("image probe options are invalid")
		}
	case "video-tasks":
		allowed = map[string]bool{"size": true, "duration": true}
		if len(frozen) != len(allowed) || !ztapiHealthProbeStringOption(frozen["size"]) || !ztapiHealthProbeIntegerOption(frozen["duration"], 1, 600) {
			return nil, errors.New("video probe options are invalid")
		}
	default:
		return nil, errors.New("media probe protocol is invalid")
	}
	for key := range frozen {
		if !allowed[key] {
			return nil, errors.New("media probe option is not allowed")
		}
	}
	frozen["model"] = job.PublicModel
	frozen["prompt"] = "A plain blue circle centered on a white background."
	return frozen, nil
}

func ztapiHealthProbeStringOption(value any) bool {
	text, ok := value.(string)
	return ok && text == strings.TrimSpace(text) && len(text) > 0 && len(text) <= 64
}

func ztapiHealthProbeIntegerOption(value any, minimum, maximum int) bool {
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number == math.Trunc(number) && number >= float64(minimum) && number <= float64(maximum)
}
