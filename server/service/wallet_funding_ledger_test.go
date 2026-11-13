package service

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"gorm.io/gorm"
)

func TestWalletFundingLedgerReconcilesRequestLifecycle(t *testing.T) {
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

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, user, _ := setupServiceTokenQuotaTest(t)
			requestID := "wallet-ledger-" + test.name
			funding := &WalletFunding{
				operationID: "operation-" + test.name,
				requestId:   requestID,
				userId:      user.Id,
			}

			if err := funding.PreConsume(test.preConsumed); err != nil {
				t.Fatalf("preconsume wallet: %v", err)
			}
			if test.refund {
				if err := funding.Refund(); err != nil {
					t.Fatalf("refund wallet: %v", err)
				}
			} else if err := funding.Settle(test.actual - test.preConsumed); err != nil {
				t.Fatalf("settle wallet: %v", err)
			}

			assertWalletLedgerReconciliation(t, db, user.Id, requestID, test.wantBalance, test.wantLedgerSum, test.wantRows)
		})
	}
}

func TestBillingSessionReserveAddsWalletReservationLedger(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	requestID := "wallet-progressive-reserve"
	operationID := "operation-progressive-reserve"
	session := &BillingSession{
		operationID: operationID,
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			RequestId:      requestID,
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: true,
		},
		funding: &WalletFunding{
			operationID: operationID,
			requestId:   requestID,
			userId:      user.Id,
		},
	}

	if err := session.Reserve(25); err != nil {
		t.Fatalf("reserve wallet: %v", err)
	}
	assertWalletLedgerReconciliation(t, db, user.Id, requestID, 75, -25, 1)
}

func TestBillingSessionGeneratesAndSynchronizesMissingRequestID(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	funding := &WalletFunding{userId: user.Id}
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: true,
		},
		funding: funding,
	}

	if err := session.Reserve(25); err != nil {
		t.Fatalf("reserve wallet with generated request ID: %v", err)
	}
	requestID := session.relayInfo.RequestId
	if requestID == "" || funding.requestId != requestID {
		t.Fatalf("generated request IDs relay=%q funding=%q", requestID, funding.requestId)
	}
	assertWalletLedgerReconciliation(t, db, user.Id, requestID, 75, -25, 1)
}

func TestWalletFundingIdempotentRetryRestoresCommittedReservationState(t *testing.T) {
	db, user, _ := setupServiceTokenQuotaTest(t)
	const (
		operationID = "operation-idempotent-wallet-retry"
		requestID   = "request-idempotent-wallet-retry"
	)

	first := &WalletFunding{operationID: operationID, requestId: requestID, userId: user.Id}
	if err := first.PreConsume(40); err != nil {
		t.Fatalf("first preconsume: %v", err)
	}

	retried := &WalletFunding{operationID: operationID, requestId: requestID, userId: user.Id}
	if err := retried.PreConsume(40); err != nil {
		t.Fatalf("idempotent preconsume retry: %v", err)
	}
	if retried.consumed != 40 {
		t.Fatalf("retried funding consumed=%d, want 40", retried.consumed)
	}
	if err := retried.Refund(); err != nil {
		t.Fatalf("refund retried reservation: %v", err)
	}

	assertWalletLedgerReconciliation(t, db, user.Id, requestID, 100, 0, 2)
}

func TestApplyWalletBalanceMutationHandlesCommittedCacheSyncFailure(t *testing.T) {
	committed := &model.BalanceLedger{ID: 77, UserID: 9}
	cacheErr := fmt.Errorf("%w: synthetic redis outage", model.ErrBalanceLedgerCacheSync)

	tests := []struct {
		name      string
		results   []error
		entries   []*model.BalanceLedger
		wantCalls int
		wantErr   error
	}{
		{name: "ordinary success", results: []error{nil}, entries: []*model.BalanceLedger{committed}, wantCalls: 1},
		{name: "transient cache failure retries", results: []error{cacheErr, nil}, entries: []*model.BalanceLedger{committed, committed}, wantCalls: 2},
		{name: "persistent cache failure remains a committed debit", results: []error{cacheErr, cacheErr}, entries: []*model.BalanceLedger{committed, committed}, wantCalls: 2},
		{name: "uncommitted failure propagates", results: []error{errors.New("database unavailable")}, entries: []*model.BalanceLedger{nil}, wantCalls: 1, wantErr: errors.New("database unavailable")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			mutation := func(userID int, amount int64, idempotencyKey string, requestID string) (*model.BalanceLedger, bool, error) {
				if userID != 9 || amount != 13 || idempotencyKey != "wallet-key" || requestID != "wallet-request" {
					t.Fatalf("unexpected mutation arguments: user=%d amount=%d key=%q request=%q", userID, amount, idempotencyKey, requestID)
				}
				index := calls
				calls++
				return test.entries[index], index == 0, test.results[index]
			}

			err := applyWalletBalanceMutation(mutation, 9, 13, "wallet-key", "wallet-request")
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("mutation error=%v, want nil", err)
				}
			} else if err == nil || err.Error() != test.wantErr.Error() {
				t.Fatalf("mutation error=%v, want %v", err, test.wantErr)
			}
			if calls != test.wantCalls {
				t.Fatalf("mutation calls=%d, want %d", calls, test.wantCalls)
			}
		})
	}
}

func TestConcurrentWalletPreConsumeLedgersMatchSuccessfulDebits(t *testing.T) {
	const (
		workers = 20
		amount  = 10
	)
	db, user, _ := setupServiceTokenQuotaTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQL database: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)

	start := make(chan struct{})
	results := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			funding := &WalletFunding{
				operationID: fmt.Sprintf("concurrent-operation-%d", index),
				requestId:   fmt.Sprintf("concurrent-request-%d", index),
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
			t.Fatalf("unexpected concurrent reservation error: %v", err)
		}
	}
	if successes != 10 || insufficient != 10 {
		t.Fatalf("concurrent reservations success/insufficient=%d/%d, want 10/10", successes, insufficient)
	}

	var stored model.User
	if err := db.First(&stored, user.Id).Error; err != nil {
		t.Fatalf("reload concurrent wallet user: %v", err)
	}
	if stored.Quota != 0 {
		t.Fatalf("concurrent wallet quota=%d, want 0", stored.Quota)
	}
	var entries []model.BalanceLedger
	if err := db.Where("user_id = ? AND source_type = ?", user.Id, "usage_reservation").Find(&entries).Error; err != nil {
		t.Fatalf("load concurrent reservation ledger: %v", err)
	}
	var sum int64
	for _, entry := range entries {
		sum += entry.Delta
	}
	if len(entries) != successes || sum != -100 {
		t.Fatalf("concurrent reservation ledger rows/sum=%d/%d, want %d/-100", len(entries), sum, successes)
	}
}

func assertWalletLedgerReconciliation(t *testing.T, db *gorm.DB, userID int, requestID string, wantBalance int, wantSum int64, wantRows int) {
	t.Helper()
	var stored model.User
	if err := db.First(&stored, userID).Error; err != nil {
		t.Fatalf("reload wallet user: %v", err)
	}
	if stored.Quota != wantBalance {
		t.Fatalf("wallet quota=%d, want %d", stored.Quota, wantBalance)
	}
	var entries []model.BalanceLedger
	if err := db.Where("user_id = ? AND request_id = ?", userID, requestID).Order("id ASC").Find(&entries).Error; err != nil {
		t.Fatalf("load request ledger: %v", err)
	}
	var sum int64
	reservationRows := 0
	for _, entry := range entries {
		sum += entry.Delta
		if entry.SourceType == "usage_reservation" {
			reservationRows++
		}
	}
	if len(entries) != wantRows || sum != wantSum {
		t.Fatalf("request ledger rows/sum=%d/%d, want %d/%d; entries=%#v", len(entries), sum, wantRows, wantSum, entries)
	}
	if reservationRows != 1 {
		t.Fatalf("request reservation ledger rows=%d, want 1", reservationRows)
	}
}
