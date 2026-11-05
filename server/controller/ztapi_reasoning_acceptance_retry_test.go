package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningAcceptanceCannotSpendOnAutomaticFallback(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "claude-fable-5", AllowedChannelIDs: []int{2, 4}})
	err := types.WithOpenAIError(types.OpenAIError{Type: "server_error", Code: "server_error", Message: "synthetic"}, http.StatusServiceUnavailable)
	require.True(t, shouldRetry(c, err, 1), "normal customer fallback remains unchanged")
	c.Request.Header.Set("X-ZTAPI-Internal-Acceptance", "1")
	require.True(t, shouldRetry(c, err, 1), "raw client headers cannot create the trusted internal context")
	common.SetContextKey(c, constant.ContextKeyZTAPIInternalAcceptance, true)
	require.False(t, shouldRetry(c, err, 1), "one reserved acceptance attempt cannot silently become two paid attempts")
	common.SetContextKey(c, constant.ContextKeyZTAPIInternalAcceptance, false)
	require.True(t, shouldRetry(c, err, 1))
}
