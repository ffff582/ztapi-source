package model

import (
	"context"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type legacyZTAPIHealthVerificationCase struct {
	ID                string `gorm:"primaryKey;size:36"`
	ModelID           int    `gorm:"not null;uniqueIndex:idx_ztapi_verify_window,priority:1"`
	ChannelID         int    `gorm:"not null;uniqueIndex:idx_ztapi_verify_window,priority:2"`
	Protocol          string `gorm:"size:32;not null;uniqueIndex:idx_ztapi_verify_window,priority:3"`
	Stream            bool   `gorm:"not null;uniqueIndex:idx_ztapi_verify_window,priority:4"`
	CredentialVersion string `gorm:"size:64;not null;uniqueIndex:idx_ztapi_verify_window,priority:5"`
	Generation        uint64 `gorm:"not null;uniqueIndex:idx_ztapi_verify_window,priority:6"`
	Window            int64  `gorm:"not null;uniqueIndex:idx_ztapi_verify_window,priority:7"`
	SourceEventID     int64  `gorm:"not null;index"`
	State             string `gorm:"size:16;not null;index:idx_ztapi_verify_due,priority:1"`
	ReadyAt           int64  `gorm:"not null;index:idx_ztapi_verify_due,priority:2"`
	LeaseToken        string `gorm:"size:36;not null"`
	LeaseUntil        int64  `gorm:"not null"`
	Attempts          int    `gorm:"not null"`
	ProbeRequestID    string `gorm:"size:255;not null"`
	Result            string `gorm:"size:16;not null"`
	CreatedAt         int64  `gorm:"not null"`
	CompletedAt       int64  `gorm:"not null"`
	CancelledAt       int64  `gorm:"not null"`
}

func (legacyZTAPIHealthVerificationCase) TableName() string {
	return "ztapi_health_verification_cases"
}

func verificationFixture(t *testing.T) (*ZTAPIHealthVerificationStore, time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification.db")+"?_pragma=busy_timeout(5000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
	require.NoError(t, MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&ZTAPIProbeBudget{}))
	_, err = InitializeZTAPIProbeAllocation(context.Background(), db, "verification-test-budget", 30_000_000_000)
	require.NoError(t, err)
	return NewZTAPIHealthVerificationStore(db), time.Unix(2_000_000_000, 0).UTC()
}

func verificationAggregateFixture(t *testing.T) (*ZTAPIHealthVerificationStore, time.Time, ZTAPIHealthRouteIdentity, ZTAPIHealthRouteIdentity) {
	t.Helper()
	store, now := verificationFixture(t)
	require.NoError(t, store.DB.AutoMigrate(&Channel{}, &Ability{}, &ZTAPIModelConfig{}, &ZTAPIModelPublicationSnapshot{}, &ZTAPICatalogLock{}))
	publicName := "zt-aggregate-model"
	sourceModel := "gpt-aggregate-model"
	require.NoError(t, store.DB.Session(&gorm.Session{SkipHooks: true}).Create(&ZTAPIModelPublicationSnapshot{
		ID: 91, ModelConfigID: 90, ModelVersion: 1, SourceModel: sourceModel, PublicName: publicName,
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
		AllowedChannelIDs: `[1,2]`, EnabledGroups: `["default"]`,
	}).Error)
	require.NoError(t, store.DB.Session(&gorm.Session{SkipHooks: true}).Create(&ZTAPIModelConfig{
		ID: 90, SourceModel: sourceModel, PublicName: &publicName, Family: ZTAPIModelFamilyOpenAI,
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
		EnabledGroups: `["default"]`, Published: true, PublicationSnapshotID: 91, Version: 1,
	}).Error)
	require.NoError(t, store.DB.Create(&ZTAPIHealthState{ModelID: 90, Generation: 1}).Error)
	for _, channel := range []Channel{
		{Id: 1, Name: "enterprise", Type: constant.ChannelTypeOpenAI, Key: "enterprise-key", Models: sourceModel, Status: common.ChannelStatusEnabled, ZTAPIManaged: true, ZTAPIFamily: ZTAPIModelFamilyOpenAI},
		{Id: 2, Name: "pool", Type: constant.ChannelTypeOpenAI, Key: "pool-key", Models: sourceModel, Status: common.ChannelStatusEnabled, ZTAPIManaged: true, ZTAPIFamily: ZTAPIModelFamilyOpenAI},
	} {
		require.NoError(t, store.DB.Session(&gorm.Session{SkipHooks: true}).Create(&channel).Error)
		require.NoError(t, store.DB.Create(&Ability{Group: "default", Model: sourceModel, ChannelId: channel.Id, Enabled: true}).Error)
	}
	enterpriseFingerprint, err := FingerprintZTAPICredential("authorization\x00Bearer enterprise-key")
	require.NoError(t, err)
	poolFingerprint, err := FingerprintZTAPICredential("authorization\x00Bearer pool-key")
	require.NoError(t, err)
	base := ZTAPIHealthRouteIdentity{ModelID: 90, EntryProtocol: "chat", Protocol: "responses", Generation: 1}
	enterprise := base
	enterprise.ChannelID, enterprise.CredentialVersion = 1, enterpriseFingerprint
	pool := base
	pool.ChannelID, pool.CredentialVersion = 2, poolFingerprint
	return store, now, enterprise, pool
}

func completeVerificationFailure(t *testing.T, store *ZTAPIHealthVerificationStore, route ZTAPIHealthRouteIdentity, eventID int64, now time.Time) ZTAPIHealthRouteState {
	t.Helper()
	verificationCase, created, err := enqueueVerificationSuspicion(t, store, context.Background(), ZTAPIHealthSuspicion{Route: route, SourceEventID: eventID}, now)
	require.NoError(t, err)
	require.True(t, created)
	claimed, err := store.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, verificationCase.ID, claimed.ID)
	dispatched := beginVerificationDispatch(t, store, claimed, now.Add(time.Second))
	state, err := store.CompleteProbe(context.Background(), ZTAPIHealthProbeCompletion{
		CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
		ProbeRequestID: dispatched.ProbeRequestID, Result: "failure",
	}, now.Add(2*time.Second))
	require.NoError(t, err)
	return state
}

func verificationIdentity() ZTAPIHealthRouteIdentity {
	fingerprint, err := FingerprintZTAPICredential("verification-test-key")
	if err != nil {
		panic(err)
	}
	return ZTAPIHealthRouteIdentity{
		ModelID:           82,
		ChannelID:         2,
		Protocol:          "responses",
		Stream:            true,
		CredentialVersion: fingerprint,
		Generation:        3,
	}
}

func verificationSuspicion(eventID int64) ZTAPIHealthSuspicion {
	return ZTAPIHealthSuspicion{Route: verificationIdentity(), SourceEventID: eventID}
}

func verificationEventForSuspicion(suspicion ZTAPIHealthSuspicion) ZTAPIHealthEvent {
	return ZTAPIHealthEvent{
		ID: suspicion.SourceEventID, ExecutionID: "verification-event-" + strconv.FormatInt(suspicion.SourceEventID, 10),
		ModelID: suspicion.Route.ModelID, Generation: suspicion.Route.Generation, Stream: suspicion.Route.Stream,
		CompletionSequence: uint64(suspicion.SourceEventID),
		Source:             "real", Result: "suspected", Counted: false, ChannelID: suspicion.Route.ChannelID,
		EntryProtocol:     canonicalZTAPIHealthEntryProtocol(suspicion.Route),
		CredentialVersion: suspicion.Route.CredentialVersion.String(), UpstreamProtocol: suspicion.Route.Protocol, Outcome: "{}",
	}
}

func legacyVerificationCases() []legacyZTAPIHealthVerificationCase {
	return []legacyZTAPIHealthVerificationCase{
		{
			ID: "legacy-queued", ModelID: 82, ChannelID: 2, Protocol: "responses", Stream: true,
			CredentialVersion: strings.Repeat("a", 64), Generation: 1, Window: 123, SourceEventID: 6901,
			State: "queued", ReadyAt: 1, LeaseToken: "queued-lease", LeaseUntil: 2, CreatedAt: 1,
		},
		{
			ID: "legacy-claimed", ModelID: 82, ChannelID: 3, Protocol: "responses", Stream: true,
			CredentialVersion: strings.Repeat("b", 64), Generation: 1, Window: 124, SourceEventID: 6902,
			State: "claimed", ReadyAt: 1, LeaseToken: "claimed-lease", LeaseUntil: 2, Attempts: 1, CreatedAt: 1,
		},
	}
}

func seedLegacyVerificationCases(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&legacyZTAPIHealthVerificationCase{}))
	for _, verificationCase := range legacyVerificationCases() {
		require.NoError(t, db.Create(&verificationCase).Error)
	}
}

func requireLegacyVerificationCasesCancelled(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	cancelledAt := make(map[string]int64, 2)
	for _, id := range []string{"legacy-queued", "legacy-claimed"} {
		var verificationCase ZTAPIHealthVerificationCase
		require.NoError(t, db.First(&verificationCase, "id = ?", id).Error)
		require.Equal(t, "cancelled", verificationCase.State)
		require.Positive(t, verificationCase.CancelledAt)
		require.Empty(t, verificationCase.LeaseToken)
		require.Zero(t, verificationCase.LeaseUntil)
		cancelledAt[id] = verificationCase.CancelledAt
	}
	return cancelledAt
}

func seedPreMigratedVerificationCases(t *testing.T, db *gorm.DB, prefix string, modelBase int) ([]string, ZTAPIHealthVerificationCase) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&ZTAPIHealthVerificationCase{}, &ZTAPIHealthVerificationGate{}))
	orphans := []ZTAPIHealthVerificationCase{
		{ID: prefix + "-queued", ModelID: modelBase, ChannelID: 2, Protocol: "responses", Stream: true, CredentialVersion: strings.Repeat("a", 64), Generation: 1, SourceEventID: int64(modelBase)*100 + 1, State: "queued", ReadyAt: 1, LeaseToken: "old-queued-lease", LeaseUntil: 2, CreatedAt: 1},
		{ID: prefix + "-claimed", ModelID: modelBase + 1, ChannelID: 3, Protocol: "chat", CredentialVersion: strings.Repeat("b", 64), Generation: 1, SourceEventID: int64(modelBase)*100 + 2, State: "claimed", ReadyAt: 1, LeaseToken: "old-claimed-lease", LeaseUntil: 2, Attempts: 1, CreatedAt: 1},
	}
	ids := make([]string, 0, len(orphans))
	for _, orphan := range orphans {
		require.NoError(t, db.Create(&orphan).Error)
		ids = append(ids, orphan.ID)
	}
	valid := ZTAPIHealthVerificationCase{ID: prefix + "-gated", ModelID: modelBase + 2, ChannelID: 4, Protocol: "responses", CredentialVersion: strings.Repeat("c", 64), Generation: 1, SourceEventID: int64(modelBase)*100 + 3, State: "queued", ReadyAt: 1, CreatedAt: 1}
	require.NoError(t, db.Create(&valid).Error)
	require.NoError(t, db.Create(&ZTAPIHealthVerificationGate{
		ModelID: valid.ModelID, ChannelID: valid.ChannelID, Protocol: valid.Protocol, Stream: valid.Stream,
		CredentialVersion: valid.CredentialVersion, Generation: valid.Generation, ActiveCaseID: valid.ID, UpdatedAt: 1,
	}).Error)
	return ids, valid
}

func requirePreMigratedVerificationCasesReconciled(t *testing.T, db *gorm.DB, orphanIDs []string, valid ZTAPIHealthVerificationCase) {
	t.Helper()
	for _, id := range orphanIDs {
		var stored ZTAPIHealthVerificationCase
		require.NoError(t, db.First(&stored, "id = ?", id).Error)
		require.Equal(t, "cancelled", stored.State)
		require.Positive(t, stored.CancelledAt)
		require.Empty(t, stored.LeaseToken)
		require.Zero(t, stored.LeaseUntil)
	}
	var preserved ZTAPIHealthVerificationCase
	require.NoError(t, db.First(&preserved, "id = ?", valid.ID).Error)
	require.Equal(t, "queued", preserved.State, "a current case referenced by its gate must survive migration")
}

func enqueueVerificationSuspicion(t *testing.T, store *ZTAPIHealthVerificationStore, ctx context.Context, suspicion ZTAPIHealthSuspicion, now time.Time) (ZTAPIHealthVerificationCase, bool, error) {
	t.Helper()
	event := verificationEventForSuspicion(suspicion)
	require.NoError(t, store.DB.Create(&event).Error)
	return store.EnqueueSuspicion(ctx, suspicion, now)
}

func verificationDispatchCheck(inputTokens, inputPrice, outputPrice int64) ZTAPIHealthVerificationDispatchCheck {
	return func(_ context.Context, _ *gorm.DB, verificationCase ZTAPIHealthVerificationCase) (ZTAPIHealthVerificationDispatchAdmission, error) {
		return ZTAPIHealthVerificationDispatchAdmission{
			Active:                  true,
			Route:                   verificationCase.RouteIdentity(),
			InputTokens:             inputTokens,
			InputNanoUSDPerMillion:  inputPrice,
			OutputNanoUSDPerMillion: outputPrice,
		}, nil
	}
}

func beginVerificationDispatch(t *testing.T, store *ZTAPIHealthVerificationStore, claimed *ZTAPIHealthVerificationCase, now time.Time) ZTAPIHealthVerificationCase {
	t.Helper()
	dispatched, send, err := store.BeginDispatch(
		context.Background(), claimed.ID, claimed.LeaseToken, claimed.Generation, now, time.Minute,
		verificationDispatchCheck(2000, 1_000_000, 2_000_000),
	)
	require.NoError(t, err)
	require.True(t, send)
	return dispatched
}

func TestZTAPIVerificationCaseDeduplicatesExactRouteFiveMinuteWindow(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	first, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1083), now)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEmpty(t, first.ID)

	second, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1084), now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, second.ID)
	require.EqualValues(t, 1083, second.SourceEventID, "deduplication must retain the first immutable observation")

	third, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1085), now.Add(5*time.Minute))
	require.NoError(t, err)
	require.False(t, created, "an active case remains the only paid verification case even after five minutes")
	require.Equal(t, first.ID, third.ID)
}

func TestZTAPIVerificationFiveMinuteCooldownUsesElapsedTimeNotWallClockBuckets(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	boundary := now.Truncate(time.Minute).Add(4*time.Minute + 59*time.Second)
	first, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1101), boundary)
	require.NoError(t, err)
	require.True(t, created)
	claimed, err := store.Claim(ctx, boundary, time.Minute)
	require.NoError(t, err)
	dispatched := beginVerificationDispatch(t, store, claimed, boundary.Add(500*time.Millisecond))
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{
		CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
		ProbeRequestID: dispatched.ProbeRequestID, Result: "healthy",
	}, boundary.Add(time.Second))
	require.NoError(t, err)

	second, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1102), boundary.Add(2*time.Second))
	require.NoError(t, err)
	require.False(t, created, "crossing a wall-clock bucket must not bypass the elapsed cooldown")
	require.Equal(t, first.ID, second.ID)

	third, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1103), boundary.Add(5*time.Minute+time.Second))
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, first.ID, third.ID)
}

func TestZTAPIVerificationCooldownStartsWhenDelayedProbeCompletes(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	first, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1151), now)
	require.NoError(t, err)
	require.True(t, created)
	claimed, err := store.Claim(ctx, now.Add(10*time.Minute), time.Minute)
	require.NoError(t, err)
	dispatched := beginVerificationDispatch(t, store, claimed, now.Add(10*time.Minute+500*time.Millisecond))
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{
		CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
		ProbeRequestID: dispatched.ProbeRequestID, Result: "healthy",
	}, now.Add(10*time.Minute+time.Second))
	require.NoError(t, err)

	second, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1152), now.Add(10*time.Minute+2*time.Second))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, second.ID)
	third, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(1153), now.Add(15*time.Minute+time.Second))
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, first.ID, third.ID)
}

func TestZTAPIVerificationGateDeduplicatesOnlyTheExactRoute(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	base := verificationSuspicion(2000)
	first, created, err := enqueueVerificationSuspicion(t, store, ctx, base, now)
	require.NoError(t, err)
	require.True(t, created)

	variants := []func(*ZTAPIHealthSuspicion){
		func(v *ZTAPIHealthSuspicion) { v.Route.ChannelID++ },
		func(v *ZTAPIHealthSuspicion) { v.Route.EntryProtocol = "chat" },
		func(v *ZTAPIHealthSuspicion) { v.Route.Stream = false },
		func(v *ZTAPIHealthSuspicion) {
			fingerprint, err := FingerprintZTAPICredential("verification-test-key-b")
			require.NoError(t, err)
			v.Route.CredentialVersion = fingerprint
		},
	}
	for i, mutate := range variants {
		candidate := base
		candidate.SourceEventID += int64(i + 1)
		mutate(&candidate)
		result, variantCreated, variantErr := enqueueVerificationSuspicion(t, store, ctx, candidate, now)
		require.NoError(t, variantErr)
		require.Truef(t, variantCreated, "exact-route gate discarded independent route variant %d", i)
		require.NotEqual(t, first.ID, result.ID)
	}

	newGeneration := base
	newGeneration.SourceEventID += 10
	newGeneration.Route.Generation++
	replacement, replacementCreated, replacementErr := enqueueVerificationSuspicion(t, store, ctx, newGeneration, now)
	require.NoError(t, replacementErr)
	require.True(t, replacementCreated)
	require.NotEqual(t, first.ID, replacement.ID)
	require.EqualValues(t, base.Route.Generation+1, replacement.Generation)
	var cancelled ZTAPIHealthVerificationCase
	require.NoError(t, store.DB.First(&cancelled, "id = ?", first.ID).Error)
	require.Equal(t, "cancelled", cancelled.State)

	for i, mutate := range []func(*ZTAPIHealthSuspicion){
		func(v *ZTAPIHealthSuspicion) { v.Route.ModelID++ },
		func(v *ZTAPIHealthSuspicion) { v.Route.Protocol = "chat" },
	} {
		candidate := base
		candidate.SourceEventID += int64(100 + i)
		mutate(&candidate)
		_, created, err = enqueueVerificationSuspicion(t, store, ctx, candidate, now)
		require.NoError(t, err)
		require.Truef(t, created, "independent model/protocol gate %d was incorrectly deduplicated", i)
	}

	var count int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthVerificationCase{}).Count(&count).Error)
	require.EqualValues(t, 8, count)
}

func TestZTAPIVerificationRouteStateIndexTagsCoverCanonicalIdentity(t *testing.T) {
	typ := reflect.TypeOf(ZTAPIHealthVerificationCase{})
	for _, name := range []string{"ModelID", "ChannelID", "EntryProtocol", "Protocol", "Stream", "CredentialVersion", "Generation"} {
		field, ok := typ.FieldByName(name)
		require.True(t, ok, name)
		require.NotContains(t, field.Tag.Get("gorm"), "idx_ztapi_verify_window")
	}

	store, _ := verificationFixture(t)
	type indexColumn struct {
		Seqno int    `gorm:"column:seqno"`
		Name  string `gorm:"column:name"`
	}
	var columns []indexColumn
	require.NoError(t, store.DB.Raw("PRAGMA index_info('idx_ztapi_health_route')").Scan(&columns).Error)
	require.Equal(t, []string{"model_id", "channel_id", "entry_protocol", "protocol", "stream", "credential_version", "generation"}, func() []string {
		names := make([]string, len(columns))
		for i := range columns {
			names[i] = columns[i].Name
		}
		return names
	}())
}

func TestZTAPIVerificationRejectsMissingOrMismatchedSourceEvent(t *testing.T) {
	mutations := map[string]func(*ZTAPIHealthEvent){
		"wrong_model":      func(event *ZTAPIHealthEvent) { event.ModelID++ },
		"wrong_channel":    func(event *ZTAPIHealthEvent) { event.ChannelID++ },
		"wrong_protocol":   func(event *ZTAPIHealthEvent) { event.UpstreamProtocol = "chat" },
		"wrong_stream":     func(event *ZTAPIHealthEvent) { event.Stream = !event.Stream },
		"wrong_generation": func(event *ZTAPIHealthEvent) { event.Generation++ },
		"wrong_credential": func(event *ZTAPIHealthEvent) {
			fingerprint, err := FingerprintZTAPICredential("verification-event-used-another-key")
			require.NoError(t, err)
			event.CredentialVersion = fingerprint.String()
		},
		"probe_source":     func(event *ZTAPIHealthEvent) { event.Source = "probe" },
		"successful_event": func(event *ZTAPIHealthEvent) { event.Result = "success" },
		"failure_counted":  func(event *ZTAPIHealthEvent) { event.Result, event.Counted = "failure", true },
		"failure_uncounted": func(event *ZTAPIHealthEvent) {
			event.Result, event.Counted = "failure", false
		},
		"suspected_counted": func(event *ZTAPIHealthEvent) { event.Counted = true },
		"stale_event":       func(event *ZTAPIHealthEvent) { event.StaleGeneration = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store, now := verificationFixture(t)
			suspicion := verificationSuspicion(2401)
			event := verificationEventForSuspicion(suspicion)
			mutate(&event)
			require.NoError(t, store.DB.Create(&event).Error)
			_, _, err := store.EnqueueSuspicion(context.Background(), suspicion, now)
			require.ErrorIs(t, err, ErrZTAPIVerificationInvalid)
			var count int64
			require.NoError(t, store.DB.Model(&ZTAPIHealthVerificationCase{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}

	store, now := verificationFixture(t)
	_, _, err := store.EnqueueSuspicion(context.Background(), verificationSuspicion(2402), now)
	require.ErrorIs(t, err, ErrZTAPIVerificationInvalid)
}

func TestZTAPIVerificationEventSchemaCarriesCredentialVersion(t *testing.T) {
	field, ok := reflect.TypeOf(ZTAPIHealthEvent{}).FieldByName("CredentialVersion")
	require.True(t, ok, "immutable health events must bind the actual credential version")
	require.Contains(t, field.Tag.Get("gorm"), "size:64")
}

func TestZTAPIVerificationRejectsRawCredentialMaterial(t *testing.T) {
	store, now := verificationFixture(t)
	suspicion := verificationSuspicion(2500)
	suspicion.Route.CredentialVersion = ZTAPICredentialFingerprint{}
	_, _, err := store.EnqueueSuspicion(context.Background(), suspicion, now)
	require.ErrorIs(t, err, ErrZTAPIVerificationInvalid)

	var count int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthVerificationCase{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestZTAPIVerificationHashesEvenHexShapedRawCredentialBeforePersistence(t *testing.T) {
	store, now := verificationFixture(t)
	raw := strings.Repeat("a", 64)
	fingerprint, err := FingerprintZTAPICredential(raw)
	require.NoError(t, err)
	suspicion := verificationSuspicion(2501)
	suspicion.Route.CredentialVersion = fingerprint
	verificationCase, created, err := enqueueVerificationSuspicion(t, store, context.Background(), suspicion, now)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, raw, verificationCase.CredentialVersion)
	require.Equal(t, fingerprint.String(), verificationCase.CredentialVersion)
}

func TestZTAPIVerificationCancelQueuedIsExactAndCancelsClaimedBeforeDispatch(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	first := verificationSuspicion(3001)
	second := verificationSuspicion(3002)
	second.Route.Protocol = "chat"
	firstCase, _, err := enqueueVerificationSuspicion(t, store, ctx, first, now)
	require.NoError(t, err)
	secondCase, _, err := enqueueVerificationSuspicion(t, store, ctx, second, now)
	require.NoError(t, err)

	cancelled, err := store.CancelQueued(ctx, first.Route, now.Add(time.Second))
	require.NoError(t, err)
	require.EqualValues(t, 1, cancelled)
	var persisted ZTAPIHealthVerificationCase
	require.NoError(t, store.DB.First(&persisted, "id = ?", firstCase.ID).Error)
	require.Equal(t, "cancelled", persisted.State)
	persisted = ZTAPIHealthVerificationCase{}
	require.NoError(t, store.DB.First(&persisted, "id = ?", secondCase.ID).Error)
	require.Equal(t, "queued", persisted.State)

	claimed, err := store.Claim(ctx, now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, secondCase.ID, claimed.ID)
	cancelled, err = store.CancelQueued(ctx, second.Route, now.Add(2*time.Second))
	require.NoError(t, err)
	require.EqualValues(t, 1, cancelled)
	persisted = ZTAPIHealthVerificationCase{}
	require.NoError(t, store.DB.First(&persisted, "id = ?", secondCase.ID).Error)
	require.Equal(t, "cancelled", persisted.State)
	require.Empty(t, persisted.LeaseToken)
}

func TestZTAPIRealSuccessCancelsClaimedProbeAndResetsClosedRouteEvidence(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	suspicion := verificationSuspicion(3501)
	verificationCase, _, err := enqueueVerificationSuspicion(t, store, ctx, suspicion, now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, verificationCase.ID, claimed.ID)

	routeState := routeStateFromIdentity(suspicion.Route)
	routeState.IndependentFailures = 1
	routeState.LastResult = "failure"
	require.NoError(t, store.DB.Create(&routeState).Error)
	request := &ZTAPIHealthRequest{
		ModelID: suspicion.Route.ModelID, Generation: suspicion.Route.Generation,
		EntryProtocol: canonicalZTAPIHealthEntryProtocol(suspicion.Route), Stream: suspicion.Route.Stream,
	}
	outcome := types.ZTAPIHealthOutcome{Attempts: []types.ZTAPIHealthAttempt{{
		Index: 1, ChannelID: suspicion.Route.ChannelID, Protocol: suspicion.Route.Protocol,
		CredentialVersion: suspicion.Route.CredentialVersion.String(), Result: "success",
	}}}
	require.NoError(t, store.DB.Transaction(func(tx *gorm.DB) error {
		return applyZTAPIHealthRealRouteActionsTx(tx, request, ZTAPIHealthEvent{}, outcome, now.Add(time.Second))
	}))

	var persistedCase ZTAPIHealthVerificationCase
	require.NoError(t, store.DB.First(&persistedCase, "id = ?", verificationCase.ID).Error)
	require.Equal(t, "cancelled", persistedCase.State)
	require.Empty(t, persistedCase.LeaseToken)
	var persistedState ZTAPIHealthRouteState
	require.NoError(t, applyZTAPIHealthRouteIdentity(store.DB, suspicion.Route).Take(&persistedState).Error)
	require.Zero(t, persistedState.IndependentFailures)
	require.Equal(t, "healthy", persistedState.LastResult)
}

func TestZTAPIPoolFailureKeepsModelPublishedWhenEnterpriseRouteIsAvailable(t *testing.T) {
	store, now, enterprise, pool := verificationAggregateFixture(t)
	completeVerificationFailure(t, store, pool, 8101, now)
	poolState := completeVerificationFailure(t, store, pool, 8102, now.Add(6*time.Minute))
	require.True(t, poolState.Open)

	allUnavailable, err := EvaluateZTAPIModelAvailability(context.Background(), store.DB, pool)
	require.NoError(t, err)
	require.False(t, allUnavailable)
	var config ZTAPIModelConfig
	require.NoError(t, store.DB.First(&config, pool.ModelID).Error)
	require.True(t, config.Published)
	var outboxCount int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthOutbox{}).Where("kind = ?", "unpublish").Count(&outboxCount).Error)
	require.Zero(t, outboxCount)
	var routeAlerts int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthOutbox{}).Where("kind = ? AND model_id = ?", "route_alert", pool.ModelID).Count(&routeAlerts).Error)
	require.EqualValues(t, 1, routeAlerts)
	var health ZTAPIHealthState
	require.NoError(t, store.DB.First(&health, pool.ModelID).Error)
	require.False(t, health.Open)

	enterpriseState := routeStateFromIdentity(enterprise)
	require.NoError(t, store.DB.Create(&enterpriseState).Error)
	allUnavailable, err = EvaluateZTAPIModelAvailability(context.Background(), store.DB, pool)
	require.NoError(t, err)
	require.False(t, allUnavailable, "a known healthy enterprise route keeps the model available")
}

func TestZTAPIAllEligibleRoutesVerifiedOpenCreatesOneModelIncident(t *testing.T) {
	store, now, enterprise, pool := verificationAggregateFixture(t)
	completeVerificationFailure(t, store, pool, 8201, now)
	completeVerificationFailure(t, store, pool, 8202, now.Add(6*time.Minute))
	completeVerificationFailure(t, store, enterprise, 8203, now.Add(12*time.Minute))
	enterpriseState := completeVerificationFailure(t, store, enterprise, 8204, now.Add(18*time.Minute))
	require.True(t, enterpriseState.Open)

	allUnavailable, err := EvaluateZTAPIModelAvailability(context.Background(), store.DB, enterprise)
	require.NoError(t, err)
	require.True(t, allUnavailable)
	var health ZTAPIHealthState
	require.NoError(t, store.DB.First(&health, enterprise.ModelID).Error)
	require.True(t, health.Open)
	var incidents int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthIncident{}).Where("model_id = ? AND rule = ?", enterprise.ModelID, "verified_all_routes").Count(&incidents).Error)
	require.EqualValues(t, 1, incidents)
	var unpublishJobs, alertJobs int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthOutbox{}).Where("model_id = ? AND kind = ?", enterprise.ModelID, "unpublish").Count(&unpublishJobs).Error)
	require.NoError(t, store.DB.Model(&ZTAPIHealthOutbox{}).Where("model_id = ? AND kind = ?", enterprise.ModelID, "alert").Count(&alertJobs).Error)
	require.EqualValues(t, 1, unpublishJobs)
	require.EqualValues(t, 1, alertJobs)
	var incident ZTAPIHealthIncident
	require.NoError(t, store.DB.Where("model_id = ? AND rule = ?", enterprise.ModelID, "verified_all_routes").First(&incident).Error)
	require.NotEmpty(t, incident.VerificationCaseID)
	// The customer request can finish on a different retry route than the
	// verification case that proved the aggregate outage. Unpublish must use
	// the persisted verification route, not the event's final-attempt fields.
	require.NoError(t, store.DB.Model(&ZTAPIHealthEvent{}).Where("id = ?", incident.TriggerEventID).Updates(map[string]any{
		"entry_protocol": "claude", "upstream_protocol": "claude", "stream": true,
	}).Error)
	healthStore := NewZTAPIHealthStore(store.DB)
	healthStore.Now = func() time.Time { return now.Add(19 * time.Minute) }
	jobs, err := healthStore.ClaimOutbox(context.Background(), "unpublish", 1, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NoError(t, healthStore.ProcessUnpublish(context.Background(), jobs[0].ID, jobs[0].LeaseToken))
	var config ZTAPIModelConfig
	require.NoError(t, store.DB.First(&config, enterprise.ModelID).Error)
	require.False(t, config.Published)
}

func TestZTAPIEmptyEligibleRouteSetNeverUnpublishes(t *testing.T) {
	store, _, _, pool := verificationAggregateFixture(t)
	require.NoError(t, store.DB.Model(&Channel{}).Where("id IN ?", []int{1, 2}).Update("status", common.ChannelStatusManuallyDisabled).Error)
	allUnavailable, err := EvaluateZTAPIModelAvailability(context.Background(), store.DB, pool)
	require.NoError(t, err)
	require.False(t, allUnavailable)
}

func TestZTAPIAggregateIncludesTrustedOfficialRoute(t *testing.T) {
	store, _, enterprise, pool := verificationAggregateFixture(t)
	require.NoError(t, store.DB.Session(&gorm.Session{SkipHooks: true}).Model(&Channel{}).Where("id = ?", enterprise.ChannelID).Updates(map[string]any{
		"ztapi_managed": false,
		"base_url":      "https://api.openai.com/v1",
	}).Error)
	poolState := routeStateFromIdentity(pool)
	enterpriseState := routeStateFromIdentity(enterprise)
	require.NoError(t, store.DB.Create(&poolState).Error)
	require.NoError(t, store.DB.Create(&enterpriseState).Error)
	require.NoError(t, applyZTAPIHealthRouteIdentity(store.DB.Model(&ZTAPIHealthRouteState{}), pool).Updates(map[string]any{"open": true, "independent_failures": 2}).Error)

	allUnavailable, err := EvaluateZTAPIModelAvailability(context.Background(), store.DB, pool)
	require.NoError(t, err)
	require.False(t, allUnavailable, "a trusted official route authorized by the publication must keep the model available")
}

func TestZTAPIAggregateExcludesDisabledAbility(t *testing.T) {
	store, _, enterprise, pool := verificationAggregateFixture(t)
	state := routeStateFromIdentity(pool)
	state.Open = true
	state.IndependentFailures = 2
	require.NoError(t, store.DB.Create(&state).Error)
	require.NoError(t, store.DB.Model(&Ability{}).Where("channel_id = ?", enterprise.ChannelID).Update("enabled", false).Error)

	allUnavailable, err := EvaluateZTAPIModelAvailability(context.Background(), store.DB, pool)
	require.NoError(t, err)
	require.True(t, allUnavailable, "a disabled ability cannot keep an otherwise unavailable model published")
}

func TestZTAPIHealthyProbeClosesPreviouslyOpenRoute(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	route := verificationIdentity()
	state := routeStateFromIdentity(route)
	state.Open = true
	state.IndependentFailures = 2
	state.OpenedAt = now.UnixMilli()
	require.NoError(t, store.DB.Create(&state).Error)

	verificationCase, created, err := enqueueVerificationSuspicion(t, store, ctx, ZTAPIHealthSuspicion{Route: route, SourceEventID: 8351}, now)
	require.NoError(t, err)
	require.True(t, created)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, verificationCase.ID, claimed.ID)
	dispatched := beginVerificationDispatch(t, store, claimed, now.Add(time.Second))
	state, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{
		CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
		ProbeRequestID: dispatched.ProbeRequestID, Result: "healthy",
	}, now.Add(2*time.Second))
	require.NoError(t, err)
	require.False(t, state.Open)
	require.Zero(t, state.IndependentFailures)
	require.Zero(t, state.OpenedAt)
	require.Equal(t, "healthy", state.LastResult)
}

func TestZTAPIUnpublishRecheckRestoresAdmissionWhenARouteRecovered(t *testing.T) {
	store, now, enterprise, pool := verificationAggregateFixture(t)
	completeVerificationFailure(t, store, pool, 8401, now)
	completeVerificationFailure(t, store, pool, 8402, now.Add(6*time.Minute))
	completeVerificationFailure(t, store, enterprise, 8403, now.Add(12*time.Minute))
	completeVerificationFailure(t, store, enterprise, 8404, now.Add(18*time.Minute))
	require.NoError(t, applyZTAPIHealthRouteIdentity(store.DB.Model(&ZTAPIHealthRouteState{}), enterprise).Updates(map[string]any{
		"open": false, "independent_failures": 0, "last_result": "healthy",
	}).Error)

	healthStore := NewZTAPIHealthStore(store.DB)
	healthStore.Now = func() time.Time { return now.Add(19 * time.Minute) }
	jobs, err := healthStore.ClaimOutbox(context.Background(), "unpublish", 1, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NoError(t, healthStore.ProcessUnpublish(context.Background(), jobs[0].ID, jobs[0].LeaseToken))

	var config ZTAPIModelConfig
	require.NoError(t, store.DB.First(&config, enterprise.ModelID).Error)
	require.True(t, config.Published)
	require.NoError(t, healthStore.CheckAvailable(context.Background(), config.PublicNameValue()))
	var state ZTAPIHealthState
	require.NoError(t, store.DB.First(&state, enterprise.ModelID).Error)
	require.False(t, state.Open)
	require.Zero(t, state.IncidentID)
	var incident ZTAPIHealthIncident
	require.NoError(t, store.DB.First(&incident, jobs[0].IncidentID).Error)
	require.Equal(t, "aggregate_recheck_found_available_route", incident.RecoveryEvidence)
}

func TestZTAPIVerifiedOpenRouteIsRejectedBeforeRealDispatchButProbeCanEnter(t *testing.T) {
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	verificationStore, _, enterprise, pool := verificationAggregateFixture(t)
	routeState := routeStateFromIdentity(pool)
	routeState.Open = true
	routeState.IndependentFailures = 2
	require.NoError(t, verificationStore.DB.Create(&routeState).Error)
	healthStore := NewZTAPIHealthStore(verificationStore.DB)

	realTicket, err := healthStore.AdmitRequestWithEntryProtocol(context.Background(), "zt-aggregate-model", "real-open-route", "real-request", 1, false, "chat")
	require.NoError(t, err)
	require.ErrorIs(t, healthStore.AdmitAttempt(context.Background(), realTicket, pool.ChannelID, pool.Protocol, pool.CredentialVersion.String()), ErrZTAPIHealthRouteOpen)
	require.NoError(t, healthStore.AdmitAttempt(context.Background(), realTicket, enterprise.ChannelID, enterprise.Protocol, enterprise.CredentialVersion.String()))

	probeTicket, err := healthStore.AdmitRequestWithEntryProtocol(context.Background(), "zt-aggregate-model", "probe-open-route", "probe-request", 1, false, "chat")
	require.NoError(t, err)
	require.NoError(t, verificationStore.DB.Model(&ZTAPIHealthRequest{}).Where("execution_id = ?", probeTicket.ExecutionID).Update("source", "probe").Error)
	probeTicket.Source = "probe"
	require.NoError(t, healthStore.AdmitAttempt(context.Background(), probeTicket, pool.ChannelID, pool.Protocol, pool.CredentialVersion.String()))
}

func TestZTAPIVerificationClaimUsesExpiringLease(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	created, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4001), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, created.ID, claimed.ID)
	require.NotEmpty(t, claimed.LeaseToken)

	none, err := store.Claim(ctx, now.Add(59*time.Second), time.Minute)
	require.NoError(t, err)
	require.Nil(t, none)
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{
		CaseID: claimed.ID, LeaseToken: claimed.LeaseToken, Generation: claimed.Generation,
		ProbeRequestID: "expired-probe", Result: "failure",
	}, now.Add(time.Minute))
	require.ErrorIs(t, err, ErrZTAPIVerificationLeaseConflict)
	reclaimed, err := store.Claim(ctx, now.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	require.NotEqual(t, claimed.LeaseToken, reclaimed.LeaseToken)
	require.Equal(t, 2, reclaimed.Attempts)
}

func TestZTAPIVerificationClaimIsAtomic(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4501), now)
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan *ZTAPIHealthVerificationCase, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			<-start
			claimed, claimErr := NewZTAPIHealthVerificationStore(store.DB.Session(&gorm.Session{NewDB: true})).Claim(ctx, now, time.Minute)
			results <- claimed
			errs <- claimErr
		}()
	}
	close(start)
	claimed := 0
	for i := 0; i < 8; i++ {
		require.NoError(t, <-errs)
		if <-results != nil {
			claimed++
		}
	}
	require.Equal(t, 1, claimed)
}

func TestZTAPIVerificationClaimDoesNotLeakRolledBackRetryResult(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	verificationCase, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4601), now)
	require.NoError(t, err)

	attempt := 0
	store.transactionFn = func(_ context.Context, fn func(*gorm.DB) error) error {
		attempt++
		tx := store.DB.Begin()
		require.NoError(t, tx.Error)
		if err := fn(tx); err != nil {
			tx.Rollback()
			return err
		}
		if attempt == 1 {
			require.NoError(t, tx.Rollback().Error)
			require.NoError(t, store.DB.Model(&ZTAPIHealthVerificationCase{}).Where("id = ?", verificationCase.ID).Updates(map[string]any{
				"state": "claimed", "lease_token": "other-worker", "lease_until": now.Add(2 * time.Minute).UnixMilli(),
			}).Error)
			return store.transactionFn(ctx, fn)
		}
		return tx.Commit().Error
	}

	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.Nil(t, claimed, "a pointer assigned by the rolled-back attempt must not escape the committed retry")
	require.Equal(t, 2, attempt)
}

func TestZTAPIVerificationDispatchReservesExactly64OutputTokens(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	verificationCase, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4701), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, verificationCase.ID, claimed.ID)

	dispatched, send, err := store.BeginDispatch(
		ctx, claimed.ID, claimed.LeaseToken, claimed.Generation, now.Add(time.Second), time.Minute,
		verificationDispatchCheck(2000, 1_000_000, 2_000_000),
	)
	require.NoError(t, err)
	require.True(t, send)
	require.Equal(t, "dispatching", dispatched.State)
	require.EqualValues(t, 64, ZTAPIVerificationMaxOutputTokens)
	expected, err := ZTAPIProbeEstimateNanoUSD(2000, ZTAPIVerificationMaxOutputTokens, 1_000_000, 2_000_000)
	require.NoError(t, err)
	require.Equal(t, expected, dispatched.ReservedNanoUSD)
	require.Equal(t, expected, dispatched.EstimateNanoUSD)
	require.Equal(t, "ztapi-health:"+dispatched.ID+":1", dispatched.ProbeRequestID)

	var budget ZTAPIProbeBudget
	require.NoError(t, store.DB.First(&budget, 1).Error)
	require.Equal(t, expected, budget.AccountedNanoUSD)
}

func TestZTAPIVerificationDispatchRequiresCurrentClaimAndExactRoute(t *testing.T) {
	mutations := map[string]func(*ZTAPIHealthVerificationDispatchAdmission){
		"model":    func(a *ZTAPIHealthVerificationDispatchAdmission) { a.Route.ModelID++ },
		"channel":  func(a *ZTAPIHealthVerificationDispatchAdmission) { a.Route.ChannelID++ },
		"protocol": func(a *ZTAPIHealthVerificationDispatchAdmission) { a.Route.Protocol = "chat" },
		"stream":   func(a *ZTAPIHealthVerificationDispatchAdmission) { a.Route.Stream = !a.Route.Stream },
		"credential": func(a *ZTAPIHealthVerificationDispatchAdmission) {
			a.Route.CredentialVersion, _ = FingerprintZTAPICredential("changed-key")
		},
		"generation": func(a *ZTAPIHealthVerificationDispatchAdmission) { a.Route.Generation++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store, now := verificationFixture(t)
			ctx := context.Background()
			_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4801), now)
			require.NoError(t, err)
			claimed, err := store.Claim(ctx, now, time.Minute)
			require.NoError(t, err)
			check := func(_ context.Context, _ *gorm.DB, verificationCase ZTAPIHealthVerificationCase) (ZTAPIHealthVerificationDispatchAdmission, error) {
				admission := ZTAPIHealthVerificationDispatchAdmission{Active: true, Route: verificationCase.RouteIdentity(), InputTokens: 2000, InputNanoUSDPerMillion: 1_000_000, OutputNanoUSDPerMillion: 2_000_000}
				mutate(&admission)
				return admission, nil
			}
			_, send, err := store.BeginDispatch(ctx, claimed.ID, claimed.LeaseToken, claimed.Generation, now.Add(time.Second), time.Minute, check)
			require.NoError(t, err)
			require.False(t, send)
			var persisted ZTAPIHealthVerificationCase
			require.NoError(t, store.DB.First(&persisted, "id = ?", claimed.ID).Error)
			require.Equal(t, "cancelled", persisted.State)
			var budget ZTAPIProbeBudget
			require.NoError(t, store.DB.First(&budget, 1).Error)
			require.Zero(t, budget.AccountedNanoUSD)
		})
	}
}

func TestZTAPIVerificationDispatchBudgetExhaustionCancelsWithoutSend(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	require.NoError(t, store.DB.Model(&ZTAPIProbeBudget{}).Where("id = ?", 1).Update("allocated_nano_usd", 1).Error)
	_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4901), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)

	persisted, send, err := store.BeginDispatch(ctx, claimed.ID, claimed.LeaseToken, claimed.Generation, now.Add(time.Second), time.Minute, verificationDispatchCheck(2000, 1_000_000, 2_000_000))
	require.NoError(t, err)
	require.False(t, send)
	require.Equal(t, "cancelled", persisted.State)
	require.Equal(t, "budget_exhausted", persisted.Result)
	require.Zero(t, persisted.ReservedNanoUSD)
	var budget ZTAPIProbeBudget
	require.NoError(t, store.DB.First(&budget, 1).Error)
	require.Zero(t, budget.AccountedNanoUSD)
}

func TestZTAPIVerificationDeferAndCancelClaimAreLeaseSafe(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4951), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.ErrorIs(t, store.DeferClaim(ctx, claimed.ID, "wrong", now.Add(time.Second), time.Minute), ErrZTAPIVerificationLeaseConflict)
	require.NoError(t, store.DeferClaim(ctx, claimed.ID, claimed.LeaseToken, now.Add(time.Second), time.Minute))
	none, err := store.Claim(ctx, now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	require.Nil(t, none)
	reclaimed, err := store.Claim(ctx, now.Add(time.Minute+time.Second), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	require.ErrorIs(t, store.CancelClaim(ctx, reclaimed.ID, "wrong", reclaimed.Generation, "route_changed", now.Add(time.Minute+2*time.Second)), ErrZTAPIVerificationLeaseConflict)
	require.NoError(t, store.CancelClaim(ctx, reclaimed.ID, reclaimed.LeaseToken, reclaimed.Generation, "route_changed", now.Add(time.Minute+2*time.Second)))
	var persisted ZTAPIHealthVerificationCase
	require.NoError(t, store.DB.First(&persisted, "id = ?", reclaimed.ID).Error)
	require.Equal(t, "cancelled", persisted.State)
}

func TestZTAPIVerificationExpiredDispatchBecomesUnknownAndNeverReclaims(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4971), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	dispatched := beginVerificationDispatch(t, store, claimed, now.Add(time.Second))
	reserved := dispatched.ReservedNanoUSD

	none, err := store.Claim(ctx, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Nil(t, none, "dispatching work may already have been sent and must never be reclaimed")
	expired, err := store.ExpireDispatches(ctx, now.Add(2*time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	require.Equal(t, "unknown", expired[0].State)
	none, err = store.Claim(ctx, now.Add(3*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Nil(t, none)
	var budget ZTAPIProbeBudget
	require.NoError(t, store.DB.First(&budget, 1).Error)
	require.Equal(t, reserved, budget.AccountedNanoUSD)
	var routeCount int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthRouteState{}).Count(&routeCount).Error)
	require.Zero(t, routeCount)
}

func TestZTAPIVerificationCompleteProbeRequiresPersistedDispatchIdentity(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4991), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: claimed.ID, LeaseToken: claimed.LeaseToken, Generation: claimed.Generation, ProbeRequestID: "not-dispatched", Result: "healthy"}, now.Add(time.Second))
	require.ErrorIs(t, err, ErrZTAPIVerificationLeaseConflict)

	dispatched := beginVerificationDispatch(t, store, claimed, now.Add(2*time.Second))
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation, ProbeRequestID: "forged", Result: "healthy"}, now.Add(3*time.Second))
	require.ErrorIs(t, err, ErrZTAPIVerificationDuplicateProbe)
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation, ProbeRequestID: dispatched.ProbeRequestID, Result: "healthy"}, now.Add(3*time.Second))
	require.NoError(t, err)
}

func TestZTAPIVerificationDispatchingCaseRemainsActiveForDeduplication(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	first, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4995), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	dispatched := beginVerificationDispatch(t, store, claimed, now.Add(time.Second))
	require.Equal(t, first.ID, dispatched.ID)
	second, created, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4996), now.Add(2*time.Second))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, dispatched.ID, second.ID)

	newGeneration := verificationSuspicion(4997)
	newGeneration.Route.Generation++
	third, created, err := enqueueVerificationSuspicion(t, store, ctx, newGeneration, now.Add(3*time.Second))
	require.NoError(t, err)
	require.True(t, created, "a current-generation route must not be blocked by an already-sent stale probe")
	require.NotEqual(t, dispatched.ID, third.ID)
	var oldDispatch ZTAPIHealthVerificationCase
	require.NoError(t, store.DB.First(&oldDispatch, "id = ?", dispatched.ID).Error)
	require.Equal(t, "dispatching", oldDispatch.State, "an already-sent probe must never be made claimable again")
}

func TestZTAPIVerificationRouteRevalidationPrecedesBudgetLock(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4998), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.DB.Delete(&ZTAPIProbeBudget{}, 1).Error)

	check := verificationDispatchCheck(2000, 1_000_000, 2_000_000)
	_, send, err := store.BeginDispatch(ctx, claimed.ID, claimed.LeaseToken, claimed.Generation, now.Add(time.Second), time.Minute, func(ctx context.Context, tx *gorm.DB, verificationCase ZTAPIHealthVerificationCase) (ZTAPIHealthVerificationDispatchAdmission, error) {
		admission, err := check(ctx, tx, verificationCase)
		admission.Route.Generation++
		return admission, err
	})
	require.NoError(t, err, "a rejected route must not require or lock the budget row")
	require.False(t, send)
	var persisted ZTAPIHealthVerificationCase
	require.NoError(t, store.DB.First(&persisted, "id = ?", claimed.ID).Error)
	require.Equal(t, "cancelled", persisted.State)
}

func TestZTAPIVerificationDispatchIsAtomicAndSingleSpendPerCase(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(4999), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			workerStore := NewZTAPIHealthVerificationStore(store.DB.Session(&gorm.Session{NewDB: true}))
			_, send, dispatchErr := workerStore.BeginDispatch(ctx, claimed.ID, claimed.LeaseToken, claimed.Generation, now.Add(time.Second), time.Minute, verificationDispatchCheck(2000, 1_000_000, 2_000_000))
			results <- send
			errs <- dispatchErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	sends := 0
	for send := range results {
		if send {
			sends++
		}
	}
	for dispatchErr := range errs {
		if dispatchErr != nil {
			require.ErrorIs(t, dispatchErr, ErrZTAPIVerificationLeaseConflict)
		}
	}
	require.Equal(t, 1, sends)
	expected, err := ZTAPIProbeEstimateNanoUSD(2000, ZTAPIVerificationMaxOutputTokens, 1_000_000, 2_000_000)
	require.NoError(t, err)
	var budget ZTAPIProbeBudget
	require.NoError(t, store.DB.First(&budget, 1).Error)
	require.Equal(t, expected, budget.AccountedNanoUSD)
}

func TestZTAPIVerificationCompleteUnknownKeepsRouteStateUntouched(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	_, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(5000), now)
	require.NoError(t, err)
	claimed, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	dispatched := beginVerificationDispatch(t, store, claimed, now.Add(time.Second))
	observed := dispatched.EstimateNanoUSD + 100
	err = store.CompleteUnknown(ctx, ZTAPIHealthProbeCompletion{
		CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
		ProbeRequestID: dispatched.ProbeRequestID, ObservedNanoUSD: observed,
	}, "missing_event", now.Add(2*time.Second))
	require.NoError(t, err)
	var persisted ZTAPIHealthVerificationCase
	require.NoError(t, store.DB.First(&persisted, "id = ?", dispatched.ID).Error)
	require.Equal(t, "unknown", persisted.State)
	require.Equal(t, observed, persisted.EstimateNanoUSD)
	require.Equal(t, observed-dispatched.ReservedNanoUSD, persisted.ExcessNanoUSD)
	var routeCount int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthRouteState{}).Count(&routeCount).Error)
	require.Zero(t, routeCount)
	var budget ZTAPIProbeBudget
	require.NoError(t, store.DB.First(&budget, 1).Error)
	require.Equal(t, observed, budget.AccountedNanoUSD)
}

func TestZTAPIVerificationCompleteProbeRequiresBoundDistinctEvidence(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	firstCase, _, err := enqueueVerificationSuspicion(t, store, ctx, verificationSuspicion(5001), now)
	require.NoError(t, err)
	first, err := store.Claim(ctx, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, firstCase.ID, first.ID)
	firstDispatched := beginVerificationDispatch(t, store, first, now.Add(500*time.Millisecond))

	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: "missing", LeaseToken: firstDispatched.LeaseToken, Generation: firstDispatched.Generation, ProbeRequestID: firstDispatched.ProbeRequestID, Result: "failure"}, now.Add(time.Second))
	require.Error(t, err)
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: firstDispatched.ID, LeaseToken: "forged", Generation: firstDispatched.Generation, ProbeRequestID: firstDispatched.ProbeRequestID, Result: "failure"}, now.Add(time.Second))
	require.ErrorIs(t, err, ErrZTAPIVerificationLeaseConflict)
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: firstDispatched.ID, LeaseToken: firstDispatched.LeaseToken, Generation: firstDispatched.Generation + 1, ProbeRequestID: firstDispatched.ProbeRequestID, Result: "failure"}, now.Add(time.Second))
	require.ErrorIs(t, err, ErrZTAPIVerificationGenerationConflict)

	state, err := store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: firstDispatched.ID, LeaseToken: firstDispatched.LeaseToken, Generation: firstDispatched.Generation, ProbeRequestID: firstDispatched.ProbeRequestID, Result: "failure"}, now.Add(time.Second))
	require.NoError(t, err)
	require.False(t, state.Open, "one diagnostic failure cannot open a route")
	require.EqualValues(t, 1, state.IndependentFailures)

	secondSuspicion := verificationSuspicion(5002)
	secondCase, _, err := enqueueVerificationSuspicion(t, store, ctx, secondSuspicion, now.Add(5*time.Minute+time.Second))
	require.NoError(t, err)
	second, err := store.Claim(ctx, now.Add(5*time.Minute+time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, secondCase.ID, second.ID)
	secondDispatched := beginVerificationDispatch(t, store, second, now.Add(5*time.Minute+1500*time.Millisecond))
	require.NoError(t, store.DB.Create(&ZTAPIHealthProbeEvidence{ProbeRequestID: secondDispatched.ProbeRequestID, CaseID: "another-case", RecordedAt: now.UnixMilli()}).Error)
	_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: secondDispatched.ID, LeaseToken: secondDispatched.LeaseToken, Generation: secondDispatched.Generation, ProbeRequestID: secondDispatched.ProbeRequestID, Result: "failure"}, now.Add(5*time.Minute+2*time.Second))
	require.ErrorIs(t, err, ErrZTAPIVerificationDuplicateProbe)
	require.NoError(t, store.DB.Delete(&ZTAPIHealthProbeEvidence{}, "probe_request_id = ?", secondDispatched.ProbeRequestID).Error)

	state, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: secondDispatched.ID, LeaseToken: secondDispatched.LeaseToken, Generation: secondDispatched.Generation, ProbeRequestID: secondDispatched.ProbeRequestID, Result: "failure"}, now.Add(5*time.Minute+2*time.Second))
	require.NoError(t, err)
	require.True(t, state.Open)
	require.EqualValues(t, 2, state.IndependentFailures)
	require.Equal(t, secondDispatched.ProbeRequestID, state.LastProbeRequestID)

	var routeCount int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthRouteState{}).Count(&routeCount).Error)
	require.EqualValues(t, 1, routeCount)
	var evidenceCount int64
	require.NoError(t, store.DB.Model(&ZTAPIHealthProbeEvidence{}).Count(&evidenceCount).Error)
	require.EqualValues(t, 2, evidenceCount)
}

func TestZTAPIVerificationHealthyProbeResetsOnlyClosedRouteEvidence(t *testing.T) {
	store, now := verificationFixture(t)
	ctx := context.Background()
	for i, result := range []string{"failure", "healthy", "failure"} {
		suspicion := verificationSuspicion(int64(6000 + i))
		_, _, err := enqueueVerificationSuspicion(t, store, ctx, suspicion, now.Add(time.Duration(i)*6*time.Minute))
		require.NoError(t, err)
		claimed, err := store.Claim(ctx, now.Add(time.Duration(i)*6*time.Minute), time.Minute)
		require.NoError(t, err)
		dispatched := beginVerificationDispatch(t, store, claimed, now.Add(time.Duration(i)*6*time.Minute+500*time.Millisecond))
		state, err := store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{
			CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
			ProbeRequestID: dispatched.ProbeRequestID, Result: result,
		}, now.Add(time.Duration(i)*6*time.Minute+time.Second))
		require.NoError(t, err)
		if result == "healthy" {
			require.Zero(t, state.IndependentFailures)
		}
		require.False(t, state.Open)
	}
}

func TestZTAPIVerificationSchemaStoresNoCustomerOrCredentialPayloads(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(ZTAPIHealthVerificationCase{}), reflect.TypeOf(ZTAPIHealthVerificationGate{}), reflect.TypeOf(ZTAPIHealthRouteState{}), reflect.TypeOf(ZTAPIHealthProbeEvidence{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, forbidden := range []string{"prompt", "responsebody", "responsetext", "toolargs", "credentialkey", "credentialsecret"} {
				require.NotContains(t, name, forbidden)
			}
		}
	}
}

func TestZTAPIVerificationMigratesLegacyWindowSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-upgrade.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	seedLegacyVerificationCases(t, db)
	require.True(t, db.Migrator().HasColumn(&legacyZTAPIHealthVerificationCase{}, "window"))
	require.True(t, db.Migrator().HasIndex(&legacyZTAPIHealthVerificationCase{}, "idx_ztapi_verify_window"))

	require.NoError(t, MigrateZTAPIHealth(db))
	require.False(t, db.Migrator().HasColumn(&ZTAPIHealthVerificationCase{}, "window"))
	require.False(t, db.Migrator().HasIndex(&ZTAPIHealthVerificationCase{}, "idx_ztapi_verify_window"))
	for _, column := range []string{"dispatch_at", "reserved_nano_usd", "estimate_nano_usd", "excess_nano_usd"} {
		require.True(t, db.Migrator().HasColumn(&ZTAPIHealthVerificationCase{}, column), column)
	}
	firstCancelledAt := requireLegacyVerificationCasesCancelled(t, db)
	require.NoError(t, MigrateZTAPIHealth(db))
	require.Equal(t, firstCancelledAt, requireLegacyVerificationCasesCancelled(t, db), "repeated migration must not rewrite audit timestamps")
	store := NewZTAPIHealthVerificationStore(db)
	now := time.Unix(2_000_000_000, 0).UTC()
	claimed, err := store.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	require.Nil(t, claimed, "legacy queued and claimed cases must never be paid work")

	verificationCase, created, err := enqueueVerificationSuspicion(t, store, context.Background(), verificationSuspicion(7001), now)
	require.NoError(t, err)
	require.True(t, created)
	duplicate, created, err := enqueueVerificationSuspicion(t, store, context.Background(), verificationSuspicion(7002), now.Add(time.Second))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, verificationCase.ID, duplicate.ID)
	var activeCount int64
	require.NoError(t, db.Model(&ZTAPIHealthVerificationCase{}).Where("state IN ?", []string{"queued", "claimed"}).Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount, "legacy rows must not coexist with a duplicate active gate case")

	claimed, err = store.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, verificationCase.ID, claimed.ID)
	require.NoError(t, MigrateZTAPIHealth(db))
	var stillClaimed ZTAPIHealthVerificationCase
	require.NoError(t, db.First(&stillClaimed, "id = ?", claimed.ID).Error)
	require.Equal(t, "claimed", stillClaimed.State, "idempotent migration must not cancel cases created under the new gate")
	require.Equal(t, claimed.LeaseToken, stillClaimed.LeaseToken)
}

func TestZTAPIVerificationMigrationCancelsPreMigratedOrphansOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-pre-migrated.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	orphanIDs, valid := seedPreMigratedVerificationCases(t, db, "pre-migrated", 82)
	require.False(t, db.Migrator().HasColumn(&ZTAPIHealthVerificationCase{}, "window"), "fixture must represent the already-migrated schema")

	require.NoError(t, MigrateZTAPIHealth(db))
	requirePreMigratedVerificationCasesReconciled(t, db, orphanIDs, valid)
	claimed, err := NewZTAPIHealthVerificationStore(db).Claim(context.Background(), time.Unix(2_000_000_000, 0).UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, valid.ID, claimed.ID)
}

func TestZTAPIVerificationNormalAndFastMigrations(t *testing.T) {
	for name, migrate := range map[string]func() error{"normal": migrateDB, "fast": migrateDBFast} {
		for _, schema := range []string{"legacy-window", "pre-migrated"} {
			t.Run(name+"/"+schema, func(t *testing.T) {
				oldDB, oldSQLite, oldMySQL, oldPG := DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
				db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-migration.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				sqlDB.SetMaxOpenConns(1)
				DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = db, true, false, false
				initCol()
				t.Cleanup(func() {
					_ = sqlDB.Close()
					DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = oldDB, oldSQLite, oldMySQL, oldPG
					initCol()
				})

				var orphanIDs []string
				var valid ZTAPIHealthVerificationCase
				if schema == "legacy-window" {
					seedLegacyVerificationCases(t, db)
				} else {
					orphanIDs, valid = seedPreMigratedVerificationCases(t, db, name+"-pre-migrated", 8200)
				}
				require.NoError(t, migrate())
				for _, column := range []string{"dispatch_at", "reserved_nano_usd", "estimate_nano_usd", "excess_nano_usd"} {
					require.True(t, db.Migrator().HasColumn(&ZTAPIHealthVerificationCase{}, column), "%s migration omitted %s", name, column)
				}
				for _, table := range []any{&ZTAPIHealthVerificationCase{}, &ZTAPIHealthVerificationGate{}, &ZTAPIHealthRouteState{}, &ZTAPIHealthProbeEvidence{}} {
					require.True(t, db.Migrator().HasTable(table), "%s migration omitted %T", name, table)
				}
				require.True(t, db.Migrator().HasIndex(&ZTAPIHealthRouteState{}, "idx_ztapi_health_route"))
				if schema == "legacy-window" {
					requireLegacyVerificationCasesCancelled(t, db)
					claimed, err := NewZTAPIHealthVerificationStore(db).Claim(context.Background(), time.Unix(2_000_000_000, 0).UTC(), time.Minute)
					require.NoError(t, err)
					require.Nil(t, claimed, "%s migration left legacy paid work claimable", name)
				} else {
					requirePreMigratedVerificationCasesReconciled(t, db, orphanIDs, valid)
				}
			})
		}
	}
}
