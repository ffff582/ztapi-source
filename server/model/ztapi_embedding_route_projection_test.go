package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIEmbeddingAdaFamilyInferenceExact(t *testing.T) {
	for _, source := range []string{"text-embedding-ada-002", "text-embedding-3-small"} {
		t.Run(source, func(t *testing.T) {
			family, supported := InferZTAPIModelFamily(source)
			require.True(t, supported)
			require.Equal(t, ZTAPIModelFamilyOpenAI, family)
		})
	}
	for _, source := range []string{
		"text-emb-ada-002", "zt-text-embedding-ada-002", "text-embedding-ada",
		"text-embedding-ada-001", "text-embedding-ada-002-extra", "text-embedding-ada-003",
		"unknown-embedding",
	} {
		t.Run(source, func(t *testing.T) {
			family, supported := InferZTAPIModelFamily(source)
			require.False(t, supported)
			require.Empty(t, family)
		})
	}
}

func TestZTAPIEmbeddingAdaUnpublishedTrustedRouteProjection(t *testing.T) {
	for _, scenario := range []string{
		"enabled_managed", "unknown_model", "untrusted_channel", "no_ability",
		"disabled_channel", "disabled_ability", "wrong_group", "wrong_family",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv(ztapiUpstreamMasterKeyEnv, ztapiChannelSecretTestMasterKey)
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "routes.db")), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			previousDB, previousSQLite, previousMySQL := DB, common.UsingSQLite, common.UsingMySQL
			DB, common.UsingSQLite, common.UsingMySQL = db, true, false
			t.Cleanup(func() {
				DB, common.UsingSQLite, common.UsingMySQL = previousDB, previousSQLite, previousMySQL
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ZTAPIModelConfig{}))

			publicName := "zt-text-embedding-ada-002"
			config := ZTAPIModelConfig{
				ID: 89, SourceModel: "text-embedding-ada-002", PublicName: &publicName,
				Family: ZTAPIModelFamilyOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
				ProviderFamily: ZTAPIProviderOpenAI, EnabledGroups: `["default"]`,
				InputCostPerMillion: 0.078, InputPricePerMillion: 0.13,
				Published: false, PublicationSnapshotID: 0, Version: 4,
			}
			if scenario == "unknown_model" {
				config.SourceModel = "text-embedding-ada-002-extra"
			}
			if scenario == "wrong_family" {
				config.Family = ZTAPIModelFamilyClaude
			}
			require.NoError(t, db.Create(&config).Error)
			baseURL := "https://approved-upstream.example.com/v1"
			channel := Channel{
				Id: 1, Type: constant.ChannelTypeOpenAI, BaseURL: &baseURL,
				Status: common.ChannelStatusEnabled, ZTAPIManaged: true,
				ZTAPIFamily: ZTAPIModelFamilyOpenAI, Key: "local-route-fixture-key",
			}
			if scenario == "untrusted_channel" {
				channel.ZTAPIManaged = false
			}
			if scenario == "disabled_channel" {
				channel.Status = common.ChannelStatusManuallyDisabled
			}
			require.NoError(t, db.Create(&channel).Error)
			ability := Ability{Group: "default", Model: config.SourceModel, ChannelId: 1, Enabled: true}
			if scenario == "disabled_ability" {
				ability.Enabled = false
			}
			if scenario == "wrong_group" {
				ability.Group = "vip"
			}
			if scenario != "no_ability" {
				require.NoError(t, db.Create(&ability).Error)
			}

			var stored ZTAPIModelConfig
			require.NoError(t, db.First(&stored, 89).Error)
			require.False(t, stored.Published)
			require.Zero(t, stored.PublicationSnapshotID)
			ids, err := GetZTAPITrustedRouteChannelIDs(stored.SourceModel, stored.Family, stored.Groups())
			if scenario == "enabled_managed" {
				require.NoError(t, err)
				require.Equal(t, []int{1}, ids)
			} else {
				require.ErrorContains(t, err, "no trusted route for group default")
				require.Empty(t, ids)
			}
		})
	}
}
