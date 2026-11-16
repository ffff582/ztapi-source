package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func parallelMessages(t *testing.T, raw string) []dto.Message {
	t.Helper()
	var messages []dto.Message
	require.NoError(t, common.UnmarshalJsonStr(raw, &messages))
	return messages
}

func TestSerializeCompleteParallelToolTurnsLosslessAndImmutable(t *testing.T) {
	messages := parallelMessages(t, `[
		{"role":"system","content":[{"type":"text","text":"scope","cache_control":{"type":"ephemeral"}}]},
		{"role":"user","content":[{"type":"text","text":"question"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},
		{"role":"assistant","name":"worker","prefix":false,"reasoning_content":"thought","reasoning":"other thought","content":[{"type":"text","text":"checking","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tool_calls":[
			{"id":"a","type":"function","function":{"name":"same_function","arguments":" { \"action\": \"search\", \"large\": 9007199254740993 } ","strict":false,"vendor_function":{"n":9007199254740993}},"cache_control":{"type":"ephemeral","ttl":"1h"},"vendor_extension":{"enabled":false,"n":9007199254740993}},
			{"id":"b","type":"function","function":{"name":"same_function","arguments":"{\"action\":\"open_page\",\"text\":\"\\u4e2d\\n\"}"},"cache_control":{"type":"ephemeral"}},
			{"id":"c","type":"custom","custom":{"name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},"cache_control":{"type":"ephemeral"}}
		]},
		{"role":"tool","name":"third","tool_call_id":"c","content":""},
		{"role":"tool","tool_call_id":"a","content":[{"type":"text","text":"search result","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AQ=="}}]},
		{"role":"tool","name":"second","tool_call_id":"b","content":"page result\nwith original bytes"},
		{"role":"assistant","content":"after"},
		{"role":"assistant","content":"single call","tool_calls":[{"id":"d","type":"function","function":{"name":"local","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"d","content":null},
		{"role":"user","content":"continue"}
	]`)
	before, err := common.Marshal(messages)
	require.NoError(t, err)
	var calls []json.RawMessage
	require.NoError(t, common.Unmarshal(messages[2].ToolCalls, &calls))
	got, err := SerializeCompleteParallelToolTurns(messages)
	require.NoError(t, err)
	require.NoError(t, ValidateChatToolHistory(got))
	require.Len(t, got, len(messages)+2)
	require.Equal(t, messages[:2], got[:2])
	first := messages[2]
	first.ToolCalls = got[2].ToolCalls
	require.Equal(t, first, got[2], "retain assistant content, reasoning, name, prefix and content cache once")
	for i, resultIndex := range []int{4, 5, 3} {
		var split []json.RawMessage
		require.NoError(t, common.Unmarshal(got[2+2*i].ToolCalls, &split))
		require.Len(t, split, 1)
		require.Equal(t, string(calls[i]), string(split[0]), "do not reconstruct or coerce raw call JSON, arguments or cache metadata")
		require.Equal(t, messages[resultIndex], got[3+2*i], "retain every result unchanged and pair by ID, not arrival order")
		if i > 0 {
			require.Equal(t, dto.Message{Role: "assistant", ToolCalls: got[2+2*i].ToolCalls}, got[2+2*i], "never duplicate assistant text, reasoning or metadata")
		}
	}
	require.Equal(t, messages[6:], got[8:])
	after, err := common.Marshal(messages)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	twice, err := SerializeCompleteParallelToolTurns(got)
	require.NoError(t, err)
	require.Equal(t, got, twice, "normalization must be idempotent")
}

func TestSerializeCompleteParallelToolTurnsLeavesNormalHistoriesUnchanged(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`,
		`[{"role":"system","content":"scope"},{"role":"user","content":"hello"},{"role":"assistant","content":"reply","tool_calls":null}]`,
		`[{"role":"assistant","content":"reply","tool_calls":[]}]`,
		`[{"role":"assistant","content":null,"tool_calls":[ {"id":"a","type":"function","function":{"name":"local","arguments":"{}"},"extra":false} ]},{"role":"tool","tool_call_id":"a","content":"result"}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			messages := parallelMessages(t, raw)
			before, err := common.Marshal(messages)
			require.NoError(t, err)
			got, err := SerializeCompleteParallelToolTurns(messages)
			require.NoError(t, err)
			require.Equal(t, messages, got)
			after, err := common.Marshal(got)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "do not reserialize even a single call")
		})
	}
}

func TestSerializeCompleteParallelToolTurnsValidatesEntireInputFirst(t *testing.T) {
	valid := `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f"}},{"id":"b","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"b","content":"b"},{"role":"tool","tool_call_id":"a","content":"a"}`
	for _, tc := range []struct{ name, raw string }{
		{"missing_result", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f"}},{"id":"b","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"a","content":"a"}]`},
		{"duplicate_call", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f"}},{"id":"a","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"a","content":"a"}]`},
		{"interrupt", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f"}},{"id":"b","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"a","content":"a"},{"role":"user","content":"interrupt"},{"role":"tool","tool_call_id":"b","content":"b"}]`},
		{"duplicate_result_after_valid_turn", `[` + valid + `,{"role":"tool","tool_call_id":"a","content":"again"}]`},
		{"orphan_after_valid_turn", `[` + valid + `,{"role":"tool","tool_call_id":"unknown","content":"orphan"}]`},
		{"incomplete_later_turn", `[` + valid + `,{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f"}}]}]`},
		{"reused_id", `[` + valid + `,{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"a","content":"again"}]`},
		{"missing_id", `[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"f"}}]},{"role":"tool","content":"a"}]`},
		{"blank_id", `[{"role":"assistant","tool_calls":[{"id":" ","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":" ","content":"a"}]`},
		{"wrong_role", `[{"role":"user","tool_calls":[{"id":"a","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"a","content":"a"}]`},
		{"malformed_calls", `[{"role":"assistant","tool_calls":{"id":"a"}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := parallelMessages(t, tc.raw)
			before, err := common.Marshal(messages)
			require.NoError(t, err)
			validationErr := ValidateChatToolHistory(messages)
			require.Error(t, validationErr)
			got, err := SerializeCompleteParallelToolTurns(messages)
			require.EqualError(t, err, validationErr.Error())
			require.Nil(t, got, "never return a partially normalized invalid history")
			after, err := common.Marshal(messages)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
}

func TestSerializeCompleteParallelToolTurnsMultipleTurns(t *testing.T) {
	messages := parallelMessages(t, `[
		{"role":"assistant","content":"first","tool_calls":[{"id":"a","type":"function","function":{"name":"f"}},{"id":"b","type":"function","function":{"name":"f"}}]},
		{"role":"tool","tool_call_id":"b","content":"b"},{"role":"tool","tool_call_id":"a","content":"a"},
		{"role":"user","content":"next"},
		{"role":"assistant","content":"second","tool_calls":[{"id":"c","type":"function","function":{"name":"f"}},{"id":"d","type":"function","function":{"name":"f"}}]},
		{"role":"tool","tool_call_id":"c","content":"c"},{"role":"tool","tool_call_id":"d","content":"d"}
	]`)
	got, err := SerializeCompleteParallelToolTurns(messages)
	require.NoError(t, err)
	require.NoError(t, ValidateChatToolHistory(got))
	require.Len(t, got, 9)
	require.Equal(t, messages[3], got[4])
	for i, id := range []string{"a", "b", "c", "d"} {
		index := 2 * i
		if i >= 2 {
			index++
		}
		var calls []dto.ToolCallRequest
		require.NoError(t, common.Unmarshal(got[index].ToolCalls, &calls))
		require.Len(t, calls, 1)
		require.Equal(t, id, calls[0].ID)
		require.Equal(t, id, got[index+1].ToolCallId)
	}
}
