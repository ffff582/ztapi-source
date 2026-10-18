package common

import (
	"net/http/httptest"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageRegressionManagedEstimatesCannotSettle(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		localFlag    bool
	}{
		{"source_only", "local_estimate", false},
		{"context_only", "", true},
		{"source_cannot_override_context", "upstream_reported", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			basecommon.SetContextKey(c, constant.ContextKeyLocalCountTokens, tc.localFlag)
			usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 2, UsageSource: tc.source}
			before := *usage
			result := ValidateZTAPIUsageDimensions(c, quotedUsageInfo(), usage)
			require.True(t, result.Managed)
			require.True(t, result.Pending, "local estimates are not upstream billing evidence")
			require.True(t, result.UsageMissing)
			require.Equal(t, int64(100), usageDimension(t, result, "input_tokens").Quantity)
			require.Equal(t, before, *usage, "validation must remain read-only")
			require.Equal(t, ZTAPIUsageDimensionResult{}, ValidateZTAPIUsageDimensions(c, &RelayInfo{}, usage), "legacy billing must remain unmanaged")
		})
	}
	result := ValidateZTAPIUsageDimensions(nil, quotedUsageInfo(), &dto.Usage{PromptTokens: 100, CompletionTokens: 2, UsageSource: "local_estimate"})
	require.True(t, result.Pending, "source guard must also work without a gin context")
}
