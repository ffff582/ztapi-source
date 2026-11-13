package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPT61SolVerificationTimeoutIsLongAndScoped(t *testing.T) {
	for _, source := range []string{"gpt-6.1-sol", " GPT-6.1-SOL "} {
		require.Equal(t, 120*time.Second, ztapiVerificationRequestTimeoutForModel(source))
	}
	for _, source := range []string{"gpt-5.4", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol-other", "qwen3.7-max"} {
		require.Equal(t, 20*time.Second, ztapiVerificationRequestTimeoutForModel(source))
	}
	require.Equal(t, 60*time.Second, ztapiVerificationRequestTimeoutForModel("gpt-image-2"))
	require.Equal(t, 60*time.Second, ztapiVerificationRequestTimeoutForModel("gpt-5.4-pro"))
}

func TestGPT61SolVerificationHonorsOuterBoundsAndCancellation(t *testing.T) {
	for _, scenario := range []string{"long_budget", "short_parent", "cancelled_parent"} {
		t.Run(scenario, func(t *testing.T) {
			channel, config := setupZTAPIModelVerifierTestDB(t)
			require.NoError(t, model.DB.Model(&config).Update("source_model", "gpt-6.1-sol").Error)
			ctx := context.Background()
			if scenario == "short_parent" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
			}
			if scenario == "cancelled_parent" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			previous := ztapiModelVerificationProbeRunner
			t.Cleanup(func() { ztapiModelVerificationProbeRunner = previous })
			ztapiModelVerificationProbeRunner = func(got context.Context, _ *model.Channel, source string) (ztapiModelProbeResult, error) {
				require.Equal(t, "gpt-6.1-sol", source)
				if scenario == "cancelled_parent" {
					require.ErrorIs(t, got.Err(), context.Canceled)
					return ztapiModelProbeResult{}, got.Err()
				}
				deadline, ok := got.Deadline()
				require.True(t, ok)
				if scenario == "short_parent" {
					require.Greater(t, time.Until(deadline), 25*time.Second)
					require.LessOrEqual(t, time.Until(deadline), 30*time.Second)
				} else {
					require.Greater(t, time.Until(deadline), 235*time.Second)
					require.LessOrEqual(t, time.Until(deadline), 240*time.Second)
				}
				return ztapiModelProbeResult{StatusCategory: "verified", NonStreamingPassed: true, StreamingRequired: true, StreamingPassed: true, UsageReconciled: true, InvalidKeyClassified: true, PromptTokens: 30, CompletionTokens: 5, TotalTokens: 35}, nil
			}
			_, err := VerifyZTAPIModel(ctx, channel.Id, "gpt-6.1-sol", 1)
			if scenario == "cancelled_parent" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGPT61SolHTTPProbeUsesLongTimeoutAndExactPoolMapping(t *testing.T) {
	base := "https://upstream.example.com/hub"
	mapping := `{"gpt-6.1-sol":"gpt-6.1-sol-hc"}`
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI, BaseURL: &base, Key: "synthetic-timeout-test-key", ModelMapping: &mapping}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	http.DefaultTransport = ztapiVerifierRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		deadline, ok := r.Context().Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 110*time.Second)
		require.LessOrEqual(t, time.Until(deadline), 120*time.Second)
		require.Equal(t, "/hub/v1/chat/completions", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "gpt-6.1-sol-hc", gjson.GetBytes(body, "model").String())
		require.Equal(t, int64(1024), gjson.GetBytes(body, "max_tokens").Int())
		if r.Header.Get("Authorization") != "Bearer synthetic-timeout-test-key" {
			return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_api_key"}}`))}, nil
		}
		response := `{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5,"total_tokens":35}}`
		if gjson.GetBytes(body, "stream").Bool() {
			response = "data: {\"choices\":[{\"delta\":{\"content\":\"77\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":5,\"total_tokens\":35}}\n\ndata: [DONE]\n\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	result, err := runZTAPIModelVerificationProbes(context.Background(), channel, "gpt-6.1-sol")
	require.NoError(t, err)
	require.Equal(t, "verified", result.StatusCategory)
	require.True(t, result.NonStreamingPassed && result.StreamingPassed && result.UsageReconciled && result.InvalidKeyClassified)
	require.Equal(t, 3, calls)
}
