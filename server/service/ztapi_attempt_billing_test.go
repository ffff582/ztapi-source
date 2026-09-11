package service

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func attemptBillingPriceFixture(t *testing.T) (model.ZTAPIRequestSettlement, model.ZTAPIAttemptBillingSubmission) {
	t.Helper()
	snapshot := relaycommon.ZTAPIPublicationSnapshot{
		PublicationID: 17, Version: 3, PublicName: "zt-proof-model", SourceModel: "proof-model", Modality: "text",
		PriceSourceID: 9, PriceSourceVersion: 2,
		BillingDimensions:    []string{"input_tokens", "output_tokens", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h"},
		SaleUSD:              map[string]string{"input_tokens": "2", "output_tokens": "4", "cache_read": "0.2", "cache_write": "3", "cache_write_5m": "4", "cache_write_1h": "6"},
		InputPricePerMillion: 999, OutputPricePerMillion: 999, CacheReadRatio: 999,
	}
	raw, err := common.Marshal(snapshot)
	require.NoError(t, err)
	parent := model.ZTAPIRequestSettlement{ID: 1, OperationID: "original-operation", RequestID: "canonical-request", UserID: 11, TokenID: 12, PublicModel: snapshot.PublicName, PriceSnapshotJSON: string(raw), Status: model.ZTAPISettlementSettled, FinalAttempt: 2, CreatedAt: time.Unix(1788790000, 0)}
	submission := model.ZTAPIAttemptBillingSubmission{Source: "offline-supplier", ProofID: "billed-proof", RequestID: parent.RequestID, UserID: parent.UserID, Attempt: 1, ChannelID: 13, CredentialVersion: "credential-version", UpstreamRequestID: "wire-first", UpstreamBillID: "bill-line", Kind: "billed", UsageSemantic: "openai", EvidenceReference: "reviewed-statement", DistinctUsageReference: "separate-attempt-usage", Usage: []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 100}, {Dimension: "output_tokens", Quantity: 20}}}
	return parent, submission
}

func changeAttemptBillingSnapshot(t *testing.T, parent *model.ZTAPIRequestSettlement, change func(*relaycommon.ZTAPIPublicationSnapshot)) {
	t.Helper()
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	require.NoError(t, common.UnmarshalJsonStr(parent.PriceSnapshotJSON, &snapshot))
	change(&snapshot)
	raw, err := common.Marshal(snapshot)
	require.NoError(t, err)
	parent.PriceSnapshotJSON = string(raw)
}

func imageAttemptBillingPriceFixture(t *testing.T, contract types.ZTAPIMediaPriceContract, usage []model.ZTAPIAttemptBillingQuantity) (model.ZTAPIRequestSettlement, model.ZTAPIAttemptBillingSubmission) {
	t.Helper()
	rawContract, err := common.Marshal(contract)
	require.NoError(t, err)
	contractJSON, err := types.CanonicalizeZTAPIMediaPriceContract(string(rawContract))
	require.NoError(t, err)
	publication := relaycommon.ZTAPIPublicationSnapshot{
		PublicationID: 31, Version: 7, PublicName: "zt-image-proof", SourceModel: "provider-image-proof", Modality: "image",
		PriceSourceID: 41, PriceSourceVersion: 11, MediaPriceContractJSON: contractJSON,
	}
	fields := make(map[string]string)
	maximum := make(map[string]string)
	cacheSemantics := "not_reported"
	for _, rule := range contract.Rules {
		for dimension := range rule.SaleUSD {
			fields[dimension] = dimension
			maximum[dimension] = "300000"
			if strings.Contains(dimension, "cached") {
				cacheSemantics = "separate_dimension"
			}
		}
	}
	protocol, protocolJSON, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersion, ProviderModel: publication.SourceModel,
		EndpointType: types.ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		Capabilities:   types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{"b64_json"}, MinCount: 1, MaxCount: 1},
		Response:       types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"b64_json": "b64_json"}},
		Usage:          types.ZTAPIImageUsageContract{UsageField: "usage", Fields: fields, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: cacheSemantics},
		Reservations:   []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "standard", ResponseFormat: "b64_json", N: 1, MaximumDimensions: maximum}},
		RequestIDField: "request_id", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	publication.ImageProtocolContract = &protocol
	frozen, err := common.Marshal(ztapiFrozenMediaReservation{
		ZTAPIPublicationSnapshot:  publication,
		SelectorJSON:              `{"modality":"image","n":1,"quality":"standard","response_format":"b64_json","size":"1024x1024"}`,
		ImageProtocolContractJSON: protocolJSON,
		ProtocolEvidenceHash:      protocol.EvidenceHash,
		QuotaPerUnit:              "500000",
		MaximumDimensions:         maximum,
		MaximumQuota:              math.MaxInt32,
	})
	require.NoError(t, err)
	parent := model.ZTAPIRequestSettlement{
		ID: 1, OperationID: "image-original-operation", RequestID: "image-canonical-request", UserID: 11, TokenID: 12,
		PublicModel: publication.PublicName, PriceSnapshotJSON: string(frozen), Status: model.ZTAPISettlementPending,
		CreatedAt: time.Unix(1788790000, 0),
	}
	submission := model.ZTAPIAttemptBillingSubmission{
		Source: "offline-supplier", ProofID: "image-billed-proof", RequestID: parent.RequestID, UserID: parent.UserID,
		Attempt: 1, ChannelID: 13, CredentialVersion: "credential-version", UpstreamRequestID: "wire-first",
		UpstreamBillID: "bill-line", Kind: "billed", UsageSemantic: "ztapi_image", Usage: usage,
		EvidenceReference: "reviewed-image-statement", DistinctUsageReference: "separate-image-attempt-usage",
	}
	rawUsage := make(map[string]int64, len(usage))
	submission.PriceRuleIDs = make(map[string]string, len(usage))
	for _, quantity := range usage {
		rawUsage[quantity.Dimension] = quantity.Quantity
		rawUsage["total_tokens"] += quantity.Quantity
	}
	if _, tiered := contract.Rules[0].Conditions["prompt_tokens_tier"]; tiered {
		tier := "lte_200k"
		if rawUsage["input_tokens"] > 200000 {
			tier = "gt_200k"
		}
		submission.SelectedRuleID = tier
		for _, quantity := range usage {
			submission.PriceRuleIDs[quantity.Dimension] = tier
		}
	} else {
		for _, quantity := range usage {
			submission.PriceRuleIDs[quantity.Dimension] = quantity.Dimension
		}
	}
	rawUsageJSON, err := common.Marshal(rawUsage)
	require.NoError(t, err)
	submission.RawUsageJSON = string(rawUsageJSON)
	return parent, submission
}

func gptImageThreeDimensionAttemptFixture(t *testing.T) (model.ZTAPIRequestSettlement, model.ZTAPIAttemptBillingSubmission) {
	t.Helper()
	_, priceJSON, protocol, protocolJSON := gptImageThreeDimensionContracts(t)
	maximum := map[string]string{"text_input": "20", "image_input": "20", "image_output": "200"}
	publication := relaycommon.ZTAPIPublicationSnapshot{
		PublicationID: 71, Version: 3, PublicName: "zt-gp-image-2", SourceModel: "gpt-image-2", Modality: "image",
		PriceSourceID: 72, PriceSourceVersion: 5, MediaPriceContractJSON: priceJSON, ImageProtocolContract: &protocol,
	}
	frozen, err := common.Marshal(ztapiFrozenMediaReservation{
		ZTAPIPublicationSnapshot: publication, SelectorJSON: `{"modality":"image","n":1,"quality":"low","response_format":"b64_json","size":"1024x1024"}`,
		ImageProtocolContractJSON: protocolJSON, ProtocolEvidenceHash: protocol.EvidenceHash, QuotaPerUnit: "500000", MaximumDimensions: maximum, MaximumQuota: 430,
	})
	require.NoError(t, err)
	parent := model.ZTAPIRequestSettlement{ID: 1, OperationID: "gpt-image-attempt", RequestID: "gpt-image-attempt-request", UserID: 11, TokenID: 12, PublicModel: publication.PublicName, PriceSnapshotJSON: string(frozen), Status: model.ZTAPISettlementPending, CreatedAt: time.Unix(1788790000, 0)}
	submission := model.ZTAPIAttemptBillingSubmission{
		Source: "offline-supplier", ProofID: "gpt-image-bill", RequestID: parent.RequestID, UserID: parent.UserID, Attempt: 1, ChannelID: 13,
		CredentialVersion: "credential-version", UpstreamRequestID: "gpt-image-wire", UpstreamBillID: "gpt-image-line", Kind: "billed", UsageSemantic: model.ZTAPIAttemptBillingUsageSemanticImage,
		Usage:             []model.ZTAPIAttemptBillingQuantity{{Dimension: "text_input", Quantity: 18}, {Dimension: "image_input", Quantity: 0}, {Dimension: "image_output", Quantity: 196}},
		PriceRuleIDs:      map[string]string{"text_input": "text_input", "image_input": "image_input", "image_output": "image_output"},
		RawUsageJSON:      `{"input_tokens":18,"input_tokens_details":{"image_tokens":0,"text_tokens":18},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":214}`,
		EvidenceReference: "reviewed-image-statement", DistinctUsageReference: "separate-image-attempt-usage",
	}
	return parent, submission
}

func TestZTAPIAttemptBillingPricesGPTImageStrictThreeDimensionsAndRejectsSyntheticCaches(t *testing.T) {
	parent, submission := gptImageThreeDimensionAttemptFixture(t)
	priced, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, 401, priced.ConsumeLog.Quota)
	require.Len(t, priced.Dimensions, 2)

	synthetic := submission
	synthetic.Usage = append(append([]model.ZTAPIAttemptBillingQuantity(nil), submission.Usage...),
		model.ZTAPIAttemptBillingQuantity{Dimension: "text_cached_input", Quantity: 0},
		model.ZTAPIAttemptBillingQuantity{Dimension: "image_cached_input", Quantity: 0},
	)
	synthetic.PriceRuleIDs = map[string]string{"text_input": "text_input", "image_input": "image_input", "image_output": "image_output", "text_cached_input": "text_cached_input", "image_cached_input": "image_cached_input"}
	synthetic.RawUsageJSON = `{"image_cached_input":0,"image_input":0,"image_output":196,"text_cached_input":0,"text_input":18,"total_tokens":214}`
	_, err = PriceZTAPIAttemptBilling(parent, synthetic)
	require.ErrorIs(t, err, model.ErrZTAPIAttemptBillingInvalid)
}

func TestZTAPIAttemptBillingPricesGPTImageFiveBucketsFromFrozenMediaContract(t *testing.T) {
	prices := map[string]string{"text_input": "1", "text_cached_input": "0.2", "image_input": "2", "image_cached_input": "0.4", "image_output": "4"}
	costs := map[string]string{"text_input": "0.6", "text_cached_input": "0.12", "image_input": "1.2", "image_cached_input": "0.24", "image_output": "2.4"}
	rules := make([]types.ZTAPIMediaPriceRule, 0, len(prices))
	for index, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		rules = append(rules, types.ZTAPIMediaPriceRule{
			ID: dimension, Conditions: map[string]string{"token_bucket": dimension}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{dimension: costs[dimension]}, SaleUSD: map[string]string{dimension: prices[dimension]},
			SourceCells: map[string]string{dimension: fmt.Sprintf("A%d", index+1)},
		})
	}
	parent, submission := imageAttemptBillingPriceFixture(t, types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules}, []model.ZTAPIAttemptBillingQuantity{
		{Dimension: "text_input", Quantity: 2}, {Dimension: "text_cached_input", Quantity: 10},
		{Dimension: "image_input", Quantity: 3}, {Dimension: "image_cached_input", Quantity: 5}, {Dimension: "image_output", Quantity: 2},
	})

	priced, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, 10, priced.ConsumeLog.Quota)
	require.Len(t, priced.Dimensions, 5)
}

func TestZTAPIAttemptBillingFreezesGeminiImageTierBoundary(t *testing.T) {
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: []types.ZTAPIMediaPriceRule{
		{ID: "lte_200k", Conditions: map[string]string{"prompt_tokens_tier": "lte_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.6", "output_tokens": "1.2"}, SaleUSD: map[string]string{"input_tokens": "1", "output_tokens": "2"}, SourceCells: map[string]string{"input_tokens": "A1", "output_tokens": "B1"}},
		{ID: "gt_200k", Conditions: map[string]string{"prompt_tokens_tier": "gt_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "1.8", "output_tokens": "2.4"}, SaleUSD: map[string]string{"input_tokens": "3", "output_tokens": "4"}, SourceCells: map[string]string{"input_tokens": "A2", "output_tokens": "B2"}},
	}}
	for _, tc := range []struct {
		name  string
		input int64
		want  int
	}{{"low_at_200000", 200000, 100001}, {"high_at_200001", 200001, 300004}} {
		t.Run(tc.name, func(t *testing.T) {
			parent, submission := imageAttemptBillingPriceFixture(t, contract, []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: tc.input}, {Dimension: "output_tokens", Quantity: 1}})
			priced, err := PriceZTAPIAttemptBilling(parent, submission)
			require.NoError(t, err)
			require.Equal(t, tc.want, priced.ConsumeLog.Quota)
		})
	}
}

func TestZTAPIGeminiImageTierBoundaryCompletesLateBillAndFullRefund(t *testing.T) {
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: []types.ZTAPIMediaPriceRule{
		{ID: "lte_200k", Conditions: map[string]string{"prompt_tokens_tier": "lte_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.6", "output_tokens": "1.2"}, SaleUSD: map[string]string{"input_tokens": "1", "output_tokens": "2"}, SourceCells: map[string]string{"input_tokens": "A1", "output_tokens": "B1"}},
		{ID: "gt_200k", Conditions: map[string]string{"prompt_tokens_tier": "gt_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "1.8", "output_tokens": "2.4"}, SaleUSD: map[string]string{"input_tokens": "3", "output_tokens": "4"}, SourceCells: map[string]string{"input_tokens": "A2", "output_tokens": "B2"}},
	}}
	for _, tc := range []struct {
		name, tier string
		input      int64
		want       int
	}{{"low_at_200000", "lte_200k", 200000, 100001}, {"high_at_200001", "gt_200k", 200001, 300004}} {
		t.Run(tc.name, func(t *testing.T) {
			oldDB, oldLogDB := model.DB, model.LOG_DB
			t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogDB })
			db, user, token := setupServiceTokenQuotaTest(t)
			model.LOG_DB = db
			require.NoError(t, db.Model(user).Update("quota", 1000000).Error)
			require.NoError(t, db.Model(token).Updates(map[string]any{"unlimited_quota": false, "remain_quota": 1000000}).Error)
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{}))
			require.NoError(t, model.MigrateZTAPISupplierRefund(db))
			require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
			require.NoError(t, db.Create(&model.Channel{Id: 23, Name: "gemini-image-proof-channel", Status: common.ChannelStatusEnabled, Type: 1}).Error)

			input, submission := imageAttemptBillingPriceFixture(t, contract, []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: tc.input}, {Dimension: "output_tokens", Quantity: 1}})
			input.ID, input.UserID, input.TokenID, input.ReservedQuota = 0, user.Id, token.Id, 400000
			input.OperationID, input.RequestID = "gemini-tier-op-"+tc.name, "gemini-tier-request-"+tc.name
			parent, err := model.BeginZTAPIRequestSettlement(input)
			require.NoError(t, err)
			attempt, err := model.BeginZTAPIRequestAttempt(parent.OperationID, 23, submission.CredentialVersion, "/v1beta/models/generateContent")
			require.NoError(t, err)
			require.NoError(t, model.RecordZTAPIRequestAttemptResponse(parent.OperationID, attempt.Attempt, attempt.ChannelID, 200, submission.UpstreamRequestID))
			parent, err = model.PendZTAPIRequestSettlement(parent.OperationID, "{}", `["upstream_usage_missing"]`)
			require.NoError(t, err)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return model.EnsureZTAPIAttemptBillingReviewsTx(tx, parent) }))
			operator := model.User{Username: "gemini-tier-finance-" + tc.name, Status: common.UserStatusEnabled, Role: common.RoleFinanceUser, AffCode: "gemini-tier-finance-" + tc.name}
			require.NoError(t, db.Create(&operator).Error)

			submission.RequestID, submission.UserID, submission.Attempt = parent.RequestID, parent.UserID, attempt.Attempt
			submission.ChannelID = attempt.ChannelID
			proof, err := model.SubmitZTAPIAttemptBilling(submission)
			require.NoError(t, err)
			require.NoError(t, model.ApproveZTAPIAttemptBilling(proof.ID, operator.Id, "verified Gemini tier bill", PriceZTAPIAttemptBilling))
			require.NoError(t, model.ProcessZTAPIAttemptBilling(proof.ID, EnqueueZTAPIAttemptBillingLogTx))

			var settled model.ZTAPIRequestSettlement
			require.NoError(t, db.First(&settled, parent.ID).Error)
			require.Equal(t, model.ZTAPISettlementSettled, settled.Status)
			require.EqualValues(t, tc.want, settled.ChargedQuota)
			var charge model.ZTAPISupplierRefundCharge
			require.NoError(t, db.Where("request_id = ?", parent.RequestID).Take(&charge).Error)
			var storedProof model.ZTAPIAttemptBillingProof
			require.NoError(t, db.First(&storedProof, proof.ID).Error)
			var stored model.ZTAPIAttemptBillingSubmission
			require.NoError(t, common.UnmarshalJsonStr(storedProof.SubmissionJSON, &stored))
			require.Equal(t, tc.tier, stored.SelectedRuleID)
			require.Equal(t, tc.tier, stored.PriceRuleIDs["input_tokens"])
			require.Equal(t, tc.tier, stored.PriceRuleIDs["output_tokens"])

			refund, err := model.SubmitZTAPISupplierRefund(model.ZTAPISupplierRefundSubmission{Source: "offline-supplier", ProofID: "gemini-tier-refund-" + tc.name, RequestID: parent.RequestID, UserID: parent.UserID, Attempt: charge.Attempt, ChannelID: charge.ChannelID, CredentialVersion: charge.CredentialVersion, UpstreamRequestID: charge.UpstreamRequestID, UpstreamBillID: charge.UpstreamBillID, Mode: "full", EvidenceReference: "verified full Gemini reversal"})
			require.NoError(t, err)
			require.NoError(t, model.ApproveZTAPISupplierRefund(refund.ID, operator.Id, "reviewed Gemini reversal"))
			require.NoError(t, model.ProcessZTAPISupplierRefund(refund.ID))
			require.NoError(t, model.ProcessZTAPISupplierRefund(refund.ID))
			var account model.User
			require.NoError(t, db.First(&account, user.Id).Error)
			require.Equal(t, 1000000, account.Quota)
			assertServiceTokenQuota(t, db, token.Id, 1000000, 11)
		})
	}
}

func TestZTAPIAttemptBillingRejectsImageRuleAndRawUsageDrift(t *testing.T) {
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: []types.ZTAPIMediaPriceRule{
		{ID: "lte_200k", Conditions: map[string]string{"prompt_tokens_tier": "lte_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.6", "output_tokens": "1.2"}, SaleUSD: map[string]string{"input_tokens": "1", "output_tokens": "2"}, SourceCells: map[string]string{"input_tokens": "A1", "output_tokens": "B1"}},
		{ID: "gt_200k", Conditions: map[string]string{"prompt_tokens_tier": "gt_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "1.8", "output_tokens": "2.4"}, SaleUSD: map[string]string{"input_tokens": "3", "output_tokens": "4"}, SourceCells: map[string]string{"input_tokens": "A2", "output_tokens": "B2"}},
	}}
	for _, tc := range []struct {
		name   string
		mutate func(*model.ZTAPIAttemptBillingSubmission)
	}{
		{"wrong_selected_tier", func(s *model.ZTAPIAttemptBillingSubmission) { s.SelectedRuleID = "gt_200k" }},
		{"wrong_dimension_rule", func(s *model.ZTAPIAttemptBillingSubmission) { s.PriceRuleIDs["output_tokens"] = "gt_200k" }},
		{"missing_raw_usage", func(s *model.ZTAPIAttemptBillingSubmission) { s.RawUsageJSON = "" }},
		{"raw_usage_mismatch", func(s *model.ZTAPIAttemptBillingSubmission) {
			s.RawUsageJSON = `{"input_tokens":199999,"output_tokens":1}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, submission := imageAttemptBillingPriceFixture(t, contract, []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 200000}, {Dimension: "output_tokens", Quantity: 1}})
			tc.mutate(&submission)
			_, err := PriceZTAPIAttemptBilling(parent, submission)
			require.Error(t, err)
		})
	}
}

func TestZTAPIAttemptBillingRequiresExplicitZeroGPTImageBuckets(t *testing.T) {
	prices := map[string]string{"text_input": "1", "text_cached_input": "0.2", "image_input": "2", "image_cached_input": "0.4", "image_output": "4"}
	costs := map[string]string{"text_input": "0.6", "text_cached_input": "0.12", "image_input": "1.2", "image_cached_input": "0.24", "image_output": "2.4"}
	rules := make([]types.ZTAPIMediaPriceRule, 0, 5)
	for index, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		rules = append(rules, types.ZTAPIMediaPriceRule{ID: dimension, Conditions: map[string]string{"token_bucket": dimension}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{dimension: costs[dimension]}, SaleUSD: map[string]string{dimension: prices[dimension]}, SourceCells: map[string]string{dimension: fmt.Sprintf("A%d", index+1)}})
	}
	parent, submission := imageAttemptBillingPriceFixture(t, types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules}, []model.ZTAPIAttemptBillingQuantity{
		{Dimension: "text_input", Quantity: 2}, {Dimension: "text_cached_input", Quantity: 0}, {Dimension: "image_input", Quantity: 3}, {Dimension: "image_cached_input", Quantity: 0}, {Dimension: "image_output", Quantity: 2},
	})
	priced, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, 8, priced.ConsumeLog.Quota)
	require.Len(t, priced.Dimensions, 3, "zero buckets are explicit evidence but not refundable charge dimensions")

	omitted := submission
	omitted.Usage = append([]model.ZTAPIAttemptBillingQuantity(nil), submission.Usage[:4]...)
	delete(omitted.PriceRuleIDs, "image_output")
	_, err = PriceZTAPIAttemptBilling(parent, omitted)
	require.Error(t, err)
}

func TestZTAPIAttemptBillingPricesFrozenExclusiveBuckets(t *testing.T) {
	parent, submission := attemptBillingPriceFixture(t)
	submission.Usage = append(submission.Usage,
		model.ZTAPIAttemptBillingQuantity{Dimension: "cache_read", Quantity: 40},
		model.ZTAPIAttemptBillingQuantity{Dimension: "cache_write", Quantity: 10},
		model.ZTAPIAttemptBillingQuantity{Dimension: "cache_write_5m", Quantity: 5},
		model.ZTAPIAttemptBillingQuantity{Dimension: "cache_write_1h", Quantity: 2})
	before, err := common.Marshal(submission)
	require.NoError(t, err)
	priced, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, 175, priced.ConsumeLog.Quota)
	require.Equal(t, 157, priced.ConsumeLog.PromptTokens)
	require.Equal(t, 20, priced.ConsumeLog.CompletionTokens)
	require.Equal(t, parent.RequestID, priced.ConsumeLog.RequestId)
	require.Equal(t, parent.UserID, priced.ConsumeLog.UserId)
	require.Equal(t, parent.TokenID, priced.ConsumeLog.TokenId)
	require.Equal(t, parent.PublicModel, priced.ConsumeLog.ModelName)
	require.Equal(t, submission.ChannelID, priced.ConsumeLog.ChannelId)
	require.Equal(t, parent.CreatedAt.Unix(), priced.ConsumeLog.CreatedAt)
	require.Equal(t, model.LogTypeConsume, priced.ConsumeLog.Type)
	require.Len(t, priced.Dimensions, 6)
	var total int64
	for _, dimension := range priced.Dimensions {
		total += dimension.ChargedQuota
		require.Zero(t, dimension.TokenChargedQuota, "model assigns actual finite-token debits")
	}
	require.EqualValues(t, priced.ConsumeLog.Quota, total)
	submission.UsageSemantic = "anthropic"
	anthropic, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, priced.Dimensions, anthropic.Dimensions, "normalized quantities must not subtract cache twice")
	submission.UsageSemantic = "openai"
	after, err := common.Marshal(submission)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "pure pricer must not mutate submitted proof")
	again, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, priced, again, "approval replay must have deterministic log metadata")
}

func TestZTAPIAttemptBillingPreservesFrozenMarkupAndRounding(t *testing.T) {
	parent, submission := attemptBillingPriceFixture(t)
	changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) {
		s.SaleUSD["input_tokens"], s.SaleUSD["output_tokens"] = "1.6666666667", "3.3333333333"
	})
	submission.Usage = []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 10}, {Dimension: "output_tokens", Quantity: 5}}
	priced, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, 17, priced.ConsumeLog.Quota, "frozen sale prices already include markup")
	require.EqualValues(t, 17, priced.Dimensions[0].ChargedQuota+priced.Dimensions[1].ChargedQuota)
	changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) {
		s.Modality = "embedding"
		s.BillingDimensions = []string{"input_tokens"}
		s.SaleUSD = map[string]string{"input_tokens": "0.026"}
	})
	submission.Usage = []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 1}}
	priced, err = PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, 1, priced.ConsumeLog.Quota)
	require.Equal(t, "0.013", priced.Dimensions[0].UnitQuota)
	require.EqualValues(t, 1, priced.Dimensions[0].ChargedQuota)
	require.Zero(t, priced.ConsumeLog.CompletionTokens)
}

func TestZTAPIAttemptBillingRejectsUnpriceableProof(t *testing.T) {
	for _, name := range []string{"missing_snapshot", "wrong_model", "missing_source", "missing_version", "missing_quote", "negative_quote", "malformed_quote", "unlisted_quote", "duplicate_quote_bucket", "missing_usage", "zero_usage", "negative_usage", "duplicate_usage", "overlapping_prompt", "overlapping_reasoning", "unknown_dimension", "quantity_overflow", "price_overflow", "wrong_customer", "wrong_request", "unknown_semantic", "nocharge", "invalid_attempt", "missing_channel"} {
		t.Run(name, func(t *testing.T) {
			parent, submission := attemptBillingPriceFixture(t)
			switch name {
			case "missing_snapshot":
				parent.PriceSnapshotJSON = "{}"
			case "wrong_model":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { s.PublicName = "another-model" })
			case "missing_source":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { s.PriceSourceID = 0 })
			case "missing_version":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { s.PriceSourceVersion = 0 })
			case "missing_quote":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { delete(s.SaleUSD, "input_tokens") })
			case "negative_quote":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { s.SaleUSD["input_tokens"] = "-1" })
			case "malformed_quote":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { s.SaleUSD["input_tokens"] = "NaN" })
			case "unlisted_quote":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { s.BillingDimensions = []string{"output_tokens"} })
			case "duplicate_quote_bucket":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) {
					s.BillingDimensions = append(s.BillingDimensions, "input_tokens")
				})
			case "missing_usage":
				submission.Usage = nil
			case "zero_usage":
				submission.Usage[0].Quantity = 0
				submission.Usage[1].Quantity = 0
			case "negative_usage":
				submission.Usage[0].Quantity = -1
			case "duplicate_usage":
				submission.Usage = append(submission.Usage, submission.Usage[0])
			case "overlapping_prompt":
				submission.Usage = append(submission.Usage, model.ZTAPIAttemptBillingQuantity{Dimension: "prompt_tokens", Quantity: 100})
			case "overlapping_reasoning":
				submission.Usage = append(submission.Usage, model.ZTAPIAttemptBillingQuantity{Dimension: "reasoning_tokens", Quantity: 10})
			case "unknown_dimension":
				submission.Usage[0].Dimension = "unknown"
			case "quantity_overflow":
				submission.Usage[0].Quantity = math.MaxInt64
			case "price_overflow":
				changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { s.SaleUSD["input_tokens"] = "999999999999" })
			case "wrong_customer":
				submission.UserID++
			case "wrong_request":
				submission.RequestID = "another-request"
			case "unknown_semantic":
				submission.UsageSemantic = "inclusive-guess"
			case "nocharge":
				submission.Kind = "nocharge"
			case "invalid_attempt":
				submission.Attempt = 3
			case "missing_channel":
				submission.ChannelID = 0
			}
			_, err := PriceZTAPIAttemptBilling(parent, submission)
			require.Error(t, err)
		})
	}
}

func TestZTAPIAttemptBillingExplicitFreeIsNotMissing(t *testing.T) {
	parent, submission := attemptBillingPriceFixture(t)
	changeAttemptBillingSnapshot(t, &parent, func(s *relaycommon.ZTAPIPublicationSnapshot) { s.SaleUSD["input_tokens"] = "0" })
	priced, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Equal(t, 40, priced.ConsumeLog.Quota)
	require.Equal(t, "0", priced.Dimensions[0].UnitQuota)
	require.Zero(t, priced.Dimensions[0].ChargedQuota)
}

func TestZTAPIAttemptBillingPendingParentAndZeroBuckets(t *testing.T) {
	parent, submission := attemptBillingPriceFixture(t)
	parent.Status, parent.FinalAttempt = model.ZTAPISettlementPending, 0
	submission.Usage = append(submission.Usage, model.ZTAPIAttemptBillingQuantity{Dimension: "cache_read", Quantity: 0})
	priced, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	require.Len(t, priced.Dimensions, 2)
	require.Equal(t, 140, priced.ConsumeLog.Quota)
}

func attemptBillingLogFixture(t *testing.T) (*gorm.DB, model.ZTAPIRequestSettlement, model.ZTAPISupplierRefundCharge, model.Log) {
	t.Helper()
	oldDB, oldLogDB := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogDB })
	db, user, token := setupServiceTokenQuotaTest(t)
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISupplierRefundCharge{}, &model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{}))
	parent, submission := attemptBillingPriceFixture(t)
	parent.UserID, parent.TokenID, submission.UserID = user.Id, token.Id, user.Id
	priced, err := PriceZTAPIAttemptBilling(parent, submission)
	require.NoError(t, err)
	parent.ChargedQuota, parent.TokenChargedQuota = int64(priced.ConsumeLog.Quota), int64(priced.ConsumeLog.Quota)
	parent.Dispatched, parent.UsageJSON, parent.ChargeDimensionsJSON = true, `{"original":"unchanged"}`, `[]`
	require.NoError(t, db.Create(&parent).Error)
	for _, id := range []int{13, 14} {
		require.NoError(t, db.Create(&model.Channel{Id: id, Name: "offline-log-channel", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	}
	require.NoError(t, db.Create(&model.ZTAPIRequestAttempt{SettlementID: parent.ID, Attempt: 1, ChannelID: 13, CredentialVersion: submission.CredentialVersion, UpstreamRequestID: submission.UpstreamRequestID, Protocol: "chat"}).Error)
	require.NoError(t, db.Create(&model.ZTAPIRequestAttempt{SettlementID: parent.ID, Attempt: 2, ChannelID: 14, CredentialVersion: "final-credential", UpstreamRequestID: "wire-final", Protocol: "chat"}).Error)
	for i := range priced.Dimensions {
		priced.Dimensions[i].TokenChargedQuota = priced.Dimensions[i].ChargedQuota
	}
	dimensions, err := common.Marshal(priced.Dimensions)
	require.NoError(t, err)
	charge := model.ZTAPISupplierRefundCharge{SettlementID: parent.ID, RequestID: parent.RequestID, UserID: parent.UserID, TokenID: parent.TokenID, Attempt: 1, ChannelID: submission.ChannelID, CredentialVersion: submission.CredentialVersion, UpstreamRequestID: submission.UpstreamRequestID, UpstreamBillID: submission.UpstreamBillID, PriceSnapshotJSON: parent.PriceSnapshotJSON, DimensionsJSON: string(dimensions), OriginalLedgerID: 1, ChargedQuota: int64(priced.ConsumeLog.Quota), TokenChargedQuota: int64(priced.ConsumeLog.Quota), BillingProofID: 7, BillingOperationID: "attempt-bill:" + strings.Repeat("a", 48)}
	require.NoError(t, db.Create(&charge).Error)
	return db, parent, charge, priced.ConsumeLog
}

func TestZTAPIAttemptBillingLogUsesPersistedChargeWithoutSavingShadow(t *testing.T) {
	db, parent, charge, log := attemptBillingLogFixture(t)
	var before model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&before, parent.ID).Error)
	originalLog := log
	originalLog.ChannelId = 14
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return model.EnqueueZTAPISettlementLogTx(tx, &parent, originalLog) }))
	for i := 0; i < 2; i++ {
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return EnqueueZTAPIAttemptBillingLogTx(tx, &parent, &charge, charge.BillingOperationID, log)
		}))
	}
	var after model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&after, parent.ID).Error)
	require.Equal(t, before, after, "shadow must never overwrite original settlement or replay identity")
	require.Equal(t, 2, parent.FinalAttempt, "caller-owned parent must remain unchanged")
	require.Equal(t, "original-operation", parent.OperationID)
	var jobs []model.ZTAPISettlementLogOutbox
	require.NoError(t, db.Order("operation_id").Find(&jobs).Error)
	require.Len(t, jobs, 2)
	var childLog model.Log
	require.NoError(t, common.UnmarshalJsonStr(jobs[0].PayloadJSON, &childLog))
	require.Equal(t, charge.BillingOperationID, jobs[0].OperationID)
	require.Equal(t, parent.RequestID, childLog.RequestId)
	require.Equal(t, charge.ChannelID, childLog.ChannelId)
	require.Equal(t, charge.UpstreamRequestID, childLog.UpstreamRequestId)
	require.EqualValues(t, charge.ChargedQuota, childLog.Quota)
	for _, channelID := range []int{13, 14} {
		var channel model.Channel
		require.NoError(t, db.First(&channel, channelID).Error)
		require.EqualValues(t, charge.ChargedQuota, channel.UsedQuota, "one charge per channel, no duplicate enqueue debit")
	}
	processed, err := model.ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	require.Equal(t, 2, processed)
	processed, err = model.ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	require.Zero(t, processed)
	var logs int64
	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ?", parent.RequestID).Count(&logs).Error)
	require.EqualValues(t, 2, logs, "original and earlier-attempt charges each have one log")
	var user model.User
	require.NoError(t, db.First(&user, parent.UserID).Error)
	require.Equal(t, 100, user.Quota, "log wrapper must not touch customer funds")
	require.Zero(t, user.RequestCount)
}

func TestZTAPIAttemptBillingLogRejectsForgedLineage(t *testing.T) {
	for _, name := range []string{"nil_parent", "nil_charge", "unpersisted_parent", "unpersisted_charge", "parent_user", "parent_snapshot", "parent_status", "charge_owner", "charge_token", "charge_request", "charge_settlement", "charge_snapshot", "charge_quota", "charge_dimensions", "charge_upstream", "charge_channel", "final_attempt", "missing_proof", "operation_mismatch", "operation_too_long", "parent_operation", "log_user", "log_request", "log_model", "log_channel", "log_quota"} {
		t.Run(name, func(t *testing.T) {
			db, parent, charge, log := attemptBillingLogFixture(t)
			p, ch, operation := &parent, &charge, charge.BillingOperationID
			switch name {
			case "nil_parent":
				p = nil
			case "nil_charge":
				ch = nil
			case "unpersisted_parent":
				parent.ID++
			case "unpersisted_charge":
				charge.ID++
			case "parent_user":
				parent.UserID++
			case "parent_snapshot":
				parent.PriceSnapshotJSON = `{}`
			case "parent_status":
				require.NoError(t, db.Model(&parent).Update("status", model.ZTAPISettlementPending).Error)
			case "charge_owner":
				charge.UserID++
			case "charge_token":
				charge.TokenID++
			case "charge_request":
				charge.RequestID = "another-request"
			case "charge_settlement":
				charge.SettlementID++
			case "charge_snapshot":
				charge.PriceSnapshotJSON = `{}`
			case "charge_quota":
				charge.ChargedQuota++
			case "charge_dimensions":
				charge.DimensionsJSON = `[]`
			case "charge_upstream":
				charge.UpstreamRequestID = "other-wire"
			case "charge_channel":
				charge.ChannelID++
			case "final_attempt":
				charge.Attempt = 2
				require.NoError(t, db.Model(&charge).Update("attempt", 2).Error)
			case "missing_proof":
				charge.BillingProofID = 0
				require.NoError(t, db.Model(&charge).Update("billing_proof_id", 0).Error)
			case "operation_mismatch":
				operation = "different-operation"
			case "operation_too_long":
				operation = strings.Repeat("a", 65)
			case "parent_operation":
				operation = parent.OperationID
				charge.BillingOperationID = operation
				require.NoError(t, db.Model(&charge).Update("billing_operation_id", operation).Error)
			case "log_user":
				log.UserId++
			case "log_request":
				log.RequestId = "another-request"
			case "log_model":
				log.ModelName = "another-model"
			case "log_channel":
				log.ChannelId++
			case "log_quota":
				log.Quota++
			}
			require.Error(t, db.Transaction(func(tx *gorm.DB) error { return EnqueueZTAPIAttemptBillingLogTx(tx, p, ch, operation, log) }))
			var jobs int64
			require.NoError(t, db.Model(&model.ZTAPISettlementLogOutbox{}).Count(&jobs).Error)
			require.Zero(t, jobs)
		})
	}
}

func TestZTAPIAttemptBillingLogRequiresTransaction(t *testing.T) {
	db, parent, charge, log := attemptBillingLogFixture(t)
	require.Error(t, EnqueueZTAPIAttemptBillingLogTx(nil, &parent, &charge, charge.BillingOperationID, log))
	require.Error(t, EnqueueZTAPIAttemptBillingLogTx(db, &parent, &charge, charge.BillingOperationID, log))
}

func TestZTAPIAttemptBillingServiceApprovalChargeLogsAndIndependentRefunds(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogDB })
	db, user, token := setupServiceTokenQuotaTest(t)
	model.LOG_DB = db
	require.NoError(t, db.Model(token).Update("unlimited_quota", false).Error)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{}))
	require.NoError(t, model.MigrateZTAPISupplierRefund(db))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	for _, id := range []int{13, 14} {
		require.NoError(t, db.Create(&model.Channel{Id: id, Name: "offline-proof-channel", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	}
	input, submission := attemptBillingPriceFixture(t)
	input.UserID, input.TokenID, input.ReservedQuota, submission.UserID = user.Id, token.Id, 30, user.Id
	parent, err := model.BeginZTAPIRequestSettlement(input)
	require.NoError(t, err)
	_, err = model.BeginZTAPIRequestAttempt(parent.OperationID, 13, submission.CredentialVersion, "chat")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(parent.OperationID, 1, 13, 502, submission.UpstreamRequestID))
	_, err = model.BeginZTAPIRequestAttempt(parent.OperationID, 14, "final-credential", "chat")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(parent.OperationID, 2, 14, 200, "wire-final"))
	usage := `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`
	dimensions := `[{"dimension":"input_tokens","units":"10","unit_quota":"1","charged_quota":10},{"dimension":"output_tokens","units":"5","unit_quota":"2","charged_quota":10}]`
	evidence := model.ZTAPISettlementEvidence{FinalAttempt: 2, ConsumeLog: model.Log{Type: model.LogTypeConsume, UserId: parent.UserID, TokenId: parent.TokenID, RequestId: parent.RequestID, ModelName: parent.PublicModel, ChannelId: 14, Quota: 20, PromptTokens: 10, CompletionTokens: 5, CreatedAt: parent.CreatedAt.Unix()}}
	parent, err = model.FinalizeZTAPIRequestSettlementWithEvidence(parent.OperationID, 20, usage, dimensions, evidence)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return model.EnsureZTAPIAttemptBillingReviewsTx(tx, parent) }))
	operator := model.User{Username: "attempt-service-finance", Status: common.UserStatusEnabled, Role: common.RoleFinanceUser, AffCode: "attempt-finance"}
	require.NoError(t, db.Create(&operator).Error)
	submission.Usage = []model.ZTAPIAttemptBillingQuantity{{Dimension: "input_tokens", Quantity: 40}}
	proof, err := model.SubmitZTAPIAttemptBilling(submission)
	require.NoError(t, err)
	require.Error(t, model.ProcessZTAPIAttemptBilling(proof.ID, EnqueueZTAPIAttemptBillingLogTx))
	var account model.User
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, 80, account.Quota, "unapproved evidence must not debit")
	require.NoError(t, model.ApproveZTAPIAttemptBilling(proof.ID, operator.Id, "verified-distinct-bill-line", PriceZTAPIAttemptBilling))
	for i := 0; i < 2; i++ {
		require.NoError(t, model.ProcessZTAPIAttemptBilling(proof.ID, EnqueueZTAPIAttemptBillingLogTx))
	}
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, 40, account.Quota)
	require.Equal(t, 60, account.UsedQuota)
	require.Equal(t, 1, account.RequestCount)
	assertServiceTokenQuota(t, db, token.Id, 40, 71)
	processed, err := model.ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	require.Equal(t, 2, processed)
	var charges []model.ZTAPISupplierRefundCharge
	require.NoError(t, db.Where("request_id = ?", parent.RequestID).Order("attempt").Find(&charges).Error)
	require.Len(t, charges, 2)
	for _, charge := range charges {
		refund, err := model.SubmitZTAPISupplierRefund(model.ZTAPISupplierRefundSubmission{
			Source: "offline-supplier", ProofID: "refund-" + charge.UpstreamRequestID, RequestID: parent.RequestID, UserID: parent.UserID,
			Attempt: charge.Attempt, ChannelID: charge.ChannelID, CredentialVersion: charge.CredentialVersion,
			UpstreamRequestID: charge.UpstreamRequestID, UpstreamTaskID: charge.UpstreamTaskID, UpstreamBillID: charge.UpstreamBillID,
			Mode: "full", EvidenceReference: "verified-reversal",
		})
		require.NoError(t, err)
		require.NoError(t, model.ApproveZTAPISupplierRefund(refund.ID, operator.Id, "reviewed-reversal"))
		for i := 0; i < 2; i++ {
			require.NoError(t, model.ProcessZTAPISupplierRefund(refund.ID))
		}
	}
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, 100, account.Quota)
	require.Equal(t, 60, account.UsedQuota)
	require.Equal(t, 1, account.RequestCount)
	assertServiceTokenQuota(t, db, token.Id, 100, 11)
	replayed, err := model.FinalizeZTAPIRequestSettlementWithEvidence(parent.OperationID, 20, usage, dimensions, evidence)
	require.NoError(t, err)
	require.EqualValues(t, 20, replayed.ChargedQuota)
	require.EqualValues(t, 60, replayed.RefundedQuota)
	require.Equal(t, 2, replayed.FinalAttempt)
	var logs int64
	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ?", parent.RequestID).Count(&logs).Error)
	require.EqualValues(t, 2, logs)
}

func TestZTAPIImagePendingFinalLateBillUsesFrozenPriceAndRefundsOriginalCustomerExactly(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	t.Cleanup(func() { model.DB, model.LOG_DB = oldDB, oldLogDB })
	db, user, token := setupServiceTokenQuotaTest(t)
	model.LOG_DB = db
	require.NoError(t, db.Model(token).Update("unlimited_quota", false).Error)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{}))
	require.NoError(t, model.MigrateZTAPISupplierRefund(db))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	require.NoError(t, model.MigrateZTAPIFinanceAlerts(db))
	require.NoError(t, db.Create(&model.Channel{Id: 13, Name: "image-proof-channel", Status: common.ChannelStatusEnabled, Type: 1}).Error)

	prices := map[string]string{"text_input": "1", "text_cached_input": "0.2", "image_input": "2", "image_cached_input": "0.4", "image_output": "4"}
	costs := map[string]string{"text_input": "0.6", "text_cached_input": "0.12", "image_input": "1.2", "image_cached_input": "0.24", "image_output": "2.4"}
	rules := make([]types.ZTAPIMediaPriceRule, 0, len(prices))
	for index, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		rules = append(rules, types.ZTAPIMediaPriceRule{
			ID: dimension, Conditions: map[string]string{"token_bucket": dimension}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{dimension: costs[dimension]}, SaleUSD: map[string]string{dimension: prices[dimension]},
			SourceCells: map[string]string{dimension: fmt.Sprintf("A%d", index+1)},
		})
	}
	input, submission := imageAttemptBillingPriceFixture(t, types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules}, []model.ZTAPIAttemptBillingQuantity{
		{Dimension: "text_input", Quantity: 2}, {Dimension: "text_cached_input", Quantity: 0},
		{Dimension: "image_input", Quantity: 3}, {Dimension: "image_cached_input", Quantity: 0}, {Dimension: "image_output", Quantity: 2},
	})
	input.ID, input.UserID, input.TokenID, input.ReservedQuota = 0, user.Id, token.Id, 20
	parent, err := model.BeginZTAPIRequestSettlement(input)
	require.NoError(t, err)
	attempt, err := model.BeginZTAPIRequestAttempt(parent.OperationID, submission.ChannelID, submission.CredentialVersion, "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(parent.OperationID, attempt.Attempt, attempt.ChannelID, 200, submission.UpstreamRequestID))
	parent, err = model.PendZTAPIRequestSettlement(parent.OperationID, "{}", `["upstream_usage_missing"]`)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return model.EnsureZTAPIAttemptBillingReviewsTx(tx, parent) }))
	operator := model.User{Username: "image-attempt-finance", Status: common.UserStatusEnabled, Role: common.RoleFinanceUser, AffCode: "image-attempt-finance-aff"}
	require.NoError(t, db.Create(&operator).Error)

	submission.RequestID, submission.UserID, submission.Attempt = parent.RequestID, parent.UserID, attempt.Attempt
	pricedPreview, err := PriceZTAPIAttemptBilling(*parent, submission)
	require.NoError(t, err)
	require.Equal(t, 8, pricedPreview.ConsumeLog.Quota)
	proof, err := model.SubmitZTAPIAttemptBilling(submission)
	require.NoError(t, err)
	if approvalErr := model.ApproveZTAPIAttemptBilling(proof.ID, operator.Id, "verified frozen image bill", PriceZTAPIAttemptBilling); approvalErr != nil {
		var pendingProof model.ZTAPIAttemptBillingProof
		require.NoError(t, db.First(&pendingProof, proof.ID).Error)
		t.Fatalf("approve image late bill: %v (pending_reason=%s priced=%+v)", approvalErr, pendingProof.PendingReason, pricedPreview)
	}
	for i := 0; i < 2; i++ {
		require.NoError(t, model.ProcessZTAPIAttemptBilling(proof.ID, EnqueueZTAPIAttemptBillingLogTx))
	}

	var settled model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&settled, parent.ID).Error)
	require.Equal(t, model.ZTAPISettlementSettled, settled.Status)
	require.EqualValues(t, 8, settled.ChargedQuota)
	require.Equal(t, attempt.Attempt, settled.FinalAttempt)
	var account model.User
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, 92, account.Quota)
	require.Equal(t, 8, account.UsedQuota)
	require.Equal(t, 1, account.RequestCount)
	assertServiceTokenQuota(t, db, token.Id, 92, 19)

	var charge model.ZTAPISupplierRefundCharge
	require.NoError(t, db.Where("request_id = ? AND attempt = ?", parent.RequestID, attempt.Attempt).Take(&charge).Error)
	require.Equal(t, parent.UserID, charge.UserID)
	require.Equal(t, parent.TokenID, charge.TokenID)
	require.Equal(t, parent.PriceSnapshotJSON, charge.PriceSnapshotJSON)
	require.Equal(t, submission.ChannelID, charge.ChannelID)
	require.Equal(t, submission.CredentialVersion, charge.CredentialVersion)
	require.Equal(t, submission.UpstreamRequestID, charge.UpstreamRequestID)
	require.Equal(t, submission.UpstreamBillID, charge.UpstreamBillID)
	require.JSONEq(t, `[
		{"dimension":"image_input","units":"3","unit_quota":"1","charged_quota":3,"token_charged_quota":3},
		{"dimension":"image_output","units":"2","unit_quota":"2","charged_quota":4,"token_charged_quota":4},
		{"dimension":"text_input","units":"2","unit_quota":"0.5","charged_quota":1,"token_charged_quota":1}
	]`, charge.DimensionsJSON)
	var storedProof model.ZTAPIAttemptBillingProof
	require.NoError(t, db.First(&storedProof, proof.ID).Error)
	var storedSubmission model.ZTAPIAttemptBillingSubmission
	require.NoError(t, common.UnmarshalJsonStr(storedProof.SubmissionJSON, &storedSubmission))
	require.Equal(t, submission.PriceRuleIDs, storedSubmission.PriceRuleIDs)
	require.JSONEq(t, submission.RawUsageJSON, storedSubmission.RawUsageJSON)

	partial, err := model.SubmitZTAPISupplierRefund(model.ZTAPISupplierRefundSubmission{
		Source: "offline-supplier", ProofID: "image-partial-refund", RequestID: parent.RequestID, UserID: parent.UserID,
		Attempt: charge.Attempt, ChannelID: charge.ChannelID, CredentialVersion: charge.CredentialVersion,
		UpstreamRequestID: charge.UpstreamRequestID, UpstreamBillID: charge.UpstreamBillID,
		Mode: "partial", Units: []model.ZTAPISupplierRefundUnits{{Dimension: "image_output", Units: "1"}}, EvidenceReference: "verified partial reversal",
	})
	require.NoError(t, err)
	require.NoError(t, model.ApproveZTAPISupplierRefund(partial.ID, operator.Id, "reviewed partial image reversal"))
	for i := 0; i < 2; i++ {
		require.NoError(t, model.ProcessZTAPISupplierRefund(partial.ID))
	}
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, 94, account.Quota)

	full, err := model.SubmitZTAPISupplierRefund(model.ZTAPISupplierRefundSubmission{
		Source: "offline-supplier", ProofID: "image-full-refund", RequestID: parent.RequestID, UserID: parent.UserID,
		Attempt: charge.Attempt, ChannelID: charge.ChannelID, CredentialVersion: charge.CredentialVersion,
		UpstreamRequestID: charge.UpstreamRequestID, UpstreamBillID: charge.UpstreamBillID,
		Mode: "full", EvidenceReference: "verified full reversal",
	})
	require.NoError(t, err)
	require.NoError(t, model.ApproveZTAPISupplierRefund(full.ID, operator.Id, "reviewed full image reversal"))
	for i := 0; i < 2; i++ {
		require.NoError(t, model.ProcessZTAPISupplierRefund(full.ID))
	}
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, 100, account.Quota)
	require.NoError(t, db.First(&settled, parent.ID).Error)
	require.EqualValues(t, 8, settled.RefundedQuota)
	assertServiceTokenQuota(t, db, token.Id, 100, 11)
}
