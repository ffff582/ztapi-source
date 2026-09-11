package model

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func validZTAPIPriceSourceForTest() ZTAPIModelPriceSource {
	return ZTAPIModelPriceSource{
		ModelConfigID: 1, SourceModel: "source-model",
		ResourceType: "enterprise", PricePolicy: string(ZTAPIPricePolicyEnterprise40Margin), SpendTier: "0-1万元",
		BillingDimensions: `["input_tokens","output_tokens"]`,
		Currency:          "USD", InputPerMillion: "1.0000000000", OutputPerMillion: "2.0000000000",
		CacheReadPerMillion: "0", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
		QuotationEffectiveAt:   1_787_000_000,
		SourceDocumentChecksum: strings.Repeat("a", 64),
		OperatorID:             7, CreatedAt: 1_787_000_001,
	}
}

func TestCalculateZTAPISalePriceUsesFortyPercentGrossMargin(t *testing.T) {
	got := CalculateZTAPISalePrice(decimal.RequireFromString("3.9000000000"))
	require.Equal(t, "6.5000000000", got.StringFixed(10))
}

func TestBuildZTAPIModelPricePreviewConvertsCNYWithThreePercentFXBuffer(t *testing.T) {
	source := validZTAPIPriceSourceForTest()
	source.Currency = "CNY"
	source.InputPerMillion = "7.2000000000"
	source.OutputPerMillion = "14.4000000000"
	source.CNYPerUSD = "7.2000000000"

	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "1.0300000000", preview.InputCostUSDPerMillion)
	require.Equal(t, "2.0600000000", preview.OutputCostUSDPerMillion)
	require.Equal(t, "1.7166666667", preview.InputSaleUSDPerMillion)
	require.Equal(t, "3.4333333333", preview.OutputSaleUSDPerMillion)
	require.Equal(t, "7.2000000000", preview.CNYPerUSD)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise40Margin), preview.PricePolicy)
}

func TestHighestZTAPICostUsesConservativeFallback(t *testing.T) {
	got, err := HighestZTAPICost(
		decimal.RequireFromString("1.0000000000"),
		decimal.RequireFromString("2.5000000000"),
		decimal.RequireFromString("2.0000000000"),
	)
	require.NoError(t, err)
	require.Equal(t, "2.5000000000", got.StringFixed(10))
}

func TestValidateZTAPIModelPriceSourceRejectsMissingEvidenceAndBilledCost(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ZTAPIModelPriceSource)
	}{
		{name: "missing output cost", mutate: func(source *ZTAPIModelPriceSource) { source.OutputPerMillion = "0" }},
		{name: "missing checksum", mutate: func(source *ZTAPIModelPriceSource) { source.SourceDocumentChecksum = "" }},
		{name: "bad checksum", mutate: func(source *ZTAPIModelPriceSource) { source.SourceDocumentChecksum = "not-a-checksum" }},
		{name: "missing timestamp", mutate: func(source *ZTAPIModelPriceSource) { source.QuotationEffectiveAt = 0 }},
		{name: "missing tier", mutate: func(source *ZTAPIModelPriceSource) { source.SpendTier = "" }},
		{name: "CNY without rate", mutate: func(source *ZTAPIModelPriceSource) { source.Currency = "CNY"; source.CNYPerUSD = "0" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := validZTAPIPriceSourceForTest()
			test.mutate(&source)
			require.Error(t, ValidateZTAPIModelPriceSource(&source))
		})
	}
}

func TestBuildZTAPIModelPricePreviewRoundsDeterministically(t *testing.T) {
	source := validZTAPIPriceSourceForTest()
	source.InputPerMillion = "0.12345678904"
	source.OutputPerMillion = "0.12345678906"

	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "0.1234567890", preview.InputCostUSDPerMillion)
	require.Equal(t, "0.1234567891", preview.OutputCostUSDPerMillion)
	require.Equal(t, "0.2057613150", preview.InputSaleUSDPerMillion)
	require.Equal(t, "0.2057613152", preview.OutputSaleUSDPerMillion)
}

func setupZTAPIModelEvidenceWriteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-evidence-write.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB := DB
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(
		&ZTAPIModelConfig{}, &ZTAPIModelIdentity{}, &ZTAPIModelPriceSource{}, &Ability{},
		&ZTAPIAuditEvent{}, &ZTAPICatalogLock{},
	))
	return db
}

func TestUpdateZTAPIModelIdentityUsesOptimisticVersionAndExplicitMapping(t *testing.T) {
	db := setupZTAPIModelEvidenceWriteTestDB(t)
	config := ZTAPIModelConfig{SourceModel: "zq-cl-op5", EnabledGroups: "[]", Version: 3}
	require.NoError(t, db.Create(&config).Error)

	committed, identity, err := UpdateZTAPIModelIdentity(ZTAPIModelIdentityUpdate{
		ModelConfigID: config.ID, SourceModel: config.SourceModel,
		PublicName: "zt-gpt-5", Protocol: ZTAPIProtocolOpenAICompatible,
		ProviderFamily:  ZTAPIProviderOpenAI,
		SourceReference: "supplier-confirmation-2026-08-27", Reason: "initial mapping",
		ExpectedVersion: 3, OperatorID: 11,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(4), committed.Version)
	require.Equal(t, "zt-gpt-5", committed.PublicNameValue())
	require.Equal(t, ZTAPIProtocolOpenAICompatible, committed.Protocol)
	require.Equal(t, ZTAPIProviderOpenAI, committed.ProviderFamily)
	require.Equal(t, ZTAPIModelFamilyOpenAI, committed.Family)
	require.Equal(t, "supplier-confirmation-2026-08-27", identity.SourceReference)

	_, _, err = UpdateZTAPIModelIdentity(ZTAPIModelIdentityUpdate{
		ModelConfigID: config.ID, SourceModel: config.SourceModel,
		PublicName: "zt-gpt-5-new", Protocol: ZTAPIProtocolOpenAICompatible,
		ProviderFamily: ZTAPIProviderOpenAI, SourceReference: "later",
		Reason: "stale writer", ExpectedVersion: 3, OperatorID: 12,
	})
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
}

func TestUpdateZTAPIModelIdentityMapsGLMToOpenAICompatibleFamily(t *testing.T) {
	db := setupZTAPIModelEvidenceWriteTestDB(t)
	config := ZTAPIModelConfig{SourceModel: "glm-5.2", EnabledGroups: "[]", Version: 1}
	require.NoError(t, db.Create(&config).Error)

	committed, identity, err := UpdateZTAPIModelIdentity(ZTAPIModelIdentityUpdate{
		ModelConfigID: config.ID, SourceModel: config.SourceModel,
		PublicName: "zt-glm-5.2", Protocol: ZTAPIProtocolOpenAICompatible,
		ProviderFamily:  ZTAPIProviderGLM,
		SourceReference: "supplier-model-list", Reason: "map GLM OpenAI-compatible route",
		ExpectedVersion: 1, OperatorID: 11,
	})
	require.NoError(t, err)
	require.Equal(t, ZTAPIProviderGLM, identity.ProviderFamily)
	require.Equal(t, ZTAPIProtocolOpenAICompatible, committed.Protocol)
	require.Equal(t, ZTAPIModelFamilyOpenAI, committed.Family)
}

func TestImportZTAPIModelPriceSourcePersistsEvidenceAndUpdatesTokenSnapshot(t *testing.T) {
	db := setupZTAPIModelEvidenceWriteTestDB(t)
	config := ZTAPIModelConfig{SourceModel: "zq-cl-op5", EnabledGroups: "[]", Version: 2}
	require.NoError(t, db.Create(&config).Error)
	source := validZTAPIPriceSourceForTest()
	source.ModelConfigID = config.ID
	source.SourceModel = config.SourceModel

	committed, persisted, preview, err := ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{
		Source: source, ExpectedVersion: 2, OperatorID: 21, Reason: "supplier quotation import",
	})
	require.NoError(t, err)
	require.Equal(t, uint64(3), committed.Version)
	require.Equal(t, uint64(1), persisted.Version)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise40Margin), persisted.PricePolicy)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise40Margin), preview.PricePolicy)
	require.Equal(t, "1.6666666667", preview.InputSaleUSDPerMillion)
	require.InDelta(t, 1.0, committed.InputCostPerMillion, 0.0000000001)
	require.InDelta(t, 2.0, committed.OutputCostPerMillion, 0.0000000001)
	require.InDelta(t, 1.6666666667, committed.InputPricePerMillion, 0.0000000001)
	require.InDelta(t, 3.3333333333, committed.OutputPricePerMillion, 0.0000000001)

	source.ID = 0
	_, _, _, err = ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{
		Source: source, ExpectedVersion: 2, OperatorID: 21, Reason: "stale import",
	})
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)

	var sourceCount, auditCount int64
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&sourceCount).Error)
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&auditCount).Error)
	require.EqualValues(t, 1, sourceCount)
	require.EqualValues(t, 1, auditCount)
}
