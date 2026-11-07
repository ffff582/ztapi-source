package service

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type ZTAPIReferralWorkerStatus struct {
	Enabled       bool   `json:"enabled"`
	Running       bool   `json:"running"`
	LastHeartbeat int64  `json:"last_heartbeat"`
	LastSuccess   int64  `json:"last_success"`
	LastError     string `json:"last_error,omitempty"`
	LastScanned   int    `json:"last_scanned"`
	LastCreated   int    `json:"last_created"`
}

func (s ZTAPIReferralWorkerStatus) Ready(at time.Time) bool {
	cutoff, now := at.Add(-2*time.Minute).Unix(), at.Unix()
	return s.Enabled && s.Running && s.LastError == "" && s.LastSuccess >= cutoff && s.LastSuccess <= now && s.LastHeartbeat >= cutoff && s.LastHeartbeat <= now
}

func ZTAPIReferralWorkerReady(at time.Time) bool {
	return GetZTAPIReferralWorkerStatus().Ready(at)
}

type ZTAPIReferralWorker struct {
	done   chan struct{}
	mu     sync.RWMutex
	status ZTAPIReferralWorkerStatus
}

func (w *ZTAPIReferralWorker) Done() <-chan struct{} { return w.done }

var ztapiReferralProductionWorker atomic.Pointer[ZTAPIReferralWorker]

func GetZTAPIReferralWorkerStatus() ZTAPIReferralWorkerStatus {
	w := ztapiReferralProductionWorker.Load()
	if w == nil {
		return ZTAPIReferralWorkerStatus{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

func StartProductionZTAPIReferralWorker(ctx context.Context) *ZTAPIReferralWorker {
	w := startZTAPIReferralWorker(ctx, os.Getenv("ZTAPI_REFERRAL_WORKER_ENABLED") == "true", 30*time.Second, func(ctx context.Context) (model.ZTAPIReferralReconcileResult, error) {
		return model.ReconcileZTAPIReferralSettlements(ctx, 100)
	})
	ztapiReferralProductionWorker.Store(w)
	return w
}

func startZTAPIReferralWorker(ctx context.Context, enabled bool, interval time.Duration, reconcile func(context.Context) (model.ZTAPIReferralReconcileResult, error)) *ZTAPIReferralWorker {
	w := &ZTAPIReferralWorker{done: make(chan struct{}), status: ZTAPIReferralWorkerStatus{Enabled: enabled, Running: enabled, LastHeartbeat: time.Now().UTC().Unix()}}
	if !enabled {
		close(w.done)
		return w
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		defer close(w.done)
		defer func() { w.mu.Lock(); w.status.Running = false; w.mu.Unlock() }()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var lastLog time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result, err := func() (result model.ZTAPIReferralReconcileResult, err error) {
					defer func() {
						if recover() != nil {
							err = model.ErrZTAPIReferralConflict
						}
					}()
					bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					return reconcile(bounded)
				}()
				now := time.Now().UTC()
				w.mu.Lock()
				w.status.LastHeartbeat = now.Unix()
				w.status.LastScanned = result.Scanned
				w.status.LastCreated = result.Created
				if err == nil {
					w.status.LastSuccess = now.Unix()
					w.status.LastError = ""
				} else {
					w.status.LastError = "reconciliation_unavailable"
				}
				w.mu.Unlock()
				if err != nil && ctx.Err() == nil && (lastLog.IsZero() || now.Sub(lastLog) >= 5*time.Minute) {
					common.SysError("referral reconciliation temporarily unavailable; committed recharge evidence will be retried")
					lastLog = now
				}
			}
		}
	}()
	return w
}
