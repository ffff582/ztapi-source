package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func usageRegressionContext(format types.RelayFormat, managed bool) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		StartTime: time.Now(), OriginModelName: "zt-claude-opus-5",
		RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: format,
		IsStream: true, DisablePing: true, ShouldIncludeUsage: true,
		ChannelMeta:       &relaycommon.ChannelMeta{UpstreamModelName: "claude-opus-5-hc"},
		ClaudeConvertInfo: &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone},
	}
	info.SetEstimatePromptTokens(1000)
	if managed {
		info.ZTAPIPublicationSnapshot = &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: info.OriginModelName, Modality: "text", PriceSourceID: 17, PriceSourceVersion: 3,
			BillingDimensions: []string{"input_tokens", "output_tokens"},
			SaleUSD:           map[string]string{"input_tokens": "5", "output_tokens": "25"},
		}
	}
	return c, recorder, info
}

func usageRegressionStream(t *testing.T, format types.RelayFormat, frames []string, managed bool) (*dto.Usage, *gin.Context, *relaycommon.RelayInfo, string) {
	t.Helper()
	c, recorder, info := usageRegressionContext(format, managed)
	body := "data: " + strings.Join(frames, "\n\ndata: ") + "\n\ndata: [DONE]\n\n"
	usage, apiErr := OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	return usage, c, info, recorder.Body.String()
}

func assertUsageRegressionOutput(t *testing.T, format types.RelayFormat, body string, prompt, completion int) {
	t.Helper()
	var lastUsage map[string]interface{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var frame map[string]interface{}
		require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(line, "data: "), &frame))
		if usage, ok := frame["usage"].(map[string]interface{}); ok {
			lastUsage = usage
		}
	}
	require.NotNil(t, lastUsage, "client must receive the parsed usage")
	input, output := "prompt_tokens", "completion_tokens"
	if format == types.RelayFormatClaude {
		input, output = "input_tokens", "output_tokens"
	}
	require.Equal(t, float64(prompt), lastUsage[input])
	require.Equal(t, float64(completion), lastUsage[output])
}

func TestUsageRegressionStreamRetainsLatestSnapshot(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		t.Run(string(format), func(t *testing.T) {
			usage, c, info, body := usageRegressionStream(t, format, []string{
				`{"choices":[{"index":0,"delta":{"content":"OK"}}],"usage":{"prompt_tokens":90,"completion_tokens":1}}`,
				`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":2,"total_tokens":102,"usage_source":"local_estimate"}}`,
				`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":2,"total_tokens":102}}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{}}`,
			}, true)
			require.Equal(t, 100, usage.PromptTokens)
			require.Equal(t, 2, usage.CompletionTokens)
			require.Equal(t, 102, usage.TotalTokens)
			require.Equal(t, "upstream_reported", usage.UsageSource)
			require.False(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
			require.False(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
			assertUsageRegressionOutput(t, format, body, 100, 2)
		})
	}
}

func TestUsageRegressionStreamAliasesReachBillingAndClient(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		t.Run(string(format), func(t *testing.T) {
			usage, c, info, body := usageRegressionStream(t, format, []string{
				`{"choices":[{"index":0,"delta":{"content":"OK"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"input_tokens":100,"output_tokens":2,"usage_source":"provider_claim"}}`,
			}, true)
			require.Equal(t, 100, usage.PromptTokens)
			require.Equal(t, 2, usage.CompletionTokens)
			require.Equal(t, 102, usage.TotalTokens)
			require.Equal(t, "upstream_reported", usage.UsageSource)
			require.False(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
			assertUsageRegressionOutput(t, format, body, 100, 2)
		})
	}
}

func TestUsageRegressionMissingStreamUsageStaysPending(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		t.Run(string(format), func(t *testing.T) {
			usage, c, info, _ := usageRegressionStream(t, format, []string{
				`{"choices":[{"index":0,"delta":{"content":"OK"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"usage_source":"upstream_reported"}}`,
			}, true)
			require.Equal(t, "local_estimate", usage.UsageSource)
			require.Equal(t, 1000, usage.PromptTokens)
			require.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
			require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
		})
	}
}

func TestUsageRegressionNonStreamAliasesAndProvenance(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		for _, reported := range []bool{false, true} {
			name := string(format) + "/estimated"
			if reported {
				name = string(format) + "/reported"
			}
			t.Run(name, func(t *testing.T) {
				c, recorder, info := usageRegressionContext(format, true)
				info.IsStream = false
				body := `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"usage_source":"upstream_reported"}}`
				if reported {
					body = `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"input_tokens":100,"output_tokens":2,"usage_source":"local_estimate"},"provider_extension":{"integer":9007199254740993}}`
				}
				usage, apiErr := OpenaiHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
				require.Nil(t, apiErr)
				require.Equal(t, !reported, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
				if reported {
					require.Equal(t, "upstream_reported", usage.UsageSource)
					require.Equal(t, 100, usage.PromptTokens)
					require.Equal(t, 2, usage.CompletionTokens)
					require.Equal(t, 102, usage.TotalTokens)
					var response struct {
						Usage map[string]interface{} `json:"usage"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
					input, output := "prompt_tokens", "completion_tokens"
					if format == types.RelayFormatClaude {
						input, output = "input_tokens", "output_tokens"
					}
					require.Equal(t, float64(100), response.Usage[input])
					require.Equal(t, float64(2), response.Usage[output])
					if format == types.RelayFormatOpenAI {
						require.Contains(t, recorder.Body.String(), "9007199254740993")
					}
				} else {
					require.Equal(t, "local_estimate", usage.UsageSource)
					require.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
				}
			})
		}
	}
}

func TestUsageRegressionUnmanagedNonStreamPolicyUnchanged(t *testing.T) {
	c, _, info := usageRegressionContext(types.RelayFormatOpenAI, false)
	info.IsStream = false
	body := `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"completion_tokens":2}}`
	usage, apiErr := OpenaiHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
	require.Nil(t, apiErr)
	require.Equal(t, 1000, usage.PromptTokens)
	require.Equal(t, 2, usage.CompletionTokens)
	require.Equal(t, relaycommon.ZTAPIUsageDimensionResult{}, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage))
}

func TestUsageRegressionTrustedSourceOverridesProviderLabel(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			c, recorder, info := usageRegressionContext(types.RelayFormatOpenAI, true)
			info.IsStream = stream
			body := `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":2,"total_tokens":102,"usage_source":"local_estimate","provider_numeric":9007199254740993}}`
			if stream {
				body = "data: " + body + "\n\ndata: [DONE]\n\n"
			}
			response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
			var usage *dto.Usage
			var apiErr *types.NewAPIError
			if stream {
				usage, apiErr = OaiStreamHandler(c, info, response)
			} else {
				usage, apiErr = OpenaiHandler(c, info, response)
			}
			require.Nil(t, apiErr)
			require.Equal(t, "upstream_reported", usage.UsageSource)
			require.Contains(t, recorder.Body.String(), `"usage_source":"upstream_reported"`)
			require.NotContains(t, recorder.Body.String(), `"usage_source":"local_estimate"`)
			require.Contains(t, recorder.Body.String(), "9007199254740993")
		})
	}
}

func TestUsageRegressionEvidenceUsesSelectedRawSnapshot(t *testing.T) {
	usage, _, info, _ := usageRegressionStream(t, types.RelayFormatOpenAI, []string{
		`{"choices":[{"index":0,"delta":{"content":"PRIVATE_REPLY"}}],"usage":{"prompt_tokens":90,"completion_tokens":1}}`,
		`{"choices":[],"usage":{"input_tokens":100,"output_tokens":2,"usage_source":"local_estimate"}}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	}, true)
	require.Equal(t, 100, usage.PromptTokens)
	require.NotNil(t, info.ZTAPIMetering)
	require.Equal(t, "stream", info.ZTAPIMetering.UsageLocation)
	require.Equal(t, 1, info.ZTAPIMetering.UsageObservations)
	require.JSONEq(t, `{"input_tokens":100,"output_tokens":2}`, string(info.ZTAPIMetering.RawUsage))
	require.NotContains(t, string(info.ZTAPIMetering.RawUsage), "PRIVATE_REPLY")
}

func TestUsageRegressionMalformedFrameKeepsScannerErrors(t *testing.T) {
	_, _, info, _ := usageRegressionStream(t, types.RelayFormatOpenAI, []string{
		`{"choices":[{"index":0,"delta":{"content":"OK"}}],"usage":{"prompt_tokens":100,"completion_tokens":2}}`,
		`{"choices":`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	}, true)
	require.True(t, info.StreamStatus.HasErrors())
	require.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
}
