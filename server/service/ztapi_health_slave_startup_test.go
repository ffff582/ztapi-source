package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIProductionHealthWorkerSlaveIsInert(t *testing.T) {
	before := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = before })
	t.Setenv("ZTAPI_HEALTH_PROBE_BUDGET_USD", "invalid-for-master")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker, err := StartProductionZTAPIHealthWorker(ctx)
	require.NoError(t, err)
	require.NotNil(t, worker)
	select {
	case <-worker.Done():
	default:
		t.Fatal("slave must not start a health worker goroutine")
	}
	require.NoError(t, worker.RunOnce(ctx), "an inert handle must not claim outbox work")
}

func TestZTAPIProductionHealthWorkerMasterStillValidatesConfiguration(t *testing.T) {
	before := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = before })
	t.Setenv("ZTAPI_HEALTH_PROBE_BUDGET_USD", "invalid-for-master")
	worker, err := StartProductionZTAPIHealthWorker(context.Background())
	require.Error(t, err)
	require.Nil(t, worker)
}
