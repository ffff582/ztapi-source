// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var sourceTestNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func sourceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, ddl := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, role INTEGER, status INTEGER, deleted_at DATETIME, password TEXT, access_token TEXT)`,
		`CREATE TABLE ztapi_health_requests (execution_id TEXT PRIMARY KEY, request_id TEXT, user_id INTEGER, public_model TEXT, stream BOOLEAN, started_at BIGINT, completed BOOLEAN, admissions TEXT)`,
		`CREATE TABLE ztapi_health_events (id INTEGER PRIMARY KEY, execution_id TEXT, request_id TEXT, public_model TEXT, completed_at BIGINT, result TEXT, reason TEXT, http_status INTEGER, result_valid BOOLEAN, outcome TEXT, credential_version TEXT, provider_error_code TEXT)`,
		`CREATE TABLE ztapi_request_settlements (id INTEGER PRIMARY KEY, request_id TEXT UNIQUE, user_id INTEGER, public_model TEXT, status TEXT, reserved_quota BIGINT, charged_quota BIGINT, refunded_quota BIGINT NOT NULL DEFAULT 0, dispatched BOOLEAN, usage_json TEXT, missing_dimensions_json TEXT, created_at DATETIME, price_snapshot_json TEXT)`,
		`CREATE TABLE ztapi_supplier_refund_charges (id INTEGER PRIMARY KEY, settlement_id INTEGER, request_id TEXT, user_id INTEGER, attempt INTEGER, billing_proof_id INTEGER, original_ledger_id INTEGER, charged_quota BIGINT, refunded_quota BIGINT, price_snapshot_json TEXT, credential_version TEXT)`,
		`CREATE TABLE ztapi_supplier_refunds (id INTEGER PRIMARY KEY, request_id TEXT, charge_id INTEGER, status TEXT, ledger_id INTEGER, refunded_quota BIGINT, submission_json TEXT)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func sourceNetFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	sourceInsert(t, db, "ztapi_request_settlements", map[string]any{
		"id": 70, "request_id": "net-request", "user_id": 7, "public_model": "zt-model", "status": "settled",
		"charged_quota": int64(5000000), "refunded_quota": int64(1000000), "reserved_quota": int64(5000000),
		"created_at": sourceTestNow.Add(-time.Hour), "dispatched": true, "usage_json": `{"prompt_tokens":10,"usage_source":"upstream_reported"}`, "missing_dimensions_json": `[]`,
	})
	sourceInsert(t, db, "ztapi_supplier_refund_charges", map[string]any{
		"id": 701, "settlement_id": 70, "request_id": "net-request", "user_id": 7, "attempt": 2,
		"billing_proof_id": 0, "original_ledger_id": 1001, "charged_quota": int64(5000000), "refunded_quota": int64(1000000),
		"price_snapshot_json": "private-purchase", "credential_version": "private-credential",
	})
	sourceInsert(t, db, "ztapi_supplier_refund_charges", map[string]any{
		"id": 702, "settlement_id": 70, "request_id": "net-request", "user_id": 7, "attempt": 1,
		"billing_proof_id": 81, "original_ledger_id": 1002, "charged_quota": int64(1500000), "refunded_quota": int64(0),
		"price_snapshot_json": "private-retry-purchase", "credential_version": "private-credential",
	})
	sourceInsert(t, db, "ztapi_supplier_refunds", map[string]any{
		"id": 801, "request_id": "net-request", "charge_id": 701, "status": "applied", "ledger_id": 2001, "refunded_quota": int64(1000000), "submission_json": "private-proof",
	})
	sourceHealth(t, db, 7, "net-request", sourceTestNow.Add(-time.Hour), "valid_output", 200, `{"Dispatched":true,"HasText":true,"TransportComplete":true}`)
}

func TestReaderNetChargeIncludesRetryAndOnlyPostedRefunds(t *testing.T) {
	for _, status := range []string{"applied", "completed"} {
		t.Run(status, func(t *testing.T) {
			db := sourceTestDB(t)
			sourceNetFixture(t, db)
			if err := db.Table("ztapi_supplier_refunds").Where("id = ?", 801).Update("status", status).Error; err != nil {
				t.Fatal(err)
			}
			for i, ledger := range []int{0, 2002} {
				sourceInsert(t, db, "ztapi_supplier_refunds", map[string]any{"id": 802 + i, "request_id": "net-request", "charge_id": 702, "status": "pending", "ledger_id": ledger, "refunded_quota": int64(500000), "submission_json": "private-pending-proof"})
			}
			sourceReadOnly(t, db)
			got, err := (&Reader{DB: db}).Diagnose(context.Background(), 7, "net-request")
			if err != nil {
				t.Fatal(err)
			}
			if got.ChargedU != "11" || got.ReservedU != "0" || got.NeedsHuman {
				t.Fatalf("want primary 10 + retry 3 - posted refund 2 = net 11: %+v", got)
			}
			if !strings.Contains(got.Summary, "已结算净扣") || strings.Contains(got.Summary, "最终扣款。") {
				t.Fatalf("primary charge still described as final net: %s", got.Summary)
			}
			encoded, _ := common.Marshal(got)
			if strings.Contains(string(encoded), "private-") {
				t.Fatalf("charge/refund secrets leaked: %s", encoded)
			}
		})
	}
}

func TestReaderNetChargeScopesOwnerSettlementAndRefundRequest(t *testing.T) {
	db := sourceTestDB(t)
	sourceNetFixture(t, db)
	for i, row := range []map[string]any{
		{"settlement_id": 70, "request_id": "net-request", "user_id": 8},
		{"settlement_id": 71, "request_id": "net-request", "user_id": 7},
		{"settlement_id": 70, "request_id": "foreign-request", "user_id": 7},
	} {
		row["id"] = 710 + i
		row["attempt"] = 1
		row["billing_proof_id"] = 90 + i
		row["original_ledger_id"] = 1100 + i
		row["charged_quota"] = int64(99000000)
		row["refunded_quota"] = int64(0)
		sourceInsert(t, db, "ztapi_supplier_refund_charges", row)
		sourceInsert(t, db, "ztapi_supplier_refunds", map[string]any{"id": 810 + i, "request_id": "net-request", "charge_id": 710 + i, "status": "completed", "ledger_id": 2100 + i, "refunded_quota": int64(9000000)})
	}
	sourceInsert(t, db, "ztapi_supplier_refunds", map[string]any{"id": 820, "request_id": "foreign-request", "charge_id": 701, "status": "completed", "ledger_id": 2200, "refunded_quota": int64(500000)})
	sourceReadOnly(t, db)
	r := &Reader{DB: db}
	got, err := r.Diagnose(context.Background(), 7, "net-request")
	if err != nil {
		t.Fatal(err)
	}
	if got.ChargedU != "11" {
		t.Fatalf("foreign owner, settlement or refund request polluted net: %+v", got)
	}
	if _, err := r.Diagnose(context.Background(), 8, "net-request"); err != ErrNotFound {
		t.Fatalf("charge ownership must not grant access to another user's settlement: %v", err)
	}
}

func TestReaderNetChargeLegacyMissingAnchorIsOnlyPrimaryAndUnknownNet(t *testing.T) {
	db := sourceTestDB(t)
	sourceNetFixture(t, db)
	if err := db.Exec("DELETE FROM ztapi_supplier_refund_charges").Error; err != nil {
		t.Fatal(err)
	}
	sourceReadOnly(t, db)
	got, err := (&Reader{DB: db}).Diagnose(context.Background(), 7, "net-request")
	if err != nil {
		t.Fatal(err)
	}
	if got.ChargedU != "" || !got.NeedsHuman || !strings.Contains(got.Summary, "primary") || !strings.Contains(got.Summary, "10 U") || !strings.Contains(got.Summary, "未知") {
		t.Fatalf("legacy primary must not masquerade as net: %+v", got)
	}
}

func TestReaderNetChargeIncompleteEvidenceIsUnknown(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"unposted_refund", `UPDATE ztapi_supplier_refunds SET ledger_id = 0 WHERE id = 801`},
		{"pending_not_posted", `UPDATE ztapi_supplier_refunds SET status = 'pending' WHERE id = 801`},
		{"parent_refund_mismatch", `UPDATE ztapi_request_settlements SET refunded_quota = 0 WHERE id = 70`},
		{"anchor_refund_mismatch", `UPDATE ztapi_supplier_refund_charges SET refunded_quota = 0 WHERE id = 701`},
		{"missing_primary", `DELETE FROM ztapi_supplier_refund_charges WHERE id = 701`},
		{"missing_retry_posting", `UPDATE ztapi_supplier_refund_charges SET original_ledger_id = 0 WHERE id = 702`},
		{"duplicate_attempt", `UPDATE ztapi_supplier_refund_charges SET attempt = 2 WHERE id = 702`},
		{"negative_retry", `UPDATE ztapi_supplier_refund_charges SET charged_quota = -1 WHERE id = 702`},
		{"negative_refund", `UPDATE ztapi_supplier_refunds SET refunded_quota = -1 WHERE id = 801`},
		{"primary_mismatch", `UPDATE ztapi_supplier_refund_charges SET charged_quota = 4000000 WHERE id = 701`},
		{"refund_table_unavailable", `DROP TABLE ztapi_supplier_refunds`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := sourceTestDB(t)
			sourceNetFixture(t, db)
			if err := db.Exec(tc.sql).Error; err != nil {
				t.Fatal(err)
			}
			sourceReadOnly(t, db)
			got, err := (&Reader{DB: db}).Diagnose(context.Background(), 7, "net-request")
			if err != nil {
				t.Fatal(err)
			}
			if got.ChargedU != "" || !got.NeedsHuman || !strings.Contains(got.Summary, "未知") {
				t.Fatalf("incomplete net evidence became a fee: %+v", got)
			}
		})
	}
}

func TestReaderNetChargePendingNeverBecomesFinalFee(t *testing.T) {
	db := sourceTestDB(t)
	sourceNetFixture(t, db)
	if err := db.Table("ztapi_request_settlements").Where("id = ?", 70).Update("status", "pending").Error; err != nil {
		t.Fatal(err)
	}
	sourceReadOnly(t, db)
	got, err := (&Reader{DB: db}).Diagnose(context.Background(), 7, "net-request")
	if err != nil {
		t.Fatal(err)
	}
	if got.ChargedU != "" || !got.NeedsHuman || !strings.Contains(got.Summary, "预留") {
		t.Fatalf("pending evidence was reported as final net: %+v", got)
	}
}

func sourceInsert(t *testing.T, db *gorm.DB, table string, row map[string]any) {
	t.Helper()
	if err := db.Table(table).Create(row).Error; err != nil {
		t.Fatal(err)
	}
}

func sourceHealth(t *testing.T, db *gorm.DB, userID int, requestID string, at time.Time, reason string, status int, outcome string) {
	t.Helper()
	execution := fmt.Sprintf("exec-%d-%s", userID, requestID)
	sourceInsert(t, db, "ztapi_health_requests", map[string]any{
		"execution_id": execution, "request_id": requestID, "user_id": userID,
		"public_model": "zt-model", "stream": true, "started_at": at.Unix(), "completed": true,
		"admissions": `{"credentials":"private-admissions"}`,
	})
	sourceInsert(t, db, "ztapi_health_events", map[string]any{
		"execution_id": execution, "request_id": requestID, "public_model": "zt-model",
		"completed_at": at.Add(time.Second).Unix(), "result": "suspected", "reason": reason,
		"http_status": status, "result_valid": reason == "valid_output", "outcome": outcome,
		"credential_version": "private-credential", "provider_error_code": "private-raw-error",
	})
}

func sourceSettlement(t *testing.T, db *gorm.DB, userID int, requestID, state, usage string, dispatched bool) {
	t.Helper()
	charged := int64(125000)
	if state == "released" {
		charged = 0
	}
	sourceInsert(t, db, "ztapi_request_settlements", map[string]any{
		"request_id": requestID, "user_id": userID, "public_model": "zt-model", "status": state,
		"reserved_quota": int64(750000), "charged_quota": charged, "dispatched": dispatched,
		"usage_json": usage, "missing_dimensions_json": `[]`, "created_at": sourceTestNow.Add(-time.Hour),
		"price_snapshot_json": `{"purchase_price":"private-purchase","credentials":"private-key"}`,
	})
	if state == "settled" || state == "released" {
		var rows []struct{ ID uint }
		if err := db.Table("ztapi_request_settlements").Select("id").Where("user_id = ? AND request_id = ?", userID, requestID).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatal("fixture settlement missing")
		}
		sourceInsert(t, db, "ztapi_supplier_refund_charges", map[string]any{
			"settlement_id": rows[0].ID, "request_id": requestID, "user_id": userID, "attempt": 1, "billing_proof_id": 0,
			"original_ledger_id": 1000 + rows[0].ID, "charged_quota": charged, "refunded_quota": int64(0),
		})
	}
}

func sourceReadOnly(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("PRAGMA query_only = ON").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("support_test_deadline", func(tx *gorm.DB) {
		deadline, ok := tx.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("SELECT lacks a bounded context deadline")
		}
		for _, prohibited := range []string{"price_snapshot", "admissions", "credential", "provider_error", "password", "access_token", "other", "raw_error"} {
			for _, column := range tx.Statement.Selects {
				if strings.Contains(strings.ToLower(column), prohibited) {
					t.Errorf("selected prohibited column %q", column)
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReaderIdentityUsesRealFieldsIncludingDeleted(t *testing.T) {
	db := sourceTestDB(t)
	deleted := sourceTestNow.Add(-time.Hour)
	sourceInsert(t, db, "users", map[string]any{"id": 8, "role": 100, "status": 2, "deleted_at": deleted, "password": "secret"})
	sourceReadOnly(t, db)
	r := &Reader{DB: db}
	identity, err := r.Identity(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != 8 || identity.Role != 100 || identity.Status != 2 || identity.DeletedAt == nil || !identity.DeletedAt.Equal(deleted) {
		t.Fatalf("identity must not synthesize enabled or hide deleted state: %+v", identity)
	}
	for _, id := range []int{0, -1, 999} {
		if _, err := r.Identity(context.Background(), id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("id %d: %v", id, err)
		}
	}
}

func TestReaderDiagnoseOwnerIsolationAndSafeProjection(t *testing.T) {
	db := sourceTestDB(t)
	sourceHealth(t, db, 11, "shared", sourceTestNow, "empty_output", 200, `{"Dispatched":true,"HasText":false,"TransportComplete":true,"raw_error":"private-outcome"}`)
	sourceHealth(t, db, 22, "shared", sourceTestNow, "valid_output", 200, `{"Dispatched":true,"HasText":true,"TransportComplete":true}`)
	sourceHealth(t, db, 22, "other", sourceTestNow, "upstream_auth", 401, `{"Dispatched":true}`)
	sourceSettlement(t, db, 22, "shared", "settled", `{"prompt_tokens":5}`, true)
	sourceSettlement(t, db, 22, "settlement-only", "pending", `{}`, true)
	sourceReadOnly(t, db)
	r := &Reader{DB: db, Now: func() time.Time { return sourceTestNow }}
	got, err := r.Diagnose(context.Background(), 11, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != "empty_output" || got.ChargedU != "" || got.ReservedU != "" || got.UsageReceived {
		t.Fatalf("cross-owner evidence leaked: %+v", got)
	}
	for _, requestID := range []string{"other", "settlement-only", "unknown", "' OR 1=1 --"} {
		if _, err := r.Diagnose(context.Background(), 11, requestID); err != ErrNotFound {
			t.Fatalf("%s: other and unknown must share exact sentinel, got %v", requestID, err)
		}
	}
	encoded, err := common.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{"private-", "purchase", "credentials", "raw_error", "prompt", "other"} {
		if strings.Contains(string(encoded), prohibited) {
			t.Fatalf("unsafe projection: %s", encoded)
		}
	}
}

func TestReaderDiagnoseEvidenceAndSettlementAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name, reason, outcome, state, usage, code string
		http                                      int
		dispatched, usageReceived                 bool
		charged, reserved                         string
	}{
		{"empty200", "empty_output", `{"Dispatched":true,"TransportComplete":true}`, "pending", `{}`, "empty_output", 200, true, false, "", "1.5"},
		{"cancelled", "client_cancelled", `{"Dispatched":true,"ClientCancelled":true}`, "pending", `{}`, "client_cancelled", 200, true, false, "", "1.5"},
		{"not_dispatched", "not_dispatched", `{"Dispatched":false}`, "released", `{}`, "not_dispatched", 0, false, false, "0", "0"},
		{"upstream401", "upstream_auth", `{"Dispatched":true}`, "pending", `{}`, "upstream_401", 401, true, false, "", "1.5"},
		{"upstream503", "upstream_http_error", `{"Dispatched":true}`, "pending", `{}`, "upstream_503", 503, true, false, "", "1.5"},
		{"missing_usage", "valid_output", `{"Dispatched":true,"HasTool":true,"TransportComplete":true}`, "pending", `{}`, "usage_missing", 200, true, false, "", "1.5"},
		{"settled", "valid_output", `{"Dispatched":true,"HasText":true,"TransportComplete":true}`, "settled", `{"prompt_tokens":10,"completion_tokens":5,"usage_source":"upstream_reported"}`, "valid_output", 200, true, true, "0.25", "0"},
		{"local_usage", "valid_output", `{"Dispatched":true,"HasText":true,"TransportComplete":true}`, "pending", `{"prompt_tokens":10,"usage_source":"local_estimate"}`, "usage_missing", 200, true, false, "", "1.5"},
		{"missing_metering", "valid_output", `{"Dispatched":true,"HasText":true,"TransportComplete":true}`, "pending", `{"prompt_tokens":10,"ztapi_metering_evidence":{"usage_source":"missing","usage_observations":0}}`, "usage_missing", 200, true, false, "", "1.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := sourceTestDB(t)
			sourceHealth(t, db, 1, tc.name, sourceTestNow.Add(-time.Hour), tc.reason, tc.http, tc.outcome)
			sourceSettlement(t, db, 1, tc.name, tc.state, tc.usage, tc.dispatched)
			sourceReadOnly(t, db)
			got, err := (&Reader{DB: db}).Diagnose(context.Background(), 1, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if got.Code != tc.code || got.HTTPStatus != tc.http || got.Dispatched != tc.dispatched || got.UsageReceived != tc.usageReceived || got.ChargedU != tc.charged || got.ReservedU != tc.reserved {
				t.Fatalf("unexpected evidence: %+v", got)
			}
			if strings.Contains(got.Summary, "已退款") || strings.Contains(got.Summary, "重复扣款") || strings.Contains(got.Summary, "多扣") {
				t.Fatalf("unproven money conclusion: %s", got.Summary)
			}
			if tc.state == "pending" && !strings.Contains(got.Summary, "预留") {
				t.Fatalf("pending must explain reservation: %s", got.Summary)
			}
			if tc.code != "valid_output" && !got.NeedsHuman {
				t.Fatalf("uncertain diagnosis must permit handoff: %+v", got)
			}
		})
	}
}

func TestReaderRecentRequestsBoundsWindowOwnersAndIncludesSettlementOnly(t *testing.T) {
	db := sourceTestDB(t)
	for i := 0; i < 25; i++ {
		sourceHealth(t, db, 7, fmt.Sprintf("req-%02d", i), sourceTestNow.Add(-time.Duration(i+1)*time.Hour), "valid_output", 200, `{"Dispatched":true,"HasText":true}`)
	}
	sourceHealth(t, db, 7, "too-old", sourceTestNow.Add(-31*24*time.Hour), "valid_output", 200, `{}`)
	sourceHealth(t, db, 8, "private-other", sourceTestNow, "valid_output", 200, `{}`)
	sourceSettlement(t, db, 7, "settlement-newest", "pending", `{}`, true)
	if err := db.Table("ztapi_request_settlements").Where("request_id = ?", "settlement-newest").Update("created_at", sourceTestNow).Error; err != nil {
		t.Fatal(err)
	}
	sourceReadOnly(t, db)
	r := &Reader{DB: db, Now: func() time.Time { return sourceTestNow }}
	got, err := r.RecentRequests(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 || got[0].RequestID != "settlement-newest" || got[19].RequestID != "req-18" {
		t.Fatalf("unexpected page: %+v", got)
	}
	for i, item := range got {
		if item.RequestID == "too-old" || item.RequestID == "private-other" {
			t.Fatal("window or owner leak")
		}
		if i > 0 && item.CreatedAt.After(got[i-1].CreatedAt) {
			t.Fatal("recent rows not newest first")
		}
	}
	empty, err := r.RecentRequests(context.Background(), 999)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list: %v, %v", empty, err)
	}
}

func TestReaderCancellationAndDatabaseFailureAreFailClosed(t *testing.T) {
	db := sourceTestDB(t)
	sourceReadOnly(t, db)
	r := &Reader{DB: db}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Identity(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("identity ignored cancellation: %v", err)
	}
	if _, err := r.RecentRequests(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("recent ignored cancellation: %v", err)
	}
	if _, err := r.Diagnose(ctx, 1, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("diagnose ignored cancellation: %v", err)
	}
	if _, err := (&Reader{}).Identity(context.Background(), 1); err == nil {
		t.Fatal("nil DB must fail")
	}
	broken := sourceTestDB(t)
	if err := broken.Exec("DROP TABLE ztapi_health_events").Error; err != nil {
		t.Fatal(err)
	}
	sourceHealthRow := map[string]any{"execution_id": "x", "request_id": "r", "user_id": 1, "started_at": sourceTestNow.Unix()}
	sourceInsert(t, broken, "ztapi_health_requests", sourceHealthRow)
	if _, err := (&Reader{DB: broken}).Diagnose(context.Background(), 1, "r"); err == nil || strings.Contains(err.Error(), "ztapi_health") {
		t.Fatalf("DB failure must not appear healthy or leak SQL: %v", err)
	}
}

func TestReaderReleasedSettlementWithConflictingChargeIsUnknown(t *testing.T) {
	db := sourceTestDB(t)
	sourceSettlement(t, db, 1, "inconsistent-release", "released", `{}`, false)
	if err := db.Table("ztapi_request_settlements").Where("request_id = ?", "inconsistent-release").Update("charged_quota", 125000).Error; err != nil {
		t.Fatal(err)
	}
	sourceReadOnly(t, db)
	got, err := (&Reader{DB: db}).Diagnose(context.Background(), 1, "inconsistent-release")
	if err != nil {
		t.Fatal(err)
	}
	if got.ChargedU != "" || !got.NeedsHuman {
		t.Fatalf("conflicting evidence must not become free or refunded: %+v", got)
	}
}

func TestReaderRecentWindowUsesAdmissionNotNewerSettlementTimestamp(t *testing.T) {
	db := sourceTestDB(t)
	sourceHealth(t, db, 1, "old-admission", sourceTestNow.Add(-31*24*time.Hour), "empty_output", 200, `{"Dispatched":true}`)
	sourceSettlement(t, db, 1, "old-admission", "pending", `{}`, true)
	sourceReadOnly(t, db)
	got, err := (&Reader{DB: db, Now: func() time.Time { return sourceTestNow }}).RecentRequests(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("old request reintroduced through newer settlement: %+v", got)
	}
}

func TestReaderRedactsCredentialsInPublicIdentifiers(t *testing.T) {
	db := sourceTestDB(t)
	requestID := "Bearer abc-super-private-token"
	sourceSettlement(t, db, 1, requestID, "pending", `{}`, true)
	if err := db.Table("ztapi_request_settlements").Where("request_id = ?", requestID).Update("public_model", "APIKey=private-model-secret").Error; err != nil {
		t.Fatal(err)
	}
	sourceReadOnly(t, db)
	got, err := (&Reader{DB: db}).Diagnose(context.Background(), 1, requestID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := common.Marshal(got)
	if strings.Contains(string(encoded), "abc-super-private-token") || strings.Contains(string(encoded), "private-model-secret") {
		t.Fatalf("credential leaked through identifier: %s", encoded)
	}
}

func TestReaderSelectsCompileForAllDatabaseDialects(t *testing.T) {
	fixture := sourceTestDB(t)
	conn, err := fixture.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, dialector := range []gorm.Dialector{
		mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}),
		postgres.New(postgres.Config{Conn: conn}),
		sqlite.Open(":memory:"),
	} {
		t.Run(dialector.Name(), func(t *testing.T) {
			db, err := gorm.Open(dialector, &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			var statements []string
			if err := db.Callback().Query().After("gorm:query").Register("support_capture_select", func(tx *gorm.DB) {
				statements = append(statements, tx.Statement.SQL.String())
				if _, ok := tx.Statement.Context.Deadline(); !ok {
					t.Error("dialect SELECT lacks deadline")
				}
				if rows, ok := tx.Statement.Dest.(*[]sourceChargeAnchor); ok {
					*rows = []sourceChargeAnchor{{ID: 1, Attempt: 1, OriginalLedgerID: 1, ChargedQuota: 125000}}
				}
			}); err != nil {
				t.Fatal(err)
			}
			r := &Reader{DB: db, Now: func() time.Time { return sourceTestNow }}
			if _, err := r.Identity(context.Background(), 42); err != ErrNotFound {
				t.Fatal(err)
			}
			if _, err := r.Diagnose(context.Background(), 42, "req"); err != ErrNotFound {
				t.Fatal(err)
			}
			if _, err := r.RecentRequests(context.Background(), 42); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if net, _, err := r.netCharge(ctx, 42, sourceSettlementRow{ID: 1, RequestID: "req", ChargedQuota: 125000}); err != nil || net != "0.25" {
				t.Fatalf("dialect net charge: %s, %v", net, err)
			}
			if len(statements) < 7 {
				t.Fatalf("missing SELECTs: %v", statements)
			}
			for _, statement := range statements {
				upper := strings.ToUpper(statement)
				if !strings.HasPrefix(upper, "SELECT ") || strings.Contains(upper, "SELECT *") || strings.Contains(upper, "FOR UPDATE") {
					t.Fatalf("not a read-only projection: %s", statement)
				}
				if strings.Contains(statement, "ztapi_health_requests") && !strings.Contains(statement, "user_id") {
					t.Fatalf("missing owner predicate: %s", statement)
				}
				if strings.Contains(statement, "ztapi_request_settlements") && !strings.Contains(statement, "user_id") {
					t.Fatalf("missing settlement owner predicate: %s", statement)
				}
			}
		})
	}
}

func TestReaderRedactionUsesSharedPrivacyBoundary(t *testing.T) {
	for _, secret := range []string{
		"API Key: api-value-very-private", "密码：password-very-private", "sk_secret_very_private", "密钥：key-very-private",
	} {
		got := redactSource(secret)
		if strings.Contains(got, "very-private") || strings.Contains(got, "very_private") {
			t.Errorf("unredacted credential: %q", got)
		}
	}
}

func TestReaderWorksWithOnlyDeclaredColumnGrants(t *testing.T) {
	db := sourceTestDB(t)
	sourceInsert(t, db, "users", map[string]any{"id": 42, "role": 1, "status": 1})
	sourceHealth(t, db, 42, "grant-test", sourceTestNow.Add(-time.Minute), "empty_output", 200, `{"Dispatched":true}`)
	sourceSettlement(t, db, 42, "grant-test", "settled", `{}`, true)
	// Views emulate column grants: referencing any undeclared projection,
	// join, filter or ordering column now fails with a real SQL error.
	for table, columns := range map[string]string{
		"users":                         "id, role, status, deleted_at",
		"ztapi_health_requests":         "execution_id, request_id, user_id, public_model, stream, started_at",
		"ztapi_health_events":           "id, execution_id, request_id, reason, http_status, result_valid, outcome",
		"ztapi_request_settlements":     "id, request_id, user_id, public_model, status, reserved_quota, charged_quota, refunded_quota, dispatched, usage_json, missing_dimensions_json, created_at",
		"ztapi_supplier_refund_charges": "id, settlement_id, request_id, user_id, attempt, billing_proof_id, original_ledger_id, charged_quota, refunded_quota",
		"ztapi_supplier_refunds":        "request_id, charge_id, status, ledger_id, refunded_quota",
	} {
		if err := db.Exec("ALTER TABLE " + table + " RENAME TO private_" + table).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("CREATE VIEW " + table + " AS SELECT " + columns + " FROM private_" + table).Error; err != nil {
			t.Fatal(err)
		}
	}
	sourceReadOnly(t, db)
	r := &Reader{DB: db, Now: func() time.Time { return sourceTestNow }}
	if _, err := r.Identity(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Diagnose(context.Background(), 42, "grant-test"); err != nil {
		t.Fatal(err)
	}
	if rows, err := r.RecentRequests(context.Background(), 42); err != nil || len(rows) != 1 {
		t.Fatalf("minimal grants not sufficient: %+v, %v", rows, err)
	}
}
