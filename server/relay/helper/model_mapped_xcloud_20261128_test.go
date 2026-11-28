package helper

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestXcloud20261128MappedExactRoute(t *testing.T) {
	for _, tc := range []struct {
		name, source, origin, physical, base, mapping string
		channel                                       int
		allowed                                       []int
		pass                                          bool
	}{
		{name: "exact", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, pass: true},
		{name: "exact_trailing_slash", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, base: "https://www.xcloudtoken.com/", pass: true},
		{name: "public_alias_chain", source: "gpt-6.2-sol", origin: "zt-gpt-6.2-sol", channel: 7, allowed: []int{7},
			mapping: `{"zt-gpt-6.2-sol":"internal-alias","internal-alias":"gpt-6.2-sol"}`, pass: true},
		{name: "self_mapping", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, mapping: `{"gpt-6.2-sol":"gpt-6.2-sol"}`, pass: true},
		{name: "wrong_sku", source: "gpt-6.2-sol", physical: "gpt-6.1-sol", channel: 7, allowed: []int{7}},
		{name: "mapped_wrong_sku", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, mapping: `{"gpt-6.2-sol":"gpt-6.1-sol"}`},
		{name: "chained_wrong_sku", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, mapping: `{"gpt-6.2-sol":"private-alias","private-alias":"gpt-6.2-astra"}`},
		{name: "unknown_source", source: "gpt-6.2-sol-hc", channel: 7, allowed: []int{7}},
		{name: "wrong_group", source: "gpt-6.2-sol", channel: 6, allowed: []int{6}},
		{name: "unknown_channel", source: "gpt-6.2-sol", channel: 99, allowed: []int{99}},
		{name: "yunxin_forbidden", source: "gpt-6.2-sol", channel: 2, allowed: []int{2}, base: "https://ai.yunxinapi.com/hub"},
		{name: "wrong_host", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, base: "https://www.xcloudtoken.com.example.invalid"},
		{name: "wrong_path", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, base: "https://www.xcloudtoken.com/v1"},
		{name: "self_mapping_cannot_skip_guard", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, base: "https://example.invalid", mapping: `{"gpt-6.2-sol":"gpt-6.2-sol"}`},
		{name: "missing_frozen_route", source: "gpt-6.2-sol", channel: 7},
		{name: "different_frozen_group", source: "gpt-6-sol", channel: 7, allowed: []int{6}},
		{name: "multiple_frozen_routes", source: "gpt-6-sol", channel: 7, allowed: []int{6, 7}},
		{name: "duplicate_frozen_route", source: "gpt-6.2-sol", channel: 7, allowed: []int{7, 7}},
		{name: "claude_max", source: "claude-fable-5-2", channel: 4, allowed: []int{4}, pass: true},
		{name: "claude_new_vip", source: "claude-fable-5-2", channel: 6, allowed: []int{6}, pass: true},
		{name: "haiku_current_group", source: "claude-haiku-4-5-20251001", channel: 5, allowed: []int{5}, pass: true},
		{name: "haiku_removed_group", source: "claude-haiku-4-5-20251001", channel: 4, allowed: []int{4}},
		{name: "test_only_fable", source: "claude-fable-5", channel: 4, allowed: []int{4}},
		{name: "test_only_gemini", source: "gemini-4-neon", channel: 4, allowed: []int{4}},
		{name: "glm_flash_xcloud", source: model.ZTAPIGLM53FlashSource, channel: 8, allowed: []int{8}, pass: true},
		{name: "glm_flash_yunxin", source: model.ZTAPIGLM53FlashSource, channel: 1, allowed: []int{1}, base: "https://ai.yunxinapi.com/hub"},
		{name: "glm_flash_not_singleton", source: model.ZTAPIGLM53FlashSource, channel: 8, allowed: []int{1, 8}},
		{name: "glm_flash_wrong_physical", source: model.ZTAPIGLM53FlashSource, physical: "glm-5.3", channel: 8, allowed: []int{8}},
		{name: "mapping_cycle", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, mapping: `{"gpt-6.2-sol":"alias","alias":"gpt-6.2-sol"}`},
		{name: "malformed_mapping", source: "gpt-6.2-sol", channel: 7, allowed: []int{7}, mapping: `{"gpt-6.2-sol":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, physical, base := tc.origin, tc.physical, tc.base
			if origin == "" {
				origin = tc.source
			}
			if physical == "" {
				physical = tc.source
			}
			if base == "" {
				base = "https://www.xcloudtoken.com"
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("model_mapping", tc.mapping)
			info := &relaycommon.RelayInfo{
				OriginModelName: origin,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelId: tc.channel, ChannelBaseUrl: base, UpstreamModelName: physical,
				},
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
					SourceModel: tc.source, PricePolicy: types.ZTAPIXcloudPricePolicy20261128, AllowedChannelIDs: tc.allowed,
				},
			}
			frozen := info.ZTAPIPublicationSnapshot.Clone()
			request := &dto.GeneralOpenAIRequest{}
			err := ModelMappedHelper(c, info, request)
			if tc.pass {
				require.NoError(t, err)
				require.Equal(t, tc.source, info.UpstreamModelName)
				if tc.name != "self_mapping" {
					require.Equal(t, tc.source, request.Model)
				}
			} else {
				require.Error(t, err)
			}
			require.Equal(t, frozen, info.ZTAPIPublicationSnapshot)
		})
	}
}

func TestXcloud20261128MappedPreservesLegacyGLMFlash(t *testing.T) {
	for _, policy := range []string{
		"", string(model.ZTAPIPricePolicyEnterprise20Margin), types.ZTAPIXcloudPricePolicy20261114,
		types.ZTAPISupplierAdmissionPricePolicy20261019, types.ZTAPIXcloudPricePolicy20261128 + "-other",
	} {
		for _, channel := range []int{1, 2, 8} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			base := "https://ai.yunxinapi.com/hub"
			if channel == 8 {
				base = "https://www.xcloudtoken.com"
			}
			info := &relaycommon.RelayInfo{
				OriginModelName: model.ZTAPIGLM53FlashSource,
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelId: channel, ChannelBaseUrl: base, UpstreamModelName: model.ZTAPIGLM53FlashSource,
				},
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
					SourceModel: model.ZTAPIGLM53FlashSource, PricePolicy: policy, AllowedChannelIDs: []int{channel},
				},
			}
			err := ModelMappedHelper(c, info, &dto.GeneralOpenAIRequest{})
			if channel == 1 {
				require.NoError(t, err, "policy %q", policy)
			} else {
				require.ErrorContains(t, err, "yunxin_enterprise_glm53_flash_route_mismatch", "policy %q", policy)
			}
		}
	}
}
