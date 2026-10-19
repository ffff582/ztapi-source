package types

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestKimiCacheWriteHeaderEvidenceSanitized(t *testing.T) {
	for _, tc := range []struct{ five, hour string }{
		{"10", "20"}, {"0", "0"}, {"9007199254740993", "10"}, {"9223372036854775807", "0"},
	} {
		t.Run(tc.five+"/"+tc.hour, func(t *testing.T) {
			var evidence ZTAPIMeteringEvidence
			require.NoError(t, common.UnmarshalJsonStr(`{"version":1,"kimi_cache_write_headers":{"tokens_5m":`+tc.five+`,"tokens_1h":`+tc.hour+`,"Authorization":"PRIVATE_SECRET","x-api-key":"PRIVATE_KEY","other_headers":{"cookie":"PRIVATE_COOKIE"}}}`, &evidence))
			sanitized := evidence.Sanitized()
			require.NotNil(t, sanitized)
			encoded, err := common.Marshal(sanitized)
			require.NoError(t, err)
			counts := gjson.GetBytes(encoded, "kimi_cache_write_headers")
			require.True(t, counts.Exists(), "strictly parsed TTL counts must survive audit persistence")
			require.Equal(t, tc.five, counts.Get("tokens_5m").Raw)
			require.Equal(t, tc.hour, counts.Get("tokens_1h").Raw)
			require.Len(t, counts.Map(), 2)
			require.NotContains(t, string(encoded), "PRIVATE")
		})
	}
	encoded, err := common.Marshal((ZTAPIMeteringEvidence{Version: 1}).Sanitized())
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(encoded, "kimi_cache_write_headers").Exists(), "absence must stay distinct from explicit zero counts")
}

func TestKimiCacheWriteHeaderEvidenceRejectsInvalidTotals(t *testing.T) {
	for _, counts := range []string{
		`{"tokens_5m":-1,"tokens_1h":0}`,
		`{"tokens_5m":0,"tokens_1h":-1}`,
		`{"tokens_5m":9223372036854775807,"tokens_1h":1}`,
	} {
		var evidence ZTAPIMeteringEvidence
		require.NoError(t, common.UnmarshalJsonStr(`{"version":1,"kimi_cache_write_headers":`+counts+`}`, &evidence))
		require.Nil(t, evidence.Sanitized(), "invalid typed header quantities must not cross the audit boundary")
	}
}

func TestKimiCacheWriteHeaderEvidenceDefensiveCopy(t *testing.T) {
	var original ZTAPIMeteringEvidence
	require.NoError(t, common.UnmarshalJsonStr(`{"version":1,"kimi_cache_write_headers":{"tokens_5m":10,"tokens_1h":20}}`, &original))
	sanitized := original.Sanitized()
	require.NotNil(t, sanitized)
	require.NoError(t, common.UnmarshalJsonStr(`{"kimi_cache_write_headers":{"tokens_5m":99,"tokens_1h":1}}`, &original))
	encoded, err := common.Marshal(sanitized)
	require.NoError(t, err)
	require.Equal(t, "10", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_5m").Raw)
	require.Equal(t, "20", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_1h").Raw)
	require.NoError(t, common.UnmarshalJsonStr(`{"kimi_cache_write_headers":{"tokens_5m":7,"tokens_1h":8}}`, sanitized))
	encoded, err = common.Marshal(original)
	require.NoError(t, err)
	require.Equal(t, "99", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_5m").Raw)
	require.Equal(t, "1", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_1h").Raw)
}
