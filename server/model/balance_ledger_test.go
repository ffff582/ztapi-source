package model

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestBalanceAdjustmentCommitsBalanceAndLedgerAtomically(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-atomic", 1_000)

	entry, applied, err := AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     77,
		Delta:          500,
		Reason:         "manual recharge verification",
		IdempotencyKey: "atomic-success",
		RequestID:      "request-atomic",
		SourceType:     BalanceLedgerSourceAdmin,
	})
	if err != nil {
		t.Fatalf("adjust balance: %v", err)
	}
	if !applied {
		t.Fatal("first adjustment was reported as a duplicate")
	}
	if entry.BalanceBefore != 1_000 || entry.BalanceAfter != 1_500 {
		t.Fatalf("ledger balances = %d -> %d, want 1000 -> 1500", entry.BalanceBefore, entry.BalanceAfter)
	}
	if entry.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at location = %v, want UTC", entry.CreatedAt.Location())
	}

	var persistedUser User
	if err := db.First(&persistedUser, user.Id).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if persistedUser.Quota != 1_500 {
		t.Fatalf("user quota = %d, want 1500", persistedUser.Quota)
	}
	var count int64
	if err := db.Model(&BalanceLedger{}).Count(&count).Error; err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 1 {
		t.Fatalf("ledger count = %d, want 1", count)
	}

	if err := db.Exec(`CREATE TRIGGER reject_balance_ledger_insert BEFORE INSERT ON balance_ledgers BEGIN SELECT RAISE(ABORT, 'forced ledger insert failure'); END`).Error; err != nil {
		t.Fatalf("create rejecting trigger: %v", err)
	}
	_, _, err = AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     77,
		Delta:          250,
		Reason:         "must roll back",
		IdempotencyKey: "atomic-rollback",
		RequestID:      "request-rollback",
		SourceType:     BalanceLedgerSourceAdmin,
	})
	if err == nil {
		t.Fatal("adjustment succeeded despite forced ledger insert failure")
	}
	if err := db.First(&persistedUser, user.Id).Error; err != nil {
		t.Fatalf("reload user after rollback: %v", err)
	}
	if persistedUser.Quota != 1_500 {
		t.Fatalf("user quota after ledger failure = %d, want 1500", persistedUser.Quota)
	}
}

func TestBalanceAdjustmentDuplicateKeyAppliesOnlyOnceUnderConcurrency(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-idempotent", 1_000)

	const callers = 8
	start := make(chan struct{})
	entries := make(chan *BalanceLedger, callers)
	appliedResults := make(chan bool, callers)
	errorsSeen := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			entry, applied, err := AdjustUserBalance(BalanceAdjustment{
				UserID:         user.Id,
				OperatorID:     88,
				Delta:          500,
				Reason:         "concurrent retry",
				IdempotencyKey: "globally-unique-key",
				RequestID:      "request-concurrent",
				SourceType:     BalanceLedgerSourceAdmin,
			})
			if err != nil {
				errorsSeen <- err
				return
			}
			entries <- entry
			appliedResults <- applied
		}()
	}
	close(start)
	wg.Wait()
	close(entries)
	close(appliedResults)
	close(errorsSeen)

	for err := range errorsSeen {
		t.Fatalf("concurrent adjustment: %v", err)
	}
	var ledgerID int
	for entry := range entries {
		if ledgerID == 0 {
			ledgerID = entry.ID
		}
		if entry.ID != ledgerID {
			t.Fatalf("duplicate returned ledger ID %d, want %d", entry.ID, ledgerID)
		}
	}
	appliedCount := 0
	for applied := range appliedResults {
		if applied {
			appliedCount++
		}
	}
	if appliedCount != 1 {
		t.Fatalf("applied result count = %d, want 1", appliedCount)
	}

	var persistedUser User
	if err := db.First(&persistedUser, user.Id).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if persistedUser.Quota != 1_500 {
		t.Fatalf("user quota = %d, want 1500", persistedUser.Quota)
	}
	var count int64
	if err := db.Model(&BalanceLedger{}).Where("idempotency_key = ?", "globally-unique-key").Count(&count).Error; err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 1 {
		t.Fatalf("ledger count = %d, want 1", count)
	}
}

func TestBalanceAdjustmentRetriesCacheSyncAfterCommittedFailure(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-cache-retry", 1_000)

	originalSync := balanceLedgerCacheSync
	syncCalls := 0
	balanceLedgerCacheSync = func(userID int) error {
		syncCalls++
		if userID != user.Id {
			t.Fatalf("cache sync user ID = %d, want %d", userID, user.Id)
		}
		if syncCalls == 1 {
			return errors.New("redis unavailable")
		}
		return nil
	}
	t.Cleanup(func() { balanceLedgerCacheSync = originalSync })

	adjustment := BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     88,
		Delta:          500,
		Reason:         "cache retry",
		IdempotencyKey: "cache-retry-key",
		RequestID:      "request-cache-retry",
		SourceType:     BalanceLedgerSourceAdmin,
	}
	committed, applied, err := AdjustUserBalance(adjustment)
	if !errors.Is(err, ErrBalanceLedgerCacheSync) {
		t.Fatalf("first error = %v, want %v", err, ErrBalanceLedgerCacheSync)
	}
	if committed == nil || !applied {
		t.Fatalf("committed result = %#v, applied=%v; want committed applied entry", committed, applied)
	}

	var persistedUser User
	if err := db.First(&persistedUser, user.Id).Error; err != nil {
		t.Fatalf("reload committed user: %v", err)
	}
	if persistedUser.Quota != 1_500 {
		t.Fatalf("committed quota = %d, want 1500", persistedUser.Quota)
	}

	retried, retryApplied, err := AdjustUserBalance(adjustment)
	if err != nil {
		t.Fatalf("idempotent cache-sync retry: %v", err)
	}
	if retryApplied {
		t.Fatal("idempotent retry applied the balance twice")
	}
	if retried == nil || retried.ID != committed.ID {
		t.Fatalf("retry entry = %#v, want ledger ID %d", retried, committed.ID)
	}
	if syncCalls != 2 {
		t.Fatalf("cache sync calls = %d, want 2", syncCalls)
	}
	if err := db.First(&persistedUser, user.Id).Error; err != nil {
		t.Fatalf("reload user after retry: %v", err)
	}
	if persistedUser.Quota != 1_500 {
		t.Fatalf("quota after retry = %d, want 1500", persistedUser.Quota)
	}
}

func TestBalanceAdjustmentRejectsInvalidFinancialState(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-validation", 100)

	tests := []struct {
		name   string
		delta  int64
		reason string
		key    string
		want   error
	}{
		{name: "blank reason", delta: 1, reason: " \t\n", key: "blank-reason", want: ErrBalanceLedgerReasonRequired},
		{name: "negative result", delta: -101, reason: "chargeback", key: "negative-result", want: ErrBalanceLedgerNegativeBalance},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := AdjustUserBalance(BalanceAdjustment{
				UserID:         user.Id,
				OperatorID:     99,
				Delta:          test.delta,
				Reason:         test.reason,
				IdempotencyKey: test.key,
				SourceType:     BalanceLedgerSourceAdmin,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	if err := db.Model(&User{}).Where("id = ?", user.Id).Update("quota", maxBalanceLedgerQuota).Error; err != nil {
		t.Fatalf("set maximum quota: %v", err)
	}
	_, _, err := AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     99,
		Delta:          1,
		Reason:         "overflow attempt",
		IdempotencyKey: "overflow",
		SourceType:     BalanceLedgerSourceAdmin,
	})
	if !errors.Is(err, ErrBalanceLedgerOverflow) {
		t.Fatalf("overflow error = %v, want %v", err, ErrBalanceLedgerOverflow)
	}

	var count int64
	if err := db.Model(&BalanceLedger{}).Count(&count).Error; err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid adjustments created %d ledger rows, want 0", count)
	}
}

func TestBalanceAdjustmentIdempotencyKeyLengthBoundary(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-key-length", 100)

	entry, applied, err := AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     99,
		Delta:          1,
		Reason:         "maximum key length",
		IdempotencyKey: strings.Repeat("a", BalanceLedgerIdempotencyKeyMaxLength),
		SourceType:     BalanceLedgerSourceAdmin,
	})
	if err != nil || !applied || entry == nil {
		t.Fatalf("128-byte key result = %#v, applied=%v, err=%v", entry, applied, err)
	}

	entry, applied, err = AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     99,
		Delta:          1,
		Reason:         "overlong key",
		IdempotencyKey: strings.Repeat("b", BalanceLedgerIdempotencyKeyMaxLength+1),
		SourceType:     BalanceLedgerSourceAdmin,
	})
	if !errors.Is(err, ErrBalanceLedgerIdempotencyKeyTooLong) {
		t.Fatalf("129-byte key error = %v, want %v", err, ErrBalanceLedgerIdempotencyKeyTooLong)
	}
	if entry != nil || applied {
		t.Fatalf("overlong key returned entry=%#v applied=%v", entry, applied)
	}

	var persistedUser User
	if err := db.First(&persistedUser, user.Id).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if persistedUser.Quota != 101 {
		t.Fatalf("quota = %d, want 101", persistedUser.Quota)
	}
	var count int64
	if err := db.Model(&BalanceLedger{}).Count(&count).Error; err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 1 {
		t.Fatalf("ledger count = %d, want 1", count)
	}
}

func TestBalanceAdjustmentRejectsZeroDeltaAndConflictingIdempotencyReuse(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	first := createBalanceLedgerTestUser(t, db, "ledger-conflict-first", 100)
	second := createBalanceLedgerTestUser(t, db, "ledger-conflict-second", 200)

	if _, _, err := AdjustUserBalance(BalanceAdjustment{
		UserID: first.Id, OperatorID: 10, Delta: 0, Reason: "zero is not a ledger event", IdempotencyKey: "zero-delta",
	}); !errors.Is(err, ErrBalanceLedgerZeroDelta) {
		t.Fatalf("zero delta error = %v, want %v", err, ErrBalanceLedgerZeroDelta)
	}

	original := BalanceAdjustment{
		UserID: first.Id, OperatorID: 10, Delta: 25, Reason: "manual recharge", IdempotencyKey: "request-fingerprint", RequestID: "request-fingerprint-original",
	}
	entry, applied, err := AdjustUserBalance(original)
	if err != nil || !applied {
		t.Fatalf("seed adjustment: entry=%#v applied=%v err=%v", entry, applied, err)
	}
	retry, retryApplied, err := AdjustUserBalance(original)
	if err != nil || retryApplied || retry.ID != entry.ID {
		t.Fatalf("same request retry: entry=%#v applied=%v err=%v", retry, retryApplied, err)
	}
	httpRetry := original
	httpRetry.RequestID = "request-fingerprint-new-http-attempt"
	retry, retryApplied, err = AdjustUserBalance(httpRetry)
	if err != nil || retryApplied || retry.ID != entry.ID {
		t.Fatalf("admin HTTP retry with a new trace ID: entry=%#v applied=%v err=%v", retry, retryApplied, err)
	}

	conflicts := []BalanceAdjustment{
		{UserID: second.Id, OperatorID: 10, Delta: 25, Reason: "manual recharge", IdempotencyKey: "request-fingerprint", RequestID: "request-fingerprint-original"},
		{UserID: first.Id, OperatorID: 10, Delta: 30, Reason: "manual recharge", IdempotencyKey: "request-fingerprint", RequestID: "request-fingerprint-original"},
		{UserID: first.Id, OperatorID: 11, Delta: 25, Reason: "manual recharge", IdempotencyKey: "request-fingerprint", RequestID: "request-fingerprint-original"},
		{UserID: first.Id, OperatorID: 10, Delta: 25, Reason: "different reason", IdempotencyKey: "request-fingerprint", RequestID: "request-fingerprint-original"},
	}
	for _, conflict := range conflicts {
		if _, _, err := AdjustUserBalance(conflict); !errors.Is(err, ErrBalanceLedgerIdempotencyConflict) {
			t.Fatalf("conflicting retry %#v error = %v, want %v", conflict, err, ErrBalanceLedgerIdempotencyConflict)
		}
	}

	var persistedFirst, persistedSecond User
	if err := db.First(&persistedFirst, first.Id).Error; err != nil {
		t.Fatalf("reload first user: %v", err)
	}
	if err := db.First(&persistedSecond, second.Id).Error; err != nil {
		t.Fatalf("reload second user: %v", err)
	}
	if persistedFirst.Quota != 125 || persistedSecond.Quota != 200 {
		t.Fatalf("conflicting retry changed balances: first=%d second=%d", persistedFirst.Quota, persistedSecond.Quota)
	}
}

func TestWalletReservationIdempotencyRejectsDifferentRequestID(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-wallet-request-conflict", 100)

	entry, applied, err := ReserveUserBalanceDebit(user.Id, 25, "wallet-request-fingerprint", "wallet-request-original")
	if err != nil || !applied || entry == nil {
		t.Fatalf("seed wallet reservation: entry=%#v applied=%v err=%v", entry, applied, err)
	}
	conflict, conflictApplied, err := ReserveUserBalanceDebit(user.Id, 25, "wallet-request-fingerprint", "wallet-request-different")
	if !errors.Is(err, ErrBalanceLedgerIdempotencyConflict) || conflict != nil || conflictApplied {
		t.Fatalf("cross-request wallet retry: entry=%#v applied=%v err=%v", conflict, conflictApplied, err)
	}
}

func TestWalletUsageBalanceMutationRequiresRequestID(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-wallet-request-required", 100)

	if entry, applied, err := ReserveUserBalanceDebit(user.Id, 25, "wallet-request-required-reserve", " \t "); !errors.Is(err, ErrBalanceLedgerRequestIDRequired) || entry != nil || applied {
		t.Fatalf("blank reservation request ID: entry=%#v applied=%v err=%v", entry, applied, err)
	}
	if entry, applied, err := SettleUserBalanceDebit(user.Id, 25, "wallet-request-required-settle", "\n"); !errors.Is(err, ErrBalanceLedgerRequestIDRequired) || entry != nil || applied {
		t.Fatalf("blank settlement request ID: entry=%#v applied=%v err=%v", entry, applied, err)
	}

	var stored User
	if err := db.First(&stored, user.Id).Error; err != nil {
		t.Fatalf("reload wallet user: %v", err)
	}
	if stored.Quota != 100 {
		t.Fatalf("blank request ID changed quota to %d, want 100", stored.Quota)
	}
	var count int64
	if err := db.Model(&BalanceLedger{}).Count(&count).Error; err != nil {
		t.Fatalf("count wallet ledgers: %v", err)
	}
	if count != 0 {
		t.Fatalf("blank request ID created %d ledger rows, want 0", count)
	}
}

func TestBalanceLedgerRejectsUpdatesAndDeletes(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-immutable", 100)
	entry, _, err := AdjustUserBalance(BalanceAdjustment{
		UserID:         user.Id,
		OperatorID:     101,
		Delta:          25,
		Reason:         "immutable entry",
		IdempotencyKey: "immutable-entry",
		SourceType:     BalanceLedgerSourceAdmin,
	})
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}

	if err := db.Model(entry).Update("reason", "tampered").Error; !errors.Is(err, ErrBalanceLedgerImmutable) {
		t.Fatalf("update error = %v, want %v", err, ErrBalanceLedgerImmutable)
	}
	if err := db.Model(entry).UpdateColumn("reason", "tampered-column").Error; !errors.Is(err, ErrBalanceLedgerImmutable) {
		t.Fatalf("UpdateColumn error = %v, want %v", err, ErrBalanceLedgerImmutable)
	}
	if err := db.Model(entry).UpdateColumns(map[string]interface{}{"reason": "tampered-columns", "delta": 999}).Error; !errors.Is(err, ErrBalanceLedgerImmutable) {
		t.Fatalf("UpdateColumns error = %v, want %v", err, ErrBalanceLedgerImmutable)
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Model(entry).Updates(map[string]interface{}{"reason": "tampered-skip-hooks"}).Error; !errors.Is(err, ErrBalanceLedgerImmutable) {
		t.Fatalf("SkipHooks update error = %v, want %v", err, ErrBalanceLedgerImmutable)
	}
	if err := db.Table("balance_ledgers").Model(&User{}).Where("balance_ledgers.id = ?", entry.ID).UpdateColumn("reason", "tampered-explicit-table").Error; !errors.Is(err, ErrBalanceLedgerImmutable) {
		t.Fatalf("explicit-table UpdateColumn error = %v, want %v", err, ErrBalanceLedgerImmutable)
	}
	if err := db.Delete(entry).Error; !errors.Is(err, ErrBalanceLedgerImmutable) {
		t.Fatalf("delete error = %v, want %v", err, ErrBalanceLedgerImmutable)
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Unscoped().Delete(&BalanceLedger{}, entry.ID).Error; !errors.Is(err, ErrBalanceLedgerImmutable) {
		t.Fatalf("SkipHooks delete error = %v, want %v", err, ErrBalanceLedgerImmutable)
	}

	var persisted BalanceLedger
	if err := db.First(&persisted, entry.ID).Error; err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	if persisted.Reason != "immutable entry" {
		t.Fatalf("reason = %q, want immutable entry", persisted.Reason)
	}
}

func TestBalanceLedgerRejectsAliasedTableMutations(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	user := createBalanceLedgerTestUser(t, db, "ledger-aliased-immutable", 100)

	tests := []struct {
		name   string
		mutate func(*BalanceLedger) error
	}{
		{
			name: "UpdateColumn",
			mutate: func(entry *BalanceLedger) error {
				return db.Table("balance_ledgers AS bl").Unscoped().Model(&User{}).
					Where("bl.id = ?", entry.ID).UpdateColumn("reason", "aliased-tamper").Error
			},
		},
		{
			name: "UpdateColumns",
			mutate: func(entry *BalanceLedger) error {
				return db.Table("`main`.`balance_ledgers` AS `bl`").Unscoped().Model(&User{}).
					Where("bl.id = ?", entry.ID).UpdateColumns(map[string]interface{}{"reason": "qualified-tamper", "delta": 999}).Error
			},
		},
		{
			name: "UnscopedDelete",
			mutate: func(entry *BalanceLedger) error {
				return db.Table(`"balance_ledgers" AS "bl"`).Unscoped().
					Where("bl.id = ?", entry.ID).Delete(&User{}).Error
			},
		},
		{
			name: "JoinedUpdate",
			mutate: func(entry *BalanceLedger) error {
				return db.Table("balance_ledgers AS bl JOIN users AS u ON u.id = bl.user_id AND u.id = ?", user.Id).Unscoped().Model(&User{}).
					Where("bl.id = ?", entry.ID).Update("reason", "joined-tamper").Error
			},
		},
		{
			name: "CommaSeparatedUpdateColumns",
			mutate: func(entry *BalanceLedger) error {
				return db.Table("users AS u, `main`.`balance_ledgers` AS `bl`").Unscoped().Model(&User{}).
					Where("bl.id = ?", entry.ID).UpdateColumns(map[string]interface{}{"reason": "comma-tamper", "delta": 999}).Error
			},
		},
		{
			name: "JoinedUnscopedDelete",
			mutate: func(entry *BalanceLedger) error {
				return db.Table(`users AS u JOIN "balance_ledgers" AS "bl" ON bl.user_id = u.id`).Unscoped().
					Where("bl.id = ?", entry.ID).Delete(&User{}).Error
			},
		},
		{
			name: "StraightJoinUpdate",
			mutate: func(entry *BalanceLedger) error {
				return db.Table("users AS u STRAIGHT_JOIN balance_ledgers AS bl ON bl.user_id = u.id").Unscoped().Model(&User{}).
					Where("bl.id = ?", entry.ID).Update("reason", "straight-join-tamper").Error
			},
		},
		{
			name: "ParameterizedClauseTableUpdateColumns",
			mutate: func(entry *BalanceLedger) error {
				return db.Table("?", clause.Table{Name: "balance_ledgers"}).Unscoped().Model(&User{}).
					Where("id = ?", entry.ID).UpdateColumns(map[string]interface{}{"reason": "parameterized-tamper", "delta": 999}).Error
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry, _, err := AdjustUserBalance(BalanceAdjustment{
				UserID:         user.Id,
				OperatorID:     101,
				Delta:          1,
				Reason:         "aliased immutable entry",
				IdempotencyKey: fmt.Sprintf("aliased-immutable-%d", index),
				SourceType:     BalanceLedgerSourceAdmin,
			})
			if err != nil {
				t.Fatalf("create ledger: %v", err)
			}
			if err := test.mutate(entry); !errors.Is(err, ErrBalanceLedgerImmutable) {
				t.Fatalf("mutation error = %v, want %v", err, ErrBalanceLedgerImmutable)
			}

			var persisted BalanceLedger
			if err := db.First(&persisted, entry.ID).Error; err != nil {
				t.Fatalf("reload immutable ledger: %v", err)
			}
			if persisted.Reason != "aliased immutable entry" || persisted.Delta != 1 {
				t.Fatalf("persisted ledger was mutated: %#v", persisted)
			}
		})
	}
}

func TestBalanceLedgerRejectsRecursiveTableExpressionVariables(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	table := clause.Table{Name: "balance_ledgers"}
	expression := clause.Expr{SQL: "?", Vars: []interface{}{table}}
	overlapping := []interface{}{"users", "balance_ledgers"}

	tests := []struct {
		name  string
		value interface{}
	}{
		{name: "string", value: "balance_ledgers"},
		{name: "clause table", value: table},
		{name: "clause table pointer", value: &table},
		{name: "clause expression", value: expression},
		{name: "clause expression pointer", value: &expression},
		{name: "nested slice", value: []interface{}{"users", []interface{}{expression}}},
		{name: "nested array", value: [2]interface{}{"users", &table}},
		{name: "overlapping slices", value: []interface{}{overlapping[:1], overlapping[:2]}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := db.Session(&gorm.Session{DryRun: true}).Table("?", test.value).Unscoped().Model(&User{}).
				Where("id = ?", 1).UpdateColumn("quota", 1).Error
			if !errors.Is(err, ErrBalanceLedgerImmutable) {
				t.Fatalf("recursive TableExpr variable error = %v, want %v", err, ErrBalanceLedgerImmutable)
			}
		})
	}
}

func TestBalanceLedgerDoesNotRejectArchiveIdentifiers(t *testing.T) {
	db := setupBalanceLedgerTestDB(t)
	tests := []struct {
		name       string
		expression string
		vars       []interface{}
	}{
		{name: "statement table", expression: "balance_ledgers_archive"},
		{name: "string", expression: "?", vars: []interface{}{"balance_ledgers_archive"}},
		{name: "clause table", expression: "?", vars: []interface{}{clause.Table{Name: "balance_ledgers_archive"}}},
		{name: "nested expression", expression: "?", vars: []interface{}{clause.Expr{SQL: "?", Vars: []interface{}{[]interface{}{clause.Table{Name: "balance_ledgers_archive"}}}}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := db.Session(&gorm.Session{DryRun: true}).Table(test.expression, test.vars...).Unscoped().Model(&User{}).
				Where("id = ?", 1).UpdateColumn("quota", 1).Error
			if err != nil {
				t.Fatalf("archive identifier update error = %v, want nil", err)
			}
		})
	}
}

func TestBalanceLedgerTableExpressionNormalization(t *testing.T) {
	tests := []struct {
		expression string
		want       bool
	}{
		{expression: "balance_ledgers", want: true},
		{expression: "balance_ledgers AS bl", want: true},
		{expression: "main.balance_ledgers bl", want: true},
		{expression: "`main`.`balance_ledgers` AS `bl`", want: true},
		{expression: `"balance_ledgers" AS "bl"`, want: true},
		{expression: "[main].[balance_ledgers] AS [bl]", want: true},
		{expression: "balance_ledgers AS bl JOIN users AS u ON u.id = bl.user_id", want: true},
		{expression: "users AS u JOIN main.balance_ledgers AS bl ON bl.user_id = u.id", want: true},
		{expression: "users AS u, `main`.`balance_ledgers` AS `bl`", want: true},
		{expression: `users AS u LEFT JOIN "main"."balance_ledgers" AS "bl" ON bl.user_id = u.id`, want: true},
		{expression: "users AS u STRAIGHT_JOIN balance_ledgers AS bl ON bl.user_id = u.id", want: true},
		{expression: "balance_ledgers_archive AS bl", want: false},
		{expression: "archive_balance_ledgers AS bl", want: false},
		{expression: "main.balance_ledgers_archive AS bl", want: false},
		{expression: "balance_ledgers_archive AS bla JOIN users AS u ON u.id = bla.user_id", want: false},
		{expression: "users AS u JOIN archive_balance_ledgers AS abl ON abl.user_id = u.id", want: false},
		{expression: "users AS u, `main`.`balance_ledgers_archive` AS `bla`", want: false},
		{expression: "balance_ledgers.extra AS bl", want: true},
		{expression: "(SELECT * FROM balance_ledgers) AS bl", want: true},
		{expression: "unrelated AS balance_ledgers", want: true},
		{expression: "users AS balance_ledgers JOIN audit_events AS ae ON ae.user_id = balance_ledgers.id", want: true},
		{expression: "users AS u JOIN audit_events AS ae ON ae.subject = 'balance_ledgers'", want: false},
		{expression: "users AS u /* JOIN balance_ledgers */", want: false},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			if got := balanceLedgerTableExpressionTargetsLedger(test.expression); got != test.want {
				t.Fatalf("balanceLedgerTableExpressionTargetsLedger(%q) = %v, want %v", test.expression, got, test.want)
			}
		})
	}
}

func setupBalanceLedgerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := DB
	originalSQLite := common.UsingSQLite
	originalMySQL := common.UsingMySQL
	originalPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access sqlite: %v", err)
	}
	DB = db
	if err := db.AutoMigrate(&User{}, &BalanceLedger{}); err != nil {
		t.Fatalf("migrate ledger tables: %v", err)
	}
	if err := registerBalanceLedgerImmutability(db); err != nil {
		t.Fatalf("register ledger immutability: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		DB = originalDB
		common.UsingSQLite = originalSQLite
		common.UsingMySQL = originalMySQL
		common.UsingPostgreSQL = originalPostgreSQL
		common.RedisEnabled = originalRedisEnabled
	})
	return db
}

func createBalanceLedgerTestUser(t *testing.T, db *gorm.DB, username string, quota int) User {
	t.Helper()
	user := User{Username: username, Password: "not-used", Quota: quota, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AffCode: username + "-aff"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}
