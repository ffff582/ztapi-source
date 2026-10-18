package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIMeteringNumericUsageHasBoundedDepth(t *testing.T) {
	raw := json.RawMessage(`{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":3,"prompt_tokens_details":{"cached_tokens":999}}}`)
	safe, err := common.Marshal(FilterZTAPINumericUsage(raw))
	require.NoError(t, err)
	require.JSONEq(t, `{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":3}}`, string(safe))
}

func TestZTAPIMeteringNumericUsageBudgetDoesNotDiscardDiagnostics(t *testing.T) {
	fields := make(map[string]json.RawMessage)
	for _, key := range []string{
		"prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens",
		"cached_tokens", "cached_creation_tokens", "cache_read_input_tokens", "cache_creation_input_tokens",
		"prompt_cache_hit_tokens", "prompt_cache_miss_tokens", "reasoning_tokens", "text_tokens", "image_tokens", "audio_tokens",
		"accepted_prediction_tokens", "rejected_prediction_tokens", "claude_cache_creation_5_m_tokens", "claude_cache_creation_1_h_tokens",
		"ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens",
	} {
		fields[key] = json.RawMessage(strings.Repeat("9", 63))
	}
	scalars, err := common.Marshal(fields)
	require.NoError(t, err)
	for _, key := range []string{"prompt_tokens_details", "completion_tokens_details", "input_tokens_details", "output_tokens_details", "cache_creation"} {
		fields[key] = scalars
	}
	raw, err := common.Marshal(fields)
	require.NoError(t, err)
	require.Greater(t, len(raw), 8192)
	evidence := (ZTAPIMeteringEvidence{Version: 1, RawUsage: raw, UsageIntegrityReasons: []string{"usage_invalid_quantity"}}).Sanitized()
	require.NotNil(t, evidence)
	require.LessOrEqual(t, len(evidence.RawUsage), 8192, "optional raw evidence must leave room in the 64 KiB durable payload")
	require.True(t, evidence.RawUsageOmitted)
	require.Equal(t, []string{"usage_invalid_quantity"}, evidence.UsageIntegrityReasons)
}

func TestZTAPIMeteringRevalidatesUnverifiedObservation(t *testing.T) {
	evidence := ZTAPIMeteringEvidence{Version: 1, UnverifiedUsage: &ZTAPIUnverifiedUsageObservation{
		Location: "PRIVATE_LOCATION", Reason: "usage_incomplete",
		RawUsage: json.RawMessage(`{"prompt_tokens":355000,"note":"PRIVATE_PROVIDER_TEXT"}`), RawUsageSHA256: "forged",
	}}
	safe, err := common.Marshal(evidence.Sanitized())
	require.NoError(t, err)
	require.Contains(t, string(safe), "355000")
	require.Contains(t, string(safe), "usage_incomplete")
	require.NotContains(t, string(safe), "PRIVATE")
	require.NotContains(t, string(safe), "forged")
}
