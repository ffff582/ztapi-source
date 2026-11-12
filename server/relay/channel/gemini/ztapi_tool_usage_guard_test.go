package gemini

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
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func geminiUsageGuardFixture(managed bool) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeGemini, UpstreamModelName: "gemini-2.0-flash"},
	}
	info.SetEstimatePromptTokens(99)
	if managed {
		info.ZTAPIPublicationSnapshot = &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: "offline-gemini", PublicationID: 1, Version: 1, PriceSourceID: 1, PriceSourceVersion: 1, Modality: "text",
			BillingDimensions: []string{"input_tokens", "output_tokens", "cache_read"},
			SaleUSD:           map[string]string{"input_tokens": "1", "output_tokens": "2", "cache_read": "0.1"},
		}
	}
	return c, recorder, info
}

func geminiUsageGuardPayload(metadata string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":"PRIVATE_REPLY"},{"inlineData":{"mimeType":"image/png","data":"UFJJVkFURV9JTUFHRQ=="}}]},"finishReason":"STOP"}]` + metadata + `}`
}

type geminiUsageGuardHandler func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)

func runGeminiUsageGuardHandler(t *testing.T, name string, handler geminiUsageGuardHandler, managed bool, payload string) (*dto.Usage, *relaycommon.RelayInfo, string) {
	t.Helper()
	c, recorder, info := geminiUsageGuardFixture(managed)
	if strings.Contains(name, "native") {
		info.RelayFormat = types.RelayFormatGemini
	}
	if strings.Contains(name, "stream") {
		info.IsStream = true
		payload = "data: " + payload + "\n\ndata: [DONE]\n\n"
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
	usage, apiErr := handler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	return usage, info, recorder.Body.String()
}

func geminiUsageGuardHandlers() map[string]geminiUsageGuardHandler {
	return map[string]geminiUsageGuardHandler{
		"chat": GeminiChatHandler, "native": GeminiTextGenerationHandler,
		"chat-stream": GeminiChatStreamHandler, "native-stream": GeminiTextGenerationStreamHandler,
	}
}

func TestGeminiClientFunctionNamesNeverEnableBuiltins(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, name := range []string{"googleSearch", "codeExecution", "urlContext", "lookup_local"} {
			prefix := "legacy/"
			if managed {
				prefix = "managed/"
			}
			t.Run(prefix+name, func(t *testing.T) {
				c, _, info := geminiUsageGuardFixture(managed)
				request := dto.GeneralOpenAIRequest{
					Model: "gemini-2.0-flash", Messages: []dto.Message{{Role: "user", Content: "offline"}},
					Tools: []dto.ToolCallRequest{{Type: "function", Function: dto.FunctionRequest{
						Name: name, Description: "client-owned function", Parameters: map[string]any{
							"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}},
						},
					}}},
					ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": name}},
				}
				converted, err := CovertOpenAI2Gemini(c, request, info)
				require.NoError(t, err)
				tools := converted.GetTools()
				require.Len(t, tools, 1)
				require.Nil(t, tools[0].GoogleSearch, "a client function name is not builtin authorization")
				require.Nil(t, tools[0].CodeExecution)
				require.Nil(t, tools[0].URLContext)
				rawFunctions, err := common.Marshal(tools[0].FunctionDeclarations)
				require.NoError(t, err)
				var functions []dto.FunctionRequest
				require.NoError(t, common.Unmarshal(rawFunctions, &functions))
				require.Len(t, functions, 1)
				require.Equal(t, name, functions[0].Name)
				require.Equal(t, "client-owned function", functions[0].Description)
				require.Equal(t, []string{name}, converted.ToolConfig.FunctionCallingConfig.AllowedFunctionNames)
			})
		}
	}
}

func TestGeminiExplicitNativeBuiltinsRemainForFinalRequestAdmission(t *testing.T) {
	c, _, info := geminiUsageGuardFixture(true)
	var request dto.GeminiChatRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"contents":[{"role":"user","parts":[{"text":"offline"}]}],"tools":[{"googleSearch":{}},{"codeExecution":{}},{"urlContext":{}}]}`, &request))
	before, err := common.Marshal(request.GetTools())
	require.NoError(t, err)
	converted, err := (&Adaptor{}).ConvertGeminiRequest(c, info, &request)
	require.NoError(t, err)
	after, err := common.Marshal(converted.(*dto.GeminiChatRequest).GetTools())
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after), "the parent final-request gate owns explicit native builtin admission")
}

func TestZTAPIGeminiMissingUsageNeverFabricatesImageTokens(t *testing.T) {
	for name, handler := range geminiUsageGuardHandlers() {
		for _, metadata := range []string{"", `,"usageMetadata":null`, `,"usageMetadata":{}`, `,"usageMetadata":{"promptTokenCount":17}`} {
			t.Run(name+"/"+metadata, func(t *testing.T) {
				usage, info, body := runGeminiUsageGuardHandler(t, name, handler, true, geminiUsageGuardPayload(metadata))
				wantPrompt := 0
				if strings.Contains(metadata, "17") {
					wantPrompt = 17
				}
				require.Equal(t, wantPrompt, usage.PromptTokens, "a local input estimate is not upstream usage")
				require.Zero(t, usage.CompletionTokens, "imageCount * 1400 is not a billable receipt")
				require.Zero(t, usage.TotalTokens)
				require.Equal(t, "upstream_incomplete", usage.UsageSource)
				check := relaycommon.ValidateZTAPIUsageDimensions(nil, info, usage)
				require.True(t, check.Pending)
				require.True(t, check.UsageMissing)
				require.NotContains(t, body, `"completion_tokens":1400`)
				require.Contains(t, body, "PRIVATE_REPLY", "suppressing billing estimates must not discard the response")
			})
		}
	}
}

func TestZTAPIGeminiRealUsageSurvivesWithoutFallback(t *testing.T) {
	cases := []struct {
		name       string
		metadata   string
		prompt     int
		completion int
		total      int
		incomplete bool
	}{
		{"reported", `,"usageMetadata":{"promptTokenCount":7,"toolUsePromptTokenCount":2,"candidatesTokenCount":3,"thoughtsTokenCount":1,"totalTokenCount":13,"cachedContentTokenCount":2}`, 9, 4, 13, false},
		{"zero-input", `,"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":4,"totalTokenCount":4}`, 0, 4, 4, false},
		{"zero-output", `,"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":0,"totalTokenCount":7}`, 7, 0, 7, false},
		{"explicit-zero", `,"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}`, 0, 0, 0, false},
		{"missing-output", `,"usageMetadata":{"promptTokenCount":7,"totalTokenCount":12}`, 7, 0, 12, true},
		{"inconsistent-total", `,"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"totalTokenCount":10}`, 7, 2, 10, true},
		{"negative-input", `,"usageMetadata":{"promptTokenCount":-1,"candidatesTokenCount":2,"totalTokenCount":1}`, -1, 2, 1, true},
	}
	for name, handler := range geminiUsageGuardHandlers() {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				usage, info, _ := runGeminiUsageGuardHandler(t, name, handler, true, geminiUsageGuardPayload(tc.metadata))
				require.Equal(t, tc.prompt, usage.PromptTokens)
				require.Equal(t, tc.completion, usage.CompletionTokens)
				require.Equal(t, tc.total, usage.TotalTokens)
				if tc.incomplete {
					require.Equal(t, "upstream_incomplete", usage.UsageSource)
					require.True(t, relaycommon.ValidateZTAPIUsageDimensions(nil, info, usage).Pending)
				} else {
					require.Equal(t, "upstream_reported", usage.UsageSource)
				}
				if tc.name == "reported" {
					require.Equal(t, 2, usage.PromptTokensDetails.CachedTokens)
					require.Equal(t, 1, usage.CompletionTokenDetails.ReasoningTokens)
					require.NotNil(t, info.ZTAPIMetering)
					require.Contains(t, string(info.ZTAPIMetering.RawUsage), `"prompt_tokens":9`)
					require.Contains(t, string(info.ZTAPIMetering.RawUsage), `"completion_tokens":4`)
					require.NotEmpty(t, info.ZTAPIMetering.RawUsageSHA256)
					require.NotContains(t, string(info.ZTAPIMetering.RawUsage), "PRIVATE_")
				}
			})
		}
	}
}

func TestZTAPIGeminiMissingTextUsageNeverUsesLocalEstimate(t *testing.T) {
	for name, handler := range geminiUsageGuardHandlers() {
		t.Run(name, func(t *testing.T) {
			payload := `{"candidates":[{"content":{"role":"model","parts":[{"text":"LOCAL_TEXT_ESTIMATE"}]},"finishReason":"STOP"}]}`
			usage, info, body := runGeminiUsageGuardHandler(t, name, handler, true, payload)
			require.Zero(t, usage.PromptTokens)
			require.Zero(t, usage.CompletionTokens)
			require.Zero(t, usage.TotalTokens)
			require.Equal(t, "upstream_incomplete", usage.UsageSource)
			require.True(t, relaycommon.ValidateZTAPIUsageDimensions(nil, info, usage).Pending)
			require.Contains(t, body, "LOCAL_TEXT_ESTIMATE")
		})
	}
}

func TestZTAPIGeminiStreamRetainsRealUsageAcrossNonUsageTrailers(t *testing.T) {
	for _, name := range []string{"chat-stream", "native-stream"} {
		for _, trailer := range []string{`{}`, `{"usageMetadata":null}`, `{"usageMetadata":{}}`, `{"usageMetadata":{"trafficType":"PRIVATE_METADATA"}}`} {
			t.Run(name+"/"+trailer, func(t *testing.T) {
				c, recorder, info := geminiUsageGuardFixture(true)
				info.IsStream = true
				if name == "native-stream" {
					info.RelayFormat = types.RelayFormatGemini
				}
				partial := geminiUsageGuardPayload(`,"usageMetadata":{"promptTokenCount":7}`)
				final := geminiUsageGuardPayload(`,"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}`)
				payload := "data: " + partial + "\n\ndata: " + final + "\n\ndata: " + trailer + "\n\ndata: [DONE]\n\n"
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
				usage, apiErr := geminiUsageGuardHandlers()[name](c, info, resp)
				require.Nil(t, apiErr)
				require.Equal(t, 9, usage.PromptTokens)
				require.Equal(t, 4, usage.CompletionTokens)
				require.Equal(t, 13, usage.TotalTokens)
				require.Equal(t, "upstream_reported", usage.UsageSource)
				require.False(t, relaycommon.ValidateZTAPIUsageDimensions(nil, info, usage).Pending)
				require.NotContains(t, string(info.ZTAPIMetering.RawUsage), "PRIVATE_")
				require.NotEmpty(t, recorder.Body.String())
			})
		}
	}
}

func TestZTAPIGeminiStreamIntegrityFailureSurvivesLaterValidUsage(t *testing.T) {
	for _, name := range []string{"chat-stream", "native-stream"} {
		for _, tc := range []struct {
			name   string
			bad    string
			reason string
		}{
			{"negative", `,"usageMetadata":{"promptTokenCount":-1,"candidatesTokenCount":2,"totalTokenCount":1}`, "usage_invalid_quantity"},
			{"total-mismatch", `,"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":99}`, "usage_total_mismatch"},
			{"wrong-type", `,"usageMetadata":{"promptTokenCount":"PRIVATE_INVALID","candidatesTokenCount":4,"totalTokenCount":13}`, "usage_invalid_json"},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				c, _, info := geminiUsageGuardFixture(true)
				info.IsStream = true
				if name == "native-stream" {
					info.RelayFormat = types.RelayFormatGemini
				}
				valid := geminiUsageGuardPayload(`,"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}`)
				payload := "data: " + valid + "\n\ndata: " + geminiUsageGuardPayload(tc.bad) + "\n\ndata: " + valid + "\n\ndata: [DONE]\n\n"
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
				usage, apiErr := geminiUsageGuardHandlers()[name](c, info, resp)
				require.Nil(t, apiErr)
				require.True(t, relaycommon.ValidateZTAPIUsageDimensions(nil, info, usage).Pending, "a valid later frame cannot erase an earlier invalid receipt")
				require.NotNil(t, info.ZTAPIMetering)
				require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, tc.reason)
				if tc.name != "wrong-type" {
					require.NotNil(t, info.ZTAPIMetering.UnverifiedUsage)
					require.Equal(t, tc.reason, info.ZTAPIMetering.UnverifiedUsage.Reason)
					require.NotContains(t, string(info.ZTAPIMetering.UnverifiedUsage.RawUsage), "PRIVATE_")
				}
			})
		}
	}
}

func TestZTAPIGeminiOversizedIntermediateUsageStaysPending(t *testing.T) {
	c, _, info := geminiUsageGuardFixture(true)
	metadata := dto.GeminiUsageMetadata{PromptTokenCount: 9, CandidatesTokenCount: 4, TotalTokenCount: 13}
	raw, err := common.Marshal(map[string]any{
		"promptTokenCount": 9, "candidatesTokenCount": 4, "totalTokenCount": 13,
		"provider_note": strings.Repeat("PRIVATE_NOTE", 7000),
	})
	require.NoError(t, err)
	geminiResponseUsage(c, info, metadata, raw, "stream", false)
	valid := []byte(`{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}`)
	usage := geminiResponseUsage(c, info, metadata, valid, "stream", true)
	require.True(t, relaycommon.ValidateZTAPIUsageDimensions(nil, info, &usage).Pending)
	require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_incomplete")
	require.NotContains(t, string(info.ZTAPIMetering.RawUsage), "PRIVATE_")
}

func TestGeminiLegacyUsageFallbackRemainsUnchanged(t *testing.T) {
	for name, handler := range geminiUsageGuardHandlers() {
		t.Run(name, func(t *testing.T) {
			usage, _, _ := runGeminiUsageGuardHandler(t, name, handler, false, geminiUsageGuardPayload(""))
			if strings.Contains(name, "stream") {
				require.Equal(t, 1400, usage.CompletionTokens)
			} else {
				require.Equal(t, 99, usage.PromptTokens)
			}
			require.Empty(t, usage.UsageSource)
		})
	}
}

func TestGeminiDebugLogsExcludeResponseBodies(t *testing.T) {
	oldDebug := common.DebugEnabled
	common.DebugEnabled = true
	var logs bytes.Buffer
	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
		common.DebugEnabled = oldDebug
	})
	for _, name := range []string{"chat", "native", "embedding"} {
		t.Run(name, func(t *testing.T) {
			logs.Reset()
			c, recorder, info := geminiUsageGuardFixture(true)
			info.RelayFormat = types.RelayFormatGemini
			payload := geminiUsageGuardPayload(`,"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}`)
			handler := geminiUsageGuardHandler(GeminiChatHandler)
			if name == "native" {
				handler = GeminiTextGenerationHandler
			} else if name == "embedding" {
				payload = `{"embedding":{"values":[0.1,0.2]},"note":"PRIVATE_EMBEDDING"}`
				handler = func(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
					return NativeGeminiEmbeddingHandler(c, resp, info)
				}
			}
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
			_, apiErr := handler(c, info, resp)
			require.Nil(t, apiErr)
			require.Contains(t, recorder.Body.String(), "PRIVATE_", "the client response must remain intact")
			require.NotContains(t, logs.String(), "PRIVATE_", "DEBUG must not disclose response text or embedding payloads")
			require.NotContains(t, logs.String(), "UFJJVkFURV9JTUFHRQ==", "DEBUG must not disclose inline images")
		})
	}
}
