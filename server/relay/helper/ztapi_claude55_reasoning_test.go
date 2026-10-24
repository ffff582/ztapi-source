package helper

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPICodexClaude55ReasoningPassesValidationButIsNotForwardedToChat(t *testing.T) {
	for _, source := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		t.Run(source, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: source}}
			request := &dto.OpenAIResponsesRequest{Reasoning: &dto.Reasoning{Effort: "low"}}
			require.NoError(t, ValidateAndNormalizeZTAPIReasoningEffort(info, request))
			out, err := ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(`{"reasoning":{"effort":"low"}}`), types.RelayFormatOpenAIResponses)
			require.NoError(t, err)
			require.JSONEq(t, `{"reasoning":{"effort":"low"}}`, string(out))
			out, err = ValidateAndNormalizeZTAPIReasoningJSON(info, []byte(`{"max_tokens":64,"reasoning_effort":"low","thinking":{"type":"enabled"},"reasoning":{"effort":"low"},"enable_thinking":true}`), types.RelayFormatOpenAI)
			require.NoError(t, err)
			require.JSONEq(t, `{"max_tokens":1024}`, string(out))
			request.Reasoning.Effort = "ultra"
			require.ErrorIs(t, ValidateAndNormalizeZTAPIReasoningEffort(info, request), model.ErrZTAPIReasoningEffortUnsupported)
		})
	}
}
