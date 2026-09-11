package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func ztapiVerifierPNG(t *testing.T, width, height int) string {
	t.Helper()
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, width, height))))
	return base64.StdEncoding.EncodeToString(encoded.Bytes())
}

func setupZTAPIModelVerifierTestDB(t *testing.T) (model.Channel, model.ZTAPIModelConfig) {
	t.Helper()
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", "verifier-test-master-key-01234567890123456789")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-verifier.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(
		&model.Channel{}, &model.ZTAPIModelConfig{}, &model.ZTAPIModelVerification{},
	))
	baseURL := "https://upstream.example.com"
	channel := model.Channel{
		Name: "managed-test", Type: constant.ChannelTypeOpenAI,
		Key: "synthetic-verifier-key", Status: common.ChannelStatusManuallyDisabled,
		BaseURL: &baseURL, ZTAPIManaged: true,
	}
	require.NoError(t, db.Create(&channel).Error)
	config := model.ZTAPIModelConfig{
		SourceModel: "deepseek-chat-upstream", Protocol: model.ZTAPIProtocolOpenAICompatible,
		ProviderFamily: model.ZTAPIProviderDeepSeek, EnabledGroups: "[]", Version: 1,
	}
	require.NoError(t, db.Create(&config).Error)
	return channel, config
}

func TestVerifyZTAPIModelPersistsOnlyBoundedOperationalEvidence(t *testing.T) {
	channel, config := setupZTAPIModelVerifierTestDB(t)
	previousRunner := ztapiModelVerificationProbeRunner
	ztapiModelVerificationProbeRunner = func(ctx context.Context, gotChannel *model.Channel, sourceModel string) (ztapiModelProbeResult, error) {
		require.Equal(t, channel.Id, gotChannel.Id)
		require.Equal(t, "synthetic-verifier-key", gotChannel.Key)
		require.Equal(t, config.SourceModel, sourceModel)
		return ztapiModelProbeResult{
			NonStreamingPassed: true, StreamingRequired: true, StreamingPassed: true,
			UsageReconciled: true, InvalidKeyClassified: true,
			InsufficientBalanceClassified: true, RateLimitClassified: true,
			TimeoutClassified: true, PromptTokens: 3, CompletionTokens: 1,
			TotalTokens: 4, LatencyMilliseconds: 125,
		}, nil
	}
	t.Cleanup(func() { ztapiModelVerificationProbeRunner = previousRunner })

	verification, err := VerifyZTAPIModel(context.Background(), channel.Id, config.SourceModel, 31)
	require.NoError(t, err)
	require.True(t, verification.NonStreamingPassed)
	require.Equal(t, 4, verification.TotalTokens)
	require.Equal(t, int64(125), verification.LatencyMilliseconds)
	require.WithinDuration(t, time.Now(), time.Unix(verification.VerifiedAt, 0), 3*time.Second)

	var stored model.ZTAPIModelVerification
	require.NoError(t, model.DB.First(&stored, verification.ID).Error)
	require.Equal(t, 31, stored.OperatorID)
}

func TestVerifyZTAPIModelPersistsFailureWithoutRawResponse(t *testing.T) {
	channel, config := setupZTAPIModelVerifierTestDB(t)
	previousRunner := ztapiModelVerificationProbeRunner
	ztapiModelVerificationProbeRunner = func(context.Context, *model.Channel, string) (ztapiModelProbeResult, error) {
		return ztapiModelProbeResult{StatusCategory: "upstream_response"}, errors.New("RAW_SECRET_RESPONSE")
	}
	t.Cleanup(func() { ztapiModelVerificationProbeRunner = previousRunner })

	verification, err := VerifyZTAPIModel(context.Background(), channel.Id, config.SourceModel, 32)
	require.Error(t, err)
	require.NotNil(t, verification)
	require.Equal(t, "upstream_response", verification.StatusCategory)

	var stored model.ZTAPIModelVerification
	require.NoError(t, model.DB.First(&stored, verification.ID).Error)
	require.Equal(t, "upstream_response", stored.StatusCategory)
}

func TestZTAPIModelVerifierClassifiesSafeErrorCategories(t *testing.T) {
	require.Equal(t, "invalid_key", classifyZTAPIVerificationFailure(401, nil))
	require.Equal(t, "insufficient_balance", classifyZTAPIVerificationFailure(402, nil))
	require.Equal(t, "rate_limit", classifyZTAPIVerificationFailure(429, nil))
	require.Equal(t, "timeout", classifyZTAPIVerificationFailure(0, context.DeadlineExceeded))
	require.Equal(t, "upstream_response", classifyZTAPIVerificationFailure(502, errors.New("RAW")))
}

func TestZTAPIVerificationMaxTokensAllowsGPT5ReasoningBudget(t *testing.T) {
	for _, sourceModel := range []string{
		"gpt-5-nano",
		"gpt-5.4-nano",
		"gpt-5.6-sol",
	} {
		require.Equal(t, 1024, ztapiVerificationMaxTokens(sourceModel), sourceModel)
	}
	require.Equal(t, 1024, ztapiVerificationMaxTokens("gpt-4.1"))
	require.Equal(t, 1024, ztapiVerificationMaxTokens("qwen3.7-max"))
}

func TestZTAPIVerificationUsesResponsesPayloadAndUsageForGPT5Pro(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requestBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(ztapiVerifierResponsesSuccess))
	}))
	t.Cleanup(server.Close)

	usage, status, err := performZTAPIOpenAIProbe(
		context.Background(), server.Client(), server.URL, "synthetic-key", "gpt-5.4-pro", false,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, ztapiProbeUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, usage)
	body := <-requestBody
	require.Equal(t, "What is 35+42? Reply with only the integer answer.", body["input"])
	require.Equal(t, "You are a helpful assistant.", body["instructions"])
	require.Equal(t, float64(1024), body["max_output_tokens"])
	require.NotContains(t, body, "messages")
	require.NotContains(t, body, "max_tokens")
	require.NotContains(t, body, "max_completion_tokens")
	require.NotContains(t, body, "stream_options")
}

func TestZTAPIVerificationParsesStreamingResponsesUsageForGPT5Pro(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requestBody <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ztapiVerifierResponsesStream(ztapiVerifierResponsesSuccess)))
	}))
	t.Cleanup(server.Close)

	usage, status, err := performZTAPIOpenAIProbe(
		context.Background(), server.Client(), server.URL, "synthetic-key", "gpt-5.4-pro", true,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, ztapiProbeUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, usage)
	body := <-requestBody
	require.Equal(t, true, body["stream"])
	require.NotContains(t, body, "stream_options")
}

const ztapiVerifierChatSuccess = `{"choices":[{"index":0,"message":{"role":"assistant","content":"77"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
const ztapiVerifierResponsesSuccess = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"77"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
const ztapiVerifierChatStreamSuccess = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"7\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"7\"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" + "data: [DONE]\n\n"

func ztapiVerifierResponsesStream(response string) string {
	return "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"77\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
}

func TestZTAPIVerificationChatStreamZeroUsagePlaceholders(t *testing.T) {
	const zero = `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`
	const positive = `{"prompt_tokens":14,"completion_tokens":13,"total_tokens":27}`
	frame := func(content, finish, usage string) string {
		return "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + content + "\"},\"finish_reason\":" + finish + "}],\"usage\":" + usage + "}\n\n"
	}
	const done = "data: [DONE]\n\n"
	initial := frame("7", "null", zero)
	final := frame("7", `"stop"`, positive)
	for _, tt := range []struct {
		name, body   string
		stream, pass bool
	}{
		{"zero intermediate then positive stop", initial + final + done, true, true},
		{"multiple zero intermediates", frame("", "null", zero) + initial + final + done, true, true},
		{"positive usage after stop", initial + frame("7", `"stop"`, "null") + "data: {\"choices\":[],\"usage\":" + positive + "}\n\n" + done, true, true},
		{"nonstream zero", strings.Replace(ztapiVerifierChatSuccess, `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}`, zero, 1), false, false},
		{"zero stop", initial + frame("7", `"stop"`, zero) + done, true, false},
		{"zero stop cannot borrow earlier positive", frame("7", "null", positive) + frame("7", `"stop"`, zero) + done, true, false},
		{"missing final usage cannot borrow earlier positive", frame("7", "null", positive) + frame("7", `"stop"`, "null") + done, true, false},
		{"zero usage trailer", initial + final + "data: {\"choices\":[],\"usage\":" + zero + "}\n\n" + done, true, false},
		{"zero content frame after stop", initial + final + frame("", "null", zero) + done, true, false},
		{"zero intermediate without final usage", initial + frame("7", `"stop"`, "null") + done, true, false},
		{"negative intermediate", frame("7", "null", `{"prompt_tokens":-1,"completion_tokens":1,"total_tokens":0}`) + final + done, true, false},
		{"inconsistent intermediate", frame("7", "null", `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":3}`) + final + done, true, false},
		{"partial zero intermediate", frame("7", "null", `{"prompt_tokens":0,"completion_tokens":1,"total_tokens":1}`) + final + done, true, false},
		{"missing placeholder fields", frame("7", "null", `{}`) + final + done, true, false},
		{"null placeholder fields", frame("7", "null", `{"prompt_tokens":null,"completion_tokens":null,"total_tokens":null}`) + final + done, true, false},
		{"string placeholder fields", frame("7", "null", `{"prompt_tokens":"0","completion_tokens":0,"total_tokens":0}`) + final + done, true, false},
		{"negative final", initial + frame("7", `"stop"`, `{"prompt_tokens":-1,"completion_tokens":28,"total_tokens":27}`) + done, true, false},
		{"inconsistent final", initial + frame("7", `"stop"`, `{"prompt_tokens":14,"completion_tokens":13,"total_tokens":28}`) + done, true, false},
		{"wrong answer", initial + frame("6", `"stop"`, positive) + done, true, false},
		{"refusal intermediate", strings.Replace(initial, `"content":"7"`, `"content":"7","refusal":"RAW_SECRET"`, 1) + final + done, true, false},
		{"length terminal", initial + frame("7", `"length"`, positive) + done, true, false},
		{"content filter terminal", initial + frame("7", `"content_filter"`, positive) + done, true, false},
		{"missing DONE", initial + final, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}
			usage, status, err := performZTAPIOpenAIProbe(context.Background(), client, "https://upstream.example.com/v1/chat/completions", "synthetic-key", "gemini-3.1-flash-lite-preview", tt.stream)
			require.Equal(t, http.StatusOK, status)
			if tt.pass {
				require.NoError(t, err)
				require.Equal(t, ztapiProbeUsage{PromptTokens: 14, CompletionTokens: 13, TotalTokens: 27}, usage)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "RAW_SECRET")
			}
		})
	}
}

func TestZTAPIVerificationRequiresFunctionalAnswerAndTerminal(t *testing.T) {
	for _, tt := range []struct {
		name, source, body string
		stream, pass       bool
	}{
		{"chat valid no id", "gpt-5.4", ztapiVerifierChatSuccess, false, true},
		{"chat usage only", "gpt-5.4", `{"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`, false, false},
		{"chat empty", "gpt-5.4", strings.Replace(ztapiVerifierChatSuccess, `"77"`, `""`, 1), false, false},
		{"chat wrong answer", "gpt-5.4", strings.Replace(ztapiVerifierChatSuccess, `"77"`, `"76"`, 1), false, false},
		{"chat length", "gpt-5.4", strings.Replace(ztapiVerifierChatSuccess, `"stop"`, `"length"`, 1), false, false},
		{"chat content filter", "gpt-5.4", strings.Replace(ztapiVerifierChatSuccess, `"stop"`, `"content_filter"`, 1), false, false},
		{"chat refusal with answer", "gpt-5.4", strings.Replace(ztapiVerifierChatSuccess, `"content":"77"`, `"content":"77","refusal":"RAW_SECRET_REFUSAL"`, 1), false, false},
		{"chat inconsistent usage", "gpt-5.4", strings.Replace(ztapiVerifierChatSuccess, `"total_tokens":5`, `"total_tokens":6`, 1), false, false},
		{"chat missing usage", "gpt-5.4", `{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}]}`, false, false},
		{"chat stream valid no id", "gpt-5.4", ztapiVerifierChatStreamSuccess, true, true},
		{"chat stream no DONE", "gpt-5.4", strings.Replace(ztapiVerifierChatStreamSuccess, "data: [DONE]\n\n", "", 1), true, false},
		{"chat stream no stop", "gpt-5.4", strings.Replace(ztapiVerifierChatStreamSuccess, `"stop"`, `null`, 1), true, false},
		{"chat stream length", "gpt-5.4", strings.Replace(ztapiVerifierChatStreamSuccess, `"stop"`, `"length"`, 1), true, false},
		{"chat stream refusal", "gpt-5.4", strings.Replace(ztapiVerifierChatStreamSuccess, `"content":"7"`, `"content":"7","refusal":"RAW_SECRET_REFUSAL"`, 1), true, false},
		{"chat stream malformed then success", "gpt-5.4", "data: {broken}\n\n" + ztapiVerifierChatStreamSuccess, true, false},
		{"chat stream truncated final", "gpt-5.4", strings.TrimSuffix(ztapiVerifierChatStreamSuccess, "\n\n"), true, false},
		{"chat stream invalid usage", "gpt-5.4", strings.Replace(ztapiVerifierChatStreamSuccess, `"total_tokens":5`, `"total_tokens":6`, 1), true, false},
		{"responses valid no id", "gpt-5.4-pro", ztapiVerifierResponsesSuccess, false, true},
		{"responses usage only", "gpt-5.4-pro", `{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`, false, false},
		{"responses wrong answer", "gpt-5.4-pro", strings.Replace(ztapiVerifierResponsesSuccess, `"77"`, `"76"`, 1), false, false},
		{"responses incomplete", "gpt-5.4-pro", strings.Replace(ztapiVerifierResponsesSuccess, `"completed"`, `"incomplete"`, 1), false, false},
		{"responses failed", "gpt-5.4-pro", strings.Replace(ztapiVerifierResponsesSuccess, `"completed"`, `"failed"`, 1), false, false},
		{"responses refusal with answer", "gpt-5.4-pro", strings.Replace(ztapiVerifierResponsesSuccess, `"content":[`, `"content":[{"type":"refusal","refusal":"RAW_SECRET_REFUSAL"},`, 1), false, false},
		{"responses incomplete details", "gpt-5.4-pro", strings.Replace(ztapiVerifierResponsesSuccess, `"status":`, `"incomplete_details":{"reason":"max_output_tokens"},"status":`, 1), false, false},
		{"responses invalid usage", "gpt-5.4-pro", strings.Replace(ztapiVerifierResponsesSuccess, `"total_tokens":5`, `"total_tokens":6`, 1), false, false},
		{"responses stream valid no id", "gpt-5.4-pro", ztapiVerifierResponsesStream(ztapiVerifierResponsesSuccess), true, true},
		{"responses initial zero usage is not final usage", "gpt-5.4-pro", "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n" + ztapiVerifierResponsesStream(ztapiVerifierResponsesSuccess), true, true},
		{"responses final missing usage cannot borrow earlier usage", "gpt-5.4-pro", "data: {\"type\":\"response.created\",\"response\":" + ztapiVerifierResponsesSuccess + "}\n\n" + ztapiVerifierResponsesStream(strings.Replace(ztapiVerifierResponsesSuccess, `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}`, "", 1)), true, false},
		{"responses typeless frame cannot provide usage", "gpt-5.4-pro", "data: {\"response\":" + ztapiVerifierResponsesSuccess + "}\n\n" + ztapiVerifierResponsesStream(strings.Replace(ztapiVerifierResponsesSuccess, `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}`, "", 1)), true, false},
		{"responses stream no completed", "gpt-5.4-pro", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"77\",\"response\":" + ztapiVerifierResponsesSuccess + "}\n\n", true, false},
		{"responses stream incomplete", "gpt-5.4-pro", ztapiVerifierResponsesStream(strings.Replace(ztapiVerifierResponsesSuccess, `"completed"`, `"incomplete"`, 1)), true, false},
		{"responses stream refusal delta", "gpt-5.4-pro", "data: {\"type\":\"response.refusal.delta\",\"delta\":\"RAW_SECRET_REFUSAL\"}\n\n" + ztapiVerifierResponsesStream(ztapiVerifierResponsesSuccess), true, false},
		{"responses stream wrong delta hidden by final", "gpt-5.4-pro", strings.Replace(ztapiVerifierResponsesStream(ztapiVerifierResponsesSuccess), `"delta":"77"`, `"delta":"76"`, 1), true, false},
		{"responses stream trailing error", "gpt-5.4-pro", ztapiVerifierResponsesStream(ztapiVerifierResponsesSuccess) + "data: {\"type\":\"error\"}\n\n", true, false},
		{"responses stream invalid usage", "gpt-5.4-pro", ztapiVerifierResponsesStream(strings.Replace(ztapiVerifierResponsesSuccess, `"total_tokens":5`, `"total_tokens":6`, 1)), true, false},
		{"nonstream trailing garbage", "gpt-5.4-pro", ztapiVerifierResponsesSuccess + " garbage", false, false},
		{"nonstream oversized", "gpt-5.4-pro", ztapiVerifierResponsesSuccess + strings.Repeat(" ", ztapiVerificationBodyLimit), false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := common.DecodeJson(r.Body, &request); err != nil {
					t.Errorf("invalid probe payload: %v", err)
				}
				if tt.source == "gpt-5.4" {
					assert.Equal(t, float64(1024), request["max_tokens"])
					assert.Equal(t, []any{map[string]any{"role": "system", "content": "You are a helpful assistant."}, map[string]any{"role": "user", "content": "What is 35+42? Reply with only the integer answer."}}, request["messages"])
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			usage, status, err := performZTAPIOpenAIProbe(context.Background(), server.Client(), server.URL, "synthetic-key", tt.source, tt.stream)
			require.Equal(t, 200, status)
			if tt.pass {
				require.NoError(t, err)
				require.Equal(t, ztapiProbeUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, usage)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "RAW_SECRET")
			}
		})
	}
}

type ztapiVerifierRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ztapiVerifierRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVerifyZTAPIModelActualProbeSequenceAndPersistedVerdict(t *testing.T) {
	for _, tt := range []struct {
		name             string
		responses        bool
		failureAt, calls int
	}{
		{"chat success", false, 0, 3},
		{"chat zero interim usage", false, 0, 3},
		{"responses success", true, 0, 3},
		{"chat wrong nonstream", false, 1, 1},
		{"responses wrong nonstream", true, 1, 1},
		{"chat wrong stream", false, 2, 2},
		{"responses wrong stream", true, 2, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			channel, config := setupZTAPIModelVerifierTestDB(t)
			source, path := "gpt-5.4", "/v1/chat/completions"
			if tt.responses {
				source, path = "gpt-5.4-pro", "/v1/responses"
			}
			require.NoError(t, model.DB.Model(&config).Update("source_model", source).Error)
			oldTransport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = oldTransport })
			calls := 0
			http.DefaultTransport = ztapiVerifierRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, "upstream.example.com", r.URL.Host)
				assert.Equal(t, path, r.URL.Path)
				var payload map[string]any
				assert.NoError(t, common.DecodeJson(r.Body, &payload))
				assert.Equal(t, source, payload["model"])
				assert.Equal(t, calls == 2, payload["stream"])
				status, body := 200, ztapiVerifierChatSuccess
				if tt.responses {
					body = ztapiVerifierResponsesSuccess
				}
				if calls == 2 {
					body = ztapiVerifierChatStreamSuccess
					if tt.name == "chat zero interim usage" {
						body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n" + body
					}
					if tt.responses {
						body = ztapiVerifierResponsesStream(ztapiVerifierResponsesSuccess)
					}
				}
				if calls == tt.failureAt {
					body = strings.ReplaceAll(body, `"77"`, `"76"`)
					body = strings.ReplaceAll(body, `"content":"7"`, `"content":"6"`)
				}
				if calls == 3 {
					assert.Equal(t, "Bearer ztapi-deliberately-invalid-credential", r.Header.Get("Authorization"))
					status, body = 401, `{"error":{"message":"RAW_SECRET_BODY"}}`
				} else {
					assert.Equal(t, "Bearer synthetic-verifier-key", r.Header.Get("Authorization"))
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			verdict, err := VerifyZTAPIModel(context.Background(), channel.Id, source, 34)
			require.NotNil(t, verdict)
			require.Equal(t, tt.calls, calls, "bounded two normal probes and one invalid-key probe; fail early")
			var stored model.ZTAPIModelVerification
			require.NoError(t, model.DB.First(&stored, verdict.ID).Error)
			if tt.failureAt == 0 {
				require.NoError(t, err)
				require.Equal(t, "verified", stored.StatusCategory)
				require.True(t, stored.NonStreamingPassed && stored.StreamingPassed && stored.UsageReconciled && stored.InvalidKeyClassified)
			} else {
				require.Error(t, err)
				require.Equal(t, tt.failureAt == 2, stored.NonStreamingPassed)
				require.False(t, stored.StreamingPassed)
				require.NotEqual(t, "verified", stored.StatusCategory)
			}
		})
	}
}

func TestZTAPIVerificationEndpointUsesResponsesOnlyForGPT5Pro(t *testing.T) {
	baseURL := "https://upstream.example.com/hub"
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI, BaseURL: &baseURL}

	endpoint, err := ztapiVerificationEndpoint(channel, "gpt-5.4-pro")
	require.NoError(t, err)
	require.Equal(t, "https://upstream.example.com/hub/v1/responses", endpoint)

	endpoint, err = ztapiVerificationEndpoint(channel, "gpt-5.4-pro-2026-03-05")
	require.NoError(t, err)
	require.Equal(t, "https://upstream.example.com/hub/v1/responses", endpoint)

	endpoint, err = ztapiVerificationEndpoint(channel, "gpt-5.4")
	require.NoError(t, err)
	require.Equal(t, "https://upstream.example.com/hub/v1/chat/completions", endpoint)
}

func TestZTAPIVerificationRequestTimeoutAllowsSlowGPT5ProModels(t *testing.T) {
	require.Equal(t, 60*time.Second, ztapiVerificationRequestTimeoutForModel("gpt-5.4-pro"))
	require.Equal(t, 60*time.Second, ztapiVerificationRequestTimeoutForModel(" GPT-5.6-PRO "))
	require.Equal(t, 20*time.Second, ztapiVerificationRequestTimeoutForModel("gpt-5.4"))
	require.Equal(t, 20*time.Second, ztapiVerificationRequestTimeoutForModel("qwen3.7-max"))
}

func TestVerifyZTAPIModelExtendsOuterDeadlineForSlowModels(t *testing.T) {
	channel, config := setupZTAPIModelVerifierTestDB(t)
	require.NoError(t, model.DB.Model(&config).Update("source_model", "gpt-5.4-pro").Error)

	previousRunner := ztapiModelVerificationProbeRunner
	ztapiModelVerificationProbeRunner = func(ctx context.Context, _ *model.Channel, sourceModel string) (ztapiModelProbeResult, error) {
		require.Equal(t, "gpt-5.4-pro", sourceModel)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 110*time.Second)
		require.LessOrEqual(t, time.Until(deadline), 120*time.Second)
		return ztapiModelProbeResult{
			NonStreamingPassed: true, StreamingRequired: true, StreamingPassed: true,
			UsageReconciled: true, InvalidKeyClassified: true,
			InsufficientBalanceClassified: true, RateLimitClassified: true,
			TimeoutClassified: true, PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4,
		}, nil
	}
	t.Cleanup(func() { ztapiModelVerificationProbeRunner = previousRunner })

	_, err := VerifyZTAPIModel(context.Background(), channel.Id, "gpt-5.4-pro", 33)
	require.NoError(t, err)
}

func TestVerifyZTAPIModelExtendsOuterDeadlineForGeminiImageGeneration(t *testing.T) {
	channel, config := setupZTAPIModelVerifierTestDB(t)
	require.NoError(t, model.DB.Model(&config).Update("source_model", "gemini-2.5-flash-image").Error)
	_, protocolContract, err := ztapiGemini25ImageProtocolContract()
	require.NoError(t, err)

	previousRunner := ztapiModelVerificationProbeRunner
	ztapiModelVerificationProbeRunner = func(ctx context.Context, _ *model.Channel, sourceModel string) (ztapiModelProbeResult, error) {
		require.Equal(t, "gemini-2.5-flash-image", sourceModel)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 110*time.Second)
		require.LessOrEqual(t, time.Until(deadline), 120*time.Second)
		return ztapiModelProbeResult{
			NonStreamingPassed: true, UsageReconciled: true, InvalidKeyClassified: true,
			InsufficientBalanceClassified: true, RateLimitClassified: true,
			TimeoutClassified: true, PromptTokens: 6, CompletionTokens: 1295, TotalTokens: 1301,
			MediaResultValid: true, ImageProtocolContractJSON: protocolContract,
		}, nil
	}
	t.Cleanup(func() { ztapiModelVerificationProbeRunner = previousRunner })

	_, err = VerifyZTAPIModel(context.Background(), channel.Id, "gemini-2.5-flash-image", 34)
	require.NoError(t, err)
}

func TestVerifyZTAPIGPTImage2UsesRealGenerationAndPersistsExactProtocol(t *testing.T) {
	channel, config := setupZTAPIModelVerifierTestDB(t)
	require.NoError(t, model.DB.Model(&config).Update("source_model", "gpt-image-2").Error)

	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	calls := 0
	http.DefaultTransport = ztapiVerifierRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "/v1/images/generations", r.URL.Path)
		if calls == 2 {
			require.Equal(t, "Bearer ztapi-deliberately-invalid-credential", r.Header.Get("Authorization"))
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"RAW_SECRET_BODY"}}`))}, nil
		}
		require.Equal(t, "Bearer synthetic-verifier-key", r.Header.Get("Authorization"))
		var payload map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		require.Equal(t, map[string]any{
			"model": "gpt-image-2", "prompt": "A simple blue circle centered on a white background.",
			"n": float64(1), "size": "1024x1024", "quality": "low",
		}, payload)
		body, err := common.Marshal(map[string]any{
			"data": []map[string]string{{"b64_json": ztapiVerifierPNG(t, 1024, 1024)}},
			"usage": map[string]any{
				"input_tokens": 18, "input_tokens_details": map[string]any{"text_tokens": 18, "image_tokens": 0},
				"output_tokens": 196, "output_tokens_details": map[string]any{"image_tokens": 196, "text_tokens": 0},
				"total_tokens": 214,
			},
		})
		require.NoError(t, err)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"provider-image-request-1"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	})

	verification, err := VerifyZTAPIModel(context.Background(), channel.Id, "gpt-image-2", 35)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, model.ZTAPIModalityImage, verification.Modality)
	require.True(t, verification.NonStreamingPassed)
	require.False(t, verification.StreamingRequired)
	require.False(t, verification.StreamingPassed)
	require.True(t, verification.UsageReconciled)
	require.True(t, verification.MediaResultValid)
	require.True(t, verification.InvalidKeyClassified)
	require.Equal(t, 18, verification.PromptTokens)
	require.Equal(t, 196, verification.CompletionTokens)
	require.Equal(t, 214, verification.TotalTokens)

	contract, canonical, err := types.ParseZTAPIImageProtocolContract(verification.ImageProtocolContractJSON)
	require.NoError(t, err)
	require.Equal(t, verification.ImageProtocolContractJSON, canonical)
	require.True(t, types.IsZTAPIGPTImage2NotReportedUsageProtocol(contract))
	require.Equal(t, types.ZTAPIResponseIDSourceHeader, contract.RequestIDSource)
	require.Equal(t, "X-Request-ID", contract.RequestIDKey)
	require.Equal(t, map[string]string{"text_input": "200000", "image_input": "0", "image_output": "196"}, contract.Reservations[0].MaximumDimensions)
	require.Equal(t, types.ZTAPIImageRequestFieldOmit, contract.UpstreamRequestFields["response_format"])

	var stored model.ZTAPIModelVerification
	require.NoError(t, model.DB.First(&stored, verification.ID).Error)
	require.Equal(t, verification.ImageProtocolContractJSON, stored.ImageProtocolContractJSON)
}

func TestVerifyZTAPIGemini25ImageUsesNativeGenerationAndPersistsExactProtocol(t *testing.T) {
	channel, config := setupZTAPIModelVerifierTestDB(t)
	baseURL := "https://upstream.example.com/hub"
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": constant.ChannelTypeGemini, "base_url": baseURL,
	}).Error)
	channel.Type, channel.BaseURL = constant.ChannelTypeGemini, &baseURL
	require.NoError(t, model.DB.Model(&config).Update("source_model", "gemini-2.5-flash-image").Error)

	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	calls := 0
	http.DefaultTransport = ztapiVerifierRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "/hub/v1beta/models/gemini-2.5-flash-image:generateContent", r.URL.Path)
		if calls == 2 {
			require.Equal(t, "Bearer ztapi-deliberately-invalid-credential", r.Header.Get("Authorization"))
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"RAW_SECRET_BODY"}}`))}, nil
		}
		require.Equal(t, "Bearer synthetic-verifier-key", r.Header.Get("Authorization"))
		var payload map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		require.Equal(t, map[string]any{
			"contents": []any{map[string]any{
				"role": "user", "parts": []any{map[string]any{"text": "Generate a neutral blue circle."}},
			}},
			"generationConfig": map[string]any{"responseModalities": []any{"TEXT", "IMAGE"}},
		}, payload)
		body, err := common.Marshal(map[string]any{
			"responseId": "provider-gemini-image-request-1",
			"candidates": []any{map[string]any{
				"finishReason": "STOP",
				"content": map[string]any{"parts": []any{
					map[string]any{"text": "Here is the requested image."},
					map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": ztapiVerifierPNG(t, 1024, 1024)}},
				}},
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount": 18, "candidatesTokenCount": 196, "totalTokenCount": 214,
				"promptTokensDetails": []any{map[string]any{"modality": "TEXT", "tokenCount": 18}},
				"candidatesTokensDetails": []any{
					map[string]any{"modality": "IMAGE", "tokenCount": 191},
					map[string]any{"modality": "TEXT", "tokenCount": 5},
				},
			},
		})
		require.NoError(t, err)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	})

	verification, err := VerifyZTAPIModel(context.Background(), channel.Id, "gemini-2.5-flash-image", 36)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, model.ZTAPIModalityImage, verification.Modality)
	require.True(t, verification.NonStreamingPassed)
	require.False(t, verification.StreamingRequired)
	require.True(t, verification.UsageReconciled)
	require.True(t, verification.MediaResultValid)
	require.True(t, verification.InvalidKeyClassified)
	require.Equal(t, 18, verification.PromptTokens)
	require.Equal(t, 196, verification.CompletionTokens)
	require.Equal(t, 214, verification.TotalTokens)

	contract, canonical, err := types.ParseZTAPIImageProtocolContract(verification.ImageProtocolContractJSON)
	require.NoError(t, err)
	require.Equal(t, verification.ImageProtocolContractJSON, canonical)
	require.Equal(t, types.ZTAPIImageWireProtocolGeminiGenerateContent, contract.WireProtocol)
	require.Equal(t, "/v1beta/models/gemini-2.5-flash-image:generateContent", contract.ProviderPath)
	require.Equal(t, types.ZTAPIResponseIDSourceBodyField, contract.RequestIDSource)
	require.Equal(t, "responseId", contract.RequestIDKey)
	require.Equal(t, map[string]string{"input_tokens": "300000", "output_tokens": "2000"}, contract.Reservations[0].MaximumDimensions)

	var stored model.ZTAPIModelVerification
	require.NoError(t, model.DB.First(&stored, verification.ID).Error)
	require.Equal(t, verification.ImageProtocolContractJSON, stored.ImageProtocolContractJSON)
}

func TestGeminiImageVerifierRejectsUsageThatRuntimeCannotSettle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "missing modality details",
			mutate: func(usage map[string]any) {
				delete(usage, "promptTokensDetails")
			},
		},
		{
			name: "unsupported cached input",
			mutate: func(usage map[string]any) {
				usage["cachedContentTokenCount"] = 1
			},
		},
		{
			name: "unsupported output modality",
			mutate: func(usage map[string]any) {
				usage["candidatesTokensDetails"] = []any{map[string]any{"modality": "AUDIO", "tokenCount": 196}}
			},
		},
		{
			name: "output modality total mismatch",
			mutate: func(usage map[string]any) {
				usage["candidatesTokensDetails"] = []any{
					map[string]any{"modality": "IMAGE", "tokenCount": 190},
					map[string]any{"modality": "TEXT", "tokenCount": 5},
				}
			},
		},
		{
			name: "missing image output usage",
			mutate: func(usage map[string]any) {
				usage["candidatesTokensDetails"] = []any{map[string]any{"modality": "TEXT", "tokenCount": 196}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := map[string]any{
				"promptTokenCount": 18, "candidatesTokenCount": 196, "totalTokenCount": 214,
				"promptTokensDetails":     []any{map[string]any{"modality": "TEXT", "tokenCount": 18}},
				"candidatesTokensDetails": []any{map[string]any{"modality": "IMAGE", "tokenCount": 196}},
			}
			tc.mutate(usage)
			body, err := common.Marshal(map[string]any{
				"responseId": "provider-gemini-image-request-usage",
				"candidates": []any{map[string]any{
					"finishReason": "STOP",
					"content": map[string]any{"parts": []any{map[string]any{
						"inlineData": map[string]any{"mimeType": "image/png", "data": ztapiVerifierPNG(t, 1024, 1024)},
					}}},
				}},
				"usageMetadata": usage,
			})
			require.NoError(t, err)
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
			})}

			_, _, _, err = performZTAPIGeminiImageProbe(context.Background(), client, "https://example.com/generate", "test-key")
			require.Error(t, err)
			require.Contains(t, err.Error(), "usage")
		})
	}
}

func TestGeminiImageVerifierRejectsRuntimeIncompatibleResponseShapes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "non-string response id",
			mutate: func(response map[string]any) {
				response["responseId"] = 12345
			},
		},
		{
			name: "empty text part",
			mutate: func(response map[string]any) {
				response["candidates"] = []any{map[string]any{
					"finishReason": "STOP",
					"content": map[string]any{"parts": []any{
						map[string]any{"text": ""},
						map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": ztapiVerifierPNG(t, 1024, 1024)}},
					}},
				}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := map[string]any{
				"responseId": "provider-gemini-image-request-shape",
				"candidates": []any{map[string]any{
					"finishReason": "STOP",
					"content": map[string]any{"parts": []any{
						map[string]any{"text": "Image generated."},
						map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": ztapiVerifierPNG(t, 1024, 1024)}},
					}},
				}},
				"usageMetadata": map[string]any{
					"promptTokenCount": 18, "candidatesTokenCount": 196, "totalTokenCount": 214,
					"promptTokensDetails":     []any{map[string]any{"modality": "TEXT", "tokenCount": 18}},
					"candidatesTokensDetails": []any{map[string]any{"modality": "IMAGE", "tokenCount": 196}},
				},
			}
			tc.mutate(response)
			body, err := common.Marshal(response)
			require.NoError(t, err)
			client := &http.Client{Transport: ztapiVerifierRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
			})}

			_, _, _, err = performZTAPIGeminiImageProbe(context.Background(), client, "https://example.com/generate", "test-key")
			require.Error(t, err)
			require.Contains(t, err.Error(), "response")
		})
	}
}
