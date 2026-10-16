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
