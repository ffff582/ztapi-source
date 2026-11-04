package channel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	base "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func admissionTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("ZTAPI_REDIS_TEST_URL")
	if raw == "" {
		if os.Getenv("ZTAPI_REQUIRE_REDIS_INTEGRATION") == "true" {
			t.Fatal("a dedicated Redis integration service is required")
		}
		t.Skip("dedicated Redis integration service not configured")
	}
	opts, err := redis.ParseURL(raw)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(opts.Addr, "127.0.0.1:"))
	require.Equal(t, 13, opts.DB)
	require.NotEmpty(t, opts.Password)
	opts.DialTimeout, opts.ReadTimeout, opts.WriteTimeout = time.Second, time.Second, time.Second
	client := redis.NewClient(opts)
	require.NoError(t, client.Ping(context.Background()).Err())
	oldClient, oldEnabled := base.RDB, base.RedisEnabled
	base.RDB, base.RedisEnabled = client, true
	t.Cleanup(func() {
		base.RDB, base.RedisEnabled = oldClient, oldEnabled
		require.NoError(t, client.Close())
	})
	return client
}

type admissionResult struct {
	response *http.Response
	err      error
}

func admissionRequest(ctx context.Context, credential string) admissionResult {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)).WithContext(ctx)
	info := &relaycommon.RelayInfo{
		OriginModelName:          "zt-gpt-6.1-sol",
		ChannelMeta:              &relaycommon.ChannelMeta{ApiKey: credential, UpstreamModelName: "gpt-6.1-sol"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-6.1-sol", Modality: "text"},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := DoRequest(c, req, info)
	return admissionResult{resp, err}
}

func TestZTAPIRealRedisUpstreamAdmissionHoldsUntilBodyClose(t *testing.T) {
	admissionTestRedis(t)
	gin.SetMode(gin.TestMode)
	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	client := service.GetHttpClient()
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	sent := make(chan struct{}, 4)
	client.Transport = healthRoundTripper(func(req *http.Request) (*http.Response, error) {
		sent <- struct{}{}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("event stream")), Request: req}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	credential := "synthetic-admission-" + uuid.NewString()
	first := admissionRequest(ctx, credential)
	require.NoError(t, first.err)
	t.Cleanup(func() { _ = first.response.Body.Close() })
	<-sent
	secondDone := make(chan admissionResult, 1)
	go func() { secondDone <- admissionRequest(ctx, credential) }()
	select {
	case <-sent:
		t.Fatal("second upstream request was dispatched while the first response body was still open")
	case <-time.After(150 * time.Millisecond):
	}
	require.NoError(t, first.response.Body.Close())
	select {
	case second := <-secondDone:
		require.NoError(t, second.err)
		require.NoError(t, second.response.Body.Close())
	case <-ctx.Done():
		t.Fatal("queued request was not admitted after the first body closed")
	}
}

func fastAdmissionPolicy(t *testing.T) {
	t.Helper()
	old := ztapiSolAdmissionPolicy
	ztapiSolAdmissionPolicy = ztapiAdmissionPolicy{wait: 600 * time.Millisecond, lease: 300 * time.Millisecond,
		heartbeat: 30 * time.Millisecond, poll: 5 * time.Millisecond, waiterTTL: 150 * time.Millisecond, capacity: 2}
	t.Cleanup(func() { ztapiSolAdmissionPolicy = old })
}

func admissionTestInput() (*http.Request, *relaycommon.RelayInfo) {
	req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer synthetic-"+uuid.NewString())
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "not-the-effective-key"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-6.1-sol", Modality: "text"}}
	return req, info
}

func TestZTAPIUpstreamAdmissionIdentityUsesEffectiveCredentialAndOrigin(t *testing.T) {
	req, info := admissionTestInput()
	keys := ztapiAdmissionKeys(req, info)
	require.Len(t, keys, 4)
	require.NotContains(t, strings.Join(keys, ""), req.Header.Get("Authorization"))
	info.ApiKey = "a-different-stored-key"
	require.Equal(t, keys, ztapiAdmissionKeys(req, info))
	req.URL.Path = "/v1/chat/completions"
	require.Equal(t, keys, ztapiAdmissionKeys(req, info), "all protocols share the same upstream resource")
	req.URL.Host = "other.invalid"
	require.NotEqual(t, keys, ztapiAdmissionKeys(req, info))
	req.URL.Host = "upstream.invalid"
	req.Header.Set("Authorization", "Bearer another-effective-key")
	require.NotEqual(t, keys, ztapiAdmissionKeys(req, info))
	info.ZTAPIPublicationSnapshot.SourceModel = "claude-opus-5"
	require.Nil(t, ztapiAdmissionKeys(req, info), "unrelated models are unchanged")
	info.ZTAPIPublicationSnapshot = nil
	require.Nil(t, ztapiAdmissionKeys(req, info), "unmanaged requests are unchanged")
}

func TestZTAPIRealRedisUpstreamAdmissionBoundedFIFOAndCancellation(t *testing.T) {
	client := admissionTestRedis(t)
	fastAdmissionPolicy(t)
	req, info := admissionTestInput()
	ctx := context.Background()
	first, err := acquireZTAPIUpstreamLease(ctx, req, info)
	require.NoError(t, err)
	t.Cleanup(first.release)
	keys := ztapiAdmissionKeys(req, info)
	queuedCtx, cancel := context.WithCancel(ctx)
	queued := make(chan error, 1)
	go func() {
		lease, err := acquireZTAPIUpstreamLease(queuedCtx, req, info)
		if lease != nil {
			lease.release()
		}
		queued <- err
	}()
	require.Eventually(t, func() bool { return client.ZCard(ctx, keys[1]).Val() == 1 }, time.Second, time.Millisecond)
	secondDone := make(chan *ztapiUpstreamLease, 1)
	go func() { lease, _ := acquireZTAPIUpstreamLease(ctx, req, info); secondDone <- lease }()
	require.Eventually(t, func() bool { return client.ZCard(ctx, keys[1]).Val() == 2 }, time.Second, time.Millisecond)
	_, err = acquireZTAPIUpstreamLease(ctx, req, info)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 429, apiErr.StatusCode)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.NotContains(t, apiErr.ToPublicOpenAIError().Message, "upstream rate")
	cancel()
	require.Error(t, <-queued)
	require.Eventually(t, func() bool { return client.ZCard(ctx, keys[1]).Val() == 1 }, time.Second, time.Millisecond)
	first.release()
	select {
	case second := <-secondDone:
		require.NotNil(t, second)
		second.release()
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter blocked the next waiter")
	}
	require.Zero(t, client.ZCard(ctx, keys[1]).Val())
	require.Zero(t, client.Exists(ctx, keys[0]).Val())
}

func TestZTAPIRealRedisUpstreamAdmissionTimeoutAndStaleWaiterRecovery(t *testing.T) {
	client := admissionTestRedis(t)
	fastAdmissionPolicy(t)
	req, info := admissionTestInput()
	ctx := context.Background()
	keys := ztapiAdmissionKeys(req, info)
	require.NoError(t, client.ZAdd(ctx, keys[1], &redis.Z{Score: 0, Member: "crashed-waiter"}).Err())
	require.NoError(t, client.ZAdd(ctx, keys[2], &redis.Z{Score: 1, Member: "crashed-waiter"}).Err())
	first, err := acquireZTAPIUpstreamLease(ctx, req, info)
	require.NoError(t, err)
	t.Cleanup(first.release)
	start := time.Now()
	_, err = acquireZTAPIUpstreamLease(ctx, req, info)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 504, apiErr.StatusCode)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Zero(t, client.ZCard(ctx, keys[1]).Val())
	require.Equal(t, first.owner, client.Get(ctx, keys[0]).Val(), "renewal must protect streams longer than one lease TTL")
}

func TestZTAPIRealRedisUpstreamAdmissionLostLeaseCancelsOnlyOutbound(t *testing.T) {
	client := admissionTestRedis(t)
	fastAdmissionPolicy(t)
	req, info := admissionTestInput()
	incoming, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	lease, err := acquireZTAPIUpstreamLease(incoming, req, info)
	require.NoError(t, err)
	t.Cleanup(lease.release)
	require.NoError(t, client.Set(context.Background(), lease.keys[0], "replacement-owner", time.Second).Err())
	require.Eventually(t, func() bool { return lease.ctx.Err() != nil }, time.Second, time.Millisecond)
	require.NoError(t, incoming.Err(), "lease loss is not client cancellation")
	lease.release()
	require.Equal(t, "replacement-owner", client.Get(context.Background(), lease.keys[0]).Val())
	require.NoError(t, client.Del(context.Background(), lease.keys[0]).Err())
}

func TestZTAPIUpstreamAdmissionUnavailableNeverDispatches(t *testing.T) {
	oldClient, oldEnabled := base.RDB, base.RedisEnabled
	base.RDB, base.RedisEnabled = nil, true
	t.Cleanup(func() { base.RDB, base.RedisEnabled = oldClient, oldEnabled })
	result := admissionRequest(context.Background(), "synthetic-unavailable")
	require.Nil(t, result.response)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, result.err, &apiErr)
	require.Equal(t, 503, apiErr.StatusCode)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.Equal(t, "request_queue_unavailable", apiErr.ToPublicOpenAIError().Code)
	require.Contains(t, apiErr.ToPublicOpenAIError().Message, "not sent upstream")
}

func TestZTAPIRealRedisUpstreamAdmissionTransportFailureReleasesWithoutReplay(t *testing.T) {
	client := admissionTestRedis(t)
	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	httpClient := service.GetHttpClient()
	old := httpClient.Transport
	t.Cleanup(func() { httpClient.Transport = old })
	sends := 0
	httpClient.Transport = healthRoundTripper(func(*http.Request) (*http.Response, error) {
		sends++
		return nil, errors.New("synthetic transport loss after dispatch")
	})
	credential := "synthetic-transport-" + uuid.NewString()
	result := admissionRequest(context.Background(), credential)
	require.Nil(t, result.response)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, result.err, &apiErr)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.Equal(t, 1, sends)
	req, info := admissionTestInput()
	req.Header.Set("Authorization", "Bearer "+credential)
	require.Zero(t, client.Exists(context.Background(), ztapiAdmissionKeys(req, info)[0]).Val())
}
