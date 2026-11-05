package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningAcceptanceGLMDeclaredLevels(t *testing.T) {
	for _, source := range []string{"glm-5", "glm-5.1", "kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code"} {
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
			got, reason := NormalizeZTAPIReasoningEffortWithReason(source, effort)
			require.Equal(t, effort, got)
			require.Empty(t, reason)
		}
		for _, effort := range []string{"max", "ultra"} {
			got, reason := NormalizeZTAPIReasoningEffortWithReason(source, effort)
			require.Empty(t, got, "supplier rejects max; do not reject a normal client request")
			require.Equal(t, "not_in_supported_list", reason)
		}
	}
}

func TestZTAPIReasoningAcceptanceLunaDeclaredLevels(t *testing.T) {
	got, reason := NormalizeZTAPIReasoningEffortWithReason("gpt-5.6-luna", "minimal")
	require.Empty(t, got)
	require.Equal(t, "not_in_supported_list", reason)
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
		got, reason = NormalizeZTAPIReasoningEffortWithReason("gpt-5.6-luna", effort)
		require.Equal(t, effort, got)
		require.Empty(t, reason)
	}
	got, reason = NormalizeZTAPIReasoningEffortWithReason("gpt-5.6-luna", "ultra")
	require.Equal(t, "max", got)
	require.Empty(t, reason)
}
