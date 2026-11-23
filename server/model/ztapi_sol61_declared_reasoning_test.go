package model

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// These expectations use the actual supplier declaration in request
// req_cc0a838be40082b5, not a guess based on another GPT model.
func TestZTAPISol61ReasoningUsesSupplierDeclarationWithoutClaimingLiveValidation(t *testing.T) {
	for _, effort := range []string{"none", "minimal"} {
		value, reason := NormalizeZTAPIReasoningEffortWithReason("gpt-6.1-sol", effort)
		require.Empty(t, value)
		require.Equal(t, "not_in_supported_list", reason)
	}
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		value, reason := NormalizeZTAPIReasoningEffortWithReason("gpt-6.1-sol", effort)
		require.Equal(t, effort, value)
		require.Empty(t, reason)
	}
	capability, ok := ZTAPIReasoningCapabilityFor("gpt-6.1-sol")
	require.True(t, ok)
	require.True(t, capability.LiveValidationRequired)
	require.Contains(t, capability.EvidenceQuote, "req_cc0a838be40082b5")
}
