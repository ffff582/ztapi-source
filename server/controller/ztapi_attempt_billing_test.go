package controller_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIAttemptBillingHTTPRoutesRequireFinance(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	support, key := createBalanceLedgerOperator(t, db, "attempt-support", common.RoleSupportUser)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/attempt-billing/reviews", ""},
		{http.MethodGet, "/api/admin/attempt-billing/proofs", ""},
		{http.MethodPost, "/api/admin/attempt-billing/proofs", `{}`},
		{http.MethodPost, "/api/admin/attempt-billing/proofs/1/approve", `{"verification_reference":"statement:1"}`},
	} {
		response := performBalanceLedgerRequest(t, engine, request.method, request.path, support, key, request.body)
		assertBalanceLedgerDenied(t, response)
	}
}

func TestZTAPIAttemptBillingHTTPVerifiedUsageIsPricedAndAppliedOnce(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.Channel{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{}, &model.ZTAPIPendingResolution{}))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	finance, auth := createBalanceLedgerOperator(t, db, "attempt-e2e-finance", common.RoleFinanceUser)
	target := createBalanceLedgerTarget(t, db, "attempt-e2e-customer", 1000)
	token := model.Token{UserId: target.Id, KeyHash: "attempt-e2e-key-hash", RemainQuota: 1000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	pool, enterprise := model.Channel{Name: "fixture-pool"}, model.Channel{Name: "fixture-enterprise"}
	require.NoError(t, db.Create(&pool).Error)
	require.NoError(t, db.Create(&enterprise).Error)
	price := relaycommon.ZTAPIPublicationSnapshot{PublicName: "zt-proof-model", SourceModel: "proof-model", Modality: "text", PublicationID: 1, Version: 1, PriceSourceID: 1, PriceSourceVersion: 1, BillingDimensions: []string{"input_tokens"}, SaleUSD: map[string]string{"input_tokens": "2"}}
	priceJSON, err := common.Marshal(price)
	require.NoError(t, err)
	row, err := model.BeginZTAPIRequestSettlement(model.ZTAPIRequestSettlement{OperationID: "attempt-http-e2e", RequestID: "attempt-http-canonical", UserID: target.Id, TokenID: token.Id, PublicModel: price.PublicName, PriceSnapshotJSON: string(priceJSON), ReservedQuota: 200})
	require.NoError(t, err)
	_, err = model.BeginZTAPIRequestAttempt(row.OperationID, pool.Id, "pool-version", "chat")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, 1, pool.Id, 502, "pool-wire"))
	_, err = model.BeginZTAPIRequestAttempt(row.OperationID, enterprise.Id, "enterprise-version", "chat")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, 2, enterprise.Id, 200, "enterprise-wire"))
	_, err = model.FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, `{"prompt_tokens":100}`, `[{"dimension":"input_tokens","units":"100","unit_quota":"1","charged_quota":100}]`, model.ZTAPISettlementEvidence{FinalAttempt: 2, ConsumeLog: model.Log{Type: model.LogTypeConsume, UserId: target.Id, TokenId: token.Id, RequestId: row.RequestID, ChannelId: enterprise.Id, ModelName: row.PublicModel, Quota: 100, PromptTokens: 100, CreatedAt: time.Now().Unix()}})
	require.NoError(t, err)
	proof := model.ZTAPIAttemptBillingSubmission{Source: "supplier-fixture", ProofID: "supplier-line-proof", RequestID: row.RequestID, UserID: row.UserID, Attempt: 1, ChannelID: pool.Id, CredentialVersion: "pool-version", UpstreamRequestID: "pool-wire", UpstreamBillID: "pool-bill-line", Kind: "billed", UsageSemantic: "openai", Usage: []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 40}}, EvidenceReference: "statement-row-1", DistinctUsageReference: "separate-pool-and-enterprise-lines"}
	raw, err := common.Marshal(proof)
	require.NoError(t, err)
	var submitted struct {
		Data struct {
			ID     uint   `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	for i := 0; i < 2; i++ {
		res := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/attempt-billing/proofs", finance, auth, string(raw))
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		require.NoError(t, common.Unmarshal(res.Body.Bytes(), &submitted))
		require.Equal(t, "pending", submitted.Data.Status)
	}
	var customer model.User
	require.NoError(t, db.First(&customer, target.Id).Error)
	require.Equal(t, 900, customer.Quota, "submission alone must not charge")
	for i := 0; i < 2; i++ {
		res := performBalanceLedgerRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/attempt-billing/proofs/%d/approve", submitted.Data.ID), finance, auth, `{"verification_reference":"reviewed-bill-line"}`)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		require.Contains(t, res.Body.String(), `"status":"completed"`)
	}
	require.NoError(t, db.First(&customer, target.Id).Error)
	require.Equal(t, 860, customer.Quota)
	require.Equal(t, 140, customer.UsedQuota)
	require.Equal(t, 1, customer.RequestCount)
	var storedKey model.Token
	require.NoError(t, db.First(&storedKey, token.Id).Error)
	require.Equal(t, 860, storedKey.RemainQuota)
	_, err = model.ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	var logs []model.Log
	require.NoError(t, db.Where("request_id = ? AND type = ?", row.RequestID, model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 2, "two distinct billed attempts, one logical request")
}

func TestZTAPIAttemptBillingHTTPAcceptsImageUsageEvidenceFields(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	finance, auth := createBalanceLedgerOperator(t, db, "attempt-image-finance", common.RoleFinanceUser)
	body := `{"source":"supplier-image","proof_id":"supplier-image-line-1","request_id":"image-request-1","user_id":1,"attempt":1,"channel_id":2,"credential_version":"enterprise-v1","upstream_request_id":"image-wire-1","upstream_task_id":"image-task-1","upstream_bill_id":"image-bill-1","kind":"billed","usage_semantic":"ztapi_image","usage":[{"dimension":"input_tokens","quantity":10},{"dimension":"output_tokens","quantity":2}],"selected_rule_id":"lte_200k","price_rule_ids":{"input_tokens":"lte_200k","output_tokens":"lte_200k"},"raw_usage_json":"{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}","evidence_reference":"supplier-statement-row-1","distinct_usage_reference":"separate-attempt-line"}`

	response := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/attempt-billing/proofs", finance, auth, body)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var stored model.ZTAPIAttemptBillingProof
	require.NoError(t, db.Where("source = ? AND proof_id = ?", "supplier-image", "supplier-image-line-1").Take(&stored).Error)
	var submission model.ZTAPIAttemptBillingSubmission
	require.NoError(t, common.UnmarshalJsonStr(stored.SubmissionJSON, &submission))
	require.Equal(t, "image-task-1", submission.UpstreamTaskID)
	require.Equal(t, "lte_200k", submission.SelectedRuleID)
	require.Equal(t, map[string]string{"input_tokens": "lte_200k", "output_tokens": "lte_200k"}, submission.PriceRuleIDs)
	require.JSONEq(t, `{"input_tokens":10,"output_tokens":2,"total_tokens":12}`, submission.RawUsageJSON)
}

func TestZTAPIAttemptBillingHTTPRejectsCallerPricesAndApprovalIdentity(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	finance, key := createBalanceLedgerOperator(t, db, "attempt-finance-input", common.RoleFinanceUser)
	for _, body := range []string{
		`{"source":"supplier","proof_id":"1","amount":10}`,
		`{"source":"supplier","proof_id":"1","charged_quota":100}`,
		`{"source":"supplier","proof_id":"1","trusted":true}`,
		`{"source":"supplier","proof_id":"1","price_snapshot":{}}`,
		`null`, `{} {}`, `{"source":`,
	} {
		response := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/attempt-billing/proofs", finance, key, body)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	response := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/admin/attempt-billing/proofs/1/approve", finance, key, `{"verification_reference":"statement:1","operator_id":999}`)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
}
