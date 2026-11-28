package types

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUsageIntegritySanitizedReasonsAreClosed(t *testing.T) {
	var evidence ZTAPIMeteringEvidence
	require.NoError(t, common.UnmarshalJsonStr(`{"version":1,"usage_integrity_reasons":["usage_incomplete","PRIVATE_PROVIDER_TEXT","usage_invalid_type","usage_incomplete"]}`, &evidence))
	encoded, err := common.Marshal(evidence.Sanitized())
	require.NoError(t, err)
	require.Contains(t, string(encoded), "usage_incomplete")
	require.Contains(t, string(encoded), "usage_invalid_type")
	require.NotContains(t, string(encoded), "PRIVATE_PROVIDER_TEXT")
	require.Len(t, gjson.GetBytes(encoded, "usage_integrity_reasons").Array(), 2)
}

func TestUsageIntegrityUnverifiedObservationIsBounded(t *testing.T) {
	nested := make(map[string]json.RawMessage)
	for _, name := range []string{"prompt_tokens", "completion_tokens", "input_tokens", "output_tokens", "total_tokens", "cached_tokens", "cached_creation_tokens", "reasoning_tokens", "text_tokens", "image_tokens", "audio_tokens", "accepted_prediction_tokens", "rejected_prediction_tokens"} {
		nested[name] = json.RawMessage(strings.Repeat("9", 64))
	}
	raw, err := common.Marshal(map[string]any{
		"prompt_tokens":         355000,
		"prompt_tokens_details": nested, "input_tokens_details": nested,
		"completion_tokens_details": nested, "output_tokens_details": nested,
		"provider_note": "PRIVATE_PROVIDER_TEXT",
	})
	require.NoError(t, err)
	observation := (&ZTAPIUnverifiedUsageObservation{
		Location: "PRIVATE_LOCATION", Reason: "PRIVATE_REASON", RawUsage: raw, RawUsageSHA256: "UNTRUSTED_HASH",
	}).Sanitized()
	require.NotNil(t, observation)
	require.LessOrEqual(t, len(observation.RawUsage), MaxZTAPIUnverifiedUsageBytes)
	require.Equal(t, int64(355000), gjson.GetBytes(observation.RawUsage, "prompt_tokens").Int())
	require.Empty(t, observation.Location)
	require.Empty(t, observation.Reason)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(observation.RawUsage)), observation.RawUsageSHA256)
	encoded, err := common.Marshal(observation)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE")
}
