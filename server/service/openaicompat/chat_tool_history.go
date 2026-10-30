package openaicompat

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// ValidateChatToolHistory requires unique IDs and complete, uninterrupted tool turns.
// It validates the converted history without reordering or modifying messages.
func ValidateChatToolHistory(messages []dto.Message) error {
	seenIDs := make(map[string]bool)
	pending := make(map[string]bool)
	for _, message := range messages {
		var toolCalls []dto.ToolCallRequest
		if len(message.ToolCalls) > 0 {
			if err := common.Unmarshal(message.ToolCalls, &toolCalls); err != nil {
				return errors.New("messages: invalid tool calls")
			}
		}
		if message.Role == "tool" {
			if !pending[message.ToolCallId] || len(toolCalls) > 0 {
				return errors.New("messages: orphan or duplicate tool result")
			}
			delete(pending, message.ToolCallId)
			continue
		}
		if len(pending) > 0 {
			return errors.New("messages: tool results must immediately follow their assistant turn")
		}
		if len(toolCalls) > 0 && message.Role != "assistant" {
			return errors.New("messages: tool calls require an assistant turn")
		}
		for _, call := range toolCalls {
			if strings.TrimSpace(call.ID) == "" || seenIDs[call.ID] {
				return errors.New("messages: missing or duplicate tool call id")
			}
			seenIDs[call.ID] = true
			pending[call.ID] = true
		}
	}
	if len(pending) > 0 {
		return errors.New("messages: missing tool results")
	}
	return nil
}
