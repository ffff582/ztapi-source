package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningLogPrivacy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	reasoningTokens := 321
	info := &relaycommon.RelayInfo{
		StartTime:                time.Now(),
		FirstResponseTime:        time.Now(),
		ReasoningEffortReceived:  "high",
		ReasoningEffortForwarded: "xhigh",
		ReasoningEffortSource:    relaycommon.ReasoningEffortSourceParameterOverride,
		ReasoningTokensReported:  &reasoningTokens,
		Request:                  &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "SECRET_PROMPT_FIXTURE"}}},
		ChannelMeta:              &relaycommon.ChannelMeta{ApiKey: "SECRET_KEY_FIXTURE"},
	}

	other := GenerateTextOtherInfo(ctx, info, 1, 1, 1, 0, 0, 0, 1)
	require.Equal(t, "high", other["reasoning_effort_received"])
	require.Equal(t, "xhigh", other["reasoning_effort_forwarded"])
	require.Equal(t, relaycommon.ReasoningEffortSourceParameterOverride, other["reasoning_effort_source"])
	require.Equal(t, reasoningTokens, other["reasoning_tokens_reported"])

	encoded, err := common.Marshal(other)
	require.NoError(t, err)
	for _, forbidden := range []string{"SECRET_PROMPT_FIXTURE", "SECRET_KEY_FIXTURE", "tool_arguments", "response_text"} {
		require.NotContains(t, strings.ToLower(string(encoded)), strings.ToLower(forbidden))
	}
}
