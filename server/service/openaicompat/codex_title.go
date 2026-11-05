package openaicompat

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"strings"
)

const codexTitleEncodingHint = `The response_format JSON schema is authoritative. Return exactly one JSON object of the form {"title":"..."}, with no Markdown or other text. The title value must follow the user's title instructions; restrictions on quotes apply to that value, not to JSON encoding.`

func ApplyResponsesCodexTitleFormatContract(request *dto.OpenAIResponsesRequest) {
	if request == nil {
		return
	}
	declarations, err := responsesToolDeclarations(request.Tools, request.Input)
	if err != nil {
		return
	}
	var tools []json.RawMessage
	if rawJSONPresent(declarations) && (common.Unmarshal(declarations, &tools) != nil || len(tools) != 0) {
		return
	}
	format, err := convertResponsesTextFormat(request.Text)
	if err != nil || !isCodexTitleFormat(format) {
		return
	}
	instructions, err := rawJSONString(request.Instructions)
	if err != nil || strings.Contains(instructions, codexTitleEncodingHint) {
		return
	}
	if instructions != "" {
		instructions += "\n\n"
	}
	request.Instructions, _ = common.Marshal(instructions + codexTitleEncodingHint)
}

// Codex's title prompt forbids quotes in the title value. Chat-only suppliers
// may ignore response_format, so make its outer JSON encoding explicit too.
// The title is still generated and billed by the original upstream model.
func applyCodexTitleFormatContract(chat *dto.GeneralOpenAIRequest) {
	if chat == nil || len(chat.Tools) != 0 || !isCodexTitleFormat(chat.ResponseFormat) {
		return
	}
	last := -1
	for i, message := range chat.Messages {
		if message.Role != "system" {
			break
		}
		last = i
	}
	if last >= 0 {
		if text, ok := chat.Messages[last].Content.(string); ok {
			chat.Messages[last].Content = text + "\n\n" + codexTitleEncodingHint
			return
		}
	}
	chat.Messages = append([]dto.Message{{Role: "system", Content: codexTitleEncodingHint}}, chat.Messages...)
}

func isCodexTitleFormat(format *dto.ResponseFormat) bool {
	if format == nil || format.Type != "json_schema" {
		return false
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
	if common.Unmarshal(format.JsonSchema, &contract) != nil {
		return false
	}
	schema := contract.Schema
	if contract.Name != "codex_output_schema" || !contract.Strict || schema.Type != "object" || len(schema.Properties) != 1 || len(schema.Required) != 1 || schema.Required[0] != "title" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		return false
	}
	title, ok := schema.Properties["title"]
	return ok && title.Type == "string" && title.MinLength == 1 && title.MaxLength == 36
}
