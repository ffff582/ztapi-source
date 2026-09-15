package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestApplyZTAPIABCommercialPricingRebindsImage2WithoutChangingOldSnapshot(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	name := "zt-gp-image-2"
	config := ZTAPIModelConfig{SourceModel: "gpt-image-2", PublicName: &name,
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
		EnabledGroups: `["default"]`, Published: true, Version: 2,
		InputCostPerMillion: 1.65, OutputCostPerMillion: 9.9,
		InputPricePerMillion: 4, OutputPricePerMillion: 24}
	require.NoError(t, db.Create(&config).Error)
	protocol := image2ABProtocol(t)
	protocolRaw, err := json.Marshal(protocol)
	require.NoError(t, err)
	oldPrice, err := canonicalizeZTAPIMediaPriceContract(gpImage2ContractForTest(t))
	require.NoError(t, err)
	source := validZTAPIPriceSourceForTest()
	source.ModelConfigID, source.SourceModel = config.ID, config.SourceModel
	source.ResourceType, source.PricePolicy, source.SpendTier = "pool", string(ZTAPIPricePolicyPoolOfficial80), "B"
	source.InputPerMillion, source.OutputPerMillion = "1.6500000000", "9.9000000000"
	source.SourceDocumentChecksum, source.MediaPriceContractJSON = ZTAPIQuotationSHA256, oldPrice
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&source).Error)
	previous := ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version,
		SourceModel: config.SourceModel, PublicName: name, Protocol: config.Protocol,
		ProviderFamily: config.ProviderFamily, EnabledGroups: config.EnabledGroups,
		AllowedChannelIDs: `[11,13]`, PriceSourceID: source.ID,
		PricePolicy: source.PricePolicy, MediaPriceContractJSON: oldPrice,
		ImageProtocolContractJSON: string(protocolRaw), VerificationIDs: `[]`, IdentityUpdatedAt: 1, CreatedAt: 2,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&previous).Error)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", previous.ID).Error)
	config.PublicationSnapshotID = previous.ID
	_, err = BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	active, err := loadZTAPIActivePublications(db)
	require.NoError(t, err)
	require.Len(t, active, 1)
	checksums, err := ztapiQuotationChecksums(db, []int64{previous.ID})
	require.NoError(t, err)
	require.Equal(t, ZTAPIQuotationSHA256, checksums[previous.ID])
	beforePreview, err := PreviewZTAPIABCommercialPricing()
	require.NoError(t, err)
	imageReady := false
	for _, item := range beforePreview.PricingReady {
		if item.ModelName == "GPT Image 2" {
			imageReady = true
			require.Len(t, item.PricingRules, 3)
		}
	}
	require.True(t, imageReady, "frozen image quote should be reviewable before approval")
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	result, err := ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"GPT Image 2"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Republished)
	var committed ZTAPIModelConfig
	require.NoError(t, db.First(&committed, config.ID).Error)
	require.NotEqual(t, previous.ID, committed.PublicationSnapshotID)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.Equal(t, string(protocolRaw), snapshot.ImageProtocolContractJSON)
	require.Equal(t, previous.VerificationIDs, snapshot.VerificationIDs)
	contract, err := parseZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
	require.NoError(t, err)
	require.Len(t, contract.Rules, 5)
	var old ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&old, previous.ID).Error)
	require.Equal(t, oldPrice, old.MediaPriceContractJSON)
	var publishedSource ZTAPIModelPriceSource
	require.NoError(t, db.First(&publishedSource, snapshot.PriceSourceID).Error)
	require.Equal(t, quote.WorkbookSHA256, publishedSource.SourceDocumentChecksum)
	checksums, err = ztapiQuotationChecksums(db, []int64{snapshot.ID})
	require.NoError(t, err)
	require.Equal(t, quote.WorkbookSHA256, checksums[snapshot.ID])
	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	require.Len(t, catalog[0].PricingRules, 3)
	second, err := ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"GPT Image 2"})
	require.NoError(t, err)
	require.Equal(t, 1, second.Unchanged)
}

func TestApplyZTAPIABCommercialPricingAtomicallyRebindsFrozenPrice(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	config := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	var previous ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&previous, config.PublicationSnapshotID).Error)
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	result, err := ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"GLM 5.2"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Republished)
	var committed ZTAPIModelConfig
	require.NoError(t, db.First(&committed, config.ID).Error)
	require.NotEqual(t, previous.ID, committed.PublicationSnapshotID)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.NotEmpty(t, snapshot.TokenPriceRulesJSON)
	require.Equal(t, previous.VerificationIDs, snapshot.VerificationIDs)
	var old ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&old, previous.ID).Error)
	require.Empty(t, old.TokenPriceRulesJSON)
	active, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	var newSource ZTAPIModelPriceSource
	require.NoError(t, db.First(&newSource, snapshot.PriceSourceID).Error)
	require.NoError(t, validateZTAPIABPriceSource(&newSource))
	checksums, checksumErr := ztapiQuotationChecksums(db, []int64{snapshot.ID})
	require.NoError(t, checksumErr)
	require.Equal(t, quote.WorkbookSHA256, checksums[snapshot.ID])
	require.Len(t, active, 1)
	require.Equal(t, snapshot.ID, active[0].SnapshotID)
	second, err := ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"GLM 5.2"})
	require.NoError(t, err)
	require.Equal(t, 1, second.Unchanged)
	require.Zero(t, second.Republished)
}

func TestApplyZTAPIABCommercialPricingRollsBackOnLateUnsupportedModel(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	first := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	seedZTAPIPublicCatalogRecord(t, db, "deepseek-v4-pro", "zt-deepseek-v4-pro", ZTAPIProviderDeepSeek,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	_, err = ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"GLM 5.2", "DeepSeek V4 Pro"})
	require.ErrorContains(t, err, "DeepSeek V4 Pro")
	var unchanged ZTAPIModelConfig
	require.NoError(t, db.First(&unchanged, first.ID).Error)
	require.Equal(t, first.PublicationSnapshotID, unchanged.PublicationSnapshotID)
}

func TestLegacyCommercialRepricingPreservesApprovedABPublication(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	config := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	_, err = ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"GLM 5.2"})
	require.NoError(t, err)
	var before ZTAPIModelConfig
	require.NoError(t, db.First(&before, config.ID).Error)
	result, err := ApplyZTAPICommercialPricingV2(7)
	require.NoError(t, err)
	require.Equal(t, 1, result.Unchanged)
	require.Zero(t, result.Republished)
	var after ZTAPIModelConfig
	require.NoError(t, db.First(&after, config.ID).Error)
	require.Equal(t, before.PublicationSnapshotID, after.PublicationSnapshotID)
}

func TestABPublicationRejectsSnapshotRuleDrift(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	config := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	_, err = ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"GLM 5.2"})
	require.NoError(t, err)
	var published ZTAPIModelConfig
	require.NoError(t, db.First(&published, config.ID).Error)
	require.NoError(t, db.Table("ztapi_model_publication_snapshots").Where("id = ?", published.PublicationSnapshotID).
		Update("token_price_rules_json", "[]").Error)
	checksums, err := ztapiQuotationChecksums(db, []int64{published.PublicationSnapshotID})
	require.NoError(t, err)
	require.NotContains(t, checksums, published.PublicationSnapshotID)
}

func TestPreviewZTAPIABCommercialPricingSeparatesReadyAndBlocked(t *testing.T) {
	preview, err := PreviewZTAPIABCommercialPricing()
	require.NoError(t, err)
	require.Equal(t, 50, preview.QuotedModels)
	require.Len(t, preview.PricingReady, 35)
	require.Len(t, preview.Blocked, 15)
	for _, item := range preview.PricingReady {
		if item.ModelName == "GPT 5.6 Sol" {
			require.Equal(t, "A", item.QuotationGrade)
			require.Len(t, item.TokenPriceRules, 2)
			require.Equal(t, "3.9000000000", item.TokenPriceRules[0].SaleUSD[ZTAPIBillingDimensionInputTokens])
			return
		}
	}
	t.Fatal("GPT 5.6 Sol omitted from quote preview")
}
