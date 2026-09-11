package relay

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

type gptImageTestTransport func(*http.Request) (*http.Response, error)

func (transport gptImageTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func configureGPTImageFixture(t *testing.T, info *relaycommon.RelayInfo) {
	t.Helper()
	contract := info.ZTAPIPublicationSnapshot.ImageProtocolContract.Clone()
	contract.ProviderModel = "gpt-image-2"
	contract.Capabilities = types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"low"}, ResponseFormats: []string{"b64_json"}, MinCount: 1, MaxCount: 1}
	contract.Response.ResultFields = map[string]string{"b64_json": "b64_json"}
	contract.Usage.Fields = map[string]string{"text_input": "input_tokens_details.text_tokens", "image_input": "input_tokens_details.image_tokens", "image_output": "output_tokens_details.image_tokens"}
	contract.Usage.CacheSemantics = "not_reported"
	maximum := map[string]string{"text_input": "200000", "image_input": "0", "image_output": "196"}
	rules := []types.ZTAPIMediaPriceRule{}
	for _, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		rules = append(rules, types.ZTAPIMediaPriceRule{ID: dimension, Conditions: map[string]string{"token_bucket": dimension}, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens, CostUSD: map[string]string{dimension: "0.6"}, SaleUSD: map[string]string{dimension: "1"}, SourceCells: map[string]string{dimension: "A1"}})
	}
	contract.Reservations = []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "low", ResponseFormat: "b64_json", N: 1, MaximumDimensions: maximum}}
	info.ZTAPIPublicationSnapshot.SourceModel = "gpt-image-2"
	info.ZTAPIPublicationSnapshot.ImageProtocolContract = &contract
	freezeImageRequestPolicy(t, info, map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "required", "response_format": "omit"})
	raw, err := common.Marshal(types.ZTAPIMediaPriceContract{Version: 1, Modality: "image", Rules: rules})
	require.NoError(t, err)
	info.ZTAPIPublicationSnapshot.MediaPriceContractJSON, err = types.CanonicalizeZTAPIMediaPriceContract(string(raw))
	require.NoError(t, err)
}

func TestZTAPIGPTImagePublicFormatRemainsExplicit(t *testing.T) {
	for _, format := range []string{"missing", "url", "", "b64_json"} {
		t.Run(format, func(t *testing.T) {
			body := validManagedImageBody()
			body["quality"], body["response_format"] = "low", format
			if format == "missing" {
				delete(body, "response_format")
			}
			c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
			configureGPTImageFixture(t, info)
			apiErr := AdmitZTAPIImageRequest(c, info)
			if format == "b64_json" {
				require.Nil(t, apiErr)
			} else {
				require.NotNil(t, apiErr)
			}
		})
	}
}

func TestZTAPIGPTImageFrozenDispatchValidatesDeliveredResponse(t *testing.T) {
	const observed = `{"data":[{"b64_json":"c3ludGhldGljLWltYWdl"}],"usage":{"input_tokens":18,"input_tokens_details":{"image_tokens":0,"text_tokens":18},"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":214}}`
	for _, name := range []string{"pending delivery", "missing header", "blank header", "malformed image", "wrong result count"} {
		t.Run(name, func(t *testing.T) {
			body := validManagedImageBody()
			body["quality"], body["response_format"] = "low", "b64_json"
			c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
			configureGPTImageFixture(t, info)
			info.RelayMode = relayconstant.RelayModeImagesGenerations
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://synthetic.invalid")
			common.SetContextKey(c, constant.ContextKeyChannelKey, "synthetic-test-key")
			common.SetContextKey(c, constant.ContextKeyOriginalModel, info.OriginModelName)
			responseBody := observed
			if name == "malformed image" {
				responseBody = strings.Replace(observed, "c3ludGhldGljLWltYWdl", "not-base64!", 1)
			}
			if name == "wrong result count" {
				responseBody = strings.Replace(observed, `[{"b64_json":"c3ludGhldGljLWltYWdl"}]`, `[]`, 1)
			}
			service.InitHttpClient()
			client := service.GetHttpClient()
			original := client.Transport
			t.Cleanup(func() { client.Transport = original })
			calls := 0
			client.Transport = gptImageTestTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				raw, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				require.JSONEq(t, `{"model":"gpt-image-2","prompt":"a neutral test image","n":1,"size":"1024x1024","quality":"low"}`, string(raw))
				headers := http.Header{"Content-Type": {"application/json"}, "x-synthetic-request-id": {"synthetic-header-id"}}
				if name == "missing header" {
					delete(headers, "x-synthetic-request-id")
				}
				if name == "blank header" {
					headers["x-synthetic-request-id"] = []string{" \t"}
				}
				return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(responseBody))}, nil
			})
			apiErr := ImageHelper(c, info)
			require.Equal(t, 1, calls)
			recorder := c.MustGet("ztapi_image_test_recorder").(*httptest.ResponseRecorder)
			if name != "pending delivery" {
				require.NotNil(t, apiErr)
				require.Zero(t, recorder.Body.Len())
				require.Nil(t, info.GetZTAPIMediaUsageEvidence())
				return
			}
			require.Nil(t, apiErr)
			require.Equal(t, observed, recorder.Body.String())
			evidence := info.GetZTAPIMediaUsageEvidence()
			require.NotNil(t, evidence)
			require.False(t, evidence.Pending)
			require.Equal(t, "synthetic-header-id", evidence.UpstreamRequestID)
			dimensions := evidence.GetDimensions()
			require.Equal(t, "18", dimensions["text_input"].String())
			require.Equal(t, "0", dimensions["image_input"].String())
			require.Equal(t, "196", dimensions["image_output"].String())
		})
	}
}

func freezeImageRequestPolicy(t *testing.T, info *relaycommon.RelayInfo, policy map[string]string) {
	t.Helper()
	contract := info.ZTAPIPublicationSnapshot.ImageProtocolContract.Clone()
	contract.Version = types.ZTAPIImageProtocolContractVersionV2
	contract.RequestIDField = ""
	contract.RequestIDSource, contract.RequestIDKey = types.ZTAPIResponseIDSourceHeader, "X-Synthetic-Request-ID"
	raw, err := common.Marshal(contract)
	require.NoError(t, err)
	var object map[string]any
	require.NoError(t, common.Unmarshal(raw, &object))
	if policy != nil {
		object["upstream_request_fields"] = policy
	}
	raw, err = common.Marshal(object)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, &contract))
	sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	info.ZTAPIPublicationSnapshot.ImageProtocolContract = &sealed
}

func TestZTAPIGPTImageDispatchOmitsOnlyFrozenUpstreamFields(t *testing.T) {
	for _, omit := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing serialization", true: "provider omission"}[omit], func(t *testing.T) {
			body := validManagedImageBody()
			body["response_format"] = "b64_json"
			c, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", body)
			info.ChannelMeta = &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, ApiType: constant.APITypeOpenAI}
			if omit {
				freezeImageRequestPolicy(t, info, map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "optional", "response_format": "omit"})
			}
			dispatch, apiErr := PrepareZTAPIManagedImageDispatch(c, info)
			require.Nil(t, apiErr)
			var upstream map[string]any
			require.NoError(t, common.Unmarshal(dispatch.Body, &upstream))
			if omit {
				require.NotContains(t, upstream, "response_format")
			} else {
				require.Equal(t, "b64_json", upstream["response_format"])
			}
			require.Equal(t, "standard", upstream["quality"], "optional upstream fields retain explicit values")
			require.Equal(t, "b64_json", info.Request.(*dto.ImageRequest).ResponseFormat, "the admitted format must survive serialization")
		})
	}
}

func TestZTAPIImageResponseV2ConfiguredIDBeforeOutput(t *testing.T) {
	for _, tt := range []struct {
		name, source, key, body string
		headers                 http.Header
		valid                   bool
	}{
		{"header lowercase", "header", "X-Synthetic-Request-ID", `{"data":[{"url":"https://example.invalid/image"}]}`, http.Header{"x-synthetic-request-id": {"header-id"}}, true},
		{"header mixed case", "header", "x-synthetic-request-id", `{"data":[{"url":"https://example.invalid/image"}]}`, http.Header{"X-SyNtHeTiC-ReQuEsT-ID": {"header-id"}}, true},
		{"missing header no body fallback", "header", "X-Synthetic-Request-ID", `{"request_id":"body-id","data":[{"url":"https://example.invalid/image"}]}`, nil, false},
		{"blank header", "header", "X-Synthetic-Request-ID", `{"data":[{"url":"https://example.invalid/image"}]}`, http.Header{"X-Synthetic-Request-ID": {" \t"}}, false},
		{"multiple header values", "header", "X-Synthetic-Request-ID", `{"data":[{"url":"https://example.invalid/image"}]}`, http.Header{"X-Synthetic-Request-ID": {"first", "second"}}, false},
		{"conflicting header casing", "header", "X-Synthetic-Request-ID", `{"data":[{"url":"https://example.invalid/image"}]}`, http.Header{"X-Synthetic-Request-ID": {"first"}, "x-synthetic-request-id": {"second"}}, false},
		{"v2 body path", "body_field", "meta.id", `{"meta":{"id":"body-id"},"data":[{"url":"https://example.invalid/image"}]}`, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, info := managedImageFixture(t, http.MethodPost, "/v1/images/generations", validManagedImageBody())
			contract := info.ZTAPIPublicationSnapshot.ImageProtocolContract.Clone()
			contract.Version, contract.RequestIDField = 2, ""
			contract.RequestIDSource, contract.RequestIDKey = tt.source, tt.key
			sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
			require.NoError(t, err)
			info.ZTAPIPublicationSnapshot.ImageProtocolContract = &sealed
			BeginZTAPIManagedImageAttempt(info)
			resp := &http.Response{StatusCode: 200, Header: tt.headers, Body: io.NopCloser(bytes.NewBufferString(tt.body))}
			candidate, apiErr := ValidateZTAPIManagedImageResponse(info, resp)
			if tt.valid {
				require.Nil(t, apiErr)
				require.Equal(t, map[string]string{"header": "header-id", "body_field": "body-id"}[tt.source], candidate.UpstreamRequestID)
			} else {
				require.NotNil(t, apiErr)
				require.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
				require.Nil(t, candidate)
				require.Zero(t, info.CurrentZTAPIImageResponseAttemptID())
				require.Equal(t, http.NoBody, resp.Body)
			}
			published, evidence := info.GetZTAPIImageSettlementEvidence()
			require.Nil(t, published)
			require.Nil(t, evidence)
		})
	}
}
