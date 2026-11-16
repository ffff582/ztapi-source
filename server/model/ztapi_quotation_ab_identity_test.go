package model

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIABIdentityBridgeMatchesOnlyFrozenIdentities(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)

	report, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.NoError(t, err)
	require.Len(t, report.Mapped, 47)
	require.Equal(t, []string{
		"Doubao Seed 2.0 Pro", "Doubao Seed 2.1 Pro", "Gemini 3.8 Flash",
	}, report.Unmatched)

	seen := map[string]bool{}
	sources := map[string]bool{}
	publics := map[string]bool{}
	for _, identity := range report.Mapped {
		require.False(t, seen[identity.ModelName], "duplicate bridge identity")
		seen[identity.ModelName] = true
		require.False(t, sources[identity.SourceModel], "duplicate upstream identity")
		sources[identity.SourceModel] = true
		require.False(t, publics[identity.PublicName], "duplicate public identity")
		publics[identity.PublicName] = true
		if _, introduced := ztapiABIntroducedIdentityClaims[identity.ModelName]; !introduced {
			require.NotEmpty(t, identity.FrozenLabel)
		}
		require.NotEmpty(t, identity.QuotationCells)
		require.NotEmpty(t, identity.SourceModel)
		require.NotEmpty(t, identity.PublicName)
		require.NotEqual(t, identity.SourceModel, identity.PublicName)
	}
	require.Equal(t, "3bc554b32065f3cc7c50ffae545af1d20fff1e908732f98b88a2970a3fcb4690", report.WorkbookSHA256)
	require.Equal(t, "3671d5d915b222177b584b19c777712f4f9ebbd6497c514cf8fd580115cb56b3", report.FrozenSHA256)

	var image ZTAPIABModelIdentity
	for _, identity := range report.Mapped {
		if identity.ModelName == "GPT Image 2" {
			image = identity
		}
	}
	require.Equal(t, "gpt-image-2", image.SourceModel)
	require.Equal(t, "zt-gp-image-2", image.PublicName)
	require.Equal(t, "gp-image-2", image.FrozenLabel)
	require.Equal(t, "image", image.Modality)
}

func TestZTAPIABIdentityBridgeDoesNotGuessUnknownNames(t *testing.T) {
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	quote := ZTAPIABQuotationManifest{
		WorkbookSHA256: "test",
		Entries: []ZTAPIABQuotationEntry{
			{ModelName: "Gemini 3.8 Flash", ModelCode: "gemini-3.8-flash", QuotationCell: "C10"},
			{ModelName: "GPT 4.1", ModelCode: "wrong-but-similar", QuotationCell: "C11"},
		},
	}
	report, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.NoError(t, err)
	require.Equal(t, []string{"Gemini 3.8 Flash"}, report.Unmatched)
	require.Len(t, report.Mapped, 1)
	require.Equal(t, "gpt-4.1", report.Mapped[0].SourceModel)
}

// A model this workbook introduces carries a declared identity, not one guessed
// from its quoted name, and never one the frozen quotation already uses.
func TestZTAPIABIdentityBridgeCarriesIntroducedIdentities(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)

	report, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.NoError(t, err)

	byName := map[string]ZTAPIABModelIdentity{}
	for _, identity := range report.Mapped {
		byName[identity.ModelName] = identity
	}
	for name, want := range map[string]ZTAPIABModelIdentity{
		"GLM 5.3":       {SourceModel: "glm-5.3", PublicName: "zt-glm-5.3", Protocol: "openai_compatible", ProviderFamily: "glm", Modality: "text"},
		"GLM 5.3 Flash": {SourceModel: "glm-5.3-flash", PublicName: "zt-glm-5.3-flash", Protocol: "openai_compatible", ProviderFamily: "glm", Modality: "text"},
		"GPT 6 Astra":   {SourceModel: "gpt-6-astra", PublicName: "zt-gpt-6-astra", Protocol: "openai_compatible", ProviderFamily: "openai", Modality: "text"},
		"Kimi K3":       {SourceModel: "kimi-k3", PublicName: "zt-kimi-k3", Protocol: "openai_compatible", ProviderFamily: "moonshot", Modality: "text"},
		"Seedance 2.5":  {SourceModel: "doubao-seedance-2-5", PublicName: "zt-seedance-2.5", Protocol: "openai_compatible", ProviderFamily: "seedance", Modality: "video"},
	} {
		got, mapped := byName[name]
		require.True(t, mapped, "%s is quoted but carries no identity", name)
		require.Equal(t, want.SourceModel, got.SourceModel, name)
		require.Equal(t, want.PublicName, got.PublicName, name)
		require.Equal(t, want.Protocol, got.Protocol, name)
		require.Equal(t, want.ProviderFamily, got.ProviderFamily, name)
		require.Equal(t, want.Modality, got.Modality, name)
		require.NotEmpty(t, got.QuotationCells, name)
		require.Empty(t, got.FrozenLabel, name)
	}
}

func TestZTAPIABIdentityBridgeRefusesIntroducedIdentityAlreadyFrozen(t *testing.T) {
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	for i := range frozen {
		if frozen[i].SourceModel == "glm-5.2" {
			frozen[i].SourceModel = "glm-5.3"
		}
	}
	quote := ZTAPIABQuotationManifest{WorkbookSHA256: "test", Entries: []ZTAPIABQuotationEntry{
		{ModelName: "GLM 5.3", QuotationCell: "D36"},
	}}
	_, err = BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.ErrorIs(t, err, ErrZTAPIQuotationIdentityMismatch)
}

func TestZTAPIABIdentityBridgeRejectsFrozenAliasDrift(t *testing.T) {
	quote := ZTAPIABQuotationManifest{WorkbookSHA256: "test", Entries: []ZTAPIABQuotationEntry{{ModelName: "GPT 4.1", QuotationCell: "C1"}}}
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	for i := range frozen {
		if frozen[i].SourceModel == "gpt-4.1" {
			frozen[i].PublicName = "zt-renamed"
		}
	}
	_, err = BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.ErrorIs(t, err, ErrZTAPIQuotationIdentityMismatch)
}

func TestZTAPIABIdentityBridgeRejectsFrozenMetadataDrift(t *testing.T) {
	quote := ZTAPIABQuotationManifest{WorkbookSHA256: "test", Entries: []ZTAPIABQuotationEntry{{ModelName: "GPT 4.1", QuotationCell: "C1"}}}
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	for i := range frozen {
		if frozen[i].SourceModel == "gpt-4.1" {
			frozen[i].Protocol = "different_protocol"
		}
	}
	_, err = BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.ErrorIs(t, err, ErrZTAPIQuotationIdentityMismatch)
}

func TestZTAPIABIdentityBridgeQuotationEvidenceIsSortedAndUnique(t *testing.T) {
	quote := ZTAPIABQuotationManifest{WorkbookSHA256: "test", Entries: []ZTAPIABQuotationEntry{
		{ModelName: "GPT 4.1", QuotationCell: "C9"},
		{ModelName: "GPT 4.1", QuotationCell: "C2"},
		{ModelName: "GPT 4.1", QuotationCell: "C9"},
	}}
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	report, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.NoError(t, err)
	require.Len(t, report.Mapped, 1)
	require.Equal(t, []string{"C2", "C9"}, report.Mapped[0].QuotationCells)
	require.True(t, sort.StringsAreSorted(report.Mapped[0].QuotationCells))
}
