package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Claude Code shows the Anthropic error shape; an OpenAI-shaped 401 surfaces
// as an unreadable failure instead of "invalid API key".
func TestRelayRejectionsUseTheClientProtocolErrorShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.RedisEnabled = false
	engine := gin.New()
	SetRelayRouter(engine)

	for _, tc := range []struct {
		path      string
		anthropic bool
	}{
		{"/v1/messages", true},
		{"/v1/messages/count_tokens", true},
		{"/v1/chat/completions", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
			var body map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
			if tc.anthropic {
				require.Equal(t, "error", body["type"])
				require.Equal(t, "authentication_error", body["error"].(map[string]any)["type"])
			} else {
				require.Equal(t, "new_api_error", body["error"].(map[string]any)["type"])
			}
		})
	}
}
