package service

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func ztapiImageReservationFixture(t *testing.T, maximumQuota int64) ZTAPIMediaReservationInput {
	t.Helper()
	contract := types.ZTAPIMediaPriceContract{
		Version:  1,
		Modality: "image",
		Rules: []types.ZTAPIMediaPriceRule{
			{
				ID:          "lte_200k",
				Conditions:  map[string]string{"prompt_tokens_tier": "lte_200k"},
				BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
				CostUSD:     map[string]string{"input_tokens": "0.6", "output_tokens": "1.2"},
				SaleUSD:     map[string]string{"input_tokens": "1", "output_tokens": "2"},
				SourceCells: map[string]string{"input_tokens": "A1", "output_tokens": "B1"},
			},
			{
				ID:          "gt_200k",
				Conditions:  map[string]string{"prompt_tokens_tier": "gt_200k"},
				BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
				CostUSD:     map[string]string{"input_tokens": "0.6", "output_tokens": "2.4"},
				SaleUSD:     map[string]string{"input_tokens": "1", "output_tokens": "4"},
				SourceCells: map[string]string{"input_tokens": "A2", "output_tokens": "B2"},
			},
		},
	}
	contractRaw, err := common.Marshal(contract)
	require.NoError(t, err)
	contractJSON, err := types.CanonicalizeZTAPIMediaPriceContract(string(contractRaw))
	require.NoError(t, err)
	protocol, protocolJSON, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersion, ProviderModel: "provider-image-test",
		EndpointType: types.ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 2},
		Response:     types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"}},
		Usage:        types.ZTAPIImageUsageContract{UsageField: "usage", Fields: map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"}, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Reservations: []types.ZTAPIImageReservationAuthority{
			{Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 1, MaximumDimensions: map[string]string{"input_tokens": "20", "output_tokens": "10"}},
			{Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 2, MaximumDimensions: map[string]string{"input_tokens": "20", "output_tokens": "10"}},
		},
		RequestIDField: "request_id", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	snapshotRaw, err := common.Marshal(relaycommon.ZTAPIPublicationSnapshot{
		PublicationID:          17,
		Version:                3,
		PublicName:             "zt-image-test",
		SourceModel:            "provider-image-test",
		Modality:               "image",
		PriceSourceID:          23,
		PriceSourceVersion:     5,
		MediaPriceContractJSON: contractJSON,
		ImageProtocolContract:  &protocol,
	})
	require.NoError(t, err)
	return ZTAPIMediaReservationInput{
		OperationID:          "media-operation-1",
		RequestID:            "media-request-1",
		PublicModel:          "zt-image-test",
		PriceSnapshotJSON:    string(snapshotRaw),
		SelectorJSON:         `{"modality":"image","n":1,"quality":"standard","response_format":"url","size":"1024x1024"}`,
		ProtocolContractJSON: protocolJSON,
		ProtocolEvidenceHash: protocol.EvidenceHash,
		QuotaPerUnit:         "500000",
		MaximumDimensions:    map[string]string{"input_tokens": "20", "output_tokens": "10"},
		MaximumQuota:         maximumQuota,
	}
}

func gptImageThreeDimensionContracts(t *testing.T) (types.ZTAPIMediaPriceContract, string, types.ZTAPIImageProtocolContract, string) {
	t.Helper()
	prices := map[string]string{"text_input": "1", "text_cached_input": "0.2", "image_input": "2", "image_cached_input": "0.4", "image_output": "4"}
	costs := map[string]string{"text_input": "0.6", "text_cached_input": "0.12", "image_input": "1.2", "image_cached_input": "0.24", "image_output": "2.4"}
	price := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image"}
	for _, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		price.Rules = append(price.Rules, types.ZTAPIMediaPriceRule{
			ID: dimension, Conditions: map[string]string{"token_bucket": dimension}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{dimension: costs[dimension]}, SaleUSD: map[string]string{dimension: prices[dimension]}, SourceCells: map[string]string{dimension: "A1"},
		})
	}
	rawPrice, err := common.Marshal(price)
	require.NoError(t, err)
	priceJSON, err := types.CanonicalizeZTAPIMediaPriceContract(string(rawPrice))
	require.NoError(t, err)
	protocol, protocolJSON, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersionV2, ProviderModel: "gpt-image-2",
		EndpointType: types.ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		WireProtocol: types.ZTAPIImageWireProtocolOpenAIImages, ProviderPath: "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"low"}, ResponseFormats: []string{"b64_json"}, MinCount: 1, MaxCount: 1},
		Response:     types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"b64_json": "b64_json"}},
		Usage: types.ZTAPIImageUsageContract{
			UsageField: "usage", TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported",
			Fields: map[string]string{"text_input": "input_tokens_details.text_tokens", "image_input": "input_tokens_details.image_tokens", "image_output": "output_tokens_details.image_tokens"},
		},
		Reservations:    []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "low", ResponseFormat: "b64_json", N: 1, MaximumDimensions: map[string]string{"text_input": "20", "image_input": "20", "image_output": "200"}}},
		RequestIDSource: types.ZTAPIResponseIDSourceHeader, RequestIDKey: "X-Synthetic-Request-ID",
		UpstreamRequestFields: map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "optional", "response_format": "omit"},
		EvidenceVersion:       types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	return price, priceJSON, protocol, protocolJSON
}

func gptImageThreeDimensionReservationFixture(t *testing.T) ZTAPIMediaReservationInput {
	t.Helper()
	_, priceJSON, protocol, protocolJSON := gptImageThreeDimensionContracts(t)
	snapshotRaw, err := common.Marshal(relaycommon.ZTAPIPublicationSnapshot{
		PublicationID: 71, Version: 3, PublicName: "zt-gp-image-2", SourceModel: "gpt-image-2", Modality: "image",
		PriceSourceID: 72, PriceSourceVersion: 5, MediaPriceContractJSON: priceJSON, ImageProtocolContract: &protocol,
	})
	require.NoError(t, err)
	return ZTAPIMediaReservationInput{
		OperationID: "gpt-image-three-operation", RequestID: "gpt-image-three-request", PublicModel: "zt-gp-image-2",
		PriceSnapshotJSON: string(snapshotRaw), SelectorJSON: `{"modality":"image","n":1,"quality":"low","response_format":"b64_json","size":"1024x1024"}`,
		ProtocolContractJSON: protocolJSON, ProtocolEvidenceHash: protocol.EvidenceHash, QuotaPerUnit: "500000",
		MaximumDimensions: map[string]string{"text_input": "20", "image_input": "20", "image_output": "200"}, MaximumQuota: 430,
	}
}

func TestGPTImageThreeDimensionReservationAndFinalSettlement(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Model(user).Update("quota", 1000).Error)
	require.NoError(t, db.Model(token).Update("remain_quota", 1000).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 71, Name: "gpt-image-three-upstream", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	input := gptImageThreeDimensionReservationFixture(t)
	input.UserID, input.TokenID = user.Id, token.Id

	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	require.EqualValues(t, 430, row.InitialReservedQuota)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 71, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 71, 200, "gpt-image-upstream-request"))

	var frozen ztapiFrozenMediaReservation
	require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &frozen))
	protocol, _, err := types.ParseZTAPIImageProtocolContract(frozen.ImageProtocolContractJSON)
	require.NoError(t, err)
	frozen.ImageProtocolContract = &protocol
	const rawUsage = `{"input_tokens":18,"input_tokens_details":{"image_tokens":0,"text_tokens":18},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":214}`
	body := []byte(`{"data":[{"b64_json":"c3ludGhldGljLWltYWdl"}],"usage":` + rawUsage + `}`)
	handoff := &relaycommon.ZTAPIValidatedImageResponse{ContractVersion: 2, EvidenceHash: frozen.ProtocolEvidenceHash, UpstreamRequestID: "gpt-image-upstream-request", ResultCount: 1, RawResponse: body, RawUsageJSON: []byte(rawUsage)}
	info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &frozen.ZTAPIPublicationSnapshot}
	evidence, err := relaycommon.NormalizeZTAPIImageUsageCandidate(info, handoff, body)
	require.NoError(t, err)
	require.Len(t, evidence.GetDimensions(), 3)
	require.Len(t, evidence.GetPriceRuleIDs(), 3)

	settled, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.NoError(t, err)
	require.EqualValues(t, 401, settled.ChargedQuota)
	require.NotContains(t, settled.UsageJSON, "cached_input")
	require.JSONEq(t, `[
		{"dimension":"image_output","units":"196","unit_quota":"2","charged_quota":392,"token_charged_quota":0},
		{"dimension":"text_input","units":"18","unit_quota":"0.5","charged_quota":9,"token_charged_quota":0}
	]`, settled.ChargeDimensionsJSON)
}

func TestZTAPIImageSettlementReservationFreezesMediaInput(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}))
	input := ztapiImageReservationFixture(t, 20)
	input.UserID = user.Id
	input.TokenID = token.Id

	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	require.Equal(t, int64(20), row.InitialReservedQuota)
	var frozen ztapiFrozenMediaReservation
	require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &frozen))
	require.EqualValues(t, 20, frozen.MaximumQuota)
	require.JSONEq(t, input.SelectorJSON, frozen.SelectorJSON)
	require.Equal(t, "500000", frozen.QuotaPerUnit)
	require.Equal(t, input.ProtocolContractJSON, frozen.ImageProtocolContractJSON)
	require.Equal(t, input.ProtocolEvidenceHash, frozen.ProtocolEvidenceHash)
	require.Equal(t, map[string]string{"input_tokens": "20", "output_tokens": "10"}, frozen.MaximumDimensions)

	changed := input
	changed.SelectorJSON = `{"modality":"image","n":2,"quality":"standard","response_format":"url","size":"1024x1024"}`
	_, err = BeginZTAPIMediaReservation(changed)
	require.ErrorIs(t, err, model.ErrZTAPISettlementConflict)
}

func TestZTAPIImageReservationRejectsCallerAuthorityTampering(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}))

	for _, mutate := range []func(*ZTAPIMediaReservationInput){
		func(input *ZTAPIMediaReservationInput) { input.MaximumQuota-- },
		func(input *ZTAPIMediaReservationInput) { input.MaximumDimensions["input_tokens"] = "19" },
		func(input *ZTAPIMediaReservationInput) { input.ProtocolEvidenceHash = strings.Repeat("0", 64) },
		func(input *ZTAPIMediaReservationInput) { input.ProtocolContractJSON = "" },
		func(input *ZTAPIMediaReservationInput) {
			input.SelectorJSON = `{"modality":"image","n":3,"quality":"standard","response_format":"url","size":"1024x1024"}`
		},
	} {
		input := ztapiImageReservationFixture(t, 20)
		input.OperationID = common.GetUUID()
		input.RequestID = common.GetUUID()
		input.UserID, input.TokenID = user.Id, token.Id
		mutate(&input)
		_, err := BeginZTAPIMediaReservation(input)
		require.Error(t, err)
	}
}

func TestFinalizeZTAPIImageSettlementUsesFrozenQuotaPerUnit(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Create(&model.Channel{Id: 35, Name: "frozen-quota-upstream", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	input := ztapiImageReservationFixture(t, 20)
	input.OperationID, input.RequestID = "media-frozen-quota-operation", "media-frozen-quota-request"
	input.UserID, input.TokenID = user.Id, token.Id
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 35, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 35, 200, "upstream-request-1"))

	originalQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 1_000_000
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })
	evidence := trustedZTAPIImageEvidence(t, frozenMediaPriceContract(t, row), `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`)
	settled, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.NoError(t, err)
	require.EqualValues(t, 12, settled.ChargedQuota)
}

func TestFinalizeZTAPIImageSettlementOverMaximumStaysPendingWithoutRefund(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Create(&model.Channel{Id: 36, Name: "over-maximum-upstream", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	input := ztapiImageReservationFixture(t, 20)
	input.OperationID, input.RequestID = "media-over-maximum-operation", "media-over-maximum-request"
	input.UserID, input.TokenID = user.Id, token.Id
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 36, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 36, 200, "upstream-request-1"))
	evidence := trustedZTAPIImageEvidence(t, frozenMediaPriceContract(t, row), `{"input_tokens":21,"output_tokens":1,"total_tokens":22}`)
	_, err = FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.ErrorIs(t, err, model.ErrZTAPISettlementPending)
	pending, err := PendZTAPIImageSettlement(row.OperationID, evidence, "usage_exceeds_frozen_maximum")
	require.NoError(t, err)
	require.Equal(t, model.ZTAPISettlementPending, pending.Status)
	var ledgers int64
	require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ?", row.RequestID).Count(&ledgers).Error)
	require.EqualValues(t, 1, ledgers)
}

func TestFinalizeZTAPIImageSettlementRejectsTamperedFrozenProtocolIdentity(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	input := ztapiImageReservationFixture(t, 20)
	input.OperationID, input.RequestID = "media-protocol-tamper-operation", "media-protocol-tamper-request"
	input.UserID, input.TokenID = user.Id, token.Id
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	var frozen ztapiFrozenMediaReservation
	require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &frozen))
	frozen.ProtocolEvidenceHash = strings.Repeat("0", 64)
	tampered, err := common.Marshal(frozen)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Where("id = ?", row.ID).Update("price_snapshot_json", string(tampered)).Error)
	evidence := trustedZTAPIImageEvidence(t, frozen.MediaPriceContractJSON, `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`)
	_, err = FinalizeZTAPIImageSettlement(row.OperationID, evidence, 1)
	require.ErrorIs(t, err, model.ErrZTAPISettlementConflict)
}

func TestFinalizeZTAPIImageSettlementRejectsTamperedFrozenReservationAuthority(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)

	mutations := map[string]func(*ztapiFrozenMediaReservation){
		"maximum quota":      func(frozen *ztapiFrozenMediaReservation) { frozen.MaximumQuota-- },
		"maximum dimensions": func(frozen *ztapiFrozenMediaReservation) { frozen.MaximumDimensions["input_tokens"] = "19" },
		"missing protocol":   func(frozen *ztapiFrozenMediaReservation) { frozen.ImageProtocolContractJSON = "" },
		"protocol hash":      func(frozen *ztapiFrozenMediaReservation) { frozen.ProtocolEvidenceHash = strings.Repeat("0", 64) },
		"quota per unit":     func(frozen *ztapiFrozenMediaReservation) { frozen.QuotaPerUnit = "500000.0" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			input := ztapiImageReservationFixture(t, 20)
			input.OperationID, input.RequestID = common.GetUUID(), common.GetUUID()
			input.UserID, input.TokenID = user.Id, token.Id
			row, err := BeginZTAPIMediaReservation(input)
			require.NoError(t, err)

			var frozen ztapiFrozenMediaReservation
			require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &frozen))
			mutate(&frozen)
			tampered, err := common.Marshal(frozen)
			require.NoError(t, err)
			require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Where("id = ?", row.ID).Update("price_snapshot_json", string(tampered)).Error)

			evidence := trustedZTAPIImageEvidence(t, frozen.MediaPriceContractJSON, `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`)
			_, err = FinalizeZTAPIImageSettlement(row.OperationID, evidence, 1)
			require.ErrorIs(t, err, model.ErrZTAPISettlementConflict)
		})
	}
}

func TestFinalizeZTAPIImageSettlementRejectsMissingFrozenTierIdentity(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Create(&model.Channel{Id: 37, Name: "tier-identity-upstream", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	input := ztapiImageReservationFixture(t, 20)
	input.OperationID, input.RequestID = "media-tier-identity-operation", "media-tier-identity-request"
	input.UserID, input.TokenID = user.Id, token.Id
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 37, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 37, 200, "upstream-request-1"))
	evidence := trustedZTAPIImageEvidence(t, frozenMediaPriceContract(t, row), `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`)
	evidence.SelectedRuleID = ""
	_, err = FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.ErrorIs(t, err, model.ErrZTAPISettlementPending)
}

func frozenMediaPriceContract(t *testing.T, row *model.ZTAPIRequestSettlement) string {
	t.Helper()
	var frozen ztapiFrozenMediaReservation
	require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &frozen))
	return frozen.MediaPriceContractJSON
}

func TestZTAPIImageReservationIgnoresGenericAmountAuthority(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}))
	input := ztapiImageReservationFixture(t, 20)
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	require.NoError(t, common.UnmarshalJsonStr(input.PriceSnapshotJSON, &snapshot))
	protocol, _, err := types.ParseZTAPIImageProtocolContract(input.ProtocolContractJSON)
	require.NoError(t, err)
	snapshot.ImageProtocolContract = &protocol
	n := uint(1)
	makeInfo := func(requestID string) *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			RequestId: requestID, UserId: user.Id, TokenId: token.Id, OriginModelName: input.PublicModel,
			Request:                  &dto.ImageRequest{N: &n, Size: "1024x1024", Quality: "standard", ResponseFormat: "url"},
			ZTAPIPublicationSnapshot: snapshot.Clone(),
		}
	}
	for _, genericAmount := range []int{1, 999999} {
		billing, apiErr := newZTAPIDurableBilling(makeInfo(common.GetUUID()), genericAmount)
		require.Nil(t, apiErr)
		require.Equal(t, 20, billing.GetPreConsumedQuota())
	}
}

func TestZTAPIImageSettlementInsufficientBalanceStopsBeforeUpstream(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}))
	input := ztapiImageReservationFixture(t, 200)
	input.QuotaPerUnit = "5000000"
	input.OperationID = "media-insufficient-operation"
	input.RequestID = "media-insufficient-request"
	input.UserID = user.Id
	input.TokenID = token.Id

	var upstreamCalls atomic.Int32
	_, err := BeginZTAPIMediaReservation(input)
	if err == nil {
		upstreamCalls.Add(1)
	}
	require.True(t, errors.Is(err, model.ErrBalanceLedgerNegativeBalance) || errors.Is(err, model.ErrInsufficientUserQuota))
	require.Zero(t, upstreamCalls.Load())
}

func replaceZTAPIImageReservationProtocol(t *testing.T, input *ZTAPIMediaReservationInput, fields, maximum map[string]string, maximumQuota int64) {
	t.Helper()
	cacheSemantics := "not_reported"
	if len(fields) == 5 {
		cacheSemantics = "separate_dimension"
	}
	protocol, protocolJSON, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersion, ProviderModel: "provider-image-test",
		EndpointType: types.ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		Capabilities:   types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 1},
		Response:       types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"}},
		Usage:          types.ZTAPIImageUsageContract{UsageField: "usage", Fields: fields, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: cacheSemantics},
		Reservations:   []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 1, MaximumDimensions: maximum}},
		RequestIDField: "request_id", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	input.ProtocolContractJSON = protocolJSON
	input.ProtocolEvidenceHash = protocol.EvidenceHash
	input.MaximumDimensions = maximum
	input.MaximumQuota = maximumQuota
}

func migrateZTAPIMediaBillingTestTables(t *testing.T) {
	t.Helper()
	db := model.DB
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.ZTAPIRequestSettlement{},
		&model.ZTAPIRequestAttempt{},
		&model.ZTAPISettlementFinalizationIntent{},
	))
	require.NoError(t, model.MigrateZTAPISupplierRefund(db))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	require.NoError(t, model.MigrateZTAPISettlementLogOutbox(db))
	require.NoError(t, model.MigrateZTAPIFinanceAlerts(db))
}

func trustedZTAPIImageEvidence(t *testing.T, contractJSON, rawUsage string) relaycommon.ZTAPIMediaUsageEvidence {
	t.Helper()
	contract, err := types.ParseZTAPIMediaPriceContract(contractJSON)
	require.NoError(t, err)
	fields := make(map[string]string)
	for _, rule := range contract.Rules {
		for dimension := range rule.SaleUSD {
			fields[dimension] = dimension
		}
	}
	cacheSemantics := "not_reported"
	if _, ok := contract.Rules[0].Conditions["token_bucket"]; ok {
		cacheSemantics = "separate_dimension"
	}
	protocol, _, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version:       types.ZTAPIImageProtocolContractVersion,
		ProviderModel: "provider-image-test",
		EndpointType:  types.ZTAPIImageEndpointGeneration,
		Method:        "POST",
		Path:          "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{
			Sizes: []string{"1024x1024"}, Qualities: []string{"standard"},
			ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 1,
		},
		Response: types.ZTAPIImageResponseContract{
			Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"},
		},
		Usage: types.ZTAPIImageUsageContract{
			UsageField: "usage", Fields: fields,
			TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: cacheSemantics,
		},
		Reservations: []types.ZTAPIImageReservationAuthority{{
			Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 1,
			MaximumDimensions: func() map[string]string {
				maximum := make(map[string]string, len(fields))
				for dimension := range fields {
					maximum[dimension] = "200000"
				}
				return maximum
			}(),
		}},
		RequestIDField: "request_id", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	response := []byte(fmt.Sprintf(`{"request_id":"upstream-request-1","data":[{"url":"https://example.test/image.png"}],"usage":%s}`, rawUsage))
	handoff := &relaycommon.ZTAPIValidatedImageResponse{
		ContractVersion: protocol.Version, EvidenceHash: protocol.EvidenceHash,
		UpstreamRequestID: "upstream-request-1", ResultCount: 1,
		RawResponse: response, RawUsageJSON: []byte(rawUsage),
	}
	info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
		Modality: "image", MediaPriceContractJSON: contractJSON, ImageProtocolContract: &protocol,
	}}
	evidence, err := relaycommon.NormalizeZTAPIImageUsageCandidate(info, handoff, response)
	require.NoError(t, err)
	return evidence
}

func TestFinalizeZTAPIImageSettlementUsesFrozenExactDecimalEvidence(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Create(&model.Channel{Id: 31, Name: "image-upstream", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	input := ztapiImageReservationFixture(t, 20)
	input.UserID, input.TokenID = user.Id, token.Id
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 31, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 31, 200, "upstream-request-1"))

	var frozen ztapiFrozenMediaReservation
	require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &frozen))
	evidence := trustedZTAPIImageEvidence(t, frozen.MediaPriceContractJSON, `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`)
	settled, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.NoError(t, err)
	require.EqualValues(t, 12, settled.ChargedQuota)
	require.Equal(t, model.ZTAPISettlementSettled, settled.Status)
	require.Equal(t, attempt.Attempt, settled.FinalAttempt)
	require.Contains(t, settled.UsageJSON, `"raw_usage_json":"{\"input_tokens\":10,\"output_tokens\":7,\"total_tokens\":17}"`)
	require.Contains(t, settled.UsageJSON, `"upstream_request_id":"upstream-request-1"`)
	require.Contains(t, settled.UsageJSON, `"price_rule_ids":{"input_tokens":"lte_200k","output_tokens":"lte_200k"}`)
	require.JSONEq(t, `[
		{"dimension":"input_tokens","units":"10","unit_quota":"0.5","charged_quota":5,"token_charged_quota":0},
		{"dimension":"output_tokens","units":"7","unit_quota":"1","charged_quota":7,"token_charged_quota":0}
	]`, settled.ChargeDimensionsJSON)

	replayed, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.NoError(t, err)
	require.Equal(t, settled.ID, replayed.ID)
	time.Sleep(1100 * time.Millisecond)
	delayedReplay, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.NoError(t, err)
	require.Equal(t, settled.ID, delayedReplay.ID)
	var ledgers, logs, charges int64
	require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ?", row.RequestID).Count(&ledgers).Error)
	require.NoError(t, db.Model(&model.ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID).Count(&logs).Error)
	require.NoError(t, db.Model(&model.ZTAPISupplierRefundCharge{}).Where("request_id = ?", row.RequestID).Count(&charges).Error)
	require.EqualValues(t, 2, ledgers)
	require.EqualValues(t, 1, logs)
	require.EqualValues(t, 1, charges)
	var outbox model.ZTAPISettlementLogOutbox
	require.NoError(t, db.Where("operation_id = ?", row.OperationID).Take(&outbox).Error)
	var consumeLog model.Log
	require.NoError(t, common.UnmarshalJsonStr(outbox.PayloadJSON, &consumeLog))
	require.Equal(t, 10, consumeLog.PromptTokens)
	require.Equal(t, 7, consumeLog.CompletionTokens)

	conflict := evidence.Clone()
	conflict.UpstreamRequestID = "different-upstream-request"
	_, err = FinalizeZTAPIImageSettlement(row.OperationID, conflict, attempt.Attempt)
	require.ErrorIs(t, err, model.ErrZTAPISettlementConflict)
}

func TestFinalizeZTAPIImageSettlementPricesEveryTrustedTokenBucket(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	rules := []types.ZTAPIMediaPriceRule{}
	prices := map[string]string{
		"text_input": "1", "text_cached_input": "0.2", "image_input": "2",
		"image_cached_input": "0.4", "image_output": "4",
	}
	costs := map[string]string{
		"text_input": "0.6", "text_cached_input": "0.12", "image_input": "1.2",
		"image_cached_input": "0.24", "image_output": "2.4",
	}
	for index, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		rules = append(rules, types.ZTAPIMediaPriceRule{
			ID: dimension, Conditions: map[string]string{"token_bucket": dimension}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{dimension: costs[dimension]}, SaleUSD: map[string]string{dimension: prices[dimension]},
			SourceCells: map[string]string{dimension: fmt.Sprintf("A%d", index+1)},
		})
	}
	rawContract, err := common.Marshal(types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules})
	require.NoError(t, err)
	contractJSON, err := types.CanonicalizeZTAPIMediaPriceContract(string(rawContract))
	require.NoError(t, err)
	input := ztapiImageReservationFixture(t, 20)
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	require.NoError(t, common.UnmarshalJsonStr(input.PriceSnapshotJSON, &snapshot))
	snapshot.MediaPriceContractJSON = contractJSON
	snapshotRaw, err := common.Marshal(snapshot)
	require.NoError(t, err)
	input.OperationID, input.RequestID = "media-five-bucket-operation", "media-five-bucket-request"
	input.UserID, input.TokenID, input.PriceSnapshotJSON = user.Id, token.Id, string(snapshotRaw)
	replaceZTAPIImageReservationProtocol(t, &input,
		map[string]string{"text_input": "text_input", "text_cached_input": "text_cached_input", "image_input": "image_input", "image_cached_input": "image_cached_input", "image_output": "image_output"},
		map[string]string{"text_input": "2", "text_cached_input": "10", "image_input": "3", "image_cached_input": "5", "image_output": "2"}, 10)
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Channel{Id: 33, Name: "five-bucket-upstream", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 33, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 33, 200, "upstream-request-1"))
	evidence := trustedZTAPIImageEvidence(t, contractJSON, `{"text_input":2,"text_cached_input":10,"image_input":3,"image_cached_input":5,"image_output":2,"total_tokens":22}`)
	settled, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.NoError(t, err)
	require.EqualValues(t, 10, settled.ChargedQuota)
	var dimensions []model.ZTAPISupplierRefundDimension
	require.NoError(t, common.UnmarshalJsonStr(settled.ChargeDimensionsJSON, &dimensions))
	require.Len(t, dimensions, 5)
}

func TestFinalizeZTAPIImageSettlementDoesNotSynthesizeMinimumQuota(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: []types.ZTAPIMediaPriceRule{
		{ID: "lte_200k", Conditions: map[string]string{"prompt_tokens_tier": "lte_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.00000006", "output_tokens": "0.00000006"}, SaleUSD: map[string]string{"input_tokens": "0.0000001", "output_tokens": "0.0000001"}, SourceCells: map[string]string{"input_tokens": "A1", "output_tokens": "B1"}},
		{ID: "gt_200k", Conditions: map[string]string{"prompt_tokens_tier": "gt_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.00000006", "output_tokens": "0.00000006"}, SaleUSD: map[string]string{"input_tokens": "0.0000001", "output_tokens": "0.0000001"}, SourceCells: map[string]string{"input_tokens": "A2", "output_tokens": "B2"}},
	}}
	rawContract, err := common.Marshal(contract)
	require.NoError(t, err)
	contractJSON, err := types.CanonicalizeZTAPIMediaPriceContract(string(rawContract))
	require.NoError(t, err)
	input := ztapiImageReservationFixture(t, 0)
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	require.NoError(t, common.UnmarshalJsonStr(input.PriceSnapshotJSON, &snapshot))
	snapshot.MediaPriceContractJSON = contractJSON
	snapshotRaw, err := common.Marshal(snapshot)
	require.NoError(t, err)
	input.OperationID, input.RequestID = "media-subquota-operation", "media-subquota-request"
	input.UserID, input.TokenID, input.PriceSnapshotJSON = user.Id, token.Id, string(snapshotRaw)
	replaceZTAPIImageReservationProtocol(t, &input,
		map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"},
		map[string]string{"input_tokens": "1", "output_tokens": "1"}, 0)
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Channel{Id: 34, Name: "subquota-upstream", Status: common.ChannelStatusEnabled, Type: 1}).Error)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 34, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 34, 200, "upstream-request-1"))
	evidence := trustedZTAPIImageEvidence(t, contractJSON, `{"input_tokens":1,"output_tokens":1,"total_tokens":2}`)
	settled, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
	require.NoError(t, err)
	require.Zero(t, settled.ChargedQuota)
}

func TestPendZTAPIImageSettlementKeepsReservationAndQueuesOneAlert(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	input := ztapiImageReservationFixture(t, 20)
	input.OperationID = "media-pending-operation"
	input.RequestID = "media-pending-request"
	input.UserID, input.TokenID = user.Id, token.Id
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 32, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 32, 200, "upstream-request-1"))

	var frozen ztapiFrozenMediaReservation
	require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &frozen))
	evidence := trustedZTAPIImageEvidence(t, frozen.MediaPriceContractJSON, `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`)
	evidence.Pending = true
	evidence.Reason = "usage total is untrusted"
	pending, err := PendZTAPIImageSettlement(row.OperationID, evidence, "image_usage_untrusted")
	require.NoError(t, err)
	require.Equal(t, model.ZTAPISettlementPending, pending.Status)
	require.JSONEq(t, `["image_usage_untrusted"]`, pending.MissingDimensionsJSON)
	require.Contains(t, pending.UsageJSON, `"raw_usage_json":"{\"input_tokens\":10,\"output_tokens\":7,\"total_tokens\":17}"`)
	require.Contains(t, pending.UsageJSON, `"upstream_request_id":"upstream-request-1"`)

	replayed, err := PendZTAPIImageSettlement(row.OperationID, evidence, "image_usage_untrusted")
	require.NoError(t, err)
	require.Equal(t, pending.ID, replayed.ID)
	_, err = model.ReleaseZTAPIRequestSettlement(row.OperationID)
	require.ErrorIs(t, err, model.ErrZTAPISettlementPending)
	var ledgers, alerts int64
	require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ?", row.RequestID).Count(&ledgers).Error)
	require.NoError(t, db.Model(&model.ZTAPIFinanceAlertOutbox{}).Where("source_kind = ? AND source_record_id = ?", "settlement", row.ID).Count(&alerts).Error)
	require.EqualValues(t, 1, ledgers, "pending delivery must not refund the reservation")
	require.EqualValues(t, 1, alerts)

	_, err = PendZTAPIImageSettlement(row.OperationID, evidence, "different_reason")
	require.ErrorIs(t, err, model.ErrZTAPISettlementConflict)
}

func TestPendZTAPIImageBillingBindsSuccessfulAttemptAfterPriorFailure(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	input := ztapiImageReservationFixture(t, 20)
	input.OperationID, input.RequestID = "media-pending-lineage-operation", "media-pending-lineage-request"
	input.UserID, input.TokenID = user.Id, token.Id
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	first, err := model.BeginZTAPIRequestAttempt(row.OperationID, 41, "credential-v1", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, first.Attempt, first.ChannelID, 502, "wire-first"))
	second, err := model.BeginZTAPIRequestAttempt(row.OperationID, 42, "credential-v2", "/v1/images/generations")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, second.Attempt, second.ChannelID, 200, ""))

	info := &relaycommon.RelayInfo{}
	info.Billing = &ztapiDurableBilling{row: row, info: info, attempt: second}
	evidence := trustedZTAPIImageEvidence(t, frozenMediaPriceContract(t, row), `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`)
	evidence.Pending = true
	evidence.Reason = "usage total is untrusted"
	require.NoError(t, PendZTAPIImageBilling(info, evidence, "image_usage_untrusted"))

	var stored model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&stored, row.ID).Error)
	require.Equal(t, model.ZTAPISettlementPending, stored.Status)
	require.Equal(t, second.Attempt, stored.FinalAttempt)
	var storedAttempt model.ZTAPIRequestAttempt
	require.NoError(t, db.Where("settlement_id = ? AND attempt = ?", row.ID, second.Attempt).Take(&storedAttempt).Error)
	require.Equal(t, evidence.UpstreamRequestID, storedAttempt.UpstreamRequestID)
}

func TestZTAPIGPTImageSyntheticFiveDimensionContractRejectedBeforeReservation(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Model(user).Update("quota", 1000).Error)
	input := ztapiImageReservationFixture(t, 20)
	input.OperationID, input.RequestID = "gpt-image-pending", "gpt-image-request"
	input.UserID, input.TokenID = user.Id, token.Id
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	require.NoError(t, common.UnmarshalJsonStr(input.PriceSnapshotJSON, &snapshot))
	protocol, _, err := types.ParseZTAPIImageProtocolContract(input.ProtocolContractJSON)
	require.NoError(t, err)
	protocol.Version, protocol.ProviderModel, protocol.RequestIDField = 2, "gpt-image-2", ""
	protocol.RequestIDSource, protocol.RequestIDKey = "header", "X-Synthetic-Request-ID"
	protocol.UpstreamRequestFields = map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "required", "response_format": "omit"}
	protocol.Capabilities = types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"low"}, ResponseFormats: []string{"b64_json"}, MinCount: 1, MaxCount: 1}
	protocol.Response.ResultFields = map[string]string{"b64_json": "b64_json"}
	// Missing cache paths are synthetic test authority, not inferred provider fields.
	protocol.Usage.Fields = map[string]string{"text_input": "input_tokens_details.text_tokens", "image_input": "input_tokens_details.image_tokens", "image_output": "output_tokens_details.image_tokens", "text_cached_input": "synthetic_text_cache", "image_cached_input": "synthetic_image_cache"}
	protocol.Usage.CacheSemantics = "separate_dimension"
	maximum := map[string]string{"text_input": "20", "image_input": "20", "image_output": "200", "text_cached_input": "20", "image_cached_input": "20"}
	protocol.Reservations = []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "low", ResponseFormat: "b64_json", N: 1, MaximumDimensions: maximum}}
	sealed, canonical, err := types.SealZTAPIImageProtocolContract(protocol)
	require.NoError(t, err)
	rules := []types.ZTAPIMediaPriceRule{}
	for _, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		rules = append(rules, types.ZTAPIMediaPriceRule{ID: dimension, Conditions: map[string]string{"token_bucket": dimension}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens, CostUSD: map[string]string{dimension: "0.6"}, SaleUSD: map[string]string{dimension: "1"}, SourceCells: map[string]string{dimension: "A1"}})
	}
	rawPrice, err := common.Marshal(types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules})
	require.NoError(t, err)
	snapshot.MediaPriceContractJSON, err = types.CanonicalizeZTAPIMediaPriceContract(string(rawPrice))
	require.NoError(t, err)
	snapshot.SourceModel, snapshot.ImageProtocolContract = "gpt-image-2", &sealed
	rawSnapshot, err := common.Marshal(snapshot)
	require.NoError(t, err)
	input.PriceSnapshotJSON, input.ProtocolContractJSON, input.ProtocolEvidenceHash = string(rawSnapshot), canonical, sealed.EvidenceHash
	input.SelectorJSON = `{"modality":"image","n":1,"quality":"low","response_format":"b64_json","size":"1024x1024"}`
	input.MaximumDimensions, input.MaximumQuota = maximum, 140
	row, err := BeginZTAPIMediaReservation(input)
	require.Nil(t, row)
	require.ErrorIs(t, err, model.ErrZTAPISettlementInvalid)
	var ledgerCount int64
	require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ?", input.RequestID).Count(&ledgerCount).Error)
	require.Zero(t, ledgerCount, "a synthetic GPT Image 2 contract must fail before reserving customer funds")
}
