package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type listModelsResponse struct {
	Success bool               `json:"success"`
	Data    []dto.OpenAIModels `json:"data"`
	Object  string             `json:"object"`
}

func setupModelListControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	initModelListColumnNames(t)

	gin.SetMode(gin.TestMode)
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db

	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Channel{}, &model.Ability{}, &model.Model{}, &model.Vendor{},
		&model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{},
	))
	model.InvalidateZTAPIAliasCache()

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

func initModelListColumnNames(t *testing.T) {
	t.Helper()

	originalIsMasterNode := common.IsMasterNode
	originalSQLitePath := common.SQLitePath
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")
	defer func() {
		common.IsMasterNode = originalIsMasterNode
		common.SQLitePath = originalSQLitePath
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	}()

	common.IsMasterNode = false
	common.SQLitePath = fmt.Sprintf("file:%s_init?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.UsingSQLite = false
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	require.NoError(t, os.Setenv("SQL_DSN", "local"))

	require.NoError(t, model.InitDB())
	if model.DB != nil {
		sqlDB, err := model.DB.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	}
}

func decodeListModelsResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]struct{} {
	t.Helper()

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload listModelsResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, "list", payload.Object)

	ids := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		ids[item.Id] = struct{}{}
	}
	return ids
}

func pricingByModelName(pricings []model.Pricing) map[string]model.Pricing {
	byName := make(map[string]model.Pricing, len(pricings))
	for _, pricing := range pricings {
		byName[pricing.ModelName] = pricing
	}
	return byName
}

func seedPublicModelListSnapshot(t *testing.T, db *gorm.DB, sourceModel, publicName string) {
	t.Helper()
	alias := publicName
	config := model.ZTAPIModelConfig{
		SourceModel: sourceModel, PublicName: &alias,
		Family: model.ZTAPIModelFamilyOpenAI, Protocol: model.ZTAPIProtocolOpenAICompatible,
		ProviderFamily: model.ZTAPIProviderOpenAI, EnabledGroups: `["default"]`,
		Published: true, Version: 2,
	}
	require.NoError(t, db.Create(&config).Error)
	price := model.ZTAPIModelPriceSource{
		ModelConfigID: config.ID, SourceModel: sourceModel,
		ResourceType: "enterprise", SpendTier: "test",
		BillingDimensions: `["input_tokens","output_tokens"]`, Currency: "USD",
		InputPerMillion: "1", OutputPerMillion: "2", CacheReadPerMillion: "0", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0", ImageUnitCost: "0", AudioUnitCost: "0",
		RequestUnitCost: "0", CNYPerUSD: "0", QuotationEffectiveAt: time.Now().Unix(),
		SourceDocumentChecksum: model.ZTAPIQuotationSHA256, OperatorID: 1, Version: 1, CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, db.Create(&price).Error)
	channels, _ := json.Marshal([]int{1})
	snapshot := model.ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version, SourceModel: sourceModel,
		PublicName: publicName, Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		EnabledGroups: config.EnabledGroups, AllowedChannelIDs: string(channels), PriceSourceID: price.ID,
		InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.1, CacheCreationRatio: 1, CacheCreation5mRatio: 1,
		CacheCreation1hRatio: 2, ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
		VerificationIDs: `[]`, IdentityUpdatedAt: time.Now().Unix(), CreatedAt: time.Now().Unix(),
	}
	require.NoError(t, db.Create(&snapshot).Error)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error)
	model.InvalidateZTAPIAliasCache()
}

func TestListModelsUsesSamePublicAliasSetAsPricing(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedPublicModelListSnapshot(t, db, "gpt-5.5", "zt-gpt-5.5")
	require.NoError(t, db.Create(&model.User{
		Id:       1001,
		Username: "model-list-user",
		Password: "password",
		Group:    "default",
		Status:   common.UserStatusEnabled,
	}).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	ctx.Set("id", 1001)

	ListModels(ctx, constant.ChannelTypeOpenAI)

	ids := decodeListModelsResponse(t, recorder)
	require.Contains(t, ids, "zt-gpt-5.5")
	require.NotContains(t, ids, "gpt-5.5")

	pricingByName := pricingByModelName(model.ApplyZTAPIPublicPricing(nil))
	require.Contains(t, pricingByName, "zt-gpt-5.5")
	require.NotContains(t, pricingByName, "gpt-5.5")
}

func TestListModelsTokenLimitCannotExposePrivateSource(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedPublicModelListSnapshot(t, db, "gpt-5.5", "zt-gpt-5.5")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{
		"zt-gpt-5.5": true,
		"gpt-5.5":    true,
	})

	ListModels(ctx, constant.ChannelTypeOpenAI)

	ids := decodeListModelsResponse(t, recorder)
	require.Contains(t, ids, "zt-gpt-5.5")
	require.NotContains(t, ids, "gpt-5.5")
}

func TestAPIKeyModelSelectionUsesPublicCatalogMetadata(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedPublicModelListSnapshot(t, db, "gpt-5.5", "zt-gpt-5.5")
	require.NoError(t, db.Create(&model.User{
		Id: 1002, Username: "key-model-user", Password: "password",
		Group: "default", Status: common.UserStatusEnabled,
	}).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/models", nil)
	ctx.Set("id", 1002)
	GetUserModels(ctx)

	var response struct {
		Success bool                           `json:"success"`
		Data    []string                       `json:"data"`
		Catalog []model.ZTAPIPublicCatalogItem `json:"catalog"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, []string{"zt-gpt-5.5"}, response.Data)
	require.Len(t, response.Catalog, 1)
	require.Equal(t, "OpenAI", response.Catalog[0].ProviderName)
	require.NotContains(t, recorder.Body.String(), `"gpt-5.5"`)
}

func TestListModelsAnthropicReturnsEmptyPageWithoutPanicking(t *testing.T) {
	setupModelListControllerTestDB(t)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")

	ListModels(ctx, constant.ChannelTypeAnthropic)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data    []dto.AnthropicModel `json:"data"`
		FirstID string               `json:"first_id"`
		LastID  string               `json:"last_id"`
		HasMore bool                 `json:"has_more"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Empty(t, response.Data)
	require.Empty(t, response.FirstID)
	require.Empty(t, response.LastID)
	require.False(t, response.HasMore)
}

func TestRetrieveModelUsesSameAuthorizedPublicAliasSetAsList(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedPublicModelListSnapshot(t, db, "gpt-5.5", "zt-gpt-5.5")

	request := func(modelName string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models/"+modelName, nil)
		ctx.Params = gin.Params{{Key: "model", Value: modelName}}
		common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
		RetrieveModel(ctx, constant.ChannelTypeOpenAI)
		return recorder
	}

	publicResponse := request("zt-gpt-5.5")
	require.Equal(t, http.StatusOK, publicResponse.Code)
	require.Contains(t, publicResponse.Body.String(), `"id":"zt-gpt-5.5"`)
	require.Contains(t, publicResponse.Body.String(), `"owned_by":"OpenAI"`)
	require.NotContains(t, publicResponse.Body.String(), `"gpt-5.5"`)

	for _, privateName := range []string{"gpt-5.5", "gpt-4o"} {
		privateResponse := request(privateName)
		require.Equal(t, http.StatusOK, privateResponse.Code)
		require.Contains(t, privateResponse.Body.String(), `"code":"model_not_found"`)
	}
}
