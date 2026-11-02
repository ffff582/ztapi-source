package types

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestPublicStreamErrorDropsOpaqueVendorFields(t *testing.T) {
	for _, raw := range []string{
		`{"error":{"message":"sk-stream-secret-1234567890","type":"server_error","metadata":{"env":{"SECRET":"opaque-stream-secret"}}},"environment":{"SECRET":"opaque-stream-secret"}}`,
		`{"type":"error","code":"opaque-stream-secret","message":"opaque-stream-secret","environment":{"SECRET":"opaque-stream-secret"}}`,
		`{"type":"response.error","code":"opaque-stream-secret","message":"opaque-stream-secret"}`,
		`{"type":"response.error","message":"opaque-stream-secret",broken-json`,
		`{"t\u0079pe":"response.er\u0072or","message":"opaque-stream-secret"}`,
		`{"type":"invalid_request_error","code":"provider_code","message":"opaque-stream-secret"}`,
		`{"type":"response.failed","sequence_number":9,"response":{"id":"resp_known","status":"failed","error":{"message":"opaque-stream-secret","type":"api_error"},"metadata":{"SECRET":"opaque-stream-secret"}}}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"opaque-stream-secret"},"environment":{"SECRET":"opaque-stream-secret"}}}`,
	} {
		clean := PublicStreamErrorPayload(raw)
		require.NotContains(t, clean, "opaque-stream-secret")
		require.NotContains(t, clean, "sk-stream-secret")
		var object map[string]any
		require.NoError(t, common.UnmarshalJsonStr(clean, &object))
		if object["type"] == "response.failed" {
			require.Equal(t, float64(9), object["sequence_number"])
			require.Equal(t, "resp_known", object["response"].(map[string]any)["id"])
		}
	}
}

func TestPublicStreamFailureKeepsNumericUsageAndKnownIncompleteReason(t *testing.T) {
	raw := `{"type":"response.incomplete","response":{"id":"resp_known","status":"incomplete","usage":{"input_tokens":120,"output_tokens":7,"total_tokens":127,"input_tokens_details":{"cached_tokens":20},"environment":{"SECRET":"opaque-secret"}},"incomplete_details":{"reason":"max_output_tokens","diagnostic":"opaque-secret"}}}`
	clean := PublicStreamErrorPayload(raw)
	require.NotContains(t, clean, "opaque-secret")
	require.Contains(t, clean, `"input_tokens":120`)
	require.Contains(t, clean, `"cached_tokens":20`)
	require.Contains(t, clean, `"reason":"max_output_tokens"`)
}

func TestPublicStreamErrorDoesNotRewriteSuccessfulModelContent(t *testing.T) {
	for _, raw := range []string{
		`{"type":"response.output_text.delta","delta":"The error is API_KEY=example"}`,
		`{"choices":[{"delta":{"content":"sk-user-example-1234567890"}}],"error":null}`,
		`{"type":"response.completed","response":{"status":"completed","error":null,"output":[]}}`,
		`[DONE]`,
	} {
		require.Equal(t, raw, PublicStreamErrorPayload(raw))
	}
}
