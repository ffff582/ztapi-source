package openaicompat

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// SerializeCompleteParallelToolTurns reshapes completed history for upstream
// bridges that cannot merge consecutive tool results into one native user turn.
// It does not change tool execution or synthesize calls/results. Callers must
// scope this compatibility workaround to the affected upstream route.
func SerializeCompleteParallelToolTurns(messages []dto.Message) ([]dto.Message, error) {
	// Validate the whole input first: reordering must never repair invalid history.
	if err := ValidateChatToolHistory(messages); err != nil {
		return nil, err
	}
	var normalized []dto.Message
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		var calls []json.RawMessage
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			if err := common.Unmarshal(message.ToolCalls, &calls); err != nil {
				return nil, err
			}
		}
		if len(calls) <= 1 {
			if normalized != nil {
				normalized = append(normalized, message)
			}
			continue
		}
		if normalized == nil {
			normalized = make([]dto.Message, 0, len(messages)+len(calls)-1)
			normalized = append(normalized, messages[:i]...)
		}
		// Validation guarantees exactly these uninterrupted results, even when
		// their arrival order differs from the original call order.
		results := make(map[string]dto.Message, len(calls))
		for _, result := range messages[i+1 : i+1+len(calls)] {
			results[result.ToolCallId] = result
		}
		for j, raw := range calls {
			var call struct {
				ID string `json:"id"`
			}
			if err := common.Unmarshal(raw, &call); err != nil {
				return nil, err
			}
			assistant := dto.Message{Role: "assistant"}
			if j == 0 {
				assistant = message
			}
			// Keep each raw call, including unknown fields and cache metadata.
			// Retain the original assistant content/metadata only on the first pair.
			assistant.ToolCalls = make(json.RawMessage, len(raw)+2)
			assistant.ToolCalls[0] = '['
			copy(assistant.ToolCalls[1:], raw)
			assistant.ToolCalls[len(raw)+1] = ']'
			normalized = append(normalized, assistant, results[call.ID])
		}
		i += len(calls)
	}
	if normalized == nil {
		return messages, nil
	}
	return normalized, nil
}
