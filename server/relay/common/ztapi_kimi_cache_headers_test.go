package common

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestKimiChatCacheHeadersParseHostIntAndPresence(t *testing.T) {
	const five = "Msh-Usage-Cache-Write-Tokens-5m"
	const hour = "Msh-Usage-Cache-Write-Tokens-1h"
	maxInt := strconv.FormatInt(int64(math.MaxInt), 10)
	for _, tc := range []struct {
		name        string
		header      http.Header
		wantFive    int64
		wantHour    int64
		wantPresent bool
		wantReason  string
	}{
		{name: "absent"},
		{name: "explicit_zero", header: http.Header{five: []string{"0"}, hour: []string{"0"}}, wantPresent: true},
		{name: "host_int_boundary", header: http.Header{five: []string{maxInt}, hour: []string{"0"}}, wantFive: int64(math.MaxInt), wantPresent: true},
		{name: "host_int_overflow", header: http.Header{five: []string{strconv.FormatUint(uint64(math.MaxInt)+1, 10)}, hour: []string{"0"}}, wantPresent: true, wantReason: "usage_invalid_quantity"},
		{name: "sum_overflow", header: http.Header{five: []string{maxInt}, hour: []string{"1"}}, wantPresent: true, wantReason: "usage_invalid_quantity"},
		{name: "case_insensitive", header: http.Header{strings.ToLower(five): []string{"10"}, strings.ToLower(hour): []string{"20"}}, wantFive: 10, wantHour: 20, wantPresent: true},
		{name: "duplicate_case", header: http.Header{five: []string{"10"}, strings.ToLower(five): []string{"10"}, hour: []string{"20"}}, wantPresent: true, wantReason: "usage_invalid_type"},
		{name: "empty_slice", header: http.Header{five: nil, hour: []string{"0"}}, wantPresent: true, wantReason: "usage_invalid_type"},
		{name: "missing_zero_is_not_zero", header: http.Header{five: []string{"0"}}, wantPresent: true, wantReason: "usage_incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotFive, gotHour, present, reason := parseZTAPIKimiCacheWriteHeaders(tc.header)
			require.Equal(t, tc.wantFive, gotFive)
			require.Equal(t, tc.wantHour, gotHour)
			require.Equal(t, tc.wantPresent, present)
			require.Equal(t, tc.wantReason, reason)
		})
	}
}

func TestKimiChatCacheHeadersAuditRetryReset(t *testing.T) {
	info := cacheUsageInfo()
	header := http.Header{}
	header.Set("Msh-Usage-Cache-Write-Tokens-5m", "10")
	header.Set("Msh-Usage-Cache-Write-Tokens-1h", "20")
	raw := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}}`)
	require.NotNil(t, ObserveZTAPIOpenAIChatUsage(nil, info, raw, header))
	first := info.ZTAPIMetering.Sanitized()
	encoded, err := basecommon.Marshal(first)
	require.NoError(t, err)
	require.Equal(t, "10", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_5m").Raw)
	require.Equal(t, "20", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_1h").Raw)

	CaptureZTAPIOutboundRequestMetering(info, 123)
	encoded, err = basecommon.Marshal(info.ZTAPIMetering.Sanitized())
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(encoded, "kimi_cache_write_headers").Exists())
	require.NotNil(t, ObserveZTAPIOpenAIUsage(nil, info, raw))
	encoded, err = basecommon.Marshal(info.ZTAPIMetering.Sanitized())
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(encoded, "kimi_cache_write_headers").Exists(), "a Responses/native observation cannot inherit previous Chat header evidence")

	CaptureZTAPIOutboundRequestMetering(info, 124)
	header.Set("Msh-Usage-Cache-Write-Tokens-5m", "0")
	header.Set("Msh-Usage-Cache-Write-Tokens-1h", "30")
	require.NotNil(t, ObserveZTAPIOpenAIChatUsage(nil, info, raw, header))
	encoded, err = basecommon.Marshal(info.ZTAPIMetering.Sanitized())
	require.NoError(t, err)
	require.Equal(t, "0", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_5m").Raw)
	require.Equal(t, "30", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_1h").Raw)
	encoded, err = basecommon.Marshal(first)
	require.NoError(t, err)
	require.Equal(t, "10", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_5m").Raw)
	require.Equal(t, "20", gjson.GetBytes(encoded, "kimi_cache_write_headers.tokens_1h").Raw)
}

func TestKimiChatCacheHeadersAuditRecordsOnlyStrictCounts(t *testing.T) {
	for _, header := range []http.Header{
		nil,
		{"Msh-Usage-Cache-Write-Tokens-5m": []string{"10"}},
		{"Msh-Usage-Cache-Write-Tokens-5m": []string{"10", "10"}, "Msh-Usage-Cache-Write-Tokens-1h": []string{"20"}},
		{"Msh-Usage-Cache-Write-Tokens-5m": []string{"PRIVATE_SECRET"}, "Msh-Usage-Cache-Write-Tokens-1h": []string{"20"}},
		{"Msh-Usage-Cache-Write-Tokens-5m": []string{"-1"}, "Msh-Usage-Cache-Write-Tokens-1h": []string{"20"}},
		{"Msh-Usage-Cache-Write-Tokens-5m": []string{"9223372036854775808"}, "Msh-Usage-Cache-Write-Tokens-1h": []string{"0"}},
	} {
		info := cacheUsageInfo()
		ObserveZTAPIOpenAIChatUsage(nil, info, []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}}`), header)
		encoded, err := basecommon.Marshal(info.ZTAPIMetering.Sanitized())
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(encoded, "kimi_cache_write_headers").Exists())
		require.NotContains(t, string(encoded), "PRIVATE_SECRET")
	}
}
