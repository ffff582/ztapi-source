package helper

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestModelPriceHelperTieredUsesPreloadedRequestInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"tiered-test-model":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"tiered-test-model":"param(\"stream\") == true ? tier(\"stream\", p * 3) : tier(\"base\", p * 2)"}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/channel/test/1", nil)
	req.Body = nil
	req.ContentLength = 0
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	ctx.Set("group", "default")

	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-test-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		RequestHeaders:  map[string]string{"Content-Type": "application/json"},
		BillingRequestInput: &billingexpr.RequestInput{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"stream":true}`),
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, 1500, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "stream", info.TieredBillingSnapshot.EstimatedTier)
	require.Equal(t, billing_setting.BillingModeTieredExpr, info.TieredBillingSnapshot.BillingMode)
	require.Equal(t, common.QuotaPerUnit, info.TieredBillingSnapshot.QuotaPerUnit)
}

func TestModelPriceHelperUsesZTAPIPublicationBeforeLegacyBillingSettings(t *testing.T) {
	savedConfig := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedConfig[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedConfig))
	})

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-price.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelConfig{}))
	previousDB := model.DB
	previousUsingSQLite := common.UsingSQLite
	previousModelPrices := ratio_setting.GetModelPriceCopy()
	previousGroupRatios := ratio_setting.GetGroupRatioCopy()
	model.DB = db
	common.UsingSQLite = true
	model.InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		common.UsingSQLite = previousUsingSQLite
		model.InvalidateZTAPIAliasCache()
		encoded, _ := json.Marshal(previousModelPrices)
		_ = ratio_setting.UpdateModelPriceByJSONString(string(encoded))
		groupEncoded, _ := json.Marshal(previousGroupRatios)
		_ = ratio_setting.UpdateGroupRatioByJSONString(string(groupEncoded))
		_ = sqlDB.Close()
	})

	alias := "zt-gpt-runtime"
	publication := model.ZTAPIModelConfig{
		SourceModel:           "gpt-runtime-source",
		PublicName:            &alias,
		Family:                model.ZTAPIModelFamilyOpenAI,
		InputCostPerMillion:   4,
		OutputCostPerMillion:  12,
		InputPricePerMillion:  10,
		OutputPricePerMillion: 30,
		CacheReadRatio:        0.1,
		CacheCreationRatio:    1.25,
		CacheCreation5mRatio:  1.25,
		CacheCreation1hRatio:  2,
		ImageRatio:            1,
		AudioRatio:            1,
		AudioCompletionRatio:  2,
		EnabledGroups:         `["default"]`,
		Published:             true,
		Version:               2,
		CreatedAt:             common.GetTimestamp(),
		UpdatedAt:             common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&publication).Error)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"zt-gpt-runtime":0.01}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":0.25}`))
	model.InvalidateZTAPIAliasCache()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "zt-gpt-runtime",
		UserGroup:       "default",
		UsingGroup:      "default",
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PublicationID:         publication.ID,
			Version:               publication.Version,
			PublicName:            alias,
			SourceModel:           publication.SourceModel,
			AllowedGroups:         []string{"default"},
			AuthorizedGroup:       "default",
			InputPricePerMillion:  10,
			OutputPricePerMillion: 30,
			CacheReadRatio:        0.1,
			CacheCreationRatio:    1.1,
			CacheCreation5mRatio:  1.25,
			CacheCreation1hRatio:  2,
			ImageRatio:            3,
			AudioRatio:            4,
			AudioCompletionRatio:  5,
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.False(t, priceData.UsePrice)
	require.Equal(t, 5.0, priceData.ModelRatio)
	require.Equal(t, 3.0, priceData.CompletionRatio)
	require.Equal(t, 0.1, priceData.CacheRatio)
	require.Equal(t, 1.1, priceData.CacheCreationRatio)
	require.Equal(t, 1.25, priceData.CacheCreation5mRatio)
	require.Equal(t, 2.0, priceData.CacheCreation1hRatio)
	require.Equal(t, 3.0, priceData.ImageRatio)
	require.Equal(t, 4.0, priceData.AudioRatio)
	require.Equal(t, 5.0, priceData.AudioCompletionRatio)
	require.Equal(t, 1.0, priceData.GroupRatioInfo.GroupRatio)

	require.NoError(t, db.Model(&model.ZTAPIModelConfig{}).Where("id = ?", publication.ID).
		Updates(map[string]any{"published": false, "version": 3}).Error)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"zt-gpt-runtime":99}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"zt-gpt-runtime":77}`))
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"zt-gpt-runtime":55}`))
	require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"zt-gpt-runtime":44}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":11}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"zt-gpt-runtime":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"zt-gpt-runtime":"tier(\"changed\", p * 1000)"}`,
	}))
	model.InvalidateZTAPIAliasCache()
	require.NoError(t, sqlDB.Close())

	priceDataAfterMutation, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, priceData, priceDataAfterMutation)
	require.Nil(t, info.TieredBillingSnapshot)
	require.Equal(t, 1.0, HandleGroupRatio(ctx, info).GroupRatio)
}

func TestModelPriceHelperChannelTestAllowsUnpricedUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/test/1", nil)
	ctx.Set("group", "default")

	const modelName = "ztapi-channel-test-unpriced-regression-model"
	_, configured, _ := ratio_setting.GetModelRatio(modelName)
	require.False(t, configured, "regression model must remain absent from legacy pricing")

	info := &relaycommon.RelayInfo{
		OriginModelName: modelName,
		UserGroup:       "default",
		UsingGroup:      "default",
		IsChannelTest:   true,
	}

	priceData, err := ModelPriceHelper(ctx, info, 0, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.False(t, priceData.UsePrice)
}

func TestModelPriceHelperUserRequestStillRejectsUnpricedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("group", "default")

	info := &relaycommon.RelayInfo{
		OriginModelName: "ztapi-user-unpriced-regression-model",
		UserGroup:       "default",
		UsingGroup:      "default",
	}

	_, err := ModelPriceHelper(ctx, info, 0, &types.TokenCountMeta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "价格")
}
