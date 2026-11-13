package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReferralWorkerDisabledDoesNotTouchDatabase(t *testing.T) {
	var calls atomic.Int32
	w := startZTAPIReferralWorker(context.Background(), false, time.Millisecond, func(context.Context) (model.ZTAPIReferralReconcileResult, error) {
		calls.Add(1)
		return model.ZTAPIReferralReconcileResult{}, nil
	})
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("disabled worker did not stop")
	}
	require.Zero(t, calls.Load())
}

func TestZTAPIReferralWorkerIsBoundedAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{}, 1)
	w := startZTAPIReferralWorker(ctx, true, time.Millisecond, func(ctx context.Context) (model.ZTAPIReferralReconcileResult, error) {
		_, ok := ctx.Deadline()
		require.True(t, ok)
		select {
		case called <- struct{}{}:
		default:
		}
		return model.ZTAPIReferralReconcileResult{Scanned: 1}, nil
	})
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not reconcile")
	}
	cancel()
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestZTAPIReferralWorkerReadinessRequiresRecentSuccessfulScan(t *testing.T) {
	now := time.Now().UTC()
	healthy := ZTAPIReferralWorkerStatus{Enabled: true, Running: true, LastHeartbeat: now.Unix(), LastSuccess: now.Unix()}
	require.True(t, healthy.Ready(now))
	for _, change := range []func(*ZTAPIReferralWorkerStatus){
		func(s *ZTAPIReferralWorkerStatus) { s.LastSuccess = 0 },
		func(s *ZTAPIReferralWorkerStatus) { s.LastSuccess = now.Add(-3 * time.Minute).Unix() },
		func(s *ZTAPIReferralWorkerStatus) { s.LastHeartbeat = now.Add(-3 * time.Minute).Unix() },
		func(s *ZTAPIReferralWorkerStatus) { s.LastError = "reconciliation_unavailable" },
		func(s *ZTAPIReferralWorkerStatus) { s.Enabled = false },
		func(s *ZTAPIReferralWorkerStatus) { s.Running = false },
		func(s *ZTAPIReferralWorkerStatus) { s.LastSuccess = now.Add(time.Minute).Unix() },
	} {
		s := healthy
		change(&s)
		require.False(t, s.Ready(now))
	}
}
