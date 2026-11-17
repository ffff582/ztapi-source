package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func hostedToolCacheFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ZTAPIModelConfig{}))
	oldDB, oldMemory, oldRedis := DB, common.MemoryCacheEnabled, common.RedisEnabled
	channelSyncLock.RLock()
	oldGroups, oldChannels := group2model2channels, channelsIDM
	channelSyncLock.RUnlock()
	DB, common.MemoryCacheEnabled, common.RedisEnabled = db, true, false
	t.Cleanup(func() {
		channelSyncLock.Lock()
		group2model2channels, channelsIDM = oldGroups, oldChannels
		channelSyncLock.Unlock()
		DB, common.MemoryCacheEnabled, common.RedisEnabled = oldDB, oldMemory, oldRedis
		_ = sqlDB.Close()
	})
	return db
}

func requireHostedSourceNotRoutable(t *testing.T, sourceID int) {
	t.Helper()
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	require.Contains(t, channelsIDM, sourceID, "source lineage metadata remains available by ID")
	for group, models := range group2model2channels {
		require.NotContains(t, models, "", "blank model route in group "+group)
		for name, channels := range models {
			require.NotContains(t, channels, sourceID, "source leaked into model route "+name)
		}
	}
}

func TestZTAPIHostedToolChannelCacheNoModelSourceAlone(t *testing.T) {
	db := hostedToolCacheFixture(t)
	source := Channel{Name: "official-tools-only", Status: common.ChannelStatusEnabled, Group: "source-only"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, source.AddAbilities(db))
	require.NotPanics(t, InitChannelCache)
	requireHostedSourceNotRoutable(t, source.Id)
	selected, err := GetRandomSatisfiedChannel("source-only", "", 0)
	require.NoError(t, err)
	require.Nil(t, selected)
}

func TestZTAPIChannelCacheMissingAbilityGroupIsNotRoutableAndCannotPanic(t *testing.T) {
	db := hostedToolCacheFixture(t)
	channel := Channel{Name: "metadata-before-ability", Status: common.ChannelStatusEnabled, Group: "not-yet-published", Models: "ordinary-model"}
	require.NoError(t, db.Create(&channel).Error)
	require.NotPanics(t, InitChannelCache)
	selected, err := GetRandomSatisfiedChannel("not-yet-published", "ordinary-model", 0)
	require.NoError(t, err)
	require.Nil(t, selected)
	require.NoError(t, channel.AddAbilities(db))
	require.NotPanics(t, InitChannelCache)
	selected, err = GetRandomSatisfiedChannel("not-yet-published", "ordinary-model", 0)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, channel.Id, selected.Id)
}

func TestZTAPIHostedToolChannelCacheNoModelSourceBesideNormalRoutes(t *testing.T) {
	db := hostedToolCacheFixture(t)
	source := Channel{Name: "official-tools-only", Status: common.ChannelStatusEnabled, Group: "default"}
	normal := Channel{Name: "normal-inference", Status: common.ChannelStatusEnabled, Group: "default,vip", Models: "ordinary-model"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&normal).Error)
	require.NoError(t, normal.AddAbilities(db))
	require.NotPanics(t, InitChannelCache)
	requireHostedSourceNotRoutable(t, source.Id)
	for _, group := range []string{"default", "vip"} {
		selected, err := GetRandomSatisfiedChannel(group, "ordinary-model", 0)
		require.NoError(t, err)
		require.NotNil(t, selected)
		require.Equal(t, normal.Id, selected.Id)
		selected, err = GetRandomSatisfiedChannel(group, "", 0)
		require.NoError(t, err)
		require.Nil(t, selected)
	}
}

func TestZTAPIHostedToolChannelCacheFixKeepsNoModelSourceUnroutable(t *testing.T) {
	db := hostedToolCacheFixture(t)
	source := Channel{Name: "official-tools-only", Status: common.ChannelStatusEnabled, Group: "source-only"}
	normal := Channel{Name: "normal-inference", Status: common.ChannelStatusEnabled, Group: " default , vip ", Models: "ordinary-model"}
	require.NoError(t, db.Create(&source).Error)
	require.NoError(t, db.Create(&normal).Error)
	require.NoError(t, db.Create(&Ability{ChannelId: source.Id, Group: source.Group, Model: "stale-source-route"}).Error)
	require.NotPanics(t, func() {
		success, failures, err := FixAbility()
		require.NoError(t, err)
		require.Equal(t, 2, success)
		require.Zero(t, failures)
	})
	var count int64
	require.NoError(t, db.Model(&Ability{}).Where("channel_id = ?", source.Id).Count(&count).Error)
	require.Zero(t, count)
	requireHostedSourceNotRoutable(t, source.Id)
	for _, group := range []string{"default", "vip"} {
		selected, err := GetRandomSatisfiedChannel(group, "ordinary-model", 0)
		require.NoError(t, err)
		require.NotNil(t, selected)
		require.Equal(t, normal.Id, selected.Id)
	}
}
