package model

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	mysqldriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func commercialMySQLTestDSN(raw string, required bool) (string, error) {
	if strings.TrimSpace(raw) == "" {
		if required {
			return "", errors.New("ZTAPI_COMMERCIAL_MYSQL_TEST_DSN is required")
		}
		return "", nil
	}
	config, err := mysqldriver.ParseDSN(raw)
	if err != nil {
		return "", errors.New("invalid commercial test DSN")
	}
	host, _, err := net.SplitHostPort(config.Addr)
	if err != nil || config.Net != "tcp" || config.DBName != "ztapi_commercial_test" || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
		return "", errors.New("commercial test requires loopback TCP and database ztapi_commercial_test")
	}
	config.ParseTime = true
	config.MultiStatements = false
	return config.FormatDSN(), nil
}

func TestZTAPICommercialMySQLConfigurationGuard(t *testing.T) {
	for _, tt := range []struct {
		name, dsn     string
		required, bad bool
	}{
		{"optional missing", "", false, false}, {"required missing", "", true, true},
		{"dedicated local", "test:pass@tcp(127.0.0.1:3306)/ztapi_commercial_test", true, false},
		{"dedicated IPv6", "test:pass@tcp([::1]:3306)/ztapi_commercial_test", true, false},
		{"remote server", "test:pass@tcp(192.0.2.1:3306)/ztapi_commercial_test", true, true},
		{"production database", "test:pass@tcp(127.0.0.1:3306)/ztapi", true, true},
		{"other test database", "test:pass@tcp(127.0.0.1:3306)/ztapi_test", true, true},
		{"socket transport", "test:pass@unix(/tmp/mysql.sock)/ztapi_commercial_test", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			safe, err := commercialMySQLTestDSN(tt.dsn, tt.required)
			if (err != nil) != tt.bad {
				t.Fatalf("guard bad=%v err=%v", tt.bad, err)
			}
			if err == nil && safe != "" && !strings.Contains(safe, "parseTime=true") {
				t.Fatal("time parsing not enabled")
			}
		})
	}
}

type commercialMySQLFixture struct {
	db          *gorm.DB
	user, admin User
	token       Token
	channel     Channel
	input       ZTAPIRequestSettlement
}

// Exercise fixture setup locally too; a skipped DSN must not hide invalid
// attempt dispatch identities until the isolated MySQL runner is available.
func TestZTAPICommercialMySQLFixtureTwoAttemptsLocal(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			db, _ := setupZTAPISettlement(t)
			if err := db.AutoMigrate(&Channel{}, &ZTAPIRequestAttempt{}, &ZTAPIPendingResolution{}, &ZTAPISettlementLogOutbox{}, &ZTAPISettlementLogReceipt{}, &Log{}); err != nil {
				t.Fatal(err)
			}
			if err := MigrateZTAPIAttemptBilling(db); err != nil {
				t.Fatal(err)
			}
			f := newCommercialMySQLFixture(t, db, 1000)
			row, evidence, input := f.twoAttempts(t, pending)
			var attempts []ZTAPIRequestAttempt
			if err := db.Where("settlement_id = ?", row.ID).Order("attempt").Find(&attempts).Error; err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 2 || attempts[0].ChannelID == attempts[1].ChannelID || evidence.ConsumeLog.ChannelId != attempts[1].ChannelID || input.ChannelID != attempts[0].ChannelID {
				t.Fatalf("invalid fixture attempt identities: %+v", attempts)
			}
		})
	}
}

func newCommercialMySQLFixture(t *testing.T, db *gorm.DB, initial int) commercialMySQLFixture {
	t.Helper()
	suffix := ztapiSupplierRefundHash(t.Name() + time.Now().String())[:10]
	user := createBalanceLedgerTestUser(t, db, "cmu"+suffix, initial)
	admin := createBalanceLedgerTestUser(t, db, "cma"+suffix, 0)
	if err := db.Model(&admin).Update("role", common.RoleFinanceUser).Error; err != nil {
		t.Fatal(err)
	}
	token := Token{UserId: user.Id, KeyHash: ztapiSupplierRefundHash("token" + suffix), RemainQuota: 1000, UsedQuota: 0, ExpiredTime: -1, Status: common.TokenStatusEnabled}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	channel := Channel{Name: "cm-" + suffix, Type: 1, Status: 1}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	input := ZTAPIRequestSettlement{OperationID: "cm-op-" + suffix, RequestID: "cm-request-" + suffix, UserID: user.Id, TokenID: token.Id, PublicModel: "commercial-test-model", PriceSnapshotJSON: `{"unit_quota":"1"}`, ReservedQuota: 100}
	f := commercialMySQLFixture{db: db, user: user, admin: admin, token: token, channel: channel, input: input}
	t.Cleanup(func() {
		// Only rows belonging to this unique synthetic request are removed.
		_ = DB.Exec("DELETE FROM ztapi_finance_alert_outboxes WHERE source_kind = 'settlement' AND source_record_id IN (SELECT id FROM ztapi_request_settlements WHERE request_id = ?)", input.RequestID).Error
		_ = DB.Exec("DELETE FROM ztapi_media_tasks WHERE request_id = ?", input.RequestID).Error
		_ = DB.Exec("DELETE FROM ztapi_attempt_billing_approvals WHERE proof_id IN (SELECT id FROM ztapi_attempt_billing_proofs WHERE request_id = ?)", input.RequestID).Error
		_ = DB.Exec("DELETE FROM ztapi_settlement_log_receipts WHERE operation_id IN (SELECT billing_operation_id FROM ztapi_supplier_refund_charges WHERE request_id = ?) OR operation_id = ?", input.RequestID, input.OperationID).Error
		_ = DB.Exec("DELETE FROM ztapi_settlement_log_outboxes WHERE operation_id IN (SELECT billing_operation_id FROM ztapi_supplier_refund_charges WHERE request_id = ?) OR operation_id = ?", input.RequestID, input.OperationID).Error
		_ = DB.Where("request_id = ?", input.RequestID).Delete(&Log{}).Error
		_ = DB.Exec("DELETE FROM ztapi_supplier_refund_approvals WHERE refund_id IN (SELECT id FROM ztapi_supplier_refunds WHERE request_id = ?)", input.RequestID).Error
		_ = DB.Exec("DELETE FROM ztapi_request_attempts WHERE settlement_id IN (SELECT id FROM ztapi_request_settlements WHERE request_id = ?)", input.RequestID).Error
		_ = DB.Exec("DELETE FROM ztapi_pending_resolutions WHERE settlement_id IN (SELECT id FROM ztapi_request_settlements WHERE request_id = ?)", input.RequestID).Error
		for _, table := range []string{"ztapi_attempt_billing_reviews", "ztapi_attempt_billing_proofs", "ztapi_supplier_refunds", "ztapi_supplier_refund_charges", "ztapi_settlement_finalization_intents", "ztapi_request_settlements", "balance_ledgers"} {
			_ = DB.Exec("DELETE FROM "+table+" WHERE request_id = ?", input.RequestID).Error
		}
		_ = DB.Where("operation_id = ?", input.OperationID).Delete(&ZTAPISettlementLogOutbox{}).Error
		_ = DB.Unscoped().Where("id = ?", token.Id).Delete(&Token{}).Error
		_ = DB.Unscoped().Where("id IN ?", []int{user.Id, admin.Id}).Delete(&User{}).Error
		_ = DB.Where("id = ?", channel.Id).Delete(&Channel{}).Error
	})
	return f
}

func commercialMySQLImageFinalizationInput(f commercialMySQLFixture) (string, string, ZTAPISettlementEvidence) {
	usage := fmt.Sprintf(`{"upstream_request_id":"wire-%s","raw_usage_json":"{\"input_tokens\":10,\"output_tokens\":10,\"total_tokens\":20}","dimensions":{"input_tokens":"10","output_tokens":"10"},"price_rule_ids":{"input_tokens":"lte_200k","output_tokens":"lte_200k"}}`, f.input.RequestID)
	dimensions, _ := common.Marshal([]ZTAPISupplierRefundDimension{
		{Dimension: "input_tokens", Units: "10", UnitQuota: "5", ChargedQuota: 50},
		{Dimension: "output_tokens", Units: "10", UnitQuota: "5", ChargedQuota: 50},
	})
	evidence := ZTAPISettlementEvidence{FinalAttempt: 1, ConsumeLog: Log{
		Type: LogTypeConsume, UserId: f.user.Id, TokenId: f.token.Id,
		RequestId: f.input.RequestID, ModelName: f.input.PublicModel, Quota: 100,
		ChannelId: f.channel.Id, CreatedAt: 1, UpstreamRequestId: "wire-" + f.input.RequestID,
	}}
	return usage, string(dimensions), evidence
}

func assertCommercialMySQLImageCounts(t *testing.T, f commercialMySQLFixture, ledger, logs, charges, alerts int64) ZTAPIRequestSettlement {
	t.Helper()
	var row ZTAPIRequestSettlement
	if err := DB.Where("request_id = ?", f.input.RequestID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		model  any
		query  string
		value  any
		wanted int64
	}{
		{&BalanceLedger{}, "request_id = ?", f.input.RequestID, ledger},
		{&ZTAPISettlementLogOutbox{}, "operation_id = ?", f.input.OperationID, logs},
		{&ZTAPISupplierRefundCharge{}, "request_id = ?", f.input.RequestID, charges},
		{&ZTAPIFinanceAlertOutbox{}, "source_kind = 'settlement' AND source_record_id = ?", row.ID, alerts},
	}
	for _, check := range checks {
		var count int64
		if err := DB.Model(check.model).Where(check.query, check.value).Count(&count).Error; err != nil || count != check.wanted {
			t.Fatalf("%T count=%d want=%d err=%v", check.model, count, check.wanted, err)
		}
	}
	return row
}

func commercialMySQLImageConcurrentFinalize(t *testing.T) {
	f := newCommercialMySQLFixture(t, DB, 1000)
	f.input.PublicModel = "commercial-image-model"
	f.input.PriceSnapshotJSON = `{"modality":"image","maximum_quota":100}`
	if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
		t.Fatal(err)
	}
	f.prepareAttempt(t)
	usage, dimensions, evidence := commercialMySQLImageFinalizationInput(f)
	commercialConcurrent(t, 16, func() error {
		_, err := FinalizeZTAPIRequestSettlementWithEvidence(f.input.OperationID, 100, usage, dimensions, evidence)
		return err
	})
	row := assertCommercialMySQLImageCounts(t, f, 1, 1, 1, 0)
	if row.Status != ZTAPISettlementSettled || row.ChargedQuota != 100 || row.FinalAttempt != 1 || row.UsageJSON != usage {
		t.Fatalf("concurrent image finalization changed evidence: %+v", row)
	}
	var user User
	if err := DB.First(&user, f.user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if user.Quota != 900 || user.UsedQuota != 100 || user.RequestCount != 1 {
		t.Fatalf("concurrent image finalization duplicated money: %+v", user)
	}
}

func commercialMySQLImagePendingFinalizeRace(t *testing.T) {
	f := newCommercialMySQLFixture(t, DB, 1000)
	f.input.PublicModel = "commercial-image-race-model"
	f.input.PriceSnapshotJSON = `{"modality":"image","maximum_quota":100}`
	if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
		t.Fatal(err)
	}
	f.prepareAttempt(t)
	usage, dimensions, evidence := commercialMySQLImageFinalizationInput(f)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := FinalizeZTAPIRequestSettlementWithEvidence(f.input.OperationID, 100, usage, dimensions, evidence)
		results <- err
	}()
	go func() {
		<-start
		_, err := PendZTAPIRequestSettlement(f.input.OperationID, usage, `["image_usage_untrusted"]`)
		results <- err
	}()
	close(start)
	first, second := <-results, <-results
	if first != nil && !errors.Is(first, ErrZTAPISettlementPending) && !errors.Is(first, ErrZTAPISettlementConflict) {
		t.Fatal(first)
	}
	if second != nil && !errors.Is(second, ErrZTAPISettlementPending) && !errors.Is(second, ErrZTAPISettlementConflict) {
		t.Fatal(second)
	}
	var row ZTAPIRequestSettlement
	if err := DB.Where("request_id = ?", f.input.RequestID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	switch row.Status {
	case ZTAPISettlementSettled:
		assertCommercialMySQLImageCounts(t, f, 1, 1, 1, 0)
	case ZTAPISettlementPending:
		if _, err := QueuePendingZTAPIFinanceAlerts(t.Context(), DB, time.Now(), 100); err != nil {
			t.Fatal(err)
		}
		assertCommercialMySQLImageCounts(t, f, 1, 0, 0, 1)
	default:
		t.Fatalf("race left image settlement in %q", row.Status)
	}
}

func TestZTAPICommercialMySQLImageIntegration(t *testing.T) {
	required := strings.EqualFold(strings.TrimSpace(os.Getenv("ZTAPI_REQUIRE_COMMERCIAL_MYSQL")), "true")
	safe, err := commercialMySQLTestDSN(os.Getenv("ZTAPI_COMMERCIAL_MYSQL_TEST_DSN"), required)
	if err != nil {
		t.Fatal(err)
	}
	if safe == "" {
		t.Skip("dedicated loopback commercial MySQL DSN absent; real MySQL image settlement NOT executed")
	}
	db, err := gorm.Open(gormmysql.Open(safe), &gorm.Config{})
	if err != nil {
		t.Fatal("cannot open dedicated commercial MySQL test database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(20)
	oldDB, oldRedis := DB, common.RedisEnabled
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() {
		_ = sqlDB.Close()
		DB, common.RedisEnabled = oldDB, oldRedis
	})
	if err := db.AutoMigrate(
		&User{}, &Token{}, &Channel{}, &BalanceLedger{}, &ZTAPIRequestSettlement{},
		&ZTAPISettlementFinalizationIntent{}, &ZTAPIRequestAttempt{}, &ZTAPIAttemptBillingReview{},
		&ZTAPIAttemptBillingProof{}, &ZTAPIAttemptBillingApproval{}, &ZTAPIPendingResolution{},
		&ZTAPIMediaTask{}, &ZTAPISettlementLogReceipt{}, &Log{},
	); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISupplierRefund(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISettlementLogOutbox(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPIFinanceAlerts(db); err != nil {
		t.Fatal(err)
	}
	if err := registerZTAPIMediaTaskMutationGuard(db); err != nil {
		t.Fatal(err)
	}
	t.Run("16_identical_finalizations", commercialMySQLImageConcurrentFinalize)
	t.Run("pending_finalize_race", commercialMySQLImagePendingFinalizeRace)
}

func (f commercialMySQLFixture) prepareAttempt(t *testing.T) {
	t.Helper()
	if _, err := BeginZTAPIRequestAttempt(f.input.OperationID, f.channel.Id, "test-credential-v1", "chat"); err != nil {
		t.Fatal(err)
	}
	if err := RecordZTAPIRequestAttemptResponse(f.input.OperationID, 1, f.channel.Id, 200, "wire-"+f.input.RequestID); err != nil {
		t.Fatal(err)
	}
}

func (f commercialMySQLFixture) finalize(amount int64) (*ZTAPIRequestSettlement, error) {
	usage, dims, evidence := f.finalizationInput(amount)
	return FinalizeZTAPIRequestSettlementWithEvidence(f.input.OperationID, amount, usage, dims, evidence)
}

func (f commercialMySQLFixture) finalizationInput(amount int64) (string, string, ZTAPISettlementEvidence) {
	dims, _ := common.Marshal([]ZTAPISupplierRefundDimension{{Dimension: "input", Units: fmt.Sprint(amount), UnitQuota: "1", ChargedQuota: amount}})
	return fmt.Sprintf(`{"input_tokens":%d}`, amount), string(dims), ZTAPISettlementEvidence{FinalAttempt: 1, ConsumeLog: Log{Type: LogTypeConsume, UserId: f.user.Id, TokenId: f.token.Id, RequestId: f.input.RequestID, ModelName: f.input.PublicModel, Quota: int(amount), ChannelId: f.channel.Id, CreatedAt: 1, UpstreamRequestId: "wire-" + f.input.RequestID}}
}

func (f commercialMySQLFixture) intent(t *testing.T) ZTAPISettlementFinalizationIntent {
	t.Helper()
	var intent ZTAPISettlementFinalizationIntent
	if err := DB.Where("operation_id = ?", f.input.OperationID).Take(&intent).Error; err != nil {
		t.Fatal(err)
	}
	return intent
}

func (f commercialMySQLFixture) assertIntentFinancialState(t *testing.T, initial, amount int, settled bool) {
	t.Helper()
	var user User
	var token Token
	var channel Channel
	var row ZTAPIRequestSettlement
	for _, query := range []*gorm.DB{DB.First(&user, f.user.Id), DB.First(&token, f.token.Id), DB.First(&channel, f.channel.Id), DB.Where("request_id = ?", f.input.RequestID).Take(&row)} {
		if query.Error != nil {
			t.Fatal(query.Error)
		}
	}
	wallet, tokenQuota, tokenUsed := initial-int(f.input.ReservedQuota), 1000-int(f.input.ReservedQuota), int(f.input.ReservedQuota)
	used, requests, charges, ledgerCount := 0, 0, int64(0), int64(1)
	if settled {
		wallet, tokenQuota, tokenUsed = initial-amount, 1000-amount, amount
		used, requests, charges = amount, 1, 1
		if int64(amount) != f.input.ReservedQuota {
			ledgerCount++
		}
		if row.Status != ZTAPISettlementSettled || row.FinalAttempt != 1 || row.ChargedQuota != int64(amount) || row.TokenChargedQuota != int64(amount) || row.RefundedQuota != 0 {
			t.Fatalf("incorrect recovered settlement: %+v", row)
		}
	} else if row.ChargedQuota != 0 || row.TokenChargedQuota != 0 || row.FinalAttempt != 0 || row.Status == ZTAPISettlementSettled || row.Status == ZTAPISettlementReleased {
		t.Fatalf("uncommitted finalization changed settlement: %+v", row)
	}
	if user.Quota != wallet || token.RemainQuota != tokenQuota || token.UsedQuota != tokenUsed || user.UsedQuota != used || user.RequestCount != requests || channel.UsedQuota != int64(used) {
		t.Fatalf("financial state wallet=%d token=%d used=%d user_used=%d requests=%d channel_used=%d", user.Quota, token.RemainQuota, token.UsedQuota, user.UsedQuota, user.RequestCount, channel.UsedQuota)
	}
	for _, check := range []struct {
		model  any
		column string
		id     any
		want   int64
	}{{&BalanceLedger{}, "request_id", f.input.RequestID, ledgerCount}, {&ZTAPISupplierRefundCharge{}, "request_id", f.input.RequestID, charges}, {&ZTAPISettlementLogOutbox{}, "operation_id", f.input.OperationID, charges}, {&ZTAPISettlementFinalizationIntent{}, "request_id", f.input.RequestID, 1}, {&ZTAPIPendingResolution{}, "settlement_id", row.ID, 0}} {
		var count int64
		if err := DB.Model(check.model).Where(check.column+" = ?", check.id).Count(&count).Error; err != nil || count != check.want {
			t.Fatalf("%T rows=%d want=%d err=%v", check.model, count, check.want, err)
		}
	}
	intent := f.intent(t)
	if (intent.CompletedAt != 0) != settled {
		t.Fatalf("intent completion disagrees with financial commit: %+v", intent)
	}
}

func (f commercialMySQLFixture) assertRecoveredIntent(t *testing.T, original ZTAPISettlementFinalizationIntent) {
	t.Helper()
	current := f.intent(t)
	if current.OperationID != original.OperationID || current.RequestID != original.RequestID || current.ActualQuota != original.ActualQuota || current.UsageJSON != original.UsageJSON || current.ChargeDimensionsJSON != original.ChargeDimensionsJSON || current.EvidenceJSON != original.EvidenceJSON || current.PayloadSHA256 != original.PayloadSHA256 || current.CreatedAt != original.CreatedAt {
		t.Fatal("recovery rewrote immutable finalization intent")
	}
	var outbox ZTAPISettlementLogOutbox
	if err := DB.Where("operation_id = ?", f.input.OperationID).Take(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	var log Log
	if err := common.UnmarshalJsonStr(outbox.PayloadJSON, &log); err != nil {
		t.Fatal(err)
	}
	if log.UserId != f.user.Id || log.TokenId != f.token.Id || log.RequestId != f.input.RequestID || log.ChannelId != f.channel.Id || log.ModelName != f.input.PublicModel || int64(log.Quota) != original.ActualQuota || log.Type != LogTypeConsume || log.CreatedAt != 1 || log.UpstreamRequestId != "wire-"+f.input.RequestID || !outbox.ChannelStatsApplied {
		t.Fatalf("recovery changed original consume evidence: %+v", log)
	}
}

func (f commercialMySQLFixture) proof(t *testing.T, id, mode, units string) *ZTAPISupplierRefund {
	t.Helper()
	input := ZTAPISupplierRefundSubmission{Source: "test-supplier", ProofID: f.input.RequestID + ":" + id, RequestID: f.input.RequestID, UserID: f.user.Id, Attempt: 1, ChannelID: f.channel.Id, CredentialVersion: "test-credential-v1", UpstreamRequestID: "wire-" + f.input.RequestID, Mode: mode, EvidenceReference: "synthetic-statement:" + id}
	if units != "" {
		input.Units = []ZTAPISupplierRefundUnits{{"input", units}}
	}
	p, err := SubmitZTAPISupplierRefund(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApproveZTAPISupplierRefund(p.ID, f.admin.Id, "synthetic-finance-review:"+id); err != nil {
		t.Fatal(err)
	}
	return p
}

func commercialConcurrent(t *testing.T, count int, fn func() error) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- fn() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrBalanceLedgerCacheSync) {
			t.Fatal(err)
		}
	}
}

func commercialMySQLApprovedNoChargeConcurrentRelease(t *testing.T) {
	f := newCommercialMySQLFixture(t, DB, 1000)
	row, err := BeginZTAPIRequestSettlement(f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.prepareAttempt(t)
	if _, err = PendZTAPIRequestSettlement(f.input.OperationID, "{}", `["upstream_usage_missing"]`); err != nil {
		t.Fatal(err)
	}
	if err = DB.First(row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = DB.Transaction(func(tx *gorm.DB) error { return EnsureZTAPIAttemptBillingReviewsTx(tx, row) }); err != nil {
		t.Fatal(err)
	}

	input := ZTAPIAttemptBillingSubmission{
		Source: "test-supplier", ProofID: f.input.RequestID + ":no-charge", RequestID: f.input.RequestID,
		UserID: f.user.Id, Attempt: 1, ChannelID: f.channel.Id, CredentialVersion: "test-credential-v1",
		UpstreamRequestID: "wire-" + f.input.RequestID, Kind: "nocharge", EvidenceReference: "synthetic-statement:no-charge",
	}
	proof, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	commercialConcurrent(t, 16, func() error {
		return ApproveZTAPIAttemptBilling(proof.ID, f.admin.Id, "synthetic-finance-review:no-charge", nil)
	})

	// Simulate a crash after durable approval and customer release but before the
	// proof status was closed, then race manual and background recovery paths.
	if err = DB.Model(&ZTAPIAttemptBillingProof{}).Where("id = ?", proof.ID).Updates(map[string]any{
		"status": "approved", "pending_reason": "nocharge_release_pending",
	}).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); errs <- ProcessZTAPIAttemptBilling(proof.ID, nil) }()
		go func() { defer wg.Done(); errs <- RetryZTAPIAttemptBillings(100, nil) }()
	}
	wg.Wait()
	close(errs)
	for err = range errs {
		if err != nil && !errors.Is(err, ErrBalanceLedgerCacheSync) {
			t.Fatal(err)
		}
	}

	assertZTAPISettlementBalances(t, DB, *row, 1000, 1000)
	var saved ZTAPIRequestSettlement
	var savedProof ZTAPIAttemptBillingProof
	if err = DB.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = DB.First(&savedProof, proof.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != ZTAPISettlementReleased || savedProof.Status != "completed" || savedProof.PendingReason != "" {
		t.Fatalf("no-charge recovery incomplete settlement=%+v proof=%+v", saved, savedProof)
	}
	for _, check := range []struct {
		model any
		query string
		args  []any
		want  int64
	}{
		{&BalanceLedger{}, "request_id = ?", []any{f.input.RequestID}, 2},
		{&BalanceLedger{}, "request_id = ? AND source_type = ?", []any{f.input.RequestID, BalanceLedgerSourceBillingRefund}, 1},
		{&ZTAPIPendingResolution{}, "settlement_id = ?", []any{row.ID}, 1},
		{&ZTAPIAttemptBillingApproval{}, "proof_id = ?", []any{proof.ID}, 1},
	} {
		var count int64
		if err = DB.Model(check.model).Where(check.query, check.args...).Count(&count).Error; err != nil || count != check.want {
			t.Fatalf("%T count=%d want=%d err=%v", check.model, count, check.want, err)
		}
	}
}

func commercialMySQLApprovedNoChargePreReleaseReconnect(t *testing.T, open func() *gorm.DB) {
	f := newCommercialMySQLFixture(t, DB, 1000)
	row, err := BeginZTAPIRequestSettlement(f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.prepareAttempt(t)
	if _, err = PendZTAPIRequestSettlement(f.input.OperationID, "{}", `["upstream_usage_missing"]`); err != nil {
		t.Fatal(err)
	}
	if err = DB.First(row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = DB.Transaction(func(tx *gorm.DB) error { return EnsureZTAPIAttemptBillingReviewsTx(tx, row) }); err != nil {
		t.Fatal(err)
	}
	input := ZTAPIAttemptBillingSubmission{
		Source: "test-supplier", ProofID: f.input.RequestID + ":pre-release-no-charge", RequestID: f.input.RequestID,
		UserID: f.user.Id, Attempt: 1, ChannelID: f.channel.Id, CredentialVersion: "test-credential-v1",
		UpstreamRequestID: "wire-" + f.input.RequestID, Kind: "nocharge", EvidenceReference: "synthetic-statement:pre-release",
	}
	proof, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}

	faultDB := DB
	callback := "commercial-test:fail-nocharge-resolution"
	injected := errors.New("injected no-charge resolution failure")
	if err = faultDB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*ZTAPIPendingResolution); ok {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	registered := true
	t.Cleanup(func() {
		if registered {
			_ = faultDB.Callback().Create().Remove(callback)
		}
	})
	if err = ApproveZTAPIAttemptBilling(proof.ID, f.admin.Id, "synthetic-finance-review:pre-release", nil); !errors.Is(err, injected) {
		t.Fatalf("approval did not stop in the pre-release window: %v", err)
	}
	if err = faultDB.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	registered = false

	assertZTAPISettlementBalances(t, DB, *row, 900, 900)
	var pending ZTAPIRequestSettlement
	var approved ZTAPIAttemptBillingProof
	if err = DB.First(&pending, row.ID).Error; err != nil || pending.Status != ZTAPISettlementPending {
		t.Fatalf("approval failure did not preserve pending hold: %+v err=%v", pending, err)
	}
	if err = DB.First(&approved, proof.ID).Error; err != nil || approved.Status != "approved" || approved.PendingReason != "nocharge_release_pending" {
		t.Fatalf("approval was not durably recorded: %+v err=%v", approved, err)
	}

	sqlDB, err := DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	DB = open()
	start := make(chan struct{})
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); <-start; errs <- ProcessZTAPIAttemptBilling(proof.ID, nil) }()
		go func() { defer wg.Done(); <-start; errs <- RetryZTAPIAttemptBillings(100, nil) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err = range errs {
		if err != nil && !errors.Is(err, ErrBalanceLedgerCacheSync) {
			t.Fatal(err)
		}
	}

	assertZTAPISettlementBalances(t, DB, *row, 1000, 1000)
	if err = DB.First(&pending, row.ID).Error; err != nil || pending.Status != ZTAPISettlementReleased {
		t.Fatalf("restart did not release pending reservation: %+v err=%v", pending, err)
	}
	if err = DB.First(&approved, proof.ID).Error; err != nil || approved.Status != "completed" || approved.PendingReason != "" {
		t.Fatalf("restart did not close approved proof: %+v err=%v", approved, err)
	}
	for _, check := range []struct {
		model any
		query string
		args  []any
		want  int64
	}{
		{&BalanceLedger{}, "request_id = ?", []any{f.input.RequestID}, 2},
		{&ZTAPIPendingResolution{}, "settlement_id = ?", []any{row.ID}, 1},
		{&ZTAPIAttemptBillingApproval{}, "proof_id = ?", []any{proof.ID}, 1},
	} {
		var count int64
		if err = DB.Model(check.model).Where(check.query, check.args...).Count(&count).Error; err != nil || count != check.want {
			t.Fatalf("%T count=%d want=%d err=%v", check.model, count, check.want, err)
		}
	}
}

func commercialMySQLConcurrentSupplierChargeClaim(t *testing.T) {
	f := newCommercialMySQLFixture(t, DB, 1000)
	if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
		t.Fatal(err)
	}
	f.prepareAttempt(t)
	if _, err := f.finalize(40); err != nil {
		t.Fatal(err)
	}
	var charge ZTAPISupplierRefundCharge
	if err := DB.Where("request_id = ? AND attempt = 1", f.input.RequestID).Take(&charge).Error; err != nil {
		t.Fatal(err)
	}
	if charge.UpstreamBillID != "" {
		t.Fatal("fixture charge must be unclaimed")
	}
	charge.DimensionsJSON = `[{"dimension":"input_tokens","units":"40","unit_quota":"1","charged_quota":40}]`
	if err := DB.Model(&ZTAPISupplierRefundCharge{}).Where("id = ?", charge.ID).UpdateColumn("dimensions_json", charge.DimensionsJSON).Error; err != nil {
		t.Fatal(err)
	}

	inputs := make([]ZTAPISupplierReconciliationImport, 2)
	for i := range inputs {
		record := validZTAPISupplierLedgerRecord(fmt.Sprintf("concurrent-bill-%d-%s", i+1, f.input.RequestID))
		record.RequestID = "wire-" + f.input.RequestID
		record.CredentialRef = "test-credential-v1"
		input := validZTAPISupplierImport(record)
		input.OperatorID = f.admin.Id
		input.IdempotencyKey = fmt.Sprintf("concurrent-claim-%d-%s", i+1, f.input.RequestID)
		input.FileChecksum = ztapiSupplierRefundHash(input.IdempotencyKey + ":file")
		input.PayloadHash = ztapiSupplierRefundHash(input.IdempotencyKey + ":payload")
		preview, err := PrepareZTAPISupplierReconciliation(DB, input)
		if err != nil {
			t.Fatal(err)
		}
		if preview.Matched != 1 || preview.Unmatched != 0 {
			t.Fatalf("invalid concurrent claim preview: %+v", preview)
		}
		input.DryRun = false
		inputs[i] = input
	}

	start := make(chan struct{})
	results := make(chan *ZTAPISupplierReconciliationResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range inputs {
		wg.Add(1)
		go func(input ZTAPISupplierReconciliationImport) {
			defer wg.Done()
			<-start
			result, err := PrepareZTAPISupplierReconciliation(DB, input)
			results <- result
			errs <- err
		}(inputs[i])
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	conflicts := 0
	for err := range errs {
		if errors.Is(err, ErrZTAPISupplierReconciliationConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	existing := 0
	for result := range results {
		if result == nil {
			continue
		}
		if len(result.Entries) != 1 {
			t.Fatalf("invalid successful reconciliation result: %+v", result)
		}
		switch result.Entries[0].ActionKind {
		case ZTAPISupplierReconciliationActionExistingCharge:
			existing++
		default:
			t.Fatalf("unexpected action: %+v", result.Entries[0])
		}
	}
	if existing != 1 || conflicts != 1 {
		t.Fatalf("charge claim results existing=%d conflicts=%d", existing, conflicts)
	}
	var claim ZTAPISupplierReconciliationChargeClaim
	if err := DB.First(&claim, charge.ID).Error; err != nil {
		t.Fatal(err)
	}
	loser := inputs[0].Records[0]
	if loser.SupplierRecordID == claim.SupplierRecordID {
		loser = inputs[1].Records[0]
	}
	recheck := validZTAPISupplierImport(loser)
	recheck.OperatorID = f.admin.Id
	recheck.IdempotencyKey = "concurrent-claim-recheck-" + f.input.RequestID
	recheck.FileChecksum = ztapiSupplierRefundHash(recheck.IdempotencyKey + ":file")
	recheck.PayloadHash = ztapiSupplierRefundHash(recheck.IdempotencyKey + ":payload")
	classified, err := PrepareZTAPISupplierReconciliation(DB, recheck)
	if err != nil || classified.Matched != 0 || classified.Unmatched != 1 {
		t.Fatalf("losing supplier bill not classified as unmatched: %+v err=%v", classified, err)
	}
}

func (f commercialMySQLFixture) twoAttempts(t *testing.T, pending bool) (ZTAPIRequestSettlement, ZTAPISettlementEvidence, ZTAPIAttemptBillingSubmission) {
	t.Helper()
	row, err := BeginZTAPIRequestSettlement(f.input)
	if err != nil {
		t.Fatal(err)
	}
	pool := Channel{Name: "cm-pool-" + f.input.RequestID, Type: 1, Status: 1}
	if err := DB.Create(&pool).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DB.Where("id = ?", pool.Id).Delete(&Channel{}).Error })
	wire := strings.Repeat("w", 200-len(f.input.RequestID)) + f.input.RequestID
	for attempt := 1; attempt <= 2; attempt++ {
		channelID := pool.Id
		if attempt == 2 {
			channelID = f.channel.Id
		}
		if _, err := BeginZTAPIRequestAttempt(f.input.OperationID, channelID, "test-credential-v1", "chat"); err != nil {
			t.Fatal(err)
		}
		id, status := wire, 502
		if attempt == 2 {
			id, status = "final-"+f.input.RequestID, 200
		}
		if err := RecordZTAPIRequestAttemptResponse(f.input.OperationID, attempt, channelID, status, id); err != nil {
			t.Fatal(err)
		}
	}
	evidence := ZTAPISettlementEvidence{FinalAttempt: 2, ConsumeLog: Log{Type: LogTypeConsume, UserId: f.user.Id, TokenId: f.token.Id, RequestId: f.input.RequestID, ModelName: f.input.PublicModel, Quota: 100, PromptTokens: 100, ChannelId: f.channel.Id, CreatedAt: 1, UpstreamRequestId: "final-" + f.input.RequestID}}
	if pending {
		row, err = PendZTAPIRequestSettlement(f.input.OperationID, `{"native_usage":{"input_tokens":17}}`, `["cache_read"]`)
	} else {
		row, err = FinalizeZTAPIRequestSettlementWithEvidence(f.input.OperationID, 100, `{"input_tokens":100}`, `[{"dimension":"input_tokens","units":"100","unit_quota":"1","charged_quota":100}]`, evidence)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := DB.Transaction(func(tx *gorm.DB) error { return EnsureZTAPIAttemptBillingReviewsTx(tx, row) }); err != nil {
		t.Fatal(err)
	}
	input := ZTAPIAttemptBillingSubmission{Source: "commercial-test-supplier", ProofID: f.input.RequestID + ":bill", RequestID: f.input.RequestID, UserID: f.user.Id, Attempt: 1, ChannelID: pool.Id, CredentialVersion: "test-credential-v1", UpstreamRequestID: wire, UpstreamBillID: f.input.RequestID + ":line", Kind: "billed", UsageSemantic: "openai", Usage: []ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 40}}, EvidenceReference: "synthetic supplier bill line", DistinctUsageReference: "finance verified separate executions and bill lines"}
	return *row, evidence, input
}

func commercialMySQLAttemptCharges(t *testing.T) {
	f := newCommercialMySQLFixture(t, DB, 1000)
	row, evidence, input := f.twoAttempts(t, false)
	p, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	commercialConcurrent(t, 8, func() error {
		if err := ApproveZTAPIAttemptBilling(p.ID, f.admin.Id, "verified distinct bill line", attemptBillingTestPricer); err != nil {
			return err
		}
		return ProcessZTAPIAttemptBilling(p.ID, attemptBillingTestLog)
	})
	var user User
	if err := DB.First(&user, f.user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if user.Quota != 860 || user.UsedQuota != 140 || user.RequestCount != 1 {
		t.Fatalf("duplicate additional debit/counter: %+v", user)
	}
	var approvalCount int64
	if err := DB.Model(&ZTAPIAttemptBillingApproval{}).Where("proof_id = ?", p.ID).Count(&approvalCount).Error; err != nil || approvalCount != 1 {
		t.Fatalf("approvals=%d %v", approvalCount, err)
	}
	var charges []ZTAPISupplierRefundCharge
	if err := DB.Where("request_id = ?", row.RequestID).Order("attempt").Find(&charges).Error; err != nil || len(charges) != 2 {
		t.Fatalf("charges=%d %v", len(charges), err)
	}
	var jobs []ZTAPISettlementLogOutbox
	if err := DB.Where("operation_id IN ?", []string{row.OperationID, charges[0].BillingOperationID}).Find(&jobs).Error; err != nil || len(jobs) != 2 {
		t.Fatalf("outboxes=%d %v", len(jobs), err)
	}
	for _, job := range jobs {
		id, err := deliverZTAPISettlementLog(DB, job)
		if err != nil {
			t.Fatal(err)
		}
		var log Log
		if err := DB.First(&log, id).Error; err != nil {
			t.Fatal(err)
		}
		if job.OperationID == charges[0].BillingOperationID && log.UpstreamRequestId != input.UpstreamRequestID {
			t.Fatal("200-char additional wire ID truncated")
		}
	}
	refund := func(charge ZTAPISupplierRefundCharge, name, mode, units string) *ZTAPISupplierRefund {
		t.Helper()
		sub := ZTAPISupplierRefundSubmission{Source: input.Source, ProofID: row.RequestID + ":" + name, RequestID: row.RequestID, UserID: row.UserID, Attempt: charge.Attempt, ChannelID: charge.ChannelID, CredentialVersion: charge.CredentialVersion, UpstreamRequestID: charge.UpstreamRequestID, UpstreamBillID: charge.UpstreamBillID, Mode: mode, EvidenceReference: "synthetic verified refund line"}
		if units != "" {
			sub.Units = []ZTAPISupplierRefundUnits{{Dimension: "input_tokens", Units: units}}
		}
		r, err := SubmitZTAPISupplierRefund(sub)
		if err != nil {
			t.Fatal(err)
		}
		if err = ApproveZTAPISupplierRefund(r.ID, f.admin.Id, "verified refund"); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for i, charge := range charges {
		units, excess := "30", "20"
		if i == 1 {
			units, excess = "70", "40"
		}
		r := refund(charge, fmt.Sprintf("partial-%d", i), "partial", units)
		commercialConcurrent(t, 8, func() error { return ProcessZTAPISupplierRefund(r.ID) })
		over := refund(charge, fmt.Sprintf("over-%d", i), "partial", excess)
		if err := ProcessZTAPISupplierRefund(over.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
			t.Fatalf("attempt %d exceeded its own cap: %v", charge.Attempt, err)
		}
	}
	assertZTAPISettlementBalances(t, DB, row, 960, 960)
	for i, charge := range charges {
		r := refund(charge, fmt.Sprintf("full-%d", i), "full", "")
		commercialConcurrent(t, 8, func() error { return ProcessZTAPISupplierRefund(r.ID) })
	}
	assertZTAPISettlementBalances(t, DB, row, 1000, 1000)
	if _, err := FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, row.UsageJSON, row.ChargeDimensionsJSON, evidence); err != nil {
		t.Fatal(err)
	}
	var saved ZTAPIRequestSettlement
	if err := DB.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ChargedQuota != 100 || saved.RefundedQuota != 140 || saved.FinalAttempt != 2 || saved.ChargeDimensionsJSON != row.ChargeDimensionsJSON {
		t.Fatalf("original final charge changed: %+v", saved)
	}
}

func commercialMySQLApprovedFinalReconnect(t *testing.T, open func() *gorm.DB) {
	f := newCommercialMySQLFixture(t, DB, 1000)
	row, _, input := f.twoAttempts(t, true)
	input.Attempt = 2
	input.ChannelID = f.channel.Id
	input.UpstreamRequestID = strings.Repeat("f", 200-len(row.RequestID)) + row.RequestID
	if err := DB.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ? AND attempt = 2", row.ID).UpdateColumn("upstream_request_id", input.UpstreamRequestID).Error; err != nil {
		t.Fatal(err)
	}
	input.Usage[0].Quantity = 60
	p, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(p.ID, f.admin.Id, "verified final bill", attemptBillingTestPricer); err != nil {
		t.Fatal(err)
	}
	original := f.intent(t)
	var approval ZTAPIAttemptBillingApproval
	if err = DB.Where("proof_id = ?", p.ID).Take(&approval).Error; err != nil {
		t.Fatal(err)
	}
	if approval.PendingUsageJSON != row.UsageJSON || approval.PendingMissingDimensionsJSON != row.MissingDimensionsJSON {
		t.Fatal("pending audit preimage lost")
	}
	assertZTAPISettlementBalances(t, DB, row, 900, 900)
	sql, err := DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err = sql.Close(); err != nil {
		t.Fatal(err)
	}
	DB = open()
	commercialConcurrent(t, 8, func() error { return RetryZTAPISettlementFinalizations(1000) })
	commercialConcurrent(t, 8, func() error { return ProcessZTAPIAttemptBilling(p.ID, nil) })
	assertZTAPISettlementBalances(t, DB, row, 940, 940)
	if current := f.intent(t); current.PayloadSHA256 != original.PayloadSHA256 || current.EvidenceJSON != original.EvidenceJSON || current.CompletedAt == 0 {
		t.Fatal("approved final intent changed or not completed")
	}
	var user User
	if err := DB.First(&user, f.user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if user.UsedQuota != 60 || user.RequestCount != 1 {
		t.Fatalf("final recovery duplicated billing: %+v", user)
	}
	var review ZTAPIAttemptBillingReview
	if err := DB.Where("request_id = ? AND attempt = 2", row.RequestID).Take(&review).Error; err != nil || review.Status != "verified_billed" {
		t.Fatalf("ghost final review %+v %v", review, err)
	}
	var job ZTAPISettlementLogOutbox
	if err := DB.Where("operation_id = ?", row.OperationID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	id, err := deliverZTAPISettlementLog(DB, job)
	if err != nil {
		t.Fatal(err)
	}
	var log Log
	if err := DB.First(&log, id).Error; err != nil || log.UpstreamRequestId != input.UpstreamRequestID {
		t.Fatalf("final 200-char wire ID lost: %+v %v", log, err)
	}
	var final ZTAPISupplierRefundCharge
	if err := DB.Where("request_id = ? AND attempt = 2", row.RequestID).Take(&final).Error; err != nil || final.UpstreamBillID != input.UpstreamBillID || final.UpstreamRequestID != input.UpstreamRequestID {
		t.Fatalf("final refund anchor lost lineage: %+v %v", final, err)
	}
}

func commercialMySQLLegacyMigration(t *testing.T) {
	suffix := ztapiSupplierRefundHash(t.Name() + time.Now().String())[:10]
	table := "cm_charge_migration_" + suffix
	if err := DB.Exec("CREATE TABLE " + table + " LIKE ztapi_supplier_refund_charges").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DB.Migrator().DropTable(table) })
	if err := DB.Migrator().DropIndex(table, "ztapi_refund_charge_attempt"); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"request_id", "settlement_id"} {
		name := DB.NamingStrategy.IndexName(table, column)
		if err := DB.Exec("CREATE UNIQUE INDEX " + name + " ON " + table + " (" + column + ")").Error; err != nil {
			t.Fatal(err)
		}
	}
	original := ZTAPISupplierRefundCharge{RequestID: "cm-migration-" + suffix, SettlementID: 1, Attempt: 2, PriceSnapshotJSON: "{}", DimensionsJSON: "[]", ProgressJSON: "[]"}
	if err := DB.Table(table).Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := DB.Table(table).AutoMigrate(&ZTAPISupplierRefundCharge{}); err != nil {
			t.Fatal(err)
		}
		if err := migrateZTAPISupplierRefundChargeIndexes(DB, table); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"request_id", "settlement_id"} {
		if DB.Migrator().HasIndex(table, DB.NamingStrategy.IndexName(table, column)) {
			t.Fatal("obsolete single uniqueness survived")
		}
	}
	if !DB.Migrator().HasIndex(table, "ztapi_refund_charge_attempt") {
		t.Fatal("composite unique missing")
	}
	extra := original
	extra.ID = 0
	extra.Attempt = 1
	if err := DB.Table(table).Create(&extra).Error; err != nil {
		t.Fatal(err)
	}
	extra.ID = 0
	if err := DB.Table(table).Create(&extra).Error; err == nil {
		t.Fatal("duplicate request+attempt accepted")
	}
	var count int64
	if err := DB.Table(table).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("migration lost or duplicated original rows: %d %v", count, err)
	}
	logTable := "cm_log_migration_" + suffix
	if err := DB.Exec("CREATE TABLE " + logTable + " LIKE logs").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DB.Migrator().DropTable(logTable) })
	if err := DB.Exec("ALTER TABLE " + logTable + " MODIFY upstream_request_id VARCHAR(128) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Table(logTable).AutoMigrate(&Log{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{logTable, "logs"} {
		var capacity int64
		if err := DB.Raw("SELECT CHARACTER_MAXIMUM_LENGTH FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'upstream_request_id'", name).Scan(&capacity).Error; err != nil || capacity < 256 {
			t.Fatalf("log capacity %s=%d %v", name, capacity, err)
		}
	}
	log := Log{UpstreamRequestId: strings.Repeat("w", 200), RequestId: "cm-log-" + suffix}
	if err := DB.Table(logTable).Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	var saved Log
	if err := DB.Table(logTable).First(&saved, log.Id).Error; err != nil || saved.UpstreamRequestId != log.UpstreamRequestId {
		t.Fatalf("widen migration truncated wire ID: %v", err)
	}
}

func commercialMySQLLateWireOverlap(t *testing.T) {
	f := newCommercialMySQLFixture(t, DB, 1000)
	row, _, input := f.twoAttempts(t, true)
	if err := DB.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ? AND attempt = 1", row.ID).UpdateColumn("upstream_request_id", "").Error; err != nil {
		t.Fatal(err)
	}
	earlierChannelID := input.ChannelID
	input.Attempt, input.ChannelID, input.UpstreamRequestID = 2, f.channel.Id, "final-"+row.RequestID
	p, err := SubmitZTAPIAttemptBilling(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPIAttemptBilling(p.ID, f.admin.Id, "verified final before late observation", attemptBillingTestPricer); err != nil {
		t.Fatal(err)
	}
	original := f.intent(t)
	if err = RecordZTAPIRequestAttemptResponse(row.OperationID, 1, earlierChannelID, 502, input.UpstreamRequestID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = RetryZTAPISettlementFinalizations(1000); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
			t.Fatalf("generic retry accepted late wire overlap: %v", err)
		}
		if err = ProcessZTAPIAttemptBilling(p.ID, nil); !errors.Is(err, ErrZTAPIAttemptBillingPending) {
			t.Fatalf("attempt retry accepted late wire overlap: %v", err)
		}
	}
	assertZTAPISettlementBalances(t, DB, row, 900, 900)
	var saved ZTAPIRequestSettlement
	if err = DB.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != ZTAPISettlementPending || saved.ChargedQuota != 0 || saved.FinalAttempt != 0 {
		t.Fatalf("ambiguous settlement committed: %+v", saved)
	}
	current := f.intent(t)
	if current.CompletedAt != 0 || current.PayloadSHA256 != original.PayloadSHA256 || current.EvidenceJSON != original.EvidenceJSON {
		t.Fatal("ambiguous intent changed or completed")
	}
	var proof ZTAPIAttemptBillingProof
	if err = DB.First(&proof, p.ID).Error; err != nil || proof.Status != "approved" || proof.ChargeID != 0 || proof.PendingReason != "overlapping_billed_usage" {
		t.Fatalf("ambiguous proof cleared: %+v %v", proof, err)
	}
	var user User
	var token Token
	var channel Channel
	for _, query := range []*gorm.DB{DB.First(&user, f.user.Id), DB.First(&token, f.token.Id), DB.First(&channel, f.channel.Id)} {
		if query.Error != nil {
			t.Fatal(query.Error)
		}
	}
	if user.UsedQuota != 0 || user.RequestCount != 0 || token.UsedQuota != 100 || channel.UsedQuota != 0 {
		t.Fatalf("financial counters escaped rollback: used=%d requests=%d tokenused=%d channel=%d", user.UsedQuota, user.RequestCount, token.UsedQuota, channel.UsedQuota)
	}
	for _, check := range []struct {
		model  any
		column string
		value  any
		want   int64
	}{
		{&BalanceLedger{}, "request_id", row.RequestID, 1},
		{&ZTAPISupplierRefundCharge{}, "request_id", row.RequestID, 0},
		{&ZTAPISettlementLogOutbox{}, "operation_id", row.OperationID, 0},
	} {
		var count int64
		if err := DB.Model(check.model).Where(check.column+" = ?", check.value).Count(&count).Error; err != nil || count != check.want {
			t.Fatalf("%T count=%d want=%d %v", check.model, count, check.want, err)
		}
	}
}

func TestZTAPICommercialMySQLIntegration(t *testing.T) {
	required := strings.EqualFold(strings.TrimSpace(os.Getenv("ZTAPI_REQUIRE_COMMERCIAL_MYSQL")), "true")
	safe, err := commercialMySQLTestDSN(os.Getenv("ZTAPI_COMMERCIAL_MYSQL_TEST_DSN"), required)
	if err != nil {
		t.Fatal(err)
	}
	if safe == "" {
		t.Skip("dedicated loopback commercial MySQL DSN absent; real MySQL financial integrity NOT executed")
	}
	open := func() *gorm.DB {
		db, e := gorm.Open(gormmysql.Open(safe), &gorm.Config{})
		if e != nil {
			t.Fatal("cannot open dedicated commercial MySQL test database")
		}
		sql, e := db.DB()
		if e != nil {
			t.Fatal(e)
		}
		sql.SetMaxOpenConns(16)
		return db
	}
	db := open()
	originalDB, originalRedis := DB, common.RedisEnabled
	DB = db
	common.RedisEnabled = false
	t.Cleanup(func() { sql, _ := DB.DB(); _ = sql.Close(); DB = originalDB; common.RedisEnabled = originalRedis })
	if err := db.AutoMigrate(&User{}, &Token{}, &Channel{}, &BalanceLedger{}, &ZTAPIRequestSettlement{}, &ZTAPIMediaTask{}, &ZTAPISettlementFinalizationIntent{}, &ZTAPIRequestAttempt{}, &ZTAPIPendingResolution{}, &ZTAPIAttemptBillingReview{}, &ZTAPIAttemptBillingProof{}, &ZTAPIAttemptBillingApproval{}, &Log{}, &ZTAPISettlementLogReceipt{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISupplierRefund(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISettlementLogOutbox(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPIFinanceAlerts(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISupplierReconciliation(db); err != nil {
		t.Fatal(err)
	}
	if err := registerZTAPIMediaTaskMutationGuard(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"users", "tokens", "channels", "balance_ledgers", "ztapi_request_settlements", "ztapi_media_tasks", "ztapi_request_attempts", "ztapi_supplier_refund_charges", "ztapi_supplier_refunds", "ztapi_supplier_refund_approvals", "ztapi_settlement_log_outboxes", "ztapi_settlement_finalization_intents", "ztapi_pending_resolutions", "ztapi_attempt_billing_reviews", "ztapi_attempt_billing_proofs", "ztapi_attempt_billing_approvals", "ztapi_finance_alert_outboxes", "ztapi_supplier_reconciliation_batches", "ztapi_supplier_reconciliation_entries", "ztapi_supplier_reconciliation_charge_claims", "ztapi_supplier_reconciliation_actions", "ztapi_supplier_reconciliation_batch_entries", "logs", "ztapi_settlement_log_receipts"} {
		var engine string
		if err := db.Raw("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&engine).Error; err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(engine, "InnoDB") {
			t.Fatalf("%s is not InnoDB", table)
		}
	}
	t.Run("attempt_concurrent_approve_apply_and_independent_refund_caps", commercialMySQLAttemptCharges)
	t.Run("approved_nocharge_concurrent_release_and_recovery", commercialMySQLApprovedNoChargeConcurrentRelease)
	t.Run("approved_nocharge_pre_release_crash_reconnect", func(t *testing.T) { commercialMySQLApprovedNoChargePreReleaseReconnect(t, open) })
	t.Run("concurrent_supplier_batches_claim_charge_once", commercialMySQLConcurrentSupplierChargeClaim)
	t.Run("approved_pending_final_crash_reconnect_intent", func(t *testing.T) { commercialMySQLApprovedFinalReconnect(t, open) })
	t.Run("legacy_single_unique_to_composite_and_log_width_migration", commercialMySQLLegacyMigration)
	t.Run("approved_final_late_wire_overlap_blocks_generic_retry", commercialMySQLLateWireOverlap)
	t.Run("concurrent_begin_finalize_refund_replay", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 1000)
		commercialConcurrent(t, 8, func() error { _, err := BeginZTAPIRequestSettlement(f.input); return err })
		f.prepareAttempt(t)
		commercialConcurrent(t, 8, func() error { _, err := f.finalize(100); return err })
		p := f.proof(t, "full", "full", "")
		commercialConcurrent(t, 8, func() error { return ProcessZTAPISupplierRefund(p.ID) })
		if err := ProcessZTAPISupplierRefund(p.ID); err != nil {
			t.Fatal(err)
		}
		var user User
		var token Token
		var row ZTAPIRequestSettlement
		if err := DB.First(&user, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if err := DB.First(&token, f.token.Id).Error; err != nil {
			t.Fatal(err)
		}
		if err := DB.Where("request_id = ?", f.input.RequestID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if user.Quota != 1000 || user.UsedQuota != 100 || user.RequestCount != 1 || token.RemainQuota != 1000 || token.UsedQuota != 0 || row.RefundedQuota != 100 {
			t.Fatalf("non-idempotent financial result user=%+v token quota=%d used=%d row=%+v", user, token.RemainQuota, token.UsedQuota, row)
		}
		var count int64
		if err := DB.Model(&BalanceLedger{}).Where("request_id = ?", f.input.RequestID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("ledgers=%d want original debit+one refund", count)
		}
	})
	t.Run("three_partial_proofs_cap", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 1000)
		if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
			t.Fatal(err)
		}
		f.prepareAttempt(t)
		if _, err := f.finalize(100); err != nil {
			t.Fatal(err)
		}
		proofs := []*ZTAPISupplierRefund{f.proof(t, "part1", "partial", "40"), f.proof(t, "part2", "partial", "40"), f.proof(t, "part3", "partial", "40")}
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		for _, p := range proofs {
			wg.Add(1)
			go func(id uint) { defer wg.Done(); errs <- ProcessZTAPISupplierRefund(id) }(p.ID)
		}
		wg.Wait()
		close(errs)
		pending := 0
		for err := range errs {
			if errors.Is(err, ErrZTAPISupplierRefundPending) {
				pending++
			} else if err != nil && !errors.Is(err, ErrBalanceLedgerCacheSync) {
				t.Fatal(err)
			}
		}
		if pending != 1 {
			t.Fatalf("pending=%d want1", pending)
		}
		var user User
		if err := DB.First(&user, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if user.Quota != 980 {
			t.Fatalf("partial cap wallet=%d", user.Quota)
		}
	})
	t.Run("token_failure_rolls_back_and_restart_replays", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 1000)
		if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
			t.Fatal(err)
		}
		f.prepareAttempt(t)
		if _, err := f.finalize(100); err != nil {
			t.Fatal(err)
		}
		p := f.proof(t, "rollback", "full", "")
		const callback = "commercial-test:fail-token"
		if err := DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "tokens" {
				_ = tx.AddError(errors.New("injected token failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		if err := ProcessZTAPISupplierRefund(p.ID); err == nil {
			t.Fatal("token failure ignored")
		}
		if err := DB.Callback().Update().Remove(callback); err != nil {
			t.Fatal(err)
		}
		var user User
		if err := DB.First(&user, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if user.Quota != 900 {
			t.Fatal("wallet escaped token rollback")
		}
		sql, _ := DB.DB()
		if err := sql.Close(); err != nil {
			t.Fatal(err)
		}
		DB = open()
		if err := ProcessZTAPISupplierRefund(p.ID); err != nil {
			t.Fatal(err)
		}
		if err := ProcessZTAPISupplierRefund(p.ID); err != nil {
			t.Fatal(err)
		}
		if err := DB.First(&user, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if user.Quota != 1000 {
			t.Fatal("restart did not apply exactly once")
		}
	})
	t.Run("debt_refund_replenishes_original_account", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 100)
		if err := DB.Model(&Token{}).Where("id = ?", f.token.Id).Update("remain_quota", 100).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
			t.Fatal(err)
		}
		f.prepareAttempt(t)
		if _, err := f.finalize(300); err != nil {
			t.Fatal(err)
		}
		var user User
		if err := DB.First(&user, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if user.Quota != -200 || !user.DebtSuspended {
			t.Fatalf("missing durable debt: quota=%d suspended=%v", user.Quota, user.DebtSuspended)
		}
		p := f.proof(t, "debt", "full", "")
		if err := ProcessZTAPISupplierRefund(p.ID); err != nil {
			t.Fatal(err)
		}
		if err := DB.First(&user, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if user.Quota != 100 || user.DebtSuspended || user.Status != common.UserStatusEnabled {
			t.Fatalf("debt restoration failed: quota=%d suspended=%v", user.Quota, user.DebtSuspended)
		}
	})
	t.Run("unknown_charge_remains_pending", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 1000)
		if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
			t.Fatal(err)
		}
		f.prepareAttempt(t)
		if _, err := PendZTAPIRequestSettlement(f.input.OperationID, "{}", `["input"]`); err != nil {
			t.Fatal(err)
		}
		p := f.proof(t, "unknown", "full", "")
		if err := ProcessZTAPISupplierRefund(p.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
			t.Fatalf("unknown bill refunded: %v", err)
		}
		var user User
		if err := DB.First(&user, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if user.Quota != 900 {
			t.Fatalf("unknown pending wallet=%d", user.Quota)
		}
	})
	t.Run("soft_deleted_owner_receives_excess_hold_credit", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 1000)
		f.input.ReservedQuota = 200
		if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
			t.Fatal(err)
		}
		f.prepareAttempt(t)
		if err := DB.Delete(&User{}, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := f.finalize(100); err != nil {
			t.Fatal(err)
		}
		var user User
		if err := DB.Unscoped().First(&user, f.user.Id).Error; err != nil {
			t.Fatal(err)
		}
		if user.Quota != 900 || !user.DeletedAt.Valid {
			t.Fatalf("deleted-owner credit lost or account resurrected: quota=%d deleted=%v", user.Quota, user.DeletedAt.Valid)
		}
	})
	t.Run("intent_persist_crash_reconnect_recovers_exactly_once", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 1000)
		if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
			t.Fatal(err)
		}
		f.prepareAttempt(t)
		usage, dims, evidence := f.finalizationInput(160)
		if err := saveZTAPISettlementFinalizationIntent(f.input.OperationID, 160, usage, dims, evidence); err != nil {
			t.Fatal(err)
		}
		original := f.intent(t)
		f.assertIntentFinancialState(t, 1000, 160, false)
		// No financial transaction ran. Discard the connection pool and recover
		// solely from the committed intent, not caller-held finalization inputs.
		sql, err := DB.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err = sql.Close(); err != nil {
			t.Fatal(err)
		}
		DB = open()
		commercialConcurrent(t, 8, func() error { return RetryZTAPISettlementFinalizations(1000) })
		if err = RetryZTAPISettlementFinalizations(1000); err != nil {
			t.Fatal(err)
		}
		f.assertIntentFinancialState(t, 1000, 160, true)
		f.assertRecoveredIntent(t, original)
	})
	t.Run("intent_final_tx_rollback_pending_reconnect_retry", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 1000)
		if _, err := BeginZTAPIRequestSettlement(f.input); err != nil {
			t.Fatal(err)
		}
		f.prepareAttempt(t)
		faultDB := DB
		callback := "commercial-test:fail-final-outbox"
		injected := errors.New("injected final outbox insertion failure")
		if err := faultDB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if job, ok := tx.Statement.Dest.(*ZTAPISettlementLogOutbox); ok && job.OperationID == f.input.OperationID {
				_ = tx.AddError(injected)
			}
		}); err != nil {
			t.Fatal(err)
		}
		registered := true
		t.Cleanup(func() {
			if registered {
				_ = faultDB.Callback().Create().Remove(callback)
			}
		})
		if _, err := f.finalize(160); !errors.Is(err, injected) {
			t.Fatalf("expected late financial transaction failure: %v", err)
		}
		if err := faultDB.Callback().Create().Remove(callback); err != nil {
			t.Fatal(err)
		}
		registered = false
		original := f.intent(t)
		f.assertIntentFinancialState(t, 1000, 160, false)
		if _, err := PendZTAPIRequestSettlement(f.input.OperationID, original.UsageJSON, `["settlement_retry_required"]`); err != nil {
			t.Fatal(err)
		}
		sql, err := DB.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err = sql.Close(); err != nil {
			t.Fatal(err)
		}
		DB = open()
		if err = RetryZTAPISettlementFinalizations(1000); err != nil {
			t.Fatal(err)
		}
		if err = RetryZTAPISettlementFinalizations(1000); err != nil {
			t.Fatal(err)
		}
		f.assertIntentFinancialState(t, 1000, 160, true)
		f.assertRecoveredIntent(t, original)
	})
	t.Run("intent_backed_pending_rejects_valid_no_charge_proof", func(t *testing.T) {
		f := newCommercialMySQLFixture(t, DB, 1000)
		row, err := BeginZTAPIRequestSettlement(f.input)
		if err != nil {
			t.Fatal(err)
		}
		f.prepareAttempt(t)
		usage, dims, evidence := f.finalizationInput(160)
		if err = saveZTAPISettlementFinalizationIntent(f.input.OperationID, 160, usage, dims, evidence); err != nil {
			t.Fatal(err)
		}
		original := f.intent(t)
		if _, err = PendZTAPIRequestSettlement(f.input.OperationID, usage, `["settlement_retry_required"]`); err != nil {
			t.Fatal(err)
		}
		proof := ZTAPINoChargeProof{Source: "test-supplier", ProofID: f.input.RequestID + ":no-charge", VerificationReference: "synthetic-review", Attempts: []ZTAPINoChargeAttempt{{Attempt: 1, ChannelID: f.channel.Id, CredentialVersion: "test-credential-v1", UpstreamRequestID: "wire-" + f.input.RequestID, VerificationReference: "synthetic-attempt-review"}}}
		if _, err = ResolveZTAPIPendingNoCharge(row.ID, f.admin.Id, proof); !errors.Is(err, ErrZTAPISettlementConflict) {
			t.Fatalf("intent-backed hold accepted no-charge proof: %v", err)
		}
		f.assertIntentFinancialState(t, 1000, 160, false)
		if err = RetryZTAPISettlementFinalizations(1000); err != nil {
			t.Fatal(err)
		}
		f.assertIntentFinancialState(t, 1000, 160, true)
		f.assertRecoveredIntent(t, original)
	})
}

type commercialMySQLMediaFixture struct {
	commercialMySQLFixture
	settlement ZTAPIRequestSettlement
	task       ZTAPIMediaTask
	attempt    ZTAPIRequestAttempt
}

func newCommercialMySQLMediaFixture(t *testing.T, processing bool) commercialMySQLMediaFixture {
	t.Helper()
	f := newCommercialMySQLFixture(t, DB, 1000)
	contract, err := canonicalizeZTAPIMediaPriceContract(seedanceContractForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := common.Marshal(ztapiMediaTaskPriceSnapshot{Version: 1, PublicName: "commercial-video-model", Modality: ZTAPIModalityVideo, PriceSourceID: 9, PriceSourceVersion: 3, MediaPriceContractJSON: contract})
	if err != nil {
		t.Fatal(err)
	}
	f.input.PublicModel = "commercial-video-model"
	f.input.PriceSnapshotJSON = string(snapshot)
	videoSelector := types.ZTAPIVideoSelector{Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false}
	protocolJSON := ztapiVideoProtocolForMediaTaskTest(t)
	protocol, _, err := types.ParseZTAPIVideoProtocolContract(protocolJSON)
	if err != nil {
		t.Fatal(err)
	}
	price, err := types.ParseZTAPIMediaPriceContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	_, maximumQuota, err := types.CalculateZTAPIVideoMaximumReservation(price, protocol, videoSelector, "500000")
	if err != nil {
		t.Fatal(err)
	}
	f.input.ReservedQuota = maximumQuota
	f.user.Quota = 100000
	f.token.RemainQuota = 100000
	if err = DB.Model(&f.user).Update("quota", 100000).Error; err != nil {
		t.Fatal(err)
	}
	if err = DB.Model(&f.token).Updates(map[string]any{"remain_quota": 100000, "used_quota": 0}).Error; err != nil {
		t.Fatal(err)
	}
	settlement, err := BeginZTAPIRequestSettlement(f.input)
	if err != nil {
		t.Fatal(err)
	}
	selector := ZTAPIMediaPriceSelector{Modality: ZTAPIModalityVideo, Conditions: map[string]string{"contains_video_input": "false", "resolution": "720p"}}
	task, err := BeginZTAPIMediaTask(ZTAPIMediaTaskInput{PublicTaskID: "cm-task-" + f.input.RequestID, SettlementID: settlement.ID, Selector: selector, VideoSelector: videoSelector, QuotaPerUnit: "500000", Action: "generate", VideoProtocolContractJSON: protocolJSON})
	if err != nil {
		t.Fatal(err)
	}
	if err = MarkZTAPIRequestDispatched(f.input.OperationID); err != nil {
		t.Fatal(err)
	}
	attempt, err := BeginZTAPIRequestAttempt(f.input.OperationID, f.channel.Id, "test-credential-v1", "commercial-video")
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordZTAPIRequestAttemptResponse(f.input.OperationID, attempt.Attempt, attempt.ChannelID, 202, "wire-"+f.input.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err = MarkZTAPIMediaTaskSubmitted(task.PublicTaskID, "upstream-task-"+f.input.RequestID, attempt.Attempt); err != nil {
		t.Fatal(err)
	}
	result := commercialMySQLMediaFixture{commercialMySQLFixture: f, settlement: *settlement, task: *task, attempt: *attempt}
	if processing {
		if _, err = ApplyZTAPIMediaTaskObservation(result.processingObservation()); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func (f commercialMySQLMediaFixture) processingObservation() ZTAPIMediaTaskObservation {
	return ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskProcessing, Attempt: f.attempt.Attempt, UpstreamTaskID: "upstream-task-" + f.input.RequestID}
}

func (f commercialMySQLMediaFixture) knownMediaObservation(t *testing.T) ZTAPIMediaTaskObservation {
	t.Helper()
	dimensions, err := common.Marshal([]ZTAPISupplierRefundDimension{{Dimension: "input_tokens", Units: "6", UnitQuota: "5.2096064815", ChargedQuota: 31}})
	if err != nil {
		t.Fatal(err)
	}
	return ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskSucceeded, Attempt: f.attempt.Attempt,
		UpstreamTaskID: "upstream-task-" + f.input.RequestID, ChargeDisposition: ZTAPIMediaChargeKnown, ActualQuota: 31,
		UsageJSON: `{"input_tokens":6}`, ChargeDimensionsJSON: string(dimensions), ResultMetadataJSON: `{"resolution":"720p","result_available":true}`,
		SettlementEvidence: ZTAPISettlementEvidence{FinalAttempt: f.attempt.Attempt, ConsumeLog: Log{Type: LogTypeConsume, UserId: f.user.Id, TokenId: f.token.Id, RequestId: f.input.RequestID, ModelName: f.input.PublicModel, Quota: 31, ChannelId: f.channel.Id, CreatedAt: 1, UpstreamRequestId: "wire-" + f.input.RequestID}}}
}

func (f commercialMySQLMediaFixture) noChargeMediaObservation() ZTAPIMediaTaskObservation {
	return ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskFailed, Attempt: f.attempt.Attempt,
		UpstreamTaskID: "upstream-task-" + f.input.RequestID, ChargeDisposition: ZTAPIMediaChargeNoCharge,
		UsageJSON: `{"provider_state":"failed"}`, ChargeDimensionsJSON: "[]", ResultMetadataJSON: "{}", FailureReason: "provider_failed"}
}

func (f commercialMySQLMediaFixture) unknownMediaObservation(state ZTAPIMediaTaskState) ZTAPIMediaTaskObservation {
	return ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: state, Attempt: f.attempt.Attempt,
		UpstreamTaskID: "upstream-task-" + f.input.RequestID, ChargeDisposition: ZTAPIMediaChargeUnknown,
		UsageJSON: `{"provider_state":"unknown"}`, ChargeDimensionsJSON: "[]", ResultMetadataJSON: "{}", FailureReason: "provider_timeout"}
}

func assertCommercialMySQLMediaFinancialResult(t *testing.T, f commercialMySQLMediaFixture, state ZTAPIMediaTaskState, settlementState string, ledgerCount, chargeCount int64) {
	t.Helper()
	var task ZTAPIMediaTask
	var settlement ZTAPIRequestSettlement
	var user User
	for _, query := range []*gorm.DB{DB.Where("public_task_id = ?", f.task.PublicTaskID).Take(&task), DB.First(&settlement, f.settlement.ID), DB.First(&user, f.user.Id)} {
		if query.Error != nil {
			t.Fatal(query.Error)
		}
	}
	if task.State != state || task.SettlementState != settlementState || settlement.Status != settlementState {
		t.Fatalf("task and finance disagree: task=%+v settlement=%+v", task, settlement)
	}
	if settlementState == ZTAPISettlementSettled && (settlement.ChargedQuota != 31 || user.Quota != 99969) {
		t.Fatalf("settled media balance mismatch: settlement=%+v wallet=%d", settlement, user.Quota)
	}
	if settlementState == ZTAPISettlementReleased && (settlement.ChargedQuota != 0 || user.Quota != 100000) {
		t.Fatalf("released media balance mismatch: settlement=%+v wallet=%d", settlement, user.Quota)
	}
	for _, check := range []struct {
		model any
		want  int64
	}{{&BalanceLedger{}, ledgerCount}, {&ZTAPISupplierRefundCharge{}, chargeCount}} {
		var count int64
		if err := DB.Model(check.model).Where("request_id = ?", f.input.RequestID).Count(&count).Error; err != nil || count != check.want {
			t.Fatalf("%T count=%d want=%d err=%v", check.model, count, check.want, err)
		}
	}
}

func commercialMySQLMedia32SuccessCallbacks(t *testing.T) {
	f := newCommercialMySQLMediaFixture(t, true)
	observation := f.knownMediaObservation(t)
	commercialConcurrent(t, 32, func() error { _, err := ApplyZTAPIMediaTaskObservation(observation); return err })
	assertCommercialMySQLMediaFinancialResult(t, f, ZTAPIMediaTaskSucceeded, ZTAPISettlementSettled, 2, 1)
}

func commercialMySQLMediaTerminalRace(t *testing.T) {
	f := newCommercialMySQLMediaFixture(t, true)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, observation := range []ZTAPIMediaTaskObservation{f.knownMediaObservation(t), f.noChargeMediaObservation()} {
		go func(input ZTAPIMediaTaskObservation) {
			<-start
			_, err := ApplyZTAPIMediaTaskObservation(input)
			errs <- err
		}(observation)
	}
	close(start)
	for range 2 {
		err := <-errs
		if err != nil && !errors.Is(err, ErrZTAPIMediaTaskConflict) && !errors.Is(err, ErrZTAPISettlementConflict) && !errors.Is(err, ErrZTAPISettlementPending) {
			t.Fatal(err)
		}
	}
	var task ZTAPIMediaTask
	if err := DB.Where("public_task_id = ?", f.task.PublicTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	if task.State == ZTAPIMediaTaskSucceeded {
		assertCommercialMySQLMediaFinancialResult(t, f, task.State, ZTAPISettlementSettled, 2, 1)
	} else if task.State == ZTAPIMediaTaskFailed {
		assertCommercialMySQLMediaFinancialResult(t, f, task.State, ZTAPISettlementReleased, 2, 0)
	} else {
		t.Fatalf("race left nonterminal task: %+v", task)
	}
}

func commercialMySQLMediaRestartAfterSubmitted(t *testing.T, open func() *gorm.DB) {
	f := newCommercialMySQLMediaFixture(t, false)
	sql, err := DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err = sql.Close(); err != nil {
		t.Fatal(err)
	}
	DB = open()
	if err = registerZTAPIMediaTaskMutationGuard(DB); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyZTAPIMediaTaskObservation(f.processingObservation()); err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyZTAPIMediaTaskObservation(f.knownMediaObservation(t)); err != nil {
		t.Fatal(err)
	}
	assertCommercialMySQLMediaFinancialResult(t, f, ZTAPIMediaTaskSucceeded, ZTAPISettlementSettled, 2, 1)
}

func commercialMySQLMediaUnknownThenSuccess(t *testing.T) {
	f := newCommercialMySQLMediaFixture(t, true)
	if _, err := ApplyZTAPIMediaTaskObservation(f.unknownMediaObservation(ZTAPIMediaTaskUnknown)); err != nil {
		t.Fatal(err)
	}
	assertCommercialMySQLMediaFinancialResult(t, f, ZTAPIMediaTaskUnknown, ZTAPISettlementPending, 1, 0)
	if _, err := ApplyZTAPIMediaTaskObservation(f.knownMediaObservation(t)); err != nil {
		t.Fatal(err)
	}
	assertCommercialMySQLMediaFinancialResult(t, f, ZTAPIMediaTaskSucceeded, ZTAPISettlementSettled, 2, 1)
}

func commercialMySQLMediaRefund(t *testing.T, f commercialMySQLMediaFixture, proofID, mode, units string) *ZTAPISupplierRefund {
	t.Helper()
	var charge ZTAPISupplierRefundCharge
	if err := DB.Where("request_id = ?", f.input.RequestID).Take(&charge).Error; err != nil {
		t.Fatal(err)
	}
	input := ZTAPISupplierRefundSubmission{Source: "commercial-media-test", ProofID: f.input.RequestID + ":" + proofID, RequestID: charge.RequestID, UserID: charge.UserID,
		Attempt: charge.Attempt, ChannelID: charge.ChannelID, CredentialVersion: charge.CredentialVersion, UpstreamRequestID: charge.UpstreamRequestID,
		UpstreamTaskID: charge.UpstreamTaskID, UpstreamBillID: charge.UpstreamBillID, Mode: mode, EvidenceReference: "synthetic media supplier refund"}
	if units != "" {
		input.Units = []ZTAPISupplierRefundUnits{{Dimension: "input_tokens", Units: units}}
	}
	refund, err := SubmitZTAPISupplierRefund(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApproveZTAPISupplierRefund(refund.ID, f.admin.Id, "verified synthetic media refund"); err != nil {
		t.Fatal(err)
	}
	return refund
}

func commercialMySQLMediaDuplicateRefund(t *testing.T) {
	f := newCommercialMySQLMediaFixture(t, true)
	if _, err := ApplyZTAPIMediaTaskObservation(f.knownMediaObservation(t)); err != nil {
		t.Fatal(err)
	}
	refund := commercialMySQLMediaRefund(t, f, "full", "full", "")
	commercialConcurrent(t, 8, func() error { return ProcessZTAPISupplierRefund(refund.ID) })
	var user User
	if err := DB.First(&user, f.user.Id).Error; err != nil || user.Quota != 100000 {
		t.Fatalf("duplicate media refund changed wallet: quota=%d err=%v", user.Quota, err)
	}
	var ledgers int64
	if err := DB.Model(&BalanceLedger{}).Where("request_id = ?", f.input.RequestID).Count(&ledgers).Error; err != nil || ledgers != 3 {
		t.Fatalf("media refund ledger count=%d err=%v", ledgers, err)
	}
}

func commercialMySQLMediaPartialRefundCeiling(t *testing.T) {
	f := newCommercialMySQLMediaFixture(t, true)
	if _, err := ApplyZTAPIMediaTaskObservation(f.knownMediaObservation(t)); err != nil {
		t.Fatal(err)
	}
	first := commercialMySQLMediaRefund(t, f, "partial-4", "partial", "4")
	if err := ProcessZTAPISupplierRefund(first.ID); err != nil {
		t.Fatal(err)
	}
	excess := commercialMySQLMediaRefund(t, f, "partial-3", "partial", "3")
	if err := ProcessZTAPISupplierRefund(excess.ID); !errors.Is(err, ErrZTAPISupplierRefundPending) {
		t.Fatalf("media refund exceeded original charge: %v", err)
	}
	var user User
	if err := DB.First(&user, f.user.Id).Error; err != nil || user.Quota != 99989 {
		t.Fatalf("media partial refund quota=%d err=%v", user.Quota, err)
	}
}

func commercialMySQLMediaRetriesTransactionFailure(t *testing.T) {
	f := newCommercialMySQLMediaFixture(t, true)
	const callback = "commercial-media-test:retry-terminal"
	var calls atomic.Int32
	if err := DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (ZTAPIMediaTask{}).TableName() && calls.Add(1) == 1 {
			_ = tx.AddError(errors.New("deadlock found when trying to get lock; try restarting transaction"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DB.Callback().Update().Remove(callback) })
	if _, err := ApplyZTAPIMediaTaskObservation(f.knownMediaObservation(t)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 {
		t.Fatalf("terminal transaction was not retried: calls=%d", calls.Load())
	}
	assertCommercialMySQLMediaFinancialResult(t, f, ZTAPIMediaTaskSucceeded, ZTAPISettlementSettled, 2, 1)
}

func TestZTAPICommercialMySQLMediaTaskIntegration(t *testing.T) {
	required := strings.EqualFold(strings.TrimSpace(os.Getenv("ZTAPI_REQUIRE_COMMERCIAL_MYSQL")), "true")
	safe, err := commercialMySQLTestDSN(os.Getenv("ZTAPI_COMMERCIAL_MYSQL_TEST_DSN"), required)
	if err != nil {
		t.Fatal(err)
	}
	if safe == "" {
		t.Skip("dedicated loopback commercial MySQL DSN absent; real MySQL media task settlement NOT executed")
	}
	open := func() *gorm.DB {
		db, openErr := gorm.Open(gormmysql.Open(safe), &gorm.Config{})
		if openErr != nil {
			t.Fatal("cannot open dedicated commercial MySQL test database")
		}
		sqlDB, sqlErr := db.DB()
		if sqlErr != nil {
			t.Fatal(sqlErr)
		}
		sqlDB.SetMaxOpenConns(40)
		return db
	}
	db := open()
	oldDB, oldRedis := DB, common.RedisEnabled
	DB, common.RedisEnabled = db, false
	t.Cleanup(func() { sqlDB, _ := DB.DB(); _ = sqlDB.Close(); DB, common.RedisEnabled = oldDB, oldRedis })
	if err = db.AutoMigrate(&User{}, &Token{}, &Channel{}, &BalanceLedger{}, &ZTAPIRequestSettlement{}, &ZTAPIMediaTask{}, &ZTAPISettlementFinalizationIntent{}, &ZTAPIRequestAttempt{}, &ZTAPIPendingResolution{}, &ZTAPIAttemptBillingReview{}, &ZTAPIAttemptBillingProof{}, &ZTAPIAttemptBillingApproval{}, &ZTAPISettlementLogReceipt{}, &Log{}); err != nil {
		t.Fatal(err)
	}
	for _, migrate := range []func(*gorm.DB) error{MigrateZTAPISupplierRefund, MigrateZTAPISettlementLogOutbox, MigrateZTAPIFinanceAlerts, registerZTAPIMediaTaskMutationGuard} {
		if err = migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"ztapi_media_tasks", "ztapi_request_settlements", "ztapi_request_attempts", "balance_ledgers", "ztapi_supplier_refund_charges", "ztapi_supplier_refunds", "ztapi_supplier_refund_approvals", "ztapi_settlement_log_outboxes", "ztapi_settlement_log_receipts", "ztapi_pending_resolutions", "ztapi_finance_alert_outboxes"} {
		var engine string
		if err = db.Raw("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&engine).Error; err != nil || !strings.EqualFold(engine, "InnoDB") {
			t.Fatalf("%s must be InnoDB: engine=%q err=%v", table, engine, err)
		}
	}
	t.Run("32_simultaneous_success_callbacks", commercialMySQLMedia32SuccessCallbacks)
	t.Run("success_vs_failure_race", commercialMySQLMediaTerminalRace)
	t.Run("worker_restart_after_submitted", func(t *testing.T) { commercialMySQLMediaRestartAfterSubmitted(t, open) })
	t.Run("unknown_timeout_then_later_success", commercialMySQLMediaUnknownThenSuccess)
	t.Run("duplicate_trusted_refund", commercialMySQLMediaDuplicateRefund)
	t.Run("partial_refund_ceiling", commercialMySQLMediaPartialRefundCeiling)
	t.Run("retry_after_transaction_failure", commercialMySQLMediaRetriesTransactionFailure)
}
