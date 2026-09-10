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

package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const ztapiUpstreamMasterKeyTestValue = "middleware-test-upstream-master-key-32"

func seedMiddlewarePublicationSnapshot(t *testing.T, db *gorm.DB, publication *model.ZTAPIModelConfig, channelIDs []int) {
	t.Helper()
	price := model.ZTAPIModelPriceSource{
		ModelConfigID: publication.ID, SourceModel: publication.SourceModel,
		ResourceType: "enterprise", SpendTier: "test",
		BillingDimensions: `["input_tokens","output_tokens"]`, Currency: "USD",
		InputPerMillion: "1", OutputPerMillion: "2", CacheReadPerMillion: "0", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0", ImageUnitCost: "0", AudioUnitCost: "0",
		RequestUnitCost: "0", CNYPerUSD: "0", QuotationEffectiveAt: common.GetTimestamp(),
		SourceDocumentChecksum: model.ZTAPIQuotationSHA256, OperatorID: 1, Version: 1, CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&price).Error)
	channels, err := common.Marshal(channelIDs)
	require.NoError(t, err)
	snapshot := model.ZTAPIModelPublicationSnapshot{
		ModelConfigID: publication.ID, ModelVersion: publication.Version,
		SourceModel: publication.SourceModel, PublicName: publication.PublicNameValue(),
		Protocol: publication.Protocol, ProviderFamily: publication.ProviderFamily,
		EnabledGroups: publication.EnabledGroups, AllowedChannelIDs: string(channels), PriceSourceID: price.ID,
		InputPricePerMillion: publication.InputPricePerMillion, OutputPricePerMillion: publication.OutputPricePerMillion,
		CacheReadRatio: publication.CacheReadRatio, CacheCreationRatio: publication.CacheCreationRatio,
		CacheCreation5mRatio: publication.CacheCreation5mRatio, CacheCreation1hRatio: publication.CacheCreation1hRatio,
		ImageRatio: publication.ImageRatio, AudioRatio: publication.AudioRatio,
		AudioCompletionRatio: publication.AudioCompletionRatio,
		VerificationIDs:      `[]`, IdentityUpdatedAt: common.GetTimestamp(), CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&snapshot).Error)
	require.NoError(t, db.Model(publication).Update("publication_snapshot_id", snapshot.ID).Error)
	publication.PublicationSnapshotID = snapshot.ID
}

func TestDistributeEnforcesZTAPIPublishedGroupsBeforeChannelSelection(t *testing.T) {
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", ztapiUpstreamMasterKeyTestValue)
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousUsingSQLite := common.UsingSQLite
	common.MemoryCacheEnabled = true
	common.UsingSQLite = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}))
	model.DB = db
	model.InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.UsingSQLite = previousUsingSQLite
		model.InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})

	priority := int64(10)
	weight := uint(100)
	channel := model.Channel{
		Name:         "ztapi-vip-route",
		Type:         constant.ChannelTypeOpenAI,
		Key:          "test-key",
		Status:       common.ChannelStatusEnabled,
		Models:       "claude-sonnet-4-6",
		Group:        "vip",
		Weight:       &weight,
		Priority:     &priority,
		CreatedTime:  common.GetTimestamp(),
		ZTAPIManaged: true,
		ZTAPIFamily:  model.ZTAPIModelFamilyClaude,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group: "vip", Model: "claude-sonnet-4-6", ChannelId: channel.Id,
		Enabled: true, Priority: &priority, Weight: weight,
	}).Error)
	alias := "zt-claude-sonnet-4.6"
	publication := model.ZTAPIModelConfig{
		SourceModel: "claude-sonnet-4-6", PublicName: &alias, Family: model.ZTAPIModelFamilyClaude,
		Protocol: model.ZTAPIProtocolOpenAICompatible, ProviderFamily: model.ZTAPIProviderAnthropic,
		InputCostPerMillion: 1, OutputCostPerMillion: 2, InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.1, CacheCreationRatio: 1.1, CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 3, AudioRatio: 4, AudioCompletionRatio: 5,
		EnabledGroups: `["vip"]`, Published: true, Version: 1,
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&publication).Error)
	seedMiddlewarePublicationSnapshot(t, db, &publication, []int{channel.Id})
	model.InitChannelCache()
	model.InvalidateZTAPIAliasCache()

	var capturedSnapshot *relaycommon.ZTAPIPublicationSnapshot
	engine := gin.New()
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		group := c.GetHeader("X-Test-Group")
		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		common.SetContextKey(c, constant.ContextKeyUserGroup, group)
		c.Next()
	}, Distribute(), func(c *gin.Context) {
		capturedSnapshot = relaycommon.GetZTAPIPublicationSnapshot(c)
		require.NotNil(t, capturedSnapshot)
		require.NoError(t, db.Model(&model.ZTAPIModelConfig{}).
			Where("id = ?", capturedSnapshot.PublicationID).
			Updates(map[string]any{"published": false, "version": capturedSnapshot.Version + 1}).Error)
		model.InvalidateZTAPIAliasCache()

		relayInfo := relaycommon.GenRelayInfoOpenAI(c, nil)
		require.Equal(t, capturedSnapshot, relayInfo.ZTAPIPublicationSnapshot)
		c.Status(http.StatusNoContent)
	})
	request := func(group string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"zt-claude-sonnet-4.6","messages":[]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Group", group)
		engine.ServeHTTP(recorder, req)
		return recorder
	}

	require.Equal(t, http.StatusForbidden, request("default").Code)
	require.Equal(t, http.StatusNoContent, request("vip").Code)
	require.NotNil(t, capturedSnapshot)
	require.Equal(t, publication.ID, capturedSnapshot.PublicationID)
	require.Equal(t, alias, capturedSnapshot.PublicName)
	require.Equal(t, "claude-sonnet-4-6", capturedSnapshot.SourceModel)
	require.Equal(t, uint64(1), capturedSnapshot.Version)
	require.Positive(t, capturedSnapshot.PriceSourceID)
	require.Equal(t, uint64(1), capturedSnapshot.PriceSourceVersion)
	require.Equal(t, "text", capturedSnapshot.Modality)
	require.Equal(t, []string{"input_tokens", "output_tokens"}, capturedSnapshot.BillingDimensions)
	require.Equal(t, map[string]string{"input_tokens": "1.6666666667", "output_tokens": "3.3333333333"}, capturedSnapshot.SaleUSD)
	require.Equal(t, []string{"vip"}, capturedSnapshot.AllowedGroups)
	require.Equal(t, []int{channel.Id}, capturedSnapshot.AllowedChannelIDs)
	require.Equal(t, "vip", capturedSnapshot.AuthorizedGroup)
	require.Equal(t, 1.6666666667, capturedSnapshot.InputPricePerMillion)
	require.Equal(t, 3.3333333333, capturedSnapshot.OutputPricePerMillion)
	require.Equal(t, 0.1, capturedSnapshot.CacheReadRatio)
	require.Equal(t, 1.1, capturedSnapshot.CacheCreationRatio)
	require.Equal(t, 1.25, capturedSnapshot.CacheCreation5mRatio)
	require.Equal(t, 2.0, capturedSnapshot.CacheCreation1hRatio)
	require.Equal(t, 3.0, capturedSnapshot.ImageRatio)
	require.Equal(t, 4.0, capturedSnapshot.AudioRatio)
	require.Equal(t, 5.0, capturedSnapshot.AudioCompletionRatio)
	require.Equal(t, http.StatusNotFound, request("vip").Code)
}

func TestDistributeSpecificChannelStillEnforcesAliasAndGroupAbility(t *testing.T) {
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", ztapiUpstreamMasterKeyTestValue)
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousUsingSQLite := common.UsingSQLite
	common.MemoryCacheEnabled = true
	common.UsingSQLite = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}))
	model.DB = db
	model.InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.UsingSQLite = previousUsingSQLite
		model.InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})

	priority := int64(10)
	weight := uint(100)
	newChannel := func(name, group string) model.Channel {
		channel := model.Channel{
			Name: name, Type: constant.ChannelTypeOpenAI, Key: "test-key",
			Status: common.ChannelStatusEnabled, Models: "claude-sonnet-4-6", Group: group,
			Weight: &weight, Priority: &priority, CreatedTime: common.GetTimestamp(),
			ZTAPIManaged: true, ZTAPIFamily: model.ZTAPIModelFamilyClaude,
		}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group: group, Model: "claude-sonnet-4-6", ChannelId: channel.Id,
			Enabled: true, Priority: &priority, Weight: weight,
		}).Error)
		return channel
	}
	defaultChannel := newChannel("ztapi-default-route", "default")
	vipChannel := newChannel("ztapi-vip-route", "vip")
	alias := "zt-claude-sonnet-4.6"
	publication := model.ZTAPIModelConfig{
		SourceModel: "claude-sonnet-4-6", PublicName: &alias, Family: model.ZTAPIModelFamilyClaude,
		Protocol: model.ZTAPIProtocolOpenAICompatible, ProviderFamily: model.ZTAPIProviderAnthropic,
		InputCostPerMillion: 1, OutputCostPerMillion: 2, InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.1, CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
		EnabledGroups: `["default","vip"]`, Published: true, Version: 1,
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&publication).Error)
	seedMiddlewarePublicationSnapshot(t, db, &publication, []int{defaultChannel.Id, vipChannel.Id})
	model.InitChannelCache()
	model.InvalidateZTAPIAliasCache()

	engine := gin.New()
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		group := c.GetHeader("X-Test-Group")
		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		common.SetContextKey(c, constant.ContextKeyUserGroup, group)
		common.SetContextKey(c, constant.ContextKeyTokenSpecificChannelId, c.GetHeader("X-Test-Channel"))
		common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, true)
		allowedAlias := c.GetHeader("X-Test-Allowed-Alias")
		common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{allowedAlias: true})
		c.Next()
	}, Distribute(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := func(group string, channelID int, allowedAlias string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"zt-claude-sonnet-4.6","messages":[]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Group", group)
		req.Header.Set("X-Test-Channel", strconv.Itoa(channelID))
		req.Header.Set("X-Test-Allowed-Alias", allowedAlias)
		engine.ServeHTTP(recorder, req)
		return recorder
	}

	require.Equal(t, http.StatusForbidden, request("default", defaultChannel.Id, "zt-other-alias").Code)
	require.Equal(t, http.StatusForbidden, request("vip", defaultChannel.Id, alias).Code)
	require.Equal(t, http.StatusNoContent, request("vip", vipChannel.Id, alias).Code)
}

func TestLoadZTAPIPublicationSnapshotDeepCopiesImageProtocolContract(t *testing.T) {
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", ztapiUpstreamMasterKeyTestValue)
	previousDB := model.DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousUsingSQLite := common.UsingSQLite
	common.MemoryCacheEnabled = true
	common.UsingSQLite = true
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/distributor-image-contract.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}))
	model.DB = db
	model.InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.UsingSQLite = previousUsingSQLite
		model.InvalidateZTAPIAliasCache()
		require.NoError(t, sqlDB.Close())
	})

	alias := "zt-gpt-5.5"
	publication := model.ZTAPIModelConfig{
		SourceModel: "gpt-5.5", PublicName: &alias, Protocol: model.ZTAPIProtocolOpenAICompatible,
		ProviderFamily: model.ZTAPIProviderOpenAI, EnabledGroups: `["default"]`, Published: true, Version: 1,
		InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.1, CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
	}
	require.NoError(t, db.Create(&publication).Error)
	seedMiddlewarePublicationSnapshot(t, db, &publication, []int{17})
	_, contractJSON, err := types.SealZTAPIImageProtocolContract(types.ZTAPIImageProtocolContract{
		Version: 1, ProviderModel: publication.SourceModel, EndpointType: "images_generation", Method: "POST", Path: "/v1/images/generations",
		Capabilities:   types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 1},
		Response:       types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"}},
		Usage:          types.ZTAPIImageUsageContract{UsageField: "usage", Fields: map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"}, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Reservations:   []types.ZTAPIImageReservationAuthority{{Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 1, MaximumDimensions: map[string]string{"input_tokens": "200000", "output_tokens": "4096"}}},
		RequestIDField: "request_id", EvidenceVersion: 1,
	})
	require.NoError(t, err)
	quotation, err := model.ZTAPIQuotationEntries()
	require.NoError(t, err)
	mediaPriceContractJSON := ""
	for _, entry := range quotation {
		for _, row := range entry.QuoteRows {
			if row.MediaPriceContractJSON != "" {
				mediaPriceContractJSON = row.MediaPriceContractJSON
				break
			}
		}
		if mediaPriceContractJSON != "" {
			break
		}
	}
	require.NotEmpty(t, mediaPriceContractJSON)
	// Seed a legacy persisted contract solely to exercise read-side deep-copy behavior.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&model.ZTAPIModelPublicationSnapshot{}).
		Where("id = ?", publication.PublicationSnapshotID).
		Updates(map[string]any{"image_protocol_contract_json": contractJSON, "media_price_contract_json": mediaPriceContractJSON}).Error)
	model.InvalidateZTAPIAliasCache()

	first, err := loadZTAPIPublicationSnapshot(alias, publication.SourceModel, "default")
	require.NoError(t, err)
	require.NotNil(t, first.ImageProtocolContract)
	require.Equal(t, mediaPriceContractJSON, first.MediaPriceContractJSON)
	encodedContract, err := common.Marshal(first.ImageProtocolContract)
	require.NoError(t, err)
	require.Equal(t, contractJSON, string(encodedContract))
	first.ImageProtocolContract.Capabilities.Sizes[0] = "changed"
	first.ImageProtocolContract.Response.ResultFields["url"] = "changed"

	second, err := loadZTAPIPublicationSnapshot(alias, publication.SourceModel, "default")
	require.NoError(t, err)
	require.Equal(t, "1024x1024", second.ImageProtocolContract.Capabilities.Sizes[0])
	require.Equal(t, "url", second.ImageProtocolContract.Response.ResultFields["url"])
	require.Equal(t, first.MediaPriceContractJSON, second.MediaPriceContractJSON)
	encoded, err := common.Marshal(second.ImageProtocolContract)
	require.NoError(t, err)
	require.Equal(t, contractJSON, string(encoded))
}

func TestZTAPIPublicationSnapshotCarriesDeepCopiedVideoProtocolContract(t *testing.T) {
	contract := types.ZTAPIVideoProtocolContract{
		Version: 1, Provider: "aihub", ProviderModel: "provider-video-exact",
		Capabilities: types.ZTAPIVideoCapabilities{Resolutions: []string{"720p"}, DurationSeconds: []int{5}},
		States:       types.ZTAPIVideoStateContract{Accepted: []string{"queued"}},
		Usage:        types.ZTAPIVideoUsageContract{Fields: map[string]string{"input_tokens": "data.usage.input_tokens"}},
		Reservations: []types.ZTAPIVideoReservationAuthority{{Resolution: "720p", DurationSeconds: 5, MaximumDimensions: map[string]string{"input_tokens": "9000"}}},
	}
	publication := model.ZTAPIRuntimePublication{
		ModelConfigID: 9, Version: 3, PublicName: "zt-seedance", SourceModel: contract.ProviderModel,
		Modality: model.ZTAPIModalityVideo, Groups: []string{"default"}, AllowedChannelIDs: []int{7},
		VideoProtocolContract: &contract,
	}
	snapshot := ztapiPublicationSnapshotFromRuntime(publication, "default")
	require.NotNil(t, snapshot.VideoProtocolContract)

	publication.VideoProtocolContract.Capabilities.Resolutions[0] = "changed"
	publication.VideoProtocolContract.Usage.Fields["input_tokens"] = "changed"
	require.Equal(t, "720p", snapshot.VideoProtocolContract.Capabilities.Resolutions[0])
	require.Equal(t, "data.usage.input_tokens", snapshot.VideoProtocolContract.Usage.Fields["input_tokens"])

	clone := snapshot.Clone()
	snapshot.VideoProtocolContract.States.Accepted[0] = "changed"
	require.Equal(t, "queued", clone.VideoProtocolContract.States.Accepted[0])
}
