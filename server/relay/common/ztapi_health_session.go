package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	base "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const ztapiHealthSessionKey = "ztapi_health_session"

type ZTAPIHealthSession struct {
	mu        sync.Mutex
	ticket    *types.ZTAPIHealthTicket
	collector *ZTAPIHealthCollector
	record    func(context.Context, *types.ZTAPIHealthTicket, types.ZTAPIHealthOutcome) error
	backend   ZTAPIHealthBackend
	operation string
	startedAt time.Time
	probePin  *ZTAPIHealthProbePin

	videoSubmissionAccepted bool
	upstreamTaskID          string
}

// The controller supplies engine callbacks to avoid model -> relay/common cycles.
type ZTAPIHealthBackend struct {
	AdmitRequest                       func(context.Context, string, string, string, int, bool) (*types.ZTAPIHealthTicket, error)
	AdmitMediaRequest                  func(context.Context, string, string, string, int, string, bool) (*types.ZTAPIHealthTicket, error)
	AdmitRequestWithEntryProtocol      func(context.Context, string, string, string, int, bool, string) (*types.ZTAPIHealthTicket, error)
	AdmitMediaRequestWithEntryProtocol func(context.Context, string, string, string, int, string, bool, string) (*types.ZTAPIHealthTicket, error)
	AdmitAttempt                       func(context.Context, *types.ZTAPIHealthTicket, int, string, string) error
	RecordOutcome                      func(context.Context, *types.ZTAPIHealthTicket, types.ZTAPIHealthOutcome) error
	CheckAvailable                     func(string) error
	ValidateProbeRoute                 func(context.Context, ZTAPIHealthProbeRouteCheck) error
	CircuitOpen                        error
	RouteOpen                          error
}

func (s *ZTAPIHealthSession) PrepareRelayAttempt() {
	if s.collector == nil {
		return
	}
	s.collector.mu.Lock()
	defer s.collector.mu.Unlock()
	if !s.collector.sealed {
		s.collector.current = nil
	}
}

// StartZTAPIHealthRequest must run before any customer quota reservation.
func StartZTAPIHealthRequest(c *gin.Context, info *RelayInfo, backend ZTAPIHealthBackend) (*ZTAPIHealthSession, error) {
	executionID := uuid.NewString()
	probePin := GetZTAPIHealthProbePin(c)
	entryProtocol := ztapiHealthProtocol(c.Request.URL.Path)
	if info != nil && info.ZTAPIPublicationSnapshot != nil && info.ZTAPIPublicationSnapshot.Modality == "video" {
		entryProtocol = "video-tasks"
	}
	if probePin != nil && (info == nil || info.OriginModelName != probePin.PublicModel || info.IsStream != probePin.Stream ||
		c.GetString(base.RequestIdKey) != probePin.RequestID || entryProtocol != probePin.EntryProtocol) {
		return nil, ZTAPIHealthAdmissionError(c.Request.Context(), errors.New("ZTAPI health probe request does not match its dispatch pin"), false)
	}
	operation := ""
	if info.ZTAPIPublicationSnapshot != nil {
		switch info.ZTAPIPublicationSnapshot.Modality {
		case "image":
			operation = types.ZTAPIHealthOperationImageGenerate
		case "video":
			operation = types.ZTAPIHealthOperationVideoSubmit
		}
	}
	var ticket *types.ZTAPIHealthTicket
	var err error
	if operation == types.ZTAPIHealthOperationVideoSubmit || operation == types.ZTAPIHealthOperationVideoFetch {
		entryProtocol = "video-tasks"
	}
	if operation != "" {
		if backend.AdmitMediaRequestWithEntryProtocol != nil {
			ticket, err = backend.AdmitMediaRequestWithEntryProtocol(c.Request.Context(), info.OriginModelName, executionID, c.GetString(base.RequestIdKey), info.UserId, operation, false, entryProtocol)
		} else if backend.AdmitMediaRequest == nil {
			err = errors.New("ZTAPI media health admission is unavailable")
		} else {
			ticket, err = backend.AdmitMediaRequest(c.Request.Context(), info.OriginModelName, executionID, c.GetString(base.RequestIdKey), info.UserId, operation, false)
		}
	} else if backend.AdmitRequestWithEntryProtocol != nil {
		ticket, err = backend.AdmitRequestWithEntryProtocol(c.Request.Context(), info.OriginModelName, executionID, c.GetString(base.RequestIdKey), info.UserId, info.IsStream, entryProtocol)
	} else if backend.AdmitRequest == nil {
		err = errors.New("ZTAPI health admission is unavailable")
	} else {
		ticket, err = backend.AdmitRequest(c.Request.Context(), info.OriginModelName, executionID, c.GetString(base.RequestIdKey), info.UserId, info.IsStream)
	}
	if err != nil {
		return nil, ZTAPIHealthAdmissionError(c.Request.Context(), err, errors.Is(err, backend.CircuitOpen))
	}
	if err := validateZTAPIHealthProbeTicket(probePin, ticket); err != nil {
		return nil, ZTAPIHealthAdmissionError(c.Request.Context(), err, false)
	}
	if probePin != nil {
		info.RequestId = probePin.RequestID
		c.Set(base.RequestIdKey, probePin.RequestID)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), base.RequestIdKey, probePin.RequestID))
		c.Header(base.RequestIdKey, probePin.RequestID)
		c.Header("X-Request-ID", probePin.RequestID)
	} else if info.ZTAPIPublicationSnapshot != nil {
		// Caller correlation IDs are not idempotency keys for separate billable calls.
		info.RequestId = executionID
		c.Set(base.RequestIdKey, executionID)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), base.RequestIdKey, executionID))
		c.Header(base.RequestIdKey, executionID)
		c.Header("X-Request-ID", executionID)
	}
	s := &ZTAPIHealthSession{ticket: ticket, record: backend.RecordOutcome, backend: backend, operation: operation, startedAt: time.Now(), probePin: probePin}
	c.Set(ztapiHealthSessionKey, s)
	if ticket == nil {
		return s, nil
	}
	s.collector = NewZTAPIHealthCollector(ticket.Source)
	s.collector.requestContext = c.Request.Context()
	c.Writer = &ztapiHealthResponseWriter{ResponseWriter: c.Writer, collector: s.collector, requestContext: c.Request.Context(), nonstream: !info.IsStream}
	return s, nil
}

func GetZTAPIHealthSession(c *gin.Context) *ZTAPIHealthSession {
	value, ok := c.Get(ztapiHealthSessionKey)
	if !ok {
		return nil
	}
	s, _ := value.(*ZTAPIHealthSession)
	return s
}

func ZTAPIHealthAdmissionError(ctx context.Context, err error, circuitOpen bool) *types.NewAPIError {
	if !circuitOpen {
		logger.LogError(ctx, fmt.Sprintf("ztapi_health_operational_fault stage=admission error_type=%T", err))
	}
	return types.NewErrorWithStatusCode(errors.New("This model is temporarily unavailable. Please retry later or choose another model."), types.ErrorCode("model_temporarily_unavailable"), http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

func ztapiHealthRouteUnavailableError() *types.NewAPIError {
	return types.NewErrorWithStatusCode(
		errors.New("This upstream route is temporarily unavailable. Trying another route."),
		types.ErrorCode("ztapi_route_temporarily_unavailable"), http.StatusServiceUnavailable,
	)
}

func ZTAPIHealthRouteCredentialExclusion(channelID int, credentialVersion string) string {
	credentialVersion = strings.TrimSpace(credentialVersion)
	if channelID <= 0 || len(credentialVersion) != 64 {
		return ""
	}
	return strconv.Itoa(channelID) + ":" + credentialVersion
}

// ZTAPIHealthExcludedCredentialVersions returns only the failed credentials
// for one channel. Reusing the same supplier key in another channel must not
// make that otherwise healthy route unavailable.
func ZTAPIHealthExcludedCredentialVersions(c *gin.Context, channelID int) []string {
	prefix := strconv.Itoa(channelID) + ":"
	entries := base.GetContextKeyStringSlice(c, constant.ContextKeyZTAPIHealthExcludedCredentials)
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry, prefix) && len(entry) == len(prefix)+64 {
			result = append(result, strings.TrimPrefix(entry, prefix))
		}
	}
	return result
}

func ztapiHealthProtocol(path string) string {
	switch {
	case strings.HasSuffix(path, "/embeddings"):
		return "embeddings"
	case strings.HasSuffix(path, "/images/generations"):
		return "images"
	case strings.HasSuffix(path, "/responses"):
		return "responses"
	case strings.HasSuffix(path, "/messages"):
		return "claude"
	case strings.HasSuffix(path, "/chat/completions"), strings.HasSuffix(path, "/completions"):
		return "chat"
	case strings.Contains(path, ":generateContent"), strings.Contains(path, ":streamGenerateContent"):
		return "gemini"
	default:
		return "unsupported"
	}
}

// BeginZTAPIHealthUpstream observes the actual wire protocol, not the requested
// format: Chat-via-Responses is a Responses attempt here.
func BeginZTAPIHealthUpstream(c *gin.Context, channelID int, path, credential string) (*ZTAPIHealthWireAttempt, error) {
	s := GetZTAPIHealthSession(c)
	if s == nil {
		return nil, nil
	}
	if s.ticket == nil {
		// Persisted circuits remain enforced when instrumentation is switched off.
		if err := s.backend.CheckAvailable(c.GetString("original_model")); err != nil {
			return nil, ZTAPIHealthAdmissionError(c.Request.Context(), err, errors.Is(err, s.backend.CircuitOpen))
		}
		return nil, nil
	}
	credentialVersion, err := ZTAPIHealthCredentialVersion(credential)
	if err != nil {
		return nil, ZTAPIHealthAdmissionError(c.Request.Context(), err, false)
	}
	protocol := ztapiHealthProtocol(path)
	if s.ticket.Modality == "video" {
		protocol = "video-tasks"
	}
	if s.probePin != nil {
		if s.backend.ValidateProbeRoute == nil {
			return nil, ZTAPIHealthAdmissionError(c.Request.Context(), errors.New("ZTAPI health probe route validation is unavailable"), false)
		}
		check := ZTAPIHealthProbeRouteCheck{
			CaseID: s.probePin.CaseID, LeaseToken: s.probePin.LeaseToken,
			ProbeRequestID: s.probePin.RequestID, ModelID: s.ticket.ModelID,
			ChannelID: channelID, Protocol: protocol, Stream: s.ticket.Stream,
			CredentialVersion: credentialVersion, Generation: s.ticket.Generation,
		}
		if err := s.backend.ValidateProbeRoute(c.Request.Context(), check); err != nil {
			return nil, ZTAPIHealthAdmissionError(c.Request.Context(), err, false)
		}
	}
	if err := s.backend.AdmitAttempt(c.Request.Context(), s.ticket, channelID, protocol, credentialVersion); err != nil {
		if s.backend.RouteOpen != nil && errors.Is(err, s.backend.RouteOpen) {
			excluded := base.GetContextKeyStringSlice(c, constant.ContextKeyZTAPIHealthExcludedCredentials)
			routeExclusion := ZTAPIHealthRouteCredentialExclusion(channelID, credentialVersion)
			found := false
			for _, entry := range excluded {
				if entry == routeExclusion {
					found = true
					break
				}
			}
			if routeExclusion != "" && !found {
				excluded = append(excluded, routeExclusion)
				base.SetContextKey(c, constant.ContextKeyZTAPIHealthExcludedCredentials, excluded)
			}
			return nil, ztapiHealthRouteUnavailableError()
		}
		return nil, ZTAPIHealthAdmissionError(c.Request.Context(), err, errors.Is(err, s.backend.CircuitOpen))
	}
	a := s.collector.beginAttempt(channelID, protocol, credentialVersion)
	if a == nil {
		return nil, ZTAPIHealthAdmissionError(c.Request.Context(), errors.New("health collector sealed or attempt limit reached"), false)
	}
	return a, nil
}

// MarkVideoSubmissionAccepted records only the provider task identity. Response
// bodies and result URLs stay outside durable health storage.
func (s *ZTAPIHealthSession) MarkVideoSubmissionAccepted(upstreamTaskID string) {
	if s == nil || s.ticket == nil || s.ticket.Operation != types.ZTAPIHealthOperationVideoSubmit || !validZTAPIHealthMetadata(upstreamTaskID, 255) {
		return
	}
	s.mu.Lock()
	s.videoSubmissionAccepted = true
	s.upstreamTaskID = upstreamTaskID
	s.mu.Unlock()
}

func validZTAPIHealthMetadata(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-/", r)) {
			return false
		}
	}
	return true
}

func (s *ZTAPIHealthSession) Finalize(requestContext context.Context, relayFailed bool) error {
	if s == nil || s.ticket == nil {
		return nil
	}
	out, first := s.collector.Seal(errors.Is(requestContext.Err(), context.Canceled), relayFailed)
	if !first {
		return nil
	}
	out.Operation = s.operation
	out.LatencyMilliseconds = time.Since(s.startedAt).Milliseconds()
	if s.ticket.Modality == "image" {
		out.ResultValid = out.HasMedia
	} else if s.ticket.Modality == "video" && s.ticket.Operation == types.ZTAPIHealthOperationVideoSubmit {
		s.mu.Lock()
		accepted, taskID := s.videoSubmissionAccepted, s.upstreamTaskID
		s.mu.Unlock()
		if accepted {
			out.Result = "success"
			out.Reason = "valid_output"
			out.UpstreamProtocol = "video-tasks"
			out.UpstreamTaskID = taskID
			out.ResultValid = true
			out.TransportComplete = true
			out.Dispatched = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.record(ctx, s.ticket, out)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("ztapi_health_operational_fault stage=outcome_persistence execution_id=%s error_type=%T", s.ticket.ExecutionID, err))
	}
	return err
}

type ztapiHealthResponseWriter struct {
	gin.ResponseWriter
	mu              sync.Mutex
	collector       *ZTAPIHealthCollector
	requestContext  context.Context
	nonstream       bool
	writtenBytes    int64
	lengthCaptured  bool
	committedLength int64
}

func (w *ztapiHealthResponseWriter) captureLength() {
	if !w.nonstream || w.lengthCaptured {
		return
	}
	w.lengthCaptured = true
	w.committedLength = -1
	if !w.ResponseWriter.Written() {
		if n, err := strconv.ParseInt(w.ResponseWriter.Header().Get("Content-Length"), 10, 64); err == nil && n > 0 {
			w.committedLength = n
		}
	}
}

func NewZTAPIHealthResponseWriter(w gin.ResponseWriter, c *ZTAPIHealthCollector) gin.ResponseWriter {
	return &ztapiHealthResponseWriter{ResponseWriter: w, collector: c}
}

func (w *ztapiHealthResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Suppress synthetic completion, not content. Return a successful write so
	// existing usage settlement/refund decisions are unaffected by monitoring.
	if bytes.Equal(bytes.TrimSpace(p), []byte("data: [DONE]")) && !w.collector.PermitCompletion() {
		return len(p), nil
	}
	if w.collector.endedWithoutTerminal() && ztapiHealthTerminalOnlyFrame(p) {
		return len(p), nil
	}
	cancelledBeforeWrite := w.requestContext != nil && errors.Is(w.requestContext.Err(), context.Canceled)
	w.captureLength()
	n, err := w.ResponseWriter.Write(p)
	if w.nonstream {
		w.writtenBytes += int64(n)
		w.collector.mu.Lock()
		if a := w.collector.current; a != nil && !w.collector.sealed {
			a.deliveryFailed = a.deliveryFailed || cancelledBeforeWrite || err != nil || n != len(p)
			a.deliveryComplete = w.committedLength > 0 && w.writtenBytes == w.committedLength && a.eof && !a.eofCancelled && !a.deliveryFailed
		}
		w.collector.mu.Unlock()
	}
	return n, err
}

func ztapiHealthTerminalOnlyFrame(p []byte) bool {
	p = bytes.TrimSpace(p)
	if bytes.Equal(p, []byte("event: message_stop")) || bytes.Equal(p, []byte("event: response.completed")) {
		return true
	}
	if !bytes.HasPrefix(p, []byte("data:")) || len(p) > ztapiHealthMaxEvent {
		return false
	}
	p = bytes.TrimSpace(bytes.TrimPrefix(p, []byte("data:")))
	if !gjson.ValidBytes(p) {
		return false
	}
	v := gjson.ParseBytes(p)
	switch v.Get("type").String() {
	case "message_stop", "response.completed":
		return true
	case "message_delta":
		return v.Get("delta.stop_reason").String() != ""
	}
	choices := v.Get("choices").Array()
	if len(choices) == 0 {
		return false
	}
	for _, choice := range choices {
		if choice.Get("finish_reason").String() == "" {
			return false
		}
		delta := choice.Get("delta")
		if delta.Get("content").String() != "" || delta.Get("reasoning_content").String() != "" || delta.Get("tool_calls").Exists() || delta.Get("function_call").Exists() || choice.Get("text").String() != "" {
			return false
		}
	}
	return true
}
func (w *ztapiHealthResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *ztapiHealthResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
}
func (w *ztapiHealthResponseWriter) WriteHeaderNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.captureLength()
	w.ResponseWriter.WriteHeaderNow()
}
func (w *ztapiHealthResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.captureLength()
	w.ResponseWriter.Flush()
}
func (w *ztapiHealthResponseWriter) Written() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.Written()
}
func (w *ztapiHealthResponseWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.Status()
}
func (w *ztapiHealthResponseWriter) Size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.Size()
}
func (w *ztapiHealthResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
