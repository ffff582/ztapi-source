package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/hostedsearch"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type hostedControllerProvider struct{}

func (hostedControllerProvider) Search(context.Context, string, int) (hostedsearch.Result, error) {
	return hostedsearch.Result{}, nil
}
func (hostedControllerProvider) Fetch(context.Context, string) (hostedsearch.Result, error) {
	return hostedsearch.Result{}, nil
}

func TestHostedResponsesReauthenticatesChildrenAndPreservesPlainRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var childIDs []string
	var privateNames []string
	authCount, dispatched := 0, 0
	router.Use(CaptureHostedResponsesCredential())
	router.Use(func(c *gin.Context) {
		require.Equal(t, "Bearer test-owned-key", c.GetHeader("Authorization"))
		authCount++
		c.Request.Header.Del("Authorization")
		c.Set("id", 1)
		c.Set("token_id", 2)
		c.Next()
	})
	options := HostedResponsesOptions{Provider: hostedControllerProvider{}, Authorize: func(*gin.Context, string) bool { return true }, Dispatch: func(*gin.Context, string) hostedsearch.ToolDispatch {
		return func(ctx context.Context, _ hostedsearch.ToolRequest, call func(context.Context) (hostedsearch.Result, error)) (hostedsearch.Result, error) {
			dispatched++
			return call(ctx)
		}
	}}
	router.POST("/v1/responses", HostedResponsesBridge(router, options), func(c *gin.Context) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(c.Request.Body).Decode(&body))
		if strings.Contains(string(mustJSON(body["tools"])), "ztapi_hosted_web_") {
			require.True(t, IsHostedSearchChild(c.Request))
			for _, tool := range body["tools"].([]any) {
				name, _ := tool.(map[string]any)["name"].(string)
				if strings.HasPrefix(name, "ztapi_hosted_web_") {
					privateNames = append(privateNames, name)
				}
			}
			childIDs = append(childIDs, c.GetHeader("X-Request-ID"))
			require.Equal(t, "203.0.113.9", c.ClientIP())
			require.Empty(t, c.GetHeader("X-ZTAPI-Health-Lease-Token"))
			c.JSON(200, gin.H{"id": "resp_child", "object": "response", "status": "completed", "output": []any{gin.H{"type": "message", "id": "msg_child", "role": "assistant", "status": "completed", "content": []any{gin.H{"type": "output_text", "text": "OK", "annotations": []any{}}}}}, "usage": gin.H{"input_tokens": 10, "output_tokens": 1, "total_tokens": 11}})
			return
		}
		c.String(200, "unchanged")
	})
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"zt-claude-opus-5","input":"web_search","tools":[{"type":"web_search"}],"stream":false}`))
	request.Header.Set("Authorization", "Bearer test-owned-key")
	request.Header.Set("X-Request-ID", "client-chosen-id")
	request.Header.Set("X-ZTAPI-Health-Lease-Token", "must-not-replay")
	request.RemoteAddr = "203.0.113.9:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Regexp(t, `^hsroot_[a-f0-9]{32}$`, response.Header().Get("X-Request-ID"))
	require.Equal(t, response.Header().Get("X-ZTAPI-Hosted-Root-ID"), response.Header().Get("X-Request-ID"))
	require.Equal(t, 2, authCount)
	require.Len(t, childIDs, 1)
	require.NotEqual(t, "client-chosen-id", childIDs[0])
	require.LessOrEqual(t, len(childIDs[0]), 64)
	require.Zero(t, dispatched)
	require.Contains(t, response.Body.String(), "OK")
	repeated := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"zt-claude-opus-5","input":"web_search","tools":[{"type":"web_search"}],"stream":false}`))
	repeated.Header.Set("Authorization", "Bearer test-owned-key")
	repeated.RemoteAddr = "203.0.113.9:1234"
	repeatedResponse := httptest.NewRecorder()
	router.ServeHTTP(repeatedResponse, repeated)
	require.Equal(t, 200, repeatedResponse.Code)
	require.Len(t, privateNames, 2)
	require.Equal(t, privateNames[0], privateNames[1], "random per-turn tool names would invalidate upstream prompt caches")
	plain := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"zt-claude-opus-5","input":"Hello"}`))
	plain.Header.Set("Authorization", "Bearer test-owned-key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, plain)
	require.Equal(t, "unchanged", response.Body.String())
	require.Equal(t, 5, authCount)
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestHostedResponsesRejectsUnauthorizedOrUnavailableBeforeModelIO(t *testing.T) {
	for _, ready := range []bool{false, true} {
		router := gin.New()
		router.Use(CaptureHostedResponsesCredential())
		options := HostedResponsesOptions{Authorize: func(*gin.Context, string) bool { return false }}
		if ready {
			options.Provider = hostedControllerProvider{}
			options.Dispatch = func(*gin.Context, string) hostedsearch.ToolDispatch {
				return func(context.Context, hostedsearch.ToolRequest, func(context.Context) (hostedsearch.Result, error)) (hostedsearch.Result, error) {
					panic("must not dispatch")
				}
			}
		}
		router.POST("/v1/responses", HostedResponsesBridge(router, options), func(c *gin.Context) { t.Fatal("unauthorized request reached relay") })
		request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"zt-claude-opus-5","input":"Hello","tools":[{"type":"web_search"}]}`))
		request.Header.Set("Authorization", "Bearer test-owned-key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.NotEqual(t, 200, response.Code)
		require.NotContains(t, response.Body.String(), "test-owned-key")
	}
}

func TestHostedCaptureDoesNotLogOrStoreCredentialInGinKeys(t *testing.T) {
	router := gin.New()
	router.Use(CaptureHostedResponsesCredential())
	router.POST("/v1/responses", func(c *gin.Context) {
		_, found := c.Get("Authorization")
		require.False(t, found)
		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		defer common.CleanupBodyStorage(c)
		_, err = io.ReadAll(storage)
		require.NoError(t, err)
		c.Status(http.StatusOK)
	})
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer test-owned-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, 200, response.Code)
}

func TestHostedSourcePreflightStopsBeforeAnyModelCharge(t *testing.T) {
	router := gin.New()
	router.Use(CaptureHostedResponsesCredential())
	options := HostedResponsesOptions{Provider: hostedControllerProvider{}, Authorize: func(*gin.Context, string) bool { return true }, Preflight: func(*gin.Context) error { return errors.New("tool source unavailable") }, Dispatch: func(*gin.Context, string) hostedsearch.ToolDispatch {
		t.Fatal("billing dispatch factory must not be entered")
		return nil
	}}
	router.POST("/v1/responses", HostedResponsesBridge(router, options), func(*gin.Context) { t.Fatal("unready hosted source reached model relay") })
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"zt-claude-opus-5","input":"Hi","tools":[{"type":"web_search"}]}`))
	r.Header.Set("Authorization", "Bearer dummy-only")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	require.Equal(t, 503, w.Code)
}
