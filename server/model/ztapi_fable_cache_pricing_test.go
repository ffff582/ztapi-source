package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIFableUniformRetailProjectionPreservesEvidence(t *testing.T) {
	publication := ZTAPIRuntimePublication{SourceModel: "claude-fable-5", PublicName: "zt-claude-fable-5",
		ProviderFamily: ZTAPIProviderAnthropic, Protocol: ZTAPIProtocolOpenAICompatible, PriceSourceID: 7,
		BillingDimensions:   []string{"input_tokens", "output_tokens", "cache_read", "cache_write_5m", "cache_write_1h"},
		SaleUSD:             map[string]string{"input_tokens": "9.4329131653", "output_tokens": "47.1645658263", "cache_read": "0.9432913166", "cache_write_5m": "11.7911414566", "cache_write_1h": "18.8658263306"},
		TokenPriceRulesJSON: `[{"conditions":[],"sale":{"input_tokens":"9.4329131653","output_tokens":"47.1645658263","cache_read":"0.9432913166","cache_write_5m":"11.7911414566","cache_write_1h":"18.8658263306"}}]`}
	beforeRules := publication.TokenPriceRulesJSON
	beforePrices := copyZTAPIStringMap(publication.SaleUSD)
	require.NoError(t, applyZTAPICacheWritePricingMode(&publication, ZTAPIFableUniformCacheWriteMode))
	require.Equal(t, []string{"input_tokens", "output_tokens", "cache_read", "cache_write"}, publication.BillingDimensions)
	require.Equal(t, "18.86582633", publication.SaleUSD["cache_write"])
	require.Equal(t, beforePrices["input_tokens"], publication.SaleUSD["input_tokens"])
	require.Contains(t, beforeRules, "cache_write_5m")
	require.Contains(t, beforePrices, "cache_write_1h")
	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{publication})
	require.Len(t, items, 1)
	require.Len(t, items[0].TokenPriceRules, 1)
	require.Equal(t, "18.86582633", items[0].TokenPriceRules[0].SaleUSD["cache_write"])
	require.NotContains(t, items[0].TokenPriceRules[0].SaleUSD, "cache_write_5m")
	require.NotContains(t, items[0].TokenPriceRules[0].SaleUSD, "cache_write_1h")
}

func TestZTAPIFableUniformPublicationIsVersionedAndInherited(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}))
	config := seedZTAPIPublicCatalogRecord(t, db, "claude-fable-5", "zt-claude-fable-5", ZTAPIProviderAnthropic, ZTAPIProtocolOpenAICompatible,
		[]string{"input_tokens", "output_tokens"})
	var previous ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&previous, config.PublicationSnapshotID).Error)
	oldJSON, err := common.Marshal(previous)
	require.NoError(t, err)
	snapshot, err := PublishZTAPIFableUniformCacheWrite(7, config.Version)
	require.NoError(t, err)
	require.NotEqual(t, previous.ID, snapshot.ID)
	require.Equal(t, config.Version+1, snapshot.ModelVersion)
	require.Equal(t, previous.PriceSourceID, snapshot.PriceSourceID)
	require.Equal(t, previous.AllowedChannelIDs, snapshot.AllowedChannelIDs)
	require.Equal(t, previous.InputPricePerMillion, snapshot.InputPricePerMillion)
	require.Equal(t, ZTAPIFableUniformCacheWriteMode, snapshot.CacheWritePricingMode)
	var old ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&old, previous.ID).Error)
	afterJSON, err := common.Marshal(old)
	require.NoError(t, err)
	require.Equal(t, string(oldJSON), string(afterJSON), "old publication is byte-identical")
	require.ErrorIs(t, func() error { _, err := PublishZTAPIFableUniformCacheWrite(7, config.Version); return err }(), ErrZTAPIModelVersionConflict)
	again, err := PublishZTAPIFableUniformCacheWrite(7, snapshot.ModelVersion)
	require.NoError(t, err)
	require.Equal(t, snapshot.ID, again.ID)
	var events int64
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&events).Error)
	require.Equal(t, int64(1), events)
	require.NoError(t, db.First(&config, config.ID).Error)
	next, err := createZTAPIModelPublicationSnapshotTx(db, &config, config.Version+1, ztapiPublicationEvidence{
		PriceSourceID: snapshot.PriceSourceID, AllowedChannelIDs: snapshot.ChannelIDs(), VerificationIDs: []int64{}})
	require.NoError(t, err)
	require.Equal(t, ZTAPIFableUniformCacheWriteMode, next.CacheWritePricingMode)
	require.Error(t, db.Model(snapshot).Update("cache_write_pricing_mode", "").Error)
	runtime, err := loadZTAPIActivePublications(db)
	require.NoError(t, err)
	require.Len(t, runtime, 1)
	require.Equal(t, "18.86582633", runtime[0].SaleUSD["cache_write"])
	// The real PUT builds a config without PublicationSnapshotID. A model
	// that was unpublished also has no active pointer; neither may lose price.
	for _, published := range []bool{true, false} {
		config.PublicationSnapshotID = 0
		config.Published = published
		next, err := createZTAPIModelPublicationSnapshotTx(db, &config, config.Version+2, ztapiPublicationEvidence{
			PriceSourceID: snapshot.PriceSourceID, AllowedChannelIDs: snapshot.ChannelIDs(), VerificationIDs: []int64{}})
		require.NoError(t, err)
		require.Equal(t, ZTAPIFableUniformCacheWriteMode, next.CacheWritePricingMode)
	}
}

func TestZTAPIFableUniformPublicationRollsBackWithoutAudit(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "claude-fable-5", "zt-claude-fable-5", ZTAPIProviderAnthropic, ZTAPIProtocolOpenAICompatible, []string{"input_tokens", "output_tokens"})
	_, err := PublishZTAPIFableUniformCacheWrite(7, config.Version)
	require.Error(t, err, "missing audit table must roll back publication")
	var current ZTAPIModelConfig
	require.NoError(t, db.First(&current, config.ID).Error)
	require.Equal(t, config.Version, current.Version)
	require.Equal(t, config.PublicationSnapshotID, current.PublicationSnapshotID)
	var snapshots int64
	require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&snapshots).Error)
	require.Equal(t, int64(1), snapshots)
}

func TestZTAPIFableUniformModeCannotBeAppliedToOtherModels(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "claude-opus-5", "zt-claude-opus-5", ZTAPIProviderAnthropic, ZTAPIProtocolOpenAICompatible, []string{"input_tokens", "output_tokens"})
	var previous ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&previous, config.PublicationSnapshotID).Error)
	previous.ID = 0
	previous.CacheWritePricingMode = ZTAPIFableUniformCacheWriteMode
	require.Error(t, db.Create(&previous).Error)
	// Read-side validation must fail closed even for an out-of-band DB change.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).
		Where("id = ?", config.PublicationSnapshotID).Update("cache_write_pricing_mode", ZTAPIFableUniformCacheWriteMode).Error)
	active, err := loadZTAPIActivePublications(db)
	require.NoError(t, err)
	require.Empty(t, active)
}
