package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type tokenQuotaTestFunding struct {
	preConsumed   int
	settled       int
	refunded      bool
	preConsumeErr error
}

func (funding *tokenQuotaTestFunding) Source() string {
	return BillingSourceWallet
}

func (funding *tokenQuotaTestFunding) PreConsume(amount int) error {
	funding.preConsumed += amount
	return funding.preConsumeErr
}

func (funding *tokenQuotaTestFunding) Settle(delta int) error {
	funding.settled += delta
	return nil
}

func (funding *tokenQuotaTestFunding) Refund() error {
	funding.refunded = true
	return nil
}

func TestSnapshotPreConsumedQuotaReturnCapturesOnlyBillingIdentity(t *testing.T) {
	original := &relaycommon.RelayInfo{
		UserId:                   17,
		TokenId:                  23,
		TokenKeyHash:             "token-hash",
		TokenUnlimited:           true,
		BillingSource:            BillingSourceSubscription,
		SubscriptionId:           31,
		FinalPreConsumedQuota:    41,
		SubscriptionPostDelta:    99,
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Version: 7},
	}

	snapshot, quota := snapshotPreConsumedQuotaReturn(original)

	if quota != 41 {
		t.Fatalf("quota = %d, want 41", quota)
	}
	if snapshot.UserId != 17 || snapshot.TokenId != 23 || snapshot.TokenKeyHash != "token-hash" {
		t.Fatalf("billing identity was not preserved: %+v", snapshot)
	}
	if !snapshot.TokenUnlimited || snapshot.BillingSource != BillingSourceSubscription || snapshot.SubscriptionId != 31 {
		t.Fatalf("billing source was not preserved: %+v", snapshot)
	}
	if snapshot.FinalPreConsumedQuota != 0 || snapshot.SubscriptionPostDelta != 0 || snapshot.ZTAPIPublicationSnapshot != nil {
		t.Fatalf("unrelated mutable request state leaked into refund snapshot: %+v", snapshot)
	}

	original.UserId = 999
	if snapshot.UserId != 17 {
		t.Fatal("snapshot changed after the request state was mutated")
	}
}

func setupServiceTokenQuotaTest(t *testing.T) (*gorm.DB, *model.User, *model.Token) {
	t.Helper()

	originalRedisEnabled := common.RedisEnabled
	originalBatchUpdateEnabled := common.BatchUpdateEnabled
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	t.Cleanup(func() {
		common.RedisEnabled = originalRedisEnabled
		common.BatchUpdateEnabled = originalBatchUpdateEnabled
	})

	dsn := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	model.DB = db
	if err := db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.BalanceLedger{},
		&model.BillingRefundPending{},
	); err != nil {
		t.Fatalf("migrate quota tables: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	user := &model.User{
		Username: "quota-user-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Password: "not-used",
		Status:   common.UserStatusEnabled,
		Quota:    100,
		Group:    "default",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create quota user: %v", err)
	}
	plaintext, keyHash, keyPrefix, err := common.GenerateZTAPIKey()
	if err != nil {
		t.Fatalf("generate token key: %v", err)
	}
	_ = plaintext
	token := &model.Token{
		UserId:         user.Id,
		KeyHash:        keyHash,
		KeyPrefix:      keyPrefix,
		Status:         common.TokenStatusEnabled,
		Name:           "unlimited-quota",
		ExpiredTime:    -1,
		RemainQuota:    100,
		UsedQuota:      11,
		UnlimitedQuota: true,
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("create quota token: %v", err)
	}
	return db, user, token
}

func assertServiceTokenQuota(t *testing.T, db *gorm.DB, tokenID int, remain int, used int) {
	t.Helper()

	var stored model.Token
	if err := db.First(&stored, tokenID).Error; err != nil {
		t.Fatalf("reload token quota: %v", err)
	}
	if stored.RemainQuota != remain || stored.UsedQuota != used {
		t.Fatalf(
			"token quota remain=%d used=%d, want remain=%d used=%d",
			stored.RemainQuota,
			stored.UsedQuota,
			remain,
			used,
		)
	}
}

func TestBillingSessionPreConsumeDoesNotTrackUnlimitedTokenReservation(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	funding := &tokenQuotaTestFunding{}
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: true,
		},
		funding: funding,
	}

	if apiErr := session.preConsume(ctx, 25); apiErr != nil {
		t.Fatalf("pre-consume unlimited token: %v", apiErr)
	}

	if session.tokenConsumed != 0 {
		t.Fatalf("tracked finite token reservation=%d, want 0", session.tokenConsumed)
	}
	if funding.preConsumed != 25 {
		t.Fatalf("funding pre-consume=%d, want 25", funding.preConsumed)
	}
	if !session.NeedsRefund() {
		t.Fatal("reserved funding must remain refundable for an unlimited token")
	}
	assertServiceTokenQuota(t, db, token.Id, 100, 11)
}

func TestBillingSessionPreConsumePersistsTokenRollbackBeforeReturningFundingError(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Updates(map[string]any{
		"unlimited_quota": false,
		"remain_quota":    100,
		"used_quota":      11,
	}).Error; err != nil {
		t.Fatalf("configure finite token: %v", err)
	}

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			RequestId:      "funding-failure-" + strings.ReplaceAll(t.Name(), "/", "-"),
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: false,
		},
		funding: &tokenQuotaTestFunding{preConsumeErr: errors.New("funding unavailable")},
	}
	if apiErr := session.preConsume(ctx, 25); apiErr == nil {
		t.Fatal("funding failure did not abort pre-consumption")
	}
	assertServiceTokenQuota(t, db, token.Id, 100, 11)

	var refund model.BillingRefundPending
	if err := db.Where("request_id = ? AND kind = ?", session.relayInfo.RequestId, model.BillingRefundKindToken).
		First(&refund).Error; err != nil {
		t.Fatalf("load durable token rollback: %v", err)
	}
	if refund.Status != model.BillingRefundStatusCompleted || refund.Amount != 25 {
		t.Fatalf("durable token rollback status=%q amount=%d", refund.Status, refund.Amount)
	}
}

func TestBillingSessionSettleDoesNotChargeUnlimitedToken(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	funding := &tokenQuotaTestFunding{}
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: true,
		},
		funding:          funding,
		preConsumedQuota: 20,
	}

	if err := session.Settle(25); err != nil {
		t.Fatalf("settle unlimited token: %v", err)
	}

	if funding.settled != 5 {
		t.Fatalf("funding settlement=%d, want 5", funding.settled)
	}
	assertServiceTokenQuota(t, db, token.Id, 100, 11)
}

func TestBillingSessionReserveDoesNotTrackUnlimitedTokenReservation(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: true,
		},
		funding: &WalletFunding{userId: user.Id},
	}

	if err := session.Reserve(25); err != nil {
		t.Fatalf("reserve unlimited token funding: %v", err)
	}

	if session.tokenConsumed != 0 {
		t.Fatalf("tracked finite token reserve=%d, want 0", session.tokenConsumed)
	}
	assertServiceTokenQuota(t, db, token.Id, 100, 11)
	var storedUser model.User
	if err := db.First(&storedUser, user.Id).Error; err != nil {
		t.Fatalf("reload reserved quota user: %v", err)
	}
	if storedUser.Quota != 75 {
		t.Fatalf("user quota=%d, want 75", storedUser.Quota)
	}
}

func TestPostConsumeQuotaDoesNotChargeUnlimitedToken(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	relayInfo := &relaycommon.RelayInfo{
		UserId:         user.Id,
		TokenId:        token.Id,
		TokenKeyHash:   token.KeyHash,
		TokenUnlimited: true,
		BillingSource:  BillingSourceWallet,
	}

	if err := PostConsumeQuota(relayInfo, 5, 0, false); err != nil {
		t.Fatalf("post-consume unlimited token: %v", err)
	}

	assertServiceTokenQuota(t, db, token.Id, 100, 11)
	var storedUser model.User
	if err := db.First(&storedUser, user.Id).Error; err != nil {
		t.Fatalf("reload quota user: %v", err)
	}
	if storedUser.Quota != 95 {
		t.Fatalf("user quota=%d, want 95", storedUser.Quota)
	}
}

func TestFinitePlaygroundTokenStillReservesQuota(t *testing.T) {
	db, _, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Updates(map[string]any{
		"unlimited_quota": false,
		"remain_quota":    25,
		"used_quota":      4,
	}).Error; err != nil {
		t.Fatalf("configure finite playground token: %v", err)
	}
	info := &relaycommon.RelayInfo{
		TokenId:        token.Id,
		TokenKeyHash:   token.KeyHash,
		TokenUnlimited: false,
		IsPlayground:   true,
	}

	if err := PreConsumeTokenQuota(info, 25); err != nil {
		t.Fatalf("reserve finite playground token: %v", err)
	}
	assertServiceTokenQuota(t, db, token.Id, 0, 29)
}

func TestConcurrentUnlimitedTokensCannotOverdrawPrepaidWallet(t *testing.T) {
	const (
		reservation = 100
		workerCount = 50
	)
	db, user, token := setupServiceTokenQuotaTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQL database: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)

	originalQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 1
	t.Cleanup(func() {
		common.QuotaPerUnit = originalQuotaPerUnit
	})

	start := make(chan struct{})
	ready := make(chan struct{}, workerCount)
	results := make(chan *types.NewAPIError, workerCount)
	var wait sync.WaitGroup
	for range workerCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, _ := gin.CreateTestContext(nil)
			info := &relaycommon.RelayInfo{
				UserId:         user.Id,
				TokenId:        token.Id,
				TokenKeyHash:   token.KeyHash,
				TokenUnlimited: true,
				UserSetting: dto.UserSetting{
					BillingPreference: "wallet_only",
				},
			}

			ready <- struct{}{}
			<-start
			results <- PreConsumeBilling(ctx, reservation, info)
		}()
	}
	for range workerCount {
		<-ready
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	insufficient := 0
	for result := range results {
		if result == nil {
			successes++
			continue
		}
		if result.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
			insufficient++
		}
	}
	if successes != 1 || insufficient != workerCount-1 {
		t.Fatalf("wallet reservations successes=%d insufficient=%d, want 1/%d", successes, insufficient, workerCount-1)
	}

	var storedUser model.User
	if err := db.First(&storedUser, user.Id).Error; err != nil {
		t.Fatalf("reload wallet user: %v", err)
	}
	if storedUser.Quota != 0 {
		t.Fatalf("wallet quota=%d, want 0", storedUser.Quota)
	}
}

func TestFiniteTokenReservationGatesRealPrepaidBillingDispatch(t *testing.T) {
	const (
		reservation = 25
		initialUsed = 7
	)
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Updates(map[string]any{
		"unlimited_quota": false,
		"remain_quota":    reservation,
		"used_quota":      initialUsed,
	}).Error; err != nil {
		t.Fatalf("configure finite token: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQL database: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)

	originalQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 1
	common.BatchUpdateEnabled = true
	t.Cleanup(func() {
		common.QuotaPerUnit = originalQuotaPerUnit
	})

	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	successfulSessions := make(chan *BillingSession, 1)
	var terminalCalls atomic.Int32
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, _ := gin.CreateTestContext(nil)
			ctx.Set("token_quota", reservation)
			info := &relaycommon.RelayInfo{
				UserId:         user.Id,
				TokenId:        token.Id,
				TokenKeyHash:   token.KeyHash,
				TokenUnlimited: false,
				UserSetting: dto.UserSetting{
					BillingPreference: "wallet_only",
				},
			}

			ready <- struct{}{}
			<-start
			if apiErr := PreConsumeBilling(ctx, reservation, info); apiErr != nil {
				results <- apiErr
				return
			}

			terminalCalls.Add(1)
			successfulSessions <- info.Billing.(*BillingSession)
			results <- nil
		}()
	}
	<-ready
	<-ready
	close(start)
	wait.Wait()
	close(results)
	close(successfulSessions)

	successes := 0
	failures := 0
	for result := range results {
		if result == nil {
			successes++
		} else {
			failures++
		}
	}
	if terminalCalls.Load() != 1 {
		t.Fatalf("terminal dispatch calls=%d, want exactly 1", terminalCalls.Load())
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("billing results successes=%d failures=%d, want 1/1", successes, failures)
	}

	common.BatchUpdateEnabled = false
	for session := range successfulSessions {
		if err := session.Settle(reservation); err != nil {
			t.Fatalf("settle successful prepaid session: %v", err)
		}
	}
	assertServiceTokenQuota(t, db, token.Id, 0, initialUsed+reservation)

	var storedUser model.User
	if err := db.First(&storedUser, user.Id).Error; err != nil {
		t.Fatalf("reload wallet user: %v", err)
	}
	if storedUser.Quota != 100-reservation {
		t.Fatalf("wallet quota=%d, want %d after prepaid settlement", storedUser.Quota, 100-reservation)
	}
}

func TestReserveExtendsFiniteTokenAndPrepaidWalletReservation(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Updates(map[string]any{
		"unlimited_quota": false,
		"remain_quota":    90,
		"used_quota":      21,
	}).Error; err != nil {
		t.Fatalf("configure finite token: %v", err)
	}

	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: false,
		},
		funding:       &WalletFunding{userId: user.Id},
		tokenConsumed: 10,
	}
	if err := session.Reserve(25); err != nil {
		t.Fatalf("extend prepaid reservation: %v", err)
	}

	if session.tokenConsumed != 25 {
		t.Fatalf("token reservation=%d, want 25", session.tokenConsumed)
	}
	assertServiceTokenQuota(t, db, token.Id, 75, 36)

	var storedUser model.User
	if err := db.First(&storedUser, user.Id).Error; err != nil {
		t.Fatalf("reload wallet user: %v", err)
	}
	if storedUser.Quota != 75 {
		t.Fatalf("wallet quota=%d, want 75 after extending prepaid reservation", storedUser.Quota)
	}
}

func TestBillingSessionTokenRefundCanRetryAfterTransientDatabaseFailure(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Updates(map[string]any{
		"unlimited_quota": false,
		"remain_quota":    75,
		"used_quota":      36,
	}).Error; err != nil {
		t.Fatalf("configure reserved finite token: %v", err)
	}

	funding := &tokenQuotaTestFunding{}
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			RequestId:      "refund-retry-" + strings.ReplaceAll(t.Name(), "/", "-"),
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: false,
		},
		funding:       funding,
		tokenConsumed: 25,
	}

	callbackName := "test:fail-token-refund-once:" + strings.ReplaceAll(t.Name(), "/", "_")
	failureObserved := make(chan struct{}, 1)
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" && failOnce.CompareAndSwap(true, false) {
			select {
			case failureObserved <- struct{}{}:
			default:
			}
			_ = tx.AddError(errors.New("transient token refund failure"))
		}
	}); err != nil {
		t.Fatalf("register transient failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(callbackName)
	})

	ctx, _ := gin.CreateTestContext(nil)
	if err := session.Refund(ctx); err == nil {
		t.Fatal("transient token refund failure was not returned")
	}
	select {
	case <-failureObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("token refund did not reach authoritative database update")
	}

	if session.refunded {
		t.Fatal("failed token refund marked the session refunded")
	}
	if funding.refunded {
		t.Fatal("funding was refunded before the token refund succeeded")
	}
	assertServiceTokenQuota(t, db, token.Id, 75, 36)

	if err := session.Refund(ctx); err != nil {
		t.Fatalf("retry token refund: %v", err)
	}
	if !session.refunded || !funding.refunded {
		t.Fatal("successful retry did not complete the refund")
	}
	assertServiceTokenQuota(t, db, token.Id, 100, 11)

	if err := session.Refund(ctx); err != nil {
		t.Fatalf("idempotent token refund: %v", err)
	}
	assertServiceTokenQuota(t, db, token.Id, 100, 11)
}

func TestBillingSessionSettleExhaustsFiniteTokenWhenActualUsageExceedsRemainingQuota(t *testing.T) {
	const (
		preConsumed = 10
		actualQuota = 100
		remaining   = 50
		initialUsed = 17
	)
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Updates(map[string]any{
		"unlimited_quota": false,
		"remain_quota":    remaining,
		"used_quota":      initialUsed,
		"status":          common.TokenStatusEnabled,
	}).Error; err != nil {
		t.Fatalf("configure finite token: %v", err)
	}

	funding := &tokenQuotaTestFunding{}
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: false,
		},
		funding:          funding,
		preConsumedQuota: preConsumed,
		tokenConsumed:    preConsumed,
	}

	if err := session.Settle(actualQuota); err != nil {
		t.Fatalf("settle over-limit finite token: %v", err)
	}
	if funding.settled != actualQuota-preConsumed {
		t.Fatalf("funding settlement=%d, want %d", funding.settled, actualQuota-preConsumed)
	}

	var stored model.Token
	if err := db.First(&stored, token.Id).Error; err != nil {
		t.Fatalf("reload exhausted token: %v", err)
	}
	if stored.RemainQuota != 0 || stored.UsedQuota != initialUsed+remaining {
		t.Fatalf("exhausted token remain=%d used=%d, want remain=0 used=%d", stored.RemainQuota, stored.UsedQuota, initialUsed+remaining)
	}
	if stored.Status != common.TokenStatusExhausted {
		t.Fatalf("exhausted token status=%d, want %d", stored.Status, common.TokenStatusExhausted)
	}
}

func TestWalletSettlementRefundUsesDurableIdempotency(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(user).Update("quota", 75).Error; err != nil {
		t.Fatalf("configure pre-consumed wallet: %v", err)
	}
	requestID := "wallet-settle-refund-" + strings.ReplaceAll(t.Name(), "/", "-")
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			RequestId:      requestID,
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: true,
		},
		funding:          &WalletFunding{requestId: requestID, userId: user.Id, consumed: 25},
		preConsumedQuota: 25,
	}
	if err := session.Settle(10); err != nil {
		t.Fatalf("settle wallet refund: %v", err)
	}
	if err := session.Settle(10); err != nil {
		t.Fatalf("repeat settled wallet refund: %v", err)
	}

	var storedUser model.User
	if err := db.First(&storedUser, user.Id).Error; err != nil {
		t.Fatalf("reload wallet user: %v", err)
	}
	if storedUser.Quota != 90 {
		t.Fatalf("wallet quota=%d, want 90 after one idempotent refund", storedUser.Quota)
	}
	var refund model.BillingRefundPending
	if err := db.Where("request_id = ? AND kind = ?", requestID, model.BillingRefundKindWallet).
		First(&refund).Error; err != nil {
		t.Fatalf("load durable wallet settlement refund: %v", err)
	}
	if refund.Status != model.BillingRefundStatusCompleted || refund.Amount != 15 {
		t.Fatalf("durable wallet settlement refund status=%q amount=%d", refund.Status, refund.Amount)
	}
}

func TestWalletSettlementRecordsDebtWhenActualUsageExceedsAvailableBalance(t *testing.T) {
	const (
		preConsumed           = 10
		actualQuota           = 500
		walletAfterPreConsume = 90
	)
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(user).Updates(map[string]any{
		"quota":  walletAfterPreConsume,
		"status": common.UserStatusEnabled,
	}).Error; err != nil {
		t.Fatalf("configure underfunded wallet: %v", err)
	}

	operationID := "settlement-debt-" + strings.ReplaceAll(t.Name(), "/", "-")
	session := &BillingSession{
		operationID: operationID,
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			RequestId:      "client-visible-request-id",
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: true,
		},
		funding: &WalletFunding{
			operationID: operationID,
			requestId:   "client-visible-request-id",
			userId:      user.Id,
			consumed:    preConsumed,
		},
		preConsumedQuota: preConsumed,
	}

	if err := session.Settle(actualQuota); err != nil {
		t.Fatalf("settle underfunded wallet: %v", err)
	}

	var storedUser model.User
	if err := db.First(&storedUser, user.Id).Error; err != nil {
		t.Fatalf("reload underfunded wallet user: %v", err)
	}
	wantBalance := walletAfterPreConsume - (actualQuota - preConsumed)
	if storedUser.Quota != wantBalance {
		t.Fatalf("wallet quota=%d, want auditable debt balance %d", storedUser.Quota, wantBalance)
	}
	if storedUser.Status != common.UserStatusDisabled {
		t.Fatalf("underfunded wallet user status=%d, want disabled", storedUser.Status)
	}

	var ledgers []model.BalanceLedger
	if err := db.Where("user_id = ? AND source_type = ?", user.Id, model.BalanceLedgerSourceUsageSettlement).
		Find(&ledgers).Error; err != nil {
		t.Fatalf("load settlement debt ledger: %v", err)
	}
	if len(ledgers) != 1 {
		t.Fatalf("settlement debt ledger count=%d, want 1", len(ledgers))
	}
	entry := ledgers[0]
	wantDelta := int64(-(actualQuota - preConsumed))
	if entry.Delta != wantDelta || entry.BalanceBefore != walletAfterPreConsume || entry.BalanceAfter != int64(wantBalance) {
		t.Fatalf("settlement ledger delta=%d before=%d after=%d", entry.Delta, entry.BalanceBefore, entry.BalanceAfter)
	}
	if !session.settled || !session.fundingSettled {
		t.Fatal("audited settlement did not complete the billing session")
	}
}

func TestBillingRefundIdempotencyDoesNotDependOnClientRequestID(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(user).Update("quota", 50).Error; err != nil {
		t.Fatalf("configure twice pre-consumed wallet: %v", err)
	}

	const sharedClientRequestID = "client-reused-request-id"
	ctx, _ := gin.CreateTestContext(nil)
	for index := 1; index <= 2; index++ {
		operationID := fmt.Sprintf("server-operation-%d-%s", index, strings.ReplaceAll(t.Name(), "/", "-"))
		session := &BillingSession{
			operationID: operationID,
			relayInfo: &relaycommon.RelayInfo{
				UserId:         user.Id,
				RequestId:      sharedClientRequestID,
				TokenId:        token.Id,
				TokenKeyHash:   token.KeyHash,
				TokenUnlimited: true,
			},
			funding: &WalletFunding{
				operationID: operationID,
				requestId:   sharedClientRequestID,
				userId:      user.Id,
				consumed:    25,
			},
			preConsumedQuota: 25,
		}
		if err := session.Refund(ctx); err != nil {
			t.Fatalf("refund session %d: %v", index, err)
		}
	}

	var storedUser model.User
	if err := db.First(&storedUser, user.Id).Error; err != nil {
		t.Fatalf("reload twice-refunded wallet user: %v", err)
	}
	if storedUser.Quota != 100 {
		t.Fatalf("wallet quota=%d, want both independent refunds applied", storedUser.Quota)
	}
	var refundCount int64
	if err := db.Model(&model.BillingRefundPending{}).
		Where("request_id = ? AND kind = ?", sharedClientRequestID, model.BillingRefundKindWallet).
		Count(&refundCount).Error; err != nil {
		t.Fatalf("count independent wallet refunds: %v", err)
	}
	if refundCount != 2 {
		t.Fatalf("wallet refund rows=%d, want 2 despite a shared client request id", refundCount)
	}
}

func TestBillingSessionTokenRefundIsImmediateWithBatchUpdatesEnabled(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Updates(map[string]any{
		"unlimited_quota": false,
		"remain_quota":    75,
		"used_quota":      36,
	}).Error; err != nil {
		t.Fatalf("configure reserved finite token: %v", err)
	}
	common.BatchUpdateEnabled = true

	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId:         user.Id,
			RequestId:      "refund-immediate-" + strings.ReplaceAll(t.Name(), "/", "-"),
			TokenId:        token.Id,
			TokenKeyHash:   token.KeyHash,
			TokenUnlimited: false,
		},
		funding:       &tokenQuotaTestFunding{},
		tokenConsumed: 25,
	}
	ctx, _ := gin.CreateTestContext(nil)
	if err := session.Refund(ctx); err != nil {
		t.Fatalf("batch-enabled token refund: %v", err)
	}

	if !session.refunded {
		t.Fatal("successful synchronous token refund was not marked complete")
	}
	assertServiceTokenQuota(t, db, token.Id, 100, 11)
}
