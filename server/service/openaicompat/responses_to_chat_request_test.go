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

func TestResponsesToChatCustomFormatRejectsUnrepresentableConstraints(t *testing.T) {
	for _, format := range []string{
		`{"type":"unknown_constraint","definition":"SAFE_ONLY"}`,
		`"grammar"`,
		`{"type":"grammar","syntax":"unknown","definition":"SAFE_ONLY"}`,
		`{"type":"grammar","syntax":"lark","definition":""}`,
		`{"type":"text","definition":"SAFE_ONLY"}`,
		`{"type":"grammar","syntax":"regex","definition":"safe","extra_constraint":true}`,
	} {
		t.Run(format, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"edit","tools":[{"type":"custom","name":"edit","format":`+format+`}]}`)
			require.ErrorContains(t, err, "format")
			require.Nil(t, conversion)
		})
	}
	for _, format := range []string{`null`, `{"type":"text"}`, `{"type":"grammar","syntax":"lark","definition":"start: SAFE"}`, `{"type":"grammar","syntax":"regex","definition":"SAFE_ONLY"}`} {
		t.Run("supported/"+format, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"edit","tools":[{"type":"custom","name":"edit","format":`+format+`}]}`)
			require.NoError(t, err)
			if format != `null` && format != `{"type":"text"}` {
				require.Contains(t, conversion.Request.Tools[0].Function.Description, "SAFE")
			}
		})
	}
}

func TestResponsesToChatCustomGrammarPreservesMeaningfulWhitespace(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"edit","tools":[{"type":"custom","name":"edit","format":{"type":"grammar","syntax":"regex","definition":" SAFE_ONLY \n"}}]}`)
	require.NoError(t, err)
	require.Contains(t, conversion.Request.Tools[0].Function.Description, "grammar:\n SAFE_ONLY \n")
	blank, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"edit","tools":[{"type":"custom","name":"edit","format":{"type":"grammar","syntax":"regex","definition":" "}}]}`)
	require.NoError(t, err, "a single space is a valid regex, not an absent definition")
	require.Contains(t, blank.Request.Tools[0].Function.Description, "grammar:\n ")
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

func TestResponsesToChatRejectsInvalidToolHistory(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"orphan_function_output_with_call_id", `[{"type":"function_call_output","call_id":"orphan","output":"result"}]`},
		{"orphan_custom_output_with_call_id", `[{"type":"custom_tool_call_output","call_id":"orphan","output":"result"}]`},
		{"orphan_shell_output_with_call_id", `[{"type":"local_shell_call_output","call_id":"orphan","output":"result"}]`},
		{"duplicate_parallel_call_id", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"type":"custom_tool_call","call_id":"a","name":"edit","input":"patch"},{"type":"function_call_output","call_id":"a","output":"result"}]`},
		{"reused_call_id_across_completed_turns", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"one"},{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"two"}]`},
		{"duplicate_result", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"one"},{"type":"function_call_output","call_id":"a","output":"two"}]`},
		{"missing_result", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"}]`},
		{"partial_results", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"type":"function_call","call_id":"b","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"one"}]`},
		{"user_before_result", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"role":"user","content":"interrupt"},{"type":"function_call_output","call_id":"a","output":"result"}]`},
		{"assistant_before_result", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"role":"assistant","content":"interrupt"},{"type":"function_call_output","call_id":"a","output":"result"}]`},
		{"system_before_result", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"role":"developer","content":"interrupt"},{"type":"function_call_output","call_id":"a","output":"result"}]`},
		{"user_between_parallel_results", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"type":"function_call","call_id":"b","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"b","output":"two"},{"role":"user","content":"interrupt"},{"type":"function_call_output","call_id":"a","output":"one"}]`},
		{"next_call_before_all_results", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"type":"function_call","call_id":"b","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"one"},{"type":"function_call","call_id":"c","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"b","output":"two"},{"type":"function_call_output","call_id":"c","output":"three"}]`},
		{"unknown_result_id", `[{"type":"function_call","call_id":"a","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"b","output":"result"}]`},
		{"result_before_call", `[{"type":"function_call_output","call_id":"a","output":"result"},{"type":"function_call","call_id":"a","name":"read","arguments":"{}"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":`+tc.input+`}`)
			require.Error(t, err, "invalid tool histories must not produce an upstream request")
			require.Nil(t, conversion)
		})
	}
}

func TestResponsesToChatKeepsCompleteStatelessToolHistory(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":[
		{"role":"developer","content":"Use the workspace."},
		{"role":"assistant","content":"working"},
		{"type":"function_call","call_id":"read_1","name":"read","arguments":"{\"path\":\"one\"}"},
		{"type":"function_call","call_id":"read_2","name":"read","arguments":"{\"path\":\"two\"}"},
		{"type":"function_call_output","call_id":"read_2","output":"second"},
		{"type":"function_call_output","call_id":"read_1","output":"first"},
		{"type":"custom_tool_call","call_id":"edit_1","name":"edit","input":"PATCH"},
		{"type":"custom_tool_call_output","call_id":"edit_1","output":"patched"},
		{"type":"local_shell_call","call_id":"shell_1","action":{"command":["echo","OK"]}},
		{"type":"local_shell_call_output","call_id":"shell_1","output":"OK"},
		{"role":"user","content":[{"type":"input_text","text":"continue"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}
	]}`)
	require.NoError(t, err)
	messages := conversion.Request.Messages
	require.Len(t, messages, 9)
	require.Equal(t, "system", messages[0].Role)
	require.Equal(t, "working", messages[1].Content)
	calls := messages[1].ParseToolCalls()
	require.Len(t, calls, 2)
	require.Equal(t, "read_1", calls[0].ID)
	require.Equal(t, "read_2", calls[1].ID)
	require.Equal(t, "read_2", messages[2].ToolCallId)
	require.Equal(t, "second", messages[2].Content)
	require.Equal(t, "read_1", messages[3].ToolCallId)
	require.Equal(t, "first", messages[3].Content)
	require.Equal(t, "edit_1", messages[4].ParseToolCalls()[0].ID)
	require.JSONEq(t, `{"input":"PATCH"}`, messages[4].ParseToolCalls()[0].Function.Arguments)
	require.Equal(t, "patched", messages[5].Content)
	require.Equal(t, "shell_1", messages[6].ParseToolCalls()[0].ID)
	require.Equal(t, "OK", messages[7].Content)
	trailing := messages[8].Content.([]dto.MediaContent)
	require.Len(t, trailing, 2)
	require.Equal(t, "continue", trailing[0].Text)
	require.Equal(t, "data:image/png;base64,AA==", trailing[1].GetImageMedia().Url)
}

func TestResponsesToChatPreservesFunctionStrictOnWire(t *testing.T) {
	for _, strict := range []string{"true", "false", "absent", "null"} {
		t.Run(strict, func(t *testing.T) {
			tool := `{"type":"function","name":"read","parameters":{"type":"object","properties":{},"additionalProperties":false}}`
			if strict != "absent" {
				tool = tool[:len(tool)-1] + `,"strict":` + strict + `}`
			}
			for _, declaration := range []struct{ name, fields string }{
				{"top level", `"tools":[` + tool + `],"input":"read"`},
				{"namespace", `"tools":[{"type":"namespace","name":"files","tools":[` + tool + `]}],"input":"read"`},
				{"additional tools", `"input":[{"type":"additional_tools","tools":[` + tool + `]},{"role":"user","content":"read"}]`},
			} {
				t.Run(declaration.name, func(t *testing.T) {
					conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5",`+declaration.fields+`}`)
					require.NoError(t, err)
					encoded, err := common.Marshal(conversion.Request)
					require.NoError(t, err)
					var sent struct {
						Tools []struct {
							Function map[string]any `json:"function"`
						} `json:"tools"`
					}
					require.NoError(t, common.Unmarshal(encoded, &sent))
					require.Len(t, sent.Tools, 1)
					if strict == "absent" || strict == "null" {
						require.NotContains(t, sent.Tools[0].Function, "strict", "do not invent strict guarantees")
					} else {
						require.Contains(t, sent.Tools[0].Function, "strict", "explicit strict must reach the chat supplier")
						require.Equal(t, strict == "true", sent.Tools[0].Function["strict"])
					}
				})
			}
		})
	}
}

func TestResponsesToChatRejectsUnsupportedToolDeclarations(t *testing.T) {
	for _, kind := range []string{"web_search", "web_search_preview", "file_search", "code_interpreter", "computer_use_preview", "image_generation", "mcp", "local_shell", "unknown_tool", ""} {
		t.Run(kind, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"hi","tools":[{"type":"`+kind+`"}]}`)
			require.ErrorContains(t, err, "tools:")
			require.Nil(t, conversion, "unsupported declarations must not produce a degraded request")
		})
	}
	for name, fields := range map[string]string{
		"mixed hosted and function": `"tools":[{"type":"function","name":"read"},{"type":"web_search"}],"input":"hi"`,
		"hosted with none choice":   `"tools":[{"type":"web_search"}],"tool_choice":"none","input":"hi"`,
		"additional hosted":         `"input":[{"type":"additional_tools","tools":[{"type":"web_search"}]},{"role":"user","content":"hi"}]`,
		"hosted before update":      `"tools":[{"type":"web_search"}],"input":[{"type":"additional_tools","tools":[{"type":"function","name":"read"}]},{"role":"user","content":"hi"}]`,
		"namespace hosted":          `"tools":[{"type":"namespace","name":"files","tools":[{"type":"web_search"}]}],"input":"hi"`,
		"namespace unknown":         `"tools":[{"type":"namespace","name":"files","tools":[{"type":"unknown_tool","name":"read"}]}],"input":"hi"`,
		"nested namespace":          `"tools":[{"type":"namespace","name":"files","tools":[{"type":"namespace","name":"inner","tools":[{"type":"function","name":"read"}]}]}],"input":"hi"`,
		"function nested tools":     `"tools":[{"type":"function","name":"read","tools":[{"type":"web_search"}]}],"input":"hi"`,
		"custom nested tools":       `"tools":[{"type":"namespace","name":"files","tools":[{"type":"custom","name":"edit","tools":[{"type":"function","name":"hidden"}]}]}],"input":"hi"`,
		"custom strict":             `"tools":[{"type":"custom","name":"edit","strict":true}],"input":"hi"`,
		"namespace strict":          `"tools":[{"type":"namespace","name":"files","strict":false,"tools":[{"type":"function","name":"read"}]}],"input":"hi"`,
	} {
		t.Run(name, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5",`+fields+`}`)
			require.ErrorContains(t, err, "tools:")
			require.Nil(t, conversion)
		})
	}
}

func TestResponsesToChatRejectsUnsupportedToolChoice(t *testing.T) {
	for name, choice := range map[string]string{
		"unknown mode":     `"any"`,
		"hosted":           `{"type":"web_search"}`,
		"allowed subset":   `{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"read"}]}`,
		"missing name":     `{"type":"function"}`,
		"undeclared name":  `{"type":"function","name":"missing"}`,
		"wrong namespace":  `{"type":"function","namespace":"other","name":"read"}`,
		"wrong type":       `{"type":"custom","name":"read"}`,
		"extra constraint": `{"type":"function","name":"read","mode":"required"}`,
		"boolean":          `true`,
		"array":            `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"read","tools":[{"type":"function","name":"read"}],"tool_choice":`+choice+`}`)
			require.ErrorContains(t, err, "tool_choice:")
			require.Nil(t, conversion)
		})
	}
	for _, choice := range []string{`"required"`, `"unsupported"`, `{"type":"function","name":"read"}`, `{"type":"web_search"}`} {
		t.Run("without tools "+choice, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"read","tool_choice":`+choice+`}`)
			require.ErrorContains(t, err, "tool_choice:")
			require.Nil(t, conversion)
		})
	}
}

func TestResponsesToChatKeepsSupportedToolChoices(t *testing.T) {
	for _, mode := range []string{"auto", "none", "required"} {
		t.Run(mode, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"read","tools":[{"type":"function","name":"read"}],"tool_choice":"`+mode+`"}`)
			require.NoError(t, err)
			require.Equal(t, mode, conversion.Request.ToolChoice)
		})
	}
	for _, mode := range []string{"auto", "none"} {
		t.Run("without tools "+mode, func(t *testing.T) {
			conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"read","tool_choice":"`+mode+`"}`)
			require.NoError(t, err)
			require.Nil(t, conversion.Request.ToolChoice)
		})
	}
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"edit","tools":[{"type":"custom","name":"edit"}],"tool_choice":{"type":"custom","name":"edit"}}`)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"type": "function", "function": map[string]any{"name": "edit"}}, conversion.Request.ToolChoice)
}
