package model

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type usdtSettledReplayFixture struct {
	User   User
	Order  USDTTopUpOrder
	TopUp  TopUp
	Ledger BalanceLedger
}

// Deliberately invalid chain identifiers keep this fixture unrelated to customer data.
func syntheticUSDTSettledReplayFixture() usdtSettledReplayFixture {
	const createdAt int64 = 1_700_000_000
	const tradeNo = "synthetic-settled-replay-order"
	txID, sender := "synthetic-settled-replay-tx", "synthetic-sender-not-a-wallet"
	settledAt, blockTimestamp := createdAt+60, (createdAt+30)*1000
	return usdtSettledReplayFixture{
		User: User{Id: 101, Username: "usdt-replay-fixture", Password: "not-used-fixture",
			Quota: 4_900_000, UsedQuota: 100_000, RequestCount: 1, Group: "default", CreatedAt: createdAt},
		Order: USDTTopUpOrder{ID: 201, TopUpID: 301, UserID: 101, TradeNo: tradeNo,
			Network: USDTTopUpNetworkTronMainnet, Asset: "USDT",
			ContractAddress: "synthetic-contract", ReceivingAddress: "synthetic-receiver-not-a-wallet",
			CreditUnits: 10, PayAmountMicros: 10_570_000, SuffixCents: 57, Status: USDTTopUpStatusSettled,
			ExpiresAt: createdAt + 1800, CooldownUntil: createdAt + 3600,
			TxID: &txID, TxFrom: &sender, BlockTimestampMS: &blockTimestamp, SettledAt: &settledAt,
			CreatedAt: createdAt, UpdatedAt: settledAt},
		TopUp: TopUp{Id: 301, UserId: 101, Amount: 10, Money: 10.57, TradeNo: tradeNo,
			PaymentMethod: PaymentMethodUSDTTRC20, PaymentProvider: PaymentProviderUSDTTRC20,
			CreateTime: createdAt, CompleteTime: settledAt, Status: common.TopUpStatusSuccess},
		Ledger: BalanceLedger{ID: 401, UserID: 101, Delta: 5_000_000, BalanceBefore: 0,
			BalanceAfter: 5_000_000, Reason: "synthetic local fixture topup",
			IdempotencyKey: "usdt-trc20:" + txID, RequestID: tradeNo,
			SourceType: BalanceLedgerSourceUSDTTopUp, CreatedAt: time.Unix(settledAt, 0).UTC()},
	}
}

// This regression uses a dedicated local database, never a production DSN.
func TestUSDTAlreadySettledReplayMySQL(t *testing.T) {
	dsn := os.Getenv("ZTAPI_USDT_REPLAY_MYSQL_TEST_DSN")
	if dsn == "" {
		if os.Getenv("ZTAPI_REQUIRE_USDT_REPLAY_TEST") == "true" {
			t.Fatal("local replay MySQL DSN is required")
		}
		t.Skip("isolated MySQL replay test was not configured")
	}
	config, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(config.Addr)
	require.NoError(t, err)
	require.Equal(t, "tcp", config.Net)
	require.Equal(t, "127.0.0.1", host, "only the loopback test container is permitted")
	require.Equal(t, "ztapi_usdt_replay_test", config.DBName)
	safeDSN, _, err := validateBalanceLedgerMySQLTestDSN(dsn, "")
	require.NoError(t, err)

	fixture := syntheticUSDTSettledReplayFixture()
	require.Equal(t, USDTTopUpStatusSettled, fixture.Order.Status)
	require.Equal(t, common.TopUpStatusSuccess, fixture.TopUp.Status)
	require.Equal(t, int64(10_570_000), fixture.Order.PayAmountMicros)
	require.Equal(t, int64(5_000_000), fixture.Ledger.Delta)
	require.Equal(t, 4_900_000, fixture.User.Quota, "fixture must model post-credit consumption")
	require.Equal(t, fixture.User.Id, fixture.Order.UserID)
	require.Equal(t, fixture.User.Id, fixture.TopUp.UserId)
	require.Equal(t, fixture.User.Id, fixture.Ledger.UserID)
	require.Equal(t, fixture.TopUp.Id, fixture.Order.TopUpID)
	require.Equal(t, fixture.TopUp.TradeNo, fixture.Order.TradeNo)
	require.Equal(t, fixture.Order.TradeNo, fixture.Ledger.RequestID)
	require.NotNil(t, fixture.Order.TxID)
	require.NotNil(t, fixture.Order.TxFrom)
	require.NotNil(t, fixture.Order.BlockTimestampMS)
	require.NotNil(t, fixture.Order.SettledAt)
	require.Equal(t, "usdt-trc20:"+*fixture.Order.TxID, fixture.Ledger.IdempotencyKey)

	db, err := gorm.Open(mysql.Open(safeDSN), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	oldDB, oldSQLite, oldMySQL, oldPostgres, oldRedis := DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL, common.RedisEnabled
	t.Cleanup(func() {
		DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL, common.RedisEnabled = oldDB, oldSQLite, oldMySQL, oldPostgres, oldRedis
		_ = sqlDB.Close()
	})
	DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL, common.RedisEnabled = db, false, true, false, false
	require.NoError(t, db.AutoMigrate(&User{}, &BalanceLedger{}, &TopUp{}, &USDTTopUpOrder{}))
	require.NoError(t, registerBalanceLedgerImmutability(db))
	for _, table := range []string{"users", "balance_ledgers", "top_ups", "usdt_top_up_orders"} {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		require.Zero(t, count, "fixture database must be empty; no existing rows will be deleted")
		var engine string
		require.NoError(t, db.Raw("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&engine).Error)
		require.Equal(t, "InnoDB", engine)
	}
	for _, row := range []any{&fixture.User, &fixture.TopUp, &fixture.Order, &fixture.Ledger} {
		require.NoError(t, db.Create(row).Error)
	}
	// Keep the post-topup consumption scenario consistent with the append-only ledger.
	consumption := BalanceLedger{
		UserID: fixture.User.Id, Delta: -100_000, BalanceBefore: fixture.Ledger.BalanceAfter,
		BalanceAfter: int64(fixture.User.Quota), Reason: "synthetic local fixture consumption",
		IdempotencyKey: "fixture-replay-consumption", RequestID: "fixture-replay-consumption",
		SourceType: BalanceLedgerSourceUsageSettlement, CreatedAt: fixture.Ledger.CreatedAt.Add(time.Second),
	}
	require.NoError(t, db.Create(&consumption).Error)

	type financialState struct {
		User    User
		Order   USDTTopUpOrder
		TopUp   TopUp
		Ledgers []BalanceLedger
		Counts  []int64
	}
	readState := func() financialState {
		var state financialState
		require.NoError(t, db.First(&state.User, fixture.User.Id).Error)
		require.NoError(t, db.First(&state.Order, fixture.Order.ID).Error)
		require.NoError(t, db.First(&state.TopUp, fixture.TopUp.Id).Error)
		require.NoError(t, db.Order("id").Find(&state.Ledgers).Error)
		for _, table := range []string{"users", "balance_ledgers", "top_ups", "usdt_top_up_orders"} {
			var count int64
			require.NoError(t, db.Table(table).Count(&count).Error)
			state.Counts = append(state.Counts, count)
		}
		return state
	}
	before := readState()
	require.Len(t, before.Ledgers, 2)
	require.Equal(t, int64(before.User.Quota), before.Ledgers[1].BalanceAfter)
	var attemptedWrites atomic.Int32
	denyMutation := func(tx *gorm.DB) {
		attemptedWrites.Add(1)
		tx.AddError(errors.New("unexpected write in settled replay regression"))
	}
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("replay_test:no_create", denyMutation))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("replay_test:no_update", denyMutation))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("replay_test:no_delete", denyMutation))
	transfer := TRC20Transfer{TxID: *fixture.Order.TxID, From: *fixture.Order.TxFrom, To: fixture.Order.ReceivingAddress,
		ContractAddress: fixture.Order.ContractAddress, AmountMicros: fixture.Order.PayAmountMicros, BlockTimestampMS: *fixture.Order.BlockTimestampMS}
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := SettleUSDTTopUp(transfer, time.Unix(*fixture.Order.SettledAt+int64(attempt), 0))
		require.NoError(t, err)
		require.NotNil(t, result)
		require.False(t, result.Applied)
		require.Equal(t, fixture.Ledger.ID, result.Ledger.ID)
		require.Equal(t, int64(5_000_000), result.QuotaAdded, "historical amount, not new credit")
		require.Zero(t, attemptedWrites.Load(), "even a swallowed write error must fail this test")
		require.Equal(t, before, readState())
		t.Logf("replay %d: applied=false historical_quota=5000000 balance=%d ledger_count=%d writes=0", attempt, before.User.Quota, len(before.Ledgers))
	}
	transfer.AmountMicros++
	_, err = SettleUSDTTopUp(transfer, time.Now())
	require.ErrorIs(t, err, ErrUSDTTopUpTransferMismatch)
	require.Zero(t, attemptedWrites.Load())
	require.Equal(t, before, readState())
	var version string
	require.NoError(t, db.Raw("SELECT VERSION()").Scan(&version).Error)
	require.True(t, strings.HasPrefix(version, "8.4."))
	t.Logf("MySQL %s; mismatched transfer rejected; all financial rows unchanged", version)
}
