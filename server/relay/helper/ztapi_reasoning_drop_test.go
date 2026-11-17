package helper

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

func TestZTAPIReasoningNormalizesEveryChatRequestFieldIndependently(t *testing.T) {
	for _, tc := range []struct{ top, nested, wantTop, wantNested string }{
		{"high", "none", "high", ""},
		{"none", "high", "", "high"},
		{"high", "ultra", "high", "max"},
		{"ultra", "ultra", "max", "max"},
	} {
		t.Run(tc.top+"_"+tc.nested, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-6.1-sol"}}
			request := &dto.GeneralOpenAIRequest{ReasoningEffort: tc.top, Reasoning: []byte(`{"effort":"` + tc.nested + `","summary":"auto"}`)}
			require.NoError(t, ValidateAndNormalizeZTAPIReasoningEffort(info, request))
			require.Equal(t, tc.wantTop, request.ReasoningEffort)
			require.Equal(t, tc.wantNested, gjson.GetBytes(request.Reasoning, "effort").String())
			require.Equal(t, "auto", gjson.GetBytes(request.Reasoning, "summary").String())
			if tc.top == "none" || tc.nested == "none" {
				require.True(t, info.ReasoningEffortDropped)
				require.Equal(t, "not_in_supported_list", info.ReasoningEffortDropReason)
			}
		})
	}
}

func TestZTAPIReasoningNormalizesEveryFinalJSONFieldIndependently(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		format           types.RelayFormat
	}{
		{"valid_then_invalid", `{"reasoning_effort":"high","reasoning":{"effort":"none","summary":"auto"}}`, `{"reasoning_effort":"high","reasoning":{"summary":"auto"}}`, types.RelayFormatOpenAI},
		{"invalid_then_valid", `{"reasoning_effort":"none","reasoning":{"effort":"high"}}`, `{"reasoning":{"effort":"high"}}`, types.RelayFormatOpenAI},
		{"second_alias", `{"reasoning_effort":"high","reasoning":{"effort":"ultra"}}`, `{"reasoning_effort":"high","reasoning":{"effort":"max"}}`, types.RelayFormatOpenAI},
		{"both_aliases", `{"reasoning_effort":"ultra","reasoning":{"effort":"ultra"}}`, `{"reasoning_effort":"max","reasoning":{"effort":"max"}}`, types.RelayFormatOpenAI},
		{"gemini_aliases", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}},"generation_config":{"thinking_config":{"thinking_level":"none","include_thoughts":true}}}`, `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}},"generation_config":{"thinking_config":{"include_thoughts":true}}}`, types.RelayFormatGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-6.1-sol"}}
			out, err := ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(tc.body), tc.format)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(out))
		})
	}
}

func TestZTAPIReasoningClaudeChatFinalOverrideKeepsDroppedAudit(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "claude-opus-5-5"},
		ParamOverrideAudit:       []string{"set reasoning_effort"},
	}
	out, err := ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(`{"reasoning_effort":"hihg","messages":[]}`), types.RelayFormatOpenAI)
	require.NoError(t, err)
	require.JSONEq(t, `{"messages":[]}`, string(out))
	relaycommon.CaptureReasoningEffortForwardedJSON(info, out, types.RelayFormatOpenAI)
	require.True(t, info.ReasoningEffortDropped)
	require.Equal(t, "unrecognized_value", info.ReasoningEffortDropReason)
	require.Equal(t, "hihg", info.ReasoningEffortReceived)
	require.Equal(t, relaycommon.ReasoningEffortSourceParameterOverride, info.ReasoningEffortSource)
	require.Empty(t, info.ReasoningEffortForwarded)
}
