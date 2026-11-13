package claude

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func claudeSearchUsageContext(t *testing.T, format types.RelayFormat) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat: format, DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-local"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PriceSourceID: 17, PriceSourceVersion: 3, Modality: "text",
			BillingDimensions: []string{"input_tokens", "output_tokens"},
			SaleUSD:           map[string]string{"input_tokens": "1", "output_tokens": "2"},
		},
	}
	return c, info
}

func claudeSearchStreamBody(startUsage string, deltas ...string) string {
	var body strings.Builder
	fmt.Fprintf(&body, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_local\",\"model\":\"claude-local\",\"usage\":%s}}\n\n", startUsage)
	for _, usage := range deltas {
		fmt.Fprintf(&body, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":%s}\n\n", usage)
	}
	body.WriteString("data: {\"type\":\"message_stop\"}\n\n")
	return body.String()
}

func claudeSearchHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestClaudeWebSearchUsageActualSnapshots(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		for _, tc := range []struct {
			name, start string
			deltas      []string
			want        int
		}{
			{
				name: "start_only", start: `{"input_tokens":10,"output_tokens":1,"server_tool_use":{"web_search_requests":2}}`,
				deltas: []string{`{"output_tokens":2}`}, want: 2,
			},
			{
				name: "delta_only", start: `{"input_tokens":10,"output_tokens":1}`,
				deltas: []string{`{"output_tokens":2,"server_tool_use":{"web_search_requests":3}}`}, want: 3,
			},
			{
				name: "cumulative_and_duplicate_snapshots", start: `{"input_tokens":10,"output_tokens":1,"server_tool_use":{"web_search_requests":1}}`,
				deltas: []string{
					`{"output_tokens":2,"server_tool_use":{"web_search_requests":2}}`,
					`{"output_tokens":2,"server_tool_use":{"web_search_requests":2}}`,
					`{"output_tokens":3,"server_tool_use":{"web_search_requests":3}}`,
					`{"output_tokens":4}`,
				}, want: 3,
			},
		} {
			t.Run(string(format)+"/"+tc.name, func(t *testing.T) {
				c, info := claudeSearchUsageContext(t, format)
				usage, apiErr := ClaudeStreamHandler(c, claudeSearchHTTPResponse(claudeSearchStreamBody(tc.start, tc.deltas...)), info)
				require.Nil(t, apiErr)
				require.Equal(t, tc.want, c.GetInt("claude_web_search_requests"))
				result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
				require.True(t, result.Pending, "actual unquoted searches must reach pending")
				require.Equal(t, []string{"web_search"}, result.MissingDimensions)
				require.Empty(t, result.InvalidDimensions)
				require.Contains(t, result.Dimensions, relaycommon.ZTAPIUsageDimension{
					Dimension: "web_search", Quantity: int64(tc.want), Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateMissing,
				})
			})
		}
	}
}

func TestClaudeWebSearchUsageFrozenQuotes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, price string
			listed      bool
			pending     bool
			state       relaycommon.ZTAPIQuoteState
		}{
			{"unlisted", "0.01", false, true, relaycommon.ZTAPIQuoteStateMissing},
			{"listed_missing_price", "", true, true, relaycommon.ZTAPIQuoteStateMissing},
			{"explicit_free", "0", true, false, relaycommon.ZTAPIQuoteStateFree},
			{"quoted", "0.01", true, false, relaycommon.ZTAPIQuoteStateQuoted},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				c, info := claudeSearchUsageContext(t, types.RelayFormatClaude)
				if tc.listed {
					info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "web_search")
				}
				if tc.price != "" {
					info.ZTAPIPublicationSnapshot.SaleUSD["web_search"] = tc.price
				}
				var usage *dto.Usage
				var apiErr *types.NewAPIError
				if stream {
					usage, apiErr = ClaudeStreamHandler(c, claudeSearchHTTPResponse(claudeSearchStreamBody(
						`{"input_tokens":10,"output_tokens":1}`,
						`{"output_tokens":2,"server_tool_use":{"web_search_requests":3}}`,
					)), info)
				} else {
					usage, apiErr = ClaudeHandler(c, claudeSearchHTTPResponse(`{"id":"msg_local","type":"message","content":[],"usage":{"input_tokens":10,"output_tokens":2,"server_tool_use":{"web_search_requests":3}}}`), info)
				}
				require.Nil(t, apiErr)
				result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
				require.Equal(t, tc.pending, result.Pending)
				require.Empty(t, result.InvalidDimensions)
				var search *relaycommon.ZTAPIUsageDimension
				for i := range result.Dimensions {
					if result.Dimensions[i].Dimension == "web_search" {
						search = &result.Dimensions[i]
					}
				}
				require.NotNil(t, search)
				require.Equal(t, int64(3), search.Quantity)
				require.Equal(t, "call", search.Unit)
				require.Equal(t, tc.state, search.QuoteState)
				if tc.pending {
					require.Equal(t, []string{"web_search"}, result.MissingDimensions)
					require.Empty(t, search.UnitPriceUSD)
				} else {
					require.Empty(t, result.MissingDimensions)
					require.Equal(t, tc.price, search.UnitPriceUSD)
				}
			})
		}
	}
}

func TestClaudeWebSearchUsageDeclarationsAreNotCalls(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, reported := range []string{"", `,"server_tool_use":{"web_search_requests":0}`} {
			t.Run(fmt.Sprintf("stream=%t/reported=%s", stream, reported), func(t *testing.T) {
				c, info := claudeSearchUsageContext(t, types.RelayFormatClaude)
				var request dto.ClaudeRequest
				require.NoError(t, common.UnmarshalJsonStr(`{"model":"claude-local","tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5}]}`, &request))
				info.Request = &request
				var usage *dto.Usage
				var apiErr *types.NewAPIError
				if stream {
					usage, apiErr = ClaudeStreamHandler(c, claudeSearchHTTPResponse(claudeSearchStreamBody(
						`{"input_tokens":10,"output_tokens":1}`,
						`{"output_tokens":2`+reported+`}`,
					)), info)
				} else {
					usage, apiErr = ClaudeHandler(c, claudeSearchHTTPResponse(`{"type":"message","tools":[{"type":"web_search_20250305","name":"web_search"}],"content":[],"usage":{"input_tokens":10,"output_tokens":2`+reported+`}}`), info)
				}
				require.Nil(t, apiErr)
				require.Zero(t, c.GetInt("claude_web_search_requests"))
				result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
				require.False(t, result.Pending)
				for _, dimension := range result.Dimensions {
					require.NotEqual(t, "web_search", dimension.Dimension)
				}
			})
		}
	}
}

func TestClaudeWebSearchUsageInvalidSnapshotsStayPending(t *testing.T) {
	for _, tc := range []struct {
		name, start, delta, reason string
		want                       int
	}{
		{"negative", `{"input_tokens":10,"output_tokens":1}`, `{"output_tokens":2,"server_tool_use":{"web_search_requests":-1}}`, "usage_invalid_quantity", 0},
		{"regression", `{"input_tokens":10,"output_tokens":1,"server_tool_use":{"web_search_requests":3}}`, `{"output_tokens":2,"server_tool_use":{"web_search_requests":2}}`, "usage_snapshot_conflict", 3},
		{"regression_to_zero", `{"input_tokens":10,"output_tokens":1,"server_tool_use":{"web_search_requests":3}}`, `{"output_tokens":2,"server_tool_use":{"web_search_requests":0}}`, "usage_snapshot_conflict", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, info := claudeSearchUsageContext(t, types.RelayFormatClaude)
			info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "web_search")
			info.ZTAPIPublicationSnapshot.SaleUSD["web_search"] = "0.01"
			usage, apiErr := ClaudeStreamHandler(c, claudeSearchHTTPResponse(claudeSearchStreamBody(tc.start, tc.delta)), info)
			require.Nil(t, apiErr)
			require.Equal(t, tc.want, c.GetInt("claude_web_search_requests"))
			result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
			require.True(t, result.Pending)
			require.Contains(t, result.InvalidDimensions, tc.reason)
		})
	}
	t.Run("nonstream_negative", func(t *testing.T) {
		c, info := claudeSearchUsageContext(t, types.RelayFormatClaude)
		usage, apiErr := ClaudeHandler(c, claudeSearchHTTPResponse(`{"type":"message","content":[],"usage":{"input_tokens":10,"output_tokens":2,"server_tool_use":{"web_search_requests":-1}}}`), info)
		require.Nil(t, apiErr)
		require.Zero(t, c.GetInt("claude_web_search_requests"))
		result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
		require.True(t, result.Pending)
		require.Contains(t, result.InvalidDimensions, "usage_invalid_quantity")
	})
}

func TestClaudeWebSearchUsageInterruptedStreamPreservesActualCount(t *testing.T) {
	c, info := claudeSearchUsageContext(t, types.RelayFormatClaude)
	body := claudeSearchStreamBody(`{"input_tokens":10,"output_tokens":1}`, `{"output_tokens":2,"server_tool_use":{"web_search_requests":3}}`)
	body = strings.TrimSuffix(body, "data: {\"type\":\"message_stop\"}\n\n")
	usage, apiErr := ClaudeStreamHandler(c, claudeSearchHTTPResponse(body), info)
	require.NotNil(t, apiErr)
	require.Equal(t, 3, c.GetInt("claude_web_search_requests"))
	result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
	require.True(t, result.Pending)
	require.Equal(t, []string{"web_search"}, result.MissingDimensions)
}

func TestClaudeWebSearchUsageNewResponseDoesNotInheritCount(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			c, info := claudeSearchUsageContext(t, types.RelayFormatClaude)
			c.Set("claude_web_search_requests", 9)
			var usage *dto.Usage
			var apiErr *types.NewAPIError
			if stream {
				usage, apiErr = ClaudeStreamHandler(c, claudeSearchHTTPResponse(claudeSearchStreamBody(
					`{"input_tokens":10,"output_tokens":1}`, `{"output_tokens":2}`,
				)), info)
			} else {
				usage, apiErr = ClaudeHandler(c, claudeSearchHTTPResponse(`{"type":"message","content":[],"usage":{"input_tokens":10,"output_tokens":2}}`), info)
			}
			require.Nil(t, apiErr)
			require.Zero(t, c.GetInt("claude_web_search_requests"))
			require.False(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
		})
	}
}

func TestClaudeResponseDebugLogOmitsBody(t *testing.T) {
	c, info := claudeSearchUsageContext(t, types.RelayFormatClaude)
	var logs bytes.Buffer
	oldDebug := common.DebugEnabled
	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	common.LogWriterMu.Unlock()
	common.DebugEnabled = true
	t.Cleanup(func() {
		common.DebugEnabled = oldDebug
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})
	body := `{"type":"message","content":[{"type":"text","text":"PRIVATE_BUSINESS_CANARY"},{"type":"tool_use","id":"tool_local","name":"lookup","input":{"query":"PRIVATE_TOOL_ARGUMENT_CANARY"}}],"usage":{"input_tokens":10,"output_tokens":2}}`
	usage, apiErr := ClaudeHandler(c, claudeSearchHTTPResponse(body), info)
	require.Nil(t, apiErr)
	require.Equal(t, 2, usage.CompletionTokens)
	require.NotContains(t, logs.String(), "PRIVATE_BUSINESS_CANARY")
	require.NotContains(t, logs.String(), "PRIVATE_TOOL_ARGUMENT_CANARY")
	require.Contains(t, logs.String(), fmt.Sprintf("response_body_bytes=%d", len(body)))
}

func TestClaudeToolArgumentLogOmitsBody(t *testing.T) {
	c, _ := claudeSearchUsageContext(t, types.RelayFormatClaude)
	var logs bytes.Buffer
	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultWriter
	gin.DefaultWriter = &logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter = oldWriter
		common.LogWriterMu.Unlock()
	})
	var request dto.GeneralOpenAIRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"model":"claude-local","messages":[{"role":"assistant","tool_calls":[{"id":"tool_local","type":"function","function":{"name":"lookup","arguments":"PRIVATE_INVALID_TOOL_ARGUMENT_CANARY"}}]}]}`, &request))
	converted, err := RequestOpenAI2ClaudeMessage(c, request)
	require.NoError(t, err)
	require.NotNil(t, converted)
	require.NotContains(t, logs.String(), "PRIVATE_INVALID_TOOL_ARGUMENT_CANARY")
	require.Contains(t, logs.String(), "arguments_bytes=36")
}
