package model

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	mysqldriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	balanceLedgerMySQLTestDSNEnv     = "ZTAPI_BALANCE_LEDGER_MYSQL_TEST_DSN"
	balanceLedgerMySQLTestConfirmEnv = "ZTAPI_BALANCE_LEDGER_MYSQL_TEST_CONFIRM"
)

func TestValidateBalanceLedgerMySQLTestDSN(t *testing.T) {
	tests := []struct {
		name         string
		dsn          string
		confirmation string
		wantDatabase string
		wantError    bool
	}{
		{name: "test suffix", dsn: "user:pass@tcp(localhost:3306)/ztapi_test?charset=utf8mb4", wantDatabase: "ztapi_test"},
		{name: "uppercase test suffix", dsn: "user:pass@tcp(localhost:3306)/ZTAPI_TEST", wantDatabase: "ZTAPI_TEST"},
		{name: "explicit database confirmation", dsn: "user:pass@tcp(localhost:3306)/ledger_ci", confirmation: "ledger_ci", wantDatabase: "ledger_ci"},
		{name: "production database", dsn: "user:pass@tcp(localhost:3306)/ztapi", wantError: true},
		{name: "wrong confirmation", dsn: "user:pass@tcp(localhost:3306)/ztapi", confirmation: "other", wantError: true},
		{name: "missing database", dsn: "user:pass@tcp(localhost:3306)/", wantError: true},
		{name: "invalid DSN", dsn: "not-a-dsn", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			safeDSN, database, err := validateBalanceLedgerMySQLTestDSN(test.dsn, test.confirmation)
			if test.wantError {
				if err == nil {
					t.Fatalf("validation succeeded with database %q and DSN %q", database, safeDSN)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate DSN: %v", err)
			}
			if database != test.wantDatabase {
				t.Fatalf("database = %q, want %q", database, test.wantDatabase)
			}
			if !strings.Contains(strings.ToLower(safeDSN), "parsetime=true") {
				t.Fatalf("safe DSN does not enable parseTime: %q", safeDSN)
			}
		})
	}
}

func TestBalanceLedgerMySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(balanceLedgerMySQLTestDSNEnv))
	if dsn == "" {
		t.Skip(balanceLedgerMySQLTestDSNEnv + " is not configured; MySQL/InnoDB integrity checks were not executed")
	}
	confirmation := strings.TrimSpace(os.Getenv(balanceLedgerMySQLTestConfirmEnv))
	safeDSN, database, err := validateBalanceLedgerMySQLTestDSN(dsn, confirmation)
	if err != nil {
		t.Fatalf("refusing unsafe MySQL integration DSN: %v", err)
	}
	t.Logf("running MySQL/InnoDB integration checks in database %q", database)

	db, err := gorm.Open(gormmysql.Open(safeDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open MySQL test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access MySQL test database: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping MySQL test database: %v", err)
	}
	sqlDB.SetMaxOpenConns(16)

	originalDB := DB
	originalSQLite := common.UsingSQLite
	originalMySQL := common.UsingMySQL
	originalPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	DB = db
	common.UsingSQLite = false
	common.UsingMySQL = true
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	runID := fmt.Sprintf("%x", time.Now().UnixNano())
	usernamePrefix := "bli" + runID
	keyPrefix := "mysql-" + runID + "-"
	t.Cleanup(func() {
		_ = db.Where("trade_no LIKE ?", keyPrefix+"%").Delete(&TopUp{}).Error
		_ = db.Where("request_id LIKE ?", keyPrefix+"%").Delete(&BillingRefundPending{}).Error
		_ = db.Unscoped().Where("key_hash LIKE ?", keyPrefix+"%").Delete(&Token{}).Error
		_ = db.Exec("DELETE FROM balance_ledgers WHERE idempotency_key LIKE ?", keyPrefix+"%").Error
		_ = db.Unscoped().Where("username LIKE ?", usernamePrefix+"%").Delete(&User{}).Error
		_ = sqlDB.Close()
		DB = originalDB
		common.UsingSQLite = originalSQLite
		common.UsingMySQL = originalMySQL
		common.UsingPostgreSQL = originalPostgreSQL
		common.RedisEnabled = originalRedisEnabled
	})

	if err := registerBalanceLedgerImmutability(db); err != nil {
		t.Fatalf("register ledger immutability: %v", err)
	}
	if err := db.AutoMigrate(&User{}, &BalanceLedger{}, &BillingRefundPending{}, &Token{}, &TopUp{}); err != nil {
		t.Fatalf("migrate MySQL integration tables: %v", err)
	}
	for _, table := range []string{"users", "balance_ledgers"} {
		var engine string
		if err := db.Raw("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&engine).Error; err != nil {
			t.Fatalf("inspect %s engine: %v", table, err)
		}
		if !strings.EqualFold(engine, "InnoDB") {
			t.Fatalf("%s engine = %q, want InnoDB", table, engine)
		}
	}
	contention, err := installBalanceLedgerMySQLContentionCallbacks(db, runID)
	if err != nil {
		t.Fatalf("install MySQL contention callbacks: %v", err)
	}
	t.Cleanup(contention.remove)

	t.Run("adjustment waits for held user row lock", func(t *testing.T) {
		user := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"a", 100)
		locker := db.Begin()
		if locker.Error != nil {
			t.Fatalf("begin row-lock transaction: %v", locker.Error)
		}
		committed := false
		defer func() {
			if !committed {
				_ = locker.Rollback().Error
			}
		}()
		var locked User
		if err := locker.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, user.Id).Error; err != nil {
			t.Fatalf("lock user row: %v", err)
		}

		done := make(chan balanceLedgerMySQLResult, 1)
		contention.armUserLockQuery()
		go func() {
			entry, applied, err := AdjustUserBalance(BalanceAdjustment{
				UserID: user.Id, OperatorID: 1, Delta: 40, Reason: "held row lock", IdempotencyKey: keyPrefix + "held-row-lock", SourceType: BalanceLedgerSourceAdmin,
			})
			done <- balanceLedgerMySQLResult{entry: entry, applied: applied, err: err}
		}()
		awaitBalanceLedgerMySQLSignal(t, contention.userLockQuery, "worker locking user query")
		assertBalanceLedgerMySQLBlocked(t, done, "user row lock")
		if err := locker.Commit().Error; err != nil {
			t.Fatalf("release user row lock: %v", err)
		}
		committed = true
		result := awaitBalanceLedgerMySQLResult(t, done, "user row lock release")
		if result.err != nil || !result.applied || result.entry == nil {
			t.Fatalf("row-lock adjustment = %#v", result)
		}
		assertBalanceLedgerMySQLQuota(t, db, user.Id, 140)
	})

	t.Run("duplicate conflict waits and rolls back loser", func(t *testing.T) {
		winner := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"b", 100)
		loser := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"c", 100)
		idempotencyKey := keyPrefix + "held-duplicate"
		winnerTx := db.Begin()
		if winnerTx.Error != nil {
			t.Fatalf("begin winner transaction: %v", winnerTx.Error)
		}
		committed := false
		defer func() {
			if !committed {
				_ = winnerTx.Rollback().Error
			}
		}()
		var lockedWinner User
		if err := winnerTx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedWinner, winner.Id).Error; err != nil {
			t.Fatalf("lock winner user: %v", err)
		}
		if err := winnerTx.Model(&User{}).Where("id = ?", winner.Id).Update("quota", 150).Error; err != nil {
			t.Fatalf("update winner quota: %v", err)
		}
		winnerEntry := BalanceLedger{
			UserID: winner.Id, OperatorID: 1, Delta: 50, BalanceBefore: 100, BalanceAfter: 150,
			Reason: "held duplicate winner", IdempotencyKey: idempotencyKey, SourceType: BalanceLedgerSourceAdmin, CreatedAt: time.Now().UTC(),
		}
		if err := winnerTx.Create(&winnerEntry).Error; err != nil {
			t.Fatalf("insert uncommitted winner ledger: %v", err)
		}

		done := make(chan balanceLedgerMySQLResult, 1)
		contention.armLedgerInsert()
		go func() {
			entry, applied, err := AdjustUserBalance(BalanceAdjustment{
				UserID: loser.Id, OperatorID: 2, Delta: 70, Reason: "held duplicate loser", IdempotencyKey: idempotencyKey, SourceType: BalanceLedgerSourceAdmin,
			})
			done <- balanceLedgerMySQLResult{entry: entry, applied: applied, err: err}
		}()
		awaitBalanceLedgerMySQLSignal(t, contention.ledgerInsert, "worker balance-ledger insert")
		assertBalanceLedgerMySQLBlocked(t, done, "uncommitted unique key")
		if err := winnerTx.Commit().Error; err != nil {
			t.Fatalf("commit winner transaction: %v", err)
		}
		committed = true
		result := awaitBalanceLedgerMySQLResult(t, done, "winner commit")
		if !errors.Is(result.err, ErrBalanceLedgerIdempotencyConflict) || result.applied || result.entry != nil {
			t.Fatalf("duplicate loser result = %#v, want idempotency conflict", result)
		}
		assertBalanceLedgerMySQLQuota(t, db, winner.Id, 150)
		assertBalanceLedgerMySQLQuota(t, db, loser.Id, 100)
		var entries []BalanceLedger
		if err := db.Where("idempotency_key = ?", idempotencyKey).Find(&entries).Error; err != nil {
			t.Fatalf("load duplicate ledger: %v", err)
		}
		if len(entries) != 1 || entries[0].ID != winnerEntry.ID {
			t.Fatalf("duplicate ledger rows = %#v, want only winner %d", entries, winnerEntry.ID)
		}
	})

	t.Run("production int overflow rolls back", func(t *testing.T) {
		user := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"d", int(maxBalanceLedgerQuota))
		entry, applied, err := AdjustUserBalance(BalanceAdjustment{
			UserID: user.Id, OperatorID: 1, Delta: 1, Reason: "overflow", IdempotencyKey: keyPrefix + "overflow", SourceType: BalanceLedgerSourceAdmin,
		})
		if !errors.Is(err, ErrBalanceLedgerOverflow) || entry != nil || applied {
			t.Fatalf("overflow result = entry %#v, applied=%v, err=%v", entry, applied, err)
		}
		assertBalanceLedgerMySQLQuota(t, db, user.Id, int(maxBalanceLedgerQuota))
	})

	t.Run("settlement debit records debt and disables user", func(t *testing.T) {
		user := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"e", 90)
		idempotencyKey := keyPrefix + "usage-settlement"
		entry, applied, err := SettleUserBalanceDebit(user.Id, 490, idempotencyKey, keyPrefix+"settlement-request")
		if err != nil || !applied || entry == nil {
			t.Fatalf("settlement debit = entry %#v, applied=%v, err=%v", entry, applied, err)
		}
		if entry.BalanceBefore != 90 || entry.BalanceAfter != -400 || entry.Delta != -490 {
			t.Fatalf("settlement ledger = %#v, want 90 -> -400", entry)
		}
		var stored User
		if err := db.First(&stored, user.Id).Error; err != nil {
			t.Fatalf("reload settled user: %v", err)
		}
		if stored.Quota != -400 || stored.Status != common.UserStatusDisabled {
			t.Fatalf("settled user quota=%d status=%d, want -400/disabled", stored.Quota, stored.Status)
		}
		repeated, repeatedApplied, err := SettleUserBalanceDebit(user.Id, 490, idempotencyKey, keyPrefix+"settlement-request")
		if err != nil || repeatedApplied || repeated == nil || repeated.ID != entry.ID {
			t.Fatalf("repeated settlement = entry %#v, applied=%v, err=%v", repeated, repeatedApplied, err)
		}
	})

	t.Run("debt can be repaid and repeatedly settled", func(t *testing.T) {
		user := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"h", 100)
		if _, applied, err := SettleUserBalanceDebit(user.Id, 500, keyPrefix+"debt-recovery-settle-1", keyPrefix+"debt-recovery-request-1"); err != nil || !applied {
			t.Fatalf("first debt settlement: applied=%v err=%v", applied, err)
		}
		assertBalanceLedgerMySQLUserState(t, db, user.Id, -400, common.UserStatusDisabled, true)

		if _, applied, err := AdjustUserBalance(BalanceAdjustment{
			UserID: user.Id, OperatorID: 1, Delta: 100, Reason: "partial debt recovery",
			IdempotencyKey: keyPrefix + "debt-recovery-partial", RequestID: keyPrefix + "debt-recovery-partial-request", SourceType: BalanceLedgerSourceAdmin,
		}); err != nil || !applied {
			t.Fatalf("partial debt recovery: applied=%v err=%v", applied, err)
		}
		assertBalanceLedgerMySQLUserState(t, db, user.Id, -300, common.UserStatusDisabled, true)
		if _, err := UpdateZTAPIUserStatus(user.Id, common.RoleRootUser, common.UserStatusEnabled, common.UserStatusDisabled); !errors.Is(err, ErrAdminUserDebtOutstanding) {
			t.Fatalf("premature debt enable error=%v, want outstanding-debt rejection", err)
		}
		assertBalanceLedgerMySQLUserState(t, db, user.Id, -300, common.UserStatusDisabled, true)

		if _, applied, err := SettleUserBalanceDebit(user.Id, 50, keyPrefix+"debt-recovery-settle-2", keyPrefix+"debt-recovery-request-2"); err != nil || !applied {
			t.Fatalf("repeated debt settlement: applied=%v err=%v", applied, err)
		}
		assertBalanceLedgerMySQLUserState(t, db, user.Id, -350, common.UserStatusDisabled, true)

		if _, applied, err := AdjustUserBalance(BalanceAdjustment{
			UserID: user.Id, OperatorID: 1, Delta: 350, Reason: "complete debt recovery",
			IdempotencyKey: keyPrefix + "debt-recovery-complete", RequestID: keyPrefix + "debt-recovery-complete-request", SourceType: BalanceLedgerSourceAdmin,
		}); err != nil || !applied {
			t.Fatalf("complete debt recovery: applied=%v err=%v", applied, err)
		}
		assertBalanceLedgerMySQLUserState(t, db, user.Id, 0, common.UserStatusEnabled, false)
	})

	t.Run("wallet refund restores indebted user", func(t *testing.T) {
		user := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"i", 40)
		if _, applied, err := SettleUserBalanceDebit(user.Id, 90, keyPrefix+"refund-recovery-settle", keyPrefix+"refund-recovery-settle-request"); err != nil || !applied {
			t.Fatalf("refund recovery settlement: applied=%v err=%v", applied, err)
		}
		pending, err := EnsureBillingRefundPending(BillingRefundPending{
			UserID: user.Id, Amount: 75, Kind: BillingRefundKindWallet,
			IdempotencyKey: keyPrefix + "refund-recovery", RequestID: keyPrefix + "refund-recovery-request",
		})
		if err != nil {
			t.Fatalf("create refund recovery: %v", err)
		}
		if err := ProcessBillingRefundPending(pending.ID); err != nil {
			t.Fatalf("process refund recovery: %v", err)
		}
		assertBalanceLedgerMySQLUserState(t, db, user.Id, 25, common.UserStatusEnabled, false)
		var stored BillingRefundPending
		if err := db.First(&stored, pending.ID).Error; err != nil {
			t.Fatalf("reload refund recovery: %v", err)
		}
		if stored.Status != BillingRefundStatusCompleted {
			t.Fatalf("refund recovery status=%q, want completed", stored.Status)
		}
	})

	t.Run("admin topup restores indebted user", func(t *testing.T) {
		user := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"j", 40)
		if _, applied, err := SettleUserBalanceDebit(user.Id, 90, keyPrefix+"topup-recovery-settle", keyPrefix+"topup-recovery-settle-request"); err != nil || !applied {
			t.Fatalf("topup recovery settlement: applied=%v err=%v", applied, err)
		}
		originalQuotaPerUnit := common.QuotaPerUnit
		common.QuotaPerUnit = 100
		t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })
		topUp := TopUp{
			UserId: user.Id, Amount: 1, TradeNo: keyPrefix + "topup-recovery", PaymentMethod: "alipay",
			PaymentProvider: PaymentProviderEpay, Status: common.TopUpStatusPending,
		}
		if err := db.Create(&topUp).Error; err != nil {
			t.Fatalf("create topup recovery: %v", err)
		}
		if _, err := ProcessAdminTopUp(AdminTopUpMutation{
			ID: topUp.Id, OperatorID: 1, ExpectedStatus: common.TopUpStatusPending, TargetStatus: common.TopUpStatusSuccess,
			Reason: "confirm debt recovery topup", RequestID: keyPrefix + "topup-recovery-request",
		}); err != nil {
			t.Fatalf("process topup recovery: %v", err)
		}
		assertBalanceLedgerMySQLUserState(t, db, user.Id, 50, common.UserStatusEnabled, false)
		if err := db.First(&topUp, topUp.Id).Error; err != nil {
			t.Fatalf("reload topup recovery: %v", err)
		}
		if topUp.Status != common.TopUpStatusSuccess {
			t.Fatalf("topup recovery status=%q, want success", topUp.Status)
		}
	})

	t.Run("concurrent wallet refund applies exactly once", func(t *testing.T) {
		user := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"f", 75)
		requestID := keyPrefix + "refund-request"
		pending, err := EnsureBillingRefundPending(BillingRefundPending{
			UserID:         user.Id,
			Amount:         25,
			Kind:           BillingRefundKindWallet,
			IdempotencyKey: keyPrefix + "wallet-refund",
			RequestID:      requestID,
		})
		if err != nil {
			t.Fatalf("create pending wallet refund: %v", err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				results <- ProcessBillingRefundPending(pending.ID)
			}()
		}
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatalf("process concurrent wallet refund: %v", err)
			}
		}
		assertBalanceLedgerMySQLQuota(t, db, user.Id, 100)
		var ledgerCount int64
		if err := db.Model(&BalanceLedger{}).Where("idempotency_key = ?", pending.IdempotencyKey).Count(&ledgerCount).Error; err != nil {
			t.Fatalf("count wallet refund ledgers: %v", err)
		}
		if ledgerCount != 1 {
			t.Fatalf("wallet refund ledger rows=%d, want 1", ledgerCount)
		}
		var stored BillingRefundPending
		if err := db.First(&stored, pending.ID).Error; err != nil {
			t.Fatalf("reload wallet refund: %v", err)
		}
		if stored.Status != BillingRefundStatusCompleted {
			t.Fatalf("wallet refund status=%q, want completed", stored.Status)
		}
	})

	t.Run("concurrent token exhaustion consumes remaining quota once", func(t *testing.T) {
		user := createBalanceLedgerMySQLUser(t, db, usernamePrefix+"g", 100)
		keyHash := keyPrefix + "token-exhaust"
		token := Token{
			UserId:      user.Id,
			KeyHash:     keyHash,
			KeyPrefix:   "sk-test",
			Name:        "MySQL exhaustion",
			Status:      common.TokenStatusEnabled,
			RemainQuota: 50,
			UsedQuota:   10,
		}
		if err := db.Create(&token).Error; err != nil {
			t.Fatalf("create finite token: %v", err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				results <- ExhaustTokenQuota(token.Id, keyHash)
			}()
		}
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatalf("exhaust token concurrently: %v", err)
			}
		}
		var stored Token
		if err := db.First(&stored, token.Id).Error; err != nil {
			t.Fatalf("reload exhausted token: %v", err)
		}
		if stored.RemainQuota != 0 || stored.UsedQuota != 60 || stored.Status != common.TokenStatusExhausted {
			t.Fatalf("exhausted token remain=%d used=%d status=%d, want 0/60/exhausted", stored.RemainQuota, stored.UsedQuota, stored.Status)
		}
	})
}

func validateBalanceLedgerMySQLTestDSN(dsn string, confirmation string) (string, string, error) {
	config, err := mysqldriver.ParseDSN(strings.TrimSpace(dsn))
	if err != nil {
		return "", "", fmt.Errorf("parse DSN: %w", err)
	}
	database := strings.TrimSpace(config.DBName)
	if database == "" {
		return "", "", errors.New("DSN must select a database")
	}
	testSuffix := strings.HasSuffix(strings.ToLower(database), "_test")
	explicitlyConfirmed := confirmation != "" && confirmation == database
	if !testSuffix && !explicitlyConfirmed {
		return "", database, fmt.Errorf("database %q is not test-only; use a _test suffix or set %s to the exact database name", database, balanceLedgerMySQLTestConfirmEnv)
	}
	config.ParseTime = true
	return config.FormatDSN(), database, nil
}

type balanceLedgerMySQLResult struct {
	entry   *BalanceLedger
	applied bool
	err     error
}

type balanceLedgerMySQLContentionCallbacks struct {
	db                   *gorm.DB
	userLockCallbackName string
	ledgerCallbackName   string
	userLockArmed        atomic.Bool
	ledgerInsertArmed    atomic.Bool
	userLockQuery        chan struct{}
	ledgerInsert         chan struct{}
}

func installBalanceLedgerMySQLContentionCallbacks(db *gorm.DB, runID string) (*balanceLedgerMySQLContentionCallbacks, error) {
	callbacks := &balanceLedgerMySQLContentionCallbacks{
		db:                   db,
		userLockCallbackName: "balance_ledger_mysql_test:before_user_lock_" + runID,
		ledgerCallbackName:   "balance_ledger_mysql_test:before_ledger_insert_" + runID,
		userLockQuery:        make(chan struct{}, 1),
		ledgerInsert:         make(chan struct{}, 1),
	}
	if err := db.Callback().Query().Before("gorm:query").Register(callbacks.userLockCallbackName, func(tx *gorm.DB) {
		if !callbacks.userLockArmed.Load() || !balanceLedgerMySQLStatementTargetsTable(tx, "users") {
			return
		}
		if _, locking := tx.Statement.Clauses["FOR"]; !locking {
			return
		}
		if callbacks.userLockArmed.CompareAndSwap(true, false) {
			callbacks.userLockQuery <- struct{}{}
		}
	}); err != nil {
		return nil, err
	}
	if err := db.Callback().Create().Before("gorm:create").Register(callbacks.ledgerCallbackName, func(tx *gorm.DB) {
		if balanceLedgerMySQLStatementTargetsTable(tx, "balance_ledgers") && callbacks.ledgerInsertArmed.CompareAndSwap(true, false) {
			callbacks.ledgerInsert <- struct{}{}
		}
	}); err != nil {
		_ = db.Callback().Query().Remove(callbacks.userLockCallbackName)
		return nil, err
	}
	return callbacks, nil
}

func (callbacks *balanceLedgerMySQLContentionCallbacks) armUserLockQuery() {
	callbacks.userLockArmed.Store(true)
}

func (callbacks *balanceLedgerMySQLContentionCallbacks) armLedgerInsert() {
	callbacks.ledgerInsertArmed.Store(true)
}

func (callbacks *balanceLedgerMySQLContentionCallbacks) remove() {
	callbacks.userLockArmed.Store(false)
	callbacks.ledgerInsertArmed.Store(false)
	_ = callbacks.db.Callback().Query().Remove(callbacks.userLockCallbackName)
	_ = callbacks.db.Callback().Create().Remove(callbacks.ledgerCallbackName)
}

func balanceLedgerMySQLStatementTargetsTable(tx *gorm.DB, table string) bool {
	return tx.Statement.Table == table || tx.Statement.Schema != nil && tx.Statement.Schema.Table == table
}

func awaitBalanceLedgerMySQLSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatalf("test callback did not observe %s", operation)
	}
}

func assertBalanceLedgerMySQLBlocked(t *testing.T, done <-chan balanceLedgerMySQLResult, blockedOn string) {
	t.Helper()
	select {
	case result := <-done:
		t.Fatalf("adjustment completed before %s was released: %#v", blockedOn, result)
	case <-time.After(250 * time.Millisecond):
	}
}

func awaitBalanceLedgerMySQLResult(t *testing.T, done <-chan balanceLedgerMySQLResult, after string) balanceLedgerMySQLResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(10 * time.Second):
		t.Fatalf("adjustment remained blocked after %s", after)
		return balanceLedgerMySQLResult{}
	}
}

func createBalanceLedgerMySQLUser(t *testing.T, db *gorm.DB, username string, quota int) User {
	t.Helper()
	user := User{
		Username: username,
		Password: "not-used",
		AffCode:  username + "-aff",
		Quota:    quota,
		Status:   common.UserStatusEnabled,
		Role:     common.RoleCommonUser,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create MySQL integration user: %v", err)
	}
	return user
}

func assertBalanceLedgerMySQLQuota(t *testing.T, db *gorm.DB, userID int, want int) {
	t.Helper()
	var user User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatalf("reload MySQL integration user: %v", err)
	}
	if user.Quota != want {
		t.Fatalf("user %d quota = %d, want %d", userID, user.Quota, want)
	}
}

func assertBalanceLedgerMySQLUserState(t *testing.T, db *gorm.DB, userID, wantQuota, wantStatus int, wantDebtSuspended bool) {
	t.Helper()
	var user User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatalf("reload MySQL integration user state: %v", err)
	}
	if user.Quota != wantQuota || user.Status != wantStatus || user.DebtSuspended != wantDebtSuspended {
		t.Fatalf("user %d quota/status/debt_suspended=%d/%d/%v, want %d/%d/%v",
			userID, user.Quota, user.Status, user.DebtSuspended, wantQuota, wantStatus, wantDebtSuspended)
	}
}
