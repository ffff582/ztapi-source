package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrZTAPIVerificationInvalid            = errors.New("invalid ztapi verification data")
	ErrZTAPIVerificationLeaseConflict      = errors.New("ztapi verification lease conflict")
	ErrZTAPIVerificationGenerationConflict = errors.New("ztapi verification generation conflict")
	ErrZTAPIVerificationDuplicateProbe     = errors.New("ztapi verification probe request already recorded")
)

const ZTAPIVerificationMaxOutputTokens int64 = 64

// ZTAPICredentialFingerprint can only be created by hashing credential
// material in this package. Its unexported representation prevents callers
// from labelling arbitrary strings as already-sanitized fingerprints.
type ZTAPICredentialFingerprint struct{ digest string }

func FingerprintZTAPICredential(credential string) (ZTAPICredentialFingerprint, error) {
	if credential == "" {
		return ZTAPICredentialFingerprint{}, ErrZTAPIVerificationInvalid
	}
	digest := sha256.Sum256([]byte(credential))
	return ZTAPICredentialFingerprint{digest: hex.EncodeToString(digest[:])}, nil
}

func ZTAPICredentialVersionMatchesKey(expected, key string) bool {
	if !validZTAPICredentialVersion(strings.TrimSpace(expected)) || key == "" {
		return false
	}
	// Health events fingerprint the final outbound credential headers, while
	// channel storage contains the raw key. These are the standard credential
	// forms supported by managed ZTAPI adapters. Custom compound credentials
	// intentionally fail closed.
	materials := []string{
		key,
		"authorization\x00Bearer " + key,
		"authorization\x00" + key,
		"api-key\x00" + key,
		"x-api-key\x00" + key,
		"x-goog-api-key\x00" + key,
	}
	for _, material := range materials {
		fingerprint, err := FingerprintZTAPICredential(material)
		if err == nil && fingerprint.String() == strings.TrimSpace(expected) {
			return true
		}
	}
	return false
}

func ztapiChannelHasCredentialVersion(channel Channel, expected string) bool {
	for index, key := range channel.GetKeys() {
		if channel.ChannelInfo.IsMultiKey && channel.ChannelInfo.MultiKeyStatusList != nil {
			if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status != common.ChannelStatusEnabled {
				continue
			}
		}
		if ZTAPICredentialVersionMatchesKey(expected, key) {
			return true
		}
	}
	return false
}

func (f ZTAPICredentialFingerprint) String() string { return f.digest }

func ztapiCredentialFingerprintFromDigest(digest string) ZTAPICredentialFingerprint {
	return ZTAPICredentialFingerprint{digest: digest}
}

// ZTAPIHealthRouteIdentity contains only server-generated routing metadata.
// CredentialVersion is an opaque SHA-256 fingerprint, never a usable
// credential.
type ZTAPIHealthRouteIdentity struct {
	ModelID           int
	ChannelID         int
	EntryProtocol     string
	Protocol          string
	Stream            bool
	CredentialVersion ZTAPICredentialFingerprint
	Generation        uint64
}

type ZTAPIHealthSuspicion struct {
	Route              ZTAPIHealthRouteIdentity
	SourceEventID      int64
	SourceAttemptIndex int
}

type ZTAPIHealthVerificationCase struct {
	ID                string `gorm:"primaryKey;size:36"`
	ModelID           int    `gorm:"not null;index"`
	ChannelID         int    `gorm:"not null;index"`
	EntryProtocol     string `gorm:"size:32;not null;default:''"`
	Protocol          string `gorm:"size:32;not null"`
	Stream            bool   `gorm:"not null"`
	CredentialVersion string `gorm:"size:64;not null"`
	Generation        uint64 `gorm:"not null"`
	SourceEventID     int64  `gorm:"not null;index"`
	State             string `gorm:"size:16;not null;index:idx_ztapi_verify_due,priority:1"`
	ReadyAt           int64  `gorm:"not null;index:idx_ztapi_verify_due,priority:2"`
	LeaseToken        string `gorm:"size:36;not null"`
	LeaseUntil        int64  `gorm:"not null"`
	Attempts          int    `gorm:"not null"`
	ProbeRequestID    string `gorm:"size:255;not null"`
	DispatchAt        int64  `gorm:"not null;default:0"`
	ReservedNanoUSD   int64  `gorm:"not null;default:0"`
	EstimateNanoUSD   int64  `gorm:"not null;default:0"`
	ExcessNanoUSD     int64  `gorm:"not null;default:0"`
	Result            string `gorm:"size:16;not null"`
	CreatedAt         int64  `gorm:"not null"`
	CompletedAt       int64  `gorm:"not null"`
	CancelledAt       int64  `gorm:"not null"`
}

func (ZTAPIHealthVerificationCase) TableName() string {
	return "ztapi_health_verification_cases"
}

func (c *ZTAPIHealthVerificationCase) BeforeCreate(_ *gorm.DB) error {
	if strings.TrimSpace(c.EntryProtocol) == "" {
		c.EntryProtocol = strings.TrimSpace(c.Protocol)
	}
	return nil
}

func (c ZTAPIHealthVerificationCase) RouteIdentity() ZTAPIHealthRouteIdentity {
	return ZTAPIHealthRouteIdentity{
		ModelID: c.ModelID, ChannelID: c.ChannelID, EntryProtocol: c.EntryProtocol,
		Protocol: c.Protocol, Stream: c.Stream,
		CredentialVersion: ztapiCredentialFingerprintFromDigest(c.CredentialVersion), Generation: c.Generation,
	}
}

type ZTAPIHealthRouteState struct {
	ID                  int64  `gorm:"primaryKey"`
	ModelID             int    `gorm:"not null;uniqueIndex:idx_ztapi_health_route,priority:1;index"`
	ChannelID           int    `gorm:"not null;uniqueIndex:idx_ztapi_health_route,priority:2;index"`
	EntryProtocol       string `gorm:"size:32;not null;default:'';uniqueIndex:idx_ztapi_health_route,priority:3"`
	Protocol            string `gorm:"size:32;not null;uniqueIndex:idx_ztapi_health_route,priority:4"`
	Stream              bool   `gorm:"not null;uniqueIndex:idx_ztapi_health_route,priority:5"`
	CredentialVersion   string `gorm:"size:64;not null;uniqueIndex:idx_ztapi_health_route,priority:6"`
	Generation          uint64 `gorm:"not null;uniqueIndex:idx_ztapi_health_route,priority:7"`
	IndependentFailures int64  `gorm:"not null"`
	Open                bool   `gorm:"not null;index"`
	LastProbeRequestID  string `gorm:"size:255;not null"`
	LastResult          string `gorm:"size:16;not null"`
	OpenedAt            int64  `gorm:"not null"`
	UpdatedAt           int64  `gorm:"not null"`
}

func (ZTAPIHealthRouteState) TableName() string { return "ztapi_health_route_states" }

func (s *ZTAPIHealthRouteState) BeforeCreate(_ *gorm.DB) error {
	if strings.TrimSpace(s.EntryProtocol) == "" {
		s.EntryProtocol = strings.TrimSpace(s.Protocol)
	}
	return nil
}

// ZTAPIHealthVerificationGate limits paid verification work for one exact
// route identity. Independent channels, stream modes and credentials must not
// suppress one another's evidence.
type ZTAPIHealthVerificationGate struct {
	ModelID           int    `gorm:"primaryKey;autoIncrement:false"`
	ChannelID         int    `gorm:"primaryKey;autoIncrement:false"`
	EntryProtocol     string `gorm:"primaryKey;size:32;default:''"`
	Protocol          string `gorm:"primaryKey;size:32"`
	Stream            bool   `gorm:"primaryKey;autoIncrement:false"`
	CredentialVersion string `gorm:"primaryKey;size:64"`
	Generation        uint64 `gorm:"primaryKey;autoIncrement:false"`
	ActiveCaseID      string `gorm:"size:36;not null"`
	LastCaseID        string `gorm:"size:36;not null"`
	CooldownUntil     int64  `gorm:"not null"`
	UpdatedAt         int64  `gorm:"not null"`
}

func (ZTAPIHealthVerificationGate) TableName() string { return "ztapi_health_verification_gates" }

func (g *ZTAPIHealthVerificationGate) BeforeCreate(_ *gorm.DB) error {
	if strings.TrimSpace(g.EntryProtocol) == "" {
		g.EntryProtocol = strings.TrimSpace(g.Protocol)
	}
	return nil
}

// ZTAPIHealthProbeEvidence makes probe request IDs globally single-use. It
// contains no customer content, response content, tool arguments, or secrets.
type ZTAPIHealthProbeEvidence struct {
	ProbeRequestID string `gorm:"primaryKey;size:255"`
	CaseID         string `gorm:"size:36;not null;uniqueIndex"`
	RecordedAt     int64  `gorm:"not null"`
}

func (ZTAPIHealthProbeEvidence) TableName() string { return "ztapi_health_probe_evidence" }

type ZTAPIHealthProbeCompletion struct {
	CaseID          string
	LeaseToken      string
	Generation      uint64
	ProbeRequestID  string
	Result          string
	ObservedNanoUSD int64
}

type ZTAPIHealthVerificationDispatchAdmission struct {
	Active                  bool
	Route                   ZTAPIHealthRouteIdentity
	InputTokens             int64
	InputNanoUSDPerMillion  int64
	OutputNanoUSDPerMillion int64
	FixedCostNanoUSD        int64
}

// ZTAPIHealthVerificationDispatchCheck revalidates the exact published route
// inside the dispatch transaction. It must not perform network side effects.
type ZTAPIHealthVerificationDispatchCheck func(context.Context, *gorm.DB, ZTAPIHealthVerificationCase) (ZTAPIHealthVerificationDispatchAdmission, error)

type ZTAPIHealthVerificationStore struct {
	DB                   *gorm.DB
	transactionFn        func(context.Context, func(*gorm.DB) error) error
	retryObserver        func(error)
	afterAggregationLock func()
	beforeStateLock      func()
}

func NewZTAPIHealthVerificationStore(db *gorm.DB) *ZTAPIHealthVerificationStore {
	return &ZTAPIHealthVerificationStore{DB: db}
}

func (s *ZTAPIHealthVerificationStore) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	if s.transactionFn != nil {
		return s.transactionFn(ctx, fn)
	}
	healthStore := NewZTAPIHealthStore(s.DB)
	healthStore.retryObserver = s.retryObserver
	return healthStore.transaction(ctx, fn)
}

func validZTAPIHealthRouteIdentity(route ZTAPIHealthRouteIdentity) bool {
	entryProtocol := canonicalZTAPIHealthEntryProtocol(route)
	protocol := strings.TrimSpace(route.Protocol)
	credentialVersion := strings.TrimSpace(route.CredentialVersion.String())
	return route.ModelID > 0 && route.ChannelID > 0 && route.Generation > 0 &&
		entryProtocol != "" && len(entryProtocol) <= 32 && protocol != "" && len(protocol) <= 32 && validZTAPICredentialVersion(credentialVersion)
}

func canonicalZTAPIHealthEntryProtocol(route ZTAPIHealthRouteIdentity) string {
	entryProtocol := strings.TrimSpace(route.EntryProtocol)
	if entryProtocol == "" {
		entryProtocol = strings.TrimSpace(route.Protocol)
	}
	return entryProtocol
}

func validZTAPICredentialVersion(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func applyZTAPIHealthRouteIdentity(query *gorm.DB, route ZTAPIHealthRouteIdentity) *gorm.DB {
	return query.Where(
		"model_id = ? AND channel_id = ? AND entry_protocol = ? AND protocol = ? AND stream = ? AND credential_version = ? AND generation = ?",
		route.ModelID, route.ChannelID, canonicalZTAPIHealthEntryProtocol(route), strings.TrimSpace(route.Protocol), route.Stream,
		strings.TrimSpace(route.CredentialVersion.String()), route.Generation,
	)
}

func sameZTAPIHealthRouteIdentity(left, right ZTAPIHealthRouteIdentity) bool {
	return left.ModelID == right.ModelID && left.ChannelID == right.ChannelID &&
		canonicalZTAPIHealthEntryProtocol(left) == canonicalZTAPIHealthEntryProtocol(right) &&
		strings.TrimSpace(left.Protocol) == strings.TrimSpace(right.Protocol) && left.Stream == right.Stream &&
		strings.TrimSpace(left.CredentialVersion.String()) == strings.TrimSpace(right.CredentialVersion.String()) &&
		left.Generation == right.Generation
}

func ztapiVerificationProbeRequestID(verificationCase ZTAPIHealthVerificationCase) string {
	return "ztapi-health:" + verificationCase.ID + ":" + strconv.Itoa(verificationCase.Attempts)
}

func verificationCaseFromSuspicion(suspicion ZTAPIHealthSuspicion, now time.Time) ZTAPIHealthVerificationCase {
	route := suspicion.Route
	return ZTAPIHealthVerificationCase{
		ID: uuid.NewString(), ModelID: route.ModelID, ChannelID: route.ChannelID,
		EntryProtocol: canonicalZTAPIHealthEntryProtocol(route), Protocol: strings.TrimSpace(route.Protocol), Stream: route.Stream,
		CredentialVersion: strings.TrimSpace(route.CredentialVersion.String()),
		Generation:        route.Generation, SourceEventID: suspicion.SourceEventID,
		State: "queued", ReadyAt: now.UTC().UnixMilli(), CreatedAt: now.UTC().UnixMilli(),
	}
}

func lockZTAPIHealthVerificationGate(tx *gorm.DB, route ZTAPIHealthRouteIdentity) (*ZTAPIHealthVerificationGate, error) {
	gate := ZTAPIHealthVerificationGate{
		ModelID: route.ModelID, ChannelID: route.ChannelID, EntryProtocol: canonicalZTAPIHealthEntryProtocol(route),
		Protocol: strings.TrimSpace(route.Protocol), Stream: route.Stream,
		CredentialVersion: strings.TrimSpace(route.CredentialVersion.String()), Generation: route.Generation,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&gate).Error; err != nil {
		return nil, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&gate, "model_id = ? AND channel_id = ? AND entry_protocol = ? AND protocol = ? AND stream = ? AND credential_version = ? AND generation = ?",
			route.ModelID, route.ChannelID, canonicalZTAPIHealthEntryProtocol(route), strings.TrimSpace(route.Protocol), route.Stream, strings.TrimSpace(route.CredentialVersion.String()), route.Generation).Error; err != nil {
		return nil, err
	}
	return &gate, nil
}

func saveZTAPIHealthVerificationGate(tx *gorm.DB, gate *ZTAPIHealthVerificationGate) error {
	result := tx.Model(&ZTAPIHealthVerificationGate{}).
		Where("model_id = ? AND channel_id = ? AND entry_protocol = ? AND protocol = ? AND stream = ? AND credential_version = ? AND generation = ?",
			gate.ModelID, gate.ChannelID, gate.EntryProtocol, gate.Protocol, gate.Stream, gate.CredentialVersion, gate.Generation).
		Updates(map[string]any{
			"active_case_id": gate.ActiveCaseID, "last_case_id": gate.LastCaseID,
			"cooldown_until": gate.CooldownUntil, "updated_at": gate.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrZTAPIVerificationLeaseConflict
	}
	return nil
}

func validateZTAPIHealthSuspicionEvent(tx *gorm.DB, suspicion ZTAPIHealthSuspicion) error {
	var event ZTAPIHealthEvent
	if err := tx.First(&event, "id = ?", suspicion.SourceEventID).Error; err != nil {
		return ErrZTAPIVerificationInvalid
	}
	route := suspicion.Route
	eventEntryProtocol := strings.TrimSpace(event.EntryProtocol)
	if eventEntryProtocol == "" {
		eventEntryProtocol = strings.TrimSpace(event.UpstreamProtocol)
	}
	if event.ModelID != route.ModelID || event.Generation != route.Generation || event.Stream != route.Stream ||
		eventEntryProtocol != canonicalZTAPIHealthEntryProtocol(route) ||
		event.Source != "real" || event.StaleGeneration || event.Counted {
		return ErrZTAPIVerificationInvalid
	}
	if suspicion.SourceAttemptIndex == 0 {
		if event.ChannelID != route.ChannelID || strings.TrimSpace(event.UpstreamProtocol) != strings.TrimSpace(route.Protocol) ||
			event.CredentialVersion != strings.TrimSpace(route.CredentialVersion.String()) || event.Result != "suspected" {
			return ErrZTAPIVerificationInvalid
		}
		return nil
	}
	var outcome types.ZTAPIHealthOutcome
	if suspicion.SourceAttemptIndex < 1 || common.Unmarshal([]byte(event.Outcome), &outcome) != nil || suspicion.SourceAttemptIndex > len(outcome.Attempts) {
		return ErrZTAPIVerificationInvalid
	}
	attempt := outcome.Attempts[suspicion.SourceAttemptIndex-1]
	if attempt.Index != suspicion.SourceAttemptIndex || attempt.ChannelID != route.ChannelID ||
		strings.TrimSpace(attempt.Protocol) != strings.TrimSpace(route.Protocol) ||
		attempt.CredentialVersion != strings.TrimSpace(route.CredentialVersion.String()) ||
		attempt.Result != "suspected" || !attempt.Dispatched {
		return ErrZTAPIVerificationInvalid
	}
	return nil
}

func cancelStaleZTAPIHealthVerificationCasesTx(tx *gorm.DB, route ZTAPIHealthRouteIdentity, now time.Time) error {
	var stale []ZTAPIHealthVerificationCase
	if err := tx.Where(
		"model_id = ? AND channel_id = ? AND protocol = ? AND stream = ? AND credential_version = ? AND generation <> ? AND state IN ?",
		route.ModelID, route.ChannelID, strings.TrimSpace(route.Protocol), route.Stream,
		strings.TrimSpace(route.CredentialVersion.String()), route.Generation, []string{"queued", "claimed"},
	).Find(&stale).Error; err != nil {
		return err
	}
	for i := range stale {
		verificationCase := &stale[i]
		gate, err := lockZTAPIHealthVerificationGate(tx, verificationCase.RouteIdentity())
		if err != nil {
			return err
		}
		verificationCase.State = "cancelled"
		verificationCase.Result = "route_changed"
		verificationCase.CancelledAt = now.UTC().UnixMilli()
		verificationCase.LeaseToken = ""
		verificationCase.LeaseUntil = 0
		if err := tx.Save(verificationCase).Error; err != nil {
			return err
		}
		if gate.ActiveCaseID == verificationCase.ID {
			gate.LastCaseID = verificationCase.ID
			gate.ActiveCaseID = ""
			gate.CooldownUntil = 0
			gate.UpdatedAt = now.UTC().UnixMilli()
			if err := saveZTAPIHealthVerificationGate(tx, gate); err != nil {
				return err
			}
		}
	}
	return nil
}

func enqueueZTAPIHealthSuspicionTx(tx *gorm.DB, suspicion ZTAPIHealthSuspicion, now time.Time) (result ZTAPIHealthVerificationCase, created bool, err error) {
	if !validZTAPIHealthRouteIdentity(suspicion.Route) || suspicion.SourceEventID <= 0 || suspicion.SourceAttemptIndex < 0 || now.IsZero() {
		return result, false, ErrZTAPIVerificationInvalid
	}
	if eventErr := validateZTAPIHealthSuspicionEvent(tx, suspicion); eventErr != nil {
		return result, false, eventErr
	}
	if err := cancelStaleZTAPIHealthVerificationCasesTx(tx, suspicion.Route, now); err != nil {
		return result, false, err
	}
	gate, lockErr := lockZTAPIHealthVerificationGate(tx, suspicion.Route)
	if lockErr != nil {
		return result, false, lockErr
	}
	if gate.ActiveCaseID != "" {
		var active ZTAPIHealthVerificationCase
		queryErr := tx.First(&active, "id = ?", gate.ActiveCaseID).Error
		if queryErr != nil && !errors.Is(queryErr, gorm.ErrRecordNotFound) {
			return result, false, queryErr
		}
		if queryErr == nil && (active.State == "queued" || active.State == "claimed" || active.State == "dispatching") {
			if active.State == "dispatching" {
				return active, false, nil
			}
			if active.Generation == suspicion.Route.Generation {
				return active, false, nil
			}
			if err := tx.Model(&active).Updates(map[string]any{
				"state": "cancelled", "cancelled_at": now.UTC().UnixMilli(), "lease_token": "", "lease_until": 0,
			}).Error; err != nil {
				return result, false, err
			}
			gate.LastCaseID = active.ID
			gate.CooldownUntil = 0
		}
		gate.ActiveCaseID = ""
	}
	if gate.CooldownUntil > now.UTC().UnixMilli() {
		if gate.LastCaseID == "" {
			return result, false, ErrZTAPIVerificationInvalid
		}
		if err := tx.First(&result, "id = ?", gate.LastCaseID).Error; err != nil {
			return result, false, err
		}
		if result.Generation == suspicion.Route.Generation {
			return result, false, nil
		}
		gate.CooldownUntil = 0
	}
	candidate := verificationCaseFromSuspicion(suspicion, now)
	if err := tx.Create(&candidate).Error; err != nil {
		return result, false, err
	}
	gate.ActiveCaseID = candidate.ID
	gate.UpdatedAt = now.UTC().UnixMilli()
	if err := saveZTAPIHealthVerificationGate(tx, gate); err != nil {
		return result, false, err
	}
	return candidate, true, nil
}

func (s *ZTAPIHealthVerificationStore) EnqueueSuspicion(ctx context.Context, suspicion ZTAPIHealthSuspicion, now time.Time) (result ZTAPIHealthVerificationCase, created bool, err error) {
	if !validZTAPIHealthRouteIdentity(suspicion.Route) || suspicion.SourceEventID <= 0 || suspicion.SourceAttemptIndex < 0 || now.IsZero() {
		return result, false, ErrZTAPIVerificationInvalid
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		result, created, err = enqueueZTAPIHealthSuspicionTx(tx, suspicion, now)
		return err
	})
	return result, created && err == nil, err
}

func cancelQueuedZTAPIHealthVerificationTx(tx *gorm.DB, route ZTAPIHealthRouteIdentity, now time.Time) (int64, error) {
	if !validZTAPIHealthRouteIdentity(route) || now.IsZero() {
		return 0, ErrZTAPIVerificationInvalid
	}
	gate, lockErr := lockZTAPIHealthVerificationGate(tx, route)
	if lockErr != nil {
		return 0, lockErr
	}
	if gate.ActiveCaseID == "" {
		return 0, nil
	}
	update := applyZTAPIHealthRouteIdentity(tx.Model(&ZTAPIHealthVerificationCase{}), route).
		Where("id = ? AND state IN ?", gate.ActiveCaseID, []string{"queued", "claimed"}).
		Updates(map[string]any{"state": "cancelled", "cancelled_at": now.UTC().UnixMilli(), "lease_token": "", "lease_until": 0})
	if update.Error != nil {
		return 0, update.Error
	}
	if update.RowsAffected > 0 {
		gate.LastCaseID = gate.ActiveCaseID
		gate.ActiveCaseID = ""
		gate.CooldownUntil = now.Add(5 * time.Minute).UTC().UnixMilli()
		gate.UpdatedAt = now.UTC().UnixMilli()
		if err := saveZTAPIHealthVerificationGate(tx, gate); err != nil {
			return 0, err
		}
	}
	return update.RowsAffected, nil
}

func (s *ZTAPIHealthVerificationStore) CancelQueued(ctx context.Context, route ZTAPIHealthRouteIdentity, now time.Time) (int64, error) {
	if !validZTAPIHealthRouteIdentity(route) || now.IsZero() {
		return 0, ErrZTAPIVerificationInvalid
	}
	var affected int64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var cancelErr error
		affected, cancelErr = cancelQueuedZTAPIHealthVerificationTx(tx, route, now)
		return cancelErr
	})
	return affected, err
}

func resetClosedZTAPIHealthRouteEvidenceTx(tx *gorm.DB, route ZTAPIHealthRouteIdentity, now time.Time) error {
	if !validZTAPIHealthRouteIdentity(route) || now.IsZero() {
		return ErrZTAPIVerificationInvalid
	}
	var state ZTAPIHealthRouteState
	query := applyZTAPIHealthRouteIdentity(tx.Clauses(clause.Locking{Strength: "UPDATE"}), route).Take(&state)
	if errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	if query.Error != nil {
		return query.Error
	}
	if state.Open || state.IndependentFailures == 0 {
		return nil
	}
	return tx.Model(&state).Updates(map[string]any{
		"independent_failures": 0,
		"last_result":          "healthy",
		"updated_at":           now.UTC().UnixMilli(),
	}).Error
}

func validZTAPIVerificationLease(lease time.Duration) bool {
	return lease >= time.Second && lease <= 5*time.Minute
}

func (s *ZTAPIHealthVerificationStore) Claim(ctx context.Context, now time.Time, lease time.Duration) (claimed *ZTAPIHealthVerificationCase, err error) {
	if now.IsZero() || !validZTAPIVerificationLease(lease) {
		return nil, ErrZTAPIVerificationInvalid
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		claimed = nil
		var candidate ZTAPIHealthVerificationCase
		nowMillis := now.UTC().UnixMilli()
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("(state = ? AND ready_at <= ?) OR (state = ? AND lease_until <= ?)", "queued", nowMillis, "claimed", nowMillis).
			Order("ready_at ASC, id ASC").Take(&candidate)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if query.Error != nil {
			return query.Error
		}
		token := uuid.NewString()
		update := tx.Model(&ZTAPIHealthVerificationCase{}).
			Where("id = ? AND ((state = ? AND ready_at <= ?) OR (state = ? AND lease_until <= ?))", candidate.ID, "queued", nowMillis, "claimed", nowMillis).
			Updates(map[string]any{"state": "claimed", "lease_token": token, "lease_until": now.Add(lease).UTC().UnixMilli(), "attempts": gorm.Expr("attempts + 1")})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			return nil
		}
		if err := tx.First(&candidate, "id = ?", candidate.ID).Error; err != nil {
			return err
		}
		claimed = &candidate
		return nil
	})
	return claimed, err
}

func validZTAPIVerificationDelay(delay time.Duration) bool {
	return delay >= time.Second && delay <= 5*time.Minute
}

func validZTAPIVerificationResultCode(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 16 {
		return false
	}
	for _, char := range code {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func lockClaimedZTAPIHealthVerificationCase(tx *gorm.DB, id, leaseToken string, generation uint64, now time.Time) (ZTAPIHealthVerificationCase, error) {
	var verificationCase ZTAPIHealthVerificationCase
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&verificationCase, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return verificationCase, err
	}
	if generation != 0 && verificationCase.Generation != generation {
		return verificationCase, ErrZTAPIVerificationGenerationConflict
	}
	if verificationCase.State != "claimed" || verificationCase.LeaseToken != strings.TrimSpace(leaseToken) || verificationCase.LeaseUntil <= now.UTC().UnixMilli() {
		return verificationCase, ErrZTAPIVerificationLeaseConflict
	}
	return verificationCase, nil
}

func finishZTAPIHealthVerificationGate(tx *gorm.DB, verificationCase *ZTAPIHealthVerificationCase, now time.Time) error {
	gate, err := lockZTAPIHealthVerificationGate(tx, verificationCase.RouteIdentity())
	if err != nil {
		return err
	}
	if gate.ActiveCaseID != verificationCase.ID {
		return ErrZTAPIVerificationLeaseConflict
	}
	gate.LastCaseID = verificationCase.ID
	gate.ActiveCaseID = ""
	gate.CooldownUntil = now.Add(5 * time.Minute).UTC().UnixMilli()
	gate.UpdatedAt = now.UTC().UnixMilli()
	return saveZTAPIHealthVerificationGate(tx, gate)
}

func lockZTAPIHealthVerificationGateForCase(tx *gorm.DB, caseID string) error {
	var snapshot ZTAPIHealthVerificationCase
	if err := tx.Select("model_id", "channel_id", "entry_protocol", "protocol", "stream", "credential_version", "generation").First(&snapshot, "id = ?", strings.TrimSpace(caseID)).Error; err != nil {
		return err
	}
	gate, err := lockZTAPIHealthVerificationGate(tx, snapshot.RouteIdentity())
	if err != nil {
		return err
	}
	if gate.ActiveCaseID != strings.TrimSpace(caseID) {
		return ErrZTAPIVerificationLeaseConflict
	}
	return nil
}

func cancelClaimedZTAPIHealthVerificationCaseTx(tx *gorm.DB, verificationCase *ZTAPIHealthVerificationCase, reason string, now time.Time) error {
	verificationCase.State = "cancelled"
	verificationCase.Result = strings.TrimSpace(reason)
	verificationCase.CancelledAt = now.UTC().UnixMilli()
	verificationCase.LeaseToken = ""
	verificationCase.LeaseUntil = 0
	if err := tx.Save(verificationCase).Error; err != nil {
		return err
	}
	return finishZTAPIHealthVerificationGate(tx, verificationCase, now)
}

func (s *ZTAPIHealthVerificationStore) DeferClaim(ctx context.Context, caseID, leaseToken string, now time.Time, delay time.Duration) error {
	if strings.TrimSpace(caseID) == "" || strings.TrimSpace(leaseToken) == "" || now.IsZero() || !validZTAPIVerificationDelay(delay) {
		return ErrZTAPIVerificationInvalid
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		verificationCase, err := lockClaimedZTAPIHealthVerificationCase(tx, caseID, leaseToken, 0, now)
		if err != nil {
			return err
		}
		verificationCase.State = "queued"
		verificationCase.ReadyAt = now.Add(delay).UTC().UnixMilli()
		verificationCase.LeaseToken = ""
		verificationCase.LeaseUntil = 0
		return tx.Save(&verificationCase).Error
	})
}

func (s *ZTAPIHealthVerificationStore) CancelClaim(ctx context.Context, caseID, leaseToken string, generation uint64, reason string, now time.Time) error {
	if strings.TrimSpace(caseID) == "" || strings.TrimSpace(leaseToken) == "" || generation == 0 || !validZTAPIVerificationResultCode(reason) || now.IsZero() {
		return ErrZTAPIVerificationInvalid
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := lockZTAPIHealthVerificationGateForCase(tx, caseID); err != nil {
			return err
		}
		verificationCase, err := lockClaimedZTAPIHealthVerificationCase(tx, caseID, leaseToken, generation, now)
		if err != nil {
			return err
		}
		return cancelClaimedZTAPIHealthVerificationCaseTx(tx, &verificationCase, reason, now)
	})
}

func validZTAPIHealthVerificationDispatchAdmission(admission ZTAPIHealthVerificationDispatchAdmission) bool {
	if admission.InputTokens < 0 || admission.InputNanoUSDPerMillion < 0 || admission.OutputNanoUSDPerMillion < 0 || admission.FixedCostNanoUSD < 0 {
		return false
	}
	return admission.FixedCostNanoUSD > 0 || admission.InputNanoUSDPerMillion > 0 || admission.OutputNanoUSDPerMillion > 0
}

func ztapiHealthVerificationReservation(admission ZTAPIHealthVerificationDispatchAdmission) (int64, error) {
	if admission.FixedCostNanoUSD > 0 {
		return admission.FixedCostNanoUSD, nil
	}
	return ZTAPIProbeEstimateNanoUSD(
		admission.InputTokens,
		ZTAPIVerificationMaxOutputTokens,
		admission.InputNanoUSDPerMillion,
		admission.OutputNanoUSDPerMillion,
	)
}

func (s *ZTAPIHealthVerificationStore) BeginDispatch(
	ctx context.Context,
	caseID, leaseToken string,
	generation uint64,
	now time.Time,
	lease time.Duration,
	check ZTAPIHealthVerificationDispatchCheck,
) (dispatched ZTAPIHealthVerificationCase, send bool, err error) {
	if strings.TrimSpace(caseID) == "" || strings.TrimSpace(leaseToken) == "" || generation == 0 || now.IsZero() || !validZTAPIVerificationLease(lease) || check == nil {
		return dispatched, false, ErrZTAPIVerificationInvalid
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		send = false
		if gateErr := lockZTAPIHealthVerificationGateForCase(tx, caseID); gateErr != nil {
			return gateErr
		}
		verificationCase, lockErr := lockClaimedZTAPIHealthVerificationCase(tx, caseID, leaseToken, generation, now)
		if lockErr != nil {
			return lockErr
		}

		admission, checkErr := check(ctx, tx, verificationCase)
		if checkErr != nil {
			return checkErr
		}
		if !admission.Active || !validZTAPIHealthRouteIdentity(admission.Route) || !sameZTAPIHealthRouteIdentity(admission.Route, verificationCase.RouteIdentity()) {
			if err := cancelClaimedZTAPIHealthVerificationCaseTx(tx, &verificationCase, "route_changed", now); err != nil {
				return err
			}
			dispatched = verificationCase
			return nil
		}
		if !validZTAPIHealthVerificationDispatchAdmission(admission) {
			return ErrZTAPIVerificationInvalid
		}
		reservation, estimateErr := ztapiHealthVerificationReservation(admission)
		if estimateErr != nil || reservation <= 0 {
			if estimateErr != nil {
				return estimateErr
			}
			return ErrZTAPIVerificationInvalid
		}

		// Route and generation validation deliberately precede the budget lock.
		budget, budgetErr := lockZTAPIProbeBudget(tx)
		if budgetErr != nil {
			return budgetErr
		}
		if budget.AllocatedNanoUSD <= 0 || budget.AccountedNanoUSD > budget.AllocatedNanoUSD || reservation > budget.AllocatedNanoUSD-budget.AccountedNanoUSD {
			if err := cancelClaimedZTAPIHealthVerificationCaseTx(tx, &verificationCase, "budget_exhausted", now); err != nil {
				return err
			}
			dispatched = verificationCase
			return nil
		}

		verificationCase.State = "dispatching"
		verificationCase.ProbeRequestID = ztapiVerificationProbeRequestID(verificationCase)
		verificationCase.DispatchAt = now.UTC().UnixMilli()
		verificationCase.LeaseUntil = now.Add(lease).UTC().UnixMilli()
		verificationCase.ReservedNanoUSD = reservation
		verificationCase.EstimateNanoUSD = reservation
		verificationCase.ExcessNanoUSD = 0
		verificationCase.Result = ""
		if err := tx.Save(&verificationCase).Error; err != nil {
			return err
		}
		if err := tx.Model(&ZTAPIProbeBudget{}).Where("id = ?", 1).UpdateColumn("accounted_nano_usd", budget.AccountedNanoUSD+reservation).Error; err != nil {
			return err
		}
		dispatched = verificationCase
		send = true
		return nil
	})
	return dispatched, send && err == nil, err
}

func routeStateFromIdentity(route ZTAPIHealthRouteIdentity) ZTAPIHealthRouteState {
	return ZTAPIHealthRouteState{
		ModelID: route.ModelID, ChannelID: route.ChannelID, EntryProtocol: canonicalZTAPIHealthEntryProtocol(route),
		Protocol: strings.TrimSpace(route.Protocol), Stream: route.Stream,
		CredentialVersion: strings.TrimSpace(route.CredentialVersion.String()), Generation: route.Generation,
	}
}

func lockZTAPIHealthRouteState(tx *gorm.DB, route ZTAPIHealthRouteIdentity) (*ZTAPIHealthRouteState, error) {
	state := routeStateFromIdentity(route)
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return nil, err
	}
	if err := applyZTAPIHealthRouteIdentity(tx.Clauses(clause.Locking{Strength: "UPDATE"}), route).Take(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func ztapiHealthChannelAllowsModel(channel Channel, sourceModel string) bool {
	for _, candidate := range channel.GetModels() {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == sourceModel {
			return true
		}
	}
	return false
}

func ztapiHealthEnabledChannelKeys(channel Channel) []string {
	keys := channel.GetKeys()
	result := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for index, key := range keys {
		if key == "" {
			continue
		}
		if channel.ChannelInfo.IsMultiKey && channel.ChannelInfo.MultiKeyStatusList != nil {
			if status, exists := channel.ChannelInfo.MultiKeyStatusList[index]; exists && status != common.ChannelStatusEnabled {
				continue
			}
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func evaluateZTAPIModelAvailabilityTx(ctx context.Context, tx *gorm.DB, route ZTAPIHealthRouteIdentity) (bool, int64, error) {
	if tx == nil || !validZTAPIHealthRouteIdentity(route) {
		return false, 0, ErrZTAPIVerificationInvalid
	}
	for _, table := range []any{&ZTAPIModelConfig{}, &ZTAPIModelPublicationSnapshot{}, &Channel{}, &Ability{}, &ZTAPIHealthRouteState{}} {
		if !tx.Migrator().HasTable(table) {
			return false, 0, nil
		}
	}
	var config ZTAPIModelConfig
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&config, route.ModelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, 0, nil
		}
		return false, 0, err
	}
	if !config.Published || config.PublicationSnapshotID <= 0 {
		return false, 0, nil
	}
	var snapshot ZTAPIModelPublicationSnapshot
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&snapshot, config.PublicationSnapshotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, 0, nil
		}
		return false, 0, err
	}
	if snapshot.ModelConfigID != config.ID || snapshot.ModelVersion != config.Version ||
		snapshot.SourceModel != config.SourceModel || snapshot.PublicName != config.PublicNameValue() ||
		snapshot.Protocol != config.Protocol || snapshot.ProviderFamily != config.ProviderFamily {
		return false, 0, nil
	}
	channelIDs := snapshot.ChannelIDs()
	groups := snapshot.Groups()
	if len(channelIDs) == 0 || validateZTAPIPublicationGroups(groups) != nil {
		return false, 0, nil
	}
	var abilities []Ability
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("model = ? AND enabled = ? AND channel_id IN ?", config.SourceModel, true, channelIDs).
		Where(commonGroupCol+" IN ?", groups).Find(&abilities).Error; err != nil {
		return false, 0, err
	}
	authorizedChannelIDs := make(map[int]struct{}, len(abilities))
	for _, ability := range abilities {
		authorizedChannelIDs[ability.ChannelId] = struct{}{}
	}
	var channels []Channel
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ? AND status = ?", channelIDs, common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return false, 0, err
	}
	var states []ZTAPIHealthRouteState
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"model_id = ? AND entry_protocol = ? AND protocol = ? AND stream = ? AND generation = ?",
		route.ModelID, canonicalZTAPIHealthEntryProtocol(route), strings.TrimSpace(route.Protocol), route.Stream, route.Generation,
	).Find(&states).Error; err != nil {
		return false, 0, err
	}
	statesByChannel := make(map[int][]ZTAPIHealthRouteState, len(channels))
	for _, state := range states {
		statesByChannel[state.ChannelID] = append(statesByChannel[state.ChannelID], state)
	}
	eligible := int64(0)
	for _, channel := range channels {
		if _, authorized := authorizedChannelIDs[channel.Id]; !authorized {
			continue
		}
		authority := ztapiRouteAuthority{
			ChannelID: channel.Id, ChannelType: channel.Type, BaseURL: channel.GetBaseURL(),
			Managed: channel.ZTAPIManaged, Family: channel.ZTAPIFamily,
		}
		if !ztapiRouteMatchesEvidence(authority, &config) {
			continue
		}
		for _, key := range ztapiHealthEnabledChannelKeys(channel) {
			eligible++
			verifiedOpen := false
			for _, state := range statesByChannel[channel.Id] {
				if state.Open && ZTAPICredentialVersionMatchesKey(state.CredentialVersion, key) {
					verifiedOpen = true
					break
				}
			}
			if !verifiedOpen {
				return false, eligible, nil
			}
		}
	}
	return eligible > 0, eligible, nil
}

func lockZTAPIHealthAggregationCatalog(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&ZTAPICatalogLock{}) {
		return nil
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ZTAPICatalogLock{ID: 1}).Error; err != nil {
		return err
	}
	var catalogLock ZTAPICatalogLock
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&catalogLock, 1).Error
}

// EvaluateZTAPIModelAvailability reports whether every currently enabled,
// publication-authorized credential route for this exact request shape has
// been independently verified open. Unknown and empty route sets fail closed.
func EvaluateZTAPIModelAvailability(ctx context.Context, tx *gorm.DB, route ZTAPIHealthRouteIdentity) (bool, error) {
	allUnavailable, _, err := evaluateZTAPIModelAvailabilityTx(ctx, tx, route)
	return allUnavailable, err
}

func openZTAPIVerifiedModelCircuitTx(ctx context.Context, tx *gorm.DB, verificationCase ZTAPIHealthVerificationCase, routeState ZTAPIHealthRouteState, now time.Time) (bool, error) {
	allUnavailable, eligible, err := evaluateZTAPIModelAvailabilityTx(ctx, tx, verificationCase.RouteIdentity())
	if err != nil || !allUnavailable {
		return false, err
	}
	state, err := lockZTAPIHealthState(tx, verificationCase.ModelID)
	if err != nil {
		return false, err
	}
	if state.Generation != verificationCase.Generation || state.Open {
		return state.Open, nil
	}
	var config ZTAPIModelConfig
	if err := tx.WithContext(ctx).First(&config, verificationCase.ModelID).Error; err != nil {
		return false, err
	}
	if !config.Published || config.PublicationSnapshotID <= 0 {
		return false, nil
	}
	openedAt := now.UTC().Unix()
	incident := ZTAPIHealthIncident{
		ModelID: verificationCase.ModelID, Generation: verificationCase.Generation, ConfigVersion: config.Version,
		PublicModel: config.PublicNameValue(), TriggerEventID: verificationCase.SourceEventID, VerificationCaseID: verificationCase.ID,
		Rule: "verified_all_routes", ConsecutiveFailures: routeState.IndependentFailures,
		ValidSamples: eligible, Failures: eligible, WindowStart: openedAt, OpenedAt: openedAt,
	}
	if err := tx.Create(&incident).Error; err != nil {
		return false, err
	}
	state.Open, state.IncidentID, state.UpdatedAt = true, incident.ID, openedAt
	if err := tx.Save(state).Error; err != nil {
		return false, err
	}
	for _, kind := range []string{"unpublish", "alert"} {
		item := ZTAPIHealthOutbox{
			DedupKey: fmt.Sprintf("incident:%d:%s", incident.ID, kind), Kind: kind,
			ModelID: verificationCase.ModelID, Generation: verificationCase.Generation,
			IncidentID: incident.ID, EventID: verificationCase.SourceEventID, VerificationCaseID: verificationCase.ID,
			Status: "pending", NextAttemptAt: openedAt, CreatedAt: openedAt,
		}
		if err := tx.Create(&item).Error; err != nil {
			return false, err
		}
	}
	return true, nil
}

func enqueueZTAPIRouteAlertTx(tx *gorm.DB, verificationCase ZTAPIHealthVerificationCase, now time.Time) error {
	item := ZTAPIHealthOutbox{
		DedupKey: "verification:" + verificationCase.ID + ":route-alert", Kind: "route_alert",
		ModelID: verificationCase.ModelID, Generation: verificationCase.Generation, VerificationCaseID: verificationCase.ID,
		EventID: verificationCase.SourceEventID, Status: "pending",
		NextAttemptAt: now.UTC().Unix(), CreatedAt: now.UTC().Unix(),
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
}

func validZTAPIHealthProbeCompletion(completion ZTAPIHealthProbeCompletion) bool {
	return strings.TrimSpace(completion.CaseID) != "" && strings.TrimSpace(completion.LeaseToken) != "" &&
		completion.Generation > 0 && strings.TrimSpace(completion.ProbeRequestID) != "" &&
		len(strings.TrimSpace(completion.ProbeRequestID)) <= 255 &&
		completion.ObservedNanoUSD >= 0 &&
		(completion.Result == "failure" || completion.Result == "healthy")
}

func lockDispatchingZTAPIHealthVerificationCase(tx *gorm.DB, completion ZTAPIHealthProbeCompletion, now time.Time) (ZTAPIHealthVerificationCase, error) {
	var verificationCase ZTAPIHealthVerificationCase
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&verificationCase, "id = ?", strings.TrimSpace(completion.CaseID)).Error; err != nil {
		return verificationCase, err
	}
	if verificationCase.Generation != completion.Generation {
		return verificationCase, ErrZTAPIVerificationGenerationConflict
	}
	if verificationCase.State != "dispatching" || verificationCase.LeaseToken != strings.TrimSpace(completion.LeaseToken) || verificationCase.LeaseUntil <= now.UTC().UnixMilli() {
		return verificationCase, ErrZTAPIVerificationLeaseConflict
	}
	if verificationCase.ProbeRequestID != strings.TrimSpace(completion.ProbeRequestID) {
		return verificationCase, ErrZTAPIVerificationDuplicateProbe
	}
	return verificationCase, nil
}

func recordZTAPIHealthProbeEvidence(tx *gorm.DB, verificationCase ZTAPIHealthVerificationCase, now time.Time) error {
	evidence := ZTAPIHealthProbeEvidence{ProbeRequestID: verificationCase.ProbeRequestID, CaseID: verificationCase.ID, RecordedAt: now.UTC().UnixMilli()}
	insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&evidence)
	if insert.Error != nil {
		return insert.Error
	}
	var canonicalEvidence ZTAPIHealthProbeEvidence
	if err := tx.First(&canonicalEvidence, "probe_request_id = ?", evidence.ProbeRequestID).Error; err != nil {
		return err
	}
	if canonicalEvidence.CaseID != verificationCase.ID {
		return ErrZTAPIVerificationDuplicateProbe
	}
	return nil
}

func settleZTAPIHealthVerificationEstimate(tx *gorm.DB, verificationCase *ZTAPIHealthVerificationCase, observedNanoUSD int64) error {
	if observedNanoUSD <= verificationCase.EstimateNanoUSD {
		return nil
	}
	delta := observedNanoUSD - verificationCase.EstimateNanoUSD
	budget, err := lockZTAPIProbeBudget(tx)
	if err != nil {
		return err
	}
	next := int64(math.MaxInt64)
	if budget.AccountedNanoUSD <= math.MaxInt64-delta {
		next = budget.AccountedNanoUSD + delta
	}
	if err := tx.Model(&ZTAPIProbeBudget{}).Where("id = ?", 1).UpdateColumn("accounted_nano_usd", next).Error; err != nil {
		return err
	}
	verificationCase.EstimateNanoUSD = observedNanoUSD
	verificationCase.ExcessNanoUSD = observedNanoUSD - verificationCase.ReservedNanoUSD
	return nil
}

func (s *ZTAPIHealthVerificationStore) CompleteProbe(ctx context.Context, completion ZTAPIHealthProbeCompletion, now time.Time) (state ZTAPIHealthRouteState, err error) {
	if !validZTAPIHealthProbeCompletion(completion) || now.IsZero() {
		return state, ErrZTAPIVerificationInvalid
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		state = ZTAPIHealthRouteState{}
		// Serialize the aggregate decision with publication changes and other
		// route completions. Otherwise concurrent route opens can miss each
		// other's uncommitted state and fail to create the model incident.
		if err := lockZTAPIHealthAggregationCatalog(tx); err != nil {
			return err
		}
		if s.afterAggregationLock != nil {
			s.afterAggregationLock()
		}
		// Real request completion locks model health before the route gate. Keep
		// the same order here so a real recovery and a probe completion cannot
		// deadlock each other on MySQL.
		var identity struct {
			ModelID int `gorm:"column:model_id"`
		}
		if err := tx.Model(&ZTAPIHealthVerificationCase{}).Select("model_id").Where("id = ?", strings.TrimSpace(completion.CaseID)).Take(&identity).Error; err != nil {
			return err
		}
		if s.beforeStateLock != nil {
			s.beforeStateLock()
		}
		if _, err := lockZTAPIHealthState(tx, identity.ModelID); err != nil {
			return err
		}
		if gateErr := lockZTAPIHealthVerificationGateForCase(tx, completion.CaseID); gateErr != nil {
			return gateErr
		}
		verificationCase, lockErr := lockDispatchingZTAPIHealthVerificationCase(tx, completion, now)
		if lockErr != nil {
			return lockErr
		}
		if err := recordZTAPIHealthProbeEvidence(tx, verificationCase, now); err != nil {
			return err
		}
		if err := settleZTAPIHealthVerificationEstimate(tx, &verificationCase, completion.ObservedNanoUSD); err != nil {
			return err
		}

		routeState, err := lockZTAPIHealthRouteState(tx, verificationCase.RouteIdentity())
		if err != nil {
			return err
		}
		if completion.Result == "failure" {
			if !routeState.Open {
				routeState.IndependentFailures++
				if routeState.IndependentFailures >= 2 {
					routeState.Open = true
					routeState.OpenedAt = now.UTC().UnixMilli()
				}
			}
		} else {
			routeState.Open = false
			routeState.IndependentFailures = 0
			routeState.OpenedAt = 0
		}
		routeState.LastProbeRequestID = verificationCase.ProbeRequestID
		routeState.LastResult = completion.Result
		routeState.UpdatedAt = now.UTC().UnixMilli()
		if err := tx.Save(routeState).Error; err != nil {
			return err
		}
		if routeState.Open && completion.Result == "failure" {
			modelOpen, err := openZTAPIVerifiedModelCircuitTx(ctx, tx, verificationCase, *routeState, now)
			if err != nil {
				return err
			}
			if !modelOpen {
				if err := enqueueZTAPIRouteAlertTx(tx, verificationCase, now); err != nil {
					return err
				}
			}
		}

		verificationCase.State = "completed"
		verificationCase.Result = completion.Result
		verificationCase.CompletedAt = now.UTC().UnixMilli()
		verificationCase.LeaseToken = ""
		verificationCase.LeaseUntil = 0
		if err := tx.Save(&verificationCase).Error; err != nil {
			return err
		}
		if err := finishZTAPIHealthVerificationGate(tx, &verificationCase, now); err != nil {
			return err
		}
		state = *routeState
		return nil
	})
	return state, err
}

func (s *ZTAPIHealthVerificationStore) CompleteUnknown(ctx context.Context, completion ZTAPIHealthProbeCompletion, reason string, now time.Time) error {
	completion.Result = "healthy"
	if !validZTAPIHealthProbeCompletion(completion) || !validZTAPIVerificationResultCode(reason) || now.IsZero() {
		return ErrZTAPIVerificationInvalid
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if gateErr := lockZTAPIHealthVerificationGateForCase(tx, completion.CaseID); gateErr != nil {
			return gateErr
		}
		verificationCase, err := lockDispatchingZTAPIHealthVerificationCase(tx, completion, now)
		if err != nil {
			return err
		}
		if err := recordZTAPIHealthProbeEvidence(tx, verificationCase, now); err != nil {
			return err
		}
		if err := settleZTAPIHealthVerificationEstimate(tx, &verificationCase, completion.ObservedNanoUSD); err != nil {
			return err
		}
		verificationCase.State = "unknown"
		verificationCase.Result = strings.TrimSpace(reason)
		verificationCase.CompletedAt = now.UTC().UnixMilli()
		verificationCase.LeaseToken = ""
		verificationCase.LeaseUntil = 0
		if err := tx.Save(&verificationCase).Error; err != nil {
			return err
		}
		return finishZTAPIHealthVerificationGate(tx, &verificationCase, now)
	})
}

func (s *ZTAPIHealthVerificationStore) ExpireDispatches(ctx context.Context, now time.Time, limit int) ([]ZTAPIHealthVerificationCase, error) {
	if now.IsZero() || limit < 1 || limit > 100 {
		return nil, ErrZTAPIVerificationInvalid
	}
	var candidates []ZTAPIHealthVerificationCase
	if err := s.DB.WithContext(ctx).Where("state = ? AND lease_until <= ?", "dispatching", now.UTC().UnixMilli()).Order("lease_until ASC, id ASC").Limit(limit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	expired := make([]ZTAPIHealthVerificationCase, 0, len(candidates))
	for _, candidate := range candidates {
		var result ZTAPIHealthVerificationCase
		err := s.transaction(ctx, func(tx *gorm.DB) error {
			gate, err := lockZTAPIHealthVerificationGate(tx, candidate.RouteIdentity())
			if err != nil {
				return err
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&result, "id = ?", candidate.ID).Error; err != nil {
				return err
			}
			if result.State != "dispatching" || result.LeaseUntil > now.UTC().UnixMilli() {
				result = ZTAPIHealthVerificationCase{}
				return nil
			}
			result.State = "unknown"
			result.Result = "dispatch_expired"
			result.CompletedAt = now.UTC().UnixMilli()
			result.LeaseToken = ""
			result.LeaseUntil = 0
			if err := tx.Save(&result).Error; err != nil {
				return err
			}
			if gate.ActiveCaseID == result.ID || gate.ActiveCaseID == "" {
				gate.LastCaseID = result.ID
				gate.ActiveCaseID = ""
				gate.CooldownUntil = now.Add(5 * time.Minute).UTC().UnixMilli()
				gate.UpdatedAt = now.UTC().UnixMilli()
				return saveZTAPIHealthVerificationGate(tx, gate)
			}
			return nil
		})
		if err != nil {
			return expired, err
		}
		if result.ID != "" {
			expired = append(expired, result)
		}
	}
	return expired, nil
}
