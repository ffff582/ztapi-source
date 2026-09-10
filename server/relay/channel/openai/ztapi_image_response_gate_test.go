package openai

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The provider evidence redacts the image bytes and does not name the header.
// These tests use synthetic base64/header/cache paths, never provider authority.
const observedGPTImageUsage = `{"input_tokens":18,"input_tokens_details":{"image_tokens":0,"text_tokens":18},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":214}`

func gptImageEvidenceInfo(t *testing.T, body []byte) *relaycommon.RelayInfo {
	t.Helper()
	info := newOpenAIManagedImageResponseInfo(t, body)
	contract := info.ZTAPIPublicationSnapshot.ImageProtocolContract.Clone()
	contract.Version, contract.ProviderModel, contract.RequestIDField = 2, "gpt-image-2", ""
	contract.RequestIDSource, contract.RequestIDKey = "header", "X-Synthetic-Request-ID"
	contract.UpstreamRequestFields = map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "optional", "response_format": "omit"}
	contract.Capabilities.ResponseFormats = []string{"b64_json"}
	contract.Response.ResultFields = map[string]string{"b64_json": "b64_json"}
	contract.Reservations[0].ResponseFormat = "b64_json"
	contract.Usage.Fields = map[string]string{
		"text_input": "input_tokens_details.text_tokens", "image_input": "input_tokens_details.image_tokens",
		"text_cached_input": "synthetic_text_cache", "image_cached_input": "synthetic_image_cache",
		"image_output": "output_tokens_details.image_tokens",
	}
	contract.Usage.CacheSemantics = "separate_dimension"
	contract.Reservations[0].MaximumDimensions = map[string]string{}
	rules := []types.ZTAPIMediaPriceRule{}
	for _, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		contract.Reservations[0].MaximumDimensions[dimension] = "200000"
		rules = append(rules, types.ZTAPIMediaPriceRule{ID: dimension, Conditions: map[string]string{"token_bucket": dimension}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens, CostUSD: map[string]string{dimension: "0.6"}, SaleUSD: map[string]string{dimension: "1"}, SourceCells: map[string]string{dimension: "A1"}})
	}
	sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	info.ZTAPIPublicationSnapshot.ImageProtocolContract = &sealed
	rawPrice, err := basecommon.Marshal(types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules})
	require.NoError(t, err)
	info.ZTAPIPublicationSnapshot.MediaPriceContractJSON, err = types.CanonicalizeZTAPIMediaPriceContract(string(rawPrice))
	require.NoError(t, err)
	attempt := info.BeginZTAPIImageResponseAttempt()
	require.True(t, info.RecordZTAPIImageResponseValidation(attempt, &relaycommon.ZTAPIValidatedImageResponse{
		ContractVersion: sealed.Version, EvidenceHash: sealed.EvidenceHash, UpstreamRequestID: "synthetic-header-id", ResultCount: 1,
		RawResponse: body, RawUsageJSON: []byte(gjson.GetBytes(body, "usage").Raw),
	}))
	return info
}

func TestOpenaiGPTImageObservedUsageAndContradictions(t *testing.T) {
	complete := strings.TrimSuffix(observedGPTImageUsage, "}") + `,"synthetic_text_cache":0,"synthetic_image_cache":0}`
	nonzeroCache := `{"input_tokens":28,"input_tokens_details":{"image_tokens":5,"text_tokens":8},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":224,"synthetic_text_cache":10,"synthetic_image_cache":5}`
	for _, tt := range []struct {
		name, usage string
		pending     bool
	}{
		{"observed missing caches", observedGPTImageUsage, true},
		{"synthetic explicit zero caches", complete, false},
		{"nonzero five bucket decomposition", nonzeroCache, false},
		{"aggregate omits caches", strings.Replace(nonzeroCache, `"input_tokens":28`, `"input_tokens":13`, 1), true},
		{"aggregate double counts caches", strings.Replace(nonzeroCache, `"input_tokens":28`, `"input_tokens":43`, 1), true},
		{"missing output text", strings.Replace(complete, `,"text_tokens":0`, "", 1), true},
		{"nonzero output text", strings.Replace(complete, `"text_tokens":0`, `"text_tokens":1`, 1), true},
		{"wrong input total", strings.Replace(complete, `"input_tokens":18`, `"input_tokens":19`, 1), true},
		{"wrong output total", strings.Replace(complete, `"output_tokens":196`, `"output_tokens":197`, 1), true},
		{"missing input total", strings.Replace(complete, `"input_tokens":18,`, "", 1), true},
		{"negative output text", strings.Replace(complete, `"text_tokens":0`, `"text_tokens":-1`, 1), true},
		{"string output text", strings.Replace(complete, `"text_tokens":0`, `"text_tokens":"0"`, 1), true},
		{"exponent output text", strings.Replace(complete, `"text_tokens":0`, `"text_tokens":0e0`, 1), true},
		{"total conflict", strings.Replace(complete, `"total_tokens":214`, `"total_tokens":215`, 1), true},
		{"duplicate cache", strings.TrimSuffix(complete, "}") + `,"synthetic_text_cache":1}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"data":[{"b64_json":"c3ludGhldGljLWltYWdl"}],"usage":` + tt.usage + `}`)
			info := gptImageEvidenceInfo(t, body)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			_, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))})
			require.Nil(t, apiErr)
			require.Equal(t, body, recorder.Body.Bytes())
			evidence := info.GetZTAPIMediaUsageEvidence()
			require.NotNil(t, evidence)
			require.Equal(t, tt.pending, evidence.Pending)
			require.Equal(t, tt.usage, string(evidence.GetRawUsageJSON()))
			require.Equal(t, "synthetic-header-id", evidence.UpstreamRequestID)
			if tt.pending {
				require.Empty(t, evidence.GetDimensions())
			} else {
				require.Len(t, evidence.GetDimensions(), 5)
			}
		})
	}
}

func newOpenAIManagedImageResponseInfo(t *testing.T, body []byte) *relaycommon.RelayInfo {
	t.Helper()
	protocol, _, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersion, ProviderModel: "provider-image", EndpointType: types.ZTAPIImageEndpointGeneration,
		Method: http.MethodPost, Path: "/v1/images/generations",
		Capabilities:   types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 1},
		Response:       types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"}},
		Usage:          types.ZTAPIImageUsageContract{UsageField: "usage", Fields: map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"}, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Reservations:   []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 1, MaximumDimensions: map[string]string{"input_tokens": "200000", "output_tokens": "4096"}}},
		RequestIDField: "request_id", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	})
	require.NoError(t, err)
	rules := make([]types.ZTAPIMediaPriceRule, 0, 2)
	for index, tier := range []string{"gt_200k", "lte_200k"} {
		rules = append(rules, types.ZTAPIMediaPriceRule{
			ID: tier, Conditions: map[string]string{"prompt_tokens_tier": tier}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD: map[string]string{"input_tokens": "0.6", "output_tokens": "0.6"}, SaleUSD: map[string]string{"input_tokens": "1", "output_tokens": "1"},
			SourceCells: map[string]string{"input_tokens": fmt.Sprintf("A%d", index+1), "output_tokens": fmt.Sprintf("B%d", index+1)},
		})
	}
	rawPrice, err := basecommon.Marshal(types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules})
	require.NoError(t, err)
	mediaPrice, err := types.CanonicalizeZTAPIMediaPriceContract(string(rawPrice))
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			Modality: "image", MediaPriceContractJSON: mediaPrice, ImageProtocolContract: &protocol,
		},
	}
	attemptID := info.BeginZTAPIImageResponseAttempt()
	var rawUsage []byte
	if usage := gjson.GetBytes(body, "usage"); usage.Exists() {
		rawUsage = []byte(usage.Raw)
	}
	require.True(t, info.RecordZTAPIImageResponseValidation(attemptID, &relaycommon.ZTAPIValidatedImageResponse{
		ContractVersion: protocol.Version, EvidenceHash: protocol.EvidenceHash, UpstreamRequestID: "current", ResultCount: 1,
		RawResponse: body, RawUsageJSON: rawUsage,
	}))
	return info
}

func publishedZTAPIImageResponse(info *relaycommon.RelayInfo) *relaycommon.ZTAPIValidatedImageResponse {
	response, _ := info.GetZTAPIImageSettlementEvidence()
	return response
}

func TestOpenaiImageHandlerPublishesTrustedAndPendingUsageEvidence(t *testing.T) {
	for _, tt := range []struct {
		name        string
		body        []byte
		wantPending bool
		wantRaw     []byte
	}{
		{name: "trusted", body: []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":10,"output_tokens":7,"total_tokens":17}}`), wantRaw: []byte(`{"input_tokens":10,"output_tokens":7,"total_tokens":17}`)},
		{name: "missing", body: []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}]}`), wantPending: true},
		{name: "exponent", body: []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1e3,"output_tokens":7,"total_tokens":1007}}`), wantPending: true, wantRaw: []byte(`{"input_tokens":1e3,"output_tokens":7,"total_tokens":1007}`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			info := newOpenAIManagedImageResponseInfo(t, tt.body)

			usage, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(tt.body))})
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			require.Zero(t, usage.TotalTokens)
			require.Equal(t, tt.body, recorder.Body.Bytes())
			require.NotNil(t, publishedZTAPIImageResponse(info))
			evidence := info.GetZTAPIMediaUsageEvidence()
			require.NotNil(t, evidence)
			require.Equal(t, tt.wantPending, evidence.Pending)
			require.Equal(t, tt.wantRaw, evidence.GetRawUsageJSON())
			if tt.wantPending {
				require.NotEmpty(t, evidence.Reason)
				require.Empty(t, evidence.GetDimensions())
			} else {
				require.Empty(t, evidence.Reason)
				require.NotEmpty(t, evidence.GetDimensions())
			}
		})
	}
}

func TestOpenaiImageHandlerHardNormalizeErrorIs502BeforeClientWrite(t *testing.T) {
	body := []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":10,"output_tokens":7,"total_tokens":17}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	info := newOpenAIManagedImageResponseInfo(t, body)
	info.ZTAPIPublicationSnapshot.MediaPriceContractJSON = ""

	usage, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))})
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.Zero(t, recorder.Body.Len())
	require.Nil(t, publishedZTAPIImageResponse(info))
	require.Nil(t, info.GetZTAPIMediaUsageEvidence())
	require.Zero(t, info.CurrentZTAPIImageResponseAttemptID())
}

type trackingOpenAIImageBody struct {
	reader io.Reader
	closed atomic.Bool
}

func (body *trackingOpenAIImageBody) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *trackingOpenAIImageBody) Close() error {
	body.closed.Store(true)
	return nil
}

func TestOpenaiImageHandlerRejectsManagedResponseWithoutValidatedHandoff(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	info := &relaycommon.RelayInfo{
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "image"},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(`{"data":[{"url":"https://example.invalid/x"}],"usage":{"total_tokens":1}}`)),
	}
	usage, err := OpenaiImageHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, err)
	require.True(t, types.IsSkipRetryError(err))
	require.Zero(t, recorder.Body.Len(), "unvalidated managed response must not reach the customer")
}

func TestOpenaiImageHandlerRejectsManagedResponseWithStaleHandoffButNoCurrentMarker(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	info := &relaycommon.RelayInfo{
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "image"},
	}
	body := []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}],"usage":{"total_tokens":1}}`)
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
	usage, err := OpenaiImageHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, err)
	require.True(t, types.IsSkipRetryError(err))
	require.Zero(t, recorder.Body.Len())
}

func TestOpenaiImageHandlerBindsMarkerToExactBodyAndMarksOnlySuccessfulAdaptor(t *testing.T) {
	validBody := []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}],"usage":{"total_tokens":1}}`)
	for _, tt := range []struct {
		name           string
		markerBody     []byte
		body           []byte
		wantError      bool
		wantSucceeded  bool
		changeEvidence bool
	}{
		{name: "different body", body: []byte(`{"request_id":"other","data":[{"url":"https://example.invalid/x"}],"usage":{"total_tokens":1}}`), wantError: true},
		{name: "adaptor error envelope", markerBody: []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}],"usage":{"total_tokens":1},"error":{"type":"provider_error","message":"failed"}}`), body: []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}],"usage":{"total_tokens":1},"error":{"type":"provider_error","message":"failed"}}`), wantError: true},
		{name: "changed evidence", body: validBody, wantError: true, changeEvidence: true},
		{name: "successful body", body: validBody, wantSucceeded: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			var info *relaycommon.RelayInfo
			markerBody := tt.markerBody
			if markerBody == nil {
				markerBody = validBody
			}
			if tt.wantSucceeded {
				info = newOpenAIManagedImageResponseInfo(t, markerBody)
			} else {
				info = &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "image", ImageProtocolContract: &types.ZTAPIImageProtocolContract{Version: 1, EvidenceHash: "evidence"}}}
				attemptID := info.BeginZTAPIImageResponseAttempt()
				require.True(t, info.RecordZTAPIImageResponseValidation(attemptID, &relaycommon.ZTAPIValidatedImageResponse{ContractVersion: 1, EvidenceHash: "evidence", RawResponse: markerBody}))
			}
			if tt.changeEvidence {
				info.ZTAPIPublicationSnapshot.ImageProtocolContract.EvidenceHash = "different"
			}
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(tt.body))}
			usage, err := OpenaiImageHandler(c, info, resp)
			if tt.wantError {
				require.Nil(t, usage)
				require.NotNil(t, err)
				require.Zero(t, info.CurrentZTAPIImageResponseAttemptID())
			} else {
				require.NotNil(t, usage)
				require.Nil(t, err)
			}
			require.Equal(t, tt.wantSucceeded, publishedZTAPIImageResponse(info) != nil)
			if tt.wantError {
				require.Zero(t, recorder.Body.Len(), "failed promotion or parsing must not write a client response")
			}
		})
	}
}

func TestOpenaiImageHandlerPromotionFailureIs502SkipRetryBeforeClientWrite(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	body := []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	info := &relaycommon.RelayInfo{
		ChannelMeta:              &relaycommon.ChannelMeta{},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "image", ImageProtocolContract: &types.ZTAPIImageProtocolContract{Version: 1, EvidenceHash: "evidence"}},
	}
	attemptID := info.BeginZTAPIImageResponseAttempt()
	require.True(t, info.RecordZTAPIImageResponseValidation(attemptID, &relaycommon.ZTAPIValidatedImageResponse{
		ContractVersion: 1,
		EvidenceHash:    "evidence",
		RawResponse:     body,
	}))
	info.ZTAPIPublicationSnapshot.ImageProtocolContract.EvidenceHash = "changed-before-promotion"

	usage, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))})
	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.Zero(t, recorder.Body.Len())
	require.Nil(t, publishedZTAPIImageResponse(info))
	require.Zero(t, info.CurrentZTAPIImageResponseAttemptID())
}

func TestOpenaiImageHandlerFailedRetryClearsPreviousHandoffAndCurrentMarker(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	previousBody := []byte(`{"request_id":"current","data":[{"url":"https://example.invalid/previous"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	info := newOpenAIManagedImageResponseInfo(t, previousBody)
	previousUsage, previousErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(previousBody))})
	require.Nil(t, previousErr)
	require.NotNil(t, previousUsage)
	require.NotNil(t, publishedZTAPIImageResponse(info))
	errorBody := []byte(`{"request_id":"retry","data":[{"url":"https://example.invalid/x"}],"usage":{"total_tokens":1},"error":{"type":"provider_error","message":"failed"}}`)
	attemptID := info.BeginZTAPIImageResponseAttempt()
	require.Nil(t, publishedZTAPIImageResponse(info))
	require.True(t, info.RecordZTAPIImageResponseValidation(attemptID, &relaycommon.ZTAPIValidatedImageResponse{
		ContractVersion: info.ZTAPIPublicationSnapshot.ImageProtocolContract.Version,
		EvidenceHash:    info.ZTAPIPublicationSnapshot.ImageProtocolContract.EvidenceHash,
		RawResponse:     errorBody,
	}))
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(errorBody))}
	usage, err := OpenaiImageHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, err)
	require.Nil(t, publishedZTAPIImageResponse(info))
	require.Zero(t, info.CurrentZTAPIImageResponseAttemptID())
}

func TestOpenaiImageHandlerClosesSuccessfulBodyAndClearsEvidenceOnParseFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		body []byte
	}{
		{name: "successful response", body: []byte(`{"request_id":"ok","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)},
		{name: "malformed json", body: []byte(`{"request_id":`)},
		{name: "error envelope", body: []byte(`{"error":{"type":"provider_error","message":"failed"}}`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			var info *relaycommon.RelayInfo
			if tt.name == "successful response" {
				info = newOpenAIManagedImageResponseInfo(t, tt.body)
			} else {
				info = &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "image", ImageProtocolContract: &types.ZTAPIImageProtocolContract{Version: 1, EvidenceHash: "evidence"}}}
				attemptID := info.BeginZTAPIImageResponseAttempt()
				require.True(t, info.RecordZTAPIImageResponseValidation(attemptID, &relaycommon.ZTAPIValidatedImageResponse{ContractVersion: 1, EvidenceHash: "evidence", RawResponse: tt.body}))
			}
			body := &trackingOpenAIImageBody{reader: bytes.NewReader(tt.body)}
			usage, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body})
			require.True(t, body.closed.Load())
			if tt.name == "successful response" {
				require.Nil(t, apiErr)
				require.NotNil(t, usage)
				require.NotNil(t, publishedZTAPIImageResponse(info))
				require.NotEmpty(t, recorder.Body.Bytes())
				return
			}
			require.NotNil(t, apiErr)
			require.Nil(t, usage)
			require.Nil(t, publishedZTAPIImageResponse(info))
			require.Zero(t, info.CurrentZTAPIImageResponseAttemptID())
			require.Zero(t, recorder.Body.Len())
		})
	}
}
