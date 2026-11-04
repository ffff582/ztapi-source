package model

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestZTAPIReasoningDropsUnsupportedInsteadOfRejecting(t *testing.T) {
	for _, tc := range []struct{ model, effort, reason string }{
		{"unknown-future", "high", "unknown_model"},
		{"gpt-5.6-sol", "minimal", "not_in_supported_list"},
		{"gpt-4.1", "high", "non_reasoning_model"},
		{"gpt-5.4", "hihg", "unrecognized_value"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			normalized, err := NormalizeZTAPIReasoningEffort(tc.model, tc.effort)
			require.NoError(t, err)
			require.Empty(t, normalized)
			value, reason := NormalizeZTAPIReasoningEffortWithReason(tc.model, tc.effort)
			require.Empty(t, value)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestZTAPIReasoningDroppedMetadataSurvivesDurableSanitization(t *testing.T) {
	for _, reason := range []string{"unknown_model", "not_in_supported_list", "non_reasoning_model", "unrecognized_value"} {
		log := Log{Other: `{"reasoning_effort_received":"unrecognized","reasoning_effort_source":"request","reasoning_effort_dropped":true,"reasoning_effort_drop_reason":"` + reason + `","api_key":"SECRET_DO_NOT_STORE"}`}
		require.NoError(t, sanitizeZTAPISettlementLogMetadata(&log))
		require.Contains(t, log.Other, `"reasoning_effort_dropped":true`)
		require.Contains(t, log.Other, reason)
		require.NotContains(t, log.Other, "SECRET_DO_NOT_STORE")
	}
	log := Log{Other: `{"reasoning_effort_dropped":true,"reasoning_effort_drop_reason":"SECRET_INVALID_REASON"}`}
	require.ErrorIs(t, sanitizeZTAPISettlementLogMetadata(&log), ErrZTAPISettlementLogInvalid)
}

func TestZTAPISol61ReasoningCapabilityDoesNotClaimLiveValidation(t *testing.T) {
	capability, ok := ZTAPIReasoningCapabilityFor("gpt-6.1-sol")
	require.True(t, ok)
	require.True(t, capability.LiveValidationRequired)
	require.Equal(t, []string{"high", "low", "max", "medium", "xhigh"}, capability.ReasoningEfforts)
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
		normalized, err := NormalizeZTAPIReasoningEffort("gpt-6.1-sol", effort)
		require.NoError(t, err)
		if effort == "none" || effort == "minimal" {
			require.Empty(t, normalized)
		} else {
			require.Equal(t, effort, normalized)
		}
	}
	video, ok := ZTAPIReasoningCapabilityFor("doubao-seedance-2-5")
	require.True(t, ok)
	require.Equal(t, ZTAPIModalityVideo, video.Modality)
	require.False(t, video.LiveValidationRequired)
}
