package finance

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReaderUsesPostingTimeAndOnlySelectsFinancialColumns(t *testing.T) {
	store := newStore(t)
	db := store.db
	for _, sql := range []string{
		"CREATE TABLE users(id INTEGER,username TEXT,role INTEGER,status INTEGER,deleted_at DATETIME)",
		"CREATE TABLE balance_ledgers(id INTEGER,user_id INTEGER,delta BIGINT,balance_before BIGINT,balance_after BIGINT,source_type TEXT,request_id TEXT,idempotency_key TEXT,created_at DATETIME)",
		"CREATE TABLE ztapi_request_settlements(id INTEGER,user_id INTEGER,request_id TEXT,operation_id TEXT,status TEXT,public_model TEXT,price_snapshot_json TEXT,charge_dimensions_json TEXT,missing_dimensions_json TEXT,reserved_quota BIGINT,charged_quota BIGINT,final_attempt INTEGER,created_at DATETIME)",
		"CREATE TABLE ztapi_settlement_log_outboxes(operation_id TEXT,created_at BIGINT)",
		"CREATE TABLE ztapi_request_attempts(settlement_id INTEGER,attempt INTEGER,channel_id INTEGER,created_at DATETIME)",
		"CREATE TABLE top_ups(id INTEGER,user_id INTEGER,trade_no TEXT,status TEXT,complete_time BIGINT)",
		"CREATE TABLE usdt_top_up_orders(top_up_id INTEGER,status TEXT,credit_units BIGINT,pay_amount_micros BIGINT)",
		"CREATE TABLE channels(id INTEGER,name TEXT,base_url TEXT)",
		"CREATE TABLE ztapi_supplier_refund_charges(id INTEGER,request_id TEXT,user_id INTEGER,original_ledger_id INTEGER,charged_quota BIGINT,billing_proof_id INTEGER,channel_id INTEGER,billing_operation_id TEXT,created_at DATETIME)",
		"CREATE TABLE ztapi_supplier_refunds(ledger_id INTEGER,charge_id INTEGER,refunded_quota BIGINT,status TEXT)",
		"INSERT INTO users VALUES(1,'customer',100,1,NULL)",
		"INSERT INTO ztapi_request_settlements VALUES(1,1,'r1','op1','settled','zt-test','{}','[]','[]',10,10,1,'2026-09-16 00:00:00')",
		"INSERT INTO ztapi_settlement_log_outboxes VALUES('op1',1789516800)",
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	reader := &Reader{DB: db, QuotaPerUnit: 500000}
	s, e := reader.Load(context.Background(), moment("2026-10-26T00:00:00Z"))
	require.NoError(t, e)
	require.Len(t, s.Settlements, 1)
	require.Equal(t, int64(1789516800), s.Settlements[0].CompletedAt)
	u, e := reader.Identity(context.Background(), 1)
	require.NoError(t, e)
	require.Equal(t, 100, u.Role)
}
