package helper

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningNormalizesUltraAcrossSupportedRequestFormats(t *testing.T) {
	tests := []struct {
		name    string
		request any
	}{
		{"openai_chat", &dto.GeneralOpenAIRequest{ReasoningEffort: "ultra"}},
		{"responses", &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "ultra"}}},
		{"anthropic_messages", &dto.ClaudeRequest{OutputConfig: []byte(`{"effort":"ultra"}`)}},
		{"gemini", &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "ultra"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-5.6-sol"}}
			require.NoError(t, ValidateAndNormalizeZTAPIReasoningEffort(info, test.request))
			require.Equal(t, "max", relaycommon.ReasoningEffortFromRequest(test.request))
			require.Equal(t, relaycommon.ReasoningEffortSourceCodexAliasMapping, info.ReasoningEffortSource)
		})
	}
}

func TestZTAPIReasoningValidatesFinalJSONAfterParameterOverride(t *testing.T) {
	tests := []struct {
		name   string
		format types.RelayFormat
		body   string
		want   string
	}{
		{"openai_chat", types.RelayFormatOpenAI, `{"reasoning_effort":"ultra"}`, `{"reasoning_effort":"max"}`},
		{"responses", types.RelayFormatOpenAIResponses, `{"reasoning":{"effort":"ultra"}}`, `{"reasoning":{"effort":"max"}}`},
		{"anthropic_messages", types.RelayFormatClaude, `{"output_config":{"effort":"ultra"}}`, `{"output_config":{"effort":"max"}}`},
		{"gemini", types.RelayFormatGemini, `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"ultra"}}}`, `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"max"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-5.6-sol"}}
			got, err := ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(test.body), test.format)
			require.NoError(t, err)
			require.JSONEq(t, test.want, string(got))
		})
	}

	info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-5.4"}}
	got, err := ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(`{"reasoning":{"effort":"high"}}`), types.RelayFormatOpenAIResponses)
	require.NoError(t, err)
	require.JSONEq(t, `{"reasoning":{"effort":"high"}}`, string(got))
}

func TestZTAPIReasoningRejectsUnsupportedEffortBeforeDispatch(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "ultra"}}
	info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-5.4"}}

	err := ValidateAndNormalizeZTAPIReasoningEffort(info, request)
	require.ErrorIs(t, err, model.ErrZTAPIReasoningEffortUnsupported)
	require.Equal(t, "ultra", request.Reasoning.Effort)
	require.Equal(t, "invalid_reasoning_effort", ZTAPIInvalidReasoningEffortCode)
}

func TestZTAPIReasoningAllowsAbsentEffortWithoutCapabilityGuess(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{}
	info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-5.4"}}
	require.NoError(t, ValidateAndNormalizeZTAPIReasoningEffort(info, request))
	require.Empty(t, relaycommon.ReasoningEffortFromRequest(request))
}
