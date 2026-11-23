package service

import (
	"encoding/json"
	"regexp"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const ztapiWorkbenchMessagesWireLimit = 131072

var ztapiWorkbenchLocalTool = regexp.MustCompile(`^mcp__ztapi_[a-zA-Z0-9_]{1,52}$`)
var ztapiWorkbenchToolID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type workbenchMessagesCache struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func (c *workbenchMessagesCache) valid() bool {
	return c == nil || c.Type == "ephemeral" && (c.TTL == "" || c.TTL == "5m" || c.TTL == "1h")
}

type workbenchMessagesBlock struct {
	Type      string                  `json:"type"`
	Text      *string                 `json:"text,omitempty"`
	ID        string                  `json:"id,omitempty"`
	Name      string                  `json:"name,omitempty"`
	Input     map[string]any          `json:"input,omitempty"`
	ToolUseID string                  `json:"tool_use_id,omitempty"`
	Content   json.RawMessage         `json:"content,omitempty"`
	IsError   *bool                   `json:"is_error,omitempty"`
	Cache     *workbenchMessagesCache `json:"cache_control,omitempty"`
}

func workbenchMessagesText(raw json.RawMessage) bool {
	var text string
	if len(raw) > 0 && raw[0] == '"' && common.Unmarshal(raw, &text) == nil {
		return true
	}
	var parts []struct {
		Type  string                  `json:"type"`
		Text  *string                 `json:"text"`
		Cache *workbenchMessagesCache `json:"cache_control,omitempty"`
	}
	if !decodeWorkbenchWire(raw, &parts) || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part.Type != "text" || part.Text == nil || !part.Cache.valid() {
			return false
		}
	}
	return true
}

// ZTAPIWorkbenchMessagesOutput accepts bounded text and local-tool results only.
func ZTAPIWorkbenchMessagesOutput(data []byte, expectedModel string) (int, error) {
	var body struct {
		Model     string          `json:"model"`
		MaxTokens int             `json:"max_tokens"`
		Stream    bool            `json:"stream"`
		System    json.RawMessage `json:"system,omitempty"`
		Messages  []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description,omitempty"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tools,omitempty"`
	}
	invalid := model.ErrZTAPISettlementInvalid
	if len(data) > ztapiWorkbenchMessagesWireLimit || !decodeWorkbenchWire(data, &body) ||
		body.Model == "" || body.Model != expectedModel || !body.Stream || body.MaxTokens < 1 || body.MaxTokens > 2048 ||
		len(body.Messages) == 0 || len(body.Tools) > 16 || len(body.System) > 0 && !workbenchMessagesText(body.System) {
		return 0, invalid
	}
	tools := make(map[string]bool, len(body.Tools))
	for _, tool := range body.Tools {
		if !ztapiWorkbenchLocalTool.MatchString(tool.Name) || tools[tool.Name] || tool.InputSchema["type"] != "object" {
			return 0, invalid
		}
		tools[tool.Name] = true
	}
	ids := make(map[string]bool)
	for _, message := range body.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return 0, invalid
		}
		if workbenchMessagesText(message.Content) {
			continue
		}
		var blocks []workbenchMessagesBlock
		if !decodeWorkbenchWire(message.Content, &blocks) || len(blocks) == 0 {
			return 0, invalid
		}
		for _, block := range blocks {
			if !block.Cache.valid() {
				return 0, invalid
			}
			switch block.Type {
			case "text":
				if block.Text == nil || block.ID != "" || block.Name != "" || block.Input != nil || block.ToolUseID != "" || len(block.Content) != 0 || block.IsError != nil {
					return 0, invalid
				}
			case "tool_use":
				_, seen := ids[block.ID]
				if message.Role != "assistant" || block.Text != nil || !ztapiWorkbenchToolID.MatchString(block.ID) || seen || !tools[block.Name] ||
					block.Input == nil || block.ToolUseID != "" || len(block.Content) != 0 || block.IsError != nil {
					return 0, invalid
				}
				ids[block.ID] = true
			case "tool_result":
				if message.Role != "user" || block.Text != nil || block.ID != "" || block.Name != "" || block.Input != nil ||
					!ids[block.ToolUseID] || !workbenchMessagesText(block.Content) {
					return 0, invalid
				}
				ids[block.ToolUseID] = false
			default:
				return 0, invalid
			}
		}
	}
	return body.MaxTokens, nil
}
