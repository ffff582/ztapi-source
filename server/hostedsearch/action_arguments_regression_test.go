package hostedsearch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostedActionArgumentsIgnoreOtherDeclaredActionFields(t *testing.T) {
	for _, tc := range []struct {
		arguments string
		want      ToolRequest
	}{
		{`{"action":"search","count":5,"pattern":"errors.Is","query":"site:pkg.go.dev/errors Is func Is","url":""}`, ToolRequest{Action: "search", Count: 5, Query: "site:pkg.go.dev/errors Is func Is"}},
		{`{"action":"open_page","query":"errors.Is","count":5,"pattern":"Is","url":"https://pkg.go.dev/errors"}`, ToolRequest{Action: "open_page", URL: "https://pkg.go.dev/errors"}},
		{`{"action":"find_in_page","query":"errors.Is","count":5,"pattern":"Is","url":"https://pkg.go.dev/errors"}`, ToolRequest{Action: "find_in_page", URL: "https://pkg.go.dev/errors", Pattern: "Is"}},
	} {
		got, err := parseTool(map[string]any{"arguments": tc.arguments})
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	for _, arguments := range []string{
		`{"action":"search","query":"","pattern":"cannot replace missing query"}`,
		`{"action":"search","query":"valid","count":0}`,
		`{"action":"open_page","url":"http://127.0.0.1/private","query":"ignored"}`,
		`{"action":"find_in_page","url":"https://pkg.go.dev/errors","pattern":""}`,
		`{"action":"search","query":"valid","unknown":"not ignored"}`,
		`{"action":"search","action":"open_page","url":"https://pkg.go.dev/errors","query":"valid"}`,
	} {
		_, err := parseTool(map[string]any{"arguments": arguments})
		require.Error(t, err)
	}
}
