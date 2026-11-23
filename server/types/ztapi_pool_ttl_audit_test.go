package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPoolTTLCacheNumericAuditKeepsExactCounters(t *testing.T) {
	raw := json.RawMessage(`{"input_tokens":100,"output_tokens":2,"cache_write_5m_tokens":0,"cache_write_1h_tokens":9007199254740993,"provider_note":"PRIVATE_TEXT","api_key":"PRIVATE_KEY"}`)
	evidence := (ZTAPIMeteringEvidence{Version: 1, RawUsage: raw}).Sanitized()
	require.NotNil(t, evidence)
	require.Contains(t, string(evidence.RawUsage), `"cache_write_5m_tokens":0`)
	require.Contains(t, string(evidence.RawUsage), `"cache_write_1h_tokens":9007199254740993`)
	require.NotContains(t, string(evidence.RawUsage), "PRIVATE")
	observation := (&ZTAPIUnverifiedUsageObservation{Location: "nonstream", Reason: "usage_invalid_quantity", RawUsage: raw}).Sanitized()
	require.Equal(t, evidence.RawUsage, observation.RawUsage)
}
