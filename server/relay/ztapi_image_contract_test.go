package relay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func syntheticManagedImageContract(t *testing.T) *types.ZTAPIImageProtocolContract {
	t.Helper()
	contract := types.ZTAPIImageProtocolContract{
		Version:       types.ZTAPIImageProtocolContractVersion,
		ProviderModel: "verified-provider-image",
		EndpointType:  types.ZTAPIImageEndpointGeneration,
		Method:        http.MethodPost,
		Path:          "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{
			Sizes:           []string{"512x512", "1024x1024"},
			Qualities:       []string{"standard", "high"},
			ResponseFormats: []string{"url", "b64_json"},
			MinCount:        1,
			MaxCount:        2,
			SupportsEdits:   false,
		},
		Response: types.ZTAPIImageResponseContract{
			Schema:       "object_results_array",
			ResultsField: "data",
			ResultFields: map[string]string{"url": "url", "b64_json": "b64_json"},
		},
		Usage: types.ZTAPIImageUsageContract{
			UsageField:     "usage",
			Fields:         map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"},
			TotalField:     "total_tokens",
			TotalSemantics: "sum_of_dimensions",
			CacheSemantics: "not_reported",
		},
		RequestIDField:  "request_id",
		EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	}
	for _, size := range contract.Capabilities.Sizes {
		for _, quality := range contract.Capabilities.Qualities {
			for _, format := range contract.Capabilities.ResponseFormats {
				for n := contract.Capabilities.MinCount; n <= contract.Capabilities.MaxCount; n++ {
					contract.Reservations = append(contract.Reservations, types.ZTAPIImageReservationAuthority{
						Size: size, Quality: quality, ResponseFormat: format, N: n,
						MaximumDimensions: map[string]string{"input_tokens": "20", "output_tokens": "10"},
					})
				}
			}
		}
	}
	sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	return &sealed
}

func syntheticManagedImageMediaPriceContract(t *testing.T) string {
	t.Helper()
	rules := make([]types.ZTAPIMediaPriceRule, 0, 2)
	for index, tier := range []string{"gt_200k", "lte_200k"} {
		rules = append(rules, types.ZTAPIMediaPriceRule{
			ID: tier, Conditions: map[string]string{"prompt_tokens_tier": tier}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.6", "output_tokens": "0.6"}, SaleUSD: map[string]string{"input_tokens": "1", "output_tokens": "1"},
			SourceCells: map[string]string{"input_tokens": fmt.Sprintf("A%d", index+1), "output_tokens": fmt.Sprintf("B%d", index+1)},
		})
	}
	raw, err := common.Marshal(types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules})
	require.NoError(t, err)
	canonical, err := types.CanonicalizeZTAPIMediaPriceContract(string(raw))
	require.NoError(t, err)
	return canonical
}

func managedImageFixture(t *testing.T, method, path string, body map[string]any) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	raw, err := common.Marshal(body)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("ztapi_image_test_recorder", recorder)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	request := &dto.ImageRequest{}
	require.NoError(t, common.Unmarshal(raw, request))
	return c, &relaycommon.RelayInfo{
		OriginModelName: "zt-quoted-image",
		RequestURLPath:  path,
		Request:         request,
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PublicName:             "zt-quoted-image",
			SourceModel:            "verified-provider-image",
			Modality:               "image",
			MediaPriceContractJSON: syntheticManagedImageMediaPriceContract(t),
			ImageProtocolContract:  syntheticManagedImageContract(t),
		},
	}
}

func validManagedImageBody() map[string]any {
	return map[string]any{
		"model": "zt-quoted-image", "prompt": "a neutral test image",
		"n": 1, "size": "1024x1024", "quality": "standard", "response_format": "url",
	}
}

func TestZTAPIImageContractAdmissionRejectsBeforeNextStage(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		mutateBody func(map[string]any)
		mutateInfo func(*relaycommon.RelayInfo)
	}{
		{name: "missing contract", method: http.MethodPost, path: "/v1/images/generations", mutateInfo: func(info *relaycommon.RelayInfo) { info.ZTAPIPublicationSnapshot.ImageProtocolContract = nil }},
		{name: "managed text alias on image route", method: http.MethodPost, path: "/v1/images/generations", mutateInfo: func(info *relaycommon.RelayInfo) {
			info.ZTAPIPublicationSnapshot.Modality = "text"
			info.ZTAPIPublicationSnapshot.ImageProtocolContract = nil
		}},
		{name: "wrong method", method: http.MethodGet, path: "/v1/images/generations"},
		{name: "image edit", method: http.MethodPost, path: "/v1/images/edits"},
		{name: "nearby path", method: http.MethodPost, path: "/v1/images/generations/extra"},
		{name: "unsupported size", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { body["size"] = "2048x2048" }},
		{name: "unsupported quality", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { body["quality"] = "ultra" }},
		{name: "unsupported format", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { body["response_format"] = "binary" }},
		{name: "count below range", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { body["n"] = 0 }},
		{name: "count above range", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { body["n"] = 3 }},
		{name: "omitted size", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { delete(body, "size") }},
		{name: "omitted quality", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { delete(body, "quality") }},
		{name: "omitted format", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { delete(body, "response_format") }},
		{name: "omitted count", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { delete(body, "n") }},
		{name: "unsupported field", method: http.MethodPost, path: "/v1/images/generations", mutateBody: func(body map[string]any) { body["provider_model"] = "attacker-model" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := validManagedImageBody()
			if tt.mutateBody != nil {
				tt.mutateBody(body)
			}
			c, info := managedImageFixture(t, tt.method, tt.path, body)
			if tt.mutateInfo != nil {
				tt.mutateInfo(info)
			}
			nextCalls := 0
			err := WithZTAPIImageAdmission(c, info, func() *types.NewAPIError {
				nextCalls++
				return nil
			})
			require.NotNil(t, err)
			require.Zero(t, nextCalls, "reservation/upstream stage must not run after rejection")
		})
	}
}

func TestZTAPIImageContractAdmissionDoesNotGuessGeminiOrChangeLegacy(t *testing.T) {
	body := validManagedImageBody()
	body["model"] = "gemini-2.5-flash-image"
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
	info.OriginModelName = "gemini-2.5-flash-image"
	info.ZTAPIPublicationSnapshot.SourceModel = "gemini-2.5-flash-image"
	info.ZTAPIPublicationSnapshot.ImageProtocolContract = nil
	require.NotNil(t, AdmitZTAPIImageRequest(c, info), "model discovery alone must not create an OpenAI Images binding")

	legacy, legacyInfo := managedImageFixture(t, http.MethodPost, "/v1/images/generations", map[string]any{"model": "dall-e-3", "prompt": "legacy"})
	legacyInfo.ZTAPIPublicationSnapshot = nil
	require.Nil(t, AdmitZTAPIImageRequest(legacy, legacyInfo))
}

func TestZTAPIImageDispatchForcesFrozenBindingDespitePassThroughInput(t *testing.T) {
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	settings := model_setting.GetGlobalSettings()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.PassThroughRequestEnabled = true
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, ApiType: constant.APITypeOpenAI, ChannelSetting: dto.ChannelSettings{PassThroughBodyEnabled: true}}
	request, err := PrepareZTAPIManagedImageDispatch(c, info)
	require.Nil(t, err)
	require.Equal(t, "verified-provider-image", request.Model)
	require.Equal(t, "verified-provider-image", info.UpstreamModelName)
	require.Equal(t, "/v1/images/generations", info.RequestURLPath)
	require.Empty(t, request.Extra)

	raw, marshalErr := common.Marshal(request)
	require.NoError(t, marshalErr)
	var outbound map[string]any
	require.NoError(t, common.Unmarshal(raw, &outbound))
	require.Equal(t, "verified-provider-image", outbound["model"])
	require.NotContains(t, outbound, "provider_model")
}

func TestZTAPIImageDispatchRejectsAbstractOpenAIAPITypeFromWrongChannel(t *testing.T) {
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAzure, ApiType: constant.APITypeOpenAI}
	request, err := PrepareZTAPIManagedImageDispatch(c, info)
	require.Nil(t, request)
	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.StatusCode)
}

func TestZTAPIImageContractAdmissionRunsAcceptedStageExactlyOnce(t *testing.T) {
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	nextCalls := 0
	err := WithZTAPIImageAdmission(c, info, func() *types.NewAPIError {
		nextCalls++
		return nil
	})
	require.Nil(t, err)
	require.Equal(t, 1, nextCalls)
}

func TestZTAPIImageResponseValidationProducesRawTypedHandoff(t *testing.T) {
	_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	BeginZTAPIManagedImageAttempt(info)
	raw := []byte("{\n  \"request_id\":\"req-verified-1\",\n  \"data\":[{\"url\":\"https://example.invalid/result.png\"}],\n  \"usage\": { \"input_tokens\" : 123456789, \"output_tokens\":7, \"total_tokens\":123456796 }\n}")
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}
	handoff, err := ValidateZTAPIManagedImageResponse(info, resp)
	require.Nil(t, err)
	require.Equal(t, "req-verified-1", handoff.UpstreamRequestID)
	require.Equal(t, 1, handoff.ResultCount)
	require.Equal(t, types.ZTAPIImageProtocolContractVersion, handoff.ContractVersion)
	require.Equal(t, info.ZTAPIPublicationSnapshot.ImageProtocolContract.EvidenceHash, handoff.EvidenceHash)
	require.Equal(t, raw, handoff.RawResponse)
	require.Equal(t, []byte(`{ "input_tokens" : 123456789, "output_tokens":7, "total_tokens":123456796 }`), handoff.RawUsageJSON)
	publishedResponse, _ := info.GetZTAPIImageSettlementEvidence()
	require.Nil(t, publishedResponse, "validation alone must not publish billing evidence")
	replayed, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.Equal(t, raw, replayed, "downstream handler must receive the exact raw response")
	evidence, normalizeErr := relaycommon.NormalizeZTAPIImageUsageCandidate(info, handoff, raw)
	require.NoError(t, normalizeErr)
	require.True(t, info.PromoteZTAPIValidatedImageResponse(raw, evidence))
	publishedResponse, _ = info.GetZTAPIImageSettlementEvidence()
	require.Equal(t, handoff, publishedResponse)
}

func TestZTAPIImageResponsePreservesUntrustedUsageCandidate(t *testing.T) {
	for _, tt := range []struct {
		name, usage, wantRaw string
	}{
		{name: "missing"},
		{name: "malformed type", usage: `,"usage":"unknown"`, wantRaw: `"unknown"`},
		{name: "dimension missing", usage: `,"usage":{"input_tokens":1,"total_tokens":1}`, wantRaw: `{"input_tokens":1,"total_tokens":1}`},
		{name: "negative", usage: `,"usage":{"input_tokens":-1,"output_tokens":2,"total_tokens":1}`, wantRaw: `{"input_tokens":-1,"output_tokens":2,"total_tokens":1}`},
		{name: "exponent", usage: `,"usage":{"input_tokens":1e3,"output_tokens":1,"total_tokens":1001}`, wantRaw: `{"input_tokens":1e3,"output_tokens":1,"total_tokens":1001}`},
		{name: "overflow", usage: `,"usage":{"input_tokens":9223372036854775808,"output_tokens":1,"total_tokens":9223372036854775809}`, wantRaw: `{"input_tokens":9223372036854775808,"output_tokens":1,"total_tokens":9223372036854775809}`},
		{name: "total mismatch", usage: `,"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":7}`, wantRaw: `{"input_tokens":1,"output_tokens":1,"total_tokens":7}`},
		{name: "duplicate usage dimension", usage: `,"usage":{"input_tokens":1,"input_tokens":2,"output_tokens":1,"total_tokens":3}`, wantRaw: `{"input_tokens":1,"input_tokens":2,"output_tokens":1,"total_tokens":3}`},
		{name: "extra dto incompatible", usage: `,"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"prompt_tokens":1e3}`, wantRaw: `{"input_tokens":1,"output_tokens":1,"total_tokens":2,"prompt_tokens":1e3}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
			BeginZTAPIManagedImageAttempt(info)
			raw := []byte(`{"request_id":"req-usage","data":[{"url":"https://example.invalid/x"}]` + tt.usage + `}`)
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}
			candidate, apiErr := ValidateZTAPIManagedImageResponse(info, resp)
			require.Nil(t, apiErr)
			require.NotNil(t, candidate)
			if tt.wantRaw == "" {
				require.Nil(t, candidate.RawUsageJSON)
			} else {
				require.Equal(t, []byte(tt.wantRaw), candidate.RawUsageJSON)
			}
			publishedResponse, _ := info.GetZTAPIImageSettlementEvidence()
			require.Nil(t, publishedResponse)
			require.NotZero(t, info.CurrentZTAPIImageResponseAttemptID())
			replayed, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, raw, replayed)
		})
	}
}

func TestZTAPIManagedImageDuplicateUsageIsPendingRatherThanHardFailure(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"request_id":"req-duplicate","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`),
		[]byte(`{"request_id":"req-duplicate","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"input_tokens":2,"output_tokens":1,"total_tokens":3}}`),
	} {
		_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
		BeginZTAPIManagedImageAttempt(info)
		resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}
		candidate, apiErr := ValidateZTAPIManagedImageResponse(info, resp)
		require.Nil(t, apiErr)
		require.NotNil(t, candidate)
		require.NotEmpty(t, candidate.UsagePendingReason)
		evidence, normalizeErr := relaycommon.NormalizeZTAPIImageUsageCandidate(info, candidate, raw)
		require.ErrorIs(t, normalizeErr, relaycommon.ErrZTAPIMediaUsagePending)
		require.True(t, evidence.Pending)
		require.NotEmpty(t, evidence.Reason)
	}
}

func TestZTAPIManagedImageRejectsDuplicateOutsideUsage(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"request_id":"first","request_id":"second","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`),
		[]byte(`{"usage":{"input_tokens":1,"input_tokens":2,"output_tokens":1,"total_tokens":3},"request_id":"first","request_id":"second","data":[{"url":"https://example.invalid/x"}]}`),
	} {
		_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
		BeginZTAPIManagedImageAttempt(info)
		candidate, apiErr := ValidateZTAPIManagedImageResponse(info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)})
		require.Nil(t, candidate)
		require.NotNil(t, apiErr)
		require.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
		require.True(t, types.IsSkipRetryError(apiErr))
	}
}

func TestZTAPIManagedImageDoesNotSynthesizeUsage(t *testing.T) {
	service.InitHttpClient()
	var upstreamCalls atomic.Int32
	const responseBody = `{"request_id":"req-exponent","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1e3,"output_tokens":1,"total_tokens":1001}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(upstream.Close)

	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	info.RelayMode = relayconstant.RelayModeImagesGenerations
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "test-only-key")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, info.OriginModelName)

	apiErr := ImageHelper(c, info)
	require.Nil(t, apiErr)
	require.Equal(t, int32(1), upstreamCalls.Load())
	recorder := c.MustGet("ztapi_image_test_recorder").(*httptest.ResponseRecorder)
	require.JSONEq(t, responseBody, recorder.Body.String())
	evidence := info.GetZTAPIMediaUsageEvidence()
	require.NotNil(t, evidence)
	require.True(t, evidence.Pending)
	require.NotEmpty(t, evidence.Reason)
	require.Equal(t, []byte(`{"input_tokens":1e3,"output_tokens":1,"total_tokens":1001}`), evidence.GetRawUsageJSON())
	require.Empty(t, evidence.GetDimensions())
}

func TestZTAPIManagedImageRejectsUnexpectedUpstreamStreamBeforeClientWrite(t *testing.T) {
	service.InitHttpClient()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `{"request_id":"req-stream","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)

	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	info.RelayMode = relayconstant.RelayModeImagesGenerations
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "test-only-key")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, info.OriginModelName)

	apiErr := ImageHelper(c, info)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	require.True(t, types.IsSkipRetryError(apiErr))
	recorder := c.MustGet("ztapi_image_test_recorder").(*httptest.ResponseRecorder)
	require.Zero(t, recorder.Body.Len())
	require.Nil(t, info.GetZTAPIMediaUsageEvidence())
	require.Zero(t, info.CurrentZTAPIImageResponseAttemptID())
}

func TestZTAPIImageResponseRepresentationMustMatchAdmittedRequest(t *testing.T) {
	tests := []struct {
		name, requestFormat, responseField, responseValue string
	}{
		{name: "url request cannot accept base64", requestFormat: "url", responseField: "b64_json", responseValue: "YWJj"},
		{name: "base64 request cannot accept url", requestFormat: "b64_json", responseField: "url", responseValue: "https://example.invalid/result.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := validManagedImageBody()
			body["response_format"] = tt.requestFormat
			_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
			BeginZTAPIManagedImageAttempt(info)
			raw := []byte(`{"request_id":"req-format","data":[{"` + tt.responseField + `":"` + tt.responseValue + `"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}
			handoff, err := ValidateZTAPIManagedImageResponse(info, resp)
			require.Nil(t, handoff)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, err.StatusCode)
			require.True(t, types.IsSkipRetryError(err))
		})
	}
}

func TestZTAPIImageAttemptClearsSuccessfulEvidenceBeforeFailedRetry(t *testing.T) {
	_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
	firstRaw := []byte(`{"request_id":"req-first","data":[{"url":"https://example.invalid/first.png"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	firstID := BeginZTAPIManagedImageAttempt(info)
	first, err := ValidateZTAPIManagedImageResponse(info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(firstRaw)), Header: make(http.Header)})
	require.Nil(t, err)
	evidence, normalizeErr := relaycommon.NormalizeZTAPIImageUsageCandidate(info, first, firstRaw)
	require.NoError(t, normalizeErr)
	require.True(t, info.PromoteZTAPIValidatedImageResponse(firstRaw, evidence))
	publishedResponse, _ := info.GetZTAPIImageSettlementEvidence()
	require.Equal(t, first, publishedResponse)
	require.Equal(t, "req-first", publishedResponse.UpstreamRequestID)

	secondID := BeginZTAPIManagedImageAttempt(info)
	require.NotEqual(t, firstID, secondID)
	publishedResponse, _ = info.GetZTAPIImageSettlementEvidence()
	require.Nil(t, publishedResponse, "a retry must clear the previous billing handoff before upstream work")
	secondRaw := []byte(`{"request_id":"req-second","data":[{"url":"https://example.invalid/second.png"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"error":{"type":"provider_error","message":"adaptor rejects this response"}}`)
	second, validationErr := ValidateZTAPIManagedImageResponse(info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(secondRaw)), Header: make(http.Header)})
	require.Nil(t, validationErr, "the frozen contract can validate before the adaptor rejects an error envelope")
	require.NotNil(t, second)
	AbortZTAPIManagedImageAttempt(info, secondID)
	publishedResponse, _ = info.GetZTAPIImageSettlementEvidence()
	require.Nil(t, publishedResponse)
}

func TestReadAndRestoreZTAPIManagedImageBodyIsBoundedAndExact(t *testing.T) {
	raw := []byte("0123456789")
	resp := &http.Response{Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw))}
	read, err := readAndRestoreZTAPIManagedImageBody(resp, 4)
	require.Nil(t, read)
	require.ErrorIs(t, err, ErrZTAPIManagedImageResponseTooLarge)
	require.Equal(t, http.NoBody, resp.Body)

	resp = &http.Response{Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw))}
	read, err = readAndRestoreZTAPIManagedImageBody(resp, int64(len(raw)))
	require.NoError(t, err)
	require.Equal(t, raw, read)
	restored, restoreErr := io.ReadAll(resp.Body)
	require.NoError(t, restoreErr)
	require.Equal(t, raw, restored)
}

type trackingZTAPIReadCloser struct {
	reader io.Reader
	closed atomic.Bool
}

func (body *trackingZTAPIReadCloser) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *trackingZTAPIReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}

type failingZTAPIReader struct{}

func (failingZTAPIReader) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }

func TestZTAPIImageResponseValidationClosesBodyOnEveryFailureFamily(t *testing.T) {
	for _, tt := range []struct {
		name string
		body io.Reader
	}{
		{name: "read error", body: failingZTAPIReader{}},
		{name: "over limit", body: io.LimitReader(&repeatingZTAPIReader{value: 'x'}, ztapiManagedImageResponseLimit+1)},
		{name: "duplicate schema", body: strings.NewReader(`{"request_id":"a","request_id":"b"}`)},
		{name: "malformed schema", body: strings.NewReader(`[]`)},
		{name: "result fault", body: strings.NewReader(`{"request_id":"a","data":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)},
		{name: "request id fault", body: strings.NewReader(`{"data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
			BeginZTAPIManagedImageAttempt(info)
			body := &trackingZTAPIReadCloser{reader: tt.body}
			resp := &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}
			candidate, apiErr := ValidateZTAPIManagedImageResponse(info, resp)
			require.Nil(t, candidate)
			require.NotNil(t, apiErr)
			require.True(t, body.closed.Load(), "original upstream body must close")
			require.Equal(t, http.NoBody, resp.Body, "failed validation must leave no body for downstream consumption")
		})
	}
}

type repeatingZTAPIReader struct{ value byte }

func (reader *repeatingZTAPIReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = reader.value
	}
	return len(buffer), nil
}

func TestZTAPIImageResponseValidationLeavesUnmanagedBodyUntouched(t *testing.T) {
	raw := bytes.Repeat([]byte("legacy-image-response"), 128)
	info := &relaycommon.RelayInfo{}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}
	candidate, err := ValidateZTAPIManagedImageResponse(info, resp)
	require.Nil(t, candidate)
	require.Nil(t, err)
	replayed, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.Equal(t, raw, replayed)
}

func TestZTAPIImageResponseValidationFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing result", body: `{"request_id":"req-1","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{name: "empty result", body: `{"request_id":"req-1","data":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{name: "missing result payload", body: `{"request_id":"req-1","data":[{}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{name: "ambiguous result payload", body: `{"request_id":"req-1","data":[{"url":"https://example.invalid/x","b64_json":"YWJj"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{name: "malformed result URL", body: `{"request_id":"req-1","data":[{"url":"not-a-url"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{name: "malformed result base64", body: `{"request_id":"req-1","data":[{"b64_json":"not-base64"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{name: "missing request id", body: `{"data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{name: "malformed request id", body: `{"request_id":7,"data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{name: "duplicate request id", body: `{"request_id":"req-1","request_id":"req-2","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
			BeginZTAPIManagedImageAttempt(info)
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(tt.body)), Header: make(http.Header)}
			handoff, err := ValidateZTAPIManagedImageResponse(info, resp)
			require.Nil(t, handoff)
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, err.StatusCode)
			require.True(t, types.IsSkipRetryError(err))
			publishedResponse, _ := info.GetZTAPIImageSettlementEvidence()
			require.Nil(t, publishedResponse)
			require.Zero(t, info.CurrentZTAPIImageResponseAttemptID(), "validation errors must clear the attempt marker")
		})
	}
}

func TestImageHelperManagedRejectionDoesNotCallUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	body := validManagedImageBody()
	body["size"] = "2048x2048"
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
	info.RelayMode = relayconstant.RelayModeImagesGenerations
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
	common.SetContextKey(c, constant.ContextKeyChannelKey, "test-only-key")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, info.OriginModelName)

	err := ImageHelper(c, info)
	require.NotNil(t, err)
	require.Zero(t, calls.Load())
}

func TestZTAPIManagedImageStreamingRemainsUnauthorized(t *testing.T) {
	body := validManagedImageBody()
	body["stream"] = true
	c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
	require.NotNil(t, AdmitZTAPIImageRequest(c, info))

	legacy, legacyInfo := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
	legacyInfo.ZTAPIPublicationSnapshot = nil
	require.Nil(t, AdmitZTAPIImageRequest(legacy, legacyInfo))
}
