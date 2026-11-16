package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runZTAPINonStreamMetadataFixture(t *testing.T, body string, managed bool, forceFormat bool) (string, *dto.Usage) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		StartTime: time.Unix(1790862987, 0), OriginModelName: "zt-claude-sonnet-5",
		RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-5-hc"},
	}
	info.ChannelSetting.ForceFormat = forceFormat
	if managed {
		info.ZTAPIPublicationSnapshot = &relaycommon.ZTAPIPublicationSnapshot{PublicName: info.OriginModelName, Modality: "text"}
	}
	usage, apiErr := OpenaiHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, apiErr)
	return recorder.Body.String(), usage
}

func TestZTAPINonStreamMetadataCapturedZeroTimestamp(t *testing.T) {
	// Production diagnostic wire shape: valid content/usage, but created=0.
	raw := `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"77","role":"assistant"}}],"created":0,"id":"msg_provider","model":"claude-sonnet-5","object":"chat.completion","usage":{"completion_tokens":3,"prompt_tokens":70,"total_tokens":73}}`
	for _, forceFormat := range []bool{false, true} {
		body, usage := runZTAPINonStreamMetadataFixture(t, raw, true, forceFormat)
		var got map[string]interface{}
		require.NoError(t, common.UnmarshalJsonStr(body, &got))
		require.Equal(t, float64(1790862987), got["created"])
		require.Equal(t, "msg_provider", got["id"])
		require.Equal(t, "claude-sonnet-5", got["model"])
		require.Equal(t, 73, usage.TotalTokens)
		require.Contains(t, body, `"content":"77"`)
	}
}

func TestZTAPINonStreamMetadataPreservesValidBody(t *testing.T) {
	raw := `{"id":"provider-id","model":"provider-model","created":1234,"object":"chat.completion","choices":[{"index":0,"message":{"content":"77","role":"assistant"},"finish_reason":"stop"}],"usage":{"prompt_tokens":70,"completion_tokens":3,"total_tokens":73},"provider_extension":{"integer":9007199254740993}}`
	body, _ := runZTAPINonStreamMetadataFixture(t, raw, true, false)
	require.Equal(t, raw, body)
}

func TestZTAPINonStreamMetadataPreservesUnmanagedBody(t *testing.T) {
	raw := `{"id":"provider-id","model":"provider-model","created":0,"object":"chat.completion","choices":[{"index":0,"message":{"content":"77","role":"assistant"},"finish_reason":"stop"}],"usage":{"prompt_tokens":70,"completion_tokens":3,"total_tokens":73}}`
	body, _ := runZTAPINonStreamMetadataFixture(t, raw, false, false)
	require.Equal(t, raw, body)
}

func TestZTAPINonStreamMetadataFillsMissingFieldsWithoutLosingTools(t *testing.T) {
	raw := `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"apply_patch","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":70,"completion_tokens":3,"total_tokens":73},"provider_extension":{"integer":9007199254740993}}`
	body, usage := runZTAPINonStreamMetadataFixture(t, raw, true, false)
	var got map[string]interface{}
	require.NoError(t, common.UnmarshalJsonStr(body, &got))
	id, ok := got["id"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(id, "chatcmpl-ztapi-"))
	require.Equal(t, "zt-claude-sonnet-5", got["model"])
	require.Equal(t, float64(1790862987), got["created"])
	require.Contains(t, body, "9007199254740993")
	require.Contains(t, body, `"finish_reason":"tool_calls"`)
	require.Contains(t, body, `"name":"apply_patch"`)
	require.Equal(t, 73, usage.TotalTokens)
}
