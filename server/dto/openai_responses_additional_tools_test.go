package dto

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestResponsesAdditionalToolsAreIncludedInPreauthorizationEstimate(t *testing.T) {
	var request OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"model":"claude-opus-5","input":[{"type":"additional_tools","tools":[{"type":"function","name":"schema_marker","description":"COUNT_THIS_TOOL_SCHEMA","parameters":{"type":"object"}}]},{"role":"user","content":"hello"}]}`, &request))
	meta := request.GetTokenCountMeta()
	require.Contains(t, meta.CombineText, "COUNT_THIS_TOOL_SCHEMA")
	require.Contains(t, meta.CombineText, "hello")
}
