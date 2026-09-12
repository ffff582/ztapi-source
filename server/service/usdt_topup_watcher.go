package service

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/google/uuid"
)

const (
	usdtWatcherRecoveryInterval  = 30 * time.Second
	usdtWatcherForbiddenDelay    = 5 * time.Minute
	usdtWatcherMaxBackoff        = 30 * time.Second
	usdtWatcherConfirmationGrace = 2 * time.Minute
)

type USDTTransferClient interface {
	ListConfirmedIncomingUSDT(ctx context.Context, address string, contract string, minTimestampMS int64) ([]model.TRC20Transfer, error)
}

type USDTWatcherSnapshot struct {
	PendingOrders         int64     `json:"pending_orders"`
	ConfirmingOrders      int64     `json:"confirming_orders"`
	Active                bool      `json:"active"`
	Standby               bool      `json:"standby"`
	Healthy               bool      `json:"healthy"`
	LastSuccessfulQueryAt time.Time `json:"last_successful_query_at"`
	NextPollAt            time.Time `json:"next_poll_at"`
	ConsecutiveFailures   int       `json:"consecutive_failures"`
	LastErrorCategory     string    `json:"last_error_category"`
	SettledTotal          int64     `json:"settled_total"`
	ExpiredTotal          int64     `json:"expired_total"`
	ManualReviewTotal     int64     `json:"manual_review_total"`
	UnmatchedTotal        int64     `json:"unmatched_total"`
}

type USDTWatcher struct {
	config           setting.USDTTopUpConfig
	client           USDTTransferClient
	holderID         string
	leaseName        string
	leaseDuration    time.Duration
	recoveryInterval time.Duration
	wake             chan struct{}
	now              func() time.Time
	backoffJitter    func(time.Duration) time.Duration

	cycleMu sync.Mutex
	stateMu sync.RWMutex
	state   USDTWatcherSnapshot
}

func NewUSDTWatcher(config setting.USDTTopUpConfig, client USDTTransferClient, holderID string) *USDTWatcher {
	leaseDuration := 3 * config.PollInterval
	if leaseDuration < 15*time.Second {
		leaseDuration = 15 * time.Second
	}
	return &USDTWatcher{
		config:           config,
		client:           client,
		holderID:         strings.TrimSpace(holderID),
		leaseName:        "usdt-trc20:" + strings.TrimSpace(config.ReceivingAddress),
		leaseDuration:    leaseDuration,
		recoveryInterval: usdtWatcherRecoveryInterval,
		wake:             make(chan struct{}, 1),
		now:              func() time.Time { return time.Now().UTC() },
		backoffJitter:    secureUSDTWatcherJitter,
		state:            USDTWatcherSnapshot{Healthy: true},
	}
}

func (watcher *USDTWatcher) Wake() {
	if watcher == nil {
		return
	}
	select {
	case watcher.wake <- struct{}{}:
	default:
	}
}

func (watcher *USDTWatcher) Snapshot() USDTWatcherSnapshot {
	if watcher == nil {
		return USDTWatcherSnapshot{}
	}
	watcher.stateMu.RLock()
	defer watcher.stateMu.RUnlock()
	return watcher.state
}

func (watcher *USDTWatcher) Run(ctx context.Context) {
	if watcher == nil {
		return
	}
	for {
		now := watcher.now().UTC()
		snapshot, _ := watcher.pollOnce(ctx, now)
		delay := watcher.recoveryInterval
		if snapshot.Active && !snapshot.NextPollAt.IsZero() {
			delay = snapshot.NextPollAt.Sub(now)
			if delay < 0 {
				delay = 0
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-watcher.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (watcher *USDTWatcher) pollOnce(ctx context.Context, now time.Time) (USDTWatcherSnapshot, error) {
	watcher.cycleMu.Lock()
	defer watcher.cycleMu.Unlock()
	now = now.UTC()
	reconciliationCutoff := now.Add(-usdtWatcherConfirmationGrace)

	_, expired, err := model.AdvanceUSDTTopUpExpiryStates(now, usdtWatcherConfirmationGrace)
	if err != nil {
		return watcher.recordUSDTWatcherFailure(now, "database", err)
	}
	orders, err := model.ListReconcilableUSDTTopUpOrders(reconciliationCutoff)
	if err != nil {
		return watcher.recordUSDTWatcherFailure(now, "database", err)
	}
	watcher.stateMu.Lock()
	watcher.state.ExpiredTotal += expired
	watcher.state.PendingOrders, watcher.state.ConfirmingOrders = countUSDTWatcherOrders(orders)
	watcher.state.Standby = false
	if len(orders) == 0 {
		watcher.state.Active = false
		watcher.state.NextPollAt = time.Time{}
		snapshot := watcher.state
		watcher.stateMu.Unlock()
		return snapshot, nil
	}
	watcher.state.Active = true
	if !watcher.state.NextPollAt.IsZero() && now.Before(watcher.state.NextPollAt) {
		snapshot := watcher.state
		watcher.stateMu.Unlock()
		return snapshot, nil
	}
	watcher.stateMu.Unlock()

	acquired, err := model.AcquireUSDTWatcherLease(watcher.leaseName, watcher.holderID, now, watcher.leaseDuration)
	if err != nil {
		return watcher.recordUSDTWatcherFailure(now, "database", err)
	}
	if !acquired {
		watcher.stateMu.Lock()
		watcher.state.Standby = true
		watcher.state.NextPollAt = now.Add(watcher.config.PollInterval)
		snapshot := watcher.state
		watcher.stateMu.Unlock()
		return snapshot, nil
	}
	if watcher.client == nil {
		return watcher.recordUSDTWatcherFailure(now, "configuration", errors.New("USDT transfer client is not configured"))
	}

	transfers, err := watcher.client.ListConfirmedIncomingUSDT(ctx, watcher.config.ReceivingAddress, model.USDTTopUpContractAddress, orders[0].CreatedAt*1000)
	if err != nil {
		category := "unavailable"
		if errors.Is(err, ErrTronGridRateLimited) {
			category = "rate_limited"
		}
		if errors.Is(err, ErrTronGridForbidden) {
			category = "forbidden"
		}
		return watcher.recordUSDTWatcherFailure(now, category, err)
	}

	settled := int64(0)
	unmatched := int64(0)
	manualReview := int64(0)
	for _, transfer := range transfers {
		result, settleErr := model.SettleUSDTTopUp(transfer, now)
		if settleErr == nil {
			if result.Applied {
				settled++
			}
			continue
		}
		if errors.Is(settleErr, model.ErrUSDTTopUpCacheSync) {
			if result != nil && result.Applied {
				settled++
			}
			continue
		}
		if errors.Is(settleErr, model.ErrUSDTTopUpTransferAmbiguous) {
			manualReview++
			continue
		}
		if errors.Is(settleErr, model.ErrUSDTTopUpTransferMismatch) {
			unmatched++
			continue
		}
		return watcher.recordUSDTWatcherFailure(now, "settlement", settleErr)
	}

	remainingOrders, err := model.ListReconcilableUSDTTopUpOrders(reconciliationCutoff)
	if err != nil {
		return watcher.recordUSDTWatcherFailure(now, "database", err)
	}
	pending, confirming := countUSDTWatcherOrders(remainingOrders)
	watcher.stateMu.Lock()
	watcher.state.PendingOrders = pending
	watcher.state.ConfirmingOrders = confirming
	watcher.state.Active = len(remainingOrders) > 0
	watcher.state.Standby = false
	watcher.state.Healthy = true
	watcher.state.LastSuccessfulQueryAt = now
	watcher.state.ConsecutiveFailures = 0
	watcher.state.LastErrorCategory = ""
	watcher.state.SettledTotal += settled
	watcher.state.UnmatchedTotal += unmatched
	watcher.state.ManualReviewTotal += manualReview
	if len(remainingOrders) > 0 {
		watcher.state.NextPollAt = now.Add(watcher.config.PollInterval)
	} else {
		watcher.state.NextPollAt = time.Time{}
	}
	snapshot := watcher.state
	watcher.stateMu.Unlock()
	return snapshot, nil
}

func countUSDTWatcherOrders(orders []model.USDTTopUpOrder) (int64, int64) {
	var pending int64
	var confirming int64
	for _, order := range orders {
		switch order.Status {
		case model.USDTTopUpStatusPending:
			pending++
		case model.USDTTopUpStatusConfirming:
			confirming++
		}
	}
	return pending, confirming
}

func (watcher *USDTWatcher) recordUSDTWatcherFailure(now time.Time, category string, failure error) (USDTWatcherSnapshot, error) {
	watcher.stateMu.Lock()
	watcher.state.ConsecutiveFailures++
	watcher.state.LastErrorCategory = category
	watcher.state.Standby = false
	if category == "forbidden" {
		watcher.state.Healthy = false
		watcher.state.NextPollAt = now.Add(usdtWatcherForbiddenDelay)
	} else {
		backoff := watcher.config.PollInterval
		for i := 0; i < watcher.state.ConsecutiveFailures; i++ {
			if backoff >= usdtWatcherMaxBackoff/2 {
				backoff = usdtWatcherMaxBackoff
				break
			}
			backoff *= 2
		}
		if backoff > usdtWatcherMaxBackoff {
			backoff = usdtWatcherMaxBackoff
		}
		remaining := usdtWatcherMaxBackoff - backoff
		jitterLimit := watcher.config.PollInterval
		if jitterLimit > remaining {
			jitterLimit = remaining
		}
		if jitterLimit > 0 && watcher.backoffJitter != nil {
			backoff += watcher.backoffJitter(jitterLimit)
		}
		watcher.state.NextPollAt = now.Add(backoff)
	}
	snapshot := watcher.state
	watcher.stateMu.Unlock()
	return snapshot, failure
}

func secureUSDTWatcherJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(limit)+1))
	if err != nil {
		return 0
	}
	return time.Duration(value.Int64())
}

var (
	activeUSDTWatcherMu sync.RWMutex
	activeUSDTWatcher   *USDTWatcher
)

func StartUSDTTopUpWatcher() error {
	if !common.IsMasterNode {
		return nil
	}
	config, err := setting.LoadUSDTTopUpConfig()
	if err != nil {
		return err
	}
	if !config.Enabled {
		return nil
	}
	hostname, _ := os.Hostname()
	holderID := strings.TrimSpace(hostname) + ":" + uuid.NewString()
	watcher := NewUSDTWatcher(config, NewTronGridClient("https://api.trongrid.io", config.TronGridAPIKey, nil), holderID)
	activeUSDTWatcherMu.Lock()
	activeUSDTWatcher = watcher
	activeUSDTWatcherMu.Unlock()
	go watcher.Run(context.Background())
	return nil
}

func WakeUSDTWatcher() {
	activeUSDTWatcherMu.RLock()
	watcher := activeUSDTWatcher
	activeUSDTWatcherMu.RUnlock()
	if watcher != nil {
		watcher.Wake()
	}
}

func CurrentUSDTWatcherSnapshot() USDTWatcherSnapshot {
	activeUSDTWatcherMu.RLock()
	watcher := activeUSDTWatcher
	activeUSDTWatcherMu.RUnlock()
	if watcher == nil {
		return USDTWatcherSnapshot{Healthy: true}
	}
	return watcher.Snapshot()
}
