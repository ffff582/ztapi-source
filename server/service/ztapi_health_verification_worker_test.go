package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func enqueueWorkerVerificationCase(t *testing.T, db *gorm.DB, now time.Time) (model.ZTAPIHealthVerificationCase, model.ZTAPIHealthRouteIdentity) {
	return enqueueWorkerVerificationCaseProtocol(t, db, now, "chat")
}

func TestZTAPIHealthProbeProtocolPreservesNativeVerificationRoute(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "claude", "gemini"} {
		got, ok := ztapiHealthProbeProtocol("quoted-text-model", model.ZTAPIModalityText, protocol)
		require.True(t, ok, protocol)
		require.Equal(t, protocol, got)
	}
	_, ok := ztapiHealthProbeProtocol("quoted-text-model", model.ZTAPIModalityText, "video-tasks")
	require.False(t, ok)
}

func enqueueWorkerVerificationCaseProtocol(t *testing.T, db *gorm.DB, now time.Time, protocol string) (model.ZTAPIHealthVerificationCase, model.ZTAPIHealthRouteIdentity) {
	t.Helper()
	fingerprint, err := model.FingerprintZTAPICredential("upstream-route-key")
	require.NoError(t, err)
	route := model.ZTAPIHealthRouteIdentity{ModelID: 1, ChannelID: 7, Protocol: protocol, Stream: false, CredentialVersion: fingerprint, Generation: 1}
	event := model.ZTAPIHealthEvent{
		ExecutionID: "customer-observation", RequestID: "customer-request", ModelID: route.ModelID,
		ConfigVersion: 1, Generation: route.Generation, PublicModel: "public-model", Source: "real",
		Result: "suspected", Reason: "empty_output", Counted: false, ChannelID: route.ChannelID,
		CredentialVersion: route.CredentialVersion.String(), UpstreamProtocol: route.Protocol, Outcome: `{}`,
	}
	require.NoError(t, db.Create(&event).Error)
	verificationCase, created, err := model.NewZTAPIHealthVerificationStore(db).EnqueueSuspicion(context.Background(), model.ZTAPIHealthSuspicion{Route: route, SourceEventID: event.ID}, now)
	require.NoError(t, err)
	require.True(t, created)
	return verificationCase, route
}

func TestZTAPIVerificationWorkerCancelsUnsupportedProtocolWithoutPaidPreparation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-worker-unsupported.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, model.MigrateZTAPIHealth(db))
	now := time.Unix(2_000_000_000, 0).UTC()
	verificationCase, _ := enqueueWorkerVerificationCaseProtocol(t, db, now, "unsupported")

	identityChecks, targetLoads, sends := 0, 0, 0
	config := DefaultZTAPIHealthWorkerConfig()
	config.ProbeKey, config.ProbeUserID = "offline-probe-key", 999
	config.Now = func() time.Time { return now }
	config.ProbeTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		sends++
		return workerResponse(http.StatusOK, `{}`), nil
	})
	worker, err := NewZTAPIHealthWorker(ZTAPIHealthWorkerBackend{
		Verifications: model.NewZTAPIHealthVerificationStore(db),
		ValidateProbeIdentity: func(context.Context) (bool, string, error) {
			identityChecks++
			return true, "probe_ready", nil
		},
		LoadVerificationTarget: func(context.Context, model.ZTAPIHealthVerificationCase) (model.ZTAPIProbeTarget, bool, error) {
			targetLoads++
			return model.ZTAPIProbeTarget{}, false, nil
		},
	}, config)
	require.NoError(t, err)
	require.NoError(t, worker.runProbes(context.Background()))
	require.Zero(t, identityChecks)
	require.Zero(t, targetLoads)
	require.Zero(t, sends)
	var persisted model.ZTAPIHealthVerificationCase
	require.NoError(t, db.First(&persisted, "id = ?", verificationCase.ID).Error)
	require.Equal(t, "cancelled", persisted.State)
	require.Equal(t, "protocol_invalid", persisted.Result)
}

func TestZTAPIVerificationWorkerIdleDoesNoPaidPreparation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-worker.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, model.MigrateZTAPIHealth(db))

	now := time.Unix(2_000_000_000, 0).UTC()
	identityChecks, targetLoads, dispatchChecks, sends := 0, 0, 0, 0
	config := DefaultZTAPIHealthWorkerConfig()
	config.SyntheticProbesEnabled = true
	config.ProbeKey = "offline-probe-key"
	config.ProbeUserID = 999
	config.ProbeIdentityValidated = true
	config.Now = func() time.Time { return now }
	config.ProbeTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		sends++
		return workerResponse(http.StatusInternalServerError, `{}`), nil
	})
	backend := ZTAPIHealthWorkerBackend{
		Verifications: model.NewZTAPIHealthVerificationStore(db),
		ValidateProbeIdentity: func(context.Context) (bool, string, error) {
			identityChecks++
			return true, "probe_ready", nil
		},
		LoadVerificationTarget: func(context.Context, model.ZTAPIHealthVerificationCase) (model.ZTAPIProbeTarget, bool, error) {
			targetLoads++
			return model.ZTAPIProbeTarget{}, false, nil
		},
		CheckVerificationDispatch: func(context.Context, *gorm.DB, model.ZTAPIHealthVerificationCase) (model.ZTAPIHealthVerificationDispatchAdmission, error) {
			dispatchChecks++
			return model.ZTAPIHealthVerificationDispatchAdmission{}, nil
		},
	}
	worker, err := NewZTAPIHealthWorker(backend, config)
	require.NoError(t, err)
	require.NoError(t, worker.runProbes(context.Background()))

	require.Zero(t, identityChecks, "an idle worker must not authenticate a paid probe identity")
	require.Zero(t, targetLoads, "an idle worker must not inspect catalog targets")
	require.Zero(t, dispatchChecks, "an idle worker must not read prices, samples, or budget")
	require.Zero(t, sends, "an idle worker must not issue an HTTP request")
	require.False(t, db.Migrator().HasTable(&model.ZTAPIProbeBudget{}), "an idle worker must not provision a runtime allowance")
}

func TestZTAPIVerificationWorkerDispatchesOnePinned64TokenProbe(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-worker-active.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.ZTAPIProbeBudget{}))
	_, err = model.InitializeZTAPIProbeAllocation(context.Background(), db, "worker-verification-budget", 30_000_000_000)
	require.NoError(t, err)
	now := time.Unix(2_000_000_000, 0).UTC()
	verificationCase, route := enqueueWorkerVerificationCase(t, db, now)
	target := model.ZTAPIProbeTarget{ModelID: 1, PublicModel: "public-model", SourceModel: "source-model", Protocol: "chat", Generation: 1, ConfigVersion: 1, InputNanoUSDPerMillion: 1_000_000, OutputNanoUSDPerMillion: 2_000_000}

	sends := 0
	config := DefaultZTAPIHealthWorkerConfig()
	config.SyntheticProbesEnabled = true
	config.ProbeKey = "offline-probe-key"
	config.ProbeUserID = 999
	config.ProbeIdentityValidated = true
	config.Now = func() time.Time { return now }
	config.RelayBaseURL = "http://127.0.0.1:3000"
	config.ProbeTransport = healthWorkerTransport(func(request *http.Request) (*http.Response, error) {
		sends++
		require.Equal(t, verificationCase.ID, request.Header.Get("X-ZTAPI-Health-Case-ID"))
		require.NotEmpty(t, request.Header.Get("X-ZTAPI-Health-Lease-Token"))
		require.Equal(t, "Bearer offline-probe-key", request.Header.Get("Authorization"))
		body, readErr := io.ReadAll(request.Body)
		require.NoError(t, readErr)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(bytes.NewReader(body)).Decode(&payload))
		require.EqualValues(t, model.ZTAPIVerificationMaxOutputTokens, payload["max_tokens"])
		return workerResponse(http.StatusOK, `{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`), nil
	})
	store := model.NewZTAPIHealthVerificationStore(db)
	backend := ZTAPIHealthWorkerBackend{
		Verifications:         store,
		ValidateProbeIdentity: func(context.Context) (bool, string, error) { return true, "probe_ready", nil },
		LoadVerificationTarget: func(_ context.Context, got model.ZTAPIHealthVerificationCase) (model.ZTAPIProbeTarget, bool, error) {
			require.Equal(t, verificationCase.ID, got.ID)
			return target, true, nil
		},
		CheckVerificationDispatch: func(_ context.Context, _ *gorm.DB, got model.ZTAPIHealthVerificationCase) (model.ZTAPIHealthVerificationDispatchAdmission, error) {
			return model.ZTAPIHealthVerificationDispatchAdmission{Active: true, Route: got.RouteIdentity(), InputTokens: 2000, InputNanoUSDPerMillion: target.InputNanoUSDPerMillion, OutputNanoUSDPerMillion: target.OutputNanoUSDPerMillion}, nil
		},
		LoadVerificationEvidence: func(_ context.Context, got model.ZTAPIHealthVerificationCase) (*ZTAPIVerificationProbeEvidence, error) {
			return &ZTAPIVerificationProbeEvidence{RequestID: got.ProbeRequestID, UpstreamRequestID: "upstream-request", Result: "healthy", Route: route, InputTokens: 10, OutputTokens: 1}, nil
		},
	}
	worker, err := NewZTAPIHealthWorker(backend, config)
	require.NoError(t, err)
	require.NoError(t, worker.runProbes(context.Background()))
	require.Equal(t, 1, sends)
	var persisted model.ZTAPIHealthVerificationCase
	require.NoError(t, db.First(&persisted, "id = ?", verificationCase.ID).Error)
	require.Equal(t, "completed", persisted.State)
	require.Equal(t, "healthy", persisted.Result)
}

func TestZTAPIVerificationWorkerRouteChangeUsesNoBudgetAndNoHTTP(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-worker-route-change.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.ZTAPIProbeBudget{}))
	_, err = model.InitializeZTAPIProbeAllocation(context.Background(), db, "worker-route-change-budget", 30_000_000_000)
	require.NoError(t, err)
	now := time.Unix(2_000_000_000, 0).UTC()
	verificationCase, _ := enqueueWorkerVerificationCase(t, db, now)
	sends, dispatchChecks := 0, 0
	config := DefaultZTAPIHealthWorkerConfig()
	config.SyntheticProbesEnabled = true
	config.ProbeKey, config.ProbeUserID, config.ProbeIdentityValidated = "offline-probe-key", 999, true
	config.Now = func() time.Time { return now }
	config.ProbeTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		sends++
		return workerResponse(http.StatusOK, `{}`), nil
	})
	backend := ZTAPIHealthWorkerBackend{
		Verifications:         model.NewZTAPIHealthVerificationStore(db),
		ValidateProbeIdentity: func(context.Context) (bool, string, error) { return true, "probe_ready", nil },
		LoadVerificationTarget: func(context.Context, model.ZTAPIHealthVerificationCase) (model.ZTAPIProbeTarget, bool, error) {
			return model.ZTAPIProbeTarget{}, false, nil
		},
		CheckVerificationDispatch: func(context.Context, *gorm.DB, model.ZTAPIHealthVerificationCase) (model.ZTAPIHealthVerificationDispatchAdmission, error) {
			dispatchChecks++
			return model.ZTAPIHealthVerificationDispatchAdmission{}, nil
		},
		LoadVerificationEvidence: func(context.Context, model.ZTAPIHealthVerificationCase) (*ZTAPIVerificationProbeEvidence, error) {
			return nil, nil
		},
	}
	worker, err := NewZTAPIHealthWorker(backend, config)
	require.NoError(t, err)
	require.NoError(t, worker.runProbes(context.Background()))
	require.Zero(t, dispatchChecks)
	require.Zero(t, sends)
	var persisted model.ZTAPIHealthVerificationCase
	require.NoError(t, db.First(&persisted, "id = ?", verificationCase.ID).Error)
	require.Equal(t, "cancelled", persisted.State)
	var budget model.ZTAPIProbeBudget
	require.NoError(t, db.First(&budget, 1).Error)
	require.Zero(t, budget.AccountedNanoUSD)
}

func TestZTAPIVerificationEvidenceJoinsConsumeLogByProbeRequestID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-evidence.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.Log{}))

	fingerprint, err := model.FingerprintZTAPICredential("authorization\x00Bearer enterprise-key")
	require.NoError(t, err)
	verificationCase := model.ZTAPIHealthVerificationCase{
		ID: "6c1ab992-1b33-4c84-8b76-2f15ff684de0", ModelID: 11, ChannelID: 12,
		EntryProtocol: "chat", Protocol: "chat", CredentialVersion: fingerprint.String(), Generation: 2,
		ProbeRequestID: "ztapi-health:6c1ab992-1b33-4c84-8b76-2f15ff684de0:1",
	}
	executionID := "f5d2ee62-3586-4ca7-927f-e3a68cce894b"
	require.NoError(t, db.Create(&model.ZTAPIHealthRequest{
		ExecutionID: executionID, RequestID: verificationCase.ProbeRequestID,
		ModelID: verificationCase.ModelID, UserID: 999, Source: "probe", EntryProtocol: "chat",
		Generation: verificationCase.Generation, Completed: true,
	}).Error)
	require.NoError(t, db.Create(&model.ZTAPIHealthEvent{
		ExecutionID: executionID, RequestID: verificationCase.ProbeRequestID,
		ModelID: verificationCase.ModelID, Generation: verificationCase.Generation,
		Source: "probe", Result: "success", ChannelID: verificationCase.ChannelID, EntryProtocol: "chat",
		CredentialVersion: verificationCase.CredentialVersion, UpstreamProtocol: verificationCase.Protocol,
		UpstreamRequestID: "upstream-probe-request", Outcome: `{}`,
	}).Error)
	require.NoError(t, db.Create(&model.Log{
		RequestId: verificationCase.ProbeRequestID, UserId: 999, Type: model.LogTypeConsume,
		PromptTokens: 17, CompletionTokens: 3,
	}).Error)

	evidence, err := productionZTAPIVerificationEvidence(context.Background(), db, db, 999, verificationCase)
	require.NoError(t, err)
	require.NotNil(t, evidence)
	require.EqualValues(t, 17, evidence.InputTokens)
	require.EqualValues(t, 3, evidence.OutputTokens)

	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ?", verificationCase.ProbeRequestID).Update("request_id", executionID).Error)
	evidence, err = productionZTAPIVerificationEvidence(context.Background(), db, db, 999, verificationCase)
	require.NoError(t, err)
	require.Nil(t, evidence, "the internal execution ID is not the billable request correlation key")
}

func TestZTAPIVerificationFailureEvidenceDoesNotRequireConsumeLog(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-failure-evidence.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.Log{}))

	fingerprint, err := model.FingerprintZTAPICredential("authorization\x00Bearer enterprise-key")
	require.NoError(t, err)
	verificationCase := model.ZTAPIHealthVerificationCase{
		ID: "4ff9e55e-63e8-4b1a-a317-9ec84553ef84", ModelID: 21, ChannelID: 22,
		EntryProtocol: "chat", Protocol: "chat", CredentialVersion: fingerprint.String(), Generation: 3,
		ProbeRequestID: "ztapi-health:4ff9e55e-63e8-4b1a-a317-9ec84553ef84:1",
	}
	executionID := "8031d649-bf08-49ff-ad85-9adf76422863"
	require.NoError(t, db.Create(&model.ZTAPIHealthRequest{
		ExecutionID: executionID, RequestID: verificationCase.ProbeRequestID,
		ModelID: verificationCase.ModelID, UserID: 999, Source: "probe", EntryProtocol: "chat",
		Generation: verificationCase.Generation, Completed: true,
	}).Error)
	require.NoError(t, db.Create(&model.ZTAPIHealthEvent{
		ExecutionID: executionID, RequestID: verificationCase.ProbeRequestID,
		ModelID: verificationCase.ModelID, Generation: verificationCase.Generation,
		Source: "probe", Result: "failure", Reason: "upstream_http_error", EntryProtocol: "chat",
		ChannelID: verificationCase.ChannelID, CredentialVersion: verificationCase.CredentialVersion,
		UpstreamProtocol: verificationCase.Protocol, UpstreamRequestID: "provider-request-1",
	}).Error)

	evidence, err := productionZTAPIVerificationEvidence(context.Background(), db, db, 999, verificationCase)
	require.NoError(t, err)
	require.NotNil(t, evidence)
	require.Equal(t, "failure", evidence.Result)
	require.Equal(t, "provider-request-1", evidence.UpstreamRequestID)
	require.Zero(t, evidence.InputTokens)
	require.Zero(t, evidence.OutputTokens)

	var consumeLogs int64
	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ? AND type = ?", verificationCase.ProbeRequestID, model.LogTypeConsume).Count(&consumeLogs).Error)
	require.Zero(t, consumeLogs)
}
