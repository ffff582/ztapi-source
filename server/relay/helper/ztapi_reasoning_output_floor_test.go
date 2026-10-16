package helper

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func managedInfo(sourceModel string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: sourceModel}}
}

func TestReasoningOutputFloorRaisesTinyCapsOfReasoningModels(t *testing.T) {
	for _, tc := range []struct {
		model, body, path string
		format            types.RelayFormat
	}{
		{"gpt-5.5", `{"max_tokens":5}`, "max_tokens", types.RelayFormatOpenAI},
		{"gpt-5-mini", `{"max_completion_tokens":16}`, "max_completion_tokens", types.RelayFormatOpenAI},
		{"glm-5.3", `{"max_tokens":32}`, "max_tokens", types.RelayFormatOpenAI},
		{"deepseek-v4-pro", `{"max_output_tokens":100}`, "max_output_tokens", types.RelayFormatOpenAIResponses},
		{"gemini-3.1-pro-preview", `{"generationConfig":{"maxOutputTokens":8}}`, "generationConfig.maxOutputTokens", types.RelayFormatGemini},
	} {
		t.Run(tc.model, func(t *testing.T) {
			out, err := ValidateAndNormalizeZTAPIReasoningJSON(managedInfo(tc.model), []byte(tc.body), tc.format)
			require.NoError(t, err)
			require.EqualValues(t, ZTAPIReasoningOutputFloor, gjson.GetBytes(out, tc.path).Int())
		})
	}
}

func TestReasoningOutputFloorLeavesDeliberateLimitsAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		info   *relaycommon.RelayInfo
		body   string
		format types.RelayFormat
	}{
		"claude reasons only on request": {managedInfo("claude-sonnet-5"), `{"max_tokens":5}`, types.RelayFormatClaude},
		"non-reasoning model":            {managedInfo("gpt-4.1-mini"), `{"max_tokens":5}`, types.RelayFormatOpenAI},
		"cap already large enough":       {managedInfo("gpt-5.5"), `{"max_tokens":4096}`, types.RelayFormatOpenAI},
		"no cap requested":               {managedInfo("gpt-5.5"), `{"messages":[]}`, types.RelayFormatOpenAI},
		"unmanaged request":              {&relaycommon.RelayInfo{}, `{"max_tokens":5}`, types.RelayFormatOpenAI},
		"embedding model":                {managedInfo("text-embedding-3-small"), `{"max_tokens":5}`, types.RelayFormatOpenAI},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := ValidateAndNormalizeZTAPIReasoningJSON(tc.info, []byte(tc.body), tc.format)
			require.NoError(t, err)
			require.JSONEq(t, tc.body, string(out))
		})
	}
}

// The supplier answers any reasoning control on Claude chat with 422, which
// failed nearly every Claude Code turn; the request must reach it without one.
func TestClaudeChatRequestsDropReasoningControls(t *testing.T) {
	body := `{"model":"claude-sonnet-5","max_tokens":64,"reasoning_effort":"high","thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`
	out, err := ValidateAndNormalizeZTAPIReasoningJSON(managedInfo("claude-sonnet-5"), []byte(body), types.RelayFormatOpenAI)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"claude-sonnet-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, string(out))

	// Other models and non-chat formats keep their reasoning controls.
	gpt := `{"max_tokens":4096,"reasoning_effort":"high"}`
	out, err = ValidateAndNormalizeZTAPIReasoningJSON(managedInfo("gpt-5.5"), []byte(gpt), types.RelayFormatOpenAI)
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(out, "reasoning_effort").String())
	native := `{"max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":1024}}`
	out, err = ValidateAndNormalizeZTAPIReasoningJSON(managedInfo("claude-sonnet-5"), []byte(native), types.RelayFormatClaude)
	require.NoError(t, err)
	require.JSONEq(t, native, string(out))
}
