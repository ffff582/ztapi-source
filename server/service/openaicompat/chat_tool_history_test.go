package openaicompat

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestValidateChatToolHistoryRejectsInvalidIDsAndOrdering(t *testing.T) {
	for _, tc := range []struct{ name, messages string }{
		{"orphan_result", `[{"role":"tool","tool_call_id":"unknown","content":"result"}]`},
		{"missing_result_id", `[{"role":"tool","content":"result"}]`},
		{"missing_call_id", `[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"read"}}]}]`},
		{"blank_call_id", `[{"role":"assistant","tool_calls":[{"id":" ","type":"function","function":{"name":"read"}}]}]`},
		{"duplicate_parallel_ids", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}},{"id":"a","type":"function","function":{"name":"read"}}]},{"role":"tool","tool_call_id":"a","content":"result"}]`},
		{"reused_id_after_completion", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}}]},{"role":"tool","tool_call_id":"a","content":"one"},{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}}]},{"role":"tool","tool_call_id":"a","content":"two"}]`},
		{"duplicate_result", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}}]},{"role":"tool","tool_call_id":"a","content":"one"},{"role":"tool","tool_call_id":"a","content":"two"}]`},
		{"missing_result", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}}]}]`},
		{"user_interrupts_results", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}}]},{"role":"user","content":"interrupt"},{"role":"tool","tool_call_id":"a","content":"result"}]`},
		{"assistant_interrupts_results", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}}]},{"role":"assistant","content":"interrupt"},{"role":"tool","tool_call_id":"a","content":"result"}]`},
		{"nonassistant_call", `[{"role":"user","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}}]},{"role":"tool","tool_call_id":"a","content":"result"}]`},
		{"tool_result_with_calls", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read"}}]},{"role":"tool","tool_call_id":"a","tool_calls":[{"id":"b","type":"function","function":{"name":"read"}}]}]`},
		{"malformed_calls_object", `[{"role":"assistant","tool_calls":{"id":"a"},"content":"text"}]`},
		{"malformed_calls_string", `[{"role":"assistant","tool_calls":"not an array","content":"text"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var messages []dto.Message
			require.NoError(t, common.UnmarshalJsonStr(tc.messages, &messages))
			before, err := common.Marshal(messages)
			require.NoError(t, err)
			err = ValidateChatToolHistory(messages)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.False(t, errors.As(err, &apiErr), "the shared validator must return ordinary errors")
			after, marshalErr := common.Marshal(messages)
			require.NoError(t, marshalErr)
			require.Equal(t, string(before), string(after), "validation must not rewrite an invalid history")
		})
	}
}

func TestValidateChatToolHistoryKeepsCompleteParallelTurns(t *testing.T) {
	for _, raw := range []string{
		`[]`,
		`[{"role":"system","content":"scope"},{"role":"user","content":"hello"},{"role":"assistant","tool_calls":null,"content":"reply"}]`,
		`[{"role":"assistant","content":[{"type":"text","text":"working"}],"tool_calls":[{"id":"a","type":"function","function":{"name":"read"}},{"id":"b","type":"function","function":{"name":"read"}}]},{"role":"tool","tool_call_id":"b","content":"second"},{"role":"tool","tool_call_id":"a","content":"first"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"read"}}]},{"role":"tool","tool_call_id":"c","content":"third"},{"role":"user","content":[{"type":"text","text":"continue"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]`,
	} {
		var messages []dto.Message
		require.NoError(t, common.UnmarshalJsonStr(raw, &messages))
		before, err := common.Marshal(messages)
		require.NoError(t, err)
		require.NoError(t, ValidateChatToolHistory(messages))
		after, err := common.Marshal(messages)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after), "validation must preserve content, identities and order")
	}
}
