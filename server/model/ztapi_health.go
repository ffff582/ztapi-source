package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

func ZTAPIHealthEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_ENABLED")))
	return err == nil && enabled
}

func CheckZTAPIHealthModelAvailable(modelName string) error {
	return NewZTAPIHealthStore(DB).CheckAvailable(context.Background(), modelName)
}

func AdmitZTAPIHealthRequest(ctx context.Context, requestedModel, executionID, requestID string, userID int, stream bool) (*types.ZTAPIHealthTicket, error) {
	return NewZTAPIHealthStore(DB).AdmitRequest(ctx, requestedModel, executionID, requestID, userID, stream)
}

func AdmitZTAPIHealthRequestWithEntryProtocol(ctx context.Context, requestedModel, executionID, requestID string, userID int, stream bool, entryProtocol string) (*types.ZTAPIHealthTicket, error) {
	return NewZTAPIHealthStore(DB).AdmitRequestWithEntryProtocol(ctx, requestedModel, executionID, requestID, userID, stream, entryProtocol)
}

func AdmitZTAPIMediaHealthRequest(ctx context.Context, requestedModel, executionID, requestID string, userID int, operation string, allowUnavailable bool) (*types.ZTAPIHealthTicket, error) {
	return NewZTAPIHealthStore(DB).AdmitMediaRequest(ctx, requestedModel, executionID, requestID, userID, operation, allowUnavailable)
}

func AdmitZTAPIMediaHealthRequestWithEntryProtocol(ctx context.Context, requestedModel, executionID, requestID string, userID int, operation string, allowUnavailable bool, entryProtocol string) (*types.ZTAPIHealthTicket, error) {
	return NewZTAPIHealthStore(DB).AdmitMediaRequestWithEntryProtocol(ctx, requestedModel, executionID, requestID, userID, operation, allowUnavailable, entryProtocol)
}

func AdmitZTAPIHealthAttempt(ctx context.Context, ticket *types.ZTAPIHealthTicket, channelID int, protocol, credentialVersion string) error {
	return NewZTAPIHealthStore(DB).AdmitAttempt(ctx, ticket, channelID, protocol, credentialVersion)
}

func RecordZTAPIHealthOutcome(ctx context.Context, ticket *types.ZTAPIHealthTicket, outcome types.ZTAPIHealthOutcome) error {
	return NewZTAPIHealthStore(DB).RecordOutcome(ctx, ticket, outcome)
}

// Missing health tables are tolerated only for pre-P5, instrumentation-disabled
// installations. Other DB errors fail closed even when instrumentation is off.
func ztapiHealthLegacySchema(err error) bool {
	if err == nil || ZTAPIHealthEnabled() {
		return false
	}
	m := strings.ToLower(err.Error())
	return (strings.Contains(m, "ztapi_health_states") || strings.Contains(m, "ztapi_model_configs")) &&
		(strings.Contains(m, "no such table") || strings.Contains(m, "doesn't exist") || strings.Contains(m, "does not exist"))
}

func (s *ZTAPIHealthStore) CheckAvailable(ctx context.Context, modelName string) error {
	if s.DB == nil {
		if !ZTAPIHealthEnabled() {
			return nil
		}
		return errors.New("ztapi health database is not initialized")
	}
	var count int64
	err := s.DB.WithContext(ctx).Table("ztapi_health_states AS h").
		Joins("JOIN ztapi_model_configs AS c ON c.id = h.model_id").
		Where("h.open = ? AND (c.public_name = ? OR c.source_model = ?)", true, strings.TrimSpace(modelName), strings.TrimSpace(modelName)).Count(&count).Error
	if ztapiHealthLegacySchema(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrZTAPIHealthCircuitOpen
	}
	return nil
}

func healthTicket(r *ZTAPIHealthRequest) *types.ZTAPIHealthTicket {
	return &types.ZTAPIHealthTicket{ExecutionID: r.ExecutionID, RequestID: r.RequestID, ModelID: r.ModelID,
		ConfigVersion: r.ConfigVersion, Generation: r.Generation, PublicModel: r.PublicModel,
		Modality: r.Modality, Operation: r.Operation, EntryProtocol: r.EntryProtocol,
		Stream: r.Stream, Source: r.Source, StartedAt: r.StartedAt}
}

func (s *ZTAPIHealthStore) AdmitRequest(ctx context.Context, requestedModel, executionID, requestID string, userID int, stream bool) (*types.ZTAPIHealthTicket, error) {
	return s.AdmitRequestWithEntryProtocol(ctx, requestedModel, executionID, requestID, userID, stream, "chat")
}

func (s *ZTAPIHealthStore) AdmitRequestWithEntryProtocol(ctx context.Context, requestedModel, executionID, requestID string, userID int, stream bool, entryProtocol string) (*types.ZTAPIHealthTicket, error) {
	return s.admitRequest(ctx, requestedModel, executionID, requestID, userID, stream, "", false, entryProtocol)
}

func (s *ZTAPIHealthStore) AdmitMediaRequest(ctx context.Context, requestedModel, executionID, requestID string, userID int, operation string, allowUnavailable bool) (*types.ZTAPIHealthTicket, error) {
	if allowUnavailable && operation != types.ZTAPIHealthOperationVideoFetch {
		return nil, ErrZTAPIHealthInvalidTicket
	}
	entryProtocol := "images"
	if operation == types.ZTAPIHealthOperationVideoSubmit || operation == types.ZTAPIHealthOperationVideoFetch {
		entryProtocol = "video-tasks"
	}
	return s.AdmitMediaRequestWithEntryProtocol(ctx, requestedModel, executionID, requestID, userID, operation, allowUnavailable, entryProtocol)
}

func (s *ZTAPIHealthStore) AdmitMediaRequestWithEntryProtocol(ctx context.Context, requestedModel, executionID, requestID string, userID int, operation string, allowUnavailable bool, entryProtocol string) (*types.ZTAPIHealthTicket, error) {
	if allowUnavailable && operation != types.ZTAPIHealthOperationVideoFetch {
		return nil, ErrZTAPIHealthInvalidTicket
	}
	return s.admitRequest(ctx, requestedModel, executionID, requestID, userID, false, operation, allowUnavailable, entryProtocol)
}

func (s *ZTAPIHealthStore) admitRequest(ctx context.Context, requestedModel, executionID, requestID string, userID int, stream bool, operation string, allowUnavailable bool, entryProtocol string) (*types.ZTAPIHealthTicket, error) {
	if !allowUnavailable {
		if err := s.CheckAvailable(ctx, requestedModel); err != nil {
			return nil, err
		}
	}
	if !ZTAPIHealthEnabled() {
		return nil, nil
	}
	var config ZTAPIModelConfig
	err := s.DB.WithContext(ctx).Where("public_name = ? OR source_model = ?", strings.TrimSpace(requestedModel), strings.TrimSpace(requestedModel)).First(&config).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	modality := ZTAPIModelModality(config.SourceModel)
	switch modality {
	case ZTAPIModalityImage:
		if operation != "" && operation != types.ZTAPIHealthOperationImageGenerate {
			return nil, ErrZTAPIHealthInvalidTicket
		}
	case ZTAPIModalityVideo:
		if operation != types.ZTAPIHealthOperationVideoSubmit && operation != types.ZTAPIHealthOperationVideoFetch {
			return nil, ErrZTAPIHealthInvalidTicket
		}
	default:
		if operation != "" {
			return nil, ErrZTAPIHealthInvalidTicket
		}
	}
	entryProtocol = strings.TrimSpace(entryProtocol)
	if executionID == "" || len(executionID) > 128 || len(requestID) > 255 || entryProtocol == "" || len(entryProtocol) > 32 {
		return nil, ErrZTAPIHealthInvalidTicket
	}
	var ticket *types.ZTAPIHealthTicket
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		ticket = nil
		state, err := lockZTAPIHealthState(tx, config.ID)
		if err != nil {
			return err
		}
		if state.Open && !allowUnavailable {
			return ErrZTAPIHealthCircuitOpen
		}
		if err := tx.First(&config, config.ID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrZTAPIModelNotPublic
		} else if err != nil {
			return err
		}
		name := strings.TrimSpace(requestedModel)
		if (!allowUnavailable && !config.Published) || config.PublicNameValue() == "" || (name != config.PublicNameValue() && name != config.SourceModel) {
			return ErrZTAPIModelNotPublic
		}
		var existing ZTAPIHealthRequest
		err = tx.First(&existing, "execution_id = ?", executionID).Error
		if err == nil {
			return ErrZTAPIHealthExecutionConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		source := "real"
		probeUser, parseErr := strconv.Atoi(strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_PROBE_USER_ID")))
		if parseErr == nil && probeUser > 0 && userID == probeUser {
			source = "probe"
		}
		r := ZTAPIHealthRequest{ExecutionID: executionID, RequestID: requestID, ModelID: config.ID,
			ConfigVersion: config.Version, Generation: state.Generation, PublicModel: config.PublicNameValue(),
			Modality: modality, Operation: operation, EntryProtocol: entryProtocol, UserID: userID, Stream: stream,
			Source: source, StartedAt: s.now(), Admissions: "[]"}
		if err := tx.Create(&r).Error; err != nil {
			return err
		}
		ticket = healthTicket(&r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ticket, nil
}

func loadZTAPIHealthRequest(tx *gorm.DB, ticket *types.ZTAPIHealthTicket) (*ZTAPIHealthRequest, error) {
	var r ZTAPIHealthRequest
	if err := tx.First(&r, "execution_id = ?", ticket.ExecutionID).Error; err != nil {
		return nil, err
	}
	if *healthTicket(&r) != *ticket {
		return nil, ErrZTAPIHealthInvalidTicket
	}
	return &r, nil
}

// Admission is a dispatch permit, not evidence that the network send happened.
// A permit granted before a trip is in flight. Call immediately before dispatch.
func (s *ZTAPIHealthStore) AdmitAttempt(ctx context.Context, ticket *types.ZTAPIHealthTicket, channelID int, protocol, credentialVersion string) error {
	if ticket == nil {
		return nil
	}
	credentialVersion = strings.TrimSpace(credentialVersion)
	if channelID <= 0 || len(protocol) > 64 || !validZTAPICredentialVersion(credentialVersion) {
		return ErrZTAPIHealthInvalidTicket
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		state, err := lockZTAPIHealthState(tx, ticket.ModelID)
		if err != nil {
			return err
		}
		r, err := loadZTAPIHealthRequest(tx, ticket)
		if err != nil {
			return err
		}
		allowUnavailable := r.Operation == types.ZTAPIHealthOperationVideoFetch
		if state.Open && !allowUnavailable {
			return ErrZTAPIHealthCircuitOpen
		}
		if state.Generation != ticket.Generation {
			return ErrZTAPIHealthGenerationConflict
		}
		// The health lock serializes trips and identity changes. A plain read
		// avoids reversing catalog writers' config -> health row lock order.
		var config ZTAPIModelConfig
		if err := tx.First(&config, ticket.ModelID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrZTAPIModelNotPublic
		} else if err != nil {
			return err
		}
		if (!allowUnavailable && !config.Published) || config.PublicNameValue() == "" || config.PublicNameValue() != ticket.PublicModel {
			return ErrZTAPIModelNotPublic
		}
		if r.Source == "real" {
			route := ZTAPIHealthRouteIdentity{
				ModelID: r.ModelID, ChannelID: channelID, EntryProtocol: r.EntryProtocol,
				Protocol: strings.TrimSpace(protocol), Stream: r.Stream,
				CredentialVersion: ztapiCredentialFingerprintFromDigest(credentialVersion), Generation: r.Generation,
			}
			var routeState ZTAPIHealthRouteState
			err := applyZTAPIHealthRouteIdentity(tx, route).Take(&routeState).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && routeState.Open {
				return ErrZTAPIHealthRouteOpen
			}
		}
		if r.Completed {
			return ErrZTAPIHealthExecutionConflict
		}
		var attempts []types.ZTAPIHealthAttempt
		if err := common.UnmarshalJsonStr(r.Admissions, &attempts); err != nil {
			return err
		}
		if len(attempts) >= 64 {
			return errors.New("ztapi health attempt limit exceeded")
		}
		attempts = append(attempts, types.ZTAPIHealthAttempt{
			Index: len(attempts) + 1, ChannelID: channelID, Protocol: protocol, CredentialVersion: credentialVersion,
		})
		encoded, err := common.Marshal(attempts)
		if err != nil {
			return err
		}
		return tx.Model(r).Update("admissions", string(encoded)).Error
	})
}

func validZTAPIHealthMetadata(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for _, ch := range value {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("_.:/-", ch)) {
			return false
		}
	}
	return true
}

func validateZTAPIHealthOutcome(o types.ZTAPIHealthOutcome) error {
	switch o.Result {
	case "success", "failure", "suspected", "excluded", "unknown":
	default:
		return errors.New("invalid ztapi health outcome result")
	}
	if !validZTAPIHealthMetadata(o.Reason, 128) || !validZTAPIHealthMetadata(o.Operation, 32) ||
		!validZTAPIHealthMetadata(o.UpstreamProtocol, 64) || !validZTAPIHealthMetadata(o.UpstreamRequestID, 255) ||
		!validZTAPIHealthMetadata(o.UpstreamTaskID, 255) || !validZTAPIHealthMetadata(o.ProviderErrorCode, 128) ||
		!validZTAPIHealthMetadata(o.TerminalStatus, 128) || len(o.FinishReasons) > 64 || len(o.Attempts) > 64 ||
		o.LatencyMilliseconds < 0 || o.LatencyMilliseconds > 86_400_000 {
		return errors.New("ztapi health metadata limit exceeded")
	}
	if o.CredentialVersion != "" && !validZTAPICredentialVersion(o.CredentialVersion) {
		return errors.New("invalid ztapi health credential version")
	}
	for _, reason := range o.FinishReasons {
		if len(reason) > 128 {
			return errors.New("ztapi health finish reason limit exceeded")
		}
	}
	for index, a := range o.Attempts {
		if len(a.Protocol) > 64 || len(a.UpstreamRequestID) > 255 || len(a.ProviderErrorCode) > 128 ||
			a.CredentialVersion != "" && !validZTAPICredentialVersion(a.CredentialVersion) {
			return errors.New("ztapi health attempt metadata limit exceeded")
		}
		if a.Index != index+1 || !validZTAPIHealthMetadata(a.Reason, 128) {
			return errors.New("invalid ztapi health attempt classification")
		}
		switch a.Result {
		case "", "success", "failure", "suspected", "excluded", "unknown":
		default:
			return errors.New("invalid ztapi health attempt result")
		}
	}
	if len(o.Attempts) > 0 && (o.CredentialVersion != "" || o.Attempts[len(o.Attempts)-1].CredentialVersion != "") &&
		o.CredentialVersion != o.Attempts[len(o.Attempts)-1].CredentialVersion {
		return errors.New("ztapi health final credential version mismatch")
	}
	return nil
}

func validateZTAPIHealthOutcomeForTicket(ticket *types.ZTAPIHealthTicket, o types.ZTAPIHealthOutcome) error {
	if ticket == nil {
		return nil
	}
	switch ticket.Modality {
	case ZTAPIModalityImage:
		if ticket.Operation != types.ZTAPIHealthOperationImageGenerate || o.Operation != ticket.Operation {
			return ErrZTAPIHealthInvalidTicket
		}
	case ZTAPIModalityVideo:
		if (ticket.Operation != types.ZTAPIHealthOperationVideoSubmit && ticket.Operation != types.ZTAPIHealthOperationVideoFetch) || o.Operation != ticket.Operation {
			return ErrZTAPIHealthInvalidTicket
		}
	default:
		if ticket.Operation != "" || o.Operation != "" {
			return ErrZTAPIHealthInvalidTicket
		}
	}
	return nil
}

func (s *ZTAPIHealthStore) RecordOutcome(ctx context.Context, ticket *types.ZTAPIHealthTicket, outcome types.ZTAPIHealthOutcome) error {
	if ticket == nil {
		return nil
	}
	if err := validateZTAPIHealthOutcome(outcome); err != nil {
		return err
	}
	if err := validateZTAPIHealthOutcomeForTicket(ticket, outcome); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		state, err := lockZTAPIHealthState(tx, ticket.ModelID)
		if err != nil {
			return err
		}
		if s.afterStateLock != nil {
			s.afterStateLock()
		}
		r, err := loadZTAPIHealthRequest(tx, ticket)
		if err != nil {
			return err
		}
		if r.Completed {
			return nil
		}
		return s.completeTx(tx, state, r, outcome)
	})
}

type ztapiHealthRouteAction struct {
	route              ZTAPIHealthRouteIdentity
	result             string
	sourceAttemptIndex int
}

func ztapiHealthRouteForOutcome(r *ZTAPIHealthRequest, channelID int, protocol, credentialVersion string) (ZTAPIHealthRouteIdentity, bool) {
	route := ZTAPIHealthRouteIdentity{
		ModelID: r.ModelID, ChannelID: channelID, EntryProtocol: r.EntryProtocol,
		Protocol: strings.TrimSpace(protocol), Stream: r.Stream,
		CredentialVersion: ztapiCredentialFingerprintFromDigest(strings.TrimSpace(credentialVersion)), Generation: r.Generation,
	}
	return route, validZTAPIHealthRouteIdentity(route)
}

func ztapiHealthRealRouteActions(r *ZTAPIHealthRequest, outcome types.ZTAPIHealthOutcome) []ztapiHealthRouteAction {
	actions := make(map[string]ztapiHealthRouteAction)
	order := make([]string, 0, len(outcome.Attempts)+1)
	add := func(channelID int, protocol, credentialVersion, result string, sourceAttemptIndex int) {
		if result != "success" && result != "suspected" {
			return
		}
		route, ok := ztapiHealthRouteForOutcome(r, channelID, protocol, credentialVersion)
		if !ok {
			return
		}
		key := fmt.Sprintf("%d\x00%s\x00%s\x00%t\x00%s\x00%d", route.ChannelID, route.EntryProtocol, route.Protocol, route.Stream, route.CredentialVersion.String(), route.Generation)
		if _, exists := actions[key]; !exists {
			order = append(order, key)
		}
		actions[key] = ztapiHealthRouteAction{route: route, result: result, sourceAttemptIndex: sourceAttemptIndex}
	}
	if len(outcome.Attempts) == 0 {
		add(outcome.ChannelID, outcome.UpstreamProtocol, outcome.CredentialVersion, outcome.Result, 0)
	} else {
		for _, attempt := range outcome.Attempts {
			add(attempt.ChannelID, attempt.Protocol, attempt.CredentialVersion, attempt.Result, attempt.Index)
		}
	}
	result := make([]ztapiHealthRouteAction, 0, len(order))
	for _, key := range order {
		result = append(result, actions[key])
	}
	return result
}

func validateZTAPIHealthOutcomeAdmissions(r *ZTAPIHealthRequest, outcome types.ZTAPIHealthOutcome) error {
	if r.Source != "real" {
		return nil
	}
	var admissions []types.ZTAPIHealthAttempt
	if err := common.UnmarshalJsonStr(r.Admissions, &admissions); err != nil {
		return ErrZTAPIHealthInvalidTicket
	}
	if len(outcome.Attempts) > 0 {
		if len(outcome.Attempts) != len(admissions) {
			return ErrZTAPIHealthInvalidTicket
		}
		for index, attempt := range outcome.Attempts {
			admission := admissions[index]
			if attempt.Index != index+1 || admission.Index != attempt.Index || admission.ChannelID != attempt.ChannelID ||
				strings.TrimSpace(admission.Protocol) != strings.TrimSpace(attempt.Protocol) ||
				!validZTAPICredentialVersion(strings.TrimSpace(admission.CredentialVersion)) ||
				strings.TrimSpace(admission.CredentialVersion) != strings.TrimSpace(attempt.CredentialVersion) {
				return ErrZTAPIHealthInvalidTicket
			}
			if (attempt.Result == "success" || attempt.Result == "suspected") &&
				(!attempt.Dispatched || !validZTAPICredentialVersion(strings.TrimSpace(attempt.CredentialVersion))) {
				return ErrZTAPIHealthInvalidTicket
			}
		}
		finalAdmission := admissions[len(admissions)-1]
		if finalAdmission.ChannelID != outcome.ChannelID ||
			strings.TrimSpace(finalAdmission.Protocol) != strings.TrimSpace(outcome.UpstreamProtocol) ||
			strings.TrimSpace(finalAdmission.CredentialVersion) != strings.TrimSpace(outcome.CredentialVersion) {
			return ErrZTAPIHealthInvalidTicket
		}
		return nil
	}
	if !outcome.Dispatched {
		if outcome.ChannelID != 0 || strings.TrimSpace(outcome.UpstreamProtocol) != "" || strings.TrimSpace(outcome.CredentialVersion) != "" {
			return ErrZTAPIHealthInvalidTicket
		}
		return nil
	}
	if len(admissions) == 0 {
		return ErrZTAPIHealthInvalidTicket
	}
	admission := admissions[len(admissions)-1]
	if admission.ChannelID != outcome.ChannelID || strings.TrimSpace(admission.Protocol) != strings.TrimSpace(outcome.UpstreamProtocol) ||
		!validZTAPICredentialVersion(strings.TrimSpace(admission.CredentialVersion)) ||
		strings.TrimSpace(admission.CredentialVersion) != strings.TrimSpace(outcome.CredentialVersion) {
		return ErrZTAPIHealthInvalidTicket
	}
	return nil
}

func applyZTAPIHealthRealRouteActionsTx(tx *gorm.DB, r *ZTAPIHealthRequest, event ZTAPIHealthEvent, outcome types.ZTAPIHealthOutcome, observedAt time.Time) error {
	for _, action := range ztapiHealthRealRouteActions(r, outcome) {
		switch action.result {
		case "suspected":
			_, _, err := enqueueZTAPIHealthSuspicionTx(tx, ZTAPIHealthSuspicion{
				Route: action.route, SourceEventID: event.ID, SourceAttemptIndex: action.sourceAttemptIndex,
			}, observedAt)
			if err != nil {
				return err
			}
		case "success":
			if _, err := cancelQueuedZTAPIHealthVerificationTx(tx, action.route, observedAt); err != nil {
				return err
			}
			if err := resetClosedZTAPIHealthRouteEvidenceTx(tx, action.route, observedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *ZTAPIHealthStore) completeTx(tx *gorm.DB, state *ZTAPIHealthState, r *ZTAPIHealthRequest, outcome types.ZTAPIHealthOutcome) error {
	observedAt := s.Now().UTC()
	now := observedAt.Unix()
	result, reason := outcome.Result, outcome.Reason
	if outcome.ClientCancelled {
		result, reason = "excluded", "client_cancelled"
	} else if (result == "success" || result == "failure") && !outcome.Dispatched {
		result, reason = "unknown", "not_dispatched"
	} else if result == "success" && !outcome.TransportComplete {
		result, reason = "unknown", "transport_incomplete"
	} else if (r.Modality == ZTAPIModalityImage || r.Modality == ZTAPIModalityVideo) && result == "success" && !outcome.ResultValid {
		result, reason = "failure", "invalid_media_result"
	}
	normalizedOutcome := outcome
	normalizedOutcome.Result, normalizedOutcome.Reason = result, reason
	if len(normalizedOutcome.Attempts) > 0 {
		last := &normalizedOutcome.Attempts[len(normalizedOutcome.Attempts)-1]
		if last.Result == "" {
			last.Result, last.Reason = result, reason
		}
	}
	normalizedOutcome = types.ClassifyZTAPIHealthSignal(r.Source, normalizedOutcome)
	result, reason = normalizedOutcome.Result, normalizedOutcome.Reason
	if err := validateZTAPIHealthOutcomeAdmissions(r, normalizedOutcome); err != nil {
		return err
	}
	encoded, err := common.Marshal(normalizedOutcome)
	if err != nil {
		return err
	}
	stale := r.Generation != state.Generation
	// Automatic verification probes are evaluated by the route-scoped
	// verification store. They must never feed the legacy model-wide circuit.
	legacyCircuitCounted := r.Source != "real" && r.Source != "probe"
	state.CompletionSequence++
	event := ZTAPIHealthEvent{ExecutionID: r.ExecutionID, RequestID: r.RequestID, ModelID: r.ModelID,
		ConfigVersion: r.ConfigVersion, Generation: r.Generation, PublicModel: r.PublicModel,
		Modality: r.Modality, Operation: r.Operation, EntryProtocol: r.EntryProtocol,
		CompletionSequence: state.CompletionSequence, CompletedAt: now, Stream: r.Stream, Source: r.Source,
		Result: result, Reason: reason, Counted: !stale && legacyCircuitCounted && (result == "success" || result == "failure"), StaleGeneration: stale,
		ChannelID: outcome.ChannelID, CredentialVersion: outcome.CredentialVersion,
		UpstreamProtocol: outcome.UpstreamProtocol, HTTPStatus: outcome.HTTPStatus,
		UpstreamRequestID: outcome.UpstreamRequestID, UpstreamTaskID: outcome.UpstreamTaskID,
		ProviderErrorCode: outcome.ProviderErrorCode, LatencyMilliseconds: outcome.LatencyMilliseconds,
		ResultValid: outcome.ResultValid, Outcome: string(encoded)}
	if err := tx.Create(&event).Error; err != nil {
		return err
	}
	if err := tx.Model(r).Update("completed", true).Error; err != nil {
		return err
	}
	if !stale && r.Source == "real" {
		if err := applyZTAPIHealthRealRouteActionsTx(tx, r, event, normalizedOutcome, observedAt); err != nil {
			return err
		}
	}
	if !stale && !state.Open {
		if event.Counted && result == "success" {
			state.ConsecutiveFailures = 0
		}
		if event.Counted && result == "failure" {
			state.ConsecutiveFailures++
		}
		// Recovery preserves the audit window. A new success must not reopen
		// the previous incident; only a new failure evaluates trip thresholds.
		if event.Counted && result == "failure" {
			window, err := ztapiHealthWindow(tx, r.ModelID, now)
			if err != nil {
				return err
			}
			rule := ""
			if state.ConsecutiveFailures >= 2 {
				rule = "consecutive_2"
			} else if window.Failures >= 3 && window.Failures*50 > window.ValidSamples {
				rule = "rolling_24h_gt_2pct"
			}
			if rule != "" {
				incident := ZTAPIHealthIncident{ModelID: r.ModelID, Generation: state.Generation, ConfigVersion: r.ConfigVersion,
					PublicModel: r.PublicModel, TriggerEventID: event.ID, Rule: rule, ConsecutiveFailures: state.ConsecutiveFailures,
					ValidSamples: window.ValidSamples, Failures: window.Failures, WindowStart: window.WindowStart, OpenedAt: now}
				if err := tx.Create(&incident).Error; err != nil {
					return err
				}
				state.Open, state.IncidentID = true, incident.ID
				for _, kind := range []string{"unpublish", "alert"} {
					item := ZTAPIHealthOutbox{DedupKey: fmt.Sprintf("incident:%d:%s", incident.ID, kind), Kind: kind,
						ModelID: r.ModelID, Generation: state.Generation, IncidentID: incident.ID, EventID: event.ID,
						Status: "pending", NextAttemptAt: now, CreatedAt: now}
					if err := tx.Create(&item).Error; err != nil {
						return err
					}
				}
			}
		}
	}
	state.UpdatedAt = now
	return tx.Save(state).Error
}
