package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func embeddingVerificationWire(t *testing.T) string {
	t.Helper()
	vector := make([]float64, 1536)
	vector[0] = .25
	raw, err := common.Marshal(map[string]any{"object": "list", "model": "text-embedding-ada-002", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": vector}}, "usage": map[string]int{"prompt_tokens": 3, "total_tokens": 3}})
	require.NoError(t, err)
	return string(raw)
}

type embeddingTestTransport func(*http.Request) (*http.Response, error)

func (f embeddingTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestZTAPIEmbeddingVerificationEndpointAndPayload(t *testing.T) {
	base := "https://upstream.example/v1"
	endpoint, err := ztapiVerificationEndpoint(&model.Channel{Type: constant.ChannelTypeOpenAI, BaseURL: &base}, "text-embedding-ada-002")
	require.NoError(t, err)
	require.Equal(t, base+"/embeddings", endpoint)
	calls := 0
	client := &http.Client{Transport: embeddingTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		require.Equal(t, http.MethodPost, r.Method)
		require.NotEmpty(t, body["input"])
		for _, key := range []string{"stream", "stream_options", "messages", "max_tokens", "max_output_tokens"} {
			require.NotContains(t, body, key)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(embeddingVerificationWire(t)))}, nil
	})}
	usage, _, err := performZTAPIOpenAIProbe(context.Background(), client, endpoint, "synthetic", "text-embedding-ada-002", false)
	require.NoError(t, err)
	require.Equal(t, ztapiProbeUsage{PromptTokens: 3, TotalTokens: 3}, usage)
	_, _, err = performZTAPIOpenAIProbe(context.Background(), client, endpoint, "synthetic", "text-embedding-ada-002", true)
	require.Error(t, err)
	require.Equal(t, 1, calls, "streaming must fail before dispatch")
}

func TestZTAPIEmbeddingHealthProbeWithoutText(t *testing.T) {
	result := parseZTAPIHealthProbe([]byte(embeddingVerificationWire(t)), "embeddings", false, ztapiHealthProbeResult{})
	require.True(t, result.Complete)
	require.Equal(t, "functional_pass", result.Code)
	require.EqualValues(t, 3, result.InputTokens)
	require.Zero(t, result.OutputTokens)
	result = parseZTAPIHealthProbe([]byte(embeddingVerificationWire(t)), "embeddings", true, ztapiHealthProbeResult{})
	require.False(t, result.Complete)
}

func TestZTAPIEmbeddingHealthWorkerTargetAndPayload(t *testing.T) {
	w, _, _ := productionWorkerFixture(t)
	db := w.backend.Probes.DB
	source := "text-embedding-ada-002"
	require.NoError(t, db.Model(&model.ZTAPIModelConfig{}).Where("id = 1").Updates(map[string]any{"source_model": source, "input_cost_per_million": .078, "output_cost_per_million": 0}).Error)
	// Seed an embedding authority projection solely for the health-worker read path.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&model.ZTAPIModelPublicationSnapshot{}).Where("model_config_id = 1").Updates(map[string]any{"source_model": source, "input_price_per_million": .13, "output_price_per_million": 0}).Error)
	require.NoError(t, db.Model(&model.ZTAPIModelPriceSource{}).Where("model_config_id = 1").Updates(map[string]any{"source_model": source, "input_per_million": "0.078", "output_per_million": "0", "billing_dimensions": `["input_tokens"]`}).Error)
	target, active, err := productionZTAPIProbeTarget(context.Background(), db, 1, false, false)
	require.NoError(t, err)
	require.True(t, active)
	require.Equal(t, "embeddings", target.Protocol)
	require.Zero(t, target.OutputNanoUSDPerMillion)
	_, active, err = productionZTAPIProbeTarget(context.Background(), db, 1, true, false)
	require.NoError(t, err)
	require.False(t, active)
	w.config.ProbeTransport = embeddingTestTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/v1/embeddings", r.URL.Path)
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		require.NotContains(t, body, "stream")
		require.NotContains(t, body, "messages")
		require.NotEmpty(t, body["input"])
		return workerResponse(200, embeddingVerificationWire(t)), nil
	})
	result := performZTAPIHealthProbe(context.Background(), w.config, model.ZTAPIProbeJob{ZTAPIProbeTarget: target})
	require.True(t, result.Complete)
}

func TestZTAPIEmbeddingVerificationNeverStreams(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	calls := 0
	http.DefaultTransport = embeddingTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(r.Header.Get("Authorization"), "deliberately-invalid") {
			return workerResponse(401, `{"error":{"code":"invalid_api_key"}}`), nil
		}
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		require.NotContains(t, body, "stream")
		return workerResponse(200, embeddingVerificationWire(t)), nil
	})
	base := "https://upstream.example"
	result, err := runZTAPIModelVerificationProbes(context.Background(), &model.Channel{Type: constant.ChannelTypeOpenAI, BaseURL: &base, Key: "synthetic"}, "text-embedding-ada-002")
	require.NoError(t, err)
	require.True(t, result.NonStreamingPassed)
	require.True(t, result.UsageReconciled)
	require.False(t, result.StreamingRequired)
	require.False(t, result.StreamingPassed)
	require.Zero(t, result.CompletionTokens)
	require.Equal(t, 2, calls)
}

func TestZTAPIEmbeddingVerifierRejectsStreamingResponse(t *testing.T) {
	client := &http.Client{Transport: embeddingTestTransport(func(*http.Request) (*http.Response, error) {
		resp := workerResponse(200, embeddingVerificationWire(t))
		resp.Header.Set("Content-Type", "text/event-stream")
		return resp, nil
	})}
	_, _, err := performZTAPIOpenAIProbe(context.Background(), client, "https://upstream.example/v1/embeddings", "synthetic", "text-embedding-ada-002", false)
	require.Error(t, err)
}
