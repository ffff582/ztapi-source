package service

import (
	"bytes"
	"encoding/json"
	"regexp"
)

var workbenchFunctionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var workbenchCallID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type workbenchFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
	Arguments   *string         `json:"arguments"`
}

func workbenchToolChoice(choice string) bool {
	return choice == "" || choice == "auto" || choice == "none" || choice == "required"
}

func workbenchObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 1 && raw[0] == '{' && json.Valid(raw)
}

func workbenchToolDeclarations(raw json.RawMessage, responses bool) bool {
	if len(raw) == 0 {
		return true
	}
	var tools []struct {
		Type string `json:"type"`
		workbenchFunction
		Function *workbenchFunction `json:"function"`
	}
	if !decodeWorkbenchWire(raw, &tools) || tools == nil || len(tools) > 64 {
		return false
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" {
			return false
		}
		f := &tool.workbenchFunction
		if !responses {
			if tool.Function == nil || f.Name != "" || f.Arguments != nil || f.Strict != nil || len(f.Parameters) != 0 || f.Description != "" {
				return false
			}
			f = tool.Function
		} else if tool.Function != nil {
			return false
		}
		if !workbenchFunctionName.MatchString(f.Name) || seen[f.Name] || f.Arguments != nil || len(f.Description) > 16384 ||
			(len(f.Parameters) > 0 && !workbenchObject(f.Parameters)) {
			return false
		}
		seen[f.Name] = true
	}
	return true
}

func workbenchTextInput(raw json.RawMessage, responses bool) bool {
	if responses && workbenchText(raw, false) {
		return true
	}
	var messages []struct {
		Type       string          `json:"type"`
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ID         string          `json:"id"`
		Status     string          `json:"status"`
		CallID     string          `json:"call_id"`
		Name       string          `json:"name"`
		Arguments  *string         `json:"arguments"`
		Output     json.RawMessage `json:"output"`
		ChatCallID string          `json:"tool_call_id"`
		ChatCalls  []struct {
			Type     string            `json:"type"`
			ID       string            `json:"id"`
			Function workbenchFunction `json:"function"`
		} `json:"tool_calls"`
	}
	if !decodeWorkbenchWire(raw, &messages) || len(messages) == 0 || len(messages) > 128 {
		return false
	}
	seen := make(map[string]bool)
	pending := make(map[string]bool)
	add := func(id, name string, arguments *string) bool {
		if !workbenchCallID.MatchString(id) || !workbenchFunctionName.MatchString(name) || seen[id] || arguments == nil || !workbenchObject([]byte(*arguments)) {
			return false
		}
		seen[id], pending[id] = true, true
		return true
	}
	finish := func(id string, result json.RawMessage) bool {
		if !pending[id] || !workbenchText(result, true) {
			return false
		}
		delete(pending, id)
		return true
	}
	for _, msg := range messages {
		if msg.Status != "" && msg.Status != "completed" || msg.ID != "" && !workbenchCallID.MatchString(msg.ID) {
			return false
		}
		if responses {
			if msg.ChatCallID != "" || msg.ChatCalls != nil {
				return false
			}
			switch msg.Type {
			case "function_call":
				if msg.Role != "" || msg.Content != nil || msg.Output != nil || !add(msg.CallID, msg.Name, msg.Arguments) {
					return false
				}
				continue
			case "function_call_output":
				if msg.Role != "" || msg.Content != nil || msg.Name != "" || msg.Arguments != nil || !finish(msg.CallID, msg.Output) {
					return false
				}
				continue
			case "", "message":
			default:
				return false
			}
		} else if msg.Type != "" || msg.ID != "" || msg.Status != "" {
			return false
		}
		if msg.CallID != "" || msg.Name != "" || msg.Arguments != nil || msg.Output != nil {
			return false
		}
		if !responses && msg.Role == "tool" {
			if msg.ChatCalls != nil || !finish(msg.ChatCallID, msg.Content) {
				return false
			}
			continue
		}
		if msg.ChatCallID != "" || len(pending) != 0 {
			return false
		}
		switch msg.Role {
		case "system", "developer", "user", "assistant":
		default:
			return false
		}
		if !responses && msg.Role == "assistant" && len(msg.ChatCalls) > 0 && len(msg.ChatCalls) <= 64 {
			if !bytes.Equal(msg.Content, []byte("null")) && !workbenchText(msg.Content, true) {
				return false
			}
			for _, call := range msg.ChatCalls {
				f := call.Function
				if call.Type != "function" || f.Description != "" || f.Strict != nil || f.Parameters != nil || !add(call.ID, f.Name, f.Arguments) {
					return false
				}
			}
		} else if msg.ChatCalls != nil || !workbenchText(msg.Content, true) {
			return false
		}
	}
	return len(pending) == 0
}
