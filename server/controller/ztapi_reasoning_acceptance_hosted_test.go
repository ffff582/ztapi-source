package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/hostedsearch"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningAcceptanceHostedCannotDuplicateModelSpend(t *testing.T) {
	for _, internal := range []bool{false, true} {
		t.Run(map[bool]string{false: "customer", true: "internal"}[internal], func(t *testing.T) {
			router := gin.New()
			router.Use(CaptureHostedResponsesCredential())
			calls, factories := 0, 0
			options := HostedResponsesOptions{Provider: hostedControllerProvider{}, Authorize: func(*gin.Context, string) bool { return true }, Dispatch: func(*gin.Context, string) hostedsearch.ToolDispatch {
				factories++
				return func(ctx context.Context, _ hostedsearch.ToolRequest, call func(context.Context) (hostedsearch.Result, error)) (hostedsearch.Result, error) {
					return call(ctx)
				}
			}, ModelDispatch: func(ctx context.Context, body []byte, native hostedsearch.ModelRound) ([]byte, error) {
				if _, err := native(ctx, body); err != nil {
					return nil, err
				}
				return native(ctx, body)
			}}
			router.POST("/v1/responses", HostedResponsesBridge(router, options), func(c *gin.Context) {
				require.True(t, IsHostedSearchChild(c.Request))
				calls++
				c.JSON(200, gin.H{"id": "resp_owned", "object": "response", "status": "completed", "output": []any{gin.H{"id": "msg_owned", "status": "completed", "type": "message", "role": "assistant", "content": []any{gin.H{"type": "output_text", "text": "OK", "annotations": []any{}}}}}, "usage": gin.H{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}})
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"zt-claude-opus-5","input":"hi","tools":[{"type":"web_search"}],"stream":false}`))
			req.Header.Set("Authorization", "Bearer synthetic-only")
			req.RemoteAddr = "203.0.113.9:1234"
			if internal {
				req = req.WithContext(context.WithValue(req.Context(), constant.ContextKeyZTAPIInternalAcceptance, true))
				req = req.WithContext(context.WithValue(req.Context(), constant.ContextKeyZTAPIAcceptanceSingleRound, true))
			}
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if internal {
				require.Equal(t, 1, calls)
				require.Zero(t, factories)
				require.NotEqual(t, 200, out.Code)
			} else {
				require.Equal(t, 2, calls)
				require.Equal(t, 1, factories)
				require.Equal(t, 200, out.Code)
			}
		})
	}
}
