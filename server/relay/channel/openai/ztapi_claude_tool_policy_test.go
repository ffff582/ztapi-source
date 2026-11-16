package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIClaudeToolPolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		managed    bool
		model      string
		wantChoice any
	}{
		{"no_tools", `{"model":"claude-sonnet-5-hc","messages":[{"role":"user","content":"hello"}]}`, true, "zt-claude-sonnet-5", "none"},
		{"explicit_none", `{"model":"claude-sonnet-5-hc","tool_choice":"none"}`, true, "zt-claude-sonnet-5", "none"},
		{"explicit_auto_preserved", `{"model":"claude-sonnet-5-hc","tool_choice":"auto"}`, true, "zt-claude-sonnet-5", "auto"},
		{"named_tool_preserved", `{"model":"claude-sonnet-5-hc","tools":[{"type":"function","function":{"name":"summarize_inventory","parameters":{"type":"object","properties":{"count":{"type":"integer"}}}}}],"tool_choice":{"type":"function","function":{"name":"summarize_inventory"}}}`, true, "zt-claude-sonnet-5", map[string]any{"type": "function", "function": map[string]any{"name": "summarize_inventory"}}},
		{"tool_default_preserved", `{"model":"claude-sonnet-5-hc","tools":[{"type":"function","function":{"name":"calculate","parameters":{"type":"object"}}}]}`, true, "zt-claude-sonnet-5", nil},
		{"legacy_functions_preserved", `{"model":"claude-sonnet-5-hc","functions":[{"name":"calculate","parameters":{"type":"object"}}]}`, true, "zt-claude-sonnet-5", nil},
		{"legacy_choice_preserved", `{"model":"claude-sonnet-5-hc","function_call":"none"}`, true, "zt-claude-sonnet-5", nil},
		{"unmanaged_preserved", `{"model":"claude-sonnet-5-hc"}`, false, "zt-claude-sonnet-5", nil},
		{"other_model_preserved", `{"model":"gpt-5"}`, true, "zt-gpt-5", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request dto.GeneralOpenAIRequest
			require.NoError(t, common.UnmarshalJsonStr(tc.body, &request))
			info := &relaycommon.RelayInfo{OriginModelName: tc.model, RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: request.Model}}
			if tc.managed {
				info.ZTAPIPublicationSnapshot = &relaycommon.ZTAPIPublicationSnapshot{PublicName: tc.model, Modality: "text"}
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			adaptor := &Adaptor{ChannelType: constant.ChannelTypeOpenAI}
			converted, err := adaptor.ConvertOpenAIRequest(c, info, &request)
			require.NoError(t, err)
			encoded, err := common.Marshal(converted)
			require.NoError(t, err)
			encoded, err = relaycommon.RemoveDisabledFields(encoded, dto.ChannelOtherSettings{}, false)
			require.NoError(t, err)
			var got, want map[string]any
			require.NoError(t, common.Unmarshal(encoded, &got))
			require.NoError(t, common.UnmarshalJsonStr(tc.body, &want))
			if tc.wantChoice != nil {
				want["tool_choice"] = tc.wantChoice
			}
			require.Equal(t, want, got, "tools, tool choice and client messages must survive final serialization")
		})
	}
}
