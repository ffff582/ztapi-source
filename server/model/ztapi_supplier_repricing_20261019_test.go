package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSupplierRetailComparisonUsesFrozenBillingPrecision(t *testing.T) {
	for _, tc := range []struct{ name, dimension, raw, multiplier, billed string }{
		{"glm47", "cache_read", "0.0620555914", "0.9", "0.0558500323"},
		{"glm51", "cache_read", "0.2016806723", "0.9", "0.1815126051"},
		{"geminiFlash", "output_tokens", "3.1237454751", "0.92", "2.8738458371"},
		{"geminiPro", "output_tokens", "12.4949819005", "0.92", "11.4953833485"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := func(price string) string {
				raw, err := common.Marshal([]ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: map[string]string{tc.dimension: price}}})
				require.NoError(t, err)
				return string(raw)
			}
			old := ZTAPIModelPriceSource{SaleMultiplier: tc.multiplier, TokenPriceRulesJSON: rules(tc.raw)}
			target := ZTAPIModelPriceSource{SaleMultiplier: "1", TokenPriceRulesJSON: rules(tc.billed)}
			require.NoError(t, ztapiRepriceDoesNotRaise(&old, &target))
			raised := decimal.RequireFromString(tc.billed).Add(decimal.RequireFromString("0.0000000001"))
			target.TokenPriceRulesJSON = rules(raised.StringFixed(10))
			require.ErrorContains(t, ztapiRepriceDoesNotRaise(&old, &target), "would increase")
		})
	}
}

func supplierRepricingFixture20261019(t *testing.T) (*gorm.DB, ZTAPIModelConfig, ZTAPIModelPublicationSnapshot, ZTAPIModelPriceSource) {
	t.Helper()
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}, &ZTAPIFXPolicy{}))
	config := seedZTAPIPublicCatalogRecord(t, db, "gemini-3.1-pro-preview", "zt-gemini-3.1-pro-preview", "google",
		ZTAPIProtocolOpenAICompatible, []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	// The legacy importer enforces the frozen provider identity as well.
	identities, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, identities)
	require.NoError(t, err)
	for _, identity := range bridge.Mapped {
		if identity.SourceModel == config.SourceModel {
			require.NoError(t, db.Model(&config).Update("provider_family", identity.ProviderFamily).Error)
		}
	}
	_, err = ApplyZTAPIABCommercialPricing(7, quote.WorkbookSHA256, []string{"Gemini 3.1 Pro Preview"})
	require.NoError(t, err)
	require.NoError(t, db.First(&config, config.ID).Error)
	var snapshot ZTAPIModelPublicationSnapshot
	var source ZTAPIModelPriceSource
	require.NoError(t, db.First(&snapshot, config.PublicationSnapshotID).Error)
	require.NoError(t, db.First(&source, snapshot.PriceSourceID).Error)
	return db, config, snapshot, source
}

func TestSupplierRepricing20261019PreservesRetailRoutesAndHistory(t *testing.T) {
	db, config, previous, oldSource := supplierRepricingFixture20261019(t)
	preview, err := PreviewZTAPISupplierPricing20261019()
	require.NoError(t, err)
	require.Len(t, preview.Ready, 1)
	require.Equal(t, config.Version, preview.Ready[0].Version)
	result, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue()}, map[string]uint64{config.PublicNameValue(): config.Version})
	require.NoError(t, err)
	require.Equal(t, 1, result.Republished)
	var after ZTAPIModelConfig
	var snapshot ZTAPIModelPublicationSnapshot
	var source ZTAPIModelPriceSource
	require.NoError(t, db.First(&after, config.ID).Error)
	require.NoError(t, db.First(&snapshot, after.PublicationSnapshotID).Error)
	require.NoError(t, db.First(&source, snapshot.PriceSourceID).Error)
	require.Equal(t, previous.AllowedChannelIDs, snapshot.AllowedChannelIDs)
	require.Equal(t, previous.VerificationIDs, snapshot.VerificationIDs)
	require.Equal(t, previous.InputPricePerMillion, snapshot.InputPricePerMillion)
	require.Equal(t, previous.OutputPricePerMillion, snapshot.OutputPricePerMillion)
	require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, source.SourceDocumentChecksum)
	require.Greater(t, source.QuotationEffectiveAt, oldSource.QuotationEffectiveAt)
	require.NoError(t, ValidateZTAPISupplierPriceSource20261019(db, &source))
	var historical ZTAPIModelPriceSource
	var historicalSnapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&historical, oldSource.ID).Error)
	require.NoError(t, db.First(&historicalSnapshot, previous.ID).Error)
	require.Equal(t, oldSource, historical)
	require.Equal(t, previous, historicalSnapshot)
	checksums, err := ztapiQuotationChecksums(db, []int64{snapshot.ID})
	require.NoError(t, err)
	require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, checksums[snapshot.ID])
	second, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{after.PublicNameValue()}, map[string]uint64{after.PublicNameValue(): after.Version})
	require.NoError(t, err)
	require.Equal(t, 1, second.Unchanged)
}

func TestSupplierRepricing20261019RollsBackAndRejectsStaleApproval(t *testing.T) {
	db, config, previous, _ := supplierRepricingFixture20261019(t)
	_, err := ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue()}, map[string]uint64{config.PublicNameValue(): config.Version - 1})
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	_, err = ApplyZTAPISupplierPricing20261019(7, ZTAPISupplierWorkbook20261019SHA256,
		[]string{config.PublicNameValue(), "zt-unverified-prime"}, map[string]uint64{config.PublicNameValue(): config.Version, "zt-unverified-prime": 1})
	require.Error(t, err)
	var after ZTAPIModelConfig
	require.NoError(t, db.First(&after, config.ID).Error)
	require.Equal(t, previous.ID, after.PublicationSnapshotID)
	var count int64
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Where("action = ?", "model.supplier_quote_20261019_published").Count(&count).Error)
	require.Zero(t, count)
}
