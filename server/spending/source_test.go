package spending

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func fixture(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fixture.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { raw.Close() })
	return db
}
func sourceFixture(t *testing.T) (*Reader, time.Time) {
	db := fixture(t)
	for _, q := range []string{
		`CREATE TABLE users(id INTEGER PRIMARY KEY,status INTEGER,deleted_at DATETIME,quota BIGINT)`,
		`CREATE TABLE ztapi_request_settlements(id INTEGER PRIMARY KEY,operation_id TEXT,request_id TEXT,user_id INTEGER,status TEXT,public_model TEXT,price_snapshot_json TEXT,usage_json TEXT,missing_dimensions_json TEXT,reserved_quota BIGINT,charged_quota BIGINT,refunded_quota BIGINT,created_at DATETIME,charge_dimensions_json TEXT DEFAULT '[]',updated_at DATETIME)`,
		`CREATE TABLE ztapi_settlement_log_outboxes(operation_id TEXT PRIMARY KEY,created_at BIGINT,log_id INTEGER)`,
		`CREATE TABLE balance_ledgers(id INTEGER PRIMARY KEY,user_id INTEGER,request_id TEXT,source_type TEXT,idempotency_key TEXT,delta BIGINT,created_at DATETIME)`,
		`CREATE TABLE ztapi_supplier_refund_charges(id INTEGER PRIMARY KEY,settlement_id INTEGER,request_id TEXT,user_id INTEGER,billing_proof_id INTEGER,billing_operation_id TEXT,original_ledger_id INTEGER,charged_quota BIGINT,refunded_quota BIGINT,price_snapshot_json TEXT)`,
		`CREATE TABLE ztapi_supplier_refunds(id INTEGER PRIMARY KEY,charge_id INTEGER,request_id TEXT,status TEXT,ledger_id INTEGER,refunded_quota BIGINT)`,
		`CREATE TABLE logs(id INTEGER PRIMARY KEY,user_id INTEGER,type INTEGER,created_at BIGINT,request_id TEXT,quota BIGINT)`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO users VALUES(1,1,NULL,1000000),(2,1,NULL,3000000)`).Error)
	now := time.Date(2026, 11, 2, 0, 30, 0, 0, Beijing)
	return &Reader{DB: db, VerifiedQuotaPerUnit: "500000"}, now
}
func seedSettlement(t *testing.T, r *Reader, now time.Time, id int, op, status string, quota int64) {
	t.Helper()
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_request_settlements(id,operation_id,request_id,user_id,status,public_model,price_snapshot_json,usage_json,missing_dimensions_json,reserved_quota,charged_quota,refunded_quota,created_at,charge_dimensions_json,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, op, op, 1, status, "text", `{"quota_per_unit":500000}`, `{"usage_source":"upstream_reported","input_tokens":100,"prompt_tokens_details":{"cached_tokens":90}}`, `[]`, 100000, quota, 0, now.Add(-48*time.Hour), `[{"dimension":"input_tokens","units":"100","unit_quota":"1","charged_quota":100}]`, now.Add(-time.Second)).Error)
	if status == "settled" {
		require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_settlement_log_outboxes VALUES(?,?,?)`, op, now.Add(-10*time.Minute).Unix(), id).Error)
		require.NoError(t, r.DB.Exec(`INSERT INTO balance_ledgers VALUES(?,?,?,?,?,?,?)`, id*10, 1, op, "usage_reservation", "ztapi:"+op+":begin", -100000, now.Add(-48*time.Hour)).Error)
		if quota != 100000 {
			require.NoError(t, r.DB.Exec(`INSERT INTO balance_ledgers VALUES(?,?,?,?,?,?,?)`, id*10+1, 1, op, "usage_settlement", "ztapi:"+op+":settle", 100000-quota, now).Error)
		}
	}
}
func TestSourceConfirmedLateHostedAdditionalAndRefundNoDoubleCount(t *testing.T) {
	r, now := sourceFixture(t)
	seedSettlement(t, r, now, 1, "primary", "settled", 100000)
	seedSettlement(t, r, now, 2, "hstool_x", "settled", 1000)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refund_charges VALUES(1,1,'primary',1,0,'',10,100000,0,'{}'),(2,1,'primary',1,9,'extra',30,50000,10000,'{"quota_per_unit":500000}')`).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO balance_ledgers VALUES(30,1,'primary','usage_settlement','attempt-billing:proof',-50000,?),(31,1,'primary','supplier_refund','supplier-refund:proof',10000,?)`, now.Add(-time.Second), now.Add(-time.Second)).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_settlement_log_outboxes VALUES('extra',?,3)`, now.Unix()-10).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refunds VALUES(1,2,'primary','applied',31,10000)`).Error)
	require.NoError(t, r.DB.Exec(`UPDATE ztapi_request_settlements SET refunded_quota=10000 WHERE id=1`).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO logs VALUES(1,1,2,?,'primary',100000),(2,1,2,?,'hstool_x',1000),(3,1,2,?,'primary',50000),(4,2,2,?,'other-owner',999999)`, now.Unix(), now.Unix(), now.Unix(), now.Unix()).Error)
	s, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.True(t, s.Overview.Complete)
	require.Equal(t, "0.302", *s.Overview.ConfirmedDailyU)
	require.Equal(t, "0.302", *s.Overview.ConfirmedHourlyU)
	require.Equal(t, "0.02", *s.Overview.ConfirmedRefundsDailyU)
	require.Equal(t, "2", *s.Overview.AvailableBalanceU)
	require.Len(t, s.Inputs, 2)
	require.EqualValues(t, 100, s.Inputs[0].Tokens)
}
func TestSourcePendingNotSpendAndLegacyUnknownNotZero(t *testing.T) {
	r, now := sourceFixture(t)
	seedSettlement(t, r, now, 1, "pending", "pending", 0)
	s, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.True(t, s.Overview.Complete)
	require.Equal(t, 1, s.Overview.PendingRequests)
	require.Equal(t, "0", *s.Overview.ConfirmedDailyU)
	require.NoError(t, r.DB.Exec(`INSERT INTO logs VALUES(1,1,2,?,'legacy',500)`, now.Unix()-1).Error)
	s, err = r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.False(t, s.Overview.Complete)
	require.Nil(t, s.Overview.ConfirmedDailyU)
}
func TestSourceMissingReceiptConflictLimitAndUnitFailClosed(t *testing.T) {
	for _, kind := range []string{"receipt", "unit", "limit", "missing_table", "ledger"} {
		t.Run(kind, func(t *testing.T) {
			r, now := sourceFixture(t)
			seedSettlement(t, r, now, 1, "one", "settled", 50000)
			switch kind {
			case "receipt":
				require.NoError(t, r.DB.Exec(`DELETE FROM ztapi_settlement_log_outboxes`).Error)
			case "unit":
				require.NoError(t, r.DB.Exec(`UPDATE ztapi_request_settlements SET price_snapshot_json='{"quota_per_unit":1000000}'`).Error)
			case "limit":
				r.Limit = 1
				seedSettlement(t, r, now, 2, "two", "settled", 100)
			case "missing_table":
				require.NoError(t, r.DB.Exec(`DROP TABLE logs`).Error)
			case "ledger":
				require.NoError(t, r.DB.Exec(`DELETE FROM balance_ledgers`).Error)
			}
			s, err := r.Load(context.Background(), 1, now)
			if err == nil {
				require.False(t, s.Overview.Complete)
				require.Nil(t, s.Overview.ConfirmedDailyU)
			}
		})
	}
}
func TestSourceBeijingMidnightAndReadsHaveDeadline(t *testing.T) {
	r, now := sourceFixture(t)
	seedSettlement(t, r, now, 1, "one", "settled", 1)
	require.NoError(t, r.DB.Exec(`UPDATE ztapi_settlement_log_outboxes SET created_at=?`, now.Add(-40*time.Minute).Unix()).Error)
	require.NoError(t, r.DB.Callback().Query().Before("gorm:query").Register("deadline", func(db *gorm.DB) { _, ok := db.Statement.Context.Deadline(); require.True(t, ok) }))
	s, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.Equal(t, "0", *s.Overview.ConfirmedDailyU)
	require.Equal(t, "0.000002", *s.Overview.ConfirmedHourlyU)
}
