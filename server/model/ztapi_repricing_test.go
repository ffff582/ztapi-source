package model

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBuildZTAPICommercialPriceSourceV2UsesQuotedPoolCostAndOfficialEightyPercent(t *testing.T) {
	current := validZTAPIPriceSourceForTest()
	current.SourceModel = "gpt-5.6-sol"
	current.BillingDimensions = `["input_tokens","output_tokens"]`

	next, preview, err := buildZTAPICommercialPriceSourceV2(current)
	require.NoError(t, err)
	require.Equal(t, "pool", next.ResourceType)
	require.Equal(t, string(ZTAPIPricePolicyPoolOfficial80), next.PricePolicy)
	require.Equal(t, "3.3000000000", next.InputPerMillion)
	require.Equal(t, "14.8500000000", next.OutputPerMillion)
	require.Equal(t, "8.0000000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "36.0000000000", preview.OutputSaleUSDPerMillion)
}

func TestBuildZTAPICommercialPriceSourceV2UsesTwentyPercentMarginWithoutPool(t *testing.T) {
	current := validZTAPIPriceSourceForTest()
	current.SourceModel = "glm-5.2"
	current.Currency = "CNY"
	current.CNYPerUSD = "7.1"
	current.InputPerMillion = "6"
	current.OutputPerMillion = "21"

	next, preview, err := buildZTAPICommercialPriceSourceV2(current)
	require.NoError(t, err)
	require.Equal(t, "enterprise", next.ResourceType)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise20Margin), next.PricePolicy)
	wantInputCost, err := ConvertZTAPICNYCostToUSD(decimal.NewFromInt(6), decimal.RequireFromString("7.1"))
	require.NoError(t, err)
	wantInputSale, err := CalculateZTAPISalePriceForPolicy(wantInputCost, ZTAPIPricePolicyEnterprise20Margin)
	require.NoError(t, err)
	require.Equal(t, wantInputSale.StringFixed(10), preview.InputSaleUSDPerMillion)
}

func TestBuildZTAPICommercialPriceSourceV2UsesPoolImageContract(t *testing.T) {
	current := validZTAPIPriceSourceForTest()
	current.SourceModel = "gpt-image-2"
	current.MediaPriceContractJSON = strings.Replace(
		mustCanonicalZTAPIMediaPriceContract(t, gpImage2ContractForTest(t)),
		`"sale_usd":{"text_input":"4.00"}`, `"sale_usd":{"text_input":"6.50"}`, 1,
	)

	next, preview, err := buildZTAPICommercialPriceSourceV2(current)
	require.NoError(t, err)
	require.Equal(t, "pool", next.ResourceType)
	require.Equal(t, string(ZTAPIPricePolicyPoolOfficial80), next.PricePolicy)
	require.Equal(t, "1.6500000000", next.InputPerMillion)
	require.Equal(t, "9.9000000000", next.OutputPerMillion)
	require.Equal(t, "4.0000000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "24.0000000000", preview.OutputSaleUSDPerMillion)
	require.Equal(t, mustCanonicalZTAPIMediaPriceContract(t, gpImage2ContractForTest(t)), next.MediaPriceContractJSON)
}

func TestBuildZTAPICommercialPriceSourceV2UsesEnterpriseImageContract(t *testing.T) {
	current := validZTAPIPriceSourceForTest()
	current.SourceModel = "gemini-2.5-flash-image"
	current.InputPerMillion = "0.246"
	current.OutputPerMillion = "2.05"
	current.MediaPriceContractJSON = strings.Replace(
		mustCanonicalZTAPIMediaPriceContract(t, geminiImageContractForTest(t)),
		`"sale_usd":{"input_tokens":"0.3075","output_tokens":"2.5625"}`,
		`"sale_usd":{"input_tokens":"0.41","output_tokens":"3.4166666667"}`, 1,
	)

	next, preview, err := buildZTAPICommercialPriceSourceV2(current)
	require.NoError(t, err)
	require.Equal(t, "enterprise", next.ResourceType)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise20Margin), next.PricePolicy)
	require.Equal(t, "0.3075000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "2.5625000000", preview.OutputSaleUSDPerMillion)
	require.Equal(t, mustCanonicalZTAPIMediaPriceContract(t, geminiImageContractForTest(t)), next.MediaPriceContractJSON)
}

func TestApplyZTAPICommercialPricingV2AtomicallyRebindsExistingPublicationEvidence(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	config := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	var previous ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&previous, config.PublicationSnapshotID).Error)

	result, err := ApplyZTAPICommercialPricingV2(7)
	require.NoError(t, err)
	require.Equal(t, 1, result.Imported)
	require.Equal(t, 1, result.Republished)

	var committed ZTAPIModelConfig
	require.NoError(t, db.First(&committed, config.ID).Error)
	require.NotEqual(t, previous.ID, committed.PublicationSnapshotID)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise20Margin), snapshot.PricePolicy)
	require.Equal(t, previous.AllowedChannelIDs, snapshot.AllowedChannelIDs)
	require.Equal(t, previous.VerificationIDs, snapshot.VerificationIDs)
	require.Equal(t, previous.IdentityUpdatedAt, snapshot.IdentityUpdatedAt)
	active, err := loadZTAPIActivePublications(db)
	require.NoError(t, err)
	require.Len(t, active, 1)
	quoted, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Len(t, quoted, 1)

	second, err := ApplyZTAPICommercialPricingV2(7)
	require.NoError(t, err)
	require.Equal(t, 1, second.Unchanged)
	require.Zero(t, second.Imported)
	require.Zero(t, second.Republished)
}

func TestApplyZTAPICommercialPricingV2RollsBackEveryModelOnLateFailure(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	first := seedZTAPIPublicCatalogRecord(t, db, "glm-5.1", "zt-glm-5.1", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	second := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM,
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	var brokenSnapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&brokenSnapshot, second.PublicationSnapshotID).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&brokenSnapshot).
		Update("verification_ids", "not-json").Error)
	var sourceCountBefore int64
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&sourceCountBefore).Error)

	_, err := ApplyZTAPICommercialPricingV2(7)
	require.ErrorContains(t, err, "published verification evidence is invalid")

	var unchanged ZTAPIModelConfig
	require.NoError(t, db.First(&unchanged, first.ID).Error)
	require.Equal(t, first.Version, unchanged.Version)
	require.Equal(t, first.PublicationSnapshotID, unchanged.PublicationSnapshotID)
	var sourceCountAfter int64
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&sourceCountAfter).Error)
	require.Equal(t, sourceCountBefore, sourceCountAfter)
}
