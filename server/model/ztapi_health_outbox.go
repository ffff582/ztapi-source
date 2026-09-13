package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ztapiHealthTestAlertOperation(operationID string) (string, error) {
	id, err := uuid.Parse(operationID)
	if len(operationID) != 36 || err != nil || id == uuid.Nil {
		return "", ErrZTAPIHealthTestAlertInvalid
	}
	return id.String(), nil
}

// EnqueueTestAlert requires an authorized operator from the caller. It creates
// only an audited outbox job, never a request, event, incident or breaker state.
func (s *ZTAPIHealthStore) EnqueueTestAlert(ctx context.Context, operationID string, operatorID int) (*ZTAPIHealthOutbox, error) {
	operationID, err := ztapiHealthTestAlertOperation(operationID)
	if err != nil || operatorID <= 0 {
		return nil, ErrZTAPIHealthTestAlertInvalid
	}
	var job ZTAPIHealthOutbox
	err = s.catalogTransaction(ctx, func(tx *gorm.DB) error {
		// The existing catalog DB write lock serializes even different processes.
		// The UUID is the admission boundary: retries return the original audited
		// job while each new release can verify its own production delivery path.
		job = ZTAPIHealthOutbox{}
		err := tx.Where("dedup_key = ? AND kind = ?", "alert-test:"+operationID, "alert_test").First(&job).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := s.now()
		job = ZTAPIHealthOutbox{DedupKey: "alert-test:" + operationID, Kind: "alert_test", Status: "pending", CreatedAt: now, NextAttemptAt: now}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		payload, err := common.Marshal(map[string]any{"operation_id": operationID, "outbox_id": job.ID})
		if err != nil {
			return err
		}
		return tx.Create(&ZTAPIAuditEvent{Action: "model.health_test_alert", OperatorID: operatorID, Payload: string(payload), CreatedAt: now}).Error
	})
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *ZTAPIHealthStore) GetTestAlert(ctx context.Context, operationID string) (*ZTAPIHealthOutbox, error) {
	operationID, err := ztapiHealthTestAlertOperation(operationID)
	if err != nil {
		return nil, err
	}
	var job ZTAPIHealthOutbox
	err = s.DB.WithContext(ctx).Where("dedup_key = ? AND kind = ?", "alert-test:"+operationID, "alert_test").First(&job).Error
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// ClaimOutbox leases at most 100 durable jobs. Delivery is at-least-once;
// receivers should deduplicate using DedupKey, not LeaseToken.
func (s *ZTAPIHealthStore) ClaimOutbox(ctx context.Context, kind string, limit int, leaseSeconds int64) ([]ZTAPIHealthOutbox, error) {
	if kind != "alert" && kind != "route_alert" && kind != "recovery_alert" && kind != "alert_test" && kind != "unpublish" && kind != "coverage" {
		return nil, errors.New("invalid ztapi health outbox kind")
	}
	if leaseSeconds <= 0 || leaseSeconds > 3600 {
		return nil, errors.New("invalid ztapi health lease duration")
	}
	now := s.now()
	var candidates []ZTAPIHealthOutbox
	err := s.DB.WithContext(ctx).Where("kind = ? AND next_attempt_at <= ? AND (status = ? OR (status = ? AND lease_until <= ?))", kind, now, "pending", "leased", now).
		Order("id ASC").Limit(ztapiHealthLimit(limit)).Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	claimed := make([]ZTAPIHealthOutbox, 0, len(candidates))
	for _, candidate := range candidates {
		var job ZTAPIHealthOutbox
		ok := false
		err := s.transaction(ctx, func(tx *gorm.DB) error {
			ok = false
			token := uuid.NewString()
			result := tx.Model(&ZTAPIHealthOutbox{}).
				Where("id = ? AND next_attempt_at <= ? AND (status = ? OR (status = ? AND lease_until <= ?))", candidate.ID, now, "pending", "leased", now).
				Updates(map[string]any{"status": "leased", "lease_until": now + leaseSeconds, "lease_token": token, "attempts": gorm.Expr("attempts + 1")})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			if err := tx.First(&job, candidate.ID).Error; err != nil {
				return err
			}
			ok = true
			return nil
		})
		if err != nil {
			return claimed, err
		}
		if ok {
			claimed = append(claimed, job)
		}
	}
	return claimed, nil
}

// FinishOutbox accepts sanitized error codes only, never raw HTTP response bodies.
// Successful unpublish jobs must go through ProcessUnpublish, not this ACK API.
func (s *ZTAPIHealthStore) FinishOutbox(ctx context.Context, id int64, token, deliveryError string, retryAt int64) error {
	return s.FinishOutboxWithReceipt(ctx, id, token, deliveryError, retryAt, "")
}

// Validate exact field names/types and re-encode only safe values. Never store
// a provider response body, even when it contains a valid receipt alongside PII.
func ztapiHealthDeliveryReceipt(receipt string) (string, error) {
	if receipt == "" {
		return "", nil
	}
	if len(receipt) > 512 {
		return "", ErrZTAPIHealthInvalidReceipt
	}
	var root, result map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(receipt, &root); err != nil || len(root) != 2 {
		return "", ErrZTAPIHealthInvalidReceipt
	}
	var ok bool
	if err := common.Unmarshal(root["ok"], &ok); err != nil || !ok {
		return "", ErrZTAPIHealthInvalidReceipt
	}
	if err := common.Unmarshal(root["result"], &result); err != nil || len(result) != 2 {
		return "", ErrZTAPIHealthInvalidReceipt
	}
	var messageID, date *int64
	if err := common.Unmarshal(result["message_id"], &messageID); err != nil || messageID == nil || *messageID <= 0 {
		return "", ErrZTAPIHealthInvalidReceipt
	}
	if err := common.Unmarshal(result["date"], &date); err != nil || date == nil || *date < 0 {
		return "", ErrZTAPIHealthInvalidReceipt
	}
	var safe struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
			Date      int64 `json:"date"`
		} `json:"result"`
	}
	safe.OK, safe.Result.MessageID, safe.Result.Date = true, *messageID, *date
	encoded, err := common.Marshal(safe)
	return string(encoded), err
}

// FinishOutboxWithReceipt atomically persists a sanitized receipt and ACK using
// the same unexpired lease CAS. Webhook senders pass an empty receipt.
func (s *ZTAPIHealthStore) FinishOutboxWithReceipt(ctx context.Context, id int64, token, deliveryError string, retryAt int64, receipt string) error {
	if token == "" || len(deliveryError) > 255 {
		return ErrZTAPIHealthLeaseConflict
	}
	receipt, err := ztapiHealthDeliveryReceipt(receipt)
	if err != nil {
		return err
	}
	if deliveryError != "" && receipt != "" {
		return ErrZTAPIHealthInvalidReceipt
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		// Read the clock after obtaining the row's write lock, including retries.
		if err := tx.Model(&ZTAPIHealthOutbox{}).Where("id = ?", id).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		now := s.now()
		if deliveryError != "" && retryAt <= now {
			return errors.New("ztapi health retry must be scheduled in the future")
		}
		updates := map[string]any{"status": "done", "delivered_at": now, "lease_until": 0, "last_error": "", "delivery_receipt": receipt}
		if deliveryError != "" {
			updates = map[string]any{"status": "pending", "next_attempt_at": retryAt, "lease_until": 0, "last_error": deliveryError, "delivery_receipt": ""}
		}
		query := tx.Model(&ZTAPIHealthOutbox{}).Where("id = ? AND status = ? AND lease_token = ? AND lease_until > ?", id, "leased", token, now)
		if deliveryError == "" {
			query = query.Where("kind <> ?", "unpublish")
		}
		result := query.Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrZTAPIHealthLeaseConflict
		}
		return nil
	})
}

// Catalog work always acquires catalog -> health, never health -> catalog.
// The initial write also serializes SQLite instances, where FOR UPDATE is absent.
func (s *ZTAPIHealthStore) catalogTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	ztapiCatalogWriteMu.Lock()
	defer ztapiCatalogWriteMu.Unlock()
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ZTAPICatalogLock{ID: 1}).Error; err != nil {
			return err
		}
		if err := tx.Model(&ZTAPICatalogLock{}).Where("id = ?", 1).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func (s *ZTAPIHealthStore) unpublishTx(tx *gorm.DB, state *ZTAPIHealthState, now int64) (*ZTAPIModelConfig, error) {
	var c ZTAPIModelConfig
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, state.ModelID).Error; err != nil {
		return nil, err
	}
	if c.Published {
		result := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ?", c.ID, c.Version).
			Updates(map[string]any{"published": false, "publication_snapshot_id": 0, "version": c.Version + 1, "updated_at": now})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, ErrZTAPIModelVersionConflict
		}
		c.Published, c.PublicationSnapshotID, c.Version, c.UpdatedAt = false, 0, c.Version+1, now
	}
	if err := tx.Model(&ZTAPIHealthIncident{}).Where("id = ? AND unpublished_at = ?", state.IncidentID, 0).
		Updates(map[string]any{"unpublished_at": now, "unpublished_version": c.Version}).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// ProcessUnpublish changes only publication status/version/snapshot reference.
// It never replays incident-time config or price data over current admin edits.
func (s *ZTAPIHealthStore) ProcessUnpublish(ctx context.Context, id int64, token string) error {
	if token == "" {
		return ErrZTAPIHealthLeaseConflict
	}
	err := s.catalogTransaction(ctx, func(tx *gorm.DB) error {
		var job ZTAPIHealthOutbox
		if err := tx.First(&job, id).Error; err != nil {
			return err
		}
		if job.Kind != "unpublish" || job.LeaseToken != token {
			return ErrZTAPIHealthLeaseConflict
		}
		if job.Status == "done" || job.Status == "superseded" {
			return nil
		}
		state, err := lockZTAPIHealthState(tx, job.ModelID)
		if err != nil {
			return err
		}
		// Re-read under lock: leases can change while catalog writers wait.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, id).Error; err != nil {
			return err
		}
		now := s.now()
		if job.Status != "leased" || job.LeaseToken != token || job.LeaseUntil <= now {
			return ErrZTAPIHealthLeaseConflict
		}
		status := "superseded"
		if state.Open && state.Generation == job.Generation && state.IncidentID == job.IncidentID {
			var incident ZTAPIHealthIncident
			if err := tx.First(&incident, job.IncidentID).Error; err != nil {
				return err
			}
			if incident.Rule == "verified_all_routes" {
				var route ZTAPIHealthRouteIdentity
				if incident.VerificationCaseID != "" {
					var verificationCase ZTAPIHealthVerificationCase
					if err := tx.First(&verificationCase, "id = ?", incident.VerificationCaseID).Error; err != nil {
						return err
					}
					if verificationCase.ModelID != incident.ModelID || verificationCase.Generation != incident.Generation || verificationCase.State != "completed" || verificationCase.Result != "failure" {
						return ErrZTAPIVerificationInvalid
					}
					route = verificationCase.RouteIdentity()
				} else {
					// Compatibility for incidents created before verification_case_id
					// existed. New incidents always use the exact persisted case.
					var event ZTAPIHealthEvent
					if err := tx.First(&event, incident.TriggerEventID).Error; err != nil {
						return err
					}
					route = ZTAPIHealthRouteIdentity{
						ModelID: event.ModelID, ChannelID: event.ChannelID, EntryProtocol: event.EntryProtocol,
						Protocol: event.UpstreamProtocol, Stream: event.Stream,
						CredentialVersion: ztapiCredentialFingerprintFromDigest(event.CredentialVersion), Generation: event.Generation,
					}
				}
				allUnavailable, err := EvaluateZTAPIModelAvailability(ctx, tx, route)
				if err != nil {
					return err
				}
				if allUnavailable {
					if _, err := s.unpublishTx(tx, state, now); err != nil {
						return err
					}
					status = "done"
				} else {
					if err := tx.Model(state).Updates(map[string]any{
						"open": false, "incident_id": 0, "updated_at": now,
					}).Error; err != nil {
						return err
					}
					if err := tx.Model(&incident).Updates(map[string]any{
						"recovered_at": now, "recovery_evidence": "aggregate_recheck_found_available_route",
					}).Error; err != nil {
						return err
					}
				}
			}
		}
		return tx.Model(&job).Updates(map[string]any{"status": status, "delivered_at": now, "lease_until": 0, "last_error": ""}).Error
	})
	if err == nil {
		invalidateZTAPICatalogCaches()
	}
	return err
}

// ManualRecover requires an already-authorized administrator from the caller.
// Positive operator/evidence checks are not authorization. Historical events
// remain in the 24h window; clearing the breaker never publishes the model.
func (s *ZTAPIHealthStore) ManualRecover(ctx context.Context, modelID int, expectedGeneration uint64, operatorID int, evidence string) error {
	evidence = strings.TrimSpace(evidence)
	if operatorID <= 0 || evidence == "" || len(evidence) > 4096 {
		return errors.New("ztapi health recovery requires operator and evidence reference")
	}
	err := s.catalogTransaction(ctx, func(tx *gorm.DB) error {
		state, err := lockZTAPIHealthState(tx, modelID)
		if err != nil {
			return err
		}
		if state.Generation != expectedGeneration || !state.Open {
			return ErrZTAPIHealthGenerationConflict
		}
		now := s.now()
		c, err := s.unpublishTx(tx, state, now)
		if err != nil {
			return err
		}
		if err := tx.Model(&ZTAPIHealthIncident{}).Where("id = ?", state.IncidentID).
			Updates(map[string]any{"recovered_at": now, "recovery_operator_id": operatorID, "recovery_evidence": evidence}).Error; err != nil {
			return err
		}
		payload, err := common.Marshal(map[string]any{"incident_id": state.IncidentID, "previous_generation": state.Generation, "generation": state.Generation + 1, "evidence": evidence, "published": false})
		if err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIAuditEvent{Action: "model.health_manual_recovery", ModelConfigID: modelID,
			PublicName: c.PublicNameValue(), Version: c.Version, OperatorID: operatorID, Payload: string(payload), CreatedAt: now}).Error; err != nil {
			return err
		}
		recoveryAlert := ZTAPIHealthOutbox{
			DedupKey: fmt.Sprintf("incident:%d:recovery-alert", state.IncidentID), Kind: "recovery_alert",
			ModelID: modelID, Generation: state.Generation, IncidentID: state.IncidentID,
			Status: "pending", NextAttemptAt: now, CreatedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&recoveryAlert).Error; err != nil {
			return err
		}
		state.Generation++
		state.Open, state.ConsecutiveFailures, state.IncidentID, state.UpdatedAt = false, 0, 0, now
		return tx.Save(state).Error
	})
	if err == nil {
		invalidateZTAPICatalogCaches()
	}
	return err
}

// MarkOrphansUnknown is a bounded sweeper, not a paid request retry. The parent
// must choose a cutoff beyond the longest allowed active request duration.
func (s *ZTAPIHealthStore) MarkOrphansUnknown(ctx context.Context, startedBefore int64, limit int) (int, error) {
	if startedBefore >= s.now() {
		return 0, errors.New("ztapi health orphan cutoff must be in the past")
	}
	var pending []ZTAPIHealthRequest
	if err := s.DB.WithContext(ctx).Where("completed = ? AND started_at < ?", false, startedBefore).
		Order("started_at ASC, execution_id ASC").Limit(ztapiHealthLimit(limit)).Find(&pending).Error; err != nil {
		return 0, err
	}
	completed := 0
	for _, request := range pending {
		changed := false
		err := s.transaction(ctx, func(tx *gorm.DB) error {
			changed = false
			state, err := lockZTAPIHealthState(tx, request.ModelID)
			if err != nil {
				return err
			}
			var r ZTAPIHealthRequest
			if err := tx.First(&r, "execution_id = ?", request.ExecutionID).Error; err != nil {
				return err
			}
			if r.Completed || r.StartedAt >= startedBefore {
				return nil
			}
			if err := s.completeTx(tx, state, &r, types.ZTAPIHealthOutcome{Result: "unknown", Reason: "orphaned_admission"}); err != nil {
				return err
			}
			var event ZTAPIHealthEvent
			if err := tx.First(&event, "execution_id = ?", r.ExecutionID).Error; err != nil {
				return err
			}
			now := s.now()
			if err := tx.Create(&ZTAPIHealthOutbox{DedupKey: fmt.Sprintf("orphan:%d", event.ID), Kind: "coverage", ModelID: r.ModelID,
				Generation: r.Generation, EventID: event.ID, Status: "pending", CreatedAt: now, NextAttemptAt: now}).Error; err != nil {
				return err
			}
			changed = true
			return nil
		})
		if err != nil {
			return completed, err
		}
		if changed {
			completed++
		}
	}
	return completed, nil
}

func (s *ZTAPIHealthStore) GetRequest(ctx context.Context, executionID string) (*ZTAPIHealthRequest, error) {
	var r ZTAPIHealthRequest
	err := s.DB.WithContext(ctx).First(&r, "execution_id = ?", executionID).Error
	return &r, err
}

func (s *ZTAPIHealthStore) ListIncidents(ctx context.Context, modelID int, afterID int64, limit int) ([]ZTAPIHealthIncident, error) {
	var incidents []ZTAPIHealthIncident
	q := s.DB.WithContext(ctx).Where("id > ?", afterID)
	if modelID > 0 {
		q = q.Where("model_id = ?", modelID)
	}
	err := q.Order("id ASC").Limit(ztapiHealthLimit(limit)).Find(&incidents).Error
	return incidents, err
}

func (s *ZTAPIHealthStore) ListOutbox(ctx context.Context, modelID int, afterID int64, limit int) ([]ZTAPIHealthOutbox, error) {
	var jobs []ZTAPIHealthOutbox
	q := s.DB.WithContext(ctx).Where("id > ?", afterID)
	if modelID > 0 {
		q = q.Where("model_id = ?", modelID)
	}
	err := q.Order("id ASC").Limit(ztapiHealthLimit(limit)).Find(&jobs).Error
	return jobs, err
}

type ZTAPIHealthCoverage struct {
	Stream           bool
	Source           string
	Modality         string
	Operation        string
	ValidSamples     int64
	UnknownSamples   int64
	ExcludedSamples  int64
	LastValidAt      int64
	LastCompletionAt int64
}

func (s *ZTAPIHealthStore) Coverage(ctx context.Context, modelID int, since int64) ([]ZTAPIHealthCoverage, error) {
	var coverage []ZTAPIHealthCoverage
	err := s.DB.WithContext(ctx).Model(&ZTAPIHealthEvent{}).
		Select("stream, source, modality, operation, SUM(CASE WHEN counted = ? OR (source = 'real' AND result IN ('success','suspected')) THEN 1 ELSE 0 END) AS valid_samples, SUM(CASE WHEN result = 'unknown' THEN 1 ELSE 0 END) AS unknown_samples, SUM(CASE WHEN result = 'excluded' THEN 1 ELSE 0 END) AS excluded_samples, MAX(CASE WHEN counted = ? OR (source = 'real' AND result IN ('success','suspected')) THEN completed_at ELSE 0 END) AS last_valid_at, MAX(completed_at) AS last_completion_at", true, true).
		Where("model_id = ? AND completed_at > ? AND completed_at <= ? AND stale_generation = ?", modelID, since, s.now(), false).
		Group("stream, source, modality, operation").Order("stream ASC, source ASC, modality ASC, operation ASC").Find(&coverage).Error
	return coverage, err
}
