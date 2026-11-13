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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestZTAPICacheControlIngressScope(t *testing.T) {
	for _, channel := range []struct {
		name     string
		kind     int
		upstream string
	}{
		{"openai", constant.ChannelTypeOpenAI, "claude-opus-5-hc"},
		{"azure", constant.ChannelTypeAzure, "claude-opus-5-hc"},
		{"openrouter", constant.ChannelTypeOpenRouter, "anthropic/claude-opus-5"},
	} {
		for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
			for _, managed := range []bool{false, true} {
				mode := "legacy"
				if managed {
					mode = "managed"
				}
				t.Run(channel.name+"/"+string(format)+"/"+mode, func(t *testing.T) {
					info := &relaycommon.RelayInfo{
						OriginModelName: "zt-claude-opus-5", RelayFormat: format,
						RelayMode:   relayconstant.RelayModeChatCompletions,
						ChannelMeta: &relaycommon.ChannelMeta{ChannelType: channel.kind, UpstreamModelName: channel.upstream},
					}
					if managed {
						info.ZTAPIPublicationSnapshot = &relaycommon.ZTAPIPublicationSnapshot{PublicName: info.OriginModelName, Modality: "text"}
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					adaptor := &Adaptor{ChannelType: channel.kind}
					var converted any
					var err error
					if format == types.RelayFormatOpenAI {
						var request dto.GeneralOpenAIRequest
						require.NoError(t, common.UnmarshalJsonStr(`{"model":"claude-opus-5-hc","cache_control":{"type":"ephemeral"},"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}},"cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"function","function":{"name":"write_file","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":[{"type":"text","text":"cached","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"uncached"}]}]}`, &request))
						converted, err = adaptor.ConvertOpenAIRequest(c, info, &request)
					} else {
						var request dto.ClaudeRequest
						require.NoError(t, common.UnmarshalJsonStr(`{"model":"claude-opus-5-hc","cache_control":{"type":"ephemeral"},"tools":[{"name":"read_file","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}},{"name":"write_file","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":"cached","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"uncached"}]}]}`, &request))
						converted, err = adaptor.ConvertClaudeRequest(c, info, &request)
					}
					require.NoError(t, err)
					body, err := common.Marshal(converted)
					require.NoError(t, err)
					body, err = relaycommon.RemoveDisabledFields(body, dto.ChannelOtherSettings{}, false)
					require.NoError(t, err)
					if managed {
						assert.JSONEq(t, `{"type":"ephemeral"}`, gjson.GetBytes(body, "cache_control").Raw)
						assert.JSONEq(t, `{"type":"ephemeral","ttl":"1h"}`, gjson.GetBytes(body, "tools.0.cache_control").Raw)
					} else {
						assert.False(t, gjson.GetBytes(body, "cache_control").Exists(), "legacy OpenAI ingress previously ignored this new DTO field")
						assert.False(t, gjson.GetBytes(body, "tools.0.cache_control").Exists())
					}
					assert.False(t, gjson.GetBytes(body, "tools.1.cache_control").Exists())
					assert.JSONEq(t, `{"type":"ephemeral","ttl":"5m"}`, gjson.GetBytes(body, "messages.0.content.0.cache_control").Raw, "pre-existing content cache support remains unchanged")
					assert.False(t, gjson.GetBytes(body, "messages.0.content.1.cache_control").Exists())
				})
			}
		}
	}
}
