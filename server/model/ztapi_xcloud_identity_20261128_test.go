package model

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func xcloudIdentityExpectations20261128() []struct{ source, public, provider string } {
	return []struct{ source, public, provider string }{
		{"claude-fable-5", "zt-claude-fable-5", "anthropic"},
		{"claude-fable-5-1", "zt-claude-fable-5-1", "anthropic"},
		{"claude-fable-5-2", "zt-claude-fable-5-2", "anthropic"},
		{"claude-haiku-4-5-20251001", "zt-claude-haiku-4.5", "anthropic"},
		{"claude-haiku-5-5", "zt-claude-haiku-5.5", "anthropic"},
		{"claude-opus-5", "zt-claude-opus-5", "anthropic"},
		{"claude-opus-5-5", "zt-claude-opus-5.5", "anthropic"},
		{"claude-sonnet-5-5", "zt-claude-sonnet-5.5", "anthropic"},
		{"deepseek-v4.1-flash", "zt-deepseek-v4.1-flash", "deepseek"},
		{"gemini-4-neon", "zt-gemini-4-neon", "google"},
		{"glm-5.2", "zt-glm-5.2", "glm"},
		{"glm-5.3", "zt-glm-5.3", "glm"},
		{"glm-5.3-flash", "zt-glm-5.3-flash", "glm"},
		{"gpt-6-astra", "zt-gpt-6-astra", "openai"},
		{"gpt-6-luna", "zt-gpt-6-luna", "openai"},
		{"gpt-6-sol", "zt-gpt-6-sol", "openai"},
		{"gpt-6.1-sol", "zt-gpt-6.1-sol", "openai"},
		{"gpt-6.1-terra", "zt-gpt-6.1-terra", "openai"},
		{"gpt-6.2-astra", "zt-gpt-6.2-astra", "openai"},
		{"gpt-6.2-sol", "zt-gpt-6.2-sol", "openai"},
		{"kimi-k3", "zt-kimi-k3", "moonshot"},
		{"qwen3.8-flash", "zt-qwen3.8-flash", "qwen"},
		{"qwen3.8-max", "zt-qwen-3.8-max", "qwen"},
	}
}

func TestZTAPIXcloudIdentity20261128ExactCatalog(t *testing.T) {
	wants := xcloudIdentityExpectations20261128()
	quotes, err := ZTAPIXcloudCatalogQuotes20261128()
	require.NoError(t, err)
	quotedSources := map[string]bool{}
	for _, quote := range quotes {
		quotedSources[quote.SourceModel] = true
	}
	require.Len(t, quotedSources, 23)
	publics := map[string]bool{}
	for _, want := range wants {
		t.Run(want.source, func(t *testing.T) {
			require.True(t, quotedSources[want.source])
			public, provider, protocol, err := ZTAPIXcloudPublicIdentity20261128(want.source)
			require.NoError(t, err)
			require.Equal(t, want.public, public)
			require.Equal(t, want.provider, provider)
			require.Equal(t, "openai_compatible", protocol)
			require.False(t, publics[public], "different SKUs must not share a public identity")
			publics[public] = true
		})
	}
	require.Len(t, publics, 23)
}

func TestZTAPIXcloudIdentity20261128RejectsNearSKUs(t *testing.T) {
	for _, source := range []string{
		"", " gpt-6.2-sol", "gpt-6.2-sol ", "GPT-6.2-sol", "zt-gpt-6.2-sol",
		"gpt-6.2", "gpt-6.2-sol-hc", "gpt-6.2-sol-preview", "gpt-6.2-terra",
		"gpt-5.6-terra", "claude-haiku-4.5", "claude-haiku-4-5", "claude-opus-5.5",
		"claude-fable-5-2-hc", "gemini-4-argon", "qwen-3.8-max", "qwen3.8-max-0902",
	} {
		t.Run(source, func(t *testing.T) {
			public, provider, protocol, err := ZTAPIXcloudPublicIdentity20261128(source)
			require.Error(t, err)
			require.Empty(t, public)
			require.Empty(t, provider)
			require.Empty(t, protocol)
		})
	}
}

// Identity-only transcription of the supplied 28-model production baseline.
func xcloudIdentityBaseline20261128() []ZTAPIModelConfig {
	rows := []struct {
		id             int
		source, public string
		version        uint64
	}{
		{48, "claude-fable-5", "zt-claude-fable-5", 34},
		{97, "claude-fable-5-1", "zt-claude-fable-5-1", 6},
		{49, "claude-haiku-4-5-20251001", "zt-claude-haiku-4.5", 51},
		{50, "claude-opus-4-6", "zt-claude-opus-4.6", 60},
		{51, "claude-opus-4-7", "zt-claude-opus-4.7", 56},
		{52, "claude-opus-4-8", "zt-claude-opus-4.8", 60},
		{53, "claude-opus-5", "zt-claude-opus-5", 40},
		{126, "claude-opus-5-5", "zt-claude-opus-5.5", 12},
		{54, "claude-sonnet-4-6", "zt-claude-sonnet-4.6", 59},
		{55, "claude-sonnet-5", "zt-claude-sonnet-5", 61},
		{5, "deepseek-v4-flash", "zt-deepseek-v4-flash", 20},
		{7, "deepseek-v4-pro", "zt-deepseek-v4-pro", 22},
		{156, "deepseek-v4.1-flash", "zt-deepseek-v4.1-flash", 6},
		{61, "gemini-3-flash-preview", "zt-gemini-3-flash-preview", 29},
		{66, "gemini-3.5-flash", "zt-gemini-3.5-flash", 32},
		{25, "glm-5", "zt-glm-5", 23},
		{27, "glm-5.1", "zt-glm-5.1", 23},
		{29, "glm-5.3", "zt-glm-5.3", 18},
		{45, "glm-5.3-flash", "zt-glm-5.3-flash", 11},
		{85, "gpt-image-2", "zt-gp-image-2", 28},
		{81, "gpt-5.6-luna", "zt-gpt-5.6-luna", 53},
		{103, "gpt-6-astra", "zt-gpt-6-astra", 23},
		{136, "gpt-6-sol", "zt-gpt-6-sol", 9},
		{137, "gpt-6.1-sol", "zt-gpt-6.1-sol", 6},
		{30, "kimi-k2.5", "zt-kimi-k2.5", 23},
		{31, "kimi-k2.6", "zt-kimi-k2.6", 24},
		{86, "kimi-k2.7-code", "zt-kimi-k2.7-code", 22},
		{47, "doubao-seedance-2-5", "zt-seedance-2.5", 10},
	}
	configs := make([]ZTAPIModelConfig, 0, len(rows))
	for _, row := range rows {
		public := row.public
		configs = append(configs, ZTAPIModelConfig{
			ID: row.id, SourceModel: row.source, PublicName: &public,
			Version: row.version, Published: true,
		})
	}
	return configs
}

func cloneXcloudIdentityConfigs20261128(configs []ZTAPIModelConfig) []ZTAPIModelConfig {
	cloned := slices.Clone(configs)
	for i := range cloned {
		if cloned[i].PublicName != nil {
			public := *cloned[i].PublicName
			cloned[i].PublicName = &public
		}
	}
	return cloned
}

func TestZTAPIXcloudIdentity20261128PlanMatchesProductionBaseline(t *testing.T) {
	existing := xcloudIdentityBaseline20261128()
	before := cloneXcloudIdentityConfigs20261128(existing)
	defer func() { require.Equal(t, before, existing) }()
	plans, err := BuildZTAPIXcloudIdentityPlan20261128(existing)
	require.NoError(t, err)
	require.Len(t, plans, 23)
	bySource := map[string]ZTAPIModelConfig{}
	for _, config := range existing {
		bySource[config.SourceModel] = config
	}
	counts := map[string]int{}
	var newSources []string
	for i, want := range xcloudIdentityExpectations20261128() {
		plan := plans[i]
		require.Equal(t, want.source, plan.SourceModel)
		require.Equal(t, want.public, plan.PublicName)
		counts[plan.Action]++
		if config, found := bySource[want.source]; found {
			require.Equal(t, config.ID, plan.ExistingConfigID)
			require.Equal(t, config.Version, plan.ExistingVersion)
			require.True(t, plan.Published)
			require.Equal(t, "existing_published", plan.Action)
		} else {
			require.Zero(t, plan.ExistingConfigID)
			require.Zero(t, plan.ExistingVersion)
			require.False(t, plan.Published)
			require.Equal(t, "new_candidate", plan.Action)
			newSources = append(newSources, plan.SourceModel)
		}
	}
	require.Equal(t, map[string]int{"existing_published": 11, "new_candidate": 12}, counts)
	require.Equal(t, []string{
		"claude-fable-5-2", "claude-haiku-5-5", "claude-sonnet-5-5", "gemini-4-neon",
		"glm-5.2", "gpt-6-luna", "gpt-6.1-terra", "gpt-6.2-astra", "gpt-6.2-sol",
		"kimi-k3", "qwen3.8-flash", "qwen3.8-max",
	}, newSources)
}

func TestZTAPIXcloudIdentity20261128PlanReusesAllExistingConfigs(t *testing.T) {
	// The fresh all-existing snapshot includes seven drafts absent from the
	// published-only baseline. Construct booleans explicitly (MySQL emits 0/1).
	rows := []struct {
		id             int
		source, public string
		version        uint64
		published      bool
	}{
		{48, "claude-fable-5", "zt-claude-fable-5", 34, true},
		{97, "claude-fable-5-1", "zt-claude-fable-5-1", 6, true},
		{49, "claude-haiku-4-5-20251001", "zt-claude-haiku-4.5", 51, true},
		{140, "claude-haiku-5-5", "", 1, false},
		{53, "claude-opus-5", "zt-claude-opus-5", 40, true},
		{126, "claude-opus-5-5", "zt-claude-opus-5.5", 12, true},
		{128, "claude-sonnet-5-5", "zt-claude-sonnet-5.5", 9, false},
		{156, "deepseek-v4.1-flash", "zt-deepseek-v4.1-flash", 6, true},
		{28, "glm-5.2", "zt-glm-5.2", 33, false},
		{29, "glm-5.3", "zt-glm-5.3", 18, true},
		{45, "glm-5.3-flash", "zt-glm-5.3-flash", 11, true},
		{103, "gpt-6-astra", "zt-gpt-6-astra", 23, true},
		{135, "gpt-6-luna", "zt-gpt-6-luna", 10, false},
		{136, "gpt-6-sol", "zt-gpt-6-sol", 9, true},
		{137, "gpt-6.1-sol", "zt-gpt-6.1-sol", 6, true},
		{32, "kimi-k3", "zt-kimi-k3", 12, false},
		{46, "qwen3.8-flash", "zt-qwen3.8-flash", 6, false},
		{39, "qwen3.8-max", "zt-qwen-3.8-max", 22, false},
	}
	existing := make([]ZTAPIModelConfig, 0, len(rows))
	bySource := map[string]ZTAPIModelConfig{}
	for _, row := range rows {
		config := ZTAPIModelConfig{ID: row.id, SourceModel: row.source, Version: row.version, Published: row.published}
		if row.public != "" {
			public := row.public
			config.PublicName = &public
		}
		existing = append(existing, config)
		bySource[row.source] = config
	}
	require.Len(t, existing, 18)
	before := cloneXcloudIdentityConfigs20261128(existing)
	defer func() { require.Equal(t, before, existing) }()
	plans, err := BuildZTAPIXcloudIdentityPlan20261128(existing)
	require.NoError(t, err)
	require.Len(t, plans, 23)
	counts := map[string]int{}
	var newSources []string
	for i, want := range xcloudIdentityExpectations20261128() {
		plan := plans[i]
		require.Equal(t, want.source, plan.SourceModel)
		require.Equal(t, want.public, plan.PublicName)
		counts[plan.Action]++
		if config, found := bySource[plan.SourceModel]; found {
			require.Equal(t, config.ID, plan.ExistingConfigID)
			require.Equal(t, config.Version, plan.ExistingVersion)
			require.Equal(t, config.Published, plan.Published)
			if config.Published {
				require.Equal(t, "existing_published", plan.Action)
			} else {
				require.Equal(t, "existing_draft", plan.Action)
			}
		} else {
			require.Equal(t, "new_candidate", plan.Action)
			require.Zero(t, plan.ExistingConfigID)
			require.Zero(t, plan.ExistingVersion)
			require.False(t, plan.Published)
			newSources = append(newSources, plan.SourceModel)
		}
	}
	require.Equal(t, map[string]int{"existing_published": 11, "existing_draft": 7, "new_candidate": 5}, counts)
	require.Equal(t, []string{"claude-fable-5-2", "gemini-4-neon", "gpt-6.1-terra", "gpt-6.2-astra", "gpt-6.2-sol"}, newSources)
}

func TestZTAPIXcloudIdentity20261128PlanReusesDrafts(t *testing.T) {
	for _, tc := range []struct {
		source, public string
		nilPublic      bool
	}{
		{"claude-fable-5-2", "", true},
		{"claude-haiku-5-5", "", false},
		{"claude-sonnet-5-5", "zt-claude-sonnet-5.5", false},
		{"qwen3.8-flash", "zt-qwen3.8-flash", false},
		{"qwen3.8-max", "zt-qwen-3.8-max", false},
	} {
		t.Run(tc.source, func(t *testing.T) {
			public := tc.public
			config := ZTAPIModelConfig{ID: 701, SourceModel: tc.source, PublicName: &public, Version: 9}
			if tc.nilPublic {
				config.PublicName = nil
			}
			existing := []ZTAPIModelConfig{config}
			before := cloneXcloudIdentityConfigs20261128(existing)
			defer func() { require.Equal(t, before, existing) }()
			plans, err := BuildZTAPIXcloudIdentityPlan20261128(existing)
			require.NoError(t, err)
			found := false
			for _, plan := range plans {
				if plan.SourceModel == tc.source {
					found = true
					require.Equal(t, 701, plan.ExistingConfigID)
					require.Equal(t, uint64(9), plan.ExistingVersion)
					require.False(t, plan.Published)
					require.Equal(t, "existing_draft", plan.Action)
				}
			}
			require.True(t, found)
		})
	}
}

func TestZTAPIXcloudIdentity20261128PlanRejectsDuplicateSources(t *testing.T) {
	for _, published := range []bool{false, true} {
		configs := []ZTAPIModelConfig{
			{ID: 701, SourceModel: "gpt-6.2-sol", Version: 2, Published: published},
			{ID: 702, SourceModel: "gpt-6.2-sol", Version: 3},
		}
		before := cloneXcloudIdentityConfigs20261128(configs)
		plans, err := BuildZTAPIXcloudIdentityPlan20261128(configs)
		require.ErrorContains(t, err, "duplicate source")
		require.Nil(t, plans)
		require.Equal(t, before, configs)
	}
	config := ZTAPIModelConfig{ID: 701, SourceModel: "gpt-6.2-sol", Version: 2}
	plans, err := BuildZTAPIXcloudIdentityPlan20261128([]ZTAPIModelConfig{config, config})
	require.ErrorContains(t, err, "duplicate source")
	require.Nil(t, plans)
}

func TestZTAPIXcloudIdentity20261128PlanRejectsAliasConflicts(t *testing.T) {
	for _, public := range []string{
		"zt-claude-haiku-4.5", "zt-claude-opus-5.5", "zt-claude-sonnet-5.5",
		"zt-qwen-3.8-max", "zt-qwen3.8-flash", "zt-gpt-6.2-sol", " zt-gpt-6.2-sol ",
	} {
		for _, published := range []bool{false, true} {
			existing := []ZTAPIModelConfig{{ID: 900, SourceModel: "different-sku", PublicName: &public, Version: 4, Published: published}}
			before := cloneXcloudIdentityConfigs20261128(existing)
			plans, err := BuildZTAPIXcloudIdentityPlan20261128(existing)
			require.ErrorContains(t, err, "public alias")
			require.Nil(t, plans)
			require.Equal(t, before, existing)
		}
	}
	public := "zt-gpt-6.2-sol"
	plans, err := BuildZTAPIXcloudIdentityPlan20261128([]ZTAPIModelConfig{
		{ID: 701, SourceModel: "gpt-6.2-sol", PublicName: &public, Version: 1},
		{ID: 702, SourceModel: "another-sku", PublicName: &public, Version: 1},
	})
	require.ErrorContains(t, err, "public alias")
	require.Nil(t, plans)
}

func TestZTAPIXcloudIdentity20261128PlanDoesNotRenameExistingIdentity(t *testing.T) {
	public := "zt-claude-haiku-4-5-20251001"
	existing := []ZTAPIModelConfig{{ID: 49, SourceModel: "claude-haiku-4-5-20251001", PublicName: &public, Version: 51, Published: true}}
	before := cloneXcloudIdentityConfigs20261128(existing)
	plans, err := BuildZTAPIXcloudIdentityPlan20261128(existing)
	require.ErrorContains(t, err, "public alias")
	require.Nil(t, plans)
	require.Equal(t, before, existing)
}

func TestZTAPIXcloudIdentity20261128PlanRejectsAliasSourceCollision(t *testing.T) {
	plans, err := BuildZTAPIXcloudIdentityPlan20261128([]ZTAPIModelConfig{{ID: 900, SourceModel: "zt-gpt-6.2-sol", Version: 1}})
	require.ErrorContains(t, err, "public alias")
	require.Nil(t, plans)
	public := "gpt-6.2-sol"
	plans, err = BuildZTAPIXcloudIdentityPlan20261128([]ZTAPIModelConfig{{ID: 900, SourceModel: "different-sku", PublicName: &public, Version: 1}})
	require.ErrorContains(t, err, "public alias")
	require.Nil(t, plans)
}

func TestZTAPIXcloudIdentity20261128PlanIsDeterministicAndIndependent(t *testing.T) {
	existing := xcloudIdentityBaseline20261128()
	before := cloneXcloudIdentityConfigs20261128(existing)
	first, err := BuildZTAPIXcloudIdentityPlan20261128(existing)
	require.NoError(t, err)
	require.Equal(t, before, existing)
	reversed := slices.Clone(existing)
	slices.Reverse(reversed)
	second, err := BuildZTAPIXcloudIdentityPlan20261128(reversed)
	require.NoError(t, err)
	require.Equal(t, first, second)
	first[0].PublicName = "changed-result"
	third, err := BuildZTAPIXcloudIdentityPlan20261128(existing)
	require.NoError(t, err)
	require.Equal(t, second, third)
	require.Equal(t, before, existing)

	allNew, err := BuildZTAPIXcloudIdentityPlan20261128(nil)
	require.NoError(t, err)
	require.Len(t, allNew, 23)
	for _, plan := range allNew {
		require.Equal(t, "new_candidate", plan.Action)
		require.False(t, plan.Published)
		require.Zero(t, plan.ExistingConfigID)
		require.Zero(t, plan.ExistingVersion)
	}
}
