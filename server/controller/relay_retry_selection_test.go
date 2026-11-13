package controller

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRelayRetryUsesPersistedNormalizedSelectionModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})

	dsn := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open retry database: %v", err)
	}
	model.DB = db
	if err := db.AutoMigrate(&model.Channel{}, &model.Ability{}); err != nil {
		t.Fatalf("migrate retry tables: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	const normalizedModel = "gpt-4o-gizmo-*"
	highPriority := int64(20)
	lowPriority := int64(10)
	weight := uint(100)
	channels := []*model.Channel{
		{
			Type:        constant.ChannelTypeOpenAI,
			Key:         "first-channel-key",
			Status:      common.ChannelStatusEnabled,
			Name:        "first-normalized-channel",
			Weight:      &weight,
			CreatedTime: common.GetTimestamp(),
			Models:      normalizedModel,
			Group:       "default",
			Priority:    &highPriority,
		},
		{
			Type:        constant.ChannelTypeOpenAI,
			Key:         "retry-channel-key",
			Status:      common.ChannelStatusEnabled,
			Name:        "second-normalized-channel",
			Weight:      &weight,
			CreatedTime: common.GetTimestamp(),
			Models:      normalizedModel,
			Group:       "default",
			Priority:    &lowPriority,
		},
	}
	for _, channel := range channels {
		if err := db.Create(channel).Error; err != nil {
			t.Fatalf("create retry channel: %v", err)
		}
		if err := db.Create(&model.Ability{
			Group:     "default",
			Model:     normalizedModel,
			ChannelId: channel.Id,
			Enabled:   true,
			Priority:  channel.Priority,
			Weight:    weight,
		}).Error; err != nil {
			t.Fatalf("create retry ability: %v", err)
		}
	}
	model.InitChannelCache()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	info := &relaycommon.RelayInfo{
		OriginModelName:    "gpt-4o-gizmo-customer-alias",
		SelectionModelName: normalizedModel,
		TokenGroup:         "default",
		ChannelMeta:        &relaycommon.ChannelMeta{},
	}
	retryParam := newRelayRetryParam(ctx, info)
	retryParam.SetRetry(1)

	channel, apiErr := getChannel(ctx, info, retryParam)
	if apiErr != nil {
		t.Fatalf("select retry channel: %v", apiErr)
	}
	if channel == nil || channel.Id != channels[1].Id {
		t.Fatalf("retry selected channel %#v, want second normalized channel %d", channel, channels[1].Id)
	}
	if ctx.GetString("original_model") != info.OriginModelName {
		t.Fatalf("retry attribution model=%q, want original %q", ctx.GetString("original_model"), info.OriginModelName)
	}
}

func TestRelayRetryCopiesFrozenZTAPIPublicationAuthority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		OriginModelName:    "zt-public-model",
		SelectionModelName: "gpt-source-model",
		TokenGroup:         "auto",
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			AllowedGroups: []string{"vip"}, AllowedChannelIDs: []int{11, 12},
		},
	}

	retryParam := newRelayRetryParam(ctx, info)
	info.ZTAPIPublicationSnapshot.AllowedGroups[0] = "blocked"
	info.ZTAPIPublicationSnapshot.AllowedChannelIDs[0] = 99

	if got := retryParam.AllowedGroups; len(got) != 1 || got[0] != "vip" {
		t.Fatalf("retry groups = %#v, want frozen vip authority", got)
	}
	if got := retryParam.AllowedChannelIDs; len(got) != 2 || got[0] != 11 || got[1] != 12 {
		t.Fatalf("retry channels = %#v, want frozen channel authority", got)
	}
}
