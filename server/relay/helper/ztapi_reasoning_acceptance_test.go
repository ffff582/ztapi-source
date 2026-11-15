package helper

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestZTAPIReasoningAcceptancePublishedProtocolMatrix(t *testing.T) {
	sources := []string{"claude-fable-5", "claude-opus-5", "claude-sonnet-5", "deepseek-v4-flash", "deepseek-v4-pro", "glm-5", "glm-5.1", "gemini-3-flash-preview", "gemini-3.5-flash", "kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code", "gpt-5.6-luna", "gpt-6.1-sol", "gpt-image-2", "doubao-seedance-2-5"}
	for _, source := range sources {
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "hihg"} {
			for _, wire := range []struct {
				format     types.RelayFormat
				body, path string
			}{
				{types.RelayFormatOpenAI, `{"marker":"unchanged","reasoning_effort":%q}`, "reasoning_effort"},
				{types.RelayFormatOpenAIResponses, `{"marker":"unchanged","reasoning":{"effort":%q}}`, "reasoning.effort"},
				{types.RelayFormatClaude, `{"marker":"unchanged","output_config":{"effort":%q}}`, "output_config.effort"},
				{types.RelayFormatGemini, `{"marker":"unchanged","generationConfig":{"thinkingConfig":{"thinkingLevel":%q}}}`, "generationConfig.thinkingConfig.thinkingLevel"},
			} {
				t.Run(source+"/"+effort+"/"+string(wire.format), func(t *testing.T) {
					info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: source}}
					out, err := ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(fmt.Sprintf(wire.body, effort)), wire.format)
					require.NoError(t, err, "optional effort must never reject a request locally")
					want, _ := model.NormalizeZTAPIReasoningEffortWithReason(source, effort)
					if strings.HasPrefix(source, "claude-") && wire.format == types.RelayFormatOpenAI {
						want = ""
					}
					require.Equal(t, want, gjson.GetBytes(out, wire.path).String())
					require.Equal(t, "unchanged", gjson.GetBytes(out, "marker").String())
					if wire.format == types.RelayFormatOpenAIResponses && want == "" {
						require.False(t, gjson.GetBytes(out, "reasoning").Exists())
					}
				})
			}
		}
	}
}

func TestZTAPIReasoningAcceptanceClaudeChatDropIsExplicit(t *testing.T) {
	for _, source := range []string{"claude-fable-5", "claude-opus-5", "claude-sonnet-5"} {
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
			t.Run(source+"/"+effort, func(t *testing.T) {
				info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: source}, ReasoningEffortReceived: effort, ReasoningEffortForwarded: effort, ReasoningEffortSource: relaycommon.ReasoningEffortSourceRequest}
				out, err := ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(`{"max_tokens":64,"reasoning_effort":"`+effort+`","thinking":{"type":"enabled"}}`), types.RelayFormatOpenAI)
				require.NoError(t, err)
				require.False(t, gjson.GetBytes(out, "reasoning_effort").Exists())
				require.False(t, gjson.GetBytes(out, "thinking").Exists())
				require.True(t, info.ReasoningEffortDropped)
				require.Equal(t, "not_in_supported_list", info.ReasoningEffortDropReason)
				require.Empty(t, info.ReasoningEffortForwarded)
				require.Equal(t, effort, info.ReasoningEffortReceived)
				require.Equal(t, int64(1024), gjson.GetBytes(out, "max_tokens").Int())
			})
		}
	}
}

func TestZTAPIReasoningAcceptanceClaudeNativeUntouched(t *testing.T) {
	info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "claude-opus-5"}}
	body := []byte(`{"max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024}}`)
	require.Equal(t, body, stripZTAPIClaudeChatReasoning(info, body, types.RelayFormatClaude))
	require.False(t, info.ReasoningEffortDropped)
}
