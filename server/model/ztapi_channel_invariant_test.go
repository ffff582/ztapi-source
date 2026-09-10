/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

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

func setupZTAPIChannelInvariantDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv(ztapiUpstreamMasterKeyEnv, ztapiChannelSecretTestMasterKey)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-channel-invariant.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &Ability{}, &ZTAPIModelConfig{}, &ZTAPIAuditEvent{},
		&ZTAPICatalogLock{}, &ZTAPIDiscoverySnapshot{}, &ZTAPIDiscoveredModel{},
		&ZTAPIModelIdentity{}, &ZTAPIModelPriceSource{}, &ZTAPIModelVerification{},
		&ZTAPIModelPublicationSnapshot{},
	))
	previousDB := DB
	previousUsingSQLite := common.UsingSQLite
	DB = db
	common.UsingSQLite = true
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		common.UsingSQLite = previousUsingSQLite
		InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})
	return db
}

func newZTAPITestChannel(modelName, group string) Channel {
	weight := uint(100)
	priority := int64(10)
	return Channel{
		Name:     "ztapi-test-channel",
		Type:     constant.ChannelTypeOpenAI,
		Key:      "test-key",
		Status:   common.ChannelStatusEnabled,
		Models:   modelName,
		Group:    group,
		Weight:   &weight,
		Priority: &priority,
	}
}

func createPublishedZTAPIConfig(t *testing.T, db *gorm.DB, source, alias, groups string) ZTAPIModelConfig {
	t.Helper()
	now := common.GetTimestamp()
	config := ZTAPIModelConfig{
		SourceModel:           source,
		PublicName:            &alias,
		Family:                ZTAPIModelFamilyOpenAI,
		InputCostPerMillion:   1,
		OutputCostPerMillion:  2,
		InputPricePerMillion:  3,
		OutputPricePerMillion: 6,
		CacheReadRatio:        0.1,
		CacheCreationRatio:    1.25,
		CacheCreation5mRatio:  1.25,
		CacheCreation1hRatio:  2,
		ImageRatio:            1,
		AudioRatio:            1,
		AudioCompletionRatio:  2,
		EnabledGroups:         groups,
		Published:             true,
		Version:               1,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	require.NoError(t, db.Create(&config).Error)
	return config
}

func TestZTAPIChannelInsertRollsBackWhenSourceCollidesWithAlias(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	createPublishedZTAPIConfig(t, db, "gpt-real-source", "zt-public-alias", `["default"]`)

	channel := newZTAPITestChannel("zt-public-alias", "default")
	err := channel.Insert()
	require.ErrorContains(t, err, "conflicts with published ZTAPI alias")

	var channelCount int64
	var abilityCount int64
	require.NoError(t, db.Model(&Channel{}).Count(&channelCount).Error)
	require.NoError(t, db.Model(&Ability{}).Count(&abilityCount).Error)
	require.Zero(t, channelCount)
	require.Zero(t, abilityCount)
}

func TestZTAPIChannelDeleteAtomicallyUnpublishesLastRoute(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-route-source", "default")
	require.NoError(t, channel.Insert())
	config := createPublishedZTAPIConfig(t, db, "gpt-route-source", "zt-gpt-route", `["default"]`)

	require.NoError(t, channel.Delete())

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, uint64(2), reloaded.Version)
	var channelCount int64
	var abilityCount int64
	require.NoError(t, db.Model(&Channel{}).Count(&channelCount).Error)
	require.NoError(t, db.Model(&Ability{}).Count(&abilityCount).Error)
	require.Zero(t, channelCount)
	require.Zero(t, abilityCount)
}

func TestZTAPIUpstreamModelSyncAtomicallyUnpublishesRemovedRoute(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-upstream-source", "default")
	require.NoError(t, channel.Insert())
	config := createPublishedZTAPIConfig(t, db, "gpt-upstream-source", "zt-gpt-upstream", `["default"]`)

	channel.Models = "gpt-replacement-source"
	channel.OtherSettings = `{}`
	require.NoError(t, SaveChannelUpstreamModelSettings(&channel, true))

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	var oldRouteCount int64
	require.NoError(t, db.Model(&Ability{}).Where("model = ?", "gpt-upstream-source").Count(&oldRouteCount).Error)
	require.Zero(t, oldRouteCount)
}

func TestZTAPIUpstreamModelSyncRollsBackChannelWhenAbilityUpdateFails(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-safe-source", "default")
	require.NoError(t, channel.Insert())
	createPublishedZTAPIConfig(t, db, "gpt-real-source", "zt-colliding-alias", `["default"]`)

	channel.Models = "zt-colliding-alias"
	channel.OtherSettings = `{"upstream_model_update_auto_sync_enabled":true}`
	err := SaveChannelUpstreamModelSettings(&channel, true)
	require.ErrorContains(t, err, "conflicts with published ZTAPI alias")

	var reloaded Channel
	require.NoError(t, db.First(&reloaded, channel.Id).Error)
	require.Equal(t, "gpt-safe-source", reloaded.Models)
	var safeRouteCount int64
	require.NoError(t, db.Model(&Ability{}).Where("channel_id = ? AND model = ?", channel.Id, "gpt-safe-source").Count(&safeRouteCount).Error)
	require.Equal(t, int64(1), safeRouteCount)
}

func TestZTAPIFixAbilityRollsBackWholeRebuildOnConflict(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-fix-safe", "default")
	require.NoError(t, channel.Insert())
	createPublishedZTAPIConfig(t, db, "gpt-fix-real", "zt-fix-collision", `["default"]`)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Update("models", "zt-fix-collision").Error)

	_, _, err := FixAbility()
	require.ErrorContains(t, err, "conflicts with published ZTAPI alias")

	var safeRouteCount int64
	require.NoError(t, db.Model(&Ability{}).Where("channel_id = ? AND model = ?", channel.Id, "gpt-fix-safe").Count(&safeRouteCount).Error)
	require.Equal(t, int64(1), safeRouteCount)
}

func TestZTAPIChannelUpdateAtomicallyUnpublishesWhenGroupLosesRoute(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-group-source", "default")
	require.NoError(t, channel.Insert())
	config := createPublishedZTAPIConfig(t, db, "gpt-group-source", "zt-gpt-group", `["default"]`)

	channel.Group = "vip"
	require.NoError(t, channel.Update())

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, uint64(2), reloaded.Version)
	var defaultRouteCount int64
	require.NoError(t, db.Model(&Ability{}).
		Where("channel_id = ? AND model = ? AND `group` = ?", channel.Id, "gpt-group-source", "default").
		Count(&defaultRouteCount).Error)
	require.Zero(t, defaultRouteCount)
}

func TestZTAPIPublishRejectsWhenAnySelectedGroupLacksTrustedRoute(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-multi-group-source", "default")
	require.NoError(t, channel.Insert())
	now := common.GetTimestamp()
	alias := "zt-gpt-multi-group"
	config := ZTAPIModelConfig{
		SourceModel: "gpt-multi-group-source", PublicName: &alias, Family: ZTAPIModelFamilyOpenAI,
		InputCostPerMillion: 1, OutputCostPerMillion: 2, InputPricePerMillion: 3, OutputPricePerMillion: 6,
		EnabledGroups: `["default"]`, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&config).Error)

	next := config
	next.EnabledGroups = `["default","vip"]`
	next.Published = true
	_, err := UpdateZTAPIModelConfigAndBilling(&next, config.Version, nil)
	var blocked *ZTAPIPublicationBlockedError
	require.ErrorAs(t, err, &blocked)
	require.Contains(t, blocked.Blockers, ZTAPIPublicationBlockerRouteUnavailable)

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, uint64(1), reloaded.Version)
}

func TestZTAPIPublishRejectsWildcardAllGroup(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-wildcard-group-source", "default")
	require.NoError(t, channel.Insert())
	now := common.GetTimestamp()
	alias := "zt-gpt-wildcard-group"
	config := ZTAPIModelConfig{
		SourceModel: "gpt-wildcard-group-source", PublicName: &alias, Family: ZTAPIModelFamilyOpenAI,
		InputCostPerMillion: 1, OutputCostPerMillion: 2, InputPricePerMillion: 3, OutputPricePerMillion: 6,
		EnabledGroups: `["default"]`, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&config).Error)

	next := config
	next.EnabledGroups = `["all"]`
	next.Published = true
	_, err := UpdateZTAPIModelConfigAndBilling(&next, config.Version, nil)
	var blocked *ZTAPIPublicationBlockedError
	require.ErrorAs(t, err, &blocked)
	require.Contains(t, blocked.Blockers, ZTAPIPublicationBlockerGroupsMissing)

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, uint64(1), reloaded.Version)
}

func TestZTAPIChannelUpdateAtomicallyUnpublishesWhenTrustedFamilyBecomesInvalid(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-family-source", "default")
	channel.ZTAPIManaged = true
	channel.ZTAPIFamily = ZTAPIModelFamilyOpenAI
	require.NoError(t, channel.Insert())
	config := createPublishedZTAPIConfig(t, db, "gpt-family-source", "zt-gpt-family", `["default"]`)

	channel.Type = constant.ChannelTypeDeepSeek
	require.NoError(t, channel.Update())

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, uint64(2), reloaded.Version)
}

func TestZTAPIChannelStatusAtomicallyUnpublishesLastRoute(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-status-source", "default")
	require.NoError(t, channel.Insert())
	config := createPublishedZTAPIConfig(t, db, "gpt-status-source", "zt-gpt-status", `["default"]`)
	previousMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCacheEnabled })

	require.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusManuallyDisabled, "manual test"))

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, uint64(2), reloaded.Version)
	var enabledRoutes int64
	require.NoError(t, db.Model(&Ability{}).Where("channel_id = ? AND enabled = ?", channel.Id, true).Count(&enabledRoutes).Error)
	require.Zero(t, enabledRoutes)
}

func TestZTAPIDisableTagAtomicallyUnpublishesLastRoute(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-tag-source", "default")
	channel.SetTag("ztapi-tag")
	require.NoError(t, channel.Insert())
	config := createPublishedZTAPIConfig(t, db, "gpt-tag-source", "zt-gpt-tag", `["default"]`)

	require.NoError(t, DisableChannelByTag("ztapi-tag"))

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, uint64(2), reloaded.Version)
	var enabledRoutes int64
	require.NoError(t, db.Model(&Ability{}).Where("channel_id = ? AND enabled = ?", channel.Id, true).Count(&enabledRoutes).Error)
	require.Zero(t, enabledRoutes)
}

func TestZTAPIEditTagAtomicallyUnpublishesWhenGroupLosesRoute(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	channel := newZTAPITestChannel("gpt-edit-tag-source", "default")
	channel.SetTag("ztapi-edit-tag")
	require.NoError(t, channel.Insert())
	config := createPublishedZTAPIConfig(t, db, "gpt-edit-tag-source", "zt-gpt-edit-tag", `["default"]`)
	vipGroup := "vip"

	require.NoError(t, EditChannelByTag("ztapi-edit-tag", nil, nil, nil, &vipGroup, nil, nil, nil, nil))

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, uint64(2), reloaded.Version)
	var defaultRouteCount int64
	require.NoError(t, db.Model(&Ability{}).
		Where("channel_id = ? AND model = ? AND `group` = ?", channel.Id, "gpt-edit-tag-source", "default").
		Count(&defaultRouteCount).Error)
	require.Zero(t, defaultRouteCount)
}
