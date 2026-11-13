package service

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type hostedBillingFixture struct {
	db    *gorm.DB
	user  model.User
	token model.Token
	input ZTAPIHostedToolBillingInput
}

func setupHostedBillingTest(t *testing.T, wallet, tokenQuota int) hostedBillingFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "hosted.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldRedis, oldBatch, oldQuota := common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = false, false, 500000
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.RedisEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = oldRedis, oldBatch, oldQuota
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.BalanceLedger{}, &model.Channel{}, &model.Ability{},
		&model.Log{}, &model.ZTAPIFXPolicy{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{},
		&model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPISettlementLogReceipt{}, &model.ZTAPIPendingResolution{}, &model.ZTAPIMediaTask{}))
	require.NoError(t, model.MigrateZTAPISupplierRefund(db))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	require.NoError(t, model.MigrateZTAPISettlementLogOutbox(db))
	user := model.User{Username: "hosted-offline", Status: common.UserStatusEnabled, Quota: wallet, Group: "default"}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, KeyHash: strings.Repeat("1", 64), KeyPrefix: "test-only",
		Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: tokenQuota}
	require.NoError(t, db.Create(&token).Error)
	baseURL := "https://api.deepseek.com"
	channel := model.Channel{Name: "explicit-official-tool-fixture", Status: common.ChannelStatusEnabled, BaseURL: &baseURL}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.ZTAPIFXPolicy{MarketCNYPerUSDT: "5", StopLossCNY: "0",
		PlatformCNYPerUSDT: "5", UpstreamCNYPerUSD: "0", MarketSource: "manual", Reason: "offline fixture"}).Error)
	return hostedBillingFixture{db: db, user: user, token: token, input: ZTAPIHostedToolBillingInput{
		UserID: user.Id, TokenID: token.Id, ParentRequestID: "hsroot_" + strings.Repeat("d", 32), Action: "search",
		OfficialChannelID: channel.Id, CredentialVersion: strings.Repeat("a", 64),
	}}
}

func (f hostedBillingFixture) reserve(t *testing.T) *model.ZTAPIRequestSettlement {
	t.Helper()
	row, err := ReserveZTAPIHostedToolBilling(f.input)
	require.NoError(t, err)
	require.NotNil(t, row)
	return row
}

func (f hostedBillingFixture) dispatch(t *testing.T, op string) *model.ZTAPIRequestAttempt {
	t.Helper()
	attempt, err := MarkZTAPIHostedToolBillingDispatched(op)
	require.NoError(t, err)
	return attempt
}

func (f hostedBillingFixture) balances(t *testing.T, wallet, remaining, used int) {
	t.Helper()
	var user model.User
	var token model.Token
	require.NoError(t, f.db.First(&user, f.user.Id).Error)
	require.NoError(t, f.db.First(&token, f.token.Id).Error)
	require.Equal(t, wallet, user.Quota)
	require.Equal(t, remaining, token.RemainQuota)
	require.Equal(t, used, user.UsedQuota)
	// Finite API-key UsedQuota includes its active reservation, unlike user UsedQuota.
	require.Equal(t, f.token.UsedQuota+f.token.RemainQuota-remaining, token.UsedQuota)
}

func hostedSuccess() ZTAPIHostedToolBillingResult {
	return ZTAPIHostedToolBillingResult{Outcome: "succeeded", HTTPStatus: 200, UpstreamRequestID: "official-tool-request-1",
		ResponseSHA256: strings.Repeat("b", 64), NonEmpty: true}
}

func TestHostedToolReadinessRejectsLegacyEmptyAbilityWithoutWalletMutation(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	require.NoError(t, CheckZTAPIHostedToolSource(f.input.OfficialChannelID))
	require.NoError(t, f.db.Create(&model.Ability{ChannelId: f.input.OfficialChannelID, Model: "", Group: "", Enabled: true}).Error)
	require.Error(t, CheckZTAPIHostedToolSource(f.input.OfficialChannelID))
	f.balances(t, 10000, 10000, 0)
	var rows int64
	require.NoError(t, f.db.Model(&model.ZTAPIRequestSettlement{}).Count(&rows).Error)
	require.Zero(t, rows)
}

func TestZTAPIHostedToolReserveIsAtomicAndServerOwned(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.balances(t, 9000, 9000, 0)
	require.Equal(t, int64(1000), row.ReservedQuota)
	require.Equal(t, int64(1000), row.TokenReservedQuota)
	require.Equal(t, "ztapi-tool-search", row.PublicModel)
	require.Equal(t, model.ZTAPISettlementReserved, row.Status)
	require.False(t, row.Dispatched)
	require.NotEqual(t, f.input.ParentRequestID, row.RequestID)
	require.NotEmpty(t, row.OperationID)
	require.LessOrEqual(t, len(row.RequestID), 64)
	var snapshot map[string]any
	require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &snapshot))
	for name, expected := range map[string]any{"schema_version": float64(1), "billing_kind": "hosted_tool", "provider": "deepseek_official",
		"tool": "search", "unit": "call", "currency": "CNY", "sale_per_call_cny": "0.01", "quoted_cost_per_call_cny": "0.005",
		"cny_per_u": "5", "quota_per_unit": float64(500000), "parent_request_id": f.input.ParentRequestID} {
		require.Equal(t, expected, snapshot[name], name)
	}
	for _, name := range []string{"OperationID", "RequestID", "Query", "URL", "Pattern", "Price", "Quota"} {
		_, exists := reflect.TypeOf(f.input).FieldByName(name)
		require.False(t, exists, "untrusted operation/content/price override: "+name)
	}
	other := f.reserve(t)
	require.NotEqual(t, row.OperationID, other.OperationID)
	require.NotEqual(t, row.RequestID, other.RequestID)
}

func TestZTAPIHostedToolReservationDenialsLeaveNoPartialDebit(t *testing.T) {
	for _, tt := range []struct{ wallet, token int }{{999, 10000}, {10000, 999}} {
		t.Run(fmt.Sprint(tt), func(t *testing.T) {
			f := setupHostedBillingTest(t, tt.wallet, tt.token)
			_, err := ReserveZTAPIHostedToolBilling(f.input)
			require.Error(t, err)
			f.balances(t, tt.wallet, tt.token, 0)
			for _, entity := range []any{&model.ZTAPIRequestSettlement{}, &model.BalanceLedger{}, &model.ZTAPIRequestAttempt{}} {
				var count int64
				require.NoError(t, f.db.Model(entity).Count(&count).Error)
				require.Zero(t, count)
			}
		})
	}
}

func TestZTAPIHostedToolRejectsInvalidAuthorityAndFreeFindBeforeReserve(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	for _, mutate := range []func(*ZTAPIHostedToolBillingInput){
		func(in *ZTAPIHostedToolBillingInput) { in.OfficialChannelID = 0 },
		func(in *ZTAPIHostedToolBillingInput) { in.OfficialChannelID = 9999 },
		func(in *ZTAPIHostedToolBillingInput) { in.Action = "find" },
		func(in *ZTAPIHostedToolBillingInput) { in.Action = "fetch" },
		func(in *ZTAPIHostedToolBillingInput) { in.CredentialVersion = "not-a-key-or-version-hash" },
		func(in *ZTAPIHostedToolBillingInput) { in.UserID++ },
		func(in *ZTAPIHostedToolBillingInput) { in.ParentRequestID = "secret?query=do-not-store" },
	} {
		input := f.input
		mutate(&input)
		_, err := ReserveZTAPIHostedToolBilling(input)
		require.Error(t, err)
	}
	f.balances(t, 10000, 10000, 0)
	var count int64
	require.NoError(t, f.db.Model(&model.ZTAPIRequestSettlement{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestZTAPIHostedToolUsesConfiguredFXAndFreezesQuota(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	require.NoError(t, f.db.Create(&model.ZTAPIFXPolicy{PlatformCNYPerUSDT: "6.63"}).Error)
	row := f.reserve(t)
	require.Equal(t, int64(755), row.ReservedQuota)
	f.dispatch(t, row.OperationID)
	require.NoError(t, f.db.Create(&model.ZTAPIFXPolicy{PlatformCNYPerUSDT: "8"}).Error)
	common.QuotaPerUnit = 1000000
	settled, err := FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.NoError(t, err)
	require.Equal(t, int64(754), settled.ChargedQuota)
	f.balances(t, 9246, 9246, 754)
}

func TestZTAPIHostedToolMissingFXCannotSilentlyUseDefault(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	require.NoError(t, f.db.Where("1 = 1").Delete(&model.ZTAPIFXPolicy{}).Error)
	_, err := ReserveZTAPIHostedToolBilling(f.input)
	require.Error(t, err)
	f.balances(t, 10000, 10000, 0)
}

func TestZTAPIHostedToolDispatchIsDurableAndNeverReauthorizesProviderIO(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	attempt := f.dispatch(t, row.OperationID)
	require.Equal(t, 1, attempt.Attempt)
	require.Equal(t, f.input.OfficialChannelID, attempt.ChannelID)
	require.Equal(t, "/v1/tools/search", attempt.Protocol)
	_, err := MarkZTAPIHostedToolBillingDispatched(row.OperationID)
	require.Error(t, err, "a replay must not authorize a second provider call")
	var stored model.ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&stored, row.ID).Error)
	require.True(t, stored.Dispatched)
	var count int64
	require.NoError(t, f.db.Model(&model.ZTAPIRequestAttempt{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestZTAPIHostedToolSuccessHasOneCallEvidenceAndIdempotentReplay(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.dispatch(t, row.OperationID)
	for i := 0; i < 2; i++ {
		settled, err := FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
		require.NoError(t, err)
		require.Equal(t, model.ZTAPISettlementSettled, settled.Status)
		require.EqualValues(t, 1000, settled.ChargedQuota)
		require.Equal(t, 1, settled.FinalAttempt)
		require.Contains(t, settled.UsageJSON, `"response_sha256":"`+strings.Repeat("b", 64)+`"`)
		require.NotContains(t, settled.UsageJSON, "input_tokens")
	}
	f.balances(t, 9000, 9000, 1000)
	var attempt model.ZTAPIRequestAttempt
	require.NoError(t, f.db.Where("settlement_id = ?", row.ID).Take(&attempt).Error)
	require.Equal(t, 200, attempt.HTTPStatus)
	require.Equal(t, "official-tool-request-1", attempt.UpstreamRequestID)
	var outbox model.ZTAPISettlementLogOutbox
	require.NoError(t, f.db.First(&outbox, "operation_id = ?", row.OperationID).Error)
	var log model.Log
	require.NoError(t, common.UnmarshalJsonStr(outbox.PayloadJSON, &log))
	require.Zero(t, log.PromptTokens)
	require.Zero(t, log.CompletionTokens)
	require.Equal(t, row.UserID, log.UserId)
	require.Equal(t, row.TokenID, log.TokenId)
	require.Equal(t, row.RequestID, log.RequestId)
	require.Equal(t, row.PublicModel, log.ModelName)
	require.Equal(t, 1000, log.Quota)
	require.Contains(t, log.Other, `"unit":"call"`)
	require.Contains(t, log.Other, `"quantity":1`)
	for _, entity := range []any{&model.BalanceLedger{}, &model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementFinalizationIntent{}} {
		var count int64
		require.NoError(t, f.db.Model(entity).Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
}

func TestZTAPIHostedToolEmptyAndConfirmedRejectionSettleZero(t *testing.T) {
	for _, outcome := range []string{"empty", "rejected"} {
		t.Run(outcome, func(t *testing.T) {
			f := setupHostedBillingTest(t, 10000, 10000)
			f.input.Action = "open_page"
			row := f.reserve(t)
			require.Equal(t, "ztapi-tool-fetch", row.PublicModel)
			f.dispatch(t, row.OperationID)
			result := hostedSuccess()
			result.Outcome, result.NonEmpty = outcome, false
			if outcome == "rejected" {
				result.HTTPStatus = 429
			}
			for i := 0; i < 2; i++ {
				settled, err := FinalizeZTAPIHostedToolBilling(row.OperationID, result)
				require.NoError(t, err)
				require.Equal(t, model.ZTAPISettlementSettled, settled.Status)
				require.Zero(t, settled.ChargedQuota)
			}
			f.balances(t, 10000, 10000, 0)
		})
	}
}

func TestZTAPIHostedToolUnknownNeverReleasesOrBecomesZero(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.dispatch(t, row.OperationID)
	unknown := ZTAPIHostedToolBillingResult{Outcome: "unknown", HTTPStatus: 403}
	for i := 0; i < 2; i++ {
		pending, err := FinalizeZTAPIHostedToolBilling(row.OperationID, unknown)
		require.NoError(t, err)
		require.Equal(t, model.ZTAPISettlementPending, pending.Status)
		require.Equal(t, `["hosted_tool_billing_unconfirmed"]`, pending.MissingDimensionsJSON)
		require.Zero(t, pending.FinalAttempt)
	}
	_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, ZTAPIHostedToolBillingResult{Outcome: "rejected"})
	require.Error(t, err)
	_, err = FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.Error(t, err)
	_, err = model.ReleaseZTAPIRequestSettlement(row.OperationID)
	require.Error(t, err)
	_, err = model.ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID)
	require.Error(t, err)
	_, err = model.ReleaseZTAPIRejectedDispatchedHolds(10)
	require.NoError(t, err)
	f.balances(t, 9000, 9000, 0)
}

func TestZTAPIHostedToolEvidenceConflictsDoNotRewriteFinancialHistory(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.dispatch(t, row.OperationID)
	_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.NoError(t, err)
	for _, mutate := range []func(*ZTAPIHostedToolBillingResult){
		func(r *ZTAPIHostedToolBillingResult) { r.ResponseSHA256 = strings.Repeat("c", 64) },
		func(r *ZTAPIHostedToolBillingResult) { r.UpstreamRequestID = "other-id" },
		func(r *ZTAPIHostedToolBillingResult) { r.HTTPStatus = 201 },
		func(r *ZTAPIHostedToolBillingResult) { r.Outcome, r.NonEmpty = "empty", false },
	} {
		result := hostedSuccess()
		mutate(&result)
		_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, result)
		require.Error(t, err)
	}
	var attempt model.ZTAPIRequestAttempt
	require.NoError(t, f.db.Where("settlement_id = ?", row.ID).Take(&attempt).Error)
	require.Equal(t, 200, attempt.HTTPStatus)
	require.Equal(t, "official-tool-request-1", attempt.UpstreamRequestID)
	f.balances(t, 9000, 9000, 1000)
}

func TestZTAPIHostedToolMalformedEvidenceCannotProveFreeOrSuccessfulUsage(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.Error(t, err, "successful usage requires an actual durable attempt")
	f.dispatch(t, row.OperationID)
	for _, mutate := range []func(*ZTAPIHostedToolBillingResult){
		func(r *ZTAPIHostedToolBillingResult) { r.NonEmpty = false },
		func(r *ZTAPIHostedToolBillingResult) { r.ResponseSHA256 = "raw response content" },
		func(r *ZTAPIHostedToolBillingResult) { r.UpstreamRequestID = "SECRET?query=raw" },
		func(r *ZTAPIHostedToolBillingResult) { r.Outcome, r.NonEmpty, r.HTTPStatus = "rejected", false, 503 },
		func(r *ZTAPIHostedToolBillingResult) { r.Outcome = "other" },
	} {
		result := hostedSuccess()
		mutate(&result)
		_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, result)
		require.Error(t, err)
	}
	f.balances(t, 9000, 9000, 0)
}

func TestZTAPIHostedToolFinalizationFailureRecoversFromDurableIntent(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.dispatch(t, row.OperationID)
	require.NoError(t, f.db.Exec(`CREATE TRIGGER reject_hosted_outbox BEFORE INSERT ON ztapi_settlement_log_outboxes BEGIN SELECT RAISE(ABORT, 'offline failure'); END`).Error)
	_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.Error(t, err)
	f.balances(t, 9000, 9000, 0)
	var intent model.ZTAPISettlementFinalizationIntent
	require.NoError(t, f.db.First(&intent, "operation_id = ?", row.OperationID).Error)
	require.Zero(t, intent.CompletedAt)
	require.NoError(t, f.db.Exec(`DROP TRIGGER reject_hosted_outbox`).Error)
	require.NoError(t, model.RetryZTAPISettlementFinalizations(10))
	_, err = FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.NoError(t, err)
	f.balances(t, 9000, 9000, 1000)
}

func TestZTAPIHostedToolConcurrentReplayChargesOnce(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.dispatch(t, row.OperationID)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	f.balances(t, 9000, 9000, 1000)
	var count int64
	require.NoError(t, f.db.Model(&model.ZTAPISettlementLogOutbox{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestZTAPIHostedToolUndispatchedLocalRejectionReleasesSafely(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	for i := 0; i < 2; i++ {
		released, err := FinalizeZTAPIHostedToolBilling(row.OperationID, ZTAPIHostedToolBillingResult{Outcome: "rejected"})
		require.NoError(t, err)
		require.Equal(t, model.ZTAPISettlementReleased, released.Status)
	}
	f.balances(t, 10000, 10000, 0)
	_, err := MarkZTAPIHostedToolBillingDispatched(row.OperationID)
	require.Error(t, err)
}

func TestZTAPIHostedToolRequiresEnabledOfficialChannelWithoutModelAbilities(t *testing.T) {
	for _, base := range []string{"https://relay.invalid", "http://api.deepseek.com", "https://user:SECRET@api.deepseek.com", "https://api.deepseek.com?key=SECRET", "https://api.deepseek.com/v1/models"} {
		t.Run(base, func(t *testing.T) {
			f := setupHostedBillingTest(t, 10000, 10000)
			require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.input.OfficialChannelID).Update("base_url", base).Error)
			_, err := ReserveZTAPIHostedToolBilling(f.input)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "SECRET")
			f.balances(t, 10000, 10000, 0)
		})
	}
	t.Run("disabled", func(t *testing.T) {
		f := setupHostedBillingTest(t, 10000, 10000)
		require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.input.OfficialChannelID).Update("status", common.ChannelStatusManuallyDisabled).Error)
		_, err := ReserveZTAPIHostedToolBilling(f.input)
		require.Error(t, err)
	})
	t.Run("model-ability", func(t *testing.T) {
		f := setupHostedBillingTest(t, 10000, 10000)
		require.NoError(t, f.db.Create(&model.Ability{ChannelId: f.input.OfficialChannelID, Model: "zt-other", Group: "default", Enabled: true}).Error)
		_, err := ReserveZTAPIHostedToolBilling(f.input)
		require.Error(t, err)
	})
}

func TestZTAPIHostedToolPausedChannelBlocksNewDispatchButNotExistingSettlement(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	first, second := f.reserve(t), f.reserve(t)
	f.dispatch(t, first.OperationID)
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", f.input.OfficialChannelID).Update("status", common.ChannelStatusManuallyDisabled).Error)
	_, err := MarkZTAPIHostedToolBillingDispatched(second.OperationID)
	require.Error(t, err)
	_, err = FinalizeZTAPIHostedToolBilling(first.OperationID, hostedSuccess())
	require.NoError(t, err)
}

func TestZTAPIHostedToolUnknownSurvivesGenericRecoveryAndTextPricing(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.dispatch(t, row.OperationID)
	pending, err := FinalizeZTAPIHostedToolBilling(row.OperationID, ZTAPIHostedToolBillingResult{Outcome: "unknown", HTTPStatus: 403})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, model.ReconcileZTAPIAttemptBillingReviews(10))
		require.NoError(t, model.RetryZTAPISettlementFinalizations(10))
		require.NoError(t, model.RetryZTAPIAttemptBillings(10, EnqueueZTAPIAttemptBillingLogTx))
		_, err = model.ReleaseZTAPIRejectedDispatchedHolds(10)
		require.NoError(t, err)
	}
	for _, semantic := range []string{"openai", "anthropic"} {
		_, err = PriceZTAPIAttemptBilling(*pending, model.ZTAPIAttemptBillingSubmission{RequestID: row.RequestID,
			UserID: row.UserID, ChannelID: f.input.OfficialChannelID, Attempt: 1, Kind: "billed", UsageSemantic: semantic,
			Usage: []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 100}}})
		require.Error(t, err, "a call quotation must never become text token charges")
	}
	f.balances(t, 9000, 9000, 0)
	var current model.ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&current, row.ID).Error)
	require.Equal(t, model.ZTAPISettlementPending, current.Status)
	require.Equal(t, `["hosted_tool_billing_unconfirmed"]`, current.MissingDimensionsJSON)
}

func TestZTAPIHostedToolReceiptSurvivesFailureBeforeFinalizationIntent(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.dispatch(t, row.OperationID)
	require.NoError(t, f.db.Exec(`CREATE TRIGGER reject_hosted_response BEFORE UPDATE ON ztapi_request_attempts BEGIN SELECT RAISE(ABORT, 'offline failure'); END`).Error)
	_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.Error(t, err)
	_, err = FinalizeZTAPIHostedToolBilling(row.OperationID, ZTAPIHostedToolBillingResult{Outcome: "unknown"})
	require.Error(t, err, "a first durable receipt cannot be replaced after process failure")
	f.balances(t, 9000, 9000, 0)
	require.NoError(t, f.db.Exec(`DROP TRIGGER reject_hosted_response`).Error)
	_, err = FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.NoError(t, err)
	f.balances(t, 9000, 9000, 1000)
}

func TestZTAPIHostedToolServerRootNeverReusesAnExternalSettlementIdentity(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	external, err := model.BeginZTAPIRequestSettlement(model.ZTAPIRequestSettlement{
		OperationID: "external-client-operation", RequestID: f.input.ParentRequestID, UserID: f.user.Id, TokenID: f.token.Id,
		PublicModel: "zt-claude-opus-5", PriceSnapshotJSON: "{}", ReservedQuota: 7,
	})
	require.NoError(t, err)
	var before model.ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&before, external.ID).Error)
	first, second := f.reserve(t), f.reserve(t)
	for _, child := range []*model.ZTAPIRequestSettlement{first, second} {
		require.NotEqual(t, external.ID, child.ID)
		require.NotEqual(t, external.RequestID, child.RequestID)
		require.NotEqual(t, external.OperationID, child.OperationID)
		require.True(t, strings.HasPrefix(child.RequestID, "hstool_"))
	}
	require.NotEqual(t, first.RequestID, second.RequestID)
	var unchanged model.ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&unchanged, external.ID).Error)
	require.Equal(t, before, unchanged)
	f.balances(t, 7993, 7993, 0)
}

func TestZTAPIHostedToolHalfQuotaUsesCanonicalPositiveChargeRounding(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	common.QuotaPerUnit = 400400
	require.NoError(t, f.db.Create(&model.ZTAPIFXPolicy{PlatformCNYPerUSDT: "8"}).Error)
	row := f.reserve(t)
	require.EqualValues(t, 501, row.ReservedQuota, "exact cost is 500.5 quota")
	f.dispatch(t, row.OperationID)
	settled, err := FinalizeZTAPIHostedToolBilling(row.OperationID, hostedSuccess())
	require.NoError(t, err)
	require.EqualValues(t, 501, settled.ChargedQuota)
	f.balances(t, 9499, 9499, 501)
}

func TestZTAPIHostedToolNoModelChannelAbilityLifecycle(t *testing.T) {
	for _, operation := range []string{"add", "update", "fix"} {
		t.Run(operation, func(t *testing.T) {
			f := setupHostedBillingTest(t, 10000, 10000)
			require.NoError(t, f.db.AutoMigrate(&model.ZTAPIModelConfig{}))
			var channel model.Channel
			require.NoError(t, f.db.First(&channel, f.input.OfficialChannelID).Error)
			require.Empty(t, channel.GetModels())
			if operation != "add" {
				require.NoError(t, f.db.Create(&model.Ability{ChannelId: channel.Id, Group: "default", Model: "stale-model", Enabled: true}).Error)
			}
			switch operation {
			case "add":
				require.NoError(t, channel.AddAbilities(f.db))
			case "update":
				require.NoError(t, channel.UpdateAbilities(nil))
			case "fix":
				success, failures, err := model.FixAbility()
				require.NoError(t, err)
				require.Equal(t, 1, success)
				require.Zero(t, failures)
			}
			var abilities []model.Ability
			require.NoError(t, f.db.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
			require.Empty(t, abilities, "no-model source must never gain a blank routing ability")
			_, err := ReserveZTAPIHostedToolBilling(f.input)
			require.NoError(t, err, "ability repair must not invalidate the official source")
		})
	}
}

func TestZTAPIHostedToolAbilityFixPreservesNonemptyModelRoutes(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	require.NoError(t, f.db.AutoMigrate(&model.ZTAPIModelConfig{}))
	channel := model.Channel{Models: "ordinary-a,ordinary-b,ordinary-a", Group: "default,vip", Status: common.ChannelStatusEnabled}
	require.NoError(t, f.db.Create(&channel).Error)
	for _, operation := range []string{"add", "update", "fix"} {
		switch operation {
		case "add":
			require.NoError(t, channel.AddAbilities(f.db))
		case "update":
			require.NoError(t, channel.UpdateAbilities(nil))
		case "fix":
			_, failures, err := model.FixAbility()
			require.NoError(t, err)
			require.Zero(t, failures)
		}
		var abilities []model.Ability
		require.NoError(t, f.db.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
		routes := make([]string, 0, len(abilities))
		for _, ability := range abilities {
			require.True(t, ability.Enabled)
			routes = append(routes, ability.Group+":"+ability.Model)
		}
		require.ElementsMatch(t, []string{"default:ordinary-a", "default:ordinary-b", "vip:ordinary-a", "vip:ordinary-b"}, routes)
	}
}

func TestZTAPIHostedToolRejected4xxMatchesProviderContract(t *testing.T) {
	for status := 400; status <= 499; status++ {
		result := hostedSuccess()
		result.Outcome, result.NonEmpty, result.HTTPStatus = "rejected", false, status
		require.Equal(t, status != 408, validHostedToolResult(result), "HTTP %d", status)
		for _, hash := range []string{"", "truncated", strings.Repeat("b", 63)} {
			result.ResponseSHA256 = hash
			require.False(t, validHostedToolResult(result), "HTTP %d requires a complete SHA witness", status)
		}
		result.ResponseSHA256, result.NonEmpty = strings.Repeat("b", 64), true
		require.False(t, validHostedToolResult(result), "HTTP %d cannot prove free rejection with nonempty results", status)
	}
	for _, status := range []int{399, 500, 502, 503, 599} {
		result := hostedSuccess()
		result.Outcome, result.NonEmpty, result.HTTPStatus = "rejected", false, status
		require.False(t, validHostedToolResult(result))
	}
}

func TestZTAPIHostedToolRejected4xxPersistsZeroFeeReceipt(t *testing.T) {
	for _, status := range []int{405, 409, 415} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := setupHostedBillingTest(t, 10000, 10000)
			row := f.reserve(t)
			f.dispatch(t, row.OperationID)
			witness := sha256.Sum256([]byte("COMPLETE_REJECTION_BODY_NOT_FOR_FINANCE"))
			result := ZTAPIHostedToolBillingResult{Outcome: "rejected", HTTPStatus: status,
				UpstreamRequestID: fmt.Sprintf("official-rejection-%d", status), ResponseSHA256: fmt.Sprintf("%x", witness)}
			for i := 0; i < 2; i++ {
				settled, err := FinalizeZTAPIHostedToolBilling(row.OperationID, result)
				require.NoError(t, err)
				require.Equal(t, model.ZTAPISettlementSettled, settled.Status)
				require.Zero(t, settled.ChargedQuota)
				require.Contains(t, settled.UsageJSON, result.ResponseSHA256)
				require.NotContains(t, settled.UsageJSON, "COMPLETE_REJECTION_BODY_NOT_FOR_FINANCE")
			}
			f.balances(t, 10000, 10000, 0)
			var attempt model.ZTAPIRequestAttempt
			require.NoError(t, f.db.Where("settlement_id = ?", row.ID).Take(&attempt).Error)
			require.Equal(t, status, attempt.HTTPStatus)
			require.Equal(t, result.UpstreamRequestID, attempt.UpstreamRequestID)
			var receipts int64
			require.NoError(t, f.db.Model(&model.ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID).Count(&receipts).Error)
			require.EqualValues(t, 1, receipts)
		})
	}
}

func TestZTAPIHostedToolRejectedWitnessCannotBeMissingOrNonempty(t *testing.T) {
	f := setupHostedBillingTest(t, 10000, 10000)
	row := f.reserve(t)
	f.dispatch(t, row.OperationID)
	for _, result := range []ZTAPIHostedToolBillingResult{
		{Outcome: "rejected", HTTPStatus: 405},
		{Outcome: "rejected", HTTPStatus: 409, ResponseSHA256: "incomplete-hash"},
		{Outcome: "rejected", HTTPStatus: 415, ResponseSHA256: strings.Repeat("b", 64), NonEmpty: true},
	} {
		_, err := FinalizeZTAPIHostedToolBilling(row.OperationID, result)
		require.Error(t, err)
	}
	f.balances(t, 9000, 9000, 0)
	var rowAfter model.ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&rowAfter, row.ID).Error)
	require.Equal(t, "{}", rowAfter.UsageJSON)
}

func TestZTAPIHostedToolIndeterminateHTTPAndTruncationRetainHold(t *testing.T) {
	for _, status := range []int{408, 500, 503, 405} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := setupHostedBillingTest(t, 10000, 10000)
			row := f.reserve(t)
			f.dispatch(t, row.OperationID)
			// Even a 4xx with a hash of partial bytes is unknown when the provider's
			// bounded reader did not confirm the complete response witness.
			partial := sha256.Sum256([]byte("incomplete-provider-response"))
			result := ZTAPIHostedToolBillingResult{Outcome: "unknown", HTTPStatus: status, ResponseSHA256: fmt.Sprintf("%x", partial)}
			pending, err := FinalizeZTAPIHostedToolBilling(row.OperationID, result)
			require.NoError(t, err)
			require.Equal(t, model.ZTAPISettlementPending, pending.Status)
			require.Equal(t, hostedToolUnknownReason, pending.MissingDimensionsJSON)
			require.NoError(t, model.RetryZTAPISettlementFinalizations(10))
			_, err = model.ReleaseZTAPIRejectedDispatchedHolds(10)
			require.NoError(t, err)
			f.balances(t, 9000, 9000, 0)
			var receipts int64
			require.NoError(t, f.db.Model(&model.ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID).Count(&receipts).Error)
			require.Zero(t, receipts)
		})
	}
}
