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

package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIAutoRetryNeverSelectsGroupOutsidePublication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousAutoGroups := setting.AutoGroups2JsonString()
	common.MemoryCacheEnabled = true
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
		_ = setting.UpdateAutoGroupsByJsonString(previousAutoGroups)
	})

	priority := int64(10)
	weight := uint(100)
	for _, group := range []string{"default", "vip"} {
		channel := model.Channel{
			Type: constant.ChannelTypeOpenAI, Key: group + "-key", Status: common.ChannelStatusEnabled,
			Name: group + "-channel", Models: "gpt-source", Group: group,
			Priority: &priority, Weight: &weight, CreatedTime: common.GetTimestamp(),
		}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group: group, Model: "gpt-source", ChannelId: channel.Id, Enabled: true,
			Priority: &priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "auto", ModelName: "gpt-source", AllowedGroups: []string{"vip"},
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	require.Equal(t, "vip", selectedGroup)
	require.Equal(t, "vip-channel", channel.Name)
}

func TestZTAPIChannelSelectionOnlyUsesPublicationTrustedChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
	})

	weight := uint(100)
	trustedPriority := int64(10)
	untrustedPriority := int64(100)
	trusted := model.Channel{
		Type: constant.ChannelTypeOpenAI, Key: "trusted-key", Status: common.ChannelStatusEnabled,
		Name: "trusted-channel", Models: "gpt-source", Group: "default",
		Priority: &trustedPriority, Weight: &weight, CreatedTime: common.GetTimestamp(),
	}
	untrusted := model.Channel{
		Type: constant.ChannelTypeOpenAI, Key: "untrusted-key", Status: common.ChannelStatusEnabled,
		Name: "untrusted-channel", Models: "gpt-source", Group: "default",
		Priority: &untrustedPriority, Weight: &weight, CreatedTime: common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&trusted).Error)
	require.NoError(t, db.Create(&untrusted).Error)
	for _, fixture := range []struct {
		channelID int
		priority  *int64
	}{
		{channelID: trusted.Id, priority: &trustedPriority},
		{channelID: untrusted.Id, priority: &untrustedPriority},
	} {
		require.NoError(t, db.Create(&model.Ability{
			Group: "default", Model: "gpt-source", ChannelId: fixture.channelID, Enabled: true,
			Priority: fixture.priority, Weight: weight,
		}).Error)
	}
	model.InitChannelCache()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel, selectedGroup, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: "gpt-source", AllowedChannelIDs: []int{trusted.Id},
	})
	require.NoError(t, err)
	require.NotNil(t, channel)
	require.Equal(t, "default", selectedGroup)
	require.Equal(t, trusted.Id, channel.Id)
}
