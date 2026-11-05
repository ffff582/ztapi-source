package helper

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

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
