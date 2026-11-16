package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBackfillZTAPIProtocolPreservesPublishedRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-dimensions.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}))

	aliases := []string{"zt-openai", "zt-claude", "zt-gemini"}
	configs := []ZTAPIModelConfig{
		{
			SourceModel: "gpt-source", PublicName: &aliases[0], Family: ZTAPIModelFamilyOpenAI,
			InputCostPerMillion: 1, OutputCostPerMillion: 2,
			InputPricePerMillion: 1.3, OutputPricePerMillion: 2.6,
			CacheReadRatio: 0.1, CacheCreationRatio: 1.25,
			CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
			ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
			EnabledGroups: `["default"]`, Published: true, Version: 7,
		},
		{
			SourceModel: "claude-source", PublicName: &aliases[1], Family: ZTAPIModelFamilyClaude,
			InputCostPerMillion: 3, OutputCostPerMillion: 15,
			InputPricePerMillion: 3.9, OutputPricePerMillion: 19.5,
			CacheReadRatio: 0.1, CacheCreationRatio: 1.25,
			CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
			ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 5,
			EnabledGroups: `["default","vip"]`, Published: true, Version: 8,
		},
		{
			SourceModel: "gemini-source", PublicName: &aliases[2], Family: ZTAPIModelFamilyGemini,
			InputCostPerMillion: 2, OutputCostPerMillion: 12,
			InputPricePerMillion: 2.6, OutputPricePerMillion: 15.6,
			CacheReadRatio: 0.1, CacheCreationRatio: 1.25,
			CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
			ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 6,
			EnabledGroups: `["vip"]`, Published: true, Version: 9,
		},
	}
	for i := range configs {
		require.NoError(t, db.Create(&configs[i]).Error)
	}

	require.NoError(t, BackfillZTAPIModelDimensions(db))

	wants := []struct {
		protocol string
		provider string
	}{
		{ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI},
		{ZTAPIProtocolAnthropic, ZTAPIProviderAnthropic},
		{ZTAPIProtocolGemini, ZTAPIProviderGoogle},
	}
	for i := range configs {
		var got ZTAPIModelConfig
		require.NoError(t, db.First(&got, configs[i].ID).Error)
		require.Equal(t, wants[i].protocol, got.Protocol)
		require.Equal(t, wants[i].provider, got.ProviderFamily)
		require.Equal(t, aliases[i], got.PublicNameValue())
		require.Equal(t, configs[i].InputPricePerMillion, got.InputPricePerMillion)
		require.Equal(t, configs[i].OutputPricePerMillion, got.OutputPricePerMillion)
		require.Equal(t, configs[i].EnabledGroups, got.EnabledGroups)
		require.Equal(t, configs[i].Published, got.Published)
		require.Equal(t, configs[i].Version, got.Version)
	}
}

func TestBackfillZTAPIModelDimensionsDoesNotOverwriteExplicitProvider(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-explicit-provider.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}))

	config := ZTAPIModelConfig{
		SourceModel: "explicit-source", Family: ZTAPIModelFamilyOpenAI,
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderDeepSeek,
		EnabledGroups: "[]", Version: 1,
	}
	require.NoError(t, db.Create(&config).Error)
	require.NoError(t, BackfillZTAPIModelDimensions(db))

	var got ZTAPIModelConfig
	require.NoError(t, db.First(&got, config.ID).Error)
	require.Equal(t, ZTAPIProviderDeepSeek, got.ProviderFamily)
}

func TestZTAPIModelEvidenceSchemaMigrates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-evidence.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&ZTAPIDiscoverySnapshot{},
		&ZTAPIDiscoveredModel{},
		&ZTAPIModelIdentity{},
		&ZTAPIModelPriceSource{},
		&ZTAPIModelVerification{},
	))

	for _, table := range []string{
		"ztapi_discovery_snapshots",
		"ztapi_discovered_models",
		"ztapi_model_identities",
		"ztapi_model_price_sources",
		"ztapi_model_verifications",
	} {
		require.True(t, db.Migrator().HasTable(table), table)
	}
}
