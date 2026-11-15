package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchWireAllowsStatelessLocalFunctionHistory(t *testing.T) {
	const wire = `{"model":"zt-test","stream":true,"store":false,"max_output_tokens":1024,"tools":[{"type":"function","name":"read_fixture","parameters":{"type":"object"}}],"tool_choice":"auto","input":[{"role":"user","content":"read fixture"},{"type":"function_call","name":"read_fixture","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"fixture data"}]}`
	output, err := ZTAPIWorkbenchWireOutput([]byte(wire), "zt-test")
	require.NoError(t, err)
	require.Equal(t, 1024, output)
	for name, invalid := range map[string]string{
		"hosted tool":        strings.Replace(wire, `"type":"function","name":"read_fixture"`, `"type":"web_search","name":"read_fixture"`, 1),
		"orphan tool output": strings.Replace(wire, `"call_id":"call_1","output"`, `"call_id":"call_other","output"`, 1),
		"stateful":           strings.Replace(wire, `"store":false`, `"store":false,"previous_response_id":"other"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ZTAPIWorkbenchWireOutput([]byte(invalid), "zt-test")
			require.ErrorIs(t, err, model.ErrZTAPISettlementInvalid)
		})
	}
}
