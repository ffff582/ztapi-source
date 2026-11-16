package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPISettlementLogRetainsSafeMeteringEvidence(t *testing.T) {
	log := Log{Other: `{"billing_source":"wallet","ztapi_metering_evidence":{"version":1,"entry_body_bytes":128,"outbound_body_bytes":256,"message_count":2,"tool_count":1,"entry_protocol":"claude","upstream_protocol":"openai","usage_source":"upstream_reported","usage_location":"stream","usage_observations":1,"raw_numeric_usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":80,"secret":"PRIVATE_NESTED"},"note":"PRIVATE_USAGE"},"raw_numeric_usage_sha256":"forged","prompt":"PRIVATE_PROMPT","headers":{"authorization":"PRIVATE_KEY"}}}`}
	require.NoError(t, sanitizeZTAPISettlementLogMetadata(&log))
	require.Contains(t, log.Other, `"ztapi_metering_evidence"`)
	require.Contains(t, log.Other, `"entry_body_bytes":128`)
	require.Contains(t, log.Other, `"prompt_tokens":100`)
	require.Contains(t, log.Other, `"cached_tokens":80`)
	require.NotContains(t, log.Other, "PRIVATE_")
	require.NotContains(t, log.Other, "forged")
}

func TestZTAPISettlementLogDropsInvalidMeteringWithoutLosingBillingLog(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"entry_body_bytes":-1}`,
		`{"version":1,"tool_count":-1}`,
		`{"version":2,"message_count":1}`,
	} {
		log := Log{Other: `{"billing_source":"wallet","ztapi_metering_evidence":` + raw + `}`}
		require.NoError(t, sanitizeZTAPISettlementLogMetadata(&log))
		require.NotContains(t, log.Other, "ztapi_metering_evidence")
		require.Contains(t, log.Other, `"billing_source":"wallet"`)
	}
}
