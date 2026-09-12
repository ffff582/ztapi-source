package service

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUSDTWatcherNeverQueriesTronGridWhileIdleOrBeyondConfirmationGrace(t *testing.T) {
	db, config := setupUSDTWatcherTest(t)
	now := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	client := &fakeUSDTTransferClient{}
	watcher := NewUSDTWatcher(config, client, "idle-watcher")

	snapshot, err := watcher.pollOnce(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, int64(0), snapshot.PendingOrders)
	require.Equal(t, 0, client.callCount())

	user := createUSDTWatcherUser(t, db, "watch-expired")
	order, err := model.CreateUSDTTopUpOrder(user.Id, 10, now.Add(-13*time.Minute), config)
	require.NoError(t, err)
	snapshot, err = watcher.pollOnce(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, int64(0), snapshot.PendingOrders)
	require.Equal(t, 0, client.callCount())
	stored, err := model.GetUserUSDTTopUpOrder(user.Id, order.TradeNo)
	require.NoError(t, err)
	require.Equal(t, model.USDTTopUpStatusExpired, stored.Status)
}

func TestUSDTWatcherSettlesInWindowTransferConfirmedDuringGrace(t *testing.T) {
	db, config := setupUSDTWatcherTest(t)
	createdAt := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	user := createUSDTWatcherUser(t, db, "watch-confirmation-grace")
	order, err := model.CreateUSDTTopUpOrder(user.Id, 10, createdAt, config)
	require.NoError(t, err)
	client := &fakeUSDTTransferClient{transfers: []model.TRC20Transfer{
		watcherTransfer(order, "watcher-tx-confirmation-grace", createdAt.Add(9*time.Minute+59*time.Second)),
	}}
	watcher := NewUSDTWatcher(config, client, "confirmation-grace-watcher")

	snapshot, err := watcher.pollOnce(context.Background(), createdAt.Add(10*time.Minute+30*time.Second))

	require.NoError(t, err)
	require.Equal(t, 1, client.callCount())
	require.Equal(t, int64(1), snapshot.SettledTotal)
	require.Equal(t, int64(0), snapshot.PendingOrders)
	require.Equal(t, int64(0), snapshot.ConfirmingOrders)
	stored, err := model.GetUserUSDTTopUpOrder(user.Id, order.TradeNo)
	require.NoError(t, err)
	require.Equal(t, model.USDTTopUpStatusSettled, stored.Status)
	var reloaded model.User
	require.NoError(t, db.First(&reloaded, user.Id).Error)
	require.Greater(t, reloaded.Quota, 0)
}

func TestUSDTWatcherRejectsPostExpiryTransferThenStopsAfterGrace(t *testing.T) {
	db, config := setupUSDTWatcherTest(t)
	createdAt := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	user := createUSDTWatcherUser(t, db, "watch-post-expiry")
	order, err := model.CreateUSDTTopUpOrder(user.Id, 10, createdAt, config)
	require.NoError(t, err)
	client := &fakeUSDTTransferClient{transfers: []model.TRC20Transfer{
		watcherTransfer(order, "watcher-tx-post-expiry", createdAt.Add(10*time.Minute+time.Second)),
	}}
	watcher := NewUSDTWatcher(config, client, "post-expiry-watcher")

	duringGrace, err := watcher.pollOnce(context.Background(), createdAt.Add(10*time.Minute+30*time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, client.callCount())
	require.Equal(t, int64(0), duringGrace.SettledTotal)
	require.Equal(t, int64(0), duringGrace.PendingOrders)
	require.Equal(t, int64(1), duringGrace.ConfirmingOrders)
	var unchanged model.User
	require.NoError(t, db.First(&unchanged, user.Id).Error)
	require.Zero(t, unchanged.Quota)
	storedDuringGrace, err := model.GetUserUSDTTopUpOrder(user.Id, order.TradeNo)
	require.NoError(t, err)
	require.Equal(t, model.USDTTopUpStatusConfirming, storedDuringGrace.Status)

	afterGrace, err := watcher.pollOnce(context.Background(), createdAt.Add(12*time.Minute+time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, client.callCount())
	require.Equal(t, int64(0), afterGrace.PendingOrders)
	require.Equal(t, int64(0), afterGrace.ConfirmingOrders)
	stored, err := model.GetUserUSDTTopUpOrder(user.Id, order.TradeNo)
	require.NoError(t, err)
	require.Equal(t, model.USDTTopUpStatusExpired, stored.Status)
}

func TestUSDTWatcherSettlesAllMatchingPendingOrdersFromOneWalletQuery(t *testing.T) {
	db, config := setupUSDTWatcherTest(t)
	now := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	firstUser := createUSDTWatcherUser(t, db, "watch-first")
	secondUser := createUSDTWatcherUser(t, db, "watch-second")
	first, err := model.CreateUSDTTopUpOrder(firstUser.Id, 10, now, config)
	require.NoError(t, err)
	second, err := model.CreateUSDTTopUpOrder(secondUser.Id, 11, now, config)
	require.NoError(t, err)
	client := &fakeUSDTTransferClient{transfers: []model.TRC20Transfer{
		watcherTransfer(first, "watcher-tx-first", now.Add(time.Second)),
		watcherTransfer(second, "watcher-tx-second", now.Add(2*time.Second)),
	}}
	watcher := NewUSDTWatcher(config, client, "settlement-watcher")

	snapshot, err := watcher.pollOnce(context.Background(), now.Add(3*time.Second))

	require.NoError(t, err)
	require.Equal(t, 1, client.callCount())
	require.Equal(t, int64(0), snapshot.PendingOrders)
	require.Equal(t, int64(2), snapshot.SettledTotal)
	for _, user := range []model.User{firstUser, secondUser} {
		var reloaded model.User
		require.NoError(t, db.First(&reloaded, user.Id).Error)
		require.Greater(t, reloaded.Quota, 0)
	}
}

func TestUSDTWatcherUsesConfiguredCadenceAndRateLimitBackoff(t *testing.T) {
	db, config := setupUSDTWatcherTest(t)
	now := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	user := createUSDTWatcherUser(t, db, "watch-cadence")
	_, err := model.CreateUSDTTopUpOrder(user.Id, 10, now, config)
	require.NoError(t, err)
	client := &fakeUSDTTransferClient{errors: []error{nil, ErrTronGridRateLimited, nil}}
	watcher := NewUSDTWatcher(config, client, "cadence-watcher")
	watcher.backoffJitter = func(time.Duration) time.Duration { return 0 }

	first, err := watcher.pollOnce(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, now.Add(5*time.Second), first.NextPollAt)
	require.Equal(t, 1, client.callCount())

	_, err = watcher.pollOnce(context.Background(), now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, client.callCount())

	rateLimited, err := watcher.pollOnce(context.Background(), now.Add(5*time.Second))
	require.ErrorIs(t, err, ErrTronGridRateLimited)
	require.Equal(t, 2, client.callCount())
	require.Equal(t, 1, rateLimited.ConsecutiveFailures)
	require.Equal(t, now.Add(15*time.Second), rateLimited.NextPollAt)

	_, err = watcher.pollOnce(context.Background(), now.Add(14*time.Second))
	require.NoError(t, err)
	require.Equal(t, 2, client.callCount())
	_, err = watcher.pollOnce(context.Background(), now.Add(15*time.Second))
	require.NoError(t, err)
	require.Equal(t, 3, client.callCount())
}

func TestUSDTWatcherForbiddenStateAndDatabaseLeasePreventTightRetries(t *testing.T) {
	db, config := setupUSDTWatcherTest(t)
	now := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	user := createUSDTWatcherUser(t, db, "watch-lease")
	_, err := model.CreateUSDTTopUpOrder(user.Id, 10, now, config)
	require.NoError(t, err)
	firstClient := &fakeUSDTTransferClient{errors: []error{ErrTronGridForbidden}}
	firstWatcher := NewUSDTWatcher(config, firstClient, "lease-holder-one")

	forbidden, err := firstWatcher.pollOnce(context.Background(), now)
	require.ErrorIs(t, err, ErrTronGridForbidden)
	require.False(t, forbidden.Healthy)
	require.Equal(t, "forbidden", forbidden.LastErrorCategory)
	require.Equal(t, 1, firstClient.callCount())
	_, err = firstWatcher.pollOnce(context.Background(), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, firstClient.callCount())

	secondClient := &fakeUSDTTransferClient{}
	secondWatcher := NewUSDTWatcher(config, secondClient, "lease-holder-two")
	standby, err := secondWatcher.pollOnce(context.Background(), now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, standby.Standby)
	require.Equal(t, 0, secondClient.callCount())
}

func TestUSDTWatcherWakeIsNonBlockingAndCoalesced(t *testing.T) {
	_, config := setupUSDTWatcherTest(t)
	watcher := NewUSDTWatcher(config, &fakeUSDTTransferClient{}, "wake-watcher")

	watcher.Wake()
	watcher.Wake()

	require.Len(t, watcher.wake, 1)
}

type fakeUSDTTransferClient struct {
	mu        sync.Mutex
	calls     int
	transfers []model.TRC20Transfer
	errors    []error
}

func (client *fakeUSDTTransferClient) ListConfirmedIncomingUSDT(context.Context, string, string, int64) ([]model.TRC20Transfer, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.calls++
	var err error
	if len(client.errors) > 0 {
		err = client.errors[0]
		client.errors = client.errors[1:]
	}
	if err != nil {
		return nil, err
	}
	return append([]model.TRC20Transfer(nil), client.transfers...), nil
}

func (client *fakeUSDTTransferClient) callCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.calls
}

func setupUSDTWatcherTest(t *testing.T) (*gorm.DB, setting.USDTTopUpConfig) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "usdt-watcher.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.BalanceLedger{}, &model.TopUp{}, &model.USDTTopUpOrder{},
		&model.USDTTopUpAmountLock{}, &model.USDTWatcherLease{},
	))
	previousDB := model.DB
	previousSQLite := common.UsingSQLite
	previousMySQL := common.UsingMySQL
	previousPostgres := common.UsingPostgreSQL
	previousRedis := common.RedisEnabled
	model.DB = db
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		common.UsingSQLite = previousSQLite
		common.UsingMySQL = previousMySQL
		common.UsingPostgreSQL = previousPostgres
		common.RedisEnabled = previousRedis
		_ = sqlDB.Close()
	})
	return db, setting.USDTTopUpConfig{
		Enabled: true, ReceivingAddress: testTronWallet, TronGridAPIKey: "test-only-key", MinTopUp: 10,
		OrderTTL: 10 * time.Minute, PollInterval: 5 * time.Second, SuffixCooldown: 24 * time.Hour,
	}
}

func createUSDTWatcherUser(t *testing.T, db *gorm.DB, username string) model.User {
	t.Helper()
	user := model.User{Username: username, Password: "not-used", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: username + "-aff"}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func watcherTransfer(order *model.USDTTopUpOrder, txID string, timestamp time.Time) model.TRC20Transfer {
	return model.TRC20Transfer{
		TxID: txID, From: "TSender1111111111111111111111111111", To: order.ReceivingAddress,
		ContractAddress: order.ContractAddress, AmountMicros: order.PayAmountMicros, BlockTimestampMS: timestamp.UnixMilli(),
	}
}
