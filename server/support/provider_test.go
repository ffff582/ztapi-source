package support

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func providerConfig(url string) ProviderConfig {
	return ProviderConfig{Endpoint: url + "/v1/chat/completions", APIKey: "test-only-upstream-secret", Model: "deepseek-v4.1-flash", ChannelID: 8, Supplier: "xcloud", Group: "国产分组", GroupMultiplier: "0.6", CashPerCredit: "0.5", InputCreditPerMillion: "0.14", OutputCreditPerMillion: "0.5", CacheReadCreditPerMillion: "0.003", QuoteEvidence: "xcloud-pricing-fixture"}
}

func TestProviderPreservesCacheScopeAndAppliesBothFactorsOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer test-only-upstream-secret", r.Header.Get("Authorization"))
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, common.Unmarshal(raw, &body))
		require.Equal(t, "disabled", body["thinking"].(map[string]any)["type"])
		require.Equal(t, false, body["stream"])
		require.NotContains(t, body, "tools")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"response-fixture","choices":[{"message":{"content":"已经看到了错误。"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_cache_hit_tokens":20,"prompt_cache_miss_tokens":80}}`))
	}))
	defer srv.Close()
	p, err := NewHTTPProvider(providerConfig(srv.URL), srv.Client())
	require.NoError(t, err)
	r, err := p.Complete(context.Background(), ProviderInput{Messages: []PromptMessage{{Role: "user", Content: "hello"}}})
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "completed", r.Outcome)
	require.Equal(t, int64(100), *r.InputTokens)
	require.Equal(t, int64(20), *r.CacheReadTokens)
	require.Equal(t, "0.000004128", *r.QuoteCostU)
	require.NotNil(t, r.UpstreamCredit)
	require.Equal(t, "0.000008256", *r.UpstreamCredit, "group-adjusted credit is quoted separately from prepaid cash")
}

func TestProviderMissingCacheKeepsAnswerAndExplicitUpperBound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}],"usage":{"prompt_tokens":100,"completion_tokens":5}}`))
	}))
	defer srv.Close()
	p, err := NewHTTPProvider(providerConfig(srv.URL), srv.Client())
	require.NoError(t, err)
	r, err := p.Complete(context.Background(), ProviderInput{})
	require.NoError(t, err)
	require.Equal(t, "completed", r.Outcome)
	require.Equal(t, "answer", r.Content)
	require.Nil(t, r.CacheReadTokens, "missing cache must not become a reported zero")
	require.NotNil(t, r.QuoteCostU)
	require.Equal(t, "0.00000495", *r.QuoteCostU)
	require.Contains(t, r.QuoteEvidence, "quote_basis=all_input_at_max_rate_upper_bound")
}

func TestProviderRejectsConflictingUsageWithoutRetrying(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_cache_hit_tokens":20}}`))
	}))
	defer srv.Close()
	p, err := NewHTTPProvider(providerConfig(srv.URL), srv.Client())
	require.NoError(t, err)
	r, err := p.Complete(context.Background(), ProviderInput{})
	require.Error(t, err)
	require.Equal(t, "usage_conflict", r.Outcome)
	require.Nil(t, r.QuoteCostU)
	require.Equal(t, int32(1), calls.Load())
}

func TestProviderHTTPFailureDoesNotExposeBodiesOrFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "https://unexpected.invalid")
		w.WriteHeader(302)
		_, _ = w.Write([]byte("sk-real-server-secret should not be echoed"))
	}))
	defer srv.Close()
	p, err := NewHTTPProvider(providerConfig(srv.URL), srv.Client())
	require.NoError(t, err)
	r, err := p.Complete(context.Background(), ProviderInput{})
	require.Error(t, err)
	require.NotContains(t, r.Content, "sk-real")
	require.NotContains(t, err.Error(), "secret")
	require.Equal(t, int32(1), calls.Load())
}

func TestProviderMissingUsageIsNotFreeAndZeroOutputIsExplicit(t *testing.T) {
	for _, raw := range []string{`{"choices":[{"message":{"content":"answer"}}]}`, `{"choices":[{"message":{"content":"answer"}}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(raw)) }))
		p, err := NewHTTPProvider(providerConfig(srv.URL), srv.Client())
		require.NoError(t, err)
		result, err := p.Complete(context.Background(), ProviderInput{})
		require.Error(t, err)
		require.Nil(t, result.QuoteCostU)
		srv.Close()
	}
}

func TestProviderRedactsSecretsAndValidatesImages(t *testing.T) {
	require.NotContains(t, Redact("Authorization: Bearer test-secret-token and sk-live-secret123456"), "test-secret")
	require.NotContains(t, Redact("Authorization: Bearer test-secret-token and sk-live-secret123456"), "sk-live")
	require.Error(t, ValidateImage(&ImageInput{MIMEType: "image/svg+xml", DataBase64: "PHN2Zz4="}))
	require.Error(t, ValidateImage(&ImageInput{MIMEType: "image/png", DataBase64: strings.Repeat("A", 5<<20)}))
}
