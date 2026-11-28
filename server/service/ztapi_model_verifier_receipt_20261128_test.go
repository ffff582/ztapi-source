package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const ztapiReceiptChatUsage20261128 = `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23,"prompt_tokens_details":{"cached_tokens":4,"cache_creation_tokens":2},"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2}}`
const ztapiReceiptResponsesUsage20261128 = `{"input_tokens":20,"output_tokens":3,"total_tokens":23,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":1}}`

func TestZTAPIProbeReceipt20261128ReusesRequestIDSafetyPolicy(t *testing.T) {
	for _, id := range []string{"sk-foreign-secret-0123456789abcdef", "https://supplier.invalid/request", "req/signed-part+value="} {
		require.Empty(t, ztapiProbeReceiptID20261128(id, "another-current-key"))
	}
	require.Equal(t, "req-safe_123", ztapiProbeReceiptID20261128("req-safe_123", "another-current-key"))
}

const ztapiReceiptBillingUsage20261128 = `{"source":"claude_messages","semantic":"anthropic","claude_usage":{"input_tokens":16,"output_tokens":4,"cache_read_input_tokens":3,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3},"claude_cache_creation_5_m_tokens":2,"claude_cache_creation_1_h_tokens":3}}`

func ztapiReceiptBody20261128(responses, stream bool, usage string) string {
	if responses {
		body := strings.Replace(ztapiVerifierResponsesSuccess, `{"input_tokens":3,"output_tokens":2,"total_tokens":5}`, usage, 1)
		if stream {
			return ztapiVerifierResponsesStream(body)
		}
		return body
	}
	body := ztapiVerifierChatSuccess
	if stream {
		body = ztapiVerifierChatStreamSuccess
	}
	return strings.Replace(body, `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`, usage, 1)
}

func TestZTAPIProbeReceipt20261128RetainsNativeSafeUsage(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			source, usageJSON := "gpt-6.2-sol", ztapiReceiptChatUsage20261128
			if responses {
				source, usageJSON = "gpt-5.4-pro", ztapiReceiptResponsesUsage20261128
			}
			noisyUsage := strings.TrimSuffix(usageJSON, "}") + `,"private_text":"PRIVATE_TEXT synthetic-key","headers":{"Authorization":"Bearer synthetic-key"}}`
			body := ztapiReceiptBody20261128(responses, stream, noisyUsage)
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{
					"X-Request-Id": {"req-success-123"}, "Authorization": {"Bearer synthetic-key"},
				}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			var receipts []model.ZTAPIModelProbeReceipt20261128
			usage, status, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid/v1/probe", "synthetic-key", source, stream, &receipts)
			require.NoError(t, err)
			require.Equal(t, 200, status)
			require.Equal(t, ztapiProbeUsage{PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23}, usage)
			require.Len(t, receipts, 1)
			receipt := receipts[0]
			wantMode := "nonstream"
			if stream {
				wantMode = "stream"
			}
			require.Equal(t, wantMode, receipt.Mode)
			require.Equal(t, 200, receipt.StatusCode)
			require.Equal(t, "req-success-123", receipt.RequestID)
			require.True(t, receipt.Complete)
			require.JSONEq(t, usageJSON, receipt.UsageJSON)
			encoded, err := common.Marshal(receipt)
			require.NoError(t, err)
			for _, secret := range []string{"PRIVATE_TEXT", "synthetic-key", "Authorization", "headers", "choices", "content", "77"} {
				require.NotContains(t, string(encoded), secret)
			}
		}
	}
}

func TestZTAPIProbeReceipt20261128DoesNotInventTTL(t *testing.T) {
	const rawUsage = `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23,"cache_creation_input_tokens":7,"prompt_tokens_details":{"cached_tokens":0}}`
	client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Request-Id": {"req-generic-cache"}},
			Body: io.NopCloser(strings.NewReader(ztapiReceiptBody20261128(false, false, rawUsage)))}, nil
	})}
	var receipts []model.ZTAPIModelProbeReceipt20261128
	_, _, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid", "synthetic-key", "gpt-6.2-sol", false, &receipts)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.JSONEq(t, rawUsage, receipts[0].UsageJSON)
	require.NotContains(t, receipts[0].UsageJSON, "5m")
	require.NotContains(t, receipts[0].UsageJSON, "1h")
	require.NotContains(t, receipts[0].UsageJSON, "cache_write")
}

func TestZTAPIProbeReceipt20261128UsesOnlyExplicitRequestIDs(t *testing.T) {
	for _, tc := range []struct {
		name, source, body, wantID string
		stream                     bool
	}{
		{"body_request_id", "gpt-6.2-sol", strings.Replace(ztapiVerifierChatSuccess, `{"choices"`, `{"request_id":"req-body","choices"`, 1), "req-body", false},
		{"response_request_id", "gpt-5.4-pro", ztapiVerifierResponsesStream(strings.Replace(ztapiVerifierResponsesSuccess, `{"status"`, `{"request_id":"req-response","status"`, 1)), "req-response", true},
		{"completion_id_is_not_request_id", "gpt-6.2-sol", strings.Replace(ztapiVerifierChatSuccess, `{"choices"`, `{"id":"chatcmpl-not-a-request-id","choices"`, 1), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			var receipts []model.ZTAPIModelProbeReceipt20261128
			_, _, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid", "synthetic-key", tc.source, tc.stream, &receipts)
			require.NoError(t, err)
			require.Len(t, receipts, 1)
			require.Equal(t, tc.wantID, receipts[0].RequestID)
			require.Equal(t, tc.wantID != "", receipts[0].Complete)
		})
	}
}

type ztapiReceiptBrokenBody20261128 struct {
	*strings.Reader
}

func (b ztapiReceiptBrokenBody20261128) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func (b ztapiReceiptBrokenBody20261128) Close() error { return nil }

func TestZTAPIProbeReceipt20261128KeepsFailedAndPartialCalls(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		stream     bool
		status     int
		broken     bool
	}{
		{"http_error", `{"request_id":"req-partial","error":{"message":"PRIVATE_TEXT synthetic-key"},"usage":` + ztapiReceiptChatUsage20261128 + `}`, false, 429, false},
		{"read_error", ztapiReceiptBody20261128(false, true, ztapiReceiptChatUsage20261128), true, 200, true},
		{"missing_done", strings.TrimSuffix(ztapiReceiptBody20261128(false, true, ztapiReceiptChatUsage20261128), "data: [DONE]\n\n"), true, 200, false},
		{"wrong_answer", strings.Replace(ztapiReceiptBody20261128(false, false, ztapiReceiptChatUsage20261128), `"77"`, `"76"`, 1), false, 200, false},
		{"oversized", ztapiReceiptBody20261128(false, false, ztapiReceiptChatUsage20261128) + strings.Repeat(" ", ztapiVerificationBodyLimit), false, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.body))
				if tc.broken {
					body = ztapiReceiptBrokenBody20261128{strings.NewReader(tc.body)}
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"X-Request-Id": {"req-partial"}}, Body: body}, nil
			})}
			var receipts []model.ZTAPIModelProbeReceipt20261128
			_, status, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid", "synthetic-key", "gpt-6.2-sol", tc.stream, &receipts)
			require.Error(t, err)
			require.Equal(t, tc.status, status)
			require.Len(t, receipts, 1)
			require.Equal(t, "req-partial", receipts[0].RequestID)
			require.Equal(t, tc.status, receipts[0].StatusCode)
			require.False(t, receipts[0].Complete)
			require.JSONEq(t, ztapiReceiptChatUsage20261128, receipts[0].UsageJSON)
			require.NotContains(t, receipts[0].UsageJSON, "PRIVATE_TEXT")
		})
	}
}

func TestZTAPIProbeReceipt20261128KeepsFailedResponsesUsage(t *testing.T) {
	body := "data: {\"type\":\"response.failed\",\"response\":{\"request_id\":\"req-failed\",\"status\":\"failed\",\"usage\":" + ztapiReceiptResponsesUsage20261128 + "}}\n\n"
	client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	var receipts []model.ZTAPIModelProbeReceipt20261128
	_, _, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid", "synthetic-key", "gpt-5.4-pro", true, &receipts)
	require.Error(t, err)
	require.Len(t, receipts, 1)
	require.Equal(t, "req-failed", receipts[0].RequestID)
	require.False(t, receipts[0].Complete)
	require.JSONEq(t, ztapiReceiptResponsesUsage20261128, receipts[0].UsageJSON)
}

func TestZTAPIProbeReceipt20261128RejectsUnsafeMetadataWithoutChangingVerdict(t *testing.T) {
	for _, tc := range []struct{ name, requestID, usage string }{
		{"no_id", "", `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}`},
		{"key_in_id", "req-synthetic-key", `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}`},
		{"long_id", strings.Repeat("a", 257), `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}`},
		{"invalid_cache", "req-safe", `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23,"cache_creation_input_tokens":"PRIVATE_TEXT synthetic-key"}`},
		{"negative_cache", "req-safe", `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23,"cache_creation_input_tokens":-1}`},
		{"fractional_cache", "req-safe", `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23,"cache_creation_input_tokens":1.5}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {tc.requestID}},
					Body: io.NopCloser(strings.NewReader(ztapiReceiptBody20261128(false, false, tc.usage)))}, nil
			})}
			var receipts []model.ZTAPIModelProbeReceipt20261128
			_, _, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid", "synthetic-key", "gpt-6.2-sol", false, &receipts)
			require.NoError(t, err, "receipt completeness must not change compatibility acceptance")
			require.Len(t, receipts, 1)
			require.False(t, receipts[0].Complete)
			require.NotContains(t, receipts[0].UsageJSON, "synthetic-key")
			require.NotContains(t, receipts[0].RequestID, "synthetic-key")
			require.JSONEq(t, `{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}`, receipts[0].UsageJSON)
		})
	}
}

func TestZTAPIProbeReceipt20261128CollectsOnlyTextValidKeyAttempts(t *testing.T) {
	for _, tc := range []struct {
		name, source, endpoint, key string
		transportFailure            bool
		wantCalls, wantReceipts     int
	}{
		{"transport_failure", "gpt-6.2-sol", "https://fixture.invalid", "synthetic-key", true, 1, 1},
		{"invalid_key_probe", "gpt-6.2-sol", "https://fixture.invalid", "ztapi-deliberately-invalid-credential", false, 1, 0},
		{"embedding", "text-embedding-ada-002", "https://fixture.invalid", "synthetic-key", false, 1, 0},
		{"image", "gpt-image-2", "https://fixture.invalid", "synthetic-key", false, 1, 0},
		{"video", "doubao-seedance-2-5", "https://fixture.invalid", "synthetic-key", false, 1, 0},
		{"not_dispatched", "gpt-6.2-sol", "://invalid", "synthetic-key", false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if tc.transportFailure {
					return nil, errors.New("PRIVATE_TEXT synthetic-key")
				}
				return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})}
			var receipts []model.ZTAPIModelProbeReceipt20261128
			_, _, err := performZTAPIOpenAIProbe(context.Background(), client, tc.endpoint, tc.key, tc.source, false, &receipts)
			require.Error(t, err)
			require.Equal(t, tc.wantCalls, calls)
			require.Len(t, receipts, tc.wantReceipts)
			if len(receipts) > 0 {
				require.False(t, receipts[0].Complete)
				require.Zero(t, receipts[0].StatusCode)
				require.Empty(t, receipts[0].RequestID)
				require.Empty(t, receipts[0].UsageJSON)
			}
		})
	}
}

func TestZTAPIProbeReceipt20261128RunnerRetainsEachPaidMode(t *testing.T) {
	for _, failureAt := range []int{0, 1, 2} {
		for _, responses := range []bool{false, true} {
			source := "gpt-6.2-sol"
			usageJSON := ztapiReceiptChatUsage20261128
			if responses {
				source, usageJSON = "gpt-5.4-pro", ztapiReceiptResponsesUsage20261128
			}
			base := "https://fixture.invalid"
			channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "synthetic-key", BaseURL: &base}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			calls := 0
			http.DefaultTransport = ztapiVerifierRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") == "Bearer ztapi-deliberately-invalid-credential" {
					return &http.Response{StatusCode: 401, Header: http.Header{"X-Request-Id": {"invalid-key-not-a-paid-receipt"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				}
				stream := calls == 2
				currentUsage := usageJSON
				if stream {
					currentUsage = strings.ReplaceAll(strings.ReplaceAll(currentUsage, ":20", ":30"), ":23", ":33")
				}
				body := ztapiReceiptBody20261128(responses, stream, currentUsage)
				if calls == failureAt {
					body = strings.ReplaceAll(strings.ReplaceAll(body, `"77"`, `"76"`), `"content":"7"`, `"content":"6"`)
				}
				id := "req-nonstream"
				if stream {
					id = "req-stream"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {id}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			result, err := runZTAPIModelVerificationProbes(context.Background(), channel, source)
			http.DefaultTransport = previous
			wantReceipts := 2
			if failureAt == 1 {
				wantReceipts = 1
			}
			require.Len(t, result.ProbeReceipts, wantReceipts)
			require.Equal(t, "nonstream", result.ProbeReceipts[0].Mode)
			require.Equal(t, "req-nonstream", result.ProbeReceipts[0].RequestID)
			require.Equal(t, failureAt != 1, result.ProbeReceipts[0].Complete)
			if wantReceipts == 2 {
				require.Equal(t, "stream", result.ProbeReceipts[1].Mode)
				require.Equal(t, "req-stream", result.ProbeReceipts[1].RequestID)
				require.Equal(t, failureAt != 2, result.ProbeReceipts[1].Complete)
				require.Equal(t, int64(33), gjson.Get(result.ProbeReceipts[1].UsageJSON, "total_tokens").Int())
				require.Equal(t, 23, result.TotalTokens, "legacy result remains nonstream-only")
			}
			if failureAt == 0 {
				require.NoError(t, err)
				require.Equal(t, 3, calls)
				require.True(t, result.InvalidKeyClassified)
			} else {
				require.Error(t, err)
				require.Equal(t, failureAt, calls)
			}
		}
	}
}

func TestZTAPIProbeReceipt20261128ReturnsTransientEvidence(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "partial_failure"
		}
		t.Run(name, func(t *testing.T) {
			channel, config := setupZTAPIModelVerifierTestDB(t)
			receipts := []model.ZTAPIModelProbeReceipt20261128{
				{Mode: "nonstream", StatusCode: 200, RequestID: "req-nonstream", UsageJSON: ztapiReceiptChatUsage20261128, Complete: true},
				{Mode: "stream", StatusCode: 200, RequestID: "req-stream", UsageJSON: ztapiReceiptChatUsage20261128, Complete: !failed},
			}
			previous := ztapiModelVerificationProbeRunner
			t.Cleanup(func() { ztapiModelVerificationProbeRunner = previous })
			ztapiModelVerificationProbeRunner = func(context.Context, *model.Channel, string) (ztapiModelProbeResult, error) {
				result := ztapiModelProbeResult{StatusCategory: "verified", ProbeReceipts: receipts}
				if failed {
					result.StatusCategory = "upstream_response"
					return result, errors.New("synthetic incomplete stream")
				}
				return result, nil
			}
			verification, err := VerifyZTAPIModel(context.Background(), channel.Id, config.SourceModel, 31)
			if failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, verification)
			require.Positive(t, verification.ID)
			require.Equal(t, receipts, verification.ProbeReceipts)
			var stored model.ZTAPIModelVerification
			require.NoError(t, model.DB.First(&stored, verification.ID).Error)
			require.Empty(t, stored.ProbeReceipts)
			require.False(t, model.DB.Migrator().HasColumn(&model.ZTAPIModelVerification{}, "probe_receipts"))
		})
	}
}

func TestZTAPIProbeReceipt20261128RetainsBillingUsageAuthority(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			source := "claude-sonnet-5"
			// Keep the historically observed generic zero output alongside the
			// native authoritative four; do not normalize either in the receipt.
			usageJSON := `{"input_tokens":16,"prompt_tokens":16,"output_tokens":0,"completion_tokens":4,"total_tokens":20,"billing_usage":` + ztapiReceiptBillingUsage20261128 + `}`
			if responses {
				source = "gpt-5.4-pro"
				usageJSON = `{"input_tokens":16,"output_tokens":4,"total_tokens":20,"billing_usage":` + ztapiReceiptBillingUsage20261128 + `}`
			}
			body := ztapiReceiptBody20261128(responses, stream, usageJSON)
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"req-authoritative"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			var receipts []model.ZTAPIModelProbeReceipt20261128
			usage, _, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid", "synthetic-key", source, stream, &receipts)
			require.NoError(t, err)
			require.Equal(t, ztapiProbeUsage{PromptTokens: 16, CompletionTokens: 4, TotalTokens: 20}, usage)
			require.Len(t, receipts, 1)
			require.True(t, receipts[0].Complete)
			require.JSONEq(t, usageJSON, receipts[0].UsageJSON)
			require.Equal(t, int64(4), gjson.Get(receipts[0].UsageJSON, "billing_usage.claude_usage.output_tokens").Int())
			if !responses {
				require.Equal(t, int64(0), gjson.Get(receipts[0].UsageJSON, "output_tokens").Int())
			}
		}
	}

	withoutTTL := strings.Replace(ztapiReceiptBillingUsage20261128, `,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3},"claude_cache_creation_5_m_tokens":2,"claude_cache_creation_1_h_tokens":3`, "", 1)
	var partial model.ZTAPIModelProbeReceipt20261128
	valid := captureZTAPIProbeReceipt20261128(&partial, []byte(`{"usage":{"billing_usage":`+withoutTTL+`}}`), false, false, "synthetic-key")
	require.True(t, valid)
	require.JSONEq(t, `{"billing_usage":`+withoutTTL+`}`, partial.UsageJSON)
	require.NotContains(t, partial.UsageJSON, "ephemeral_")
	require.NotContains(t, partial.UsageJSON, "claude_cache_creation_")
}

func TestZTAPIProbeReceipt20261128BillingUsageInvalidEvidenceStaysPartial(t *testing.T) {
	base := ztapiReceiptBillingUsage20261128
	for _, tc := range []struct{ name, billing, omitted string }{
		{"unknown_source", strings.Replace(base, `"claude_messages"`, `"openai"`, 1), "billing_usage.source"},
		{"unknown_semantic", strings.Replace(base, `"anthropic"`, `"openai"`, 1), "billing_usage.semantic"},
		{"source_text", strings.Replace(base, `"claude_messages"`, `"PRIVATE_TEXT synthetic-key"`, 1), "billing_usage.source"},
		{"source_number", strings.Replace(base, `"claude_messages"`, `1`, 1), "billing_usage.source"},
		{"semantic_null", strings.Replace(base, `"anthropic"`, `null`, 1), "billing_usage.semantic"},
		{"missing_source", strings.Replace(base, `"source":"claude_messages",`, "", 1), "billing_usage.source"},
		{"missing_semantic", strings.Replace(base, `"semantic":"anthropic",`, "", 1), "billing_usage.semantic"},
		{"billing_string", `"PRIVATE_TEXT synthetic-key"`, "billing_usage"},
		{"billing_array", `[]`, "billing_usage"},
		{"billing_null", `null`, "billing_usage"},
		{"claude_array", `{"source":"claude_messages","semantic":"anthropic","claude_usage":[]}`, "billing_usage.claude_usage"},
		{"claude_null", `{"source":"claude_messages","semantic":"anthropic","claude_usage":null}`, "billing_usage.claude_usage"},
		{"claude_string", `{"source":"claude_messages","semantic":"anthropic","claude_usage":"PRIVATE_TEXT synthetic-key"}`, "billing_usage.claude_usage"},
		{"missing_input", strings.Replace(base, `"input_tokens":16,`, "", 1), "billing_usage.claude_usage.input_tokens"},
		{"output_string", strings.Replace(base, `"output_tokens":4`, `"output_tokens":"PRIVATE_TEXT synthetic-key"`, 1), "billing_usage.claude_usage.output_tokens"},
		{"output_null", strings.Replace(base, `"output_tokens":4`, `"output_tokens":null`, 1), "billing_usage.claude_usage.output_tokens"},
		{"output_negative", strings.Replace(base, `"output_tokens":4`, `"output_tokens":-1`, 1), "billing_usage.claude_usage.output_tokens"},
		{"output_fractional", strings.Replace(base, `"output_tokens":4`, `"output_tokens":4.0`, 1), "billing_usage.claude_usage.output_tokens"},
		{"output_overflow", strings.Replace(base, `"output_tokens":4`, `"output_tokens":9223372036854775808`, 1), "billing_usage.claude_usage.output_tokens"},
		{"output_parser_limit", strings.Replace(base, `"output_tokens":4`, `"output_tokens":1152921504606846976`, 1), "billing_usage.claude_usage.output_tokens"},
		{"input_aggregate_overflow", strings.Replace(base, `"input_tokens":16`, `"input_tokens":1152921504606846975`, 1), ""},
		{"cache_creation_null", strings.Replace(base, `"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}`, `"cache_creation":null`, 1), "billing_usage.claude_usage.cache_creation"},
		{"cache_creation_array", strings.Replace(base, `"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}`, `"cache_creation":[]`, 1), "billing_usage.claude_usage.cache_creation"},
		{"tier_negative", strings.Replace(base, `"ephemeral_5m_input_tokens":2`, `"ephemeral_5m_input_tokens":-1`, 1), "billing_usage.claude_usage.cache_creation.ephemeral_5m_input_tokens"},
		{"tier_overflow", strings.Replace(base, `"ephemeral_1h_input_tokens":3`, `"ephemeral_1h_input_tokens":9223372036854775808`, 1), "billing_usage.claude_usage.cache_creation.ephemeral_1h_input_tokens"},
		{"alias_null", strings.Replace(base, `"claude_cache_creation_5_m_tokens":2`, `"claude_cache_creation_5_m_tokens":null`, 1), "billing_usage.claude_usage.claude_cache_creation_5_m_tokens"},
		{"unknown_billing_text", strings.Replace(base, `"source":`, `"private_text":"PRIVATE_TEXT synthetic-key","source":`, 1), "billing_usage.private_text"},
		{"unknown_claude_text", strings.Replace(base, `"input_tokens":`, `"private_text":"PRIVATE_TEXT synthetic-key","input_tokens":`, 1), "billing_usage.claude_usage.private_text"},
		{"unknown_tier", strings.Replace(base, `"ephemeral_5m_input_tokens":2`, `"ephemeral_2h_input_tokens":7,"ephemeral_5m_input_tokens":2`, 1), "billing_usage.claude_usage.cache_creation.ephemeral_2h_input_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageJSON := strings.TrimSuffix(ztapiReceiptChatUsage20261128, "}") + `,"billing_usage":` + tc.billing + `}`
			body := ztapiReceiptBody20261128(false, false, usageJSON)
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"req-partial-billing"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			var receipts []model.ZTAPIModelProbeReceipt20261128
			usage, _, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid", "synthetic-key", "claude-sonnet-5", false, &receipts)
			require.NoError(t, err, "metadata validation must not change the existing verifier verdict")
			require.Equal(t, ztapiProbeUsage{PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23}, usage)
			require.Len(t, receipts, 1)
			require.False(t, receipts[0].Complete)
			require.Equal(t, int64(23), gjson.Get(receipts[0].UsageJSON, "total_tokens").Int())
			if tc.omitted != "" {
				require.False(t, gjson.Get(receipts[0].UsageJSON, tc.omitted).Exists())
			}
			require.NotContains(t, receipts[0].UsageJSON, "PRIVATE_TEXT")
			require.NotContains(t, receipts[0].UsageJSON, "synthetic-key")
			if tc.name != "billing_string" && tc.name != "billing_array" && tc.name != "billing_null" {
				if tc.omitted != "billing_usage.source" {
					require.Equal(t, "claude_messages", gjson.Get(receipts[0].UsageJSON, "billing_usage.source").String())
				}
				if tc.omitted != "billing_usage.claude_usage" {
					require.Equal(t, int64(3), gjson.Get(receipts[0].UsageJSON, "billing_usage.claude_usage.cache_read_input_tokens").Int())
				}
			}
		})
	}
}

func TestZTAPIProbeReceipt20261128BillingUsageInvalidStreamEvidenceIsSticky(t *testing.T) {
	for _, responses := range []bool{false, true} {
		source := "claude-sonnet-5"
		baseUsage := `{"prompt_tokens":16,"completion_tokens":4,"total_tokens":20,"billing_usage":` + ztapiReceiptBillingUsage20261128 + `}`
		if responses {
			source = "gpt-5.4-pro"
			baseUsage = `{"input_tokens":16,"output_tokens":4,"total_tokens":20,"billing_usage":` + ztapiReceiptBillingUsage20261128 + `}`
		}
		badUsage := strings.Replace(baseUsage, `"source":"claude_messages"`, `"source":"future_source"`, 1)
		prefix := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":null}],\"usage\":" + badUsage + "}\n\n"
		if responses {
			prefix = "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\",\"usage\":" + badUsage + "}}\n\n"
		}
		body := prefix + ztapiReceiptBody20261128(responses, true, baseUsage)
		client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"req-sticky"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		var receipts []model.ZTAPIModelProbeReceipt20261128
		_, _, err := performZTAPIOpenAIProbe(context.Background(), client, "https://fixture.invalid", "synthetic-key", source, true, &receipts)
		require.NoError(t, err)
		require.Len(t, receipts, 1)
		require.False(t, receipts[0].Complete)
		require.JSONEq(t, baseUsage, receipts[0].UsageJSON)
	}
}
