package helper

import (
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
