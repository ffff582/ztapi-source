package gemini_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay"
	gemini "github.com/QuantumNous/new-api/relay/channel/gemini"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	geminiImagePublicModel   = "zt-gemini-2.5-flash-image"
	geminiImageProviderModel = "gemini-2.5-flash-image"
	geminiImageProviderPath  = "/v1beta/models/gemini-2.5-flash-image:generateContent"
)

var (
	syntheticPNGOnce   sync.Once
	syntheticPNGBase64 string
)

func geminiManagedImageProtocol(t *testing.T, idSource, idKey string) *types.ZTAPIImageProtocolContract {
	t.Helper()
	contract := types.ZTAPIImageProtocolContract{
		Version:       types.ZTAPIImageProtocolContractVersionV2,
		ProviderModel: geminiImageProviderModel,
		EndpointType:  types.ZTAPIImageEndpointGeneration,
		Method:        http.MethodPost,
		Path:          "/v1/images/generations",
		WireProtocol:  types.ZTAPIImageWireProtocolGeminiGenerateContent,
		ProviderPath:  geminiImageProviderPath,
		Capabilities: types.ZTAPIImageCapabilities{
			Sizes:           []string{"1024x1024"},
			Qualities:       []string{"standard"},
			ResponseFormats: []string{"b64_json"},
			MinCount:        1,
			MaxCount:        1,
		},
		Response: types.ZTAPIImageResponseContract{
			Schema:       types.ZTAPIImageResponseSchemaGeminiInlineImages,
			ResultsField: "candidates",
			ResultFields: map[string]string{"b64_json": "content.parts.inlineData.data"},
		},
		Usage: types.ZTAPIImageUsageContract{
			UsageField:     "usageMetadata",
			Fields:         map[string]string{"input_tokens": "promptTokenCount", "output_tokens": "candidatesTokenCount"},
			TotalField:     "totalTokenCount",
			TotalSemantics: "sum_of_dimensions",
			CacheSemantics: "not_reported",
		},
		Reservations: []types.ZTAPIImageReservationAuthority{{
			Size: "1024x1024", Quality: "standard", ResponseFormat: "b64_json", N: 1,
			MaximumDimensions: map[string]string{"input_tokens": "300000", "output_tokens": "2000"},
		}},
		RequestIDSource: idSource,
		RequestIDKey:    idKey,
		EvidenceVersion: types.ZTAPIImageEvidenceVersion,
		UpstreamRequestFields: map[string]string{
			"model": types.ZTAPIImageRequestFieldOmit, "prompt": types.ZTAPIImageRequestFieldRequired,
			"n": types.ZTAPIImageRequestFieldOmit, "size": types.ZTAPIImageRequestFieldOmit,
			"quality": types.ZTAPIImageRequestFieldOmit, "response_format": types.ZTAPIImageRequestFieldOmit,
		},
	}
	sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	return &sealed
}

func geminiManagedImagePrice(t *testing.T) string {
	t.Helper()
	rules := []types.ZTAPIMediaPriceRule{
		{ID: "gt_200k", Conditions: map[string]string{"prompt_tokens_tier": "gt_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.246", "output_tokens": "24.60"}, SaleUSD: map[string]string{"input_tokens": "0.41", "output_tokens": "41.00"},
			SourceCells: map[string]string{"input_tokens": "F68", "output_tokens": "J68"}},
		{ID: "lte_200k", Conditions: map[string]string{"prompt_tokens_tier": "lte_200k"}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.246", "output_tokens": "2.05"}, SaleUSD: map[string]string{"input_tokens": "0.41", "output_tokens": "3.4166666667"},
			SourceCells: map[string]string{"input_tokens": "F67", "output_tokens": "J67"}},
	}
	raw, err := common.Marshal(types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules})
	require.NoError(t, err)
	canonical, err := types.CanonicalizeZTAPIMediaPriceContract(string(raw))
	require.NoError(t, err)
	return canonical
}

func geminiManagedImageFixture(t *testing.T, protocol *types.ZTAPIImageProtocolContract) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	requestBody := []byte(`{"model":"zt-gemini-2.5-flash-image","prompt":"Generate a neutral blue circle.","n":1,"size":"1024x1024","quality":"standard","response_format":"b64_json"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(requestBody))
	c.Request.Header.Set("Content-Type", "application/json")
	request := &dto.ImageRequest{}
	require.NoError(t, common.Unmarshal(requestBody, request))
	info := &relaycommon.RelayInfo{
		OriginModelName: geminiImagePublicModel,
		RequestURLPath:  "/v1/images/generations",
		Request:         request,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeGemini,
			ApiType:        constant.APITypeGemini,
			ChannelBaseUrl: "https://ai.example.invalid/hub",
		},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: geminiImagePublicModel, SourceModel: geminiImageProviderModel, Modality: "image",
			MediaPriceContractJSON: geminiManagedImagePrice(t), ImageProtocolContract: protocol,
		},
	}
	return c, recorder, info
}

func geminiNativeImageResponse(responseID string, promptTokens int) []byte {
	image := validSyntheticPNGBase64()
	return []byte(fmt.Sprintf(`{
  "candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":%q}}],"role":"model"},"finishReason":"STOP"}],
  "responseId":%q,
  "usageMetadata":{"promptTokenCount":%d,"promptTokensDetails":[{"modality":"TEXT","tokenCount":%d}],"candidatesTokenCount":1290,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1290}],"totalTokenCount":%d}
}`, image, responseID, promptTokens, promptTokens, promptTokens+1290))
}

func validSyntheticPNGBase64() string {
	syntheticPNGOnce.Do(func() {
		bitmap := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
		bitmap.Set(0, 0, color.RGBA{B: 255, A: 255})
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, bitmap); err != nil {
			panic(err)
		}
		syntheticPNGBase64 = base64.StdEncoding.EncodeToString(encoded.Bytes())
	})
	return syntheticPNGBase64
}

func truncatedSyntheticPNGBase64() string {
	raw, err := base64.StdEncoding.DecodeString(validSyntheticPNGBase64())
	if err != nil || len(raw) <= 12 {
		panic("valid synthetic PNG fixture is unavailable")
	}
	return base64.StdEncoding.EncodeToString(raw[:len(raw)-12])
}

func validateAndHandleGeminiImage(t *testing.T, info *relaycommon.RelayInfo, c *gin.Context, raw []byte, headers http.Header) *types.NewAPIError {
	t.Helper()
	dispatch, dispatchErr := relay.PrepareZTAPIManagedImageDispatch(c, info)
	require.Nil(t, dispatchErr)
	require.NotNil(t, dispatch)
	relay.BeginZTAPIManagedImageAttempt(info)
	resp := &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(bytes.NewReader(raw))}
	_, validationErr := relay.ValidateZTAPIManagedImageResponse(info, resp)
	if validationErr != nil {
		return validationErr
	}
	_, handlerErr := (&gemini.Adaptor{}).DoResponse(c, resp, info)
	return handlerErr
}

func TestGeminiManagedImageDispatchUsesFrozenNativeGenerateContent(t *testing.T) {
	protocol := geminiManagedImageProtocol(t, types.ZTAPIResponseIDSourceBodyField, "responseId")
	c, _, info := geminiManagedImageFixture(t, protocol)

	dispatch, apiErr := relay.PrepareZTAPIManagedImageDispatch(c, info)
	require.Nil(t, apiErr)
	require.Equal(t, types.ZTAPIImageWireProtocolGeminiGenerateContent, dispatch.WireProtocol)
	require.Equal(t, geminiImageProviderPath, dispatch.ProviderPath)
	require.Equal(t, geminiImageProviderPath, info.RequestURLPath)

	var body map[string]any
	require.NoError(t, common.Unmarshal(dispatch.Body, &body))
	require.Equal(t, map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Generate a neutral blue circle."}}}},
		"generationConfig": map[string]any{"responseModalities": []any{"TEXT", "IMAGE"}},
	}, body)
	for _, forbidden := range []string{"model", "n", "size", "quality", "response_format", "instances", "parameters"} {
		require.NotContains(t, body, forbidden)
	}

	url, err := (&gemini.Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://ai.example.invalid/hub"+geminiImageProviderPath, url)
	require.NotContains(t, url, ":predict")

	dispatch.Body[0] = 'x'
	frozen := info.GetZTAPIManagedImageDispatch()
	require.NotNil(t, frozen)
	require.True(t, bytes.HasPrefix(frozen.Body, []byte("{")), "stored dispatch body must be an immutable copy")
}

func TestGeminiManagedImageDispatchUsesAIHubBearerAuthentication(t *testing.T) {
	protocol := geminiManagedImageProtocol(t, types.ZTAPIResponseIDSourceBodyField, "responseId")
	c, _, info := geminiManagedImageFixture(t, protocol)
	info.ApiKey = "synthetic-test-key"
	dispatch, apiErr := relay.PrepareZTAPIManagedImageDispatch(c, info)
	require.Nil(t, apiErr)
	require.NotNil(t, dispatch)

	headers := make(http.Header)
	require.NoError(t, (&gemini.Adaptor{}).SetupRequestHeader(c, &headers, info))
	require.Equal(t, "Bearer synthetic-test-key", headers.Get("Authorization"))
	require.Empty(t, headers.Get("x-goog-api-key"))

	legacyHeaders := make(http.Header)
	legacyInfo := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "legacy-test-key"}}
	require.NoError(t, (&gemini.Adaptor{}).SetupRequestHeader(c, &legacyHeaders, legacyInfo))
	require.Equal(t, "legacy-test-key", legacyHeaders.Get("x-goog-api-key"))
	require.Empty(t, legacyHeaders.Get("Authorization"))
}

func TestGeminiManagedImageContractRejectsUnquotedOrNonNativeBindings(t *testing.T) {
	base := geminiManagedImageProtocol(t, types.ZTAPIResponseIDSourceBodyField, "responseId")
	tests := []struct {
		name   string
		mutate func(*types.ZTAPIImageProtocolContract)
	}{
		{"different provider model", func(c *types.ZTAPIImageProtocolContract) { c.ProviderModel = "gemini-other-image" }},
		{"imagen wire", func(c *types.ZTAPIImageProtocolContract) { c.WireProtocol = types.ZTAPIImageWireProtocolOpenAIImages }},
		{"predict path", func(c *types.ZTAPIImageProtocolContract) {
			c.ProviderPath = "/v1beta/models/gemini-2.5-flash-image:predict"
		}},
		{"model in body", func(c *types.ZTAPIImageProtocolContract) {
			c.UpstreamRequestFields["model"] = types.ZTAPIImageRequestFieldRequired
		}},
		{"multiple images", func(c *types.ZTAPIImageProtocolContract) { c.Capabilities.MaxCount = 2 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contract := base.Clone()
			contract.EvidenceHash = ""
			tc.mutate(&contract)
			_, _, err := types.SealZTAPIImageProtocolContract(contract)
			require.Error(t, err)
		})
	}
}

func TestGeminiManagedImageResponseConvertsOnceAndSelectsFrozenTier(t *testing.T) {
	tests := []struct {
		name         string
		promptTokens int
		wantRule     string
	}{
		{"200K stays low tier", 200000, "lte_200k"},
		{"200K plus one uses high tier", 200001, "gt_200k"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			protocol := geminiManagedImageProtocol(t, types.ZTAPIResponseIDSourceBodyField, "responseId")
			c, recorder, info := geminiManagedImageFixture(t, protocol)
			raw := geminiNativeImageResponse("gemini-response-1", tc.promptTokens)

			require.Nil(t, validateAndHandleGeminiImage(t, info, c, raw, make(http.Header)))
			var customer dto.ImageResponse
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &customer))
			require.Len(t, customer.Data, 1)
			require.Equal(t, validSyntheticPNGBase64(), customer.Data[0].B64Json)
			require.NotContains(t, recorder.Body.String(), "candidates")
			require.NotContains(t, recorder.Body.String(), "usageMetadata")

			validated, evidence := info.GetZTAPIImageSettlementEvidence()
			require.NotNil(t, validated)
			require.NotNil(t, evidence)
			require.Equal(t, "gemini-response-1", evidence.UpstreamRequestID)
			require.False(t, evidence.Pending)
			require.Equal(t, tc.wantRule, evidence.SelectedRuleID)
			require.Equal(t, tc.wantRule, evidence.GetPriceRuleIDs()["input_tokens"])
			require.Equal(t, tc.wantRule, evidence.GetPriceRuleIDs()["output_tokens"])
		})
	}
}

func TestGeminiManagedImageResponseSupportsFrozenHeaderIDSource(t *testing.T) {
	protocol := geminiManagedImageProtocol(t, types.ZTAPIResponseIDSourceHeader, "X-AIHub-Request-ID")
	c, _, info := geminiManagedImageFixture(t, protocol)
	raw := geminiNativeImageResponse("body-id-is-not-authoritative", 13)
	headers := http.Header{"x-aihub-request-id": []string{"header-request-1"}}

	require.Nil(t, validateAndHandleGeminiImage(t, info, c, raw, headers))
	_, evidence := info.GetZTAPIImageSettlementEvidence()
	require.NotNil(t, evidence)
	require.Equal(t, "header-request-1", evidence.UpstreamRequestID)
}

func TestGeminiManagedImageResponseRejectsInvalidProviderResults(t *testing.T) {
	validImage := validSyntheticPNGBase64()
	validUsage := `"usageMetadata":{"promptTokenCount":13,"promptTokensDetails":[{"modality":"TEXT","tokenCount":13}],"candidatesTokenCount":1290,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1290}],"totalTokenCount":1303}`
	tests := []struct {
		name string
		raw  string
	}{
		{"empty candidates", `{"candidates":[],"responseId":"rid",` + validUsage + `}`},
		{"text only", `{"candidates":[{"content":{"parts":[{"text":"no image"}]},"finishReason":"STOP"}],"responseId":"rid",` + validUsage + `}`},
		{"bad MIME", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"text/plain","data":"` + validImage + `"}}]},"finishReason":"STOP"}],"responseId":"rid",` + validUsage + `}`},
		{"bad base64", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"%%%"}}]},"finishReason":"STOP"}],"responseId":"rid",` + validUsage + `}`},
		{"base64 is not PNG", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"c3ludGhldGljLXBuZw=="}}]},"finishReason":"STOP"}],"responseId":"rid",` + validUsage + `}`},
		{"truncated PNG", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + truncatedSyntheticPNGBase64() + `"}}]},"finishReason":"STOP"}],"responseId":"rid",` + validUsage + `}`},
		{"safety finish", `{"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}],"responseId":"rid",` + validUsage + `}`},
		{"blocked safety rating", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + validImage + `"}}]},"finishReason":"STOP","safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","probability":"LOW","blocked":true}]}],"responseId":"rid",` + validUsage + `}`},
		{"unknown finish", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + validImage + `"}}]},"finishReason":"FUTURE_REASON"}],"responseId":"rid",` + validUsage + `}`},
		{"wrong image count", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + validImage + `"}},{"inlineData":{"mimeType":"image/png","data":"` + validImage + `"}}]},"finishReason":"STOP"}],"responseId":"rid",` + validUsage + `}`},
		{"extra text candidate", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + validImage + `"}}]},"finishReason":"STOP"},{"content":{"parts":[{"text":"extra"}]},"finishReason":"STOP"}],"responseId":"rid",` + validUsage + `}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			protocol := geminiManagedImageProtocol(t, types.ZTAPIResponseIDSourceBodyField, "responseId")
			c, _, info := geminiManagedImageFixture(t, protocol)
			err := validateAndHandleGeminiImage(t, info, c, []byte(tc.raw), make(http.Header))
			require.NotNil(t, err)
			validated, evidence := info.GetZTAPIImageSettlementEvidence()
			require.Nil(t, validated)
			require.Nil(t, evidence)
		})
	}
}

func TestGeminiManagedImageIncompleteUsageDeliversResultButStaysPending(t *testing.T) {
	protocol := geminiManagedImageProtocol(t, types.ZTAPIResponseIDSourceBodyField, "responseId")
	c, recorder, info := geminiManagedImageFixture(t, protocol)
	raw := geminiNativeImageResponse("gemini-pending-1", 13)
	raw = []byte(strings.ReplaceAll(string(raw), `,"promptTokensDetails":[{"modality":"TEXT","tokenCount":13}]`, ""))

	require.Nil(t, validateAndHandleGeminiImage(t, info, c, raw, make(http.Header)))
	require.NotEmpty(t, recorder.Body.Bytes(), "a valid image remains deliverable")
	_, evidence := info.GetZTAPIImageSettlementEvidence()
	require.NotNil(t, evidence)
	require.True(t, evidence.Pending)
	require.NotEmpty(t, evidence.Reason)
	require.Contains(t, evidence.RawUsageJSON, `"promptTokenCount":13`)
	require.Empty(t, evidence.GetDimensions(), "untrusted usage must not become billable dimensions")
}
