package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIQuotationIdentityExactMatch(t *testing.T) {
	entries, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	require.Len(t, entries, 46)
	mapped, pending, rows := 0, 0, 0
	for _, entry := range entries {
		rows += len(entry.QuoteRows)
		if entry.Status == "mapping_pending" {
			pending++
			require.Empty(t, entry.SourceModel)
			require.Empty(t, entry.PublicName)
			require.Empty(t, entry.Protocol)
			require.ErrorIs(t, ValidateZTAPIQuotationIdentity(entry.Label, "zt-"+entry.Label, ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, ZTAPIQuotationSHA256), ErrZTAPIQuotationMappingPending)
			continue
		}
		mapped++
		require.NoError(t, ValidateZTAPIQuotationIdentity(entry.SourceModel, entry.PublicName, entry.Protocol, entry.ProviderFamily, ZTAPIQuotationSHA256))
	}
	require.Equal(t, 43, mapped)
	require.Equal(t, 3, pending)
	require.Equal(t, 59, rows)
	require.NoError(t, ValidateZTAPIQuotationIdentity("gpt-image-2", "zt-gp-image-2", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, ZTAPIQuotationSHA256))
	require.NoError(t, ValidateZTAPIQuotationIdentity("gemini-2.5-flash-image", "zt-gemini-2.5-flash-image", ZTAPIProtocolOpenAICompatible, ZTAPIProviderGoogle, ZTAPIQuotationSHA256))
	entries[0].SourceModel = "mutated"
	entries[0].QuoteRows[0].Cell = "Z999"
	again, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	require.NotEqual(t, "mutated", again[0].SourceModel)
	require.NotEqual(t, "Z999", again[0].QuoteRows[0].Cell)
}

func TestZTAPIQuotationIdentityRejectsNearMatches(t *testing.T) {
	for _, source := range []string{"gpt-5.5-hc", "GPT-5.5", "gpt-5.5 ", "gemini-3.1-flash-lite", "gpt-4o-mini-2024-07-18", "", "unknown"} {
		require.ErrorIs(t, ValidateZTAPIQuotationIdentity(source, "zt-gpt-5.5", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, ZTAPIQuotationSHA256), ErrZTAPIQuotationModelNotQuoted)
	}
	require.ErrorIs(t, ValidateZTAPIQuotationIdentity("gpt-5.5", "zt-wrong", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, ZTAPIQuotationSHA256), ErrZTAPIQuotationIdentityMismatch)
	require.ErrorIs(t, ValidateZTAPIQuotationIdentity("claude-sonnet-5", "zt-claude-sonnet-5", ZTAPIProtocolAnthropic, ZTAPIProviderAnthropic, ZTAPIQuotationSHA256), ErrZTAPIQuotationIdentityMismatch)
	require.ErrorIs(t, ValidateZTAPIQuotationIdentity("gpt-5.5", "zt-gpt-5.5", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOther, ZTAPIQuotationSHA256), ErrZTAPIQuotationIdentityMismatch)
	for _, hash := range []string{"", strings.Repeat("a", 64), ZTAPIQuotationSHA256 + " "} {
		require.ErrorIs(t, ValidateZTAPIQuotationIdentity("gpt-5.5", "zt-gpt-5.5", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, hash), ErrZTAPIQuotationVersionMismatch)
	}
}

func TestZTAPIQuotationManifestRejectsDuplicates(t *testing.T) {
	for _, mutate := range []func(*ztapiQuotationManifest){
		func(m *ztapiQuotationManifest) { m.Entries[1] = m.Entries[0] },
		func(m *ztapiQuotationManifest) { m.Entries[1].SourceModel = m.Entries[0].SourceModel },
		func(m *ztapiQuotationManifest) { m.Entries[1].PublicName = m.Entries[0].PublicName },
		func(m *ztapiQuotationManifest) { m.Entries[1].QuoteRows = m.Entries[0].QuoteRows },
		func(m *ztapiQuotationManifest) { m.SHA256 = strings.Repeat("a", 64) },
		func(m *ztapiQuotationManifest) { m.Entries[45].SourceModel = "guessed-video-id" },
	} {
		var manifest ztapiQuotationManifest
		require.NoError(t, common.Unmarshal(ztapiQuotationManifestJSON, &manifest))
		mutate(&manifest)
		raw, err := common.Marshal(manifest)
		require.NoError(t, err)
		_, err = parseZTAPIQuotationManifest(raw)
		require.Error(t, err)
	}
}

func TestZTAPIQuotationBlocksUnquotedCompletePublication(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	const unquoted = "deepseek-v4-flash-hc"
	require.NoError(t, f.db.Model(&Ability{}).Where("channel_id = ?", f.channel.Id).Update("model", unquoted).Error)
	require.NoError(t, f.db.Model(&f.channel).Update("models", unquoted).Error)
	require.NoError(t, f.db.Model(&ZTAPIDiscoveredModel{}).Where("snapshot_id = ?", f.snapshot.ID).Update("source_model", unquoted).Error)
	require.NoError(t, f.db.Model(&f.price).Update("source_model", unquoted).Error)
	require.NoError(t, f.db.Model(&f.config).Update("source_model", unquoted).Error)
	f.config.SourceModel = unquoted
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"enterprise_price_basis_missing", "quotation_model_not_quoted"}, blockers)
	next := f.config
	next.Published = true
	_, err = UpdateZTAPIModelConfigAndBilling(&next, next.Version, nil)
	require.Error(t, err)
	var stored ZTAPIModelConfig
	require.NoError(t, f.db.First(&stored, next.ID).Error)
	require.False(t, stored.Published)
	require.Zero(t, stored.PublicationSnapshotID)
}

func TestZTAPIQuotationBlocksLegacyUnquotedSnapshot(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5-hc", "zt-gpt-5.5-hc", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Empty(t, catalog)
	published, err := GetPublishedZTAPIModelConfigs()
	require.NoError(t, err)
	require.Empty(t, published)
	_, err = GetZTAPIRuntimePublication(config.PublicNameValue())
	require.Error(t, err)
	for _, requested := range []string{config.SourceModel, config.PublicNameValue()} {
		_, err = ResolveZTAPIRequestModel(requested, "default")
		require.ErrorIs(t, err, ErrZTAPIModelNotPublic)
	}
	var stored ZTAPIModelConfig
	require.NoError(t, db.First(&stored, config.ID).Error)
	require.True(t, stored.Published, "read-side rejection must preserve historical records")
}

func TestZTAPIQuotationRejectsInvalidLegacyIdentity(t *testing.T) {
	for _, field := range []string{"public_name", "protocol", "provider_family", "checksum"} {
		t.Run(field, func(t *testing.T) {
			db := setupZTAPIPublicCatalogTestDB(t)
			c := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
				[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
			require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", c.ID).Update("source_document_checksum", ZTAPIQuotationSHA256).Error)
			values := map[string]string{"public_name": "zt-guessed", "protocol": ZTAPIProtocolAnthropic, "provider_family": ZTAPIProviderOther}
			if field == "checksum" {
				require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", c.ID).Update("source_document_checksum", strings.Repeat("b", 64)).Error)
				newPrice := validZTAPIPriceSourceForTest()
				newPrice.ModelConfigID, newPrice.SourceModel, newPrice.Version = c.ID, c.SourceModel, 99
				newPrice.SourceDocumentChecksum = ZTAPIQuotationSHA256
				require.NoError(t, db.Create(&newPrice).Error)
			} else {
				// Seed a corrupted historical snapshot solely to exercise read-side rejection.
				require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", c.PublicationSnapshotID).Update(field, values[field]).Error)
				require.NoError(t, db.Model(&c).Update(field, values[field]).Error)
			}
			catalog, err := ListZTAPIPublicCatalog()
			require.NoError(t, err)
			require.Empty(t, catalog)
		})
	}
}

func TestZTAPIQuotationPendingEntriesCannotUseLegacySnapshots(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	entries, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Status != "mapping_pending" {
			continue
		}
		seedZTAPIPublicCatalogRecord(t, db, entry.Label, "zt-"+entry.Label, ZTAPIProviderOther, ZTAPIProtocolOpenAICompatible,
			[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	}
	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Empty(t, catalog)
	var historical int64
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).Count(&historical).Error)
	require.EqualValues(t, 3, historical)
}

func TestZTAPIQuotationPublicationRejectsWrongDocumentVersion(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	require.NoError(t, f.db.Model(&f.price).Update("source_document_checksum", strings.Repeat("a", 64)).Error)
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Equal(t, []string{ErrZTAPIQuotationVersionMismatch.Error()}, blockers)
	next := f.config
	next.Published = true
	_, err = UpdateZTAPIModelConfigAndBilling(&next, next.Version, nil)
	require.Error(t, err)
}

func TestZTAPIQuotationWarmCacheRejectsChangedPriceEvidence(t *testing.T) {
	for _, mutation := range []string{"checksum", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			db := setupZTAPIPublicCatalogTestDB(t)
			c := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
				[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
			before, err := GetZTAPIRuntimePublication(c.PublicNameValue())
			require.NoError(t, err)
			require.Equal(t, c.PublicationSnapshotID, before.SnapshotID)
			if mutation == "checksum" {
				require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", c.ID).
					Update("source_document_checksum", strings.Repeat("b", 64)).Error)
			} else {
				require.NoError(t, db.Where("model_config_id = ?", c.ID).Delete(&ZTAPIModelPriceSource{}).Error)
			}
			// Simulate another writer without a cache invalidation or version bump.
			_, err = GetZTAPIRuntimePublication(c.PublicNameValue())
			require.Error(t, err)
			_, ok := ResolveZTAPIPublicAlias(c.PublicNameValue())
			require.False(t, ok)
			_, err = ResolveZTAPIRequestModel(c.PublicNameValue(), "default")
			require.ErrorIs(t, err, ErrZTAPIModelNotPublic)
			catalog, err := ListZTAPIPublicCatalog()
			require.NoError(t, err)
			require.Empty(t, catalog)
		})
	}
}

func TestZTAPIQuotationPreservesPinnedPublicationAndHealthGate(t *testing.T) {
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, MigrateZTAPIHealth(db))
	c := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", c.ID).Update("source_document_checksum", ZTAPIQuotationSHA256).Error)
	newPrice := validZTAPIPriceSourceForTest()
	newPrice.ModelConfigID, newPrice.SourceModel, newPrice.Version = c.ID, c.SourceModel, 99
	newPrice.SourceDocumentChecksum = strings.Repeat("b", 64)
	require.NoError(t, db.Create(&newPrice).Error)
	p, err := GetZTAPIRuntimePublication(c.PublicNameValue())
	require.NoError(t, err)
	require.Equal(t, c.SourceModel, p.SourceModel)
	require.Equal(t, c.PublicationSnapshotID, p.SnapshotID)
	require.Equal(t, []int{11, 13}, p.AllowedChannelIDs)
	require.Equal(t, []string{"default"}, p.Groups)
	require.Equal(t, 1.6666666667, p.InputPricePerMillion)
	_, err = ResolveZTAPIRequestModel(c.PublicNameValue(), "vip")
	require.ErrorIs(t, err, ErrZTAPIModelGroupForbidden)
	identity, err := ResolveZTAPIRequestIdentity(c.SourceModel, "default")
	require.NoError(t, err)
	require.Equal(t, c.PublicNameValue(), identity.PublicName)
	require.Equal(t, c.SourceModel, identity.SourceModel)
	require.NoError(t, db.Create(&ZTAPIHealthState{ModelID: c.ID, Generation: 1, Open: true}).Error)
	_, err = ResolveZTAPIRequestModel(c.PublicNameValue(), "default")
	require.ErrorIs(t, err, ErrZTAPIHealthCircuitOpen)
	_, err = ResolveZTAPIRequestModel(c.SourceModel, "default")
	require.ErrorIs(t, err, ErrZTAPIHealthCircuitOpen)
	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Empty(t, catalog)
}
