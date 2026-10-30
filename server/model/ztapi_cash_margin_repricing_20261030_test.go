package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCashMargin20261030ScopedApplyAndOriginalPriceRollback(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	var selected []ZTAPICashMarginSelection20261030
	var originals []ZTAPIModelPublicationSnapshot
	var bases []ZTAPIModelPriceSource
	for _, name := range []string{"Gemini 3 Flash Preview", "Gemini 3.1 Pro Preview", "Kimi K2.7 Code"} {
		c, p, s := cashMarginFixture(t, db, name)
		selected = append(selected, ZTAPICashMarginSelection20261030{ModelID: c.ID, Version: c.Version, SnapshotID: p.ID})
		originals, bases = append(originals, p), append(bases, s)
	}
	other := ZTAPIModelConfig{SourceModel: "unrelated-model", Version: 1, EnabledGroups: `[]`}
	require.NoError(t, db.Create(&other).Error)
	preview, err := PreviewZTAPICashMargin20261030(selected, false)
	require.NoError(t, err)
	require.Len(t, preview, 3)
	require.Equal(t, "enterprise", preview[1].ProcurementResourceType)
	require.Equal(t, "0.30", preview[1].TargetMargin)
	require.Equal(t, []int{2}, preview[1].AllowedChannels)
	var sourceCount int64
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&sourceCount).Error)
	require.Equal(t, int64(6), sourceCount, "preview must not persist anything")
	result, err := ApplyZTAPICashMargin20261030(9, selected, false)
	require.NoError(t, err)
	require.Equal(t, 3, result.Imported)
	require.Equal(t, 3, result.Republished)
	second, err := ApplyZTAPICashMargin20261030(9, selected, false)
	require.NoError(t, err)
	require.Equal(t, 3, second.Unchanged, "exact same approved baseline retries are idempotent")
	publications, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Len(t, publications, 3, "the new compatible reader must expose all three derived publications")
	var rollback []ZTAPICashMarginSelection20261030
	for i, s := range selected {
		var c ZTAPIModelConfig
		var p ZTAPIModelPublicationSnapshot
		var source ZTAPIModelPriceSource
		require.NoError(t, db.First(&c, s.ModelID).Error)
		require.True(t, c.Published)
		require.Equal(t, s.Version+1, c.Version)
		require.NoError(t, db.First(&p, c.PublicationSnapshotID).Error)
		require.NoError(t, db.First(&source, p.PriceSourceID).Error)
		require.Equal(t, bases[i].ID, source.CashMarginBaseSourceID)
		require.Equal(t, bases[i].ResourceType, source.ResourceType)
		require.Equal(t, originals[i].AllowedChannelIDs, p.AllowedChannelIDs)
		require.Equal(t, originals[i].VerificationIDs, p.VerificationIDs)
		require.NoError(t, ValidateZTAPICashMarginSource20261030(db, &source))
		checksums, err := ztapiQuotationChecksums(db, []int64{p.ID})
		require.NoError(t, err)
		require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, checksums[p.ID])
		rollback = append(rollback, ZTAPICashMarginSelection20261030{ModelID: c.ID, Version: c.Version, SnapshotID: p.ID, RestoreSnapshotID: originals[i].ID})
		var old ZTAPIModelPriceSource
		require.NoError(t, db.First(&old, bases[i].ID).Error)
		require.Equal(t, bases[i], old)
	}
	restored, err := ApplyZTAPICashMargin20261030(9, rollback, true)
	require.NoError(t, err)
	require.Equal(t, 3, restored.Republished)
	second, err = ApplyZTAPICashMargin20261030(9, rollback, true)
	require.NoError(t, err)
	require.Equal(t, 3, second.Unchanged)
	publications, err = loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Len(t, publications, 3, "restored publications must remain visible without a new-policy reader")
	for i, s := range selected {
		var c ZTAPIModelConfig
		var p ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&c, s.ModelID).Error)
		require.True(t, c.Published)
		require.NoError(t, db.First(&p, c.PublicationSnapshotID).Error)
		require.NotEqual(t, originals[i].PriceSourceID, p.PriceSourceID, "restore creates a new old-policy source for old-binary latest-source compatibility")
		require.Equal(t, originals[i].InputPricePerMillion, p.InputPricePerMillion)
		require.Equal(t, originals[i].OutputPricePerMillion, p.OutputPricePerMillion)
		require.Equal(t, string(ZTAPIPricePolicyQuoteFrozenRetail20261019), p.PricePolicy)
		var latest ZTAPIModelPriceSource
		require.NoError(t, db.Where("model_config_id = ?", c.ID).Order("version DESC, id DESC").First(&latest).Error)
		require.Equal(t, string(ZTAPIPricePolicyQuoteFrozenRetail20261019), latest.PricePolicy,
			"old binaries must see a supported policy even through latest-source admin gates")
		require.Empty(t, ztapiABPriceSourceEvidenceMismatch(&latest, &bases[i]))
		require.Zero(t, latest.CashMarginBaseSourceID)
		var old ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&old, originals[i].ID).Error)
		require.Equal(t, originals[i], old)
	}
	var unchanged ZTAPIModelConfig
	require.NoError(t, db.First(&unchanged, other.ID).Error)
	require.Equal(t, other, unchanged)
}

func TestCashMargin20261030BatchRejectsStaleVersionAndRouteChanges(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	c, p, _ := cashMarginFixture(t, db, "Gemini 3 Flash Preview")
	c2, p2, _ := cashMarginFixture(t, db, "Gemini 3.1 Pro Preview")
	valid := ZTAPICashMarginSelection20261030{ModelID: c.ID, Version: c.Version, SnapshotID: p.ID}
	stale := ZTAPICashMarginSelection20261030{ModelID: c2.ID, Version: c2.Version + 1, SnapshotID: p2.ID}
	_, err := ApplyZTAPICashMargin20261030(9, []ZTAPICashMarginSelection20261030{valid, stale}, false)
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	var after ZTAPIModelConfig
	require.NoError(t, db.First(&after, c.ID).Error)
	require.Equal(t, p.ID, after.PublicationSnapshotID, "earlier models in the batch must also roll back")
	var count int64
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&count).Error)
	require.Zero(t, count)
	_, err = ApplyZTAPICashMargin20261030(9, []ZTAPICashMarginSelection20261030{valid, valid}, false)
	require.Error(t, err)
	require.NoError(t, db.Exec("UPDATE ztapi_model_publication_snapshots SET allowed_channel_ids = ? WHERE id = ?", "[1,2]", p.ID).Error)
	_, err = PreviewZTAPICashMargin20261030([]ZTAPICashMarginSelection20261030{valid}, false)
	require.Error(t, err, "preview must reject broadened or incorrect actual routes")
}

func TestCashMargin20261030RejectsUnapprovedModelAndPreservesPausedState(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	other := seedZTAPIPublicCatalogRecord(t, db, "glm-5.2", "zt-glm-5.2", ZTAPIProviderGLM, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	_, err := ApplyZTAPICashMargin20261030(9, []ZTAPICashMarginSelection20261030{{ModelID: other.ID, Version: other.Version, SnapshotID: other.PublicationSnapshotID}}, false)
	require.Error(t, err)
	c, p, _ := cashMarginFixture(t, db, "Gemini 3 Flash Preview")
	require.NoError(t, db.Model(&c).Updates(map[string]any{"published": false, "publication_snapshot_id": 0}).Error)
	_, err = ApplyZTAPICashMargin20261030(9, []ZTAPICashMarginSelection20261030{{ModelID: c.ID, Version: c.Version, SnapshotID: p.ID}}, false)
	require.Error(t, err)
	require.NoError(t, db.First(&c, c.ID).Error)
	require.False(t, c.Published)
	require.Zero(t, c.PublicationSnapshotID)
}

func TestCashMargin20261030DowngradeBlockedUntilRestoredAndPendingDrained(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}, &ZTAPIRequestSettlement{}))
	c, p, _ := cashMarginFixture(t, db, "Gemini 3 Flash Preview")
	selected := []ZTAPICashMarginSelection20261030{{ModelID: c.ID, Version: c.Version, SnapshotID: p.ID}}
	_, err := ApplyZTAPICashMargin20261030(9, selected, false)
	require.NoError(t, err)
	status, err := CheckZTAPICashMarginDowngrade20261030()
	require.NoError(t, err)
	require.False(t, status.CanDowngrade)
	require.Len(t, status.ActiveModels, 1)
	request := ZTAPIRequestSettlement{OperationID: "cash-pending", RequestID: "cash-pending-request", PublicModel: c.PublicNameValue(),
		Status: ZTAPISettlementPending, PriceSnapshotJSON: `{"PricePolicy":"cash_enterprise_20_20261030"}`, UsageJSON: `{}`, ChargeDimensionsJSON: `[]`, MissingDimensionsJSON: `[]`}
	require.NoError(t, db.Create(&request).Error)
	require.NoError(t, db.First(&request, request.ID).Error)
	require.NoError(t, db.First(&c, c.ID).Error)
	_, err = ApplyZTAPICashMargin20261030(9, []ZTAPICashMarginSelection20261030{{ModelID: c.ID, Version: c.Version, SnapshotID: c.PublicationSnapshotID, RestoreSnapshotID: p.ID}}, true)
	require.NoError(t, err)
	status, err = CheckZTAPICashMarginDowngrade20261030()
	require.NoError(t, err)
	require.False(t, status.CanDowngrade, "restoring catalog prices must not erase frozen pending-policy obligations")
	require.Empty(t, status.ActiveModels)
	require.Equal(t, []uint{request.ID}, status.UnsettledRequestIDs)
	var unchanged ZTAPIRequestSettlement
	require.NoError(t, db.First(&unchanged, request.ID).Error)
	require.Equal(t, request, unchanged, "restore and downgrade checks must not rewrite settlement evidence")
	require.NoError(t, db.Model(&request).Update("status", ZTAPISettlementSettled).Error)
	status, err = CheckZTAPICashMarginDowngrade20261030()
	require.NoError(t, err)
	require.True(t, status.CanDowngrade)
	require.NoError(t, db.Model(&request).Updates(map[string]any{"status": ZTAPISettlementReserved, "price_snapshot_json": "{"}).Error)
	status, err = CheckZTAPICashMarginDowngrade20261030()
	require.NoError(t, err)
	require.False(t, status.CanDowngrade, "ambiguous in-flight evidence must fail closed")
}

func TestCashMargin20261030DowngradeSourceIdentityDoesNotLoseIntegerPrecision(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIRequestSettlement{}))
	_, _, base := cashMarginFixture(t, db, "Gemini 3 Flash Preview")
	target, err := BuildZTAPICashMarginSource20261030(db, &base, 9)
	require.NoError(t, err)
	target.ID, target.Version = 9007199254740993, 3
	require.NoError(t, db.Create(&target).Error)
	row := ZTAPIRequestSettlement{OperationID: "large-source", RequestID: "large-source-request", PublicModel: "zt-gemini-3-flash-preview",
		Status: ZTAPISettlementReserved, PriceSnapshotJSON: `{"PricePolicy":"enterprise_20_margin","PriceSourceID":9007199254740993}`, UsageJSON: `{}`, ChargeDimensionsJSON: `[]`, MissingDimensionsJSON: `[]`}
	require.NoError(t, db.Create(&row).Error)
	status, err := CheckZTAPICashMarginDowngrade20261030()
	require.NoError(t, err)
	require.False(t, status.CanDowngrade)
	require.Equal(t, []uint{row.ID}, status.UnsettledRequestIDs)
}
