package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageRegressionValidUsageAliases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage *dto.Usage
		valid bool
	}{
		{"nil", nil, false},
		{"zero", &dto.Usage{}, false},
		{"total_only", &dto.Usage{TotalTokens: 102}, false},
		{"prompt", &dto.Usage{PromptTokens: 100}, true},
		{"completion", &dto.Usage{CompletionTokens: 2}, true},
		{"input_alias", &dto.Usage{InputTokens: 100}, true},
		{"output_alias", &dto.Usage{OutputTokens: 2}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.valid, ValidUsage(tc.usage))
		})
	}
}

func TestUsageRegressionLocalEstimateHasProvenance(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	usage := ResponseText2Usage(c, "OK", "claude-opus-5-hc", 1000)
	require.Equal(t, "local_estimate", usage.UsageSource)
	require.True(t, common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens))
	require.Equal(t, 1000, usage.PromptTokens)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
}
