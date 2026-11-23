package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPINestedCacheEvidencePreservesNumericTTLOnly(t *testing.T) {
	raw := json.RawMessage(`{"prompt_tokens_details":{"cache_creation":{"ephemeral_1h_input_tokens":15607,"ephemeral_5m_input_tokens":0,"note":"PRIVATE_PROVIDER_TEXT"},"cache_creation_input_tokens":15607,"cached_tokens":0}}`)
	evidence := (ZTAPIMeteringEvidence{Version: 1, RawUsage: raw}).Sanitized()
	require.Contains(t, string(evidence.RawUsage), `"ephemeral_1h_input_tokens":15607`)
	require.Contains(t, string(evidence.RawUsage), `"ephemeral_5m_input_tokens":0`)
	require.NotContains(t, string(evidence.RawUsage), "PRIVATE_PROVIDER_TEXT")
	require.NotEmpty(t, evidence.RawUsageSHA256)
}
