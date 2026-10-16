package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestShouldResponsesUseChatCompletionsCoversChatOnlySuppliers(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "Claude-Sonnet-5", "kimi-k2.6"} {
		require.True(t, ShouldResponsesUseChatCompletions(model), model)
	}
	for _, model := range []string{"gpt-5.5", "gemini-3.1-pro-preview", "deepseek-v4-pro", "glm-5.3", ""} {
		require.False(t, ShouldResponsesUseChatCompletions(model), model)
	}
}

func convertResponsesForTest(t *testing.T, body string) (*ResponsesChatConversion, error) {
	t.Helper()
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalJsonStr(body, &request))
	return ResponsesRequestToChatCompletionsRequest(&request)
}

func TestResponsesToChatAcceptsPlainTextInputAndStructuredOutput(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"Hello","temperature":0.2,
		"text":{"format":{"type":"json_schema","name":"answer","strict":true,"schema":{"type":"object"}}},
		"reasoning":{"effort":"high","summary":"auto"}}`)
	require.NoError(t, err)
	chat := conversion.Request
	require.Equal(t, []dto.Message{{Role: "user", Content: "Hello"}}, chat.Messages)
	require.Equal(t, 0.2, *chat.Temperature)
	require.Equal(t, "high", chat.ReasoningEffort)
	require.Nil(t, chat.Tools)
	require.Nil(t, chat.ToolChoice, "tool_choice without tools would be rejected by chat suppliers")
	require.Equal(t, "json_schema", chat.ResponseFormat.Type)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(chat.ResponseFormat.JsonSchema, &schema))
	require.Equal(t, map[string]any{"name": "answer", "strict": true, "schema": map[string]any{"type": "object"}}, schema)
}

func TestResponsesToChatKeepsUserImages(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":[{"role":"user","content":[
		{"type":"input_text","text":"What is this?"},{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"high"}]}]}`)
	require.NoError(t, err)
	content := conversion.Request.Messages[0].Content.([]dto.MediaContent)
	require.Equal(t, dto.MediaContent{Type: dto.ContentTypeText, Text: "What is this?"}, content[0])
	require.Equal(t, dto.ContentTypeImageURL, content[1].Type)
	require.Equal(t, map[string]any{"url": "data:image/png;base64,AAAA", "detail": "high"}, content[1].ImageUrl)
}

func TestResponsesToChatRejectsWhatChatCannotCarry(t *testing.T) {
	for name, body := range map[string]string{
		"item reference": `{"model":"claude-opus-5","input":[{"type":"item_reference","id":"msg_1"}]}`,
		"stored prompt":  `{"model":"claude-opus-5","input":"hi","prompt":{"id":"pmpt_1"}}`,
		"conversation":   `{"model":"claude-opus-5","input":"hi","conversation":"conv_1"}`,
		"file reference": `{"model":"claude-opus-5","input":[{"role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`,
		"orphan output":  `{"model":"claude-opus-5","input":[{"type":"function_call_output","output":"x"}]}`,
		"empty input":    `{"model":"claude-opus-5"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := convertResponsesForTest(t, body)
			require.Error(t, err)
		})
	}
}

func TestResponsesToChatGroupsConsecutiveToolCallsIntoOneTurn(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"kimi-k2.6","input":[{"role":"user","content":"Read both."},
		{"type":"function_call","call_id":"a","name":"read","arguments":"{\"path\":\"a\"}"},
		{"type":"function_call","call_id":"b","name":"read","arguments":"{\"path\":\"b\"}"},
		{"type":"function_call_output","call_id":"a","output":[{"type":"input_text","text":"A"}]},
		{"type":"function_call_output","call_id":"b","output":"B"}],
		"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"read"}}`)
	require.NoError(t, err)
	messages := conversion.Request.Messages
	require.Len(t, messages, 4)
	var calls []dto.ToolCallRequest
	require.NoError(t, json.Unmarshal(messages[1].ToolCalls, &calls))
	require.Len(t, calls, 2, "parallel calls must share one assistant turn")
	require.Nil(t, messages[1].Content)
	require.Equal(t, "A", messages[2].Content)
	require.Equal(t, "b", messages[3].ToolCallId)
	require.Equal(t, map[string]any{"type": "function", "function": map[string]any{"name": "read"}}, conversion.Request.ToolChoice)
}
