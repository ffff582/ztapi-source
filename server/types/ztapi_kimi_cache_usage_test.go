package types

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestKimiCacheWriteNumericAuditEvidenceRetainsExactValue(t *testing.T) {
	for _, path := range []string{"prompt_tokens_details", "input_tokens_details"} {
		for _, value := range []string{"0", "30", "-1", "9007199254740993", "9223372036854775808"} {
			t.Run(path+"/"+value, func(t *testing.T) {
				raw := json.RawMessage(`{"input_tokens":100,"output_tokens":2,"` + path + `":{"cache_write_tokens":` + value + `,"provider_note":"PRIVATE_TEXT"}}`)
				evidence := (ZTAPIMeteringEvidence{Version: 1, RawUsage: raw, RawUsageSHA256: "UNTRUSTED"}).Sanitized()
				require.NotNil(t, evidence)
				var fields map[string]json.RawMessage
				var top map[string]json.RawMessage
				require.NoError(t, common.Unmarshal(evidence.RawUsage, &top))
				require.Contains(t, top, path)
				require.NoError(t, common.Unmarshal(top[path], &fields))
				require.Equal(t, value, string(fields["cache_write_tokens"]))
				require.NotContains(t, fields, "provider_note")
				require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(evidence.RawUsage)), evidence.RawUsageSHA256)
				observation := (&ZTAPIUnverifiedUsageObservation{Location: "stream", Reason: "usage_invalid_quantity", RawUsage: raw}).Sanitized()
				require.NotNil(t, observation)
				require.Equal(t, evidence.RawUsage, observation.RawUsage)
				require.Equal(t, evidence.RawUsageSHA256, observation.RawUsageSHA256)
			})
		}
	}
}
