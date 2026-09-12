package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	mysqldriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	walletLedgerMySQLTestDSNEnv     = "ZTAPI_BALANCE_LEDGER_MYSQL_TEST_DSN"
	walletLedgerMySQLTestConfirmEnv = "ZTAPI_BALANCE_LEDGER_MYSQL_TEST_CONFIRM"
)

func TestWalletFundingLedgerMySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(walletLedgerMySQLTestDSNEnv))
	if dsn == "" {
		t.Skip(walletLedgerMySQLTestDSNEnv + " is not configured; wallet lifecycle MySQL checks were not executed")
	}
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse MySQL integration DSN: %v", err)
	}
	database := strings.TrimSpace(config.DBName)
	confirmation := strings.TrimSpace(os.Getenv(walletLedgerMySQLTestConfirmEnv))
	if database == "" || (!strings.HasSuffix(strings.ToLower(database), "_test") && confirmation != database) {
		t.Fatalf("refusing non-test MySQL database %q", database)
	}
	config.ParseTime = true

	db, err := gorm.Open(gormmysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open MySQL integration database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access MySQL integration database: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping MySQL integration database: %v", err)
	}
	sqlDB.SetMaxOpenConns(24)

	originalDB := model.DB
	originalSQLite := common.UsingSQLite
	originalMySQL := common.UsingMySQL
	originalPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalBatchUpdateEnabled := common.BatchUpdateEnabled
	model.DB = db
	common.UsingSQLite = false
	common.UsingMySQL = true
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false

	runID := fmt.Sprintf("%x", time.Now().UnixNano())
	usernamePrefix := "wli" + runID
	requestPrefix := "wallet-mysql-" + runID + "-"
	t.Cleanup(func() {
		_ = db.Where("request_id LIKE ?", requestPrefix+"%").Delete(&model.BillingRefundPending{}).Error
		_ = db.Exec("DELETE FROM balance_ledgers WHERE request_id LIKE ?", requestPrefix+"%").Error
		_ = db.Unscoped().Where("username LIKE ?", usernamePrefix+"%").Delete(&model.User{}).Error
		_ = sqlDB.Close()
		model.DB = originalDB
		common.UsingSQLite = originalSQLite
		common.UsingMySQL = originalMySQL
		common.UsingPostgreSQL = originalPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.BatchUpdateEnabled = originalBatchUpdateEnabled
	})

	if err := db.AutoMigrate(&model.User{}, &model.BalanceLedger{}, &model.BillingRefundPending{}); err != nil {
		t.Fatalf("migrate MySQL wallet ledger tables: %v", err)
	}
	for _, table := range []string{"users", "balance_ledgers", "billing_refund_pendings"} {
		var engine string
		if err := db.Raw("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&engine).Error; err != nil {
			t.Fatalf("inspect %s engine: %v", table, err)
		}
		if !strings.EqualFold(engine, "InnoDB") {
			t.Fatalf("%s engine=%q, want InnoDB", table, engine)
		}
	}

	t.Run("lifecycle reconciliation", func(t *testing.T) {
		tests := []struct {
			name          string
			preConsumed   int
			actual        int
			refund        bool
			wantBalance   int
			wantLedgerSum int64
			wantRows      int
		}{
			{name: "preconsume exceeds actual", preConsumed: 80, actual: 30, wantBalance: 70, wantLedgerSum: -30, wantRows: 2},
			{name: "preconsume below actual", preConsumed: 40, actual: 80, wantBalance: 20, wantLedgerSum: -80, wantRows: 2},
			{name: "preconsume equals actual", preConsumed: 50, actual: 50, wantBalance: 50, wantLedgerSum: -50, wantRows: 1},
			{name: "failed request refunds full reservation", preConsumed: 60, refund: true, wantBalance: 100, wantLedgerSum: 0, wantRows: 2},
		}
		for index, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				user := createWalletLedgerMySQLUser(t, db, fmt.Sprintf("%s%d", usernamePrefix, index), 100)
				requestID := fmt.Sprintf("%s%d", requestPrefix, index)
				funding := &WalletFunding{
					operationID: "operation-" + requestID,
					requestId:   requestID,
					userId:      user.Id,
				}
				if err := funding.PreConsume(test.preConsumed); err != nil {
					t.Fatalf("preconsume MySQL wallet: %v", err)
				}
				if test.refund {
					if err := funding.Refund(); err != nil {
						t.Fatalf("refund MySQL wallet: %v", err)
					}
				} else if err := funding.Settle(test.actual - test.preConsumed); err != nil {
					t.Fatalf("settle MySQL wallet: %v", err)
				}
				assertWalletLedgerReconciliation(t, db, user.Id, requestID, test.wantBalance, test.wantLedgerSum, test.wantRows)
			})
		}
	})

	t.Run("progressive reserve reconciliation", func(t *testing.T) {
		user := createWalletLedgerMySQLUser(t, db, usernamePrefix+"reserve", 100)
		requestID := requestPrefix + "reserve"
		operationID := "operation-" + requestID
		session := &BillingSession{
			operationID: operationID,
			relayInfo: &relaycommon.RelayInfo{
				UserId:         user.Id,
				RequestId:      requestID,
				TokenUnlimited: true,
			},
			funding: &WalletFunding{
				operationID: operationID,
				requestId:   requestID,
				userId:      user.Id,
			},
		}
		if err := session.Reserve(25); err != nil {
			t.Fatalf("progressively reserve MySQL wallet: %v", err)
		}
		assertWalletLedgerReconciliation(t, db, user.Id, requestID, 75, -25, 1)
	})

	t.Run("concurrent same-user reservations", func(t *testing.T) {
		const (
			workers = 20
			amount  = 10
		)
		user := createWalletLedgerMySQLUser(t, db, usernamePrefix+"concurrent", 100)
		start := make(chan struct{})
		results := make(chan error, workers)
		var wait sync.WaitGroup
		for index := 0; index < workers; index++ {
			index := index
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				requestID := fmt.Sprintf("%sconcurrent-%d", requestPrefix, index)
				funding := &WalletFunding{
					operationID: "operation-" + requestID,
					requestId:   requestID,
					userId:      user.Id,
				}
				results <- funding.PreConsume(amount)
			}()
		}
		close(start)
		wait.Wait()
		close(results)

		successes := 0
		insufficient := 0
		for err := range results {
			switch {
			case err == nil:
				successes++
			case errors.Is(err, model.ErrInsufficientUserQuota):
				insufficient++
			default:
				t.Fatalf("unexpected MySQL concurrent reservation error: %v", err)
			}
		}
		if successes != 10 || insufficient != 10 {
			t.Fatalf("MySQL concurrent success/insufficient=%d/%d, want 10/10", successes, insufficient)
		}
		var stored model.User
		if err := db.First(&stored, user.Id).Error; err != nil {
			t.Fatalf("reload MySQL concurrent wallet: %v", err)
		}
		var count int64
		var sum int64
		if err := db.Model(&model.BalanceLedger{}).
			Where("user_id = ? AND request_id LIKE ? AND source_type = ?", user.Id, requestPrefix+"concurrent-%", model.BalanceLedgerSourceUsageReservation).
			Count(&count).Select("COALESCE(SUM(delta), 0)").Scan(&sum).Error; err != nil {
			t.Fatalf("summarize MySQL concurrent reservation ledger: %v", err)
		}
		if stored.Quota != 0 || count != 10 || sum != -100 {
			t.Fatalf("MySQL concurrent quota/rows/sum=%d/%d/%d, want 0/10/-100", stored.Quota, count, sum)
		}
	})
}

func createWalletLedgerMySQLUser(t *testing.T, db *gorm.DB, username string, quota int) model.User {
	t.Helper()
	affCode := username
	if len(affCode) > 20 {
		affCode = affCode[:20]
	}
	user := model.User{
		Username: username,
		Password: "not-used",
		AffCode:  "a-" + affCode,
		Quota:    quota,
		Status:   common.UserStatusEnabled,
		Role:     common.RoleCommonUser,
		Group:    "default",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create MySQL wallet user: %v", err)
	}
	return user
}
