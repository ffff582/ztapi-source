package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningAcceptanceNativeTitleEncodingStaysManaged(t *testing.T) {
	for _, managed := range []bool{false, true} {
		_, _, info := nativeResponsesContext(true, managed)
		var request dto.OpenAIResponsesRequest
		require.NoError(t, common.UnmarshalJsonStr(`{"model":"deepseek-v4-flash","input":"Generate a title.","tools":[],"reasoning":{"effort":"high"},"text":{"format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":36}},"required":["title"],"additionalProperties":false}}}}`, &request))
		converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
		require.NoError(t, err)
		got := converted.(dto.OpenAIResponsesRequest)
		if managed {
			var instructions string
			require.NoError(t, common.Unmarshal(got.Instructions, &instructions))
			require.Contains(t, instructions, `{"title":`)
			got.Instructions = request.Instructions
		}
		require.Equal(t, request, got)
	}
}
