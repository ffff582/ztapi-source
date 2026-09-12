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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIAudioAndRealtimeQuotaUsesFrozenFourPriceSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/realtime", nil)
	ctx.Set("group", "vip")

	snapshot := &relaycommon.ZTAPIPublicationSnapshot{
		PublicationID:         42,
		Version:               7,
		PublicName:            "zt-gpt-realtime",
		SourceModel:           "gpt-realtime-source",
		AllowedGroups:         []string{"vip"},
		AuthorizedGroup:       "vip",
		InputPricePerMillion:  10,
		OutputPricePerMillion: 30,
		CacheReadRatio:         0.1,
		CacheCreationRatio:     1.1,
		CacheCreation5mRatio:   1.25,
		CacheCreation1hRatio:   2,
		ImageRatio:             3,
		AudioRatio:             4,
		AudioCompletionRatio:   5,
	}
	relayInfo := &relaycommon.RelayInfo{
		OriginModelName:          snapshot.PublicName,
		UserGroup:                "vip",
		UsingGroup:               "vip",
		ZTAPIPublicationSnapshot: snapshot,
	}
	priceData, err := relayhelper.ModelPriceHelper(ctx, relayInfo, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, priceData, relayInfo.PriceData)

	// Mutating the publication object after price loading simulates a concurrent
	// unpublish/reprice. Audio and realtime settlement must use PriceData already
	// frozen for this request.
	snapshot.InputPricePerMillion = 198
	snapshot.OutputPricePerMillion = 396
	snapshot.AllowedGroups = []string{"blocked"}

	quotaInfo := newAudioQuotaInfo(
		relayInfo,
		relayInfo.OriginModelName,
		TokenDetails{TextTokens: 100, AudioTokens: 20},
		TokenDetails{TextTokens: 50, AudioTokens: 10},
	)
	quota := calculateAudioQuota(quotaInfo)

	// (100 input text + 50*3 output text + 20*4 input audio + 10*4*5 output audio) * 5
	require.Equal(t, 2650, quota)
	require.Equal(t, 5.0, quotaInfo.ModelRatio)
	require.Equal(t, 3.0, quotaInfo.CompletionRatio)
	require.Equal(t, 4.0, quotaInfo.AudioRatio)
	require.Equal(t, 5.0, quotaInfo.AudioCompletionRatio)
	require.Equal(t, 1.0, quotaInfo.GroupRatio)
}

func TestZTAPIAudioAndRealtimeSettlementEntrypointsUseFrozenPriceData(t *testing.T) {
	tests := []struct {
		name   string
		settle func(*gin.Context, *relaycommon.RelayInfo)
	}{
		{
			name: "audio",
			settle: func(ctx *gin.Context, info *relaycommon.RelayInfo) {
				PostAudioConsumeQuota(ctx, info, &dto.Usage{
					PromptTokens:     120,
					CompletionTokens: 60,
					TotalTokens:      180,
					PromptTokensDetails: dto.InputTokenDetails{
						TextTokens: 100, AudioTokens: 20,
					},
					CompletionTokenDetails: dto.OutputTokenDetails{
						TextTokens: 50, AudioTokens: 10,
					},
				}, "")
			},
		},
		{
			name: "realtime",
			settle: func(ctx *gin.Context, info *relaycommon.RelayInfo) {
				PostWssConsumeQuota(ctx, info, info.OriginModelName, &dto.RealtimeUsage{
					InputTokens: 120, OutputTokens: 60, TotalTokens: 180,
					InputTokenDetails: dto.InputTokenDetails{
						TextTokens: 100, AudioTokens: 20,
					},
					OutputTokenDetails: dto.OutputTokenDetails{
						TextTokens: 50, AudioTokens: 10,
					},
				}, "")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, ctx, relayInfo := setupZTAPIQuotaSettlementTest(t)
			priceData, err := relayhelper.ModelPriceHelper(ctx, relayInfo, 1000, &types.TokenCountMeta{})
			require.NoError(t, err)
			require.Equal(t, priceData, relayInfo.PriceData)
			relayInfo.FinalPreConsumedQuota = 2650

			// A concurrent publication mutation must not alter this request.
			relayInfo.ZTAPIPublicationSnapshot.InputPricePerMillion = 198
			relayInfo.ZTAPIPublicationSnapshot.OutputPricePerMillion = 396
			test.settle(ctx, relayInfo)

			var consumeLog model.Log
			require.NoError(t, db.Where("type = ?", model.LogTypeConsume).Order("id DESC").First(&consumeLog).Error)
			require.Equal(t, 2650, consumeLog.Quota)
			require.Equal(t, relayInfo.OriginModelName, consumeLog.ModelName)
		})
	}
}

func setupZTAPIQuotaSettlementTest(t *testing.T) (*gorm.DB, *gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousBatchUpdate := common.BatchUpdateEnabled
	previousLogConsume := common.LogConsumeEnabled
	previousDataExport := common.DataExportEnabled
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = true
	common.DataExportEnabled = false

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "quota.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db

	user := model.User{
		Username: fmt.Sprintf("quota-%s", t.Name()), Password: "not-used",
		Status: common.UserStatusEnabled, Quota: 100_000, Group: "default",
		AffCode: fmt.Sprintf("quota-%d", time.Now().UnixNano()),
	}
	require.NoError(t, db.Create(&user).Error)
	weight := uint(1)
	priority := int64(0)
	channel := model.Channel{
		Name: "quota-channel", Type: 1, Key: "test-only", Status: common.ChannelStatusEnabled,
		Models: "gpt-realtime-source", Group: "default", Weight: &weight, Priority: &priority,
	}
	require.NoError(t, db.Create(&channel).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/audio", nil)
	ctx.Set("group", "default")
	ctx.Set("username", user.Username)

	snapshot := &relaycommon.ZTAPIPublicationSnapshot{
		PublicationID: 42, Version: 7, PublicName: "zt-gpt-realtime",
		SourceModel: "gpt-realtime-source", AllowedGroups: []string{"default"}, AuthorizedGroup: "default",
		InputPricePerMillion: 10, OutputPricePerMillion: 30,
		CacheReadRatio: 0.1, CacheCreationRatio: 1.1,
		CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 3, AudioRatio: 4, AudioCompletionRatio: 5,
	}
	relayInfo := &relaycommon.RelayInfo{
		UserId: user.Id, OriginModelName: snapshot.PublicName,
		UserGroup: "default", UsingGroup: "default", StartTime: time.Now(),
		ZTAPIPublicationSnapshot: snapshot,
		ChannelMeta:              &relaycommon.ChannelMeta{ChannelId: channel.Id},
	}

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.BatchUpdateEnabled = previousBatchUpdate
		common.LogConsumeEnabled = previousLogConsume
		common.DataExportEnabled = previousDataExport
	})
	return db, ctx, relayInfo
}
