package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageProtocolRequiresFrozenProviderFamily(t *testing.T) {
	sealed, _, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: 1, ProviderModel: "provider-image", EndpointType: types.ZTAPIImageEndpointGeneration,
		Method: http.MethodPost, Path: "/v1/images/generations",
		Capabilities:   types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 1},
		Response:       types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"}},
		Usage:          types.ZTAPIImageUsageContract{UsageField: "usage", Fields: map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"}, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Reservations:   []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 1, MaximumDimensions: map[string]string{"input_tokens": "200000", "output_tokens": "4096"}}},
		RequestIDField: "request_id", EvidenceVersion: 1,
	})
	require.NoError(t, err)

	for _, tt := range []struct {
		name        string
		channelType int
		allowed     bool
	}{
		{name: "OpenAI compatible channel", channelType: constant.ChannelTypeOpenAI, allowed: true},
		{name: "Azure channel is not the exact frozen family", channelType: constant.ChannelTypeAzure},
		{name: "Gemini channel cannot be guessed", channelType: constant.ChannelTypeGemini},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{
				PublicName: "zt-image", SourceModel: "provider-image", Modality: "image", ImageProtocolContract: &sealed,
			})
			err := validateZTAPIRequestProtocol(c, &model.Channel{Type: tt.channelType}, "zt-image")
			if tt.allowed {
				require.Nil(t, err)
			} else {
				require.NotNil(t, err)
				require.Equal(t, http.StatusBadRequest, err.StatusCode)
			}
		})
	}
}

func TestZTAPIImageProtocolAllowsFrozenGeminiChannel(t *testing.T) {
	sealed, _, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: 2, ProviderModel: "gemini-2.5-flash-image", EndpointType: types.ZTAPIImageEndpointGeneration,
		Method: http.MethodPost, Path: "/v1/images/generations",
		WireProtocol:    types.ZTAPIImageWireProtocolGeminiGenerateContent,
		ProviderPath:    "/v1beta/models/gemini-2.5-flash-image:generateContent",
		Capabilities:    types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{"b64_json"}, MinCount: 1, MaxCount: 1},
		Response:        types.ZTAPIImageResponseContract{Schema: types.ZTAPIImageResponseSchemaGeminiInlineImages, ResultsField: "candidates", ResultFields: map[string]string{"b64_json": "content.parts.inlineData.data"}},
		Usage:           types.ZTAPIImageUsageContract{UsageField: "usageMetadata", Fields: map[string]string{"input_tokens": "promptTokenCount", "output_tokens": "candidatesTokenCount"}, TotalField: "totalTokenCount", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Reservations:    []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "standard", ResponseFormat: "b64_json", N: 1, MaximumDimensions: map[string]string{"input_tokens": "300000", "output_tokens": "2000"}}},
		RequestIDSource: types.ZTAPIResponseIDSourceBodyField, RequestIDKey: "responseId", EvidenceVersion: types.ZTAPIImageEvidenceVersion,
		UpstreamRequestFields: map[string]string{
			"model": types.ZTAPIImageRequestFieldOmit, "prompt": types.ZTAPIImageRequestFieldRequired,
			"n": types.ZTAPIImageRequestFieldOmit, "size": types.ZTAPIImageRequestFieldOmit,
			"quality": types.ZTAPIImageRequestFieldOmit, "response_format": types.ZTAPIImageRequestFieldOmit,
		},
	})
	require.NoError(t, err)

	for _, tt := range []struct {
		name        string
		channelType int
		allowed     bool
	}{
		{name: "Gemini channel", channelType: constant.ChannelTypeGemini, allowed: true},
		{name: "OpenAI channel is not the frozen Gemini family", channelType: constant.ChannelTypeOpenAI},
		{name: "Azure channel is not the frozen Gemini family", channelType: constant.ChannelTypeAzure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{
				PublicName: "zt-gemini-2.5-flash-image", SourceModel: "gemini-2.5-flash-image", Modality: "image", ImageProtocolContract: &sealed,
			})
			err := validateZTAPIRequestProtocol(c, &model.Channel{Type: tt.channelType}, "zt-gemini-2.5-flash-image")
			if tt.allowed {
				require.Nil(t, err)
			} else {
				require.NotNil(t, err)
				require.Equal(t, http.StatusBadRequest, err.StatusCode)
			}
		})
	}
}

func TestZTAPIResponsesProtocolChannelSetup(t *testing.T) {
	for _, tt := range []struct {
		name, path, source, requested, pattern                             string
		published, enabled, wrongChannel, globalPass, channelPass, allowed bool
	}{
		{name: "reject default Chat", path: "/v1/chat/completions", published: true},
		{name: "reject playground Chat", path: "/pg/chat/completions", published: true},
		{name: "allow native Responses", path: "/v1/responses", published: true, allowed: true},
		{name: "allow explicit Chat conversion", path: "/v1/chat/completions", published: true, enabled: true, allowed: true},
		{name: "allow explicit Messages conversion", path: "/v1/messages", published: true, enabled: true, allowed: true},
		{name: "reject Messages without conversion", path: "/v1/messages", published: true},
		{name: "legacy completion has no converter", path: "/v1/completions", published: true, enabled: true},
		{name: "embeddings is not Responses", path: "/v1/embeddings", published: true, enabled: true},
		{name: "channel not authorized including retry", path: "/v1/chat/completions", published: true, enabled: true, wrongChannel: true},
		{name: "source regex does not authorize alias conversion", path: "/v1/chat/completions", published: true, enabled: true, pattern: `^gpt-5\.4-pro$`},
		{name: "invalid regex fails closed", path: "/v1/chat/completions", published: true, enabled: true, pattern: `[`},
		{name: "global passthrough disables conversion", path: "/v1/chat/completions", published: true, enabled: true, globalPass: true},
		{name: "channel passthrough disables conversion", path: "/v1/chat/completions", published: true, enabled: true, channelPass: true},
		{name: "source not alias determines restriction", path: "/v1/chat/completions", published: true, requested: "friendly-name"},
		{name: "misleading alias is not restricted", path: "/v1/chat/completions", published: true, source: "gpt-5.5", allowed: true},
		{name: "non ZTAPI unchanged", path: "/v1/chat/completions", allowed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := setupRelaySecurityFixture(t, "gpt-5.4-pro")
			settings := model_setting.GetGlobalSettings()
			old := *settings
			t.Cleanup(func() { *settings = old })
			channel := *fixture.channel
			channelID := channel.Id
			if tt.wrongChannel {
				channelID++
			}
			pattern := tt.pattern
			if pattern == "" {
				pattern = `^zt-gpt-5\.4-pro$`
			}
			settings.PassThroughRequestEnabled = tt.globalPass
			settings.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{Enabled: tt.enabled, ChannelIDs: []int{channelID}, ModelPatterns: []string{pattern}}
			if tt.channelPass {
				channel.Setting = common.GetPointer(`{"pass_through_body_enabled":true}`)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", tt.path, nil)
			source, requested := tt.source, tt.requested
			if source == "" {
				source = "gpt-5.4-pro"
			}
			if requested == "" {
				requested = "zt-gpt-5.4-pro"
			}
			if tt.published {
				relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{SourceModel: source, PublicName: requested})
			}
			err := SetupContextForSelectedChannel(c, &channel, requested)
			if tt.allowed {
				require.Nil(t, err)
			} else {
				require.NotNil(t, err)
				require.Equal(t, 400, err.StatusCode)
				if tt.path == "/v1/embeddings" {
					require.Contains(t, err.Error(), "modality")
				} else {
					require.Contains(t, err.Error(), "/v1/responses")
				}
				_, keySelected := c.Get(string(constant.ContextKeyChannelKey))
				require.False(t, keySelected, "reject before reading the channel key")
			}
		})
	}
}
