package helper

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierAdmissionFinalMappingGuard(t *testing.T) {
	for _, tc := range []struct {
		name      string
		channel   int
		policy    string
		mapping   map[string]string
		wantError bool
	}{
		{"exact", 2, string(model.ZTAPIPricePolicySupplierAdmission20261019), map[string]string{"gpt-6.1-sol": "gpt-6.1-sol-hc"}, false},
		{"chained_exact", 2, string(model.ZTAPIPricePolicySupplierAdmission20261019), map[string]string{"gpt-6.1-sol": "supplier-alias", "supplier-alias": "gpt-6.1-sol-hc"}, false},
		{"terminal_self_mapping", 2, string(model.ZTAPIPricePolicySupplierAdmission20261019), map[string]string{"gpt-6.1-sol": "gpt-6.1-sol-hc", "gpt-6.1-sol-hc": "gpt-6.1-sol-hc"}, false},
		{"chain_changes_sku", 2, string(model.ZTAPIPricePolicySupplierAdmission20261019), map[string]string{"gpt-6.1-sol": "gpt-6.1-sol-hc", "gpt-6.1-sol-hc": "gpt-6-sol-hc"}, true},
		{"enterprise", 1, string(model.ZTAPIPricePolicySupplierAdmission20261019), map[string]string{"gpt-6.1-sol": "gpt-6.1-sol-hc"}, true},
		{"self_mapping", 2, string(model.ZTAPIPricePolicySupplierAdmission20261019), map[string]string{"gpt-6.1-sol": "gpt-6.1-sol"}, true},
		{"missing_mapping", 2, string(model.ZTAPIPricePolicySupplierAdmission20261019), nil, true},
		{"legacy_unchanged", 1, "", map[string]string{"gpt-6.1-sol": "legacy-private-model"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			raw, err := common.Marshal(tc.mapping)
			require.NoError(t, err)
			c.Set("model_mapping", string(raw))
			info := &relaycommon.RelayInfo{
				OriginModelName: "gpt-6.1-sol",
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: tc.channel, UpstreamModelName: "gpt-6.1-sol"},
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
					SourceModel: "gpt-6.1-sol", PricePolicy: tc.policy,
				},
			}
			err = ModelMappedHelper(c, info, &dto.GeneralOpenAIRequest{})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSupplierAdmissionXcloudExactRoute(t *testing.T) {
	for _, tc := range []struct {
		source   string
		channel  int
		base     string
		physical string
		pass     bool
	}{
		{"claude-opus-5-5", 6, "https://www.xcloudtoken.com", "claude-opus-5-5", true},
		{"claude-sonnet-5-5", 6, "https://www.xcloudtoken.com", "claude-sonnet-5-5", true},
		{"gpt-6-astra", 6, "https://www.xcloudtoken.com", "gpt-6-astra", true},
		{"gpt-6-luna", 6, "https://www.xcloudtoken.com", "gpt-6-luna", true},
		{"gpt-6-sol", 6, "https://www.xcloudtoken.com", "gpt-6-sol", true},
		{"gpt-5.5", 7, "https://www.xcloudtoken.com", "gpt-5.5", true},
		{"gpt-5.6-sol", 7, "https://www.xcloudtoken.com", "gpt-5.6-sol", true},
		{"gpt-5.6-terra", 7, "https://www.xcloudtoken.com", "gpt-5.6-terra", true},
		{"gpt-6-sol", 7, "https://www.xcloudtoken.com", "gpt-6-sol", false},
		{"gpt-6-sol", 6, "https://other.example", "gpt-6-sol", false},
		{"gpt-6-sol", 6, "https://www.xcloudtoken.com", "gpt-6-astra", false},
		{"gpt-6.1-sol", 6, "https://www.xcloudtoken.com", "gpt-6.1-sol", false},
	} {
		t.Run(fmt.Sprintf("%s-%d-%s-%s", tc.source, tc.channel, tc.base, tc.physical), func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{
				OriginModelName:          tc.source,
				ChannelMeta:              &relaycommon.ChannelMeta{ChannelId: tc.channel, ChannelBaseUrl: tc.base, UpstreamModelName: tc.physical},
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: tc.source, PricePolicy: string(model.ZTAPIPricePolicySupplierAdmission20261019)},
			}
			err := ModelMappedHelper(c, info, &dto.GeneralOpenAIRequest{})
			if tc.pass {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestXcloudGLM53FinalRouteGuard(t *testing.T) {
	for _, tc := range []struct {
		name           string
		channel        int
		base, physical string
		pass           bool
	}{
		{"xcloud", 8, "https://www.xcloudtoken.com", "glm-5.3", true},
		{"wrong_channel", 2, "https://www.xcloudtoken.com", "glm-5.3", false},
		{"wrong_host", 8, "https://example.invalid", "glm-5.3", false},
		{"wrong_model", 8, "https://www.xcloudtoken.com", "glm-5.2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{
				OriginModelName:          "glm-5.3",
				ChannelMeta:              &relaycommon.ChannelMeta{ChannelId: tc.channel, ChannelBaseUrl: tc.base, UpstreamModelName: tc.physical},
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "glm-5.3", PricePolicy: string(model.ZTAPIPricePolicyXcloudGLM53)},
			}
			err := ModelMappedHelper(c, info, &dto.GeneralOpenAIRequest{})
			if tc.pass {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
