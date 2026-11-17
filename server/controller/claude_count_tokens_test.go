package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func countClaudeTokensForTest(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	service.InitTokenEncoders()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	CountClaudeMessageTokens(ctx)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &decoded))
	return recorder, decoded
}

func TestCountClaudeMessageTokensEstimatesLocally(t *testing.T) {
	plain := `{"model":"zt-claude-sonnet-5","system":"You are Claude Code.","messages":[{"role":"user","content":"Summarise the repository layout."}]}`
	recorder, small := countClaudeTokensForTest(t, plain)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Greater(t, small["input_tokens"].(float64), 5.0)

	withTools := `{"model":"zt-claude-sonnet-5","system":"You are Claude Code.","messages":[{"role":"user","content":"Summarise the repository layout."}],
		"tools":[{"name":"Read","description":"Read a file from the local filesystem.","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}]}`
	_, larger := countClaudeTokensForTest(t, withTools)
	require.Greater(t, larger["input_tokens"].(float64), small["input_tokens"].(float64), "tool definitions count toward the context")
}

func TestCountClaudeMessageTokensRejectsIncompleteRequestsInAnthropicShape(t *testing.T) {
	for _, body := range []string{`{"messages":[{"role":"user","content":"hi"}]}`, `{"model":"zt-claude-sonnet-5","messages":[]}`, `not json`} {
		recorder, decoded := countClaudeTokensForTest(t, body)
		require.Equal(t, http.StatusBadRequest, recorder.Code, body)
		require.Equal(t, "error", decoded["type"], body)
		require.Equal(t, "invalid_request_error", decoded["error"].(map[string]any)["type"], body)
	}
}
