package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestReasoningAuditMarksResponsesModelSuffix(t *testing.T) {
	info := &relaycommon.RelayInfo{}
	request := dto.OpenAIResponsesRequest{Model: "gpt-5.6-sol-high"}

	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, request)
	require.NoError(t, err)
	got := converted.(dto.OpenAIResponsesRequest)
	require.Equal(t, "gpt-5.6-sol", got.Model)
	require.NotNil(t, got.Reasoning)
	require.Equal(t, "high", got.Reasoning.Effort)
	require.Equal(t, relaycommon.ReasoningEffortSourceModelSuffix, info.ReasoningEffortSource)
}
