package openaicompat

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// responsesViaChatModelPrefixes lists upstream model families whose supplier
// serves Chat Completions only. Codex speaks the Responses API exclusively, so
// requests for these models are translated instead of failing upstream: in
// 2026-10 every Responses request for claude-* (78) and kimi-* (12) failed
// with model_route_unavailable or InvalidParameter.
var responsesViaChatModelPrefixes = []string{"claude-", "kimi-"}

// ShouldResponsesUseChatCompletions reports whether a Responses request for
// the given upstream model must be served through Chat Completions.
func ShouldResponsesUseChatCompletions(upstreamModel string) bool {
	model := strings.ToLower(strings.TrimSpace(upstreamModel))
	for _, prefix := range responsesViaChatModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// ResponsesChatConversion is a Responses request rewritten for Chat Completions.
// CustomTools names the chat functions that stand in for Responses custom
// (freeform) tools, so their calls can be returned in the shape the client
// declared.
type ResponsesChatConversion struct {
	Request     *dto.GeneralOpenAIRequest
	CustomTools map[string]bool
	ToolNames   map[string]ResponsesToolName
}

type ResponsesToolName struct {
	Name      string
	Namespace string
}

// Chat function names cannot carry namespaces. Use a stable collision-resistant
// alias and restore the original name and namespace in the Responses output.
func ResponsesChatToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	sum := sha256.Sum256([]byte(namespace + "\x00" + name))
	suffix := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if len(suffix) > 40 {
		suffix = suffix[:40]
	}
	return fmt.Sprintf("ns_%x_%s", sum[:8], suffix)
}

// CustomToolInputArgument is the single string argument a custom tool is
// given when it is offered to a Chat Completions model as a function.
const CustomToolInputArgument = "input"

type responsesInputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Action    json.RawMessage `json:"action"`
	Tools     []responsesTool `json:"tools"`
}

type responsesContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Refusal  string          `json:"refusal"`
	ImageURL json.RawMessage `json:"image_url"`
	FileID   string          `json:"file_id"`
	Detail   string          `json:"detail"`
	FileData string          `json:"file_data"`
	Filename string          `json:"filename"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict,omitempty"`
	Format      json.RawMessage `json:"format"`
	Tools       []responsesTool `json:"tools"`
}

// ResponsesRequestToChatCompletionsRequest rewrites a stateless Responses
// request as a Chat Completions request. Server-side state (previous responses,
// stored conversations and prompts) cannot be reproduced and is rejected.
func ResponsesRequestToChatCompletionsRequest(req *dto.OpenAIResponsesRequest) (*ResponsesChatConversion, error) {
	if req == nil {
		return nil, errors.New("request is nil")
	}
	if req.Model == "" {
		return nil, errors.New("model is required")
	}
	if strings.TrimSpace(req.PreviousResponseID) != "" {
		return nil, errors.New("previous_response_id is not supported for this model; send the full conversation in input")
	}
	if rawJSONPresent(req.Conversation) {
		return nil, errors.New("conversation is not supported for this model; send the full conversation in input")
	}
	if rawJSONPresent(req.Prompt) {
		return nil, errors.New("stored prompts are not supported for this model")
	}

	conversion := &ResponsesChatConversion{CustomTools: map[string]bool{}, ToolNames: map[string]ResponsesToolName{}}
	declarations, err := responsesToolDeclarations(req.Tools, req.Input)
	if err != nil {
		return nil, err
	}
	tools, err := convertResponsesTools(declarations, conversion.CustomTools, conversion.ToolNames)
	if err != nil {
		return nil, err
	}
	// Validate against current declarations before history adds old custom calls.
	toolChoice, err := convertResponsesToolChoice(req.ToolChoice, conversion.CustomTools, conversion.ToolNames)
	if err != nil {
		return nil, err
	}

	messages := make([]dto.Message, 0, 8)
	if instructions, err := rawJSONString(req.Instructions); err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	} else if strings.TrimSpace(instructions) != "" {
		messages = append(messages, dto.Message{Role: "system", Content: instructions})
	}
	inputMessages, err := convertResponsesInput(req.Input, conversion.CustomTools)
	if err != nil {
		return nil, err
	}
	messages = append(messages, inputMessages...)
	if len(messages) == 0 {
		return nil, errors.New("input is required")
	}

	chat := &dto.GeneralOpenAIRequest{
		Model:       req.Model,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxOutputTokens,
		User:        req.User,
		Tools:       tools,
		ToolChoice:  toolChoice,
	}
	if len(tools) > 0 {
		if rawJSONPresent(req.ParallelToolCalls) {
			var parallel bool
			if err := common.Unmarshal(req.ParallelToolCalls, &parallel); err != nil {
				return nil, fmt.Errorf("parallel_tool_calls: %w", err)
			}
			chat.ParallelTooCalls = &parallel
		}
	}
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		chat.ReasoningEffort = req.Reasoning.Effort
	}
	format, err := convertResponsesTextFormat(req.Text)
	if err != nil {
		return nil, err
	}
	chat.ResponseFormat = format
	if err := ValidateChatToolHistory(chat.Messages); err != nil {
		return nil, err
	}
	conversion.Request = chat
	return conversion, nil
}

func rawJSONPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func rawJSONString(raw json.RawMessage) (string, error) {
	if !rawJSONPresent(raw) {
		return "", nil
	}
	var value string
	if err := common.Unmarshal(raw, &value); err != nil {
		return "", errors.New("must be a string")
	}
	return value, nil
}

// Recent Codex clients send tool updates as input items rather than repeating
// the top-level tools array. Apply updates in order while retaining other tools.
func responsesToolDeclarations(raw, input json.RawMessage) (json.RawMessage, error) {
	var items []responsesInputItem
	if common.Unmarshal(input, &items) != nil {
		return raw, nil
	}
	var declared []responsesTool
	loaded := false
	for _, item := range items {
		if item.Type != "additional_tools" {
			continue
		}
		if !loaded && rawJSONPresent(raw) {
			if err := common.Unmarshal(raw, &declared); err != nil {
				return nil, fmt.Errorf("tools: %w", err)
			}
		}
		loaded = true
		declared = mergeResponsesTools(declared, item.Tools)
	}
	if !loaded {
		return raw, nil
	}
	return common.Marshal(declared)
}

func mergeResponsesTools(current, updates []responsesTool) []responsesTool {
	for _, update := range updates {
		matched := false
		for i := range current {
			sameType := current[i].Type == update.Type ||
				(current[i].Type == "function" || current[i].Type == "custom") && (update.Type == "function" || update.Type == "custom")
			if !sameType || current[i].Name != update.Name {
				continue
			}
			if update.Type == "namespace" {
				if update.Description == "" {
					update.Description = current[i].Description
				}
				update.Tools = mergeResponsesTools(current[i].Tools, update.Tools)
			}
			current[i] = update
			matched = true
			break
		}
		if !matched {
			current = append(current, update)
		}
	}
	return current
}

func convertResponsesTools(raw json.RawMessage, customTools map[string]bool, toolNames map[string]ResponsesToolName) ([]dto.ToolCallRequest, error) {
	if !rawJSONPresent(raw) {
		return nil, nil
	}
	var declared []responsesTool
	if err := common.Unmarshal(raw, &declared); err != nil {
		return nil, fmt.Errorf("tools: %w", err)
	}
	tools := make([]dto.ToolCallRequest, 0, len(declared))
	seen := map[string]bool{}
	var add func(responsesTool, string) error
	add = func(tool responsesTool, namespace string) error {
		if tool.Type != "function" && tool.Strict != nil {
			return fmt.Errorf("tools: strict is not supported for tool type %q", tool.Type)
		}
		if tool.Type == "namespace" {
			if namespace != "" || strings.TrimSpace(tool.Name) == "" {
				return errors.New("tools: invalid or nested namespace")
			}
			for _, nested := range tool.Tools {
				if nested.Type != "function" && nested.Type != "custom" {
					return fmt.Errorf("tools: unsupported namespaced tool type %q", nested.Type)
				}
				nested.Description = strings.TrimSpace(tool.Description + "\n" + nested.Description)
				if err := add(nested, tool.Name); err != nil {
					return err
				}
			}
			return nil
		}
		if tool.Type != "function" && tool.Type != "custom" {
			return fmt.Errorf("tools: unsupported tool type %q for this model", tool.Type)
		}
		if len(tool.Tools) > 0 {
			return errors.New("tools: only namespaces can contain nested tools")
		}
		name := ResponsesChatToolName(namespace, tool.Name)
		if strings.TrimSpace(tool.Name) == "" || seen[name] {
			return errors.New("tools: missing or duplicate function name")
		}
		seen[name] = true
		if namespace != "" {
			toolNames[name] = ResponsesToolName{Name: tool.Name, Namespace: namespace}
			tool.Description = strings.TrimSpace(tool.Description + "\nNamespace: " + namespace + ". Tool: " + tool.Name + ".")
		}
		switch tool.Type {
		case "function":
			customTools[name] = false
			var parameters any
			if rawJSONPresent(tool.Parameters) {
				if err := common.Unmarshal(tool.Parameters, &parameters); err != nil {
					return fmt.Errorf("tool %q parameters: %w", tool.Name, err)
				}
			}
			tools = append(tools, dto.ToolCallRequest{Type: "function", Function: dto.FunctionRequest{
				Name: name, Description: tool.Description, Parameters: parameters, Strict: tool.Strict,
			}})
		case "custom":
			description, err := customToolDescription(tool)
			if err != nil {
				return err
			}
			customTools[name] = true
			tools = append(tools, dto.ToolCallRequest{Type: "function", Function: dto.FunctionRequest{
				Name:        name,
				Description: description,
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						CustomToolInputArgument: map[string]any{"type": "string", "description": "The complete raw input for this tool."},
					},
					"required":             []string{CustomToolInputArgument},
					"additionalProperties": false,
				},
			}})
		}
		return nil
	}
	for _, tool := range declared {
		if err := add(tool, ""); err != nil {
			return nil, err
		}
	}
	return tools, nil
}

// customToolDescription keeps the grammar a freeform tool declares, because the
// model has to produce input that satisfies it without a native custom tool.
func customToolDescription(tool responsesTool) (string, error) {
	description := strings.TrimSpace(tool.Description)
	var format struct {
		Type       string `json:"type"`
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	}
	if rawJSONPresent(tool.Format) {
		var fields map[string]json.RawMessage
		if err := common.Unmarshal(tool.Format, &fields); err != nil {
			return "", errors.New("custom tool format: invalid object")
		}
		if err := common.Unmarshal(tool.Format, &format); err != nil {
			return "", errors.New("custom tool format: invalid fields")
		}
		for field := range fields {
			if field != "type" && (format.Type != "grammar" || (field != "syntax" && field != "definition")) {
				return "", errors.New("custom tool format: unsupported fields")
			}
		}
		switch format.Type {
		case "text":
		case "grammar":
			if (format.Syntax != "lark" && format.Syntax != "regex") || format.Definition == "" {
				return "", errors.New("custom tool format: unsupported or empty grammar")
			}
		default:
			return "", errors.New("custom tool format: unsupported type")
		}
	}
	if format.Type == "grammar" {
		description += fmt.Sprintf("\n\nPass the raw tool input in the %q argument. It must match this %s grammar:\n%s", CustomToolInputArgument, format.Syntax, format.Definition)
	} else {
		description += fmt.Sprintf("\n\nPass the raw tool input in the %q argument.", CustomToolInputArgument)
	}
	return description, nil
}

func convertResponsesToolChoice(raw json.RawMessage, customTools map[string]bool, toolNames map[string]ResponsesToolName) (any, error) {
	if !rawJSONPresent(raw) {
		return nil, nil
	}
	var mode string
	if common.Unmarshal(raw, &mode) == nil {
		switch mode {
		case "auto", "none":
			if len(customTools) == 0 {
				return nil, nil
			}
			return mode, nil
		case "required":
			if len(customTools) == 0 {
				return nil, errors.New("tool_choice: required needs at least one declared tool")
			}
			return mode, nil
		}
		return nil, fmt.Errorf("tool_choice: unsupported mode %q", mode)
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil {
		return nil, errors.New("tool_choice: must be a supported mode or tool selection")
	}
	for field := range fields {
		if field != "type" && field != "name" && field != "namespace" {
			return nil, errors.New("tool_choice: unsupported selection fields")
		}
	}
	var choice struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := common.Unmarshal(raw, &choice); err != nil || (choice.Type != "function" && choice.Type != "custom") || strings.TrimSpace(choice.Name) == "" {
		return nil, errors.New("tool_choice: unsupported or missing tool type/name")
	}
	name := ResponsesChatToolName(choice.Namespace, choice.Name)
	custom, declared := customTools[name]
	original := toolNames[name]
	if !declared || custom != (choice.Type == "custom") || original.Namespace != choice.Namespace || choice.Namespace != "" && original.Name != choice.Name {
		return nil, errors.New("tool_choice: selection must match a declared tool type, name and namespace")
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
}

func convertResponsesTextFormat(raw json.RawMessage) (*dto.ResponseFormat, error) {
	if !rawJSONPresent(raw) {
		return nil, nil
	}
	var text struct {
		Format map[string]json.RawMessage `json:"format"`
	}
	if err := common.Unmarshal(raw, &text); err != nil {
		return nil, fmt.Errorf("text: %w", err)
	}
	if text.Format == nil {
		return nil, nil
	}
	var formatType string
	_ = common.Unmarshal(text.Format["type"], &formatType)
	switch formatType {
	case "json_object":
		return &dto.ResponseFormat{Type: "json_object"}, nil
	case "json_schema":
		schema := map[string]json.RawMessage{}
		for key, value := range text.Format {
			if key != "type" {
				schema[key] = value
			}
		}
		encoded, err := common.Marshal(schema)
		if err != nil {
			return nil, err
		}
		return &dto.ResponseFormat{Type: "json_schema", JsonSchema: encoded}, nil
	default:
		return nil, nil
	}
}

func convertResponsesInput(raw json.RawMessage, customTools map[string]bool) ([]dto.Message, error) {
	if !rawJSONPresent(raw) {
		return nil, nil
	}
	var text string
	if common.Unmarshal(raw, &text) == nil {
		return []dto.Message{{Role: "user", Content: text}}, nil
	}
	var items []responsesInputItem
	if err := common.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}

	messages := make([]dto.Message, 0, len(items))
	// pendingCalls collects consecutive tool calls so they become one assistant
	// turn, the shape Chat Completions requires before their results.
	var pendingCalls []dto.ToolCallRequest
	flushCalls := func() error {
		if len(pendingCalls) == 0 {
			return nil
		}
		encoded, err := common.Marshal(pendingCalls)
		if err != nil {
			return err
		}
		if n := len(messages); n > 0 && messages[n-1].Role == "assistant" && len(messages[n-1].ToolCalls) == 0 {
			messages[n-1].ToolCalls = encoded
		} else {
			messages = append(messages, dto.Message{Role: "assistant", Content: nil, ToolCalls: encoded})
		}
		pendingCalls = nil
		return nil
	}
	addCall := func(callID, name, arguments string) error {
		if strings.TrimSpace(callID) == "" || strings.TrimSpace(name) == "" {
			return errors.New("input: tool call is missing call_id or name")
		}
		pendingCalls = append(pendingCalls, dto.ToolCallRequest{ID: callID, Type: "function", Function: dto.FunctionRequest{Name: name, Arguments: arguments}})
		return nil
	}

	for index, item := range items {
		switch item.Type {
		case "", "message":
			if err := flushCalls(); err != nil {
				return nil, err
			}
			message, ok, err := convertResponsesMessage(item)
			if err != nil {
				return nil, fmt.Errorf("input[%d]: %w", index, err)
			}
			if ok {
				messages = append(messages, message)
			}
		case "function_call":
			if err := addCall(item.CallID, ResponsesChatToolName(item.Namespace, item.Name), argumentsString(item.Arguments)); err != nil {
				return nil, err
			}
		case "custom_tool_call":
			name := ResponsesChatToolName(item.Namespace, item.Name)
			if _, declared := customTools[name]; !declared {
				customTools[name] = true
			}
			input, _ := rawJSONString(item.Input)
			arguments, err := common.Marshal(map[string]string{CustomToolInputArgument: input})
			if err != nil {
				return nil, err
			}
			if err := addCall(item.CallID, name, string(arguments)); err != nil {
				return nil, err
			}
		case "local_shell_call":
			if err := addCall(item.CallID, "local_shell", argumentsString(item.Action)); err != nil {
				return nil, err
			}
		case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
			if err := flushCalls(); err != nil {
				return nil, err
			}
			if strings.TrimSpace(item.CallID) == "" {
				return nil, fmt.Errorf("input[%d]: tool output is missing call_id", index)
			}
			messages = append(messages, dto.Message{Role: "tool", ToolCallId: item.CallID, Content: toolOutputText(item.Output)})
		case "item_reference":
			return nil, errors.New("item_reference is not supported for this model; send the full item in input")
		default:
			// Reasoning items and results of hosted tools carry supplier-side
			// state that a Chat Completions model can neither read nor need.
		}
	}
	if err := flushCalls(); err != nil {
		return nil, err
	}
	return messages, nil
}

func argumentsString(raw json.RawMessage) string {
	if !rawJSONPresent(raw) {
		return "{}"
	}
	var text string
	if common.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func toolOutputText(raw json.RawMessage) string {
	if !rawJSONPresent(raw) {
		return ""
	}
	var text string
	if common.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []responsesContentPart
	if common.Unmarshal(raw, &parts) == nil {
		var builder strings.Builder
		for _, part := range parts {
			if part.Text != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(part.Text)
			}
		}
		return builder.String()
	}
	return string(raw)
}

func convertResponsesMessage(item responsesInputItem) (dto.Message, bool, error) {
	role := strings.TrimSpace(item.Role)
	switch role {
	case "developer":
		role = "system"
	case "system", "user", "assistant":
	default:
		return dto.Message{}, false, fmt.Errorf("unsupported message role %q", item.Role)
	}
	if !rawJSONPresent(item.Content) {
		return dto.Message{Role: role, Content: ""}, true, nil
	}
	var text string
	if common.Unmarshal(item.Content, &text) == nil {
		return dto.Message{Role: role, Content: text}, true, nil
	}
	var parts []responsesContentPart
	if err := common.Unmarshal(item.Content, &parts); err != nil {
		return dto.Message{}, false, err
	}
	// Assistant and system turns are text in Chat Completions.
	if role != "user" {
		var builder strings.Builder
		for _, part := range parts {
			value := part.Text
			if part.Type == "refusal" {
				value = part.Refusal
			}
			if value == "" {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(value)
		}
		return dto.Message{Role: role, Content: builder.String()}, true, nil
	}
	content := make([]dto.MediaContent, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text":
			content = append(content, dto.MediaContent{Type: dto.ContentTypeText, Text: part.Text})
		case "input_image":
			url, _ := rawJSONString(part.ImageURL)
			if url == "" {
				return dto.Message{}, false, errors.New("input_image without image_url is not supported for this model")
			}
			image := map[string]any{"url": url}
			if part.Detail != "" {
				image["detail"] = part.Detail
			}
			content = append(content, dto.MediaContent{Type: dto.ContentTypeImageURL, ImageUrl: image})
		case "input_file":
			if part.FileData == "" {
				return dto.Message{}, false, errors.New("input_file without file_data is not supported for this model")
			}
			content = append(content, dto.MediaContent{Type: dto.ContentTypeFile, File: map[string]any{"file_data": part.FileData, "filename": part.Filename}})
		default:
			return dto.Message{}, false, fmt.Errorf("unsupported content type %q", part.Type)
		}
	}
	return dto.Message{Role: role, Content: content}, true, nil
}
