package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestReasoningAuditCapturedByRelayInfoConstruction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	tests := []struct {
		name string
		make func() *RelayInfo
	}{
		{"openai_chat", func() *RelayInfo { return GenRelayInfoOpenAI(ctx, &dto.GeneralOpenAIRequest{ReasoningEffort: "high"}) }},
		{"responses", func() *RelayInfo {
			return GenRelayInfoResponses(ctx, &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "high"}})
		}},
		{"anthropic_messages", func() *RelayInfo {
			return GenRelayInfoClaude(ctx, &dto.ClaudeRequest{OutputConfig: []byte(`{"effort":"high"}`)})
		}},
		{"gemini", func() *RelayInfo {
			return GenRelayInfoGemini(ctx, &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "high"}}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := test.make()
			require.Equal(t, "high", info.ReasoningEffortReceived)
			require.Equal(t, ReasoningEffortSourceRequest, info.ReasoningEffortSource)
		})
	}
}

func TestReasoningAuditSeparatesReceivedAndForwardedEffort(t *testing.T) {
	tests := []struct {
		name       string
		format     types.RelayFormat
		request    any
		body       string
		override   map[string]interface{}
		wantSource string
	}{
		{
			name:       "openai_chat",
			format:     types.RelayFormatOpenAI,
			request:    &dto.GeneralOpenAIRequest{ReasoningEffort: "high"},
			body:       `{"reasoning_effort":"high"}`,
			override:   reasoningOverride("reasoning_effort", "xhigh"),
			wantSource: ReasoningEffortSourceParameterOverride,
		},
		{
			name:       "responses",
			format:     types.RelayFormatOpenAIResponses,
			request:    &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "high"}},
			body:       `{"reasoning":{"effort":"high"}}`,
			override:   reasoningOverride("reasoning.effort", "xhigh"),
			wantSource: ReasoningEffortSourceParameterOverride,
		},
		{
			name:       "anthropic_messages",
			format:     types.RelayFormatClaude,
			request:    &dto.ClaudeRequest{OutputConfig: []byte(`{"effort":"high"}`)},
			body:       `{"output_config":{"effort":"high"}}`,
			override:   reasoningOverride("output_config.effort", "xhigh"),
			wantSource: ReasoningEffortSourceParameterOverride,
		},
		{
			name:       "gemini",
			format:     types.RelayFormatGemini,
			request:    &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "high"}}},
			body:       `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`,
			override:   reasoningOverride("generationConfig.thinkingConfig.thinkingLevel", "xhigh"),
			wantSource: ReasoningEffortSourceParameterOverride,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := &RelayInfo{
				FinalRequestRelayFormat: test.format,
				ChannelMeta:             &ChannelMeta{ParamOverride: test.override},
			}
			CaptureReasoningEffortReceived(info, test.request)
			body, err := ApplyParamOverrideWithRelayInfo([]byte(test.body), info)
			require.NoError(t, err)
			CaptureReasoningEffortForwardedJSON(info, body, test.format)

			require.Equal(t, "high", info.ReasoningEffortReceived)
			require.Equal(t, "xhigh", info.ReasoningEffortForwarded)
			require.Equal(t, test.wantSource, info.ReasoningEffortSource)
		})
	}
}

func reasoningOverride(path string, value string) map[string]interface{} {
	return map[string]interface{}{
		"operations": []interface{}{
			map[string]interface{}{
				"mode":  "set",
				"path":  path,
				"value": value,
			},
		},
	}
}

func TestReasoningAuditRecordsMissingEffortAsAbsent(t *testing.T) {
	info := &RelayInfo{}
	CaptureReasoningEffortReceived(info, &dto.OpenAIResponsesRequest{})
	CaptureReasoningEffortForwardedJSON(info, []byte(`{"model":"gpt-5.6-sol"}`), types.RelayFormatOpenAIResponses)

	require.Empty(t, info.ReasoningEffortReceived)
	require.Empty(t, info.ReasoningEffortForwarded)
	require.Empty(t, info.ReasoningEffortSource)
}

func TestReasoningAuditMarksModelSuffixWithoutInventingReceivedEffort(t *testing.T) {
	info := &RelayInfo{}
	CaptureReasoningEffortReceived(info, &dto.GeneralOpenAIRequest{})
	MarkReasoningEffortSource(info, ReasoningEffortSourceModelSuffix)
	CaptureReasoningEffortForwardedJSON(info, []byte(`{"reasoning_effort":"high"}`), types.RelayFormatOpenAI)

	require.Empty(t, info.ReasoningEffortReceived)
	require.Equal(t, "high", info.ReasoningEffortForwarded)
	require.Equal(t, ReasoningEffortSourceModelSuffix, info.ReasoningEffortSource)
}
