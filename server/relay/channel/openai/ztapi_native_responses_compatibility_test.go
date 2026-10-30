package openai

import (
	"crypto/sha256"
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

const nativeReceipt = `{"input_tokens":120,"output_tokens":7,"total_tokens":127,"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":3}}`

func nativeResponsesContext(stream, managed bool) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	c, recorder, info := usageRegressionContext(types.RelayFormatOpenAI, managed)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info.IsStream = stream
	info.UpstreamModelName = "synthetic-native-model"
	info.FinalRequestRelayFormat = types.RelayFormatOpenAIResponses
	if managed {
		info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "cache_read")
		info.ZTAPIPublicationSnapshot.SaleUSD["cache_read"] = "1"
	}
	return c, recorder, info
}

func nativeResponseBody(status, receipt string) string {
	body := `{"id":"resp_synthetic","object":"response","model":"synthetic-native-model","status":"` + status + `","output":[]`
	if receipt != "absent" {
		body += `,"usage":` + receipt
	}
	return body + `}`
}

func nativeResponseEvent(kind, receipt string) string {
	status := "in_progress"
	if kind == "response.completed" {
		status = "completed"
	} else if kind == "response.failed" {
		status = "failed"
	} else if kind == "response.incomplete" {
		status = "incomplete"
	}
	return `{"type":"` + kind + `","sequence_number":19,"response":` + nativeResponseBody(status, receipt) + `}`
}

func nativeSSE(frames ...string) string {
	return "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
}

func nativeHTTPResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestZTAPINativeResponsesClientToolsDoNotCreateBillableCalls(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		t.Run(kind, func(t *testing.T) {
			c, _, info := nativeResponsesContext(false, true)
			info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
				kind: {ToolName: kind},
			}}
			body := strings.TrimSuffix(nativeResponseBody("completed", nativeReceipt), "}") + `,"tools":[{"type":"` + kind + `","name":"lookup"}]}`
			usage, apiErr := OaiResponsesHandler(c, info, nativeHTTPResponse(body))
			require.Nil(t, apiErr)
			require.Zero(t, info.ResponsesUsageInfo.BuiltInTools[kind].CallCount)
			require.False(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
		})
	}
}

func TestZTAPINativeResponsesFailedHostedCallRetainsItsDimension(t *testing.T) {
	for _, quoted := range []bool{false, true} {
		c, _, info := nativeResponsesContext(false, true)
		if quoted {
			info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "file_search")
			info.ZTAPIPublicationSnapshot.SaleUSD["file_search"] = "0.002"
		}
		body := strings.Replace(nativeResponseBody("incomplete", nativeReceipt), `"output":[]`, `"output":[{"id":"fs_1","type":"file_search_call","status":"completed"}]`, 1)
		usage, apiErr := OaiResponsesHandler(c, info, nativeHTTPResponse(body))
		require.NotNil(t, apiErr)
		check := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
		require.Equal(t, !quoted, check.Pending)
		var calls int64
		for _, d := range check.Dimensions {
			if d.Dimension == "file_search" {
				calls += d.Quantity
			}
		}
		require.EqualValues(t, 1, calls)
	}
}

func TestZTAPINativeResponsesHostedCountersDeduplicateAndFailClosed(t *testing.T) {
	for _, kind := range []string{"file_search_call", "web_search_call", "image_generation_call", "code_interpreter_call"} {
		t.Run(kind, func(t *testing.T) {
			_, _, info := nativeResponsesContext(true, true)
			seen := make(map[string]string)
			item := dto.ResponsesOutput{ID: "hosted_1", Type: kind, Status: "completed"}
			captureNativeHostedCalls(info, []dto.ResponsesOutput{item}, seen)
			captureNativeHostedCalls(info, []dto.ResponsesOutput{item}, seen)
			count := 0
			for _, tool := range info.ResponsesUsageInfo.BuiltInTools {
				count += tool.CallCount
			}
			require.Equal(t, 1, count)
			check := relaycommon.ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22})
			require.True(t, check.Pending)
		})
	}
}

func TestZTAPINativeResponsesHostedStateChangesRemainUnconfirmedOnce(t *testing.T) {
	for _, statuses := range [][]string{{"completed", "failed"}, {"failed", "failed"}, {"in_progress", "completed"}} {
		_, _, info := nativeResponsesContext(true, true)
		seen := make(map[string]string)
		for _, status := range statuses {
			captureNativeHostedCalls(info, []dto.ResponsesOutput{{ID: "fs_1", Type: "file_search_call", Status: status}}, seen)
		}
		tool := info.ResponsesUsageInfo.BuiltInTools["responses_hosted_usage_unconfirmed"]
		require.NotNil(t, tool, statuses)
		require.Equal(t, 1, tool.CallCount, statuses)
	}
}

func runNativeResponses(t *testing.T, stream, managed bool, body string) (*dto.Usage, *types.NewAPIError, *gin.Context, *relaycommon.RelayInfo, string) {
	t.Helper()
	c, recorder, info := nativeResponsesContext(stream, managed)
	var usage *dto.Usage
	var apiErr *types.NewAPIError
	if stream {
		usage, apiErr = OaiResponsesStreamHandler(c, info, nativeHTTPResponse(body))
	} else {
		usage, apiErr = OaiResponsesHandler(c, info, nativeHTTPResponse(body))
	}
	return usage, apiErr, c, info, recorder.Body.String()
}

func assertNativeReceipt(t *testing.T, c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, location string, observations int) {
	t.Helper()
	require.NotNil(t, usage)
	require.Equal(t, 120, usage.PromptTokens)
	require.Equal(t, 7, usage.CompletionTokens)
	require.Equal(t, 127, usage.TotalTokens)
	require.Equal(t, 40, usage.PromptTokensDetails.CachedTokens)
	require.Equal(t, 3, usage.CompletionTokenDetails.ReasoningTokens)
	require.Equal(t, "upstream_reported", usage.UsageSource)
	require.Equal(t, "openai", usage.UsageSemantic)
	require.False(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
	require.NotNil(t, info.ZTAPIMetering)
	evidence := info.ZTAPIMetering.Sanitized()
	require.NotNil(t, evidence)
	require.True(t, evidence.UsageIntegrityComplete)
	require.Equal(t, "upstream_reported", evidence.UsageSource)
	require.Equal(t, location, evidence.UsageLocation)
	require.Equal(t, string(types.RelayFormatOpenAIResponses), evidence.UpstreamProtocol)
	require.Equal(t, observations, evidence.UsageObservations)
	require.JSONEq(t, nativeReceipt, string(evidence.RawUsage))
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(evidence.RawUsage)), evidence.RawUsageSHA256)
}

func TestZTAPINativeResponsesStreamFiltersOnlyMetadata(t *testing.T) {
	frames := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_synthetic","status":"in_progress","usage":null}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_synthetic","status":"in_progress"}}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_synthetic","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_synthetic","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_synthetic","output_index":0,"content_index":0,"delta":"Synthetic text"}`,
		`{"type":"response.metadata","sequence_number":5,"opaque":{"marker":"SYNTHETIC_EXTENSION_ONLY"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":6,"item_id":"fc_synthetic","output_index":1,"delta":"{}"}`,
		`{"type":"response.reasoning_summary_text.delta","sequence_number":7,"item_id":"rs_synthetic","output_index":2,"summary_index":0,"delta":"Synthetic summary"}`,
		`{"type":"response.future_standard_event","sequence_number":8,"future_field":{"preserve":true}}`,
		`{"type":"response.output_text.done","sequence_number":9,"item_id":"msg_synthetic","output_index":0,"content_index":0,"text":"Synthetic text"}`,
		`{"type":"response.content_part.done","sequence_number":10,"item_id":"msg_synthetic","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Synthetic text","annotations":[]}}`,
		`{"type":"response.output_item.done","sequence_number":11,"output_index":0,"item":{"id":"msg_synthetic","type":"message","status":"completed","role":"assistant","content":[]}}`,
		nativeResponseEvent("response.completed", nativeReceipt),
	}
	_, apiErr, _, _, output := runNativeResponses(t, true, true, nativeSSE(frames...))
	require.Nil(t, apiErr)
	require.NotContains(t, output, "response.metadata")
	require.NotContains(t, output, "SYNTHETIC_EXTENSION_ONLY")
	var want strings.Builder
	for _, frame := range frames {
		var event struct {
			Type string `json:"type"`
		}
		require.NoError(t, common.UnmarshalJsonStr(frame, &event))
		if event.Type != "response.metadata" {
			fmt.Fprintf(&want, "event: %s\ndata: %s\n\n", event.Type, frame)
		}
	}
	require.Equal(t, want.String(), output, "preserve raw payload, IDs, sequence gaps and event order")
}

func TestZTAPINativeResponsesMetadataIsOpaqueAndNotAReceipt(t *testing.T) {
	metadata := `{"type":"response.metadata","response":{"usage":{"input_tokens":9999,"output_tokens":9999,"total_tokens":19998}},"delta":{"opaque":true}}`
	usage, apiErr, c, info, output := runNativeResponses(t, true, true, nativeSSE(metadata, nativeResponseEvent("response.completed", nativeReceipt)))
	require.Nil(t, apiErr)
	require.NotContains(t, output, "response.metadata")
	assertNativeReceipt(t, c, info, usage, "stream", 1)
}

func TestZTAPINativeResponsesUsageEvidence(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			body, location := nativeResponseBody("completed", nativeReceipt), "nonstream"
			if stream {
				body, location = nativeSSE(nativeResponseEvent("response.completed", nativeReceipt)), "stream"
			}
			usage, apiErr, c, info, output := runNativeResponses(t, stream, true, body)
			require.Nil(t, apiErr)
			assertNativeReceipt(t, c, info, usage, location, 1)
			if !stream {
				require.Equal(t, body, output, "native nonstream payload must be byte-preserved")
			}
		})
	}
}

func TestZTAPINativeResponsesExplicitZerosAreNotEstimated(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, managed := range []bool{false, true} {
			for _, tc := range []struct {
				name, receipt string
				input, output int
			}{
				{"input_zero", `{"input_tokens":0,"output_tokens":7,"total_tokens":7}`, 0, 7},
				{"output_zero", `{"input_tokens":120,"output_tokens":0,"total_tokens":120}`, 120, 0},
				{"all_zero", `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, 0, 0},
			} {
				t.Run(fmt.Sprintf("stream=%t/managed=%t/%s", stream, managed, tc.name), func(t *testing.T) {
					body := nativeResponseBody("completed", tc.receipt)
					if stream {
						body = nativeSSE(`{"type":"response.output_text.delta","delta":"Synthetic nonempty text"}`, nativeResponseEvent("response.completed", tc.receipt))
					}
					usage, apiErr, c, info, _ := runNativeResponses(t, stream, managed, body)
					require.Nil(t, apiErr)
					require.NotNil(t, usage)
					require.Equal(t, tc.input, usage.PromptTokens)
					require.Equal(t, tc.output, usage.CompletionTokens)
					require.Equal(t, tc.input+tc.output, usage.TotalTokens)
					if managed {
						require.Equal(t, "upstream_reported", usage.UsageSource)
						require.False(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
					}
				})
			}
		}
	}
}

func TestZTAPINativeResponsesUnverifiedUsageStaysPending(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct{ name, receipt, reason string }{
			{"absent", "absent", ""},
			{"null", "null", ""},
			{"empty", `{}`, ""},
			{"partial", `{"input_tokens":120}`, "usage_incomplete"},
			{"alias_conflict", `{"input_tokens":120,"prompt_tokens":121,"output_tokens":7}`, "usage_alias_conflict"},
			{"total_conflict", `{"input_tokens":120,"output_tokens":7,"total_tokens":999}`, "usage_total_mismatch"},
			{"negative", `{"input_tokens":-1,"output_tokens":7}`, "usage_invalid_quantity"},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				body := nativeResponseBody("completed", tc.receipt)
				if stream {
					body = nativeSSE(`{"type":"response.output_text.delta","delta":"Synthetic nonempty text"}`, nativeResponseEvent("response.completed", tc.receipt))
				}
				usage, apiErr, c, info, _ := runNativeResponses(t, stream, true, body)
				require.Nil(t, apiErr)
				result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
				require.True(t, result.Pending)
				require.True(t, usage == nil || usage.PromptTokens == 0 && usage.CompletionTokens == 0, "unverified receipts cannot authorize estimated or partial billing")
				if tc.reason != "" {
					require.Contains(t, result.InvalidDimensions, tc.reason)
				}
				if info.ZTAPIMetering != nil {
					require.Zero(t, info.ZTAPIMetering.UsageObservations)
				}
			})
		}
	}
}

func TestZTAPINativeResponsesStreamIntegrityIsSticky(t *testing.T) {
	for _, poison := range []struct{ name, receipt, reason string }{
		{"partial", `{"input_tokens":355000,"provider_note":"SYNTHETIC_PRIVATE_NOTE"}`, "usage_incomplete"},
		{"alias_conflict", `{"input_tokens":120,"prompt_tokens":121,"output_tokens":7}`, "usage_alias_conflict"},
		{"cache_conflict", `{"input_tokens":120,"output_tokens":7,"input_tokens_details":{"cached_tokens":0},"prompt_tokens_details":{"cached_tokens":40}}`, "usage_alias_conflict"},
		{"regression", `{"input_tokens":119,"output_tokens":7,"total_tokens":126}`, "usage_snapshot_conflict"},
	} {
		t.Run(poison.name, func(t *testing.T) {
			frames := []string{
				nativeResponseEvent("response.in_progress", nativeReceipt),
				nativeResponseEvent("response.in_progress", poison.receipt),
				nativeResponseEvent("response.completed", nativeReceipt),
			}
			usage, apiErr, c, info, _ := runNativeResponses(t, true, true, nativeSSE(frames...))
			require.Nil(t, apiErr)
			require.Equal(t, 120, usage.PromptTokens)
			require.Equal(t, 7, usage.CompletionTokens)
			require.Equal(t, "upstream_reported", usage.UsageSource)
			result := relaycommon.ValidateZTAPIUsageDimensions(c, info, usage)
			require.True(t, result.Pending)
			require.Contains(t, result.InvalidDimensions, poison.reason)
			fresh := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: info.ZTAPIPublicationSnapshot}
			require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, fresh, usage).Pending)
			require.Equal(t, 2, info.ZTAPIMetering.UsageObservations)
			require.JSONEq(t, nativeReceipt, string(info.ZTAPIMetering.RawUsage))
			evidence, err := common.Marshal(info.ZTAPIMetering.Sanitized())
			require.NoError(t, err)
			require.NotContains(t, string(evidence), "SYNTHETIC_PRIVATE_NOTE")
			require.NotNil(t, info.ZTAPIMetering.UnverifiedUsage)
			require.Equal(t, poison.reason, info.ZTAPIMetering.UnverifiedUsage.Reason)
		})
	}
	for _, empty := range []string{"absent", "null", `{}`} {
		t.Run("empty_terminal/"+empty, func(t *testing.T) {
			usage, apiErr, c, info, _ := runNativeResponses(t, true, true, nativeSSE(nativeResponseEvent("response.in_progress", nativeReceipt), nativeResponseEvent("response.completed", empty)))
			require.Nil(t, apiErr)
			assertNativeReceipt(t, c, info, usage, "stream", 1)
		})
	}
	t.Run("invalid_first_then_valid", func(t *testing.T) {
		usage, apiErr, c, info, _ := runNativeResponses(t, true, true, nativeSSE(
			nativeResponseEvent("response.in_progress", `{"input_tokens":355000,"provider_note":"SYNTHETIC_PRIVATE_NOTE"}`),
			nativeResponseEvent("response.completed", nativeReceipt),
		))
		require.Nil(t, apiErr)
		require.Equal(t, 120, usage.PromptTokens)
		require.Equal(t, 7, usage.CompletionTokens)
		require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
		require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_incomplete")
		require.Equal(t, 1, info.ZTAPIMetering.UsageObservations)
		require.Contains(t, string(info.ZTAPIMetering.UnverifiedUsage.RawUsage), `"input_tokens":355000`)
		require.NotContains(t, string(info.ZTAPIMetering.UnverifiedUsage.RawUsage), "SYNTHETIC_PRIVATE_NOTE")
	})
	t.Run("regressing_zero_terminal", func(t *testing.T) {
		usage, apiErr, c, info, _ := runNativeResponses(t, true, true, nativeSSE(
			nativeResponseEvent("response.in_progress", nativeReceipt),
			nativeResponseEvent("response.completed", `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`),
		))
		require.Nil(t, apiErr)
		require.Equal(t, 120, usage.PromptTokens)
		require.Equal(t, 7, usage.CompletionTokens)
		require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
		require.Contains(t, info.ZTAPIMetering.UsageIntegrityReasons, "usage_snapshot_conflict")
		require.Equal(t, 1, info.ZTAPIMetering.UsageObservations)
	})
}

func TestZTAPINativeResponsesStreamFailuresRetainConfirmedUsage(t *testing.T) {
	for _, tc := range []struct{ name, terminal string }{
		{"failed", nativeResponseEvent("response.failed", nativeReceipt)},
		{"incomplete", nativeResponseEvent("response.incomplete", nativeReceipt)},
		{"top_level_error", `{"type":"error","code":"synthetic_failure","message":"Synthetic failure"}`},
		{"nested_error", `{"type":"response.error","error":{"type":"server_error","message":"Synthetic failure"}}`},
		{"failed_receipt_error", `{"type":"response.failed","response":{"status":"failed","error":{"type":"server_error","code":"synthetic_failure","message":"Synthetic failure"},"usage":` + nativeReceipt + `}}`},
		{"eof_without_terminal", ""},
		{"done_without_terminal", "[DONE]"},
		{"malformed_then_completed", `not-json` + "\n\ndata: " + nativeResponseEvent("response.completed", "absent")},
		{"completed_without_response", `{"type":"response.completed"}`},
		{"completed_with_failed_status", `{"type":"response.completed","response":{"status":"failed"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, apiErr, c, info, output := runNativeResponses(t, true, true, nativeSSE(nativeResponseEvent("response.in_progress", nativeReceipt), tc.terminal))
			require.NotNil(t, apiErr, "failure or missing terminal must not become success")
			require.True(t, types.IsSkipRetryError(apiErr), "do not retry a consumed native stream")
			require.NotContains(t, output, `"type":"response.completed"`, "failed streams must not advertise a successful completion")
			observations := 1
			if strings.Contains(tc.terminal, `"usage":`+nativeReceipt) {
				observations++
			}
			assertNativeReceipt(t, c, info, usage, "stream", observations)
		})
	}
}

func TestZTAPINativeResponsesDownstreamFailureDrainsUsage(t *testing.T) {
	c, recorder, info := nativeResponsesContext(true, true)
	c.Writer = &nativeFailingGinWriter{ResponseWriter: c.Writer}
	usage, apiErr := OaiResponsesStreamHandler(c, info, nativeHTTPResponse(nativeSSE(
		`{"type":"response.output_text.delta","delta":"Synthetic text"}`,
		nativeResponseEvent("response.completed", nativeReceipt),
	)))
	require.NotNil(t, apiErr)
	require.True(t, types.IsSkipRetryError(apiErr))
	assertNativeReceipt(t, c, info, usage, "stream", 1)
	require.Empty(t, recorder.Body.String())
}

type nativeFailingGinWriter struct{ gin.ResponseWriter }

func (w *nativeFailingGinWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func (w *nativeFailingGinWriter) WriteString(string) (int, error) { return 0, io.ErrClosedPipe }

func TestZTAPINativeResponsesNonstreamFailuresRetainConfirmedUsage(t *testing.T) {
	for _, status := range []string{"failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			body := nativeResponseBody(status, nativeReceipt)
			usage, apiErr, c, info, _ := runNativeResponses(t, false, true, body)
			require.NotNil(t, apiErr)
			require.True(t, types.IsSkipRetryError(apiErr))
			require.NotEmpty(t, apiErr.Error(), "a terminal failure without provider details still needs a useful error")
			assertNativeReceipt(t, c, info, usage, "nonstream", 1)
		})
	}
	body := `{"id":"resp_synthetic","status":"failed","error":{"type":"server_error","message":"Synthetic failure"},"usage":` + nativeReceipt + `}`
	usage, apiErr, c, info, _ := runNativeResponses(t, false, true, body)
	require.NotNil(t, apiErr)
	require.True(t, types.IsSkipRetryError(apiErr))
	assertNativeReceipt(t, c, info, usage, "nonstream", 1)
}

func TestZTAPINativeResponsesNonstreamDownstreamFailureRetainsUsage(t *testing.T) {
	c, _, info := nativeResponsesContext(false, true)
	c.Writer = &nativeFailingGinWriter{ResponseWriter: c.Writer}
	usage, apiErr := OaiResponsesHandler(c, info, nativeHTTPResponse(nativeResponseBody("completed", nativeReceipt)))
	require.NotNil(t, apiErr)
	require.True(t, types.IsSkipRetryError(apiErr))
	assertNativeReceipt(t, c, info, usage, "nonstream", 1)
}

type nativeErrorReader struct{}

func (nativeErrorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestZTAPINativeResponsesScannerErrorRetainsUsage(t *testing.T) {
	c, _, info := nativeResponsesContext(true, true)
	resp := nativeHTTPResponse("")
	resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(nativeSSE(nativeResponseEvent("response.in_progress", nativeReceipt))), nativeErrorReader{}))
	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)
	require.NotNil(t, apiErr)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.Equal(t, relaycommon.StreamEndReasonScannerErr, info.StreamStatus.EndReason)
	assertNativeReceipt(t, c, info, usage, "stream", 1)
}

func TestZTAPINativeResponsesFailureIsTerminalWithoutUsage(t *testing.T) {
	for _, kind := range []string{"response.failed", "response.incomplete", "response.error", "error"} {
		t.Run(kind, func(t *testing.T) {
			usage, apiErr, c, info, output := runNativeResponses(t, true, true, nativeSSE(
				`{"type":"response.output_text.delta","delta":"Synthetic text"}`,
				nativeResponseEvent(kind, "absent"),
				nativeResponseEvent("response.completed", nativeReceipt),
			))
			require.NotNil(t, apiErr)
			require.True(t, types.IsSkipRetryError(apiErr))
			require.NotEmpty(t, apiErr.Error())
			require.NotContains(t, output, "response.completed")
			require.NotNil(t, usage)
			require.Zero(t, usage.TotalTokens)
			require.True(t, relaycommon.ValidateZTAPIUsageDimensions(c, info, usage).Pending)
			if info.ZTAPIMetering != nil {
				require.Zero(t, info.ZTAPIMetering.UsageObservations)
			}
		})
	}
}

func TestZTAPINativeResponsesLegacyMissingReceiptStillEstimates(t *testing.T) {
	usage, apiErr, _, _, _ := runNativeResponses(t, true, false, nativeSSE(
		`{"type":"response.output_text.delta","delta":"Synthetic text"}`,
		nativeResponseEvent("response.completed", "absent"),
	))
	require.Nil(t, apiErr)
	require.Equal(t, 1000, usage.PromptTokens)
	require.Positive(t, usage.CompletionTokens)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
}

func TestZTAPINativeResponsesLegacyReceiptStillNormalizesTotal(t *testing.T) {
	usage, apiErr, _, _, _ := runNativeResponses(t, true, false, nativeSSE(
		nativeResponseEvent("response.completed", `{"input_tokens":120,"output_tokens":7}`),
	))
	require.Nil(t, apiErr)
	require.Equal(t, 120, usage.PromptTokens)
	require.Equal(t, 7, usage.CompletionTokens)
	require.Equal(t, 127, usage.TotalTokens)
}

func TestZTAPINativeResponsesCompletedIsTerminal(t *testing.T) {
	usage, apiErr, c, info, output := runNativeResponses(t, true, true, nativeSSE(
		nativeResponseEvent("response.completed", nativeReceipt),
		nativeResponseEvent("response.failed", `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`),
	))
	require.Nil(t, apiErr)
	require.NotContains(t, output, "response.failed")
	assertNativeReceipt(t, c, info, usage, "stream", 1)
}
