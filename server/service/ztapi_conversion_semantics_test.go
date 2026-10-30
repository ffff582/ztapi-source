package service

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func conversionSemanticsInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "test-model",
	}}
}

func conversionSemanticsJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := common.Marshal(value)
	require.NoError(t, err)
	return body
}

func conversionSemanticsInvalidRequest(t *testing.T, err error) {
	t.Helper()
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	require.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
	require.True(t, types.IsSkipRetryError(apiErr))
}

func TestZTAPIConversionSemanticsAnthropicToolChoice(t *testing.T) {
	for _, choice := range []struct {
		name, input, want string
	}{
		{"auto", `"type":"auto"`, `"auto"`},
		{"none", `"type":"none"`, `"none"`},
		{"any", `"type":"any"`, `"required"`},
		{"tool", `"type":"tool","name":"lookup"`, `{"type":"function","function":{"name":"lookup"}}`},
	} {
		for _, parallel := range []struct {
			name, input string
			want        *bool
		}{
			{"absent", "", nil},
			{"explicit_false", `,"disable_parallel_tool_use":false`, common.GetPointer(true)},
			{"explicit_true", `,"disable_parallel_tool_use":true`, common.GetPointer(false)},
		} {
			t.Run(choice.name+"/"+parallel.name, func(t *testing.T) {
				var input dto.ClaudeRequest
				require.NoError(t, common.UnmarshalJsonStr(`{"model":"test-model","tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{`+choice.input+parallel.input+`},"messages":[{"role":"user","content":"hello"}]}`, &input))
				converted, err := ClaudeToOpenAIRequest(input, conversionSemanticsInfo())
				require.NoError(t, err)
				body := conversionSemanticsJSON(t, converted)
				require.True(t, gjson.GetBytes(body, "tool_choice").Exists(), "tool_choice must not disappear")
				require.JSONEq(t, choice.want, gjson.GetBytes(body, "tool_choice").Raw)
				if parallel.want == nil {
					require.False(t, gjson.GetBytes(body, "parallel_tool_calls").Exists())
				} else {
					require.True(t, gjson.GetBytes(body, "parallel_tool_calls").Exists())
					require.Equal(t, *parallel.want, gjson.GetBytes(body, "parallel_tool_calls").Bool())
				}
			})
		}
	}
}

func TestZTAPIConversionSemanticsAnthropicTypedToolChoiceFalse(t *testing.T) {
	choice := dto.ClaudeToolChoice{Type: "auto", DisableParallelToolUse: false}
	for _, value := range []any{choice, &choice} {
		converted, err := ClaudeToOpenAIRequest(dto.ClaudeRequest{ToolChoice: value}, conversionSemanticsInfo())
		require.NoError(t, err)
		require.NotNil(t, converted.ParallelTooCalls, "typed false must not disappear")
		require.True(t, *converted.ParallelTooCalls)
	}
}

func TestZTAPIConversionSemanticsAnthropicRejectsInvalidToolChoice(t *testing.T) {
	for _, choice := range []string{
		`{"type":"tool","name":"undeclared"}`,
		`{"type":"tool"}`,
		`{"type":"unexpected"}`,
		`{"type":"auto","disable_parallel_tool_use":"false"}`,
	} {
		t.Run(choice, func(t *testing.T) {
			var input dto.ClaudeRequest
			require.NoError(t, common.UnmarshalJsonStr(`{"tools":[{"name":"lookup","input_schema":{}}],"tool_choice":`+choice+`}`, &input))
			converted, err := ClaudeToOpenAIRequest(input, conversionSemanticsInfo())
			require.ErrorContains(t, err, "tool_choice")
			conversionSemanticsInvalidRequest(t, err)
			require.Nil(t, converted)
		})
	}
}

func TestZTAPIConversionSemanticsAnthropicPreservesMixedContentAndResultOrder(t *testing.T) {
	var input dto.ClaudeRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"messages":[
		{"role":"assistant","content":[{"type":"text","text":"before","cache_control":{"type":"ephemeral"}},{"type":"tool_use","id":"a","name":"lookup","input":{"n":1}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}},{"type":"tool_use","id":"b","name":"lookup","input":{"n":2}},{"type":"text","text":"after"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"second"},{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"first"}]},{"type":"text","text":"prefix"},{"type":"text","text":"middle"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"BB=="}}]}
	]}`, &input))
	before := conversionSemanticsJSON(t, input)
	converted, err := ClaudeToOpenAIRequest(input, conversionSemanticsInfo())
	require.NoError(t, err)
	require.Len(t, converted.Messages, 4)
	assistant := converted.Messages[0]
	require.Equal(t, "assistant", assistant.Role)
	require.Len(t, assistant.ParseToolCalls(), 2)
	content := assistant.ParseContent()
	require.Len(t, content, 3)
	require.Equal(t, "before", content[0].Text)
	require.JSONEq(t, `{"type":"ephemeral"}`, string(content[0].CacheControl))
	require.Equal(t, "data:image/png;base64,AA==", content[1].GetImageMedia().Url)
	require.Equal(t, "after", content[2].Text)
	require.Equal(t, "tool", converted.Messages[1].Role)
	require.Equal(t, "b", converted.Messages[1].ToolCallId)
	require.Equal(t, "second", converted.Messages[1].StringContent())
	require.Equal(t, "lookup", *converted.Messages[1].Name)
	require.Equal(t, "tool", converted.Messages[2].Role)
	require.Equal(t, "a", converted.Messages[2].ToolCallId)
	require.JSONEq(t, `[{"type":"text","text":"first"}]`, converted.Messages[2].StringContent())
	require.Equal(t, "user", converted.Messages[3].Role)
	trailing := converted.Messages[3].ParseContent()
	require.Len(t, trailing, 3)
	require.Equal(t, "prefix", trailing[0].Text)
	require.Equal(t, "middle", trailing[1].Text)
	require.Equal(t, "data:image/png;base64,BB==", trailing[2].GetImageMedia().Url)
	require.JSONEq(t, string(before), string(conversionSemanticsJSON(t, input)))
}

func TestZTAPIConversionSemanticsAnthropicRejectsIncompleteOrInterruptedToolTurns(t *testing.T) {
	for _, tc := range []struct{ name, messages string }{
		{"missing_result", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]}]`},
		{"orphan_chat_tool_message", `[{"role":"tool","content":"orphan"}]`},
		{"tool_call_in_user_message", `[{"role":"user","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"result"}]}]`},
		{"partial_results", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"alpha","input":{}},{"type":"tool_use","id":"b","name":"beta","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"one"}]}]`},
		{"text_before_result", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"text","text":"prefix"},{"type":"tool_result","tool_use_id":"a","content":"result"}]}]`},
		{"media_before_result", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}},{"type":"tool_result","tool_use_id":"a","content":"result"}]}]`},
		{"text_between_results", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"alpha","input":{}},{"type":"tool_use","id":"b","name":"beta","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"second"},{"type":"text","text":"middle"},{"type":"tool_result","tool_use_id":"a","content":"first"}]}]`},
		{"user_message_before_result", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]},{"role":"user","content":"interrupt"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"result"}]}]`},
		{"assistant_before_result", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]},{"role":"assistant","content":"interrupt"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"result"}]}]`},
		{"next_call_before_result", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"alpha","input":{}}]},{"role":"assistant","content":[{"type":"tool_use","id":"b","name":"beta","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"one"},{"type":"tool_result","tool_use_id":"b","content":"two"}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input dto.ClaudeRequest
			require.NoError(t, common.UnmarshalJsonStr(`{"messages":`+tc.messages+`}`, &input))
			before := conversionSemanticsJSON(t, input)
			converted, err := ClaudeToOpenAIRequest(input, conversionSemanticsInfo())
			require.Error(t, err, "incomplete or interrupted tool turns must not reach Chat")
			conversionSemanticsInvalidRequest(t, err)
			require.Nil(t, converted)
			require.JSONEq(t, string(before), string(conversionSemanticsJSON(t, input)))
		})
	}
}

func TestZTAPIConversionSemanticsAnthropicRejectsInvalidToolHistory(t *testing.T) {
	for _, tc := range []struct{ name, messages string }{
		{"orphan", `[{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","content":"result"}]}]`},
		{"future_call", `[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"result"}]},{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]}]`},
		{"duplicate_call", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}},{"type":"tool_use","id":"a","name":"lookup","input":{}}]}]`},
		{"duplicate_result", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"one"},{"type":"tool_result","tool_use_id":"a","content":"two"}]}]`},
		{"missing_call_id", `[{"role":"assistant","content":[{"type":"tool_use","name":"lookup","input":{}}]}]`},
		{"mismatched_result_name", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","name":"other","content":"result"}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input dto.ClaudeRequest
			require.NoError(t, common.UnmarshalJsonStr(`{"messages":`+tc.messages+`}`, &input))
			converted, err := ClaudeToOpenAIRequest(input, conversionSemanticsInfo())
			require.Error(t, err)
			conversionSemanticsInvalidRequest(t, err)
			require.Nil(t, converted)
		})
	}
}

func TestZTAPIConversionSemanticsGeminiAssociatesToolResults(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		wantIDs        []string
		wantResults    []string
	}{
		{"separate_content", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"n":1}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"n":1}}}]}]`, []string{"call_1"}, []string{"call_1"}},
		{"parallel_reverse_results", `[{"role":"model","parts":[{"functionCall":{"name":"alpha","args":{}}},{"functionCall":{"name":"beta","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"beta","response":{"n":2}}},{"functionResponse":{"name":"alpha","response":{"n":1}}}]}]`, []string{"call_1", "call_2"}, []string{"call_2", "call_1"}},
		{"repeated_name_across_turns", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"n":1}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"n":1}}}]},{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"n":2}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"n":2}}}]}]`, []string{"call_1", "call_2"}, []string{"call_1", "call_2"}},
		{"split_result_contents", `[{"role":"model","parts":[{"functionCall":{"name":"alpha","args":{}}},{"functionCall":{"name":"beta","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"beta","response":{"n":2}}}]},{"role":"function","parts":[{"functionResponse":{"name":"alpha","response":{"n":1}}}]}]`, []string{"call_1", "call_2"}, []string{"call_2", "call_1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input dto.GeminiChatRequest
			require.NoError(t, common.UnmarshalJsonStr(`{"contents":`+tc.contents+`}`, &input))
			before := conversionSemanticsJSON(t, input)
			converted, err := GeminiToOpenAIRequest(&input, conversionSemanticsInfo())
			require.NoError(t, err)
			var callIDs, resultIDs []string
			for _, message := range converted.Messages {
				for _, call := range message.ParseToolCalls() {
					callIDs = append(callIDs, call.ID)
				}
				if message.Role == "tool" {
					resultIDs = append(resultIDs, message.ToolCallId)
				}
			}
			require.Equal(t, tc.wantIDs, callIDs)
			require.Equal(t, tc.wantResults, resultIDs)
			for _, message := range converted.Messages {
				if message.Role == "tool" {
					require.NotEmpty(t, message.Name)
					require.NotEmpty(t, message.StringContent())
				}
			}
			require.JSONEq(t, string(before), string(conversionSemanticsJSON(t, input)))
		})
	}
}

func TestZTAPIConversionSemanticsGeminiPreservesMixedContentAndResultOrder(t *testing.T) {
	var input dto.GeminiChatRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"contents":[
		{"role":"model","parts":[{"text":"before"},{"functionCall":{"name":"lookup","args":{"n":1}}},{"inlineData":{"mimeType":"image/png","data":"AA=="}},{"text":"after"}]},
		{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"n":1}}},{"text":"prefix"},{"fileData":{"mimeType":"image/png","fileUri":"https://example.invalid/image.png"}},{"text":"suffix"}]}
	]}`, &input))
	converted, err := GeminiToOpenAIRequest(&input, conversionSemanticsInfo())
	require.NoError(t, err)
	require.Len(t, converted.Messages, 3)
	content := converted.Messages[0].ParseContent()
	require.Len(t, content, 3)
	require.Equal(t, "before", content[0].Text)
	require.Equal(t, "data:image/png;base64,AA==", content[1].GetImageMedia().Url)
	require.Equal(t, "after", content[2].Text)
	require.Len(t, converted.Messages[0].ParseToolCalls(), 1)
	require.Equal(t, "tool", converted.Messages[1].Role)
	require.Equal(t, "call_1", converted.Messages[1].ToolCallId)
	require.JSONEq(t, `{"n":1}`, converted.Messages[1].StringContent())
	require.Equal(t, "user", converted.Messages[2].Role)
	last := converted.Messages[2].ParseContent()
	require.Len(t, last, 3)
	require.Equal(t, "prefix", last[0].Text)
	require.Equal(t, "https://example.invalid/image.png", last[1].GetImageMedia().Url)
	require.Equal(t, "suffix", last[2].Text)
}

func TestZTAPIConversionSemanticsGeminiRejectsIncompleteOrInterruptedToolTurns(t *testing.T) {
	for _, tc := range []struct{ name, contents string }{
		{"missing_result", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]}]`},
		{"tool_call_in_user_message", `[{"role":"user","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]`},
		{"partial_results", `[{"role":"model","parts":[{"functionCall":{"name":"alpha","args":{}}},{"functionCall":{"name":"beta","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"alpha","response":{}}}]}]`},
		{"text_before_result", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"text":"prefix"},{"functionResponse":{"name":"lookup","response":{}}}]}]`},
		{"media_before_result", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}},{"functionResponse":{"name":"lookup","response":{}}}]}]`},
		{"text_between_results", `[{"role":"model","parts":[{"functionCall":{"name":"alpha","args":{}}},{"functionCall":{"name":"beta","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"beta","response":{}}},{"text":"middle"},{"functionResponse":{"name":"alpha","response":{}}}]}]`},
		{"user_message_before_result", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"text":"interrupt"}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]`},
		{"assistant_before_result", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"model","parts":[{"text":"interrupt"}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]`},
		{"next_call_before_result", `[{"role":"model","parts":[{"functionCall":{"name":"alpha","args":{}}}]},{"role":"model","parts":[{"functionCall":{"name":"beta","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"alpha","response":{}}},{"functionResponse":{"name":"beta","response":{}}}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input dto.GeminiChatRequest
			require.NoError(t, common.UnmarshalJsonStr(`{"contents":`+tc.contents+`}`, &input))
			before := conversionSemanticsJSON(t, input)
			converted, err := GeminiToOpenAIRequest(&input, conversionSemanticsInfo())
			require.Error(t, err, "incomplete or interrupted tool turns must not reach Chat")
			conversionSemanticsInvalidRequest(t, err)
			require.Nil(t, converted)
			require.JSONEq(t, string(before), string(conversionSemanticsJSON(t, input)))
		})
	}
}

func TestZTAPIConversionSemanticsGeminiRejectsUnrepresentableToolHistory(t *testing.T) {
	for _, tc := range []struct{ name, contents string }{
		{"orphan", `[{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]`},
		{"future_call", `[{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]},{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]}]`},
		{"missing_result", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]}]`},
		{"unknown_name", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"other","response":{}}}]}]`},
		{"ambiguous_parallel_name", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"n":1}}},{"functionCall":{"name":"lookup","args":{"n":2}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"n":2}}},{"functionResponse":{"name":"lookup","response":{"n":1}}}]}]`},
		{"duplicate_result", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}},{"functionResponse":{"name":"lookup","response":{}}}]}]`},
		{"response_id_without_call_id_dto", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"id":"external-id","name":"lookup","response":{}}}]}]`},
		{"missing_call_name", `[{"role":"model","parts":[{"functionCall":{"args":{}}}]}]`},
		{"missing_response_name", `[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"response":{}}}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input dto.GeminiChatRequest
			require.NoError(t, common.UnmarshalJsonStr(`{"contents":`+tc.contents+`}`, &input))
			converted, err := GeminiToOpenAIRequest(&input, conversionSemanticsInfo())
			require.Error(t, err)
			conversionSemanticsInvalidRequest(t, err)
			require.Nil(t, converted)
		})
	}
}

func TestZTAPIConversionSemanticsGeminiRejectsAmbiguousParts(t *testing.T) {
	for _, part := range []string{
		`{"text":"text","functionCall":{"name":"lookup","args":{}}}`,
		`{"inlineData":{"mimeType":"image/png","data":"AA=="},"functionCall":{"name":"lookup","args":{}}}`,
	} {
		var input dto.GeminiChatRequest
		require.NoError(t, common.UnmarshalJsonStr(`{"contents":[{"role":"model","parts":[`+part+`]}]}`, &input))
		converted, err := GeminiToOpenAIRequest(&input, conversionSemanticsInfo())
		require.Error(t, err, "a conflicting part must not silently discard its functionCall")
		conversionSemanticsInvalidRequest(t, err)
		require.Nil(t, converted)
	}
}

func TestZTAPIConversionSemanticsGeminiStopSequencesBounded(t *testing.T) {
	for _, tc := range []struct {
		stops, want []string
		reject      bool
	}{
		{[]string{}, nil, false},
		{[]string{"a"}, []string{"a"}, false},
		{[]string{"a", "b", "c"}, []string{"a", "b", "c"}, false},
		{[]string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d"}, false},
		{[]string{"a", "b", "c", "d", "e"}, nil, true},
	} {
		t.Run(fmt.Sprintf("len_%d", len(tc.stops)), func(t *testing.T) {
			input := &dto.GeminiChatRequest{GenerationConfig: dto.GeminiChatGenerationConfig{StopSequences: tc.stops[:len(tc.stops):len(tc.stops)]}}
			var converted *dto.GeneralOpenAIRequest
			var err error
			require.NotPanics(t, func() { converted, err = GeminiToOpenAIRequest(input, conversionSemanticsInfo()) })
			if tc.reject {
				require.Error(t, err, "unsupported stop constraints must not be truncated")
				conversionSemanticsInvalidRequest(t, err)
				require.Nil(t, converted)
				require.Equal(t, tc.stops, input.GenerationConfig.StopSequences)
				return
			}
			require.NoError(t, err)
			if tc.want == nil {
				require.Nil(t, converted.Stop)
			} else {
				require.Equal(t, tc.want, converted.Stop)
			}
			require.Equal(t, tc.stops, input.GenerationConfig.StopSequences)
		})
	}
}

func TestZTAPIConversionSemanticsForwardedExplicitZeroAndFalse(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		var input dto.ClaudeRequest
		require.NoError(t, common.UnmarshalJsonStr(`{"temperature":0,"top_p":0,"top_k":0,"max_tokens":0,"stream":false}`, &input))
		converted, err := ClaudeToOpenAIRequest(input, conversionSemanticsInfo())
		require.NoError(t, err)
		body := conversionSemanticsJSON(t, converted)
		for _, field := range []string{"temperature", "top_p", "top_k", "max_tokens", "stream"} {
			require.True(t, gjson.GetBytes(body, field).Exists(), field)
			require.Contains(t, []string{"0", "false"}, gjson.GetBytes(body, field).Raw)
		}
		require.Nil(t, converted.ParallelTooCalls)
		require.Nil(t, converted.ToolChoice)
	})
	t.Run("gemini", func(t *testing.T) {
		var input dto.GeminiChatRequest
		require.NoError(t, common.UnmarshalJsonStr(`{"generationConfig":{"temperature":0,"topP":0,"topK":0,"maxOutputTokens":0,"candidateCount":0}}`, &input))
		converted, err := GeminiToOpenAIRequest(&input, conversionSemanticsInfo())
		require.NoError(t, err)
		body := conversionSemanticsJSON(t, converted)
		for _, field := range []string{"temperature", "top_p", "top_k", "max_tokens", "n", "stream"} {
			require.True(t, gjson.GetBytes(body, field).Exists(), field)
			require.Contains(t, []string{"0", "false"}, gjson.GetBytes(body, field).Raw)
		}
		absent, err := GeminiToOpenAIRequest(&dto.GeminiChatRequest{}, conversionSemanticsInfo())
		require.NoError(t, err)
		absentBody := conversionSemanticsJSON(t, absent)
		for _, field := range []string{"temperature", "top_p", "top_k", "max_tokens", "n"} {
			require.False(t, gjson.GetBytes(absentBody, field).Exists(), field)
		}
	})
}
