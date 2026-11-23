package model

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestZTAPIWorkbenchDatabaseConcurrency(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			addr := os.Getenv("ZTAPI_R36_TEST_" + strings.ToUpper(dialect) + "_ADDR")
			password := os.Getenv("ZTAPI_R36_TEST_" + strings.ToUpper(dialect) + "_PASSWORD")
			upgradeFixture := dialect == "mysql" && os.Getenv("ZTAPI_R36_TEST_MYSQL_UPGRADE_FIXTURE") == "1"
			if addr == "" && password == "" {
				if upgradeFixture {
					t.Fatal("the requested MySQL upgrade fixture requires an isolated loopback database")
				}
				t.Skip("requires an isolated loopback r36_acceptance database")
			}
			require.Regexp(t, regexp.MustCompile(`^127\.0\.0\.1:[0-9]{1,5}$`), addr)
			if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(password) {
				t.Fatal("isolated database credentials invalid")
			}
			var driver gorm.Dialector
			if dialect == "mysql" {
				driver = mysql.Open(fmt.Sprintf("root:%s@tcp(%s)/r36_acceptance?parseTime=true&timeout=5s", password, addr))
			} else {
				driver = postgres.Open(fmt.Sprintf("postgresql://postgres:%s@%s/r36_acceptance?sslmode=disable&connect_timeout=5", password, addr))
			}
			db, err := gorm.Open(driver, &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal("could not open disposable loopback database")
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(10)
			previous, logDB := DB, LOG_DB
			sqlite, my, pg, redis := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL, common.RedisEnabled
			DB, LOG_DB = db, db
			common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL, common.RedisEnabled = false, dialect == "mysql", dialect == "postgres", false
			initCol()
			t.Cleanup(func() {
				DB, LOG_DB = previous, logDB
				common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL, common.RedisEnabled = sqlite, my, pg, redis
				initCol()
				_ = sqlDB.Close()
			})
			if upgradeFixture {
				if !t.Run("upgrade", func(t *testing.T) { verifyWorkbenchMySQLUpgradeFixture(t, db) }) {
					t.FailNow()
				}
			}
			require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &BalanceLedger{}, &ZTAPIWorkbenchTask{}, &ZTAPIWorkbenchPilotSpend{}, &ZTAPIRequestSettlement{}, &ZTAPIRequestAttempt{}, &ZTAPISettlementFinalizationIntent{}, &ZTAPISupplierRefundCharge{}))
			if dialect == "mysql" {
				var count int64
				require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'r36_acceptance' AND column_name = 'price_snapshot_json' AND data_type = 'mediumtext' AND table_name IN ('ztapi_request_settlements', 'ztapi_supplier_refund_charges')`).Scan(&count).Error)
				require.EqualValues(t, 2, count)
			}
			for round := 0; round < 5; round++ {
				t.Run(fmt.Sprint(round), func(t *testing.T) { verifyWorkbenchConcurrentTransactions(t, db) })
			}
			t.Run("pilot-hard-cap", func(t *testing.T) {
				verifyWorkbenchPilotConcurrentCharges(t, db)
			})
			for round := 0; round < 5; round++ {
				t.Run(fmt.Sprintf("expiry-%d", round), func(t *testing.T) {
					namespace := uuid.NewString()[:12]
					user := createBalanceLedgerTestUser(t, db, "exp-"+namespace, 1000)
					verifyWorkbenchExpiredHolds(t, db, ZTAPIRequestSettlement{UserID: user.Id, PublicModel: "zt-test", PriceSnapshotJSON: `{"version":1}`}, namespace)
				})
			}
		})
	}
}

func verifyWorkbenchPilotConcurrentCharges(t *testing.T, db *gorm.DB) {
	user := createBalanceLedgerTestUser(t, db, "pilot-"+uuid.NewString()[:12], 10000)
	for _, tc := range []struct {
		requests int
		budget   int64
		reserve  int
	}{
		{2, 1600, 8},
		{14, 400, 2},
	} {
		p := ZTAPIWorkbenchPilotPolicy{BatchID: uuid.NewString(), PolicySHA256: strings.Repeat("a", 64),
			ExpiresAt: time.Now().Add(10 * time.Minute).Unix(), UserID: user.Id, ChannelID: 4,
			MaxRequests: tc.requests, BudgetQuota: tc.budget}
		type job struct {
			task, hold string
			token      int
			leg        ZTAPIWorkbenchLeg
		}
		jobs := make([]job, 8)
		for i := range jobs {
			task, hold := uuid.NewString(), uuid.NewString()
			key := Token{UserId: user.Id, KeyHash: strings.ReplaceAll(task, "-", ""), Status: common.TokenStatusEnabled,
				RemainQuota: 1000, ModelLimitsEnabled: true, ModelLimits: "zt-claude-fable-5",
				WorkbenchOnly: true, ExpiredTime: p.ExpiresAt}
			require.NoError(t, db.Create(&key).Error)
			leg := ZTAPIWorkbenchLeg{Stage: "main", Settlement: ZTAPIRequestSettlement{
				OperationID: hold, RequestID: hold, UserID: user.Id, TokenID: key.Id, PublicModel: key.ModelLimits,
				PriceSnapshotJSON: `{"version":1}`, ReservedQuota: 200},
				Native: &ZTAPIWorkbenchNativeBounds{BodySHA256: strings.Repeat("a", 64), OutboundBytes: 2000, MaxOutput: 1024}}
			jobs[i] = job{task, hold, key.Id, leg}
		}
		reserved := make([]bool, len(jobs))
		accepted := 0
		for _, err := range workbenchConcurrentActions(len(jobs), func(i int) error {
			_, err := BeginZTAPIWorkbenchBatch(jobs[i].task, 1000, []ZTAPIWorkbenchLeg{jobs[i].leg}, p)
			reserved[i] = err == nil
			return err
		}) {
			if err == nil {
				accepted++
			} else {
				require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
			}
		}
		require.Equal(t, tc.reserve, accepted)
		eligible := make([]job, 0, accepted)
		for i, j := range jobs {
			if reserved[i] {
				eligible = append(eligible, j)
			}
		}
		claim := func(j job, policy ZTAPIWorkbenchPilotPolicy) error {
			_, err := ClaimZTAPIWorkbenchNativeAttempt(j.task, user.Id, j.token, j.hold, 4,
				strings.Repeat("b", 64), "/v1/chat/completions", policy)
			return err
		}
		success := 0
		for _, err := range workbenchConcurrentActions(len(eligible), func(i int) error { return claim(eligible[i], p) }) {
			if err == nil {
				success++
			} else {
				require.ErrorIs(t, err, ErrZTAPIWorkbenchPilotLimit)
			}
		}
		require.Equal(t, 2, success)
		var batch ZTAPIWorkbenchPilotSpend
		require.NoError(t, db.First(&batch, "id = ?", p.BatchID).Error)
		var claims []workbenchPilotClaim
		require.NoError(t, common.Unmarshal([]byte(batch.ClaimsJSON), &claims))
		require.Len(t, claims, 2)
		for _, j := range eligible {
			var row ZTAPIRequestSettlement
			require.NoError(t, db.First(&row, "operation_id = ?", j.hold).Error)
			if row.Dispatched {
				require.ErrorIs(t, claim(j, p), ErrZTAPISettlementConflict)
			} else {
				changed := p
				changed.PolicySHA256 = strings.Repeat("c", 64)
				require.ErrorIs(t, claim(j, changed), ErrZTAPISettlementConflict)
			}
		}
	}
}

func workbenchConcurrentActions(n int, f func(int) error) []error {
	start, done := make(chan struct{}), make(chan error, n)
	for i := 0; i < n; i++ {
		go func(index int) { <-start; done <- f(index) }(i)
	}
	close(start)
	results := make([]error, n)
	for i := range results {
		results[i] = <-done
	}
	return results
}

func verifyWorkbenchConcurrentTransactions(t *testing.T, db *gorm.DB) {
	t.Helper()
	task, hold := uuid.NewString(), uuid.NewString()
	user := User{Username: "wb" + task[:12], AffCode: task[:7], Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Quota: 1000}
	require.NoError(t, db.Create(&user).Error)
	key := Token{UserId: user.Id, KeyHash: strings.ReplaceAll(task, "-", ""), Status: common.TokenStatusEnabled, RemainQuota: 1000,
		ModelLimitsEnabled: true, ModelLimits: "zt-test", ExpiredTime: time.Now().Add(10 * time.Minute).Unix()}
	require.NoError(t, db.Create(&key).Error)
	input := ZTAPIRequestSettlement{OperationID: hold, RequestID: hold, UserID: user.Id, TokenID: key.Id,
		PublicModel: key.ModelLimits, PriceSnapshotJSON: `{"version":1}`, ReservedQuota: 200}
	leg := ZTAPIWorkbenchLeg{Stage: "main", Settlement: input,
		Native: &ZTAPIWorkbenchNativeBounds{BodySHA256: strings.Repeat("a", 64), OutboundBytes: 2000, MaxOutput: 1024}}
	for _, err := range workbenchConcurrentActions(8, func(int) error {
		_, err := BeginZTAPIWorkbenchBatch(task, 600, []ZTAPIWorkbenchLeg{leg})
		return err
	}) {
		require.NoError(t, err)
	}
	assertZTAPISettlementBalances(t, db, input, 800, 800)
	success := 0
	for _, err := range workbenchConcurrentActions(2, func(int) error {
		child := leg
		child.Stage = "child"
		child.Settlement.OperationID = uuid.NewString()
		child.Settlement.RequestID = child.Settlement.OperationID
		child.Settlement.ReservedQuota = 300
		_, err := BeginZTAPIWorkbenchBatch(task, 600, []ZTAPIWorkbenchLeg{child})
		return err
	}) {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
		}
	}
	require.Equal(t, 1, success)
	success = 0
	for _, err := range workbenchConcurrentActions(8, func(i int) error {
		_, err := ClaimZTAPIWorkbenchNativeAttempt(task, user.Id, key.Id, hold, i+1, strings.Repeat("b", 64), "/v1/chat/completions")
		return err
	}) {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, ErrZTAPISettlementConflict)
		}
	}
	require.Equal(t, 1, success)
	for _, err := range workbenchConcurrentActions(8, func(int) error {
		_, err := FinalizeZTAPIRequestSettlement(hold, 70, `{"input":70}`, `[{"dimension":"input_tokens","units":"70","unit_quota":"1","charged_quota":70}]`)
		return err
	}) {
		require.NoError(t, err)
	}
	assertZTAPISettlementBalances(t, db, input, 630, 630)
	var row ZTAPIRequestSettlement
	require.NoError(t, db.Where("operation_id = ?", hold).Take(&row).Error)
	var count int64
	require.NoError(t, db.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ?", row.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	for _, kind := range []string{BalanceLedgerSourceUsageReservation, BalanceLedgerSourceUsageSettlement} {
		require.NoError(t, db.Model(&BalanceLedger{}).Where("request_id = ? AND source_type = ?", hold, kind).Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
}
