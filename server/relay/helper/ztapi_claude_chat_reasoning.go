package helper

import (
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ztapiClaudeChatReasoningFields are the reasoning controls the supplier's
// Chat Completions endpoint refuses for Claude. Any of them, with any value,
// is answered 422 unsupported_conversation_shape; verified 2026-10-16 for
// reasoning_effort low/high and thinking enabled. Claude Code sends an effort
// on every turn, so forwarding it failed nearly every Claude Code request.
var ztapiClaudeChatReasoningFields = []string{"reasoning_effort", "thinking", "reasoning", "enable_thinking"}

// stripZTAPIClaudeChatReasoning removes reasoning controls from a managed
// Claude request bound for Chat Completions. Removing unsupported controls
// does not guarantee that the supplier disables hidden reasoning.
func stripZTAPIClaudeChatReasoning(info *relaycommon.RelayInfo, body []byte, format types.RelayFormat) []byte {
	if format != types.RelayFormatOpenAI || info == nil || info.ZTAPIPublicationSnapshot == nil ||
		!strings.HasPrefix(strings.ToLower(info.ZTAPIPublicationSnapshot.SourceModel), "claude-") {
		return body
	}
	for _, field := range ztapiClaudeChatReasoningFields {
		if !gjson.GetBytes(body, field).Exists() {
			continue
		}
		if updated, err := sjson.DeleteBytes(body, field); err == nil {
			body = updated
		}
	}
	return body
}
