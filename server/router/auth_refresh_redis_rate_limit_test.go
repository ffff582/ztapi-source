package router

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

// Exercise the real Redis client and limiter branch without a server or new dependency.
func refreshRateLimitRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	var mu sync.Mutex
	lists := map[string][]string{}
	client := redis.NewClient(&redis.Options{MaxRetries: -1, Dialer: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			reader := bufio.NewReader(server)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
				if err != nil || count < 2 {
					t.Errorf("invalid Redis command header: %q", line)
					return
				}
				args := make([]string, count)
				for i := range args {
					line, err = reader.ReadString('\n')
					if err != nil {
						return
					}
					size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "$")))
					if err != nil || size < 0 || size > 1024 {
						t.Errorf("invalid Redis bulk size: %q", line)
						return
					}
					body := make([]byte, size+2)
					if _, err = io.ReadFull(reader, body); err != nil {
						return
					}
					args[i] = string(body[:size])
				}
				mu.Lock()
				key, reply := args[1], ""
				switch strings.ToLower(args[0]) {
				case "llen":
					reply = fmt.Sprintf(":%d\r\n", len(lists[key]))
				case "lpush":
					lists[key] = append([]string{args[2]}, lists[key]...)
					reply = fmt.Sprintf(":%d\r\n", len(lists[key]))
				case "expire":
					reply = ":1\r\n"
				case "lindex":
					if len(lists[key]) == 0 || args[2] != "-1" {
						reply = "$-1\r\n"
					} else {
						value := lists[key][len(lists[key])-1]
						reply = fmt.Sprintf("$%d\r\n%s\r\n", len(value), value)
					}
				default:
					t.Errorf("unexpected Redis command %q", args[0])
					reply = "-ERR unsupported test command\r\n"
				}
				mu.Unlock()
				if _, err = io.WriteString(server, reply); err != nil {
					return
				}
			}
		}()
		return client, nil
	}})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func TestAuthRouterRedisRefreshBucketIsolation(t *testing.T) {
	_, remote := newRefreshRateLimitTestRouter(t)
	previous := common.RDB
	common.RDB = refreshRateLimitRedisClient(t)
	common.RedisEnabled = true
	t.Cleanup(func() { common.RDB = previous })
	engine := gin.New()
	SetApiRouter(engine)
	for i := 0; i < 100; i++ {
		want := http.StatusUnauthorized
		if i >= 20 {
			want = http.StatusTooManyRequests
		}
		response := performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/refresh", `{}`, "", remote)
		require.Equal(t, want, response.Code, "Redis refresh request %d", i+1)
	}
	for i := 0; i < 20; i++ {
		path, want := "/api/auth/login", http.StatusUnauthorized
		if i%2 == 1 {
			path, want = "/api/auth/register", http.StatusBadRequest
		}
		response := performZTAPIRouterRequest(engine, http.MethodPost, path, `{}`, "", remote)
		require.Equal(t, want, response.Code, "Redis critical request %d", i+1)
	}
	for _, path := range []string{"/api/auth/login", "/api/auth/register", "/api/auth/refresh"} {
		response := performZTAPIRouterRequest(engine, http.MethodPost, path, `{}`, "", remote)
		require.Equal(t, http.StatusTooManyRequests, response.Code)
	}
	response := performZTAPIRouterRequest(engine, http.MethodPost, "/api/auth/logout", "", "", remote)
	require.Equal(t, http.StatusOK, response.Code)
}
