package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestObserveZTAPIBillingResponseDefersBodyFieldRequestID(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}))
	row, err := model.BeginZTAPIRequestSettlement(model.ZTAPIRequestSettlement{
		OperationID: "body-request-id-operation", RequestID: "body-request-id-request",
		UserID: user.Id, TokenID: token.Id, PublicModel: "zt-gemini-2.5-flash-image",
		TokenUnlimited: token.UnlimitedQuota, PriceSnapshotJSON: `{}`, ReservedQuota: 10,
	})
	require.NoError(t, err)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 23, "credential-version", "/v1beta/models/gemini-2.5-flash-image:generateContent")
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			ImageProtocolContract: &types.ZTAPIImageProtocolContract{RequestIDSource: types.ZTAPIResponseIDSourceBodyField},
		},
	}
	info.Billing = &ztapiDurableBilling{row: row, info: info, attempt: attempt}
	require.NoError(t, ObserveZTAPIBillingResponse(info, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"gateway-request-id"}},
	}))

	var stored model.ZTAPIRequestAttempt
	require.NoError(t, db.First(&stored, attempt.ID).Error)
	require.Equal(t, http.StatusOK, stored.HTTPStatus)
	require.Empty(t, stored.UpstreamRequestID, "the verified body response ID owns financial lineage")
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, attempt.ChannelID, http.StatusOK, "provider-body-response-id"))
	require.NoError(t, db.First(&stored, attempt.ID).Error)
	require.Equal(t, "provider-body-response-id", stored.UpstreamRequestID)
}

func TestZTAPIDurableBillingDispatchErrorKeepsPendingHold(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Update("unlimited_quota", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}); err != nil {
		t.Fatal(err)
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, RequestId: "durable-dispatch", OriginModelName: "zt-model", ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{PublicationID: 1, Version: 1, PublicName: "zt-model", InputPricePerMillion: 1, OutputPricePerMillion: 2}}
	if err := PreConsumeBilling(ctx, 20, info); err != nil {
		t.Fatal(err)
	}
	if err := MarkZTAPIBillingDispatched(info); err != nil {
		t.Fatal(err)
	}
	if err := info.Billing.Refund(ctx); err != nil {
		t.Fatal(err)
	}
	var row model.ZTAPIRequestSettlement
	if err := db.Where("request_id = ?", info.RequestId).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != model.ZTAPISettlementPending || row.ReservedQuota != 20 {
		t.Fatalf("unknown provider charge was refunded: %+v", row)
	}
	var got model.User
	db.First(&got, user.Id)
	if got.Quota != 80 {
		t.Fatalf("wallet=%d want retained hold 80", got.Quota)
	}
	if err := info.Billing.Settle(0); err == nil {
		t.Fatal("pending converted to zero settlement")
	}
}

func TestZTAPIDurableBillingDatabaseFailureIsNotCustomerQuotaError(t *testing.T) {
	_, user, token := setupServiceTokenQuotaTest(t)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenUnlimited: true, RequestId: "storage-down", OriginModelName: "zt-model", ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{PublicName: "zt-model"}}
	err := PreConsumeBilling(ctx, 20, info)
	if err == nil || err.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("storage failure misreported: %+v", err)
	}
	if err.ToOpenAIError().Message != "Billing is temporarily unavailable. Please retry later." {
		t.Fatalf("raw database details leaked: %+v", err.ToOpenAIError())
	}
}

func TestZTAPIDurableBillingFullUsageAndSupplierRefundJourney(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Update("unlimited_quota", false).Error; err != nil {
		t.Fatal(err)
	}
	oldLogDB := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = oldLogDB })
	if err := db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{}); err != nil {
		t.Fatal(err)
	}
	if err := model.MigrateZTAPISupplierRefund(db); err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{Name: "offline-billing", Status: common.ChannelStatusEnabled, Type: 1}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, RequestId: "full-durable-journey", OriginModelName: "zt-model", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ApiKey: "synthetic-offline-credential"}, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{PublicationID: 1, Version: 1, PublicName: "zt-model", PriceSourceID: 1, PriceSourceVersion: 1, BillingDimensions: []string{"input_tokens", "output_tokens"}, SaleUSD: map[string]string{"input_tokens": "2", "output_tokens": "4"}}}
	if err := PreConsumeBilling(ctx, 30, info); err != nil {
		t.Fatal(err)
	}
	if err := BeginZTAPIBillingAttempt(info, "/v1/chat/completions"); err != nil {
		t.Fatal(err)
	}
	if err := ObserveZTAPIBillingResponse(info, &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"upstream-offline-id"}}}); err != nil {
		t.Fatal(err)
	}
	usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	PostTextConsumeQuota(ctx, info, usage, nil)
	PostTextConsumeQuota(ctx, info, usage, nil)
	if err := FinishZTAPIBilling(ctx, info); err != nil {
		t.Fatal(err)
	}
	var row model.ZTAPIRequestSettlement
	if err := db.Where("request_id = ?", info.RequestId).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != model.ZTAPISettlementSettled || row.ChargedQuota != 20 || row.TokenChargedQuota != 20 {
		t.Fatalf("not settled: %+v", row)
	}
	var got model.User
	db.First(&got, user.Id)
	if got.Quota != 80 || got.UsedQuota != 20 || got.RequestCount != 1 {
		t.Fatalf("wrong final account %+v", got)
	}
	var logs int64
	db.Model(&model.Log{}).Where("request_id = ?", info.RequestId).Count(&logs)
	if logs != 1 {
		t.Fatalf("consume logs %d", logs)
	}
	var charge model.ZTAPISupplierRefundCharge
	if err := db.Where("request_id = ?", info.RequestId).Take(&charge).Error; err != nil {
		t.Fatal(err)
	}
	operator := model.User{Username: "finance-journey", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "finance-journey"}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatal(err)
	}
	proof, err := model.SubmitZTAPISupplierRefund(model.ZTAPISupplierRefundSubmission{Source: "offline-provider", ProofID: "refund-full", RequestID: row.RequestID, UserID: user.Id, Attempt: charge.Attempt, ChannelID: charge.ChannelID, CredentialVersion: charge.CredentialVersion, UpstreamRequestID: charge.UpstreamRequestID, Mode: "full", EvidenceReference: "offline-confirmed-reversal"})
	if err != nil {
		t.Fatal(err)
	}
	if err = model.ApproveZTAPISupplierRefund(proof.ID, operator.Id, "offline-verified-statement"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = model.ProcessZTAPISupplierRefund(proof.ID); err != nil {
			t.Fatal(err)
		}
	}
	db.First(&got, user.Id)
	if got.Quota != 100 || got.UsedQuota != 20 || got.RequestCount != 1 {
		t.Fatalf("refund/counters %+v", got)
	}
	assertServiceTokenQuota(t, db, token.Id, 100, 11)
}

func TestZTAPIDurablePostUsageDoesNotPriceUnquotedCache(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	oldLogDB := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() { model.LOG_DB = oldLogDB })
	if err := db.AutoMigrate(&model.Channel{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}); err != nil {
		t.Fatal(err)
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenUnlimited: true, RequestId: "missing-cache", OriginModelName: "zt-model", ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{PublicationID: 1, Version: 1, PublicName: "zt-model", PriceSourceID: 1, PriceSourceVersion: 1, BillingDimensions: []string{"input_tokens", "output_tokens"}, SaleUSD: map[string]string{"input_tokens": "1", "output_tokens": "2"}}}
	if err := PreConsumeBilling(ctx, 20, info); err != nil {
		t.Fatal(err)
	}
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 1}
	PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: 100, CompletionTokens: 1, TotalTokens: 101, PromptTokensDetails: dto.InputTokenDetails{CachedCreationTokens: 50}}, nil)
	var row model.ZTAPIRequestSettlement
	if err := db.Where("request_id = ?", info.RequestId).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != model.ZTAPISettlementPending {
		t.Fatalf("unquoted usage not suspended: %+v", row)
	}
	var got model.User
	db.First(&got, user.Id)
	if got.Quota != 80 || got.UsedQuota != 0 || got.RequestCount != 0 {
		t.Fatalf("unquoted usage billed: %+v", got)
	}
}

func TestZTAPIDurableBillingUndispatchedRefundAtomic(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	if err := db.Model(token).Update("unlimited_quota", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}); err != nil {
		t.Fatal(err)
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, RequestId: "durable-undispatched", OriginModelName: "zt-model", ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{PublicationID: 1, Version: 1, PublicName: "zt-model"}}
	if err := PreConsumeBilling(ctx, 20, info); err != nil {
		t.Fatal(err)
	}
	if err := info.Billing.Refund(ctx); err != nil {
		t.Fatal(err)
	}
	if err := info.Billing.Refund(ctx); err != nil {
		t.Fatal(err)
	}
	var got model.User
	db.First(&got, user.Id)
	if got.Quota != 100 {
		t.Fatalf("wallet=%d want100", got.Quota)
	}
	var rows int64
	db.Model(&model.BalanceLedger{}).Where("request_id = ?", info.RequestId).Count(&rows)
	if rows != 2 {
		t.Fatalf("ledger rows=%d want2", rows)
	}
}
