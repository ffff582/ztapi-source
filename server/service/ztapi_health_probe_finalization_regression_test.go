package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func finalizationWorkerFixture(t *testing.T) (*ZTAPIHealthWorker, *int) {
	t.Helper()
	w, _, sends := healthWorkerFixture(t)
	list := w.backend.ListTargets
	w.backend.ListTargets = func(ctx context.Context, limit int) ([]model.ZTAPIProbeTarget, error) {
		targets, err := list(ctx, limit)
		return targets[:1], err
	}
	w.config.BatchSize = 1
	return w, sends
}

func TestZTAPIHealthProbeFinalizationDelayedPersistence(t *testing.T) {
	w, sends := finalizationWorkerFixture(t)
	db := w.backend.Probes.DB
	require.NoError(t, model.MigrateZTAPIHealth(db))
	store := model.NewZTAPIHealthStore(db)
	store.Now = w.config.Now
	backend, err := AttachZTAPIHealthStore(w.backend, store, w.config.ProbeUserID)
	require.NoError(t, err)
	type pendingFinalization struct {
		job       model.ZTAPIProbeJob
		requestID string
	}
	ready := make(chan pendingFinalization, 1)
	persisted := make(chan error, 1)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() { close(stop); wg.Wait() })
	go func() {
		defer wg.Done()
		var pending pendingFinalization
		select {
		case pending = <-ready:
		case <-stop:
			return
		}
		timer := time.NewTimer(75 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-stop:
			return
		}
		job := pending.job
		persisted <- db.Transaction(func(tx *gorm.DB) error {
			request := model.ZTAPIHealthRequest{ExecutionID: "delayed-finalization", RequestID: pending.requestID, ModelID: job.ModelID,
				UserID: w.config.ProbeUserID, PublicModel: job.PublicModel, Generation: job.Generation, ConfigVersion: job.ConfigVersion,
				Source: "probe", Stream: job.Stream, Completed: true}
			if err := tx.Create(&request).Error; err != nil {
				return err
			}
			return tx.Create(&model.ZTAPIHealthEvent{ExecutionID: request.ExecutionID, RequestID: request.RequestID, ModelID: job.ModelID,
				Generation: job.Generation, ConfigVersion: job.ConfigVersion, Source: "probe", Stream: job.Stream,
				Result: "success", Reason: "probe_valid_output", Counted: true, CompletionSequence: 1}).Error
		})
	}()
	reads := 0
	w.backend.ProbeFinalized = func(ctx context.Context, job model.ZTAPIProbeJob, requestID string) (bool, error) {
		reads++
		finalized, err := backend.ProbeFinalized(ctx, job, requestID)
		if reads == 1 {
			ready <- pendingFinalization{job: job, requestID: requestID}
		}
		return finalized, err
	}
	require.NoError(t, w.runProbes(context.Background()))
	require.Equal(t, 1, *sends)
	var job model.ZTAPIProbeJob
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, "completed", job.State)
	require.Equal(t, "functional_pass", job.ResultCode)
	require.Greater(t, reads, 1)
	require.NoError(t, <-persisted)
	for _, table := range []any{&model.ZTAPIHealthRequest{}, &model.ZTAPIHealthEvent{}} {
		var count int64
		require.NoError(t, db.Model(table).Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
	for _, table := range []any{&model.ZTAPIHealthState{}, &model.ZTAPIHealthIncident{}, &model.ZTAPIHealthOutbox{}} {
		var count int64
		require.NoError(t, db.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
	budget, err := w.backend.Probes.Budget(context.Background())
	require.NoError(t, err)
	require.NoError(t, w.runProbes(context.Background()))
	after, err := w.backend.Probes.Budget(context.Background())
	require.NoError(t, err)
	require.Equal(t, budget, after)
	require.Equal(t, 1, *sends)
}

func TestZTAPIHealthProbeFinalizationTimeout(t *testing.T) {
	w, sends := finalizationWorkerFixture(t)
	reads := 0
	bounded := true
	w.backend.ProbeFinalized = func(ctx context.Context, _ model.ZTAPIProbeJob, _ string) (bool, error) {
		reads++
		deadline, ok := ctx.Deadline()
		bounded = bounded && ok && time.Until(deadline) <= 2*time.Second
		return false, nil
	}
	start := time.Now()
	require.NoError(t, w.runProbes(context.Background()))
	elapsed := time.Since(start)
	require.True(t, bounded, "every persistence read must have a bounded context")
	require.Greater(t, reads, 1)
	require.LessOrEqual(t, reads, 45)
	require.GreaterOrEqual(t, elapsed, 1800*time.Millisecond)
	require.Less(t, elapsed, 4*time.Second)
	var job model.ZTAPIProbeJob
	require.NoError(t, w.backend.Probes.DB.First(&job).Error)
	require.Equal(t, "unknown", job.State)
	require.Equal(t, "probe_final_result_missing", job.ResultCode)
	require.NoError(t, w.runProbes(context.Background()))
	require.Equal(t, 1, *sends, "finalization timeout must not retry the HTTP request")
}

func TestZTAPIHealthProbeFinalizationCancellation(t *testing.T) {
	w, sends := finalizationWorkerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	w.backend.ProbeFinalized = func(readCtx context.Context, _ model.ZTAPIProbeJob, _ string) (bool, error) {
		reads++
		if reads == 1 {
			return false, nil
		}
		cancel()
		return false, readCtx.Err()
	}
	start := time.Now()
	require.ErrorIs(t, w.runProbes(ctx), context.Canceled)
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, 2, reads)
	require.Equal(t, 1, *sends)
	var job model.ZTAPIProbeJob
	require.NoError(t, w.backend.Probes.DB.First(&job).Error)
	require.Equal(t, "dispatching", job.State, "cancelled completion must retain the ambiguous durable dispatch")
	require.NoError(t, w.runProbes(context.Background()))
	require.Equal(t, 1, *sends)
}

func TestZTAPIHealthProbeFinalizationLeaseBudget(t *testing.T) {
	w, sends := finalizationWorkerFixture(t)
	config := w.config
	config.RequestTimeout, config.LeaseDuration = time.Second, 1100*time.Millisecond
	var err error
	w, err = NewZTAPIHealthWorker(w.backend, config)
	require.NoError(t, err)
	bounded := true
	w.backend.ProbeFinalized = func(ctx context.Context, _ model.ZTAPIProbeJob, _ string) (bool, error) {
		deadline, ok := ctx.Deadline()
		bounded = bounded && ok && time.Until(deadline) <= 1100*time.Millisecond
		return false, nil
	}
	start := time.Now()
	require.NoError(t, w.runProbes(context.Background()))
	require.True(t, bounded)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, 1, *sends)
}

func TestZTAPIHealthProbeFinalizationLookupError(t *testing.T) {
	w, sends := finalizationWorkerFixture(t)
	reads := 0
	w.backend.ProbeFinalized = func(context.Context, model.ZTAPIProbeJob, string) (bool, error) {
		reads++
		return false, errors.New("offline persistence unavailable")
	}
	require.NoError(t, w.runProbes(context.Background()))
	require.Equal(t, 1, reads)
	require.Equal(t, 1, *sends)
	var job model.ZTAPIProbeJob
	require.NoError(t, w.backend.Probes.DB.First(&job).Error)
	require.Equal(t, "unknown", job.State)
	require.Equal(t, "probe_final_result_missing", job.ResultCode)
}
