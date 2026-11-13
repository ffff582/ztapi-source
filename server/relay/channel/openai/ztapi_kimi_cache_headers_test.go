package openai

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const kimiWrite5mHeader = "Msh-Usage-Cache-Write-Tokens-5m"
const kimiWrite1hHeader = "Msh-Usage-Cache-Write-Tokens-1h"

func kimiCacheHeaders(five, hour string) http.Header {
	header := http.Header{}
	header.Set(kimiWrite5mHeader, five)
	header.Set(kimiWrite1hHeader, hour)
	return header
}

func runKimiCacheHeaderFixture(t *testing.T, route string, stream bool, header http.Header, usages []string, model string, genericQuote bool) (*dto.Usage, *gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	c, _, info := usageRegressionContext(types.RelayFormatOpenAI, true)
	info.IsStream = stream
	info.OriginModelName, info.ChannelMeta.UpstreamModelName = model, model
	snapshot := info.ZTAPIPublicationSnapshot
	snapshot.PublicName, snapshot.SourceModel = model, model
	for _, dimension := range []string{"cache_read", "cache_write_5m", "cache_write_1h"} {
		snapshot.BillingDimensions = append(snapshot.BillingDimensions, dimension)
		snapshot.SaleUSD[dimension] = "1.25"
	}
	if genericQuote {
		snapshot.BillingDimensions = append(snapshot.BillingDimensions, "cache_write")
		snapshot.SaleUSD["cache_write"] = "1"
	}
	var frames []string
	for _, rawUsage := range usages {
		frame := `{"choices":[{"index":0,"message":{"role":"assistant","content":""},"delta":{},"finish_reason":"stop"}],"usage":` + rawUsage + `}`
		if route == "responses_to_chat" {
			frame = `{"status":"completed","output":[],"usage":` + rawUsage + `}`
			if stream {
				frame = `{"type":"response.completed","response":` + frame + `}`
			}
		}
		frames = append(frames, frame)
	}
	body := frames[0]
	if stream {
		body = "data: " + strings.Join(frames, "\n\ndata: ") + "\n\ndata: [DONE]\n\n"
	}
	before := header.Clone()
	resp := &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}
	var usage *dto.Usage
	var apiErr *types.NewAPIError
	switch route {
	case "chat":
		if stream {
			usage, apiErr = OaiStreamHandler(c, info, resp)
		} else {
			usage, apiErr = OpenaiHandler(c, info, resp)
		}
	case "chat_to_responses":
		if stream {
			usage, apiErr = OaiChatToResponsesStreamHandler(c, info, resp, nil)
		} else {
			usage, apiErr = OaiChatToResponsesHandler(c, info, resp, nil)
		}
	case "responses_to_chat":
		if stream {
			usage, apiErr = OaiResponsesToChatStreamHandler(c, info, resp)
		} else {
			usage, apiErr = OaiResponsesToChatHandler(c, info, resp)
		}
	default:
		t.Fatalf("unknown fixture route %s", route)
	}
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Equal(t, before, header, "header validation must not rewrite upstream evidence")
	return usage, c, info
}

func kimiUsageDimensions(result relaycommon.ZTAPIUsageDimensionResult) map[string]int64 {
	quantities := map[string]int64{}
	for _, dimension := range result.Dimensions {
		quantities[dimension.Dimension] = dimension.Quantity
	}
	return quantities
}

func TestKimiChatCacheHeadersReachFrozenDimensions(t *testing.T) {
	for _, route := range []string{"chat", "chat_to_responses"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, five, hour, raw string
				want                  map[string]int64
			}{
				{"both_ttls", "10", "20", `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`, map[string]int64{"input_tokens": 50, "output_tokens": 2, "cache_read": 20, "cache_write_5m": 10, "cache_write_1h": 20}},
				{"zero_5m", "0", "30", `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`, map[string]int64{"input_tokens": 50, "output_tokens": 2, "cache_read": 20, "cache_write_1h": 30}},
				{"zero_writes", "0", "0", `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":0}}`, map[string]int64{"input_tokens": 80, "output_tokens": 2, "cache_read": 20}},
				{"zero_receipt", "0", "0", `{"prompt_tokens":0,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`, map[string]int64{}},
				{"matching_body_aliases", "10", "20", `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"claude_cache_creation_5_m_tokens":10,"claude_cache_creation_1_h_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}`, map[string]int64{"input_tokens": 50, "output_tokens": 2, "cache_read": 20, "cache_write_5m": 10, "cache_write_1h": 20}},
				{"partial_body_split_backed_by_headers", "10", "20", `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"claude_cache_creation_5_m_tokens":10}`, map[string]int64{"input_tokens": 50, "output_tokens": 2, "cache_read": 20, "cache_write_5m": 10, "cache_write_1h": 20}},
			} {
				t.Run(route+"/"+strconv.FormatBool(stream)+"/"+tc.name, func(t *testing.T) {
					header := kimiCacheHeaders(tc.five, tc.hour)
					header.Set("X-Unrelated-Audit-Field", "PRIVATE_HEADER_TEXT")
					usage, c, info := runKimiCacheHeaderFixture(t, route, stream, header, []string{tc.raw}, "kimi-k3", false)
					result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
					require.False(t, result.Pending, "%+v", result)
					require.Equal(t, tc.want, kimiUsageDimensions(result))
					frozen := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: info.ZTAPIPublicationSnapshot.Clone()}
					require.Equal(t, result, relaycommon.ValidateZTAPIUsageDimensions(c, frozen, usage))
					require.Equal(t, int64(usage.ClaudeCacheCreation5mTokens), info.ZTAPIUsageLastSnapshot["cache_write_5m"])
					require.Equal(t, int64(usage.ClaudeCacheCreation1hTokens), info.ZTAPIUsageLastSnapshot["cache_write_1h"])
					require.NotContains(t, kimiUsageDimensions(result), "cache_write", "TTL counters partition the aggregate, not extra charges")
					audit, err := common.Marshal(info.ZTAPIMetering.Sanitized())
					require.NoError(t, err)
					require.Equal(t, tc.five, gjson.GetBytes(audit, "kimi_cache_write_headers.tokens_5m").Raw)
					require.Equal(t, tc.hour, gjson.GetBytes(audit, "kimi_cache_write_headers.tokens_1h").Raw)
					require.NotContains(t, string(audit), "PRIVATE_HEADER_TEXT")
				})
			}
		}
	}
}

func TestKimiChatCacheHeadersInvalidEvidenceStaysPending(t *testing.T) {
	validUsage := `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`
	cases := []struct {
		name   string
		header http.Header
		raw    string
	}{
		{"missing_both", nil, validUsage},
		{"missing_1h", http.Header{kimiWrite5mHeader: []string{"10"}}, validUsage},
		{"missing_5m", http.Header{kimiWrite1hHeader: []string{"20"}}, validUsage},
		{"duplicate", http.Header{kimiWrite5mHeader: []string{"10", "10"}, kimiWrite1hHeader: []string{"20"}}, validUsage},
		{"duplicate_case_alias", http.Header{kimiWrite5mHeader: []string{"10"}, strings.ToLower(kimiWrite5mHeader): []string{"10"}, kimiWrite1hHeader: []string{"20"}}, validUsage},
		{"host_int_overflow", kimiCacheHeaders(strconv.FormatUint(uint64(math.MaxInt)+1, 10), "0"), validUsage},
		{"sum_overflow", kimiCacheHeaders(strconv.FormatInt(int64(math.MaxInt), 10), "1"), validUsage},
		{"aggregate_smaller", kimiCacheHeaders("10", "20"), `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":29}}`},
		{"aggregate_larger", kimiCacheHeaders("10", "20"), `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":31}}`},
		{"aggregate_explicit_zero", kimiCacheHeaders("10", "20"), `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":0}}`},
		{"missing_aggregate", kimiCacheHeaders("10", "20"), `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20},"claude_cache_creation_5_m_tokens":10,"claude_cache_creation_1_h_tokens":20}`},
		{"body_split_conflict", kimiCacheHeaders("10", "20"), `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"claude_cache_creation_5_m_tokens":20,"claude_cache_creation_1_h_tokens":10}`},
		{"body_split_explicit_zero", kimiCacheHeaders("10", "20"), `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"claude_cache_creation_5_m_tokens":0}`},
		{"body_alias_explicit_zero", kimiCacheHeaders("10", "20"), `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30,"cached_creation_tokens":0}}`},
	}
	for _, value := range []string{"", "-1", "+10", " 10", "10 ", "1.0", "1e1", "10,10", "0xA", "ten"} {
		cases = append(cases, struct {
			name   string
			header http.Header
			raw    string
		}{"invalid_value_" + value, kimiCacheHeaders(value, "20"), validUsage})
	}
	for _, route := range []string{"chat", "chat_to_responses"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(route+"/"+strconv.FormatBool(stream)+"/"+tc.name, func(t *testing.T) {
					usage, c, info := runKimiCacheHeaderFixture(t, route, stream, tc.header, []string{tc.raw}, "kimi-k3", false)
					result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
					require.True(t, result.Pending, "%+v", result)
					if tc.name != "missing_both" {
						require.NotEmpty(t, result.InvalidDimensions, "invalid header evidence must be sticky, not merely lack a quote")
					}
					frozen := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: info.ZTAPIPublicationSnapshot.Clone()}
					require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, frozen, usage).Pending)
				})
			}
		}
	}
}

func TestKimiChatCacheHeadersScopeAndResponsesIsolation(t *testing.T) {
	chatUsage := `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`
	for _, route := range []string{"chat", "chat_to_responses"} {
		for _, stream := range []bool{false, true} {
			for _, header := range []http.Header{nil, kimiCacheHeaders("invalid", "20")} {
				usage, c, info := runKimiCacheHeaderFixture(t, route, stream, header, []string{chatUsage}, "gpt-5.4", true)
				result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
				require.False(t, result.Pending)
				require.Equal(t, map[string]int64{"input_tokens": 50, "output_tokens": 2, "cache_read": 20, "cache_write": 30}, kimiUsageDimensions(result))
				audit, err := common.Marshal(info.ZTAPIMetering.Sanitized())
				require.NoError(t, err)
				require.False(t, gjson.GetBytes(audit, "kimi_cache_write_headers").Exists())
			}
		}
	}
	for _, stream := range []bool{false, true} {
		for _, header := range []http.Header{kimiCacheHeaders("10", "20"), kimiCacheHeaders("invalid", "20")} {
			usage, c, info := runKimiCacheHeaderFixture(t, "responses_to_chat", stream, header, []string{`{"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`}, "kimi-k3", false)
			result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
			require.True(t, result.Pending)
			require.Empty(t, result.InvalidDimensions, "native Responses must not consume undocumented headers")
			require.Equal(t, []string{"cache_write"}, result.MissingDimensions)
			require.Zero(t, usage.ClaudeCacheCreation5mTokens)
			require.Zero(t, usage.ClaudeCacheCreation1hTokens)
			audit, err := common.Marshal(info.ZTAPIMetering.Sanitized())
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(audit, "kimi_cache_write_headers").Exists())
		}
	}
}

func TestKimiChatCacheHeadersConflictingSnapshotRemainsSticky(t *testing.T) {
	for _, route := range []string{"chat", "chat_to_responses"} {
		usage, c, info := runKimiCacheHeaderFixture(t, route, true, kimiCacheHeaders("10", "20"), []string{
			`{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`,
			`{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30},"claude_cache_creation_5_m_tokens":20,"claude_cache_creation_1_h_tokens":10}`,
			`{"prompt_tokens":100,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`,
		}, "kimi-k3", false)
		require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
		require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_alias_conflict")
		require.Equal(t, 10, usage.ClaudeCacheCreation5mTokens)
		require.Equal(t, 20, usage.ClaudeCacheCreation1hTokens)
	}
}

func TestKimiChatCacheHeadersRepeatedSnapshotsAreIdempotent(t *testing.T) {
	for _, route := range []string{"chat", "chat_to_responses"} {
		raw := `{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}}`
		usage, c, info := runKimiCacheHeaderFixture(t, route, true, kimiCacheHeaders("10", "20"), []string{raw, raw, `{}`}, "zt-kimi-k3", false)
		result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
		require.False(t, result.Pending)
		require.Equal(t, map[string]int64{"input_tokens": 50, "output_tokens": 2, "cache_read": 20, "cache_write_5m": 10, "cache_write_1h": 20}, kimiUsageDimensions(result))
		require.Equal(t, int64(30), info.ZTAPIUsageLastSnapshot["cache_write"])
		require.Equal(t, int64(0), info.ZTAPIUsageLastSnapshot["cache_write_unspecified"])
		require.Contains(t, string(info.ZTAPIMetering.RawUsage), `"cache_write_tokens":30`)
		require.NotContains(t, string(info.ZTAPIMetering.RawUsage), "claude_cache_creation", "header evidence must not be fabricated into the original body")
	}
}
