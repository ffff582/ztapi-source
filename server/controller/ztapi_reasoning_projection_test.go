package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIAdminReasoningCapabilityProjection(t *testing.T) {
	projection := ztapiReasoningProjectionFor("gpt-5.6-sol")
	require.Equal(t, []string{"high", "low", "max", "medium", "none", "xhigh"}, projection.ReasoningEfforts)
	require.NotEmpty(t, projection.ReasoningCapabilityEvidence)
	require.True(t, projection.ReasoningCapabilityLiveValidationRequired)

	pending := ztapiReasoningProjectionFor("gpt-5.4")
	require.Empty(t, pending.ReasoningEfforts)
	require.True(t, pending.ReasoningCapabilityLiveValidationRequired)
}
