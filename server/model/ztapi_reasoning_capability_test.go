package model

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIUltraMapsToMaxOnlyWhenDeclared(t *testing.T) {
	got, err := NormalizeZTAPIReasoningEffort("gpt-5.6-sol", "ultra")
	require.NoError(t, err)
	require.Equal(t, "max", got)

	_, err = NormalizeZTAPIReasoningEffort("gpt-5.4", "ultra")
	require.ErrorIs(t, err, ErrZTAPIReasoningEffortUnsupported)
	_, err = NormalizeZTAPIReasoningEffort("model-without-max", "ultra")
	require.ErrorIs(t, err, ErrZTAPIReasoningEffortUnsupported)
}

func TestZTAPIPendingReasoningCapabilityPassesKnownWireEffortWithoutGuessingAliases(t *testing.T) {
	got, err := NormalizeZTAPIReasoningEffort("gpt-5.4", "high")
	require.NoError(t, err)
	require.Equal(t, "high", got)

	_, err = NormalizeZTAPIReasoningEffort("gpt-5.4", "ultra")
	require.ErrorIs(t, err, ErrZTAPIReasoningEffortUnsupported)
	_, err = NormalizeZTAPIReasoningEffort("gpt-5.4", "turbo")
	require.ErrorIs(t, err, ErrZTAPIReasoningEffortUnsupported)
}

func TestZTAPIQuotationBackedReasoningCapabilitiesAreCompleteAndSorted(t *testing.T) {
	entries, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	mapped := 0
	for _, entry := range entries {
		capability, found := ZTAPIReasoningCapabilityFor(entry.SourceModel)
		if entry.Status != "mapped" {
			require.False(t, found, entry.Label)
			continue
		}
		mapped++
		require.True(t, found, entry.SourceModel)
		require.Equal(t, entry.SourceModel, capability.SourceModel)
		require.Equal(t, entry.Modality, capability.Modality)
		require.True(t, sort.StringsAreSorted(capability.ReasoningEfforts), entry.SourceModel)
		if entry.Modality != ZTAPIModalityText {
			require.Empty(t, capability.ReasoningEfforts, entry.SourceModel)
			require.False(t, capability.LiveValidationRequired, entry.SourceModel)
		}
	}
	require.Equal(t, 43, mapped)
}

func TestZTAPISolReasoningCapabilityUsesApprovedContractAndRequiresLiveValidation(t *testing.T) {
	capability, found := ZTAPIReasoningCapabilityFor("gpt-5.6-sol")
	require.True(t, found)
	require.Equal(t, []string{"high", "low", "max", "medium", "none", "xhigh"}, capability.ReasoningEfforts)
	require.Equal(t, "Owner-approved contract: none, low, medium, high, xhigh, and max; ultra is a local alias that maps to max.", capability.EvidenceQuote)
	require.True(t, capability.LiveValidationRequired)
}
