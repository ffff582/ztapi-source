package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const workbenchMessagesFixture = `{"model":"zt-claude-sonnet-5","max_tokens":1024,"stream":true,
"system":[{"type":"text","text":"Use local tools","cache_control":{"type":"ephemeral"}}],
"tools":[{"name":"mcp__ztapi_project__read_file","description":"Read project file","input_schema":{"type":"object"}}],
"messages":[{"role":"user","content":"Read the fixture"},
{"role":"assistant","content":[{"type":"tool_use","id":"toolu_fixture","name":"mcp__ztapi_project__read_file","input":{"path":"calc.mjs"}}]},
{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_fixture","content":[{"type":"text","text":"fixture"}]}]}]}`

func TestZTAPIWorkbenchMessagesWireBounds(t *testing.T) {
	const name = "zt-claude-sonnet-5"
	output, err := ZTAPIWorkbenchMessagesOutput([]byte(workbenchMessagesFixture), name)
	require.NoError(t, err)
	require.Equal(t, 1024, output)
	for _, tc := range []struct{ before, after string }{
		{`"max_tokens":1024`, `"max_tokens":2049`},
		{`"max_tokens":1024`, `"max_output_tokens":1024`},
		{`"stream":true`, `"stream":false`},
		{`"stream":true`, `"stream":true,"stream":true`},
		{`"stream":true`, `"stream":true,"mcp_servers":[{}]`},
		{`"stream":true`, `"stream":true,"service_tier":"priority"`},
		{`"type":"ephemeral"`, `"type":"ephemeral","ttl":"1h","unpriced":true`},
		{`"type":"text","text":"fixture"`, `"type":"image","source":{"type":"url","url":"https://example.test"}`},
		{`"description":"Read project file"`, `"description":"Read project file","type":"web_search_20250305"`},
		{`"name":"mcp__ztapi_project__read_file","input"`, `"name":"other_tool","input"`},
		{`"tool_use_id":"toolu_fixture"`, `"tool_use_id":"toolu_other"`},
	} {
		t.Run(fmt.Sprintf("%s", tc.after), func(t *testing.T) {
			wire := strings.Replace(workbenchMessagesFixture, tc.before, tc.after, 1)
			require.NotEqual(t, wire, workbenchMessagesFixture)
			_, err := ZTAPIWorkbenchMessagesOutput([]byte(wire), name)
			require.Error(t, err)
		})
	}
	_, err = ZTAPIWorkbenchMessagesOutput([]byte(workbenchMessagesFixture), "zt-claude-fable-5")
	require.Error(t, err)
}
