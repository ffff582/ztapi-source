package openaicompat

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningResponsesToChatPreservesTextAndFunctionCalls(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order []int
	}{
		{"text_first", []int{0, 1, 2}},
		{"tools_first", []int{1, 2, 0}},
		{"text_between_tools", []int{1, 0, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response dto.OpenAIResponsesResponse
			require.NoError(t, common.UnmarshalJsonStr(`{
				"id":"resp_mixed","object":"response","created_at":123,"model":"gpt-5.6-luna","status":"completed",
				"output":[
					{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Calling the tools."}]},
					{"id":"fc_echo","type":"function_call","call_id":"call_echo","name":"audit_echo","arguments":"{\"text\":\"OK\"}"},
					{"id":"fc_count","type":"function_call","name":" audit_count ","arguments":{"count":2}}
				],
				"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25,"input_tokens_details":{"cached_tokens":3}}
			}`, &response))
			original := append([]dto.ResponsesOutput(nil), response.Output...)
			for i, index := range tc.order {
				response.Output[i] = original[index]
			}
			before, err := common.Marshal(response)
			require.NoError(t, err)

			chat, usage, err := ResponsesResponseToChatCompletionsResponse(&response, "chat_mixed")
			require.NoError(t, err)
			require.Equal(t, "chat_mixed", chat.Id)
			require.Equal(t, "gpt-5.6-luna", chat.Model)
			require.EqualValues(t, 123, chat.Created)
			require.Len(t, chat.Choices, 1)
			choice := chat.Choices[0]
			require.Equal(t, "assistant", choice.Message.Role)
			require.Equal(t, "Calling the tools.", choice.Message.StringContent())
			require.Equal(t, "tool_calls", choice.FinishReason)
			calls := choice.Message.ParseToolCalls()
			require.Len(t, calls, 2, "text must not discard any pending function calls")
			require.Equal(t, "call_echo", calls[0].ID)
			require.Equal(t, "function", calls[0].Type)
			require.Equal(t, "audit_echo", calls[0].Function.Name)
			require.JSONEq(t, `{"text":"OK"}`, calls[0].Function.Arguments)
			require.Equal(t, "fc_count", calls[1].ID)
			require.Equal(t, "audit_count", calls[1].Function.Name)
			require.JSONEq(t, `{"count":2}`, calls[1].Function.Arguments)
			require.Equal(t, 20, usage.PromptTokens)
			require.Equal(t, 5, usage.CompletionTokens)
			require.Equal(t, 25, usage.TotalTokens)
			require.Equal(t, 3, usage.PromptTokensDetails.CachedTokens)
			require.Equal(t, *usage, chat.Usage)
			after, err := common.Marshal(response)
			require.NoError(t, err)
			require.Equal(t, before, after, "conversion must not mutate the supplier receipt")
		})
	}
}

func TestZTAPIReasoningResponsesToChatOrdinaryOutputsRemainIntact(t *testing.T) {
	for _, tc := range []struct {
		name, output, text, finish string
		toolCalls                  int
	}{
		{"text_only", `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]`, "OK", "stop", 0},
		{"tools_only", `[{"id":"fc_echo","type":"function_call","call_id":"call_echo","name":"audit_echo","arguments":"{}"}]`, "", "tool_calls", 1},
		{"empty_output", `[]`, "", "stop", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response dto.OpenAIResponsesResponse
			require.NoError(t, common.UnmarshalJsonStr(`{"id":"resp_control","output":`+tc.output+`}`, &response))
			chat, usage, err := ResponsesResponseToChatCompletionsResponse(&response, "chat_control")
			require.NoError(t, err)
			require.Equal(t, tc.text, chat.Choices[0].Message.StringContent())
			require.Len(t, chat.Choices[0].Message.ParseToolCalls(), tc.toolCalls)
			require.Equal(t, tc.finish, chat.Choices[0].FinishReason)
			require.Zero(t, usage.TotalTokens)
		})
	}
}
