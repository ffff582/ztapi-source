package controller

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetZTAPIMediaContractProjectsEvidenceWithoutRawContractsOrSecrets(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "media-contract.db")), &gorm.Config{})
	require.NoError(t, err)
	previous := model.DB
	model.DB = db
	t.Cleanup(func() {
		model.DB = previous
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})
	require.NoError(t, db.AutoMigrate(
		&model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelVerification{},
		&model.ZTAPIModelPublicationSnapshot{}, &model.ZTAPIRequestSettlement{},
	))

	alias := "zt-gp-image-2"
	config := model.ZTAPIModelConfig{
		SourceModel: "gp-image-2", PublicName: &alias, Protocol: model.ZTAPIProtocolOpenAICompatible,
		ProviderFamily: model.ZTAPIProviderOpenAI, EnabledGroups: `["default"]`, Published: true, Version: 7,
	}
	require.NoError(t, db.Create(&config).Error)
	listProjection, projectionErr := buildZTAPIModelProjection(&config)
	require.NoError(t, projectionErr)
	require.Equal(t, model.ZTAPIModalityImage, listProjection.Modality)
	price := model.ZTAPIModelPriceSource{
		ID: 81, ModelConfigID: config.ID, SourceModel: config.SourceModel,
		ResourceType: "enterprise", PricePolicy: string(model.ZTAPIPricePolicyEnterprise40Margin),
		SpendTier: "quoted", Currency: "USD", QuotationEffectiveAt: 1787000000,
		SourceDocumentChecksum: strings.Repeat("a", 64), Version: 3, CreatedAt: 1787000000,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&price).Error)
	protocol := `{"version":1,"provider_model":"gp-image-2","endpoint_type":"images_generation"}`
	snapshot := model.ZTAPIModelPublicationSnapshot{
		ID: 91, ModelConfigID: config.ID, ModelVersion: config.Version,
		PublicName: alias, Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		PriceSourceID: price.ID, PricePolicy: price.PricePolicy,
		ImageProtocolContractJSON: protocol, CreatedAt: 1787000100,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&snapshot).Error)
	require.NoError(t, db.Model(&config).Updates(map[string]any{"publication_snapshot_id": snapshot.ID}).Error)
	for index, status := range []string{model.ZTAPISettlementPending, model.ZTAPISettlementPending, model.ZTAPISettlementSettled} {
		row := model.ZTAPIRequestSettlement{OperationID: fmt.Sprintf("op-%d", index), RequestID: fmt.Sprintf("req-%d", index), UserID: 1, TokenID: 1, PublicModel: alias, PriceSnapshotJSON: `{}`, Status: status}
		require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&row).Error)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/models/ztapi/%d/media-contract", config.ID), nil)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(config.ID)}}
	ctx.Set("id", 8)
	GetZTAPIMediaContract(ctx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(protocol)))
	body := recorder.Body.String()
	for _, expected := range []string{
		`"modality":"image"`, `"quotation_sheet":"国外模型"`, `"quotation_cell":"C56"`,
		`"price_policy":"enterprise_40_margin"`, `"frozen_pricing_version":"ztapi-snapshot-91"`,
		`"price_source_version":3`, `"pending_reconciliation_count":2`, `"protocol_evidence_sha256":"` + digest + `"`,
	} {
		require.Contains(t, body, expected)
	}
	require.NotContains(t, body, "image_protocol_contract")
	require.NotContains(t, body, "media_price_contract")
	require.NotContains(t, body, "allowed_channel_ids")
}
