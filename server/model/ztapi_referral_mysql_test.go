package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func ztapiReferralCommercialMySQLDSN(raw, mandatory string) (string, error) {
	required := false
	switch strings.ToLower(strings.TrimSpace(mandatory)) {
	case "", "0", "false":
	case "1", "true":
		required = true
	default:
		return "", errors.New("invalid mandatory commercial MySQL test switch")
	}
	return commercialMySQLTestDSN(raw, required)
}

func TestZTAPIReferralMySQLMandatoryConfigurationGuard(t *testing.T) {
	for _, flag := range []string{"1", "true", "TRUE"} {
		_, err := ztapiReferralCommercialMySQLDSN("", flag)
		require.Error(t, err)
	}
	for _, dsn := range []string{"test:pass@tcp(127.0.0.1:3306)/ztapi", "test:pass@tcp(192.0.2.1:3306)/ztapi_commercial_test"} {
		_, err := ztapiReferralCommercialMySQLDSN(dsn, "1")
		require.Error(t, err)
	}
	_, err := ztapiReferralCommercialMySQLDSN("", "unexpected")
	require.Error(t, err)
	safe, err := ztapiReferralCommercialMySQLDSN("", "0")
	require.NoError(t, err)
	require.Empty(t, safe)
	safe, err = ztapiReferralCommercialMySQLDSN("test:pass@tcp(127.0.0.1:3306)/ztapi_commercial_test", "1")
	require.NoError(t, err)
	require.Contains(t, safe, "parseTime=true")
}

func ztapiReferralOpenCommercialMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	safe, err := ztapiReferralCommercialMySQLDSN(os.Getenv("ZTAPI_COMMERCIAL_MYSQL_TEST_DSN"), os.Getenv("ZTAPI_REQUIRE_COMMERCIAL_MYSQL"))
	require.NoError(t, err)
	if safe == "" {
		t.Skip("dedicated loopback ztapi_commercial_test DSN absent; real referral MySQL concurrency NOT executed")
	}
	config, err := mysqldriver.ParseDSN(safe)
	require.NoError(t, err)
	config.Timeout = 5 * time.Second
	config.ReadTimeout = 10 * time.Second
	config.WriteTimeout = 10 * time.Second
	db, err := gorm.Open(gormmysql.Open(config.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("cannot open dedicated loopback commercial MySQL referral test database")
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(16)
	oldDB, oldRedis := DB, common.RedisEnabled
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { DB, common.RedisEnabled = oldDB, oldRedis; _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&User{}, &USDTTopUpOrder{}, &TopUp{}, &BalanceLedger{}))
	require.NoError(t, MigrateZTAPIReferral(db))
	for _, model := range append(ZTAPIReferralModels(), &User{}, &USDTTopUpOrder{}, &TopUp{}, &BalanceLedger{}) {
		statement := &gorm.Statement{DB: db}
		require.NoError(t, statement.Parse(model))
		var engine string
		require.NoError(t, db.Raw("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", statement.Schema.Table).Scan(&engine).Error)
		require.True(t, strings.EqualFold(engine, "InnoDB"), "%s must use InnoDB", statement.Schema.Table)
	}
	return db
}

func TestZTAPICommercialMySQLCNYReferralMigrationAndReplay(t *testing.T) {
	db := ztapiReferralOpenCommercialMySQL(t)
	require.NoError(t, db.AutoMigrate(&CNYTopUpOrder{}))
	f := newZTAPIReferralMySQLFixture(t, db, 600)
	f.fund(t)
	var legacy ZTAPIReferralCommission
	require.NoError(t, db.Where("order_id = ?", f.order.ID).First(&legacy).Error)
	// Recreate the previous two NOT NULL columns in this isolated fixture DB.
	require.NoError(t, db.Exec("ALTER TABLE ztapi_referral_commissions MODIFY order_id BIGINT NOT NULL, MODIFY top_up_id BIGINT NOT NULL").Error)
	require.NoError(t, MigrateZTAPIReferral(db))
	var legacyAfter ZTAPIReferralCommission
	require.NoError(t, db.First(&legacyAfter, legacy.ID).Error)
	require.Equal(t, legacy, legacyAfter)
	policy := ZTAPICNYReferralPolicy{EnabledAt: f.now.Add(-time.Hour).Unix(), MerchantNo: "merchant1", AppID: "app1", CNYPerUnit: "7", QuotaPerUnit: 500000, FeeBPS: map[string]int{"alipay": 500, "wxpay": 500}}
	for _, method := range []string{"alipay", "wxpay"} {
		order := cnyReferralPaidForUser(t, policy, method, 700, f.now.Add(-30*time.Minute), f.invitee.Id)
		t.Cleanup(func() {
			require.NoError(t, db.Exec("DELETE FROM balance_ledgers WHERE request_id = ? AND user_id = ?", order.OrderNo, f.invitee.Id).Error)
			require.NoError(t, db.Where("id = ? AND user_id = ?", order.ID, f.invitee.Id).Delete(&CNYTopUpOrder{}).Error)
		})
	}
	replayed := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := ReconcileZTAPICNYReferralSettlements(context.Background(), &policy, ZTAPICNYReferralCursor{}, 20, f.now)
			replayed <- err
		}()
	}
	require.NoError(t, <-replayed)
	require.NoError(t, <-replayed)
	var count int64
	require.NoError(t, db.Model(&ZTAPIReferralCommission{}).Where("user_id = ? AND cny_order_id IS NOT NULL AND order_id IS NULL AND top_up_id IS NULL AND commission_micros = 50000", f.inviter.Id).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

type ztapiReferralMySQLFixture struct {
	db               *gorm.DB
	inviter, invitee User
	order            USDTTopUpOrder
	now              time.Time
	suffix           string
}

func newZTAPIReferralMySQLFixture(t *testing.T, db *gorm.DB, units int64) *ztapiReferralMySQLFixture {
	t.Helper()
	f := &ztapiReferralMySQLFixture{db: db, now: time.Now().UTC().Truncate(time.Second), suffix: referralHash(t.Name() + common.GetUUID())[:12]}
	f.inviter = User{Username: "rfi" + f.suffix, AffCode: "rfi" + f.suffix, Status: common.UserStatusEnabled}
	f.invitee = User{Username: "rfu" + f.suffix, AffCode: "rfu" + f.suffix, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&f.inviter).Error)
	require.NoError(t, db.Create(&f.invitee).Error)
	// Cleanup is confined to the two generated fixture identities and their exact
	// order/request IDs. Raw SQL is intentional for immutable test-only rows.
	t.Cleanup(func() {
		for _, table := range []string{"ztapi_referral_allocations", "ztapi_referral_withdrawals", "ztapi_referral_ledger_entries", "ztapi_referral_audits", "ztapi_referral_commissions", "ztapi_referral_account_locks"} {
			if err := db.Exec("DELETE FROM "+table+" WHERE user_id = ?", f.inviter.Id).Error; err != nil {
				t.Errorf("fixture cleanup %s: %v", table, err)
			}
		}
		require.NoError(t, db.Exec("DELETE FROM ztapi_referral_bindings WHERE user_id = ? AND inviter_id = ?", f.invitee.Id, f.inviter.Id).Error)
		if f.order.ID > 0 {
			require.NoError(t, db.Exec("DELETE FROM balance_ledgers WHERE request_id = ? AND user_id = ?", f.order.TradeNo, f.invitee.Id).Error)
			require.NoError(t, db.Where("id = ? AND user_id = ?", f.order.ID, f.invitee.Id).Delete(&USDTTopUpOrder{}).Error)
			require.NoError(t, db.Where("id = ? AND user_id = ?", f.order.TopUpID, f.invitee.Id).Delete(&TopUp{}).Error)
		}
		for _, u := range []User{f.inviter, f.invitee} {
			require.NoError(t, db.Exec("DELETE FROM users WHERE id = ? AND username = ?", u.Id, u.Username).Error)
		}
	})
	bound := f.now.Add(-10 * 24 * time.Hour)
	// An explicit existing promise is independent of current campaign admission.
	require.NoError(t, db.Create(&ZTAPIReferralBinding{UserID: f.invitee.Id, InviterID: f.inviter.Id, CampaignID: 1, BoundAt: bound.Unix(), ExpiresAt: referralAnniversary(bound).Unix(), CampaignStartedAt: bound.Unix(), RuleVersion: ZTAPIReferralRuleVersion}).Error)
	f.order = referralSettlementForUser(t, db, f.invitee.Id, units, f.now.Add(-9*24*time.Hour), f.now.Add(-8*24*time.Hour))
	return f
}

func (f *ztapiReferralMySQLFixture) fund(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := ReconcileZTAPIReferralSettlements(ctx, 1000)
	require.NoError(t, err)
	var count int64
	require.NoError(t, f.db.Model(&ZTAPIReferralCommission{}).Where("order_id = ?", f.order.ID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func (f *ztapiReferralMySQLFixture) request(ctx context.Context, amount int64, key string) (*ZTAPIReferralWithdrawal, error) {
	return RequestZTAPIReferralWithdrawal(ctx, ZTAPIReferralWithdrawalRequest{UserID: f.inviter.Id, AmountMicros: amount, Address: USDTTopUpContractAddress, IdempotencyKey: f.suffix + key, Now: f.now})
}

func (f *ztapiReferralMySQLFixture) action(w *ZTAPIReferralWithdrawal, admin int) ZTAPIReferralWithdrawalAction {
	return ZTAPIReferralWithdrawalAction{WithdrawalID: w.ID, ExpectedVersion: w.Version, UserID: f.inviter.Id, OperatorID: admin, Now: f.now}
}

func (f *ztapiReferralMySQLFixture) confirmed(t *testing.T) *ZTAPIReferralWithdrawal {
	t.Helper()
	w, err := f.request(t.Context(), 20_000_000, "confirmed")
	require.NoError(t, err)
	in := f.action(w, f.inviter.Id)
	in.FeeMicros = 1_000_000
	w, err = QuoteZTAPIReferralWithdrawal(t.Context(), in)
	require.NoError(t, err)
	w, err = ConfirmZTAPIReferralWithdrawal(t.Context(), f.action(w, f.inviter.Id))
	require.NoError(t, err)
	return w
}

func ztapiReferralConcurrent(count int, fn func(int) error) []error {
	start := make(chan struct{})
	results := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; results <- fn(i) }(i)
	}
	close(start)
	wg.Wait()
	close(results)
	errors := make([]error, 0, count)
	for err := range results {
		errors = append(errors, err)
	}
	return errors
}

func ztapiReferralMySQLWorkers(t *testing.T, f *ztapiReferralMySQLFixture) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, err := range ztapiReferralConcurrent(8, func(int) error { _, err := ReconcileZTAPIReferralSettlements(ctx, 1000); return err }) {
		require.NoError(t, err)
	}
	var c ZTAPIReferralCommission
	require.NoError(t, f.db.Where("order_id = ?", f.order.ID).First(&c).Error)
	var count int64
	require.NoError(t, f.db.Model(&ZTAPIReferralCommission{}).Where("order_id = ?", f.order.ID).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.NoError(t, f.db.Model(&ZTAPIReferralLedgerEntry{}).Where("commission_id = ? AND kind = ?", c.ID, "commission").Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.Equal(t, f.order.CreditUnits*1_000_000/20, c.CommissionMicros)
}

func ztapiReferralMySQLWallet(t *testing.T, f *ztapiReferralMySQLFixture) {
	f.fund(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, err := range ztapiReferralConcurrent(8, func(int) error { _, err := f.request(ctx, 20_000_000, "same-key"); return err }) {
		require.NoError(t, err)
	}
	var count int64
	require.NoError(t, f.db.Model(&ZTAPIReferralWithdrawal{}).Where("user_id = ?", f.inviter.Id).Count(&count).Error)
	require.Equal(t, int64(1), count)
	success := 0
	for _, err := range ztapiReferralConcurrent(8, func(i int) error {
		_, err := f.request(ctx, 20_000_000, fmt.Sprintf("different-key-%d", i))
		return err
	}) {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, ErrZTAPIReferralInsufficient)
		}
	}
	require.Equal(t, 1, success)
	s, err := GetZTAPIReferralSummary(ctx, f.inviter.Id, f.now)
	require.NoError(t, err)
	require.Zero(t, s.AvailableCommissionMicros)
	require.Equal(t, int64(40_000_000), s.WithdrawalReservedMicros)
}

func ztapiReferralMySQLClaim(t *testing.T, f *ztapiReferralMySQLFixture) {
	f.fund(t)
	w := f.confirmed(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admins := []int{f.inviter.Id, f.invitee.Id}
	success := 0
	for _, err := range ztapiReferralConcurrent(2, func(i int) error { _, err := ClaimZTAPIReferralPaymentIntent(ctx, f.action(w, admins[i])); return err }) {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, ErrZTAPIReferralConflict)
		}
	}
	require.Equal(t, 1, success)
	reloaded, err := referralLoadWithdrawal(f.db, w.ID)
	require.NoError(t, err)
	require.Equal(t, "paying", reloaded.Status)
	require.Contains(t, admins, reloaded.HolderID)
}

func ztapiReferralMySQLRefundPaid(t *testing.T, f *ztapiReferralMySQLFixture) {
	f.fund(t)
	w := f.confirmed(t)
	w, err := ClaimZTAPIReferralPaymentIntent(t.Context(), f.action(w, f.inviter.Id))
	require.NoError(t, err)
	evidence := referralEvidence(w, f.now)
	evidence.TxID = referralHash("payout:" + f.suffix)
	payment := ZTAPIReferralPaymentConfirmation{WithdrawalID: w.ID, ExpectedVersion: w.Version, OperatorID: f.inviter.Id, Evidence: evidence, Now: f.now}
	refund := ZTAPIReferralRefundInput{OrderID: f.order.ID, CumulativePrincipalMicros: f.order.CreditUnits * 1_000_000, OperatorID: f.inviter.Id, IdempotencyKey: f.suffix + "refund", EvidenceReference: f.suffix + "verified-receipt", Reason: "isolated concurrency fixture", At: f.now}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, err := range ztapiReferralConcurrent(2, func(i int) error {
		if i == 0 {
			_, err := ConfirmZTAPIReferralManualPayment(ctx, payment)
			return err
		}
		_, err := ReverseZTAPIReferralRechargePrincipal(ctx, refund)
		return err
	}) {
		require.NoError(t, err)
	}
	_, err = ConfirmZTAPIReferralManualPayment(ctx, payment)
	require.NoError(t, err)
	_, err = ReverseZTAPIReferralRechargePrincipal(ctx, refund)
	require.NoError(t, err)
	s, err := GetZTAPIReferralSummary(ctx, f.inviter.Id, f.now)
	require.NoError(t, err)
	require.Zero(t, s.AvailableCommissionMicros)
	require.Zero(t, s.WithdrawalReservedMicros)
	require.Equal(t, int64(20_000_000), s.ReversedCommissionMicros)
	require.Equal(t, int64(20_000_000), s.PaidCommissionMicros)
	require.Equal(t, int64(20_000_000), s.ManualLossMicros)
	var entries []ZTAPIReferralLedgerEntry
	require.NoError(t, f.db.Where("user_id = ?", f.inviter.Id).Find(&entries).Error)
	counts := map[string]int{}
	var net int64
	for _, e := range entries {
		counts[e.Kind]++
		if e.Kind != "manual_loss" {
			net += e.AmountMicros
		}
	}
	require.Equal(t, map[string]int{"commission": 1, "refund": 1, "manual_loss": 1}, counts)
	require.Zero(t, net)
}

func TestZTAPICommercialMySQLReferralWorkersExactlyOnce(t *testing.T) {
	db := ztapiReferralOpenCommercialMySQL(t)
	ztapiReferralMySQLWorkers(t, newZTAPIReferralMySQLFixture(t, db, 800))
}

func TestZTAPICommercialMySQLReferralWalletConcurrency(t *testing.T) {
	db := ztapiReferralOpenCommercialMySQL(t)
	ztapiReferralMySQLWallet(t, newZTAPIReferralMySQLFixture(t, db, 800))
}

func TestZTAPICommercialMySQLReferralExclusiveClaim(t *testing.T) {
	db := ztapiReferralOpenCommercialMySQL(t)
	ztapiReferralMySQLClaim(t, newZTAPIReferralMySQLFixture(t, db, 400))
}

func TestZTAPICommercialMySQLReferralRefundPaidRace(t *testing.T) {
	db := ztapiReferralOpenCommercialMySQL(t)
	ztapiReferralMySQLRefundPaid(t, newZTAPIReferralMySQLFixture(t, db, 400))
}

// The identical fixtures/scenarios run locally too, so missing MySQL cannot
// conceal broken setup. This is not evidence of MySQL isolation/lock behavior.
func TestZTAPIReferralMySQLFixturesLocal(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		units int64
		run   func(*testing.T, *ztapiReferralMySQLFixture)
	}{
		{"workers", 800, ztapiReferralMySQLWorkers}, {"wallet", 800, ztapiReferralMySQLWallet}, {"claim", 400, ztapiReferralMySQLClaim}, {"refund_paid", 400, ztapiReferralMySQLRefundPaid},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := referralTestDB(t)
			scenario.run(t, newZTAPIReferralMySQLFixture(t, db, scenario.units))
		})
	}
}
