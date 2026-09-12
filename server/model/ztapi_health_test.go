package model

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func healthFixture(t *testing.T) (*ZTAPIHealthStore, ZTAPIModelConfig, *int64) {
	t.Helper()
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "health.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	require.NoError(t, MigrateZTAPIHealth(db))
	alias := "zt-health-test"
	c := ZTAPIModelConfig{SourceModel: "upstream-health", PublicName: &alias, Published: true, Version: 7, EnabledGroups: `["default"]`, InputPricePerMillion: 4, OutputPricePerMillion: 8}
	require.NoError(t, db.Create(&c).Error)
	now := int64(2_000_000_000)
	s := NewZTAPIHealthStore(db)
	s.Now = func() time.Time { return time.Unix(now, 0) }
	return s, c, &now
}

func healthAdmit(t *testing.T, s *ZTAPIHealthStore, c ZTAPIModelConfig, id string) *types.ZTAPIHealthTicket {
	t.Helper()
	ticket, err := s.AdmitRequest(context.Background(), c.PublicNameValue(), id, "client-controlled-id", 1, false)
	require.NoError(t, err)
	require.NotNil(t, ticket)
	return ticket
}

func healthRecord(t *testing.T, s *ZTAPIHealthStore, ticket *types.ZTAPIHealthTicket, result string) {
	t.Helper()
	require.NoError(t, s.RecordOutcome(context.Background(), ticket, types.ZTAPIHealthOutcome{Result: result, Reason: "synthetic", Dispatched: true, TransportComplete: result == "success", HasText: result == "success"}))
}

func TestZTAPIHealthMediaObservationPersistsStructuredOperationEvidence(t *testing.T) {
	s, c, _ := healthFixture(t)
	original := ztapiQuotation
	ztapiQuotation.Entries = append(append([]ZTAPIQuotationEntry(nil), original.Entries...), ZTAPIQuotationEntry{
		Label: "health-video", SourceModel: c.SourceModel, PublicName: c.PublicNameValue(),
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderSeedance,
		Modality: ZTAPIModalityVideo, Status: "mapped", QuoteRows: []ZTAPIQuotationRow{{Resource: "企业资源"}},
	})
	t.Cleanup(func() { ztapiQuotation = original })

	ticket, err := s.AdmitMediaRequest(context.Background(), c.PublicNameValue(), "media-video-fetch", "client-controlled-id", 1, types.ZTAPIHealthOperationVideoFetch, false)
	require.NoError(t, err)
	require.NotNil(t, ticket)
	require.Equal(t, ZTAPIModalityVideo, ticket.Modality)
	require.NoError(t, s.RecordOutcome(context.Background(), ticket, types.ZTAPIHealthOutcome{
		Result: "success", Reason: "valid_output", Operation: types.ZTAPIHealthOperationVideoFetch,
		ChannelID: 9, UpstreamProtocol: "video-tasks", HTTPStatus: 200,
		UpstreamRequestID: "req-safe-1", UpstreamTaskID: "task-safe-1",
		LatencyMilliseconds: 731, ResultValid: true, HasMedia: true,
		Dispatched: true, TransportComplete: true,
	}))

	var event ZTAPIHealthEvent
	require.NoError(t, s.DB.Where("execution_id = ?", ticket.ExecutionID).First(&event).Error)
	require.Equal(t, ZTAPIModalityVideo, event.Modality)
	require.Equal(t, types.ZTAPIHealthOperationVideoFetch, event.Operation)
	require.Equal(t, "task-safe-1", event.UpstreamTaskID)
	require.EqualValues(t, 731, event.LatencyMilliseconds)
	require.True(t, event.ResultValid)
}

func TestZTAPIHealthMediaSuccessWithoutUsableResultCountsAsFailure(t *testing.T) {
	s, c, _ := healthFixture(t)
	original := ztapiQuotation
	ztapiQuotation.Entries = append(append([]ZTAPIQuotationEntry(nil), original.Entries...), ZTAPIQuotationEntry{
		Label: "health-image", SourceModel: c.SourceModel, PublicName: c.PublicNameValue(),
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
		Modality: ZTAPIModalityImage, Status: "mapped", QuoteRows: []ZTAPIQuotationRow{{Resource: "企业资源"}},
	})
	t.Cleanup(func() { ztapiQuotation = original })

	for _, executionID := range []string{"media-image-invalid-1", "media-image-invalid-2"} {
		ticket, err := s.AdmitMediaRequest(context.Background(), c.PublicNameValue(), executionID, "client-controlled-id", 1, types.ZTAPIHealthOperationImageGenerate, false)
		require.NoError(t, err)
		require.NotNil(t, ticket)
		require.NoError(t, s.RecordOutcome(context.Background(), ticket, types.ZTAPIHealthOutcome{
			Result: "success", Reason: "valid_output", Operation: types.ZTAPIHealthOperationImageGenerate,
			ChannelID: 8, UpstreamProtocol: "images", HTTPStatus: 200,
			LatencyMilliseconds: 500, ResultValid: false,
			Dispatched: true, TransportComplete: true,
		}))
	}

	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.True(t, state.Open)
	var events []ZTAPIHealthEvent
	require.NoError(t, s.DB.Order("id ASC").Find(&events).Error)
	require.Len(t, events, 2)
	require.Equal(t, "failure", events[0].Result)
	require.Equal(t, "invalid_media_result", events[0].Reason)
}

func TestZTAPIHealthRejectsMediaOperationThatDoesNotMatchPublishedModality(t *testing.T) {
	s, c, _ := healthFixture(t)
	original := ztapiQuotation
	ztapiQuotation.Entries = append(append([]ZTAPIQuotationEntry(nil), original.Entries...), ZTAPIQuotationEntry{
		Label: "health-image-mismatch", SourceModel: c.SourceModel, PublicName: c.PublicNameValue(),
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
		Modality: ZTAPIModalityImage, Status: "mapped", QuoteRows: []ZTAPIQuotationRow{{Resource: "企业资源"}},
	})
	t.Cleanup(func() { ztapiQuotation = original })
	ticket, err := s.AdmitMediaRequest(context.Background(), c.PublicNameValue(), "media-operation-mismatch", "client-controlled-id", 1, types.ZTAPIHealthOperationImageGenerate, false)
	require.NoError(t, err)
	require.NotNil(t, ticket)
	err = s.RecordOutcome(context.Background(), ticket, types.ZTAPIHealthOutcome{
		Result: "failure", Reason: "provider_task_failed", Operation: types.ZTAPIHealthOperationVideoFetch,
		Dispatched: true,
	})
	require.ErrorIs(t, err, ErrZTAPIHealthInvalidTicket)
}

func TestZTAPIHealthVideoFetchObservationContinuesAfterCircuitOpenWithoutRecovery(t *testing.T) {
	s, c, _ := healthFixture(t)
	original := ztapiQuotation
	ztapiQuotation.Entries = append(append([]ZTAPIQuotationEntry(nil), original.Entries...), ZTAPIQuotationEntry{
		Label: "health-video-open", SourceModel: c.SourceModel, PublicName: c.PublicNameValue(),
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderSeedance,
		Modality: ZTAPIModalityVideo, Status: "mapped", QuoteRows: []ZTAPIQuotationRow{{Resource: "企业资源"}},
	})
	t.Cleanup(func() { ztapiQuotation = original })
	require.NoError(t, s.DB.Create(&ZTAPIHealthState{ModelID: c.ID, Generation: 1, Open: true, IncidentID: 9}).Error)
	require.NoError(t, s.DB.Model(&c).Update("published", false).Error)

	_, err := s.AdmitMediaRequest(context.Background(), c.PublicNameValue(), "blocked-submit", "req-submit", 1, types.ZTAPIHealthOperationVideoSubmit, false)
	require.ErrorIs(t, err, ErrZTAPIHealthCircuitOpen)
	ticket, err := s.AdmitMediaRequest(context.Background(), c.PublicNameValue(), "allowed-fetch", "req-fetch", 1, types.ZTAPIHealthOperationVideoFetch, true)
	require.NoError(t, err)
	require.Equal(t, types.ZTAPIHealthOperationVideoFetch, ticket.Operation)
	require.NoError(t, s.AdmitAttempt(context.Background(), ticket, 7, "video-tasks"))
	require.NoError(t, s.RecordOutcome(context.Background(), ticket, types.ZTAPIHealthOutcome{
		Result: "success", Reason: "valid_output", Operation: types.ZTAPIHealthOperationVideoFetch,
		UpstreamProtocol: "video-tasks", UpstreamTaskID: "task-after-open",
		LatencyMilliseconds: 500, ResultValid: true, Dispatched: true, TransportComplete: true,
	}))
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.True(t, state.Open)
	require.EqualValues(t, 9, state.IncidentID)
}

func TestZTAPIHealthConsecutiveAndExcluded(t *testing.T) {
	for _, results := range [][]string{{"failure", "success", "failure"}, {"failure", "excluded", "unknown", "failure"}} {
		t.Run(fmt.Sprint(results), func(t *testing.T) {
			s, c, _ := healthFixture(t)
			for i, result := range results {
				healthRecord(t, s, healthAdmit(t, s, c, fmt.Sprint(i)), result)
			}
			state, err := s.GetState(context.Background(), c.ID)
			require.NoError(t, err)
			require.Equal(t, results[1] != "success", state.Open)
			window, err := s.Window(context.Background(), c.ID)
			require.NoError(t, err)
			if state.Open {
				require.EqualValues(t, 2, window.ValidSamples)
				require.EqualValues(t, 2, state.ConsecutiveFailures)
			}
		})
	}
}

func TestZTAPIHealthStrictRollingThreshold(t *testing.T) {
	for _, n := range []int{149, 150} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, c, _ := healthFixture(t)
			for i := 0; i < n; i++ {
				result := "success"
				if i == n-5 || i == n-3 || i == n-1 {
					result = "failure"
				}
				healthRecord(t, s, healthAdmit(t, s, c, fmt.Sprint(i)), result)
			}
			state, err := s.GetState(context.Background(), c.ID)
			require.NoError(t, err)
			require.Equal(t, n == 149, state.Open)
			window, err := s.Window(context.Background(), c.ID)
			require.NoError(t, err)
			require.EqualValues(t, 3, window.Failures)
			require.EqualValues(t, n, window.ValidSamples)
		})
	}
}

func TestZTAPIHealthWindowBoundaries(t *testing.T) {
	s, c, now := healthFixture(t)
	healthRecord(t, s, healthAdmit(t, s, c, "old"), "failure")
	*now += 86400
	healthRecord(t, s, healthAdmit(t, s, c, "now"), "success")
	w, err := s.Window(context.Background(), c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, w.ValidSamples)
	require.Zero(t, w.Failures)
	*now -= 1
	w, err = s.Window(context.Background(), c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, w.ValidSamples)
	require.EqualValues(t, 1, w.Failures)
}

func TestZTAPIHealthDedupAndForgedClientID(t *testing.T) {
	s, c, _ := healthFixture(t)
	one := healthAdmit(t, s, c, "server-1")
	two := healthAdmit(t, s, c, "server-2")
	healthRecord(t, s, one, "failure")
	healthRecord(t, s, one, "failure")
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.False(t, state.Open)
	healthRecord(t, s, two, "failure")
	state, err = s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.True(t, state.Open)
	require.EqualValues(t, 2, state.CompletionSequence)
	var count int64
	require.NoError(t, s.DB.Model(&ZTAPIHealthIncident{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, s.DB.Model(&ZTAPIHealthOutbox{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestZTAPIHealthCompletionOrderAndConcurrency(t *testing.T) {
	s, c, _ := healthFixture(t)
	tickets := make([]*types.ZTAPIHealthTicket, 32)
	for i := range tickets {
		tickets[i] = healthAdmit(t, s, c, fmt.Sprint(i))
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for _, ticket := range tickets {
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func(ticket *types.ZTAPIHealthTicket) {
				defer wg.Done()
				errs <- s.RecordOutcome(context.Background(), ticket, types.ZTAPIHealthOutcome{Result: "success", Dispatched: true, TransportComplete: true})
			}(ticket)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 32, state.CompletionSequence)
	events, err := s.ListEvents(context.Background(), c.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 32)
	for i, event := range events {
		require.EqualValues(t, i+1, event.CompletionSequence)
	}
}

func TestZTAPIHealthAttributionAndProbeIdentity(t *testing.T) {
	s, c, _ := healthFixture(t)
	t.Setenv("ZTAPI_HEALTH_PROBE_USER_ID", "42")
	probe, err := s.AdmitRequest(context.Background(), c.PublicNameValue(), "probe", "same", 42, true)
	require.NoError(t, err)
	require.Equal(t, "probe", probe.Source)
	real := healthAdmit(t, s, c, "real")
	require.Equal(t, "real", real.Source)
	require.NoError(t, s.RecordOutcome(context.Background(), real, types.ZTAPIHealthOutcome{Result: "failure", ClientCancelled: true, Dispatched: true}))
	undispatched := healthAdmit(t, s, c, "not-dispatched")
	require.NoError(t, s.RecordOutcome(context.Background(), undispatched, types.ZTAPIHealthOutcome{Result: "failure"}))
	healthRecord(t, s, probe, "failure")
	healthRecord(t, s, healthAdmit(t, s, c, "real-failure"), "failure")
	w, err := s.Window(context.Background(), c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, w.ValidSamples)
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.True(t, state.Open)
}

func healthTrip(t *testing.T, s *ZTAPIHealthStore, c ZTAPIModelConfig) {
	t.Helper()
	healthRecord(t, s, healthAdmit(t, s, c, "trip-one"), "failure")
	healthRecord(t, s, healthAdmit(t, s, c, "trip-two"), "failure")
}

func TestZTAPIHealthDisabledStillGatesAndAttemptsRecheck(t *testing.T) {
	s, c, _ := healthFixture(t)
	inflight := healthAdmit(t, s, c, "inflight")
	require.NoError(t, s.AdmitAttempt(context.Background(), inflight, 11, "openai"))
	require.NoError(t, s.AdmitAttempt(context.Background(), inflight, 12, "anthropic"))
	r, err := s.GetRequest(context.Background(), inflight.ExecutionID)
	require.NoError(t, err)
	require.Contains(t, r.Admissions, `"ChannelID":12`)
	healthTrip(t, s, c)
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	previous := DB
	DB = s.DB
	t.Cleanup(func() { DB = previous; InvalidateZTAPIAliasCache() })
	require.ErrorIs(t, CheckZTAPIHealthModelAvailable(c.PublicNameValue()), ErrZTAPIHealthCircuitOpen)
	_, err = AdmitZTAPIHealthRequest(context.Background(), c.PublicNameValue(), "new", "new", 1, false)
	require.ErrorIs(t, err, ErrZTAPIHealthCircuitOpen)
	require.ErrorIs(t, AdmitZTAPIHealthAttempt(context.Background(), inflight, 13, "openai"), ErrZTAPIHealthCircuitOpen)
	_, err = ResolveZTAPIRequestModel(c.PublicNameValue(), "default")
	require.ErrorIs(t, err, ErrZTAPIHealthCircuitOpen)
	nilTicket, err := AdmitZTAPIHealthRequest(context.Background(), "not-tracked", "new", "new", 1, false)
	require.NoError(t, err)
	require.Nil(t, nilTicket)
	require.NoError(t, RecordZTAPIHealthOutcome(context.Background(), nil, types.ZTAPIHealthOutcome{}))
	// A warm cache from another worker must not leak the tripped model.
	ztapiAliasCache.Lock()
	ztapiAliasCache.loaded = true
	ztapiAliasCache.loadedAt = time.Now()
	ztapiAliasCache.aliases = map[string]ztapiPublishedModel{c.PublicNameValue(): {ModelConfigID: c.ID, PublicName: c.PublicNameValue(), SourceModel: c.SourceModel}}
	ztapiAliasCache.Unlock()
	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Empty(t, catalog)
	configs, err := GetPublishedZTAPIModelConfigs()
	require.NoError(t, err)
	require.Empty(t, configs)
}

func TestZTAPIHealthOutboxLeaseRetryAndNarrowUnpublish(t *testing.T) {
	s, c, now := healthFixture(t)
	healthTrip(t, s, c)
	ctx := context.Background()
	jobs, err := s.ClaimOutbox(ctx, "alert", 10, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	job := jobs[0]
	require.Equal(t, 1, job.Attempts)
	other, err := s.ClaimOutbox(ctx, "alert", 10, 30)
	require.NoError(t, err)
	require.Empty(t, other)
	require.ErrorIs(t, s.FinishOutbox(ctx, job.ID, "forged", "", 0), ErrZTAPIHealthLeaseConflict)
	require.NoError(t, s.FinishOutbox(ctx, job.ID, job.LeaseToken, "synthetic_delivery_failure", *now+60))
	other, err = s.ClaimOutbox(ctx, "alert", 10, 30)
	require.NoError(t, err)
	require.Empty(t, other)
	*now += 60
	other, err = s.ClaimOutbox(ctx, "alert", 10, 30)
	require.NoError(t, err)
	require.Len(t, other, 1)
	require.Equal(t, 2, other[0].Attempts)
	require.NoError(t, s.FinishOutbox(ctx, other[0].ID, other[0].LeaseToken, "", 0))
	// Administrator updates after the incident must survive delayed unpublish.
	require.NoError(t, s.DB.Model(&c).Updates(map[string]any{"input_price_per_million": 99, "enabled_groups": `["enterprise"]`, "version": 8}).Error)
	jobs, err = s.ClaimOutbox(ctx, "unpublish", 10, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NoError(t, s.ProcessUnpublish(ctx, jobs[0].ID, jobs[0].LeaseToken))
	require.NoError(t, s.ProcessUnpublish(ctx, jobs[0].ID, jobs[0].LeaseToken))
	require.NoError(t, s.DB.First(&c, c.ID).Error)
	require.False(t, c.Published)
	require.Equal(t, float64(99), c.InputPricePerMillion)
	require.Equal(t, `["enterprise"]`, c.EnabledGroups)
	require.EqualValues(t, 9, c.Version)
	incidents, err := s.ListIncidents(ctx, c.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, incidents, 1)
	require.Equal(t, *now, incidents[0].UnpublishedAt)
	require.EqualValues(t, 9, incidents[0].UnpublishedVersion)
}

func TestZTAPIHealthManualRecoveryAndOldGeneration(t *testing.T) {
	s, c, _ := healthFixture(t)
	ctx := context.Background()
	inflight := healthAdmit(t, s, c, "old-inflight")
	healthTrip(t, s, c)
	jobs, err := s.ClaimOutbox(ctx, "unpublish", 1, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.ErrorIs(t, s.ManualRecover(ctx, c.ID, 999, 7, "verification:123"), ErrZTAPIHealthGenerationConflict)
	require.Error(t, s.ManualRecover(ctx, c.ID, 1, 0, "verification:123"))
	require.Error(t, s.ManualRecover(ctx, c.ID, 1, 7, ""))
	require.NoError(t, s.ManualRecover(ctx, c.ID, 1, 7, "verification:123"))
	state, err := s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.False(t, state.Open)
	require.EqualValues(t, 2, state.Generation)
	require.Zero(t, state.ConsecutiveFailures)
	require.ErrorIs(t, s.AdmitAttempt(ctx, inflight, 1, "openai"), ErrZTAPIHealthGenerationConflict)
	healthRecord(t, s, inflight, "failure")
	w, err := s.Window(ctx, c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, w.ValidSamples, "manual recovery must retain historical window")
	require.NoError(t, s.DB.First(&c, c.ID).Error)
	require.False(t, c.Published, "recovery never republishes, even with pending outbox")
	// Simulate a subsequent authorized publication. An old outbox cannot undo it.
	require.NoError(t, s.DB.Model(&c).Update("published", true).Error)
	require.NoError(t, s.ProcessUnpublish(ctx, jobs[0].ID, jobs[0].LeaseToken))
	require.NoError(t, s.DB.First(&c, c.ID).Error)
	require.True(t, c.Published)
	state, err = s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.False(t, state.Open)
	require.Zero(t, state.ConsecutiveFailures)
	var audit ZTAPIAuditEvent
	require.NoError(t, s.DB.Where("action = ?", "model.health_manual_recovery").First(&audit).Error)
	require.Equal(t, 7, audit.OperatorID)
	require.Contains(t, audit.Payload, "verification:123")
}

func TestZTAPIHealthRestartAndOrphans(t *testing.T) {
	s, c, now := healthFixture(t)
	ctx := context.Background()
	old := healthAdmit(t, s, c, "orphan")
	healthRecord(t, s, healthAdmit(t, s, c, "failed"), "failure")
	var dbs []struct {
		Seq  int
		Name string
		File string
	}
	require.NoError(t, s.DB.Raw("PRAGMA database_list").Scan(&dbs).Error)
	newDB, err := gorm.Open(sqlite.Open(dbs[0].File), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := newDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	s = NewZTAPIHealthStore(newDB)
	s.Now = func() time.Time { return time.Unix(*now, 0) }
	*now += 100
	count, err := s.MarkOrphansUnknown(ctx, *now-10, 10)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = s.MarkOrphansUnknown(ctx, *now-10, 10)
	require.NoError(t, err)
	require.Zero(t, count)
	healthRecord(t, s, old, "success")
	state, err := s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, state.ConsecutiveFailures)
	coverage, err := s.Coverage(ctx, c.ID, *now-3600)
	require.NoError(t, err)
	require.Len(t, coverage, 1)
	require.EqualValues(t, 1, coverage[0].UnknownSamples)
	require.EqualValues(t, 1, coverage[0].ValidSamples)
	jobs, err := s.ClaimOutbox(ctx, "coverage", 10, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	healthRecord(t, s, healthAdmit(t, s, c, "second-failure"), "failure")
	require.ErrorIs(t, s.CheckAvailable(ctx, c.PublicNameValue()), ErrZTAPIHealthCircuitOpen)
}

func TestZTAPIHealthTripAndOutboxAtomicRollback(t *testing.T) {
	s, c, _ := healthFixture(t)
	healthRecord(t, s, healthAdmit(t, s, c, "one"), "failure")
	two := healthAdmit(t, s, c, "two")
	require.NoError(t, s.DB.Exec("CREATE TRIGGER reject_health_outbox BEFORE INSERT ON ztapi_health_outbox BEGIN SELECT RAISE(ABORT, 'synthetic outbox failure'); END").Error)
	err := s.RecordOutcome(context.Background(), two, types.ZTAPIHealthOutcome{Result: "failure", Dispatched: true})
	require.Error(t, err)
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.False(t, state.Open)
	require.EqualValues(t, 1, state.ConsecutiveFailures)
	require.EqualValues(t, 1, state.CompletionSequence)
	require.NoError(t, s.DB.Exec("DROP TRIGGER reject_health_outbox").Error)
	healthRecord(t, s, two, "failure")
	require.ErrorIs(t, s.CheckAvailable(context.Background(), c.PublicNameValue()), ErrZTAPIHealthCircuitOpen)
}

func TestZTAPIHealthPublicationBlocker(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	require.NoError(t, MigrateZTAPIHealth(f.db))
	require.NoError(t, f.db.Create(&ZTAPIHealthState{ModelID: f.config.ID, Generation: 1, Open: true}).Error)
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Contains(t, blockers, "health_circuit_open")
	f.config.Published = true
	_, err = UpdateZTAPIModelConfigAndBilling(&f.config, f.config.Version, nil)
	require.Error(t, err)
}

func TestZTAPIHealthRefreshCannotReinsertTrippedAlias(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	require.NoError(t, MigrateZTAPIHealth(f.db))
	f.config.Published = true
	_, err := UpdateZTAPIModelConfigAndBilling(&f.config, f.config.Version, nil)
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&ZTAPIHealthState{ModelID: f.config.ID, Generation: 1, Open: true}).Error)
	require.NoError(t, refreshZTAPIAliasCache())
	ztapiAliasCache.RLock()
	_, found := ztapiAliasCache.aliases[f.config.PublicNameValue()]
	ztapiAliasCache.RUnlock()
	require.False(t, found, "a concurrent refresh must not reinsert an already tripped alias after another reader filters it")
	t.Cleanup(InvalidateZTAPIAliasCache)
}

func TestZTAPIHealthAdmissionOrderDoesNotDetermineStreak(t *testing.T) {
	s, c, _ := healthFixture(t)
	a, b, d := healthAdmit(t, s, c, "a"), healthAdmit(t, s, c, "b"), healthAdmit(t, s, c, "d")
	healthRecord(t, s, a, "failure")
	healthRecord(t, s, d, "success")
	healthRecord(t, s, b, "failure")
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.False(t, state.Open)
	require.EqualValues(t, 1, state.ConsecutiveFailures)
	events, err := s.ListEvents(context.Background(), c.ID, 0, 3)
	require.NoError(t, err)
	require.Equal(t, "d", events[1].ExecutionID)
}

func TestZTAPIHealthConcurrentOutboxClaimsAndLeaseExpiry(t *testing.T) {
	s, c, now := healthFixture(t)
	healthTrip(t, s, c)
	ctx := context.Background()
	var wg sync.WaitGroup
	jobs := make(chan ZTAPIHealthOutbox, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batch, err := s.ClaimOutbox(ctx, "alert", 1, 30)
			errs <- err
			for _, job := range batch {
				jobs <- job
			}
		}()
	}
	wg.Wait()
	close(jobs)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, jobs, 1)
	old := <-jobs
	*now += 30
	batch, err := s.ClaimOutbox(ctx, "alert", 1, 30)
	require.NoError(t, err)
	require.Len(t, batch, 1)
	require.NotEqual(t, old.LeaseToken, batch[0].LeaseToken)
	require.ErrorIs(t, s.FinishOutbox(ctx, old.ID, old.LeaseToken, "", 0), ErrZTAPIHealthLeaseConflict)
	require.NoError(t, s.FinishOutbox(ctx, batch[0].ID, batch[0].LeaseToken, "", 0))
}

func TestZTAPIHealthConcurrentAdminPriceEdit(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	require.NoError(t, MigrateZTAPIHealth(f.db))
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	f.config.Published = true
	c, err := UpdateZTAPIModelConfigAndBilling(&f.config, f.config.Version, nil)
	require.NoError(t, err)
	s := NewZTAPIHealthStore(f.db)
	healthTrip(t, s, *c)
	ctx := context.Background()
	jobs, err := s.ClaimOutbox(ctx, "unpublish", 1, 60)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	start, errs := make(chan struct{}), make(chan error, 2)
	go func() { <-start; errs <- s.ProcessUnpublish(ctx, jobs[0].ID, jobs[0].LeaseToken) }()
	go func() {
		<-start
		for i := 0; i < 5; i++ {
			current, err := GetZTAPIModelConfig(c.ID)
			if err != nil {
				errs <- err
				return
			}
			current.Published = false
			current.InputPricePerMillion = 123
			current.EnabledGroups = `["enterprise"]`
			_, err = UpdateZTAPIModelConfigAndBilling(current, current.Version, nil)
			if err == ErrZTAPIModelVersionConflict {
				continue
			}
			errs <- err
			return
		}
		errs <- fmt.Errorf("admin version retry exhausted")
	}()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	current, err := GetZTAPIModelConfig(c.ID)
	require.NoError(t, err)
	require.False(t, current.Published)
	require.Equal(t, float64(123), current.InputPricePerMillion)
	require.Equal(t, `["enterprise"]`, current.EnabledGroups)
}

func TestZTAPIHealthDuplicateAdmissionAndTicketTamper(t *testing.T) {
	s, c, _ := healthFixture(t)
	ticket := healthAdmit(t, s, c, "unique-server-id")
	_, err := s.AdmitRequest(context.Background(), c.PublicNameValue(), ticket.ExecutionID, ticket.RequestID, 1, false)
	require.ErrorIs(t, err, ErrZTAPIHealthExecutionConflict)
	for _, mutate := range []func(*types.ZTAPIHealthTicket){func(t *types.ZTAPIHealthTicket) { t.Generation++ }, func(t *types.ZTAPIHealthTicket) { t.Source = "probe" }, func(t *types.ZTAPIHealthTicket) { t.ConfigVersion++ }} {
		forged := *ticket
		mutate(&forged)
		require.ErrorIs(t, s.RecordOutcome(context.Background(), &forged, types.ZTAPIHealthOutcome{Result: "failure", Dispatched: true}), ErrZTAPIHealthInvalidTicket)
	}
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.Zero(t, state.CompletionSequence)
}
