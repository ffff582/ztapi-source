package common_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAICacheUsageHandlerEntrypoints(t *testing.T) {
	for _, route := range []string{"chat", "chat_to_responses", "responses_to_chat"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, details string
				ttlOnlyPrice  bool
				pending       bool
			}{
				{"kimi_documented", `"cached_tokens":20,"cache_write_tokens":30`, false, false},
				{"generic_alias", `"cached_tokens":20,"cached_creation_tokens":30`, false, false},
				{"explicit_zero", `"cached_tokens":0,"cache_write_tokens":0`, true, false},
				{"header_ttls_chat_only", `"cached_tokens":20,"cache_write_tokens":30`, true, true},
				{"missing_write", `"cached_tokens":20`, false, true},
				{"negative_write", `"cached_tokens":20,"cache_write_tokens":-1`, false, true},
			} {
				mode := "nonstream"
				if stream {
					mode = "stream"
				}
				t.Run(route+"/"+mode+"/"+tc.name, func(t *testing.T) {
					gin.SetMode(gin.TestMode)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					info := &relaycommon.RelayInfo{
						StartTime: time.Now(), OriginModelName: "zt-kimi-k3",
						RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI,
						IsStream: stream, DisablePing: true, ShouldIncludeUsage: true,
						ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kimi-k3"},
						ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
							Modality: "text", PriceSourceID: 17, PriceSourceVersion: 3,
							BillingDimensions: []string{"input_tokens", "output_tokens", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h"},
							SaleUSD:           map[string]string{"input_tokens": "1", "output_tokens": "1", "cache_read": "1", "cache_write": "1", "cache_write_5m": "1", "cache_write_1h": "1"},
						},
					}
					info.SetEstimatePromptTokens(1000)
					if tc.ttlOnlyPrice {
						delete(info.ZTAPIPublicationSnapshot.SaleUSD, "cache_write")
					}
					body := `{"choices":[{"index":0,"message":{"role":"assistant","content":""},"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{` + tc.details + `}}}`
					if route == "responses_to_chat" {
						body = `{"status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":2,"input_tokens_details":{` + tc.details + `}}}`
						if stream {
							body = `{"type":"response.completed","response":` + body + `}`
						}
					}
					if stream {
						body = "data: " + body + "\n\ndata: [DONE]\n\n"
					}
					resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
					if tc.name == "header_ttls_chat_only" {
						resp.Header.Set("Msh-Usage-Cache-Write-Tokens-5m", "10")
						resp.Header.Set("Msh-Usage-Cache-Write-Tokens-1h", "20")
					}
					var usage *dto.Usage
					var apiErr *types.NewAPIError
					switch route {
					case "chat":
						if stream {
							usage, apiErr = openai.OaiStreamHandler(c, info, resp)
						} else {
							usage, apiErr = openai.OpenaiHandler(c, info, resp)
						}
					case "chat_to_responses":
						if stream {
							usage, apiErr = openai.OaiChatToResponsesStreamHandler(c, info, resp, nil)
						} else {
							usage, apiErr = openai.OaiChatToResponsesHandler(c, info, resp, nil)
						}
					case "responses_to_chat":
						if stream {
							usage, apiErr = openai.OaiResponsesToChatStreamHandler(c, info, resp)
						} else {
							usage, apiErr = openai.OaiResponsesToChatHandler(c, info, resp)
						}
					}
					require.Nil(t, apiErr)
					result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
					headerSplit := tc.name == "header_ttls_chat_only" && route != "responses_to_chat"
					require.Equal(t, tc.pending && !headerSplit, result.Pending, "%+v", result)
					if (!tc.pending || headerSplit) && tc.name != "explicit_zero" {
						quantities := map[string]int64{}
						for _, dimension := range result.Dimensions {
							quantities[dimension.Dimension] = dimension.Quantity
						}
						want := map[string]int64{"input_tokens": 50, "cache_read": 20, "cache_write": 30, "output_tokens": 2}
						if headerSplit {
							want = map[string]int64{"input_tokens": 50, "cache_read": 20, "cache_write_5m": 10, "cache_write_1h": 20, "output_tokens": 2}
						}
						require.Equal(t, want, quantities)
					}
				})
			}
		}
	}
}
