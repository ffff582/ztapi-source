package helper

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestZTAPIReasoningDropKeepsOtherFieldsAcrossProtocols(t *testing.T) {
	for _, tc := range []struct {
		name       string
		format     types.RelayFormat
		body, want string
		request    any
	}{
		{"chat", types.RelayFormatOpenAI, `{"reasoning_effort":"high","reasoning":{"effort":"low","summary":"auto"},"model":"same"}`, `{"reasoning":{"summary":"auto"},"model":"same"}`, &dto.GeneralOpenAIRequest{ReasoningEffort: "high", Reasoning: []byte(`{"effort":"low","summary":"auto"}`)}},
		{"responses", types.RelayFormatOpenAIResponses, `{"reasoning":{"effort":"high","summary":"auto"},"model":"same"}`, `{"reasoning":{"summary":"auto"},"model":"same"}`, &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "high", Summary: "auto"}}},
		{"empty_responses", types.RelayFormatOpenAIResponses, `{"reasoning":{"effort":"high"},"model":"same"}`, `{"model":"same"}`, &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "high"}}},
		{"claude", types.RelayFormatClaude, `{"output_config":{"effort":"high","format":{"type":"json_schema"}},"model":"same"}`, `{"output_config":{"format":{"type":"json_schema"}},"model":"same"}`, &dto.ClaudeRequest{OutputConfig: []byte(`{"effort":"high","format":{"type":"json_schema"}}`)}},
		{"gemini", types.RelayFormatGemini, `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}},"model":"same"}`, `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}},"model":"same"}`, &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "high"}}}},
		{"gemini_snake", types.RelayFormatGemini, `{"generation_config":{"thinking_config":{"thinking_level":"high","thinking_budget":20}},"model":"same"}`, `{"generation_config":{"thinking_config":{"thinking_budget":20}},"model":"same"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-4.1"}}
			if tc.request != nil {
				require.NoError(t, ValidateAndNormalizeZTAPIReasoningEffort(info, tc.request))
				require.Empty(t, relaycommon.ReasoningEffortFromRequest(tc.request))
				encoded, err := common.Marshal(tc.request)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), `"effort"`)
			}
			out, err := ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(tc.body), tc.format)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(out))
		})
	}
}
