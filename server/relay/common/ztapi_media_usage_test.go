package common

import (
	"fmt"
	"reflect"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestNormalizeZTAPIImageUsageRequiresVerifiedHandoff(t *testing.T) {
	evidence, err := NormalizeZTAPIImageUsage(&RelayInfo{}, []byte(`{}`))
	require.Error(t, err)
	require.False(t, evidence.ResultAvailable)
	require.Empty(t, evidence.GetDimensions())
}

func TestNormalizeZTAPIImageUsageTrustedFiveBuckets(t *testing.T) {
	const rawUsage = `{ "text_input" : 10, "text_cached_input":2, "image_input":20, "image_cached_input":5, "image_output":30, "total_tokens":67 }`
	info, response := newZTAPIMediaUsageInfo(t, "five", "url", rawUsage)

	evidence, err := NormalizeZTAPIImageUsage(info, response)
	require.NoError(t, err)
	require.Equal(t, "upstream-request-1", evidence.UpstreamRequestID)
	require.Equal(t, map[string]decimal.Decimal{
		"text_input": decimal.NewFromInt(10), "text_cached_input": decimal.NewFromInt(2),
		"image_input": decimal.NewFromInt(20), "image_cached_input": decimal.NewFromInt(5), "image_output": decimal.NewFromInt(30),
	}, evidence.GetDimensions())
	require.Equal(t, 1, evidence.ResultCount)
	require.True(t, evidence.ResultAvailable)
	require.False(t, evidence.Pending)
	require.Empty(t, evidence.Reason)
	require.Equal(t, rawUsage, evidence.RawUsageJSON)
	require.Equal(t, map[string]string{
		"text_input": "text_input", "text_cached_input": "text_cached_input",
		"image_input": "image_input", "image_cached_input": "image_cached_input", "image_output": "image_output",
	}, evidence.GetPriceRuleIDs())
}

func TestNormalizeGPTImage2ObservedUsageKeepsStrictThreeDimensions(t *testing.T) {
	const rawUsage = `{"input_tokens":18,"input_tokens_details":{"image_tokens":0,"text_tokens":18},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":214}`
	info, response := newZTAPIMediaUsageInfo(t, "gpt-three", "b64_json", rawUsage)

	evidence, err := NormalizeZTAPIImageUsage(info, response)
	require.NoError(t, err)
	require.False(t, evidence.Pending)
	require.Equal(t, map[string]decimal.Decimal{
		"text_input":   decimal.NewFromInt(18),
		"image_input":  decimal.NewFromInt(0),
		"image_output": decimal.NewFromInt(196),
	}, evidence.GetDimensions())
	require.Equal(t, map[string]string{
		"text_input": "text_input", "image_input": "image_input", "image_output": "image_output",
	}, evidence.GetPriceRuleIDs())
}

func TestNormalizeGPTImage2UsageFailsPendingOnCacheOrAggregateDrift(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rawUsage string
	}{
		{
			name:     "cache member",
			rawUsage: `{"input_tokens":18,"input_tokens_details":{"image_tokens":0,"text_tokens":18,"cached_tokens":1},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":214}`,
		},
		{
			name:     "input aggregate drift",
			rawUsage: `{"input_tokens":19,"input_tokens_details":{"image_tokens":0,"text_tokens":18},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":214}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, response := newZTAPIMediaUsageInfo(t, "gpt-three", "b64_json", tc.rawUsage)
			evidence, err := NormalizeZTAPIImageUsage(info, response)
			require.ErrorIs(t, err, ErrZTAPIMediaUsagePending)
			require.True(t, evidence.Pending)
			require.Empty(t, evidence.GetDimensions())
			require.Empty(t, evidence.GetPriceRuleIDs())
		})
	}
}

func TestNormalizeZTAPIImageUsageSelectsGeminiTierBoundary(t *testing.T) {
	tests := []struct {
		name, rawUsage, wantRuleID string
	}{
		{"lte_200k", `{"input_tokens":200000,"output_tokens":7,"total_tokens":200007}`, "lte_200k"},
		{"gt_200k", `{"input_tokens":200001,"output_tokens":7,"total_tokens":200008}`, "gt_200k"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, response := newZTAPIMediaUsageInfo(t, "gemini", "url", tc.rawUsage)
			evidence, err := NormalizeZTAPIImageUsage(info, response)
			require.NoError(t, err)
			require.Equal(t, tc.wantRuleID, evidence.SelectedRuleID)
			require.Equal(t, map[string]string{
				"input_tokens": tc.wantRuleID, "output_tokens": tc.wantRuleID,
			}, evidence.GetPriceRuleIDs())
		})
	}
}

func TestNormalizeZTAPIImageUsagePendingEvidence(t *testing.T) {
	tests := []struct {
		name, rawUsage string
		mutate         func(*testing.T, *RelayInfo)
	}{
		{"usage_missing", "", nil},
		{"dimension_missing", `{"text_input":10,"text_cached_input":2,"image_input":20,"image_cached_input":5,"total_tokens":37}`, nil},
		{"negative", `{"text_input":-1,"text_cached_input":2,"image_input":20,"image_cached_input":5,"image_output":30,"total_tokens":56}`, nil},
		{"int64_overflow", `{"text_input":9223372036854775808,"text_cached_input":2,"image_input":20,"image_cached_input":5,"image_output":30,"total_tokens":55}`, nil},
		{"exponent", `{"text_input":1e3,"text_cached_input":2,"image_input":20,"image_cached_input":5,"image_output":30,"total_tokens":1057}`, nil},
		{"total_mismatch", `{"text_input":10,"text_cached_input":2,"image_input":20,"image_cached_input":5,"image_output":30,"total_tokens":68}`, nil},
		{"included_in_input", `{"text_input":10,"text_cached_input":2,"image_input":20,"image_cached_input":5,"image_output":30,"total_tokens":67}`, func(t *testing.T, info *RelayInfo) {
			resealZTAPIMediaUsageProtocol(t, info, func(contract *types.ZTAPIImageProtocolContract) {
				contract.Usage.CacheSemantics = "included_in_input"
			})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, response := newZTAPIMediaUsageInfo(t, "five", "url", tc.rawUsage)
			if tc.mutate != nil {
				tc.mutate(t, info)
			}
			evidence, err := NormalizeZTAPIImageUsage(info, response)
			require.ErrorIs(t, err, ErrZTAPIMediaUsagePending)
			require.True(t, evidence.ResultAvailable)
			require.True(t, evidence.Pending)
			require.NotEmpty(t, evidence.Reason)
			require.Empty(t, evidence.GetDimensions())
			require.Empty(t, evidence.GetPriceRuleIDs())
		})
	}
}

func TestNormalizeZTAPIImageUsageRejectsHardEvidenceMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RelayInfo)
	}{
		{"raw_response", func(info *RelayInfo) { info.ztapiValidatedImageResponse.RawResponse = []byte(`{}`) }},
		{"raw_usage_json", func(info *RelayInfo) { info.ztapiValidatedImageResponse.RawUsageJSON = []byte(`{}`) }},
		{"contract_version", func(info *RelayInfo) { info.ztapiValidatedImageResponse.ContractVersion++ }},
		{"contract_hash", func(info *RelayInfo) { info.ztapiValidatedImageResponse.EvidenceHash = "mismatch" }},
		{"result_count", func(info *RelayInfo) { info.ztapiValidatedImageResponse.ResultCount = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const rawUsage = `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`
			info, response := newZTAPIMediaUsageInfo(t, "gemini", "url", rawUsage)
			tc.mutate(info)
			evidence, err := NormalizeZTAPIImageUsage(info, response)
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrZTAPIMediaUsagePending)
			require.False(t, evidence.ResultAvailable)
			require.Empty(t, evidence.GetDimensions())
			require.Empty(t, evidence.GetPriceRuleIDs())
		})
	}
}

func TestNormalizeZTAPIImageUsageAcceptsURLAndBase64Results(t *testing.T) {
	for _, responseFormat := range []string{"url", "b64_json"} {
		t.Run(responseFormat, func(t *testing.T) {
			const rawUsage = `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`
			info, response := newZTAPIMediaUsageInfo(t, "gemini", responseFormat, rawUsage)
			evidence, err := NormalizeZTAPIImageUsage(info, response)
			require.NoError(t, err)
			require.True(t, evidence.ResultAvailable)
			require.Equal(t, 1, evidence.ResultCount)
			require.Equal(t, "upstream-request-1", evidence.UpstreamRequestID)
		})
	}
}

func TestNormalizeZTAPIImageUsageEvidenceDeepCopiesRawUsageAndDimensions(t *testing.T) {
	const rawUsage = `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`
	info, response := newZTAPIMediaUsageInfo(t, "gemini", "url", rawUsage)
	evidence, err := NormalizeZTAPIImageUsage(info, response)
	require.NoError(t, err)

	got := evidence.GetDimensions()
	got["input_tokens"] = decimal.NewFromInt(30)
	require.Equal(t, decimal.NewFromInt(10), evidence.GetDimensions()["input_tokens"])

	raw := evidence.GetRawUsageJSON()
	raw[0] = '['
	require.Equal(t, rawUsage, string(evidence.GetRawUsageJSON()))
	require.Equal(t, []byte(rawUsage), info.ztapiValidatedImageResponse.RawUsageJSON)

	info.ztapiValidatedImageResponse.RawUsageJSON[0] = '['
	require.Equal(t, rawUsage, evidence.RawUsageJSON)

	_, exported := reflect.TypeOf(evidence).FieldByName("Dimensions")
	require.False(t, exported, "callers must not be able to bypass the clone getter")
}

func TestZTAPIMediaUsageEvidenceCloneFreezesPersistentHandoff(t *testing.T) {
	const rawUsage = `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`
	info, response := newZTAPIMediaUsageInfo(t, "gemini", "url", rawUsage)
	evidence, err := NormalizeZTAPIImageUsage(info, response)
	require.NoError(t, err)
	persisted := evidence.Clone()
	originalDimensions := evidence.dimensions
	originalRuleIDs := evidence.priceRuleIDs
	originalDimensions["input_tokens"] = decimal.NewFromInt(888)
	originalRuleIDs["input_tokens"] = "original-tampered"
	require.Equal(t, decimal.NewFromInt(10), persisted.GetDimensions()["input_tokens"])
	require.Equal(t, "lte_200k", persisted.GetPriceRuleIDs()["input_tokens"])
	persisted.dimensions["output_tokens"] = decimal.NewFromInt(777)
	persisted.priceRuleIDs["output_tokens"] = "clone-tampered"
	require.Equal(t, decimal.NewFromInt(7), evidence.GetDimensions()["output_tokens"])
	require.Equal(t, "lte_200k", evidence.GetPriceRuleIDs()["output_tokens"])

	dimensions := evidence.GetDimensions()
	dimensions["input_tokens"] = decimal.NewFromInt(999)
	ruleIDs := evidence.GetPriceRuleIDs()
	ruleIDs["input_tokens"] = "tampered"
	raw := evidence.GetRawUsageJSON()
	raw[0] = '['
	evidence.RawUsageJSON = `{"tampered":true}`

	require.Equal(t, decimal.NewFromInt(10), persisted.GetDimensions()["input_tokens"])
	require.Equal(t, "lte_200k", persisted.GetPriceRuleIDs()["input_tokens"])
	require.Equal(t, rawUsage, string(persisted.GetRawUsageJSON()))
	require.Equal(t, []byte(rawUsage), info.ztapiValidatedImageResponse.RawUsageJSON)

	typ := reflect.TypeOf(evidence)
	_, dimensionsExported := typ.FieldByName("Dimensions")
	_, ruleIDsExported := typ.FieldByName("PriceRuleIDs")
	require.False(t, dimensionsExported)
	require.False(t, ruleIDsExported)
}

func TestNormalizeZTAPIImageUsageFromPrivateCandidate(t *testing.T) {
	const rawUsage = `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`
	info, response := newZTAPIMediaUsageInfo(t, "gemini", "url", rawUsage)
	handoff := info.ztapiValidatedImageResponse
	info.ztapiValidatedImageResponse = nil
	attemptID := info.BeginZTAPIImageResponseAttempt()
	require.True(t, info.RecordZTAPIImageResponseValidation(attemptID, handoff))

	candidate := info.CloneCurrentZTAPIImageResponseCandidate(response)
	require.NotNil(t, candidate)
	candidate.RawUsageJSON[0] = '['
	candidate = info.CloneCurrentZTAPIImageResponseCandidate(response)
	require.Equal(t, []byte(rawUsage), candidate.RawUsageJSON)

	evidence, err := NormalizeZTAPIImageUsageCandidate(info, candidate, response)
	require.NoError(t, err)
	require.False(t, evidence.Pending)
	require.Nil(t, info.ztapiValidatedImageResponse)
	require.Nil(t, info.GetZTAPIMediaUsageEvidence())
}

func TestZTAPIImagePromotionPublishesResponseAndClonedEvidenceTogether(t *testing.T) {
	const rawUsage = `{"input_tokens":10,"output_tokens":7,"total_tokens":17}`
	info, response := newZTAPIMediaUsageInfo(t, "gemini", "url", rawUsage)
	handoff := info.ztapiValidatedImageResponse
	info.ztapiValidatedImageResponse = nil
	attemptID := info.BeginZTAPIImageResponseAttempt()
	require.True(t, info.RecordZTAPIImageResponseValidation(attemptID, handoff))
	candidate := info.CloneCurrentZTAPIImageResponseCandidate(response)
	evidence, err := NormalizeZTAPIImageUsageCandidate(info, candidate, response)
	require.NoError(t, err)

	require.True(t, info.PromoteZTAPIValidatedImageResponse(response, evidence))
	require.NotNil(t, info.ztapiValidatedImageResponse)
	publishedResponse, publishedUsage := info.GetZTAPIImageSettlementEvidence()
	require.NotNil(t, publishedResponse)
	require.NotNil(t, publishedUsage)
	publishedResponse.RawResponse[0] = '!'
	publishedUsage.Pending = true
	publishedUsage.SetRawUsageJSON([]byte(`{"tampered":true}`))
	publishedUsage.GetDimensions()["input_tokens"] = decimal.NewFromInt(999)
	stableResponse, stableUsage := info.GetZTAPIImageSettlementEvidence()
	require.Equal(t, response, stableResponse.RawResponse)
	require.Equal(t, decimal.NewFromInt(10), stableUsage.GetDimensions()["input_tokens"])
	published := info.GetZTAPIMediaUsageEvidence()
	require.NotNil(t, published)
	require.Equal(t, "upstream-request-1", published.UpstreamRequestID)
	published.Pending = true
	published.Reason = "tampered"
	published.SetRawUsageJSON([]byte(`{"tampered":true}`))

	stable := info.GetZTAPIMediaUsageEvidence()
	require.False(t, stable.Pending)
	require.Empty(t, stable.Reason)
	require.Equal(t, rawUsage, string(stable.GetRawUsageJSON()))
}

func TestZTAPIImageSettlementEvidenceNeverMixesGenerations(t *testing.T) {
	info, _ := newZTAPIMediaUsageInfo(t, "gemini", "url", `{"input_tokens":1,"output_tokens":1,"total_tokens":2}`)
	info.ztapiValidatedImageResponse = nil

	done := make(chan struct{})
	errorsSeen := make(chan string, 1)
	go func() {
		defer close(done)
		for index := 0; index < 500; index++ {
			response, usage := info.GetZTAPIImageSettlementEvidence()
			if response != nil && usage != nil && response.UpstreamRequestID != usage.UpstreamRequestID {
				select {
				case errorsSeen <- response.UpstreamRequestID + "/" + usage.UpstreamRequestID:
				default:
				}
				return
			}
		}
	}()

	for index := 0; index < 100; index++ {
		requestID := fmt.Sprintf("req-%d", index)
		rawUsage := []byte(`{"input_tokens":1,"output_tokens":1,"total_tokens":2}`)
		raw := []byte(fmt.Sprintf(`{"request_id":%q,"data":[{"url":"https://example.invalid/x"}],"usage":%s}`, requestID, rawUsage))
		attemptID := info.BeginZTAPIImageResponseAttempt()
		candidate := &ZTAPIValidatedImageResponse{
			ContractVersion:   info.ZTAPIPublicationSnapshot.ImageProtocolContract.Version,
			EvidenceHash:      info.ZTAPIPublicationSnapshot.ImageProtocolContract.EvidenceHash,
			UpstreamRequestID: requestID, ResultCount: 1, RawResponse: raw, RawUsageJSON: rawUsage,
		}
		require.True(t, info.RecordZTAPIImageResponseValidation(attemptID, candidate))
		evidence := ZTAPIMediaUsageEvidence{UpstreamRequestID: requestID, ResultCount: 1, ResultAvailable: true}
		evidence.SetRawUsageJSON(rawUsage)
		require.True(t, info.PromoteZTAPIValidatedImageResponse(raw, evidence))
	}
	<-done
	select {
	case mismatch := <-errorsSeen:
		t.Fatalf("mixed image settlement evidence generations: %s", mismatch)
	default:
	}
}

func TestNormalizeZTAPIImageUsageSeparateCacheMayExceedNonCacheInput(t *testing.T) {
	const rawUsage = `{"text_input":3,"text_cached_input":5,"image_input":7,"image_cached_input":11,"image_output":13,"total_tokens":39}`
	info, response := newZTAPIMediaUsageInfo(t, "five", "url", rawUsage)

	evidence, err := NormalizeZTAPIImageUsage(info, response)
	require.NoError(t, err)
	require.True(t, evidence.ResultAvailable)
	require.Equal(t, decimal.NewFromInt(5), evidence.GetDimensions()["text_cached_input"])
	require.Equal(t, decimal.NewFromInt(11), evidence.GetDimensions()["image_cached_input"])
}

func newZTAPIMediaUsageInfo(t *testing.T, shape, responseFormat, rawUsage string) (*RelayInfo, []byte) {
	t.Helper()
	fields := map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"}
	cacheSemantics := "not_reported"
	providerModel := "provider-image-model"
	version := types.ZTAPIImageProtocolContractVersion
	requestIDField, requestIDSource, requestIDKey := "request_id", "", ""
	priceShape := shape
	var upstreamRequestFields map[string]string
	if shape == "five" {
		fields = map[string]string{
			"text_input": "text_input", "text_cached_input": "text_cached_input",
			"image_input": "image_input", "image_cached_input": "image_cached_input", "image_output": "image_output",
		}
		cacheSemantics = "separate_dimension"
	} else if shape == "gpt-three" {
		fields = map[string]string{
			"text_input":   "input_tokens_details.text_tokens",
			"image_input":  "input_tokens_details.image_tokens",
			"image_output": "output_tokens_details.image_tokens",
		}
		providerModel = "gpt-image-2"
		priceShape = "five"
		version = types.ZTAPIImageProtocolContractVersionV2
		requestIDField, requestIDSource, requestIDKey = "", types.ZTAPIResponseIDSourceHeader, "X-Synthetic-Request-ID"
		upstreamRequestFields = map[string]string{
			"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "optional", "response_format": "omit",
		}
	}
	maximumDimensions := make(map[string]string, len(fields))
	for dimension := range fields {
		maximumDimensions[dimension] = "200000"
	}
	protocol, _, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: version, ProviderModel: providerModel,
		EndpointType: types.ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		Capabilities:   types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{responseFormat}, MinCount: 1, MaxCount: 1},
		Response:       types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{responseFormat: responseFormat}},
		Usage:          types.ZTAPIImageUsageContract{UsageField: "usage", Fields: fields, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: cacheSemantics},
		Reservations:   []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "standard", ResponseFormat: responseFormat, N: 1, MaximumDimensions: maximumDimensions}},
		RequestIDField: requestIDField, RequestIDSource: requestIDSource, RequestIDKey: requestIDKey,
		UpstreamRequestFields: upstreamRequestFields, EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	result := `{"url":"https://example.test/image.png"}`
	if responseFormat == "b64_json" {
		result = `{"b64_json":"aGVsbG8="}`
	}
	response := []byte(fmt.Sprintf(`{"request_id":"upstream-request-1","data":[%s]`, result))
	if rawUsage != "" {
		response = append(response, []byte(`,"usage":`+rawUsage)...)
	}
	response = append(response, '}')
	handoff := &ZTAPIValidatedImageResponse{
		ContractVersion: protocol.Version, EvidenceHash: protocol.EvidenceHash, UpstreamRequestID: "upstream-request-1", ResultCount: 1,
		RawResponse: append([]byte(nil), response...), RawUsageJSON: []byte(rawUsage),
	}
	return &RelayInfo{
		ZTAPIPublicationSnapshot:    &ZTAPIPublicationSnapshot{Modality: "image", MediaPriceContractJSON: canonicalMediaPriceContract(t, priceShape), ImageProtocolContract: &protocol},
		ztapiValidatedImageResponse: handoff,
	}, response
}

func resealZTAPIMediaUsageProtocol(t *testing.T, info *RelayInfo, mutate func(*types.ZTAPIImageProtocolContract)) {
	t.Helper()
	contract := info.ZTAPIPublicationSnapshot.ImageProtocolContract.Clone()
	mutate(&contract)
	sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	info.ZTAPIPublicationSnapshot.ImageProtocolContract = &sealed
	info.ztapiValidatedImageResponse.ContractVersion = sealed.Version
	info.ztapiValidatedImageResponse.EvidenceHash = sealed.EvidenceHash
}

func canonicalMediaPriceContract(t *testing.T, shape string) string {
	t.Helper()
	rule := func(id, conditionKey, dimension, cell string) types.ZTAPIMediaPriceRule {
		return types.ZTAPIMediaPriceRule{
			ID: id, Conditions: map[string]string{conditionKey: id}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{dimension: "0.6"}, SaleUSD: map[string]string{dimension: "1"}, SourceCells: map[string]string{dimension: cell},
		}
	}
	var rules []types.ZTAPIMediaPriceRule
	if shape == "five" {
		for index, dimension := range []string{"image_cached_input", "image_input", "image_output", "text_cached_input", "text_input"} {
			rules = append(rules, rule(dimension, "token_bucket", dimension, fmt.Sprintf("A%d", index+1)))
		}
	} else {
		for index, tier := range []string{"gt_200k", "lte_200k"} {
			rules = append(rules, types.ZTAPIMediaPriceRule{
				ID: tier, Conditions: map[string]string{"prompt_tokens_tier": tier}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
				CostUSD: map[string]string{"input_tokens": "0.6", "output_tokens": "0.6"}, SaleUSD: map[string]string{"input_tokens": "1", "output_tokens": "1"},
				SourceCells: map[string]string{"input_tokens": fmt.Sprintf("A%d", index+1), "output_tokens": fmt.Sprintf("B%d", index+1)},
			})
		}
	}
	raw, err := basecommon.Marshal(types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules})
	require.NoError(t, err)
	canonical, err := types.CanonicalizeZTAPIMediaPriceContract(string(raw))
	require.NoError(t, err)
	return canonical
}
