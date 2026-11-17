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

func TestPoolTTLNumericAuditRetainsOnlyExactInputDetailObject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detail string
		keep   bool
	}{
		{"explicit zeroes", `{"ttl_30m_tokens":0,"ttl_12h_tokens":0}`, true},
		{"observed nonzero", `{"ttl_30m_tokens":7,"ttl_12h_tokens":12}`, true},
		{"missing field", `{"ttl_30m_tokens":0}`, false},
		{"duplicate key", `{"ttl_30m_tokens":1,"ttl_30m_tokens":0,"ttl_12h_tokens":0}`, false},
		{"wrong type", `{"ttl_30m_tokens":null,"ttl_12h_tokens":0}`, false},
		{"unrecognized field", `{"ttl_30m_tokens":0,"ttl_12h_tokens":0,"note":"PRIVATE"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"input_tokens":4226,"input_tokens_details":{"cache_write_tokens":0,"cache_write_tokens_details":` + tc.detail +
				`},"prompt_tokens_details":{"cache_write_tokens_details":` + tc.detail + `},"private":"PRIVATE"}`)
			observation := (&ZTAPIUnverifiedUsageObservation{Location: "nonstream", RawUsage: raw}).Sanitized()
			require.NotNil(t, observation)
			require.NotContains(t, string(observation.RawUsage), "PRIVATE")
			var result struct {
				Input map[string]json.RawMessage `json:"input_tokens_details"`
				Chat  map[string]json.RawMessage `json:"prompt_tokens_details"`
			}
			require.NoError(t, json.Unmarshal(observation.RawUsage, &result))
			require.NotContains(t, result.Chat, "cache_write_tokens_details")
			if tc.keep {
				require.JSONEq(t, tc.detail, string(result.Input["cache_write_tokens_details"]))
				require.Equal(t, observation, observation.Sanitized())
			} else {
				require.NotContains(t, result.Input, "cache_write_tokens_details")
			}
		})
	}
}
