package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPIModelEvidenceControllerTestDB(t *testing.T) (*gorm.DB, model.ZTAPIModelConfig) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-evidence-controller.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(
		&model.ZTAPIModelConfig{}, &model.ZTAPIModelIdentity{},
		&model.ZTAPIModelPriceSource{}, &model.ZTAPIModelVerification{}, &model.ZTAPIAuditEvent{},
		&model.ZTAPICatalogLock{}, &model.Ability{},
	))
	config := model.ZTAPIModelConfig{SourceModel: "zq-cl-op5", EnabledGroups: "[]", Version: 1}
	require.NoError(t, db.Create(&config).Error)
	return db, config
}

func performZTAPIModelEvidenceRequest(
	t *testing.T,
	handler gin.HandlerFunc,
	method string,
	path string,
	modelID int,
	body string,
	operatorID int,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(modelID)}}
	ctx.Set("id", operatorID)
	handler(ctx)
	return recorder
}

func TestZTAPIModelIdentityHandlerMapsExplicitEvidenceWithoutEchoingSecrets(t *testing.T) {
	db, config := setupZTAPIModelEvidenceControllerTestDB(t)
	body := `{
		"version":1,"source_model":"zq-cl-op5","public_name":"zt-gpt-5",
		"protocol":"openai_compatible","provider_family":"openai",
		"source_reference":"supplier-confirmation-2026-08-27",
		"reason":"confirmed mapping","confirm":true,
		"api_key":"SECRET_MUST_NOT_ECHO","upstream_body":"RAW_MUST_NOT_ECHO"
	}`
	recorder := performZTAPIModelEvidenceRequest(
		t, UpdateZTAPIModelIdentity, http.MethodPut,
		fmt.Sprintf("/api/models/ztapi/%d/identity", config.ID), config.ID, body, 9,
	)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"public_name":"zt-gpt-5"`)
	require.Contains(t, recorder.Body.String(), `"source_model":"zq-cl-op5"`)
	require.NotContains(t, recorder.Body.String(), "SECRET_MUST_NOT_ECHO")
	require.NotContains(t, recorder.Body.String(), "RAW_MUST_NOT_ECHO")

	var identity model.ZTAPIModelIdentity
	require.NoError(t, db.First(&identity, "model_config_id = ?", config.ID).Error)
	require.Equal(t, 9, identity.OperatorID)

	stale := performZTAPIModelEvidenceRequest(
		t, UpdateZTAPIModelIdentity, http.MethodPut,
		fmt.Sprintf("/api/models/ztapi/%d/identity", config.ID), config.ID, body, 10,
	)
	require.Equal(t, http.StatusConflict, stale.Code)
}

func TestZTAPIModelPriceSourceHandlersPreviewAndPersistFortyPercentGrossMargin(t *testing.T) {
	_, config := setupZTAPIModelEvidenceControllerTestDB(t)
	checksum := strings.Repeat("b", 64)
	body := fmt.Sprintf(`{
		"version":1,"source_model":"zq-cl-op5","resource_type":"enterprise","price_policy":"enterprise_40_margin",
		"spend_tier":"0-1万元","billing_dimensions":["input_tokens","output_tokens"],
		"currency":"USD","input_per_million":"1.0000000000",
		"output_per_million":"2.0000000000","cache_read_per_million":"0",
		"cache_write_per_million":"0","cache_write_5m_per_million":"0",
		"cache_write_1h_per_million":"0","image_unit_cost":"0",
		"audio_unit_cost":"0","request_unit_cost":"0","cny_per_usd":"0",
		"quotation_effective_at":1787000000,"source_document_checksum":"%s",
		"reason":"supplier quotation","confirm":true,"api_key":"SECRET_MUST_NOT_ECHO"
	}`, checksum)

	preview := performZTAPIModelEvidenceRequest(
		t, PreviewZTAPIModelPriceSource, http.MethodPost,
		fmt.Sprintf("/api/models/ztapi/%d/price-preview", config.ID), config.ID, body, 8,
	)
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Contains(t, preview.Body.String(), `"input_sale_usd_per_million":"1.6666666667"`)
	require.NotContains(t, preview.Body.String(), "SECRET_MUST_NOT_ECHO")

	imported := performZTAPIModelEvidenceRequest(
		t, ImportZTAPIModelPriceSource, http.MethodPost,
		fmt.Sprintf("/api/models/ztapi/%d/price-sources", config.ID), config.ID, body, 8,
	)
	require.Equal(t, http.StatusOK, imported.Code, imported.Body.String())
	require.Contains(t, imported.Body.String(), `"price_source_version":1`)
	require.Contains(t, imported.Body.String(), `"output_sale_usd_per_million":"3.3333333333"`)
	require.NotContains(t, imported.Body.String(), "SECRET_MUST_NOT_ECHO")
}

func TestZTAPIModelEvidenceWritePermissionMatrix(t *testing.T) {
	require.False(t, common.HasAdminPermission(common.RoleSupportUser, common.PermissionModelWrite))
	require.False(t, common.HasAdminPermission(common.RoleFinanceUser, common.PermissionModelWrite))
	require.True(t, common.HasAdminPermission(common.RoleAdminUser, common.PermissionModelWrite))
	require.True(t, common.HasAdminPermission(common.RoleRootUser, common.PermissionModelWrite))
}

func TestZTAPIModelVerificationHandlerReturnsOnlyStructuredEvidence(t *testing.T) {
	_, config := setupZTAPIModelEvidenceControllerTestDB(t)
	previousVerifier := ztapiModelVerifier
	ztapiModelVerifier = func(_ context.Context, channelID int, sourceModel string, operatorID int) (*model.ZTAPIModelVerification, error) {
		require.Equal(t, 17, channelID)
		require.Equal(t, config.SourceModel, sourceModel)
		require.Equal(t, 23, operatorID)
		return &model.ZTAPIModelVerification{
			ModelConfigID: config.ID, ChannelID: channelID, StatusCategory: "verified",
			NonStreamingPassed: true, StreamingRequired: true, StreamingPassed: true,
			UsageReconciled: true, TotalTokens: 4, OperatorID: operatorID,
		}, nil
	}
	t.Cleanup(func() { ztapiModelVerifier = previousVerifier })

	recorder := performZTAPIModelEvidenceRequest(
		t, VerifyZTAPIModel, http.MethodPost,
		fmt.Sprintf("/api/models/ztapi/%d/verify", config.ID), config.ID,
		`{"channel_id":17,"api_key":"SECRET_MUST_NOT_ECHO"}`, 23,
	)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"status_category":"verified"`)
	require.NotContains(t, recorder.Body.String(), "SECRET_MUST_NOT_ECHO")
}

func TestZTAPIModelVerificationHandlerDoesNotEchoUpstreamFailure(t *testing.T) {
	_, config := setupZTAPIModelEvidenceControllerTestDB(t)
	previousVerifier := ztapiModelVerifier
	ztapiModelVerifier = func(context.Context, int, string, int) (*model.ZTAPIModelVerification, error) {
		return &model.ZTAPIModelVerification{
			ModelConfigID: config.ID, ChannelID: 18, StatusCategory: "upstream_response",
		}, errors.New("RAW_UPSTREAM_SECRET_RESPONSE")
	}
	t.Cleanup(func() { ztapiModelVerifier = previousVerifier })

	recorder := performZTAPIModelEvidenceRequest(
		t, VerifyZTAPIModel, http.MethodPost,
		fmt.Sprintf("/api/models/ztapi/%d/verify", config.ID), config.ID,
		`{"channel_id":18}`, 24,
	)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"status_category":"upstream_response"`)
	require.NotContains(t, recorder.Body.String(), "RAW_UPSTREAM_SECRET_RESPONSE")
}
