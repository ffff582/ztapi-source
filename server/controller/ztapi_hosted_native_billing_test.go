package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/hostedsearch"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type nativeBillingSearchProvider struct{ calls int }

func (p *nativeBillingSearchProvider) Search(context.Context, string, int) (hostedsearch.Result, error) {
	p.calls++
	return hostedsearch.Result{Outcome: hostedsearch.OutcomeSucceeded, HTTPStatus: 200,
		Sources: []hostedsearch.Source{{Title: "Fixture", URL: "https://example.com/guide", Summary: "Offline evidence"}}}, nil
}

func (*nativeBillingSearchProvider) Fetch(context.Context, string) (hostedsearch.Result, error) {
	return hostedsearch.Result{}, nil
}

func TestHostedNativeResponsesDoesNotDispatchUnpricedSearch(t *testing.T) {
	for _, fixture := range []struct {
		name                string
		stream, longContext bool
	}{{name: "json"}, {name: "stream", stream: true}, {name: "long-context", longContext: true}} {
		t.Run(fixture.name, func(t *testing.T) {
			stream := fixture.stream
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(CaptureHostedResponsesCredential())
			provider := &nativeBillingSearchProvider{}
			rounds, chargedCalls := 0, 0
			options := HostedResponsesOptions{Provider: provider,
				ModelDispatch: func(ctx context.Context, body []byte, native hostedsearch.ModelRound) ([]byte, error) {
					deadline, ok := ctx.Deadline()
					require.True(t, ok)
					require.Greater(t, time.Until(deadline), 10*time.Minute, "native long responses must not inherit the short Chat bridge deadline")
					return native(ctx, body)
				},
				Authorize: func(_ *gin.Context, model string) bool { return model == "zt-gpt-6.1-sol" },
				Dispatch: func(*gin.Context, string) hostedsearch.ToolDispatch {
					return func(ctx context.Context, _ hostedsearch.ToolRequest, call func(context.Context) (hostedsearch.Result, error)) (hostedsearch.Result, error) {
						chargedCalls++
						return call(ctx)
					}
				},
			}
			router.POST("/v1/responses", HostedResponsesBridge(router, options), func(c *gin.Context) {
				var body map[string]any
				require.NoError(t, common.DecodeJson(c.Request.Body, &body))
				require.True(t, IsHostedSearchChild(c.Request), "native unquoted search must enter the priced bridge before model I/O")
				require.Equal(t, "zt-gpt-6.1-sol", body["model"], "the model and native Responses route must not change")
				require.Equal(t, false, body["stream"])
				require.Equal(t, "high", body["reasoning"].(map[string]any)["effort"])
				var private string
				for _, item := range body["tools"].([]any) {
					tool := item.(map[string]any)
					require.NotEqual(t, "web_search", tool["type"], "the unpriced native provider tool cannot be dispatched")
					name, _ := tool["name"].(string)
					if strings.HasPrefix(name, "ztapi_hosted_web_") {
						private = name
					}
				}
				require.NotEmpty(t, private)
				rounds++
				output := []any{gin.H{"type": "message", "id": "msg_done", "role": "assistant", "status": "completed",
					"content": []any{gin.H{"type": "output_text", "text": "Verified guide", "annotations": []any{}}}}}
				if rounds == 1 {
					output = []any{gin.H{"type": "function_call", "id": "fc_search", "call_id": "call_search", "name": private,
						"arguments": `{"action":"search","query":"fixture guide"}`}}
				}
				c.JSON(200, gin.H{"id": "resp_native", "object": "response", "status": "completed", "output": output,
					"usage": gin.H{"input_tokens": 20, "output_tokens": 2, "total_tokens": 22}})
			})
			input := "Find the guide"
			if fixture.longContext {
				input += strings.Repeat("x", hostedsearch.MaxRequestBytes)
			}
			body, err := common.Marshal(map[string]any{"model": "zt-gpt-6.1-sol", "input": input, "stream": stream,
				"reasoning": map[string]any{"effort": "high"}, "tools": []any{map[string]any{"type": "web_search"}}})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
			request.Header.Set("Authorization", "Bearer offline-fixture-only")
			request.RemoteAddr = "203.0.113.9:1234"
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, 200, response.Code, response.Body.String())
			require.Equal(t, 2, rounds)
			require.Equal(t, 1, provider.calls)
			require.Equal(t, 1, chargedCalls, "a successful search must settle exactly once, separately from model tokens")
			require.Contains(t, response.Body.String(), "Verified guide")
			require.Contains(t, response.Header().Get("X-ZTAPI-Tool-Pricing"), "search=0.01CNY")
			if stream {
				require.Contains(t, response.Body.String(), "event: response.completed")
			}
		})
	}
}
