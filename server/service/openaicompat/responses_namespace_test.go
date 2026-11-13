package openaicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToChatPreservesCodexNamespacedTools(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"read it","tools":[
		{"type":"namespace","name":"functions","tools":[{"type":"function","name":"read","parameters":{"type":"object"}},{"type":"custom","name":"apply_patch"}]},
		{"type":"namespace","name":"other","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}],
		"tool_choice":{"type":"function","namespace":"functions","name":"read"}}`)
	require.NoError(t, err)
	require.Len(t, conversion.Request.Tools, 3, "namespace tools must not silently disappear")
	first := conversion.Request.Tools[0].Function.Name
	second := conversion.Request.Tools[2].Function.Name
	require.NotEqual(t, first, second, "equal local names in separate namespaces must stay distinct")
	require.LessOrEqual(t, len(first), 64)
	require.Equal(t, map[string]any{"type": "function", "function": map[string]any{"name": first}}, conversion.Request.ToolChoice)
	require.True(t, conversion.CustomTools[conversion.Request.Tools[1].Function.Name])
}

func TestResponsesToChatPreservesNamespacedToolHistory(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}],
		"input":[{"role":"user","content":"read it"},{"type":"function_call","namespace":"functions","name":"read","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"OK"}]}`)
	require.NoError(t, err)
	require.Len(t, conversion.Request.Tools, 1)
	name := conversion.Request.Tools[0].Function.Name
	require.Contains(t, string(conversion.Request.Messages[1].ToolCalls), `"name":"`+name+`"`)
}

func TestResponsesToChatKeepsCodexAdditionalTools(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":[
		{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","description":"old"}]}]},
		{"role":"user","content":"edit it"},
		{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","description":"updated"},{"type":"function","name":"exec_command","parameters":{"type":"object"}}]}]}]}`)
	require.NoError(t, err)
	require.Len(t, conversion.Request.Tools, 2)
	require.Contains(t, conversion.Request.Tools[0].Function.Description, "updated")
	require.Len(t, conversion.Request.Messages, 1, "tool declarations are not chat messages")
	require.Equal(t, "user", conversion.Request.Messages[0].Role)
}

func TestResponsesAdditionalToolsCanChangeToolType(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","tools":[{"type":"custom","name":"edit"}],"input":[{"type":"additional_tools","tools":[{"type":"function","name":"edit","parameters":{"type":"object"}}]},{"role":"user","content":"edit"}]}`)
	require.NoError(t, err)
	require.Len(t, conversion.Request.Tools, 1)
	require.False(t, conversion.CustomTools["edit"])
}

func TestResponsesNamespaceKeepsSharedInstructions(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","input":"read","tools":[{"type":"namespace","name":"files","description":"Only access /workspace/safe.","tools":[{"type":"function","name":"read","description":"Read one file.","parameters":{"type":"object"}}]}]}`)
	require.NoError(t, err)
	require.Contains(t, conversion.Request.Tools[0].Function.Description, "Only access /workspace/safe.")
	require.Contains(t, conversion.Request.Tools[0].Function.Description, "Read one file.")
}

func TestResponsesAdditionalToolsTypeOverridesHistoricalCustomCall(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","tools":[{"type":"namespace","name":"files","tools":[{"type":"custom","name":"edit"}]}],"input":[{"type":"custom_tool_call","namespace":"files","name":"edit","call_id":"old","input":"old edit"},{"type":"custom_tool_call_output","call_id":"old","output":"done"},{"type":"additional_tools","tools":[{"type":"namespace","name":"files","tools":[{"type":"function","name":"edit","parameters":{"type":"object"}}]}]},{"role":"user","content":"edit again"}]}`)
	require.NoError(t, err)
	require.Len(t, conversion.Request.Tools, 1)
	require.False(t, conversion.CustomTools[conversion.Request.Tools[0].Function.Name])
}

func TestResponsesNamespaceUpdateRetainsSharedInstructions(t *testing.T) {
	conversion, err := convertResponsesForTest(t, `{"model":"claude-opus-5","tools":[{"type":"namespace","name":"files","description":"Only access /workspace/safe.","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}],"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"files","tools":[{"type":"function","name":"write","parameters":{"type":"object"}}]}]},{"role":"user","content":"edit"}]}`)
	require.NoError(t, err)
	require.Len(t, conversion.Request.Tools, 2)
	for _, tool := range conversion.Request.Tools {
		require.Contains(t, tool.Function.Description, "Only access /workspace/safe.")
	}
}
