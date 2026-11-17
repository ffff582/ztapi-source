package router

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

// Explicit opt-in only. This test accepts only the dedicated loopback Redis DB 13.
func TestZTAPIRealRedisAuthAdmissionIsolation(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("ZTAPI_REDIS_TEST_URL"))
	required := strings.EqualFold(
		strings.TrimSpace(os.Getenv("ZTAPI_REQUIRE_REDIS_INTEGRATION")),
		"true",
	)
	if rawURL == "" {
		if required {
			t.Fatal("ZTAPI_REDIS_TEST_URL is required when ZTAPI_REQUIRE_REDIS_INTEGRATION=true")
		}
		t.Skip("dedicated loopback Redis integration service not configured")
	}

	options, err := redis.ParseURL(rawURL)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:6379", options.Addr)
	require.NotEmpty(t, options.Password, "the integration Redis service must require a password")
	require.Equal(t, 13, options.DB, "the integration suite must use isolated Redis DB 13")
	options.DialTimeout = 2 * time.Second
	options.ReadTimeout = 2 * time.Second
	options.WriteTimeout = 2 * time.Second

	ctx := context.Background()
	client := redis.NewClient(options)
	require.NoError(t, client.Ping(ctx).Err())
	unauthenticated := redis.NewClient(&redis.Options{
		Addr:        options.Addr,
		DB:          options.DB,
		DialTimeout: 2 * time.Second,
	})
	require.ErrorContains(t, unauthenticated.Ping(ctx).Err(), "NOAUTH")
	require.NoError(t, unauthenticated.Close())
	require.NoError(t, client.FlushDB(ctx).Err())
	t.Cleanup(func() {
		require.NoError(t, client.FlushDB(ctx).Err())
		require.NoError(t, client.Close())
	})

	_, remote := newRefreshRateLimitTestRouter(t)
	previousClient, previousEnabled := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() {
		common.RDB, common.RedisEnabled = previousClient, previousEnabled
	})
	engine := gin.New()
	SetApiRouter(engine)

	for i := 0; i < 21; i++ {
		want := http.StatusUnauthorized
		if i == 20 {
			want = http.StatusTooManyRequests
		}
		response := performZTAPIRouterRequest(
			engine,
			http.MethodPost,
			"/api/auth/refresh",
			`{}`,
			"",
			remote,
		)
		require.Equal(t, want, response.Code, "refresh request %d", i+1)
	}

	for i := 0; i < 20; i++ {
		path, want := "/api/auth/login", http.StatusUnauthorized
		if i%2 == 1 {
			path, want = "/api/auth/register", http.StatusBadRequest
		}
		response := performZTAPIRouterRequest(
			engine,
			http.MethodPost,
			path,
			`{}`,
			"",
			remote,
		)
		require.Equal(t, want, response.Code, "critical request %d", i+1)
	}

	for _, path := range []string{"/api/auth/login", "/api/auth/register"} {
		response := performZTAPIRouterRequest(
			engine,
			http.MethodPost,
			path,
			`{}`,
			"",
			remote,
		)
		require.Equal(t, http.StatusTooManyRequests, response.Code, path)
	}
	logout := performZTAPIRouterRequest(
		engine,
		http.MethodPost,
		"/api/auth/logout",
		"",
		"",
		remote,
	)
	require.Equal(t, http.StatusOK, logout.Code)
}
