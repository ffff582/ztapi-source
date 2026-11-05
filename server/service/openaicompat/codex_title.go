package openaicompat

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// Codex's title prompt forbids quotes in the title value. Chat-only suppliers
// may ignore response_format, so make its outer JSON encoding explicit too.
// The title is still generated and billed by the original upstream model.
func applyCodexTitleFormatContract(chat *dto.GeneralOpenAIRequest) {
	if chat == nil || len(chat.Tools) != 0 || chat.ResponseFormat == nil || chat.ResponseFormat.Type != "json_schema" {
		return
	}
	var contract struct {
		Name   string `json:"name"`
		Strict bool   `json:"strict"`
		Schema struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type      string `json:"type"`
				MinLength int    `json:"minLength"`
				MaxLength int    `json:"maxLength"`
			} `json:"properties"`
			Required             []string `json:"required"`
			AdditionalProperties *bool    `json:"additionalProperties"`
		} `json:"schema"`
	}
	if common.Unmarshal(chat.ResponseFormat.JsonSchema, &contract) != nil {
		return
	}
	schema := contract.Schema
	if contract.Name != "codex_output_schema" || !contract.Strict || schema.Type != "object" || len(schema.Properties) != 1 || len(schema.Required) != 1 || schema.Required[0] != "title" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		return
	}
	title, ok := schema.Properties["title"]
	if !ok || title.Type != "string" || title.MinLength != 1 || title.MaxLength != 36 {
		return
	}
	const hint = `The response_format JSON schema is authoritative. Return exactly one JSON object of the form {"title":"..."}, with no Markdown or other text. The title value must follow the user's title instructions; restrictions on quotes apply to that value, not to JSON encoding.`
	last := -1
	for i, message := range chat.Messages {
		if message.Role != "system" {
			break
		}
		last = i
	}
	if last >= 0 {
		if text, ok := chat.Messages[last].Content.(string); ok {
			chat.Messages[last].Content = text + "\n\n" + hint
			return
		}
	}
	chat.Messages = append([]dto.Message{{Role: "system", Content: hint}}, chat.Messages...)
}
