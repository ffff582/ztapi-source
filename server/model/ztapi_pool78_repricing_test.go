package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIPool78RepricingRequiresPoolVerificationAndDropsEnterpriseRoute(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	db := f.db
	require.NoError(t, db.AutoMigrate(&ZTAPIFXPolicy{}, &ZTAPIHealthState{}))
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	alias := "zt-gpt-5.6-sol"
	require.NoError(t, db.Model(&f.config).Updates(map[string]any{
		"source_model": "gpt-5.6-sol", "public_name": alias,
		"provider_family": ZTAPIProviderOpenAI, "version": 2, "published": true,
	}).Error)
	require.NoError(t, db.Model(&f.identity).Updates(map[string]any{
		"public_name": alias, "provider_family": ZTAPIProviderOpenAI,
	}).Error)
	require.NoError(t, db.Model(&ZTAPIDiscoveredModel{}).
		Where("snapshot_id = ?", f.snapshot.ID).Update("source_model", "gpt-5.6-sol").Error)
	require.NoError(t, db.Model(&f.channel).Update("models", "gpt-5.6-sol").Error)
	require.NoError(t, db.Where("channel_id = ?", f.channel.Id).Delete(&Ability{}).Error)
	f.channel.Models = "gpt-5.6-sol"
	require.NoError(t, f.channel.AddAbilities(db))
	oldSource, err := BuildZTAPIABTextPriceSource(quote, "GPT 5.6 Sol", f.config.ID, 1, 1_790_640_000)
	require.NoError(t, err)
	oldSource.Version = 2
	require.NoError(t, db.Create(&oldSource).Error)
	oldPreview, err := BuildZTAPIModelPricePreview(&oldSource)
	require.NoError(t, err)
	f.config.SourceModel, f.config.ProviderFamily, f.config.Version = "gpt-5.6-sol", ZTAPIProviderOpenAI, 2
	f.config.PublicName = &alias
	f.config.Published = true
	require.NoError(t, applyZTAPICommercialPreview(&f.config, oldPreview))
	require.NoError(t, db.Model(&f.config).Updates(map[string]any{
		"input_cost_per_million":   f.config.InputCostPerMillion,
		"output_cost_per_million":  f.config.OutputCostPerMillion,
		"input_price_per_million":  f.config.InputPricePerMillion,
		"output_price_per_million": f.config.OutputPricePerMillion,
		"cache_read_ratio":         f.config.CacheReadRatio,
		"cache_creation_ratio":     f.config.CacheCreationRatio,
	}).Error)
	verificationIDs, _ := json.Marshal([]int64{f.verification.ID})
	previous := ZTAPIModelPublicationSnapshot{
		ModelConfigID: f.config.ID, ModelVersion: 2,
		SourceModel: f.config.SourceModel, PublicName: alias,
		Protocol: f.config.Protocol, ProviderFamily: f.config.ProviderFamily,
		EnabledGroups: f.config.EnabledGroups, AllowedChannelIDs: `[1]`,
		PriceSourceID: oldSource.ID, VerificationIDs: string(verificationIDs),
		PricePolicy: oldSource.PricePolicy, TokenPriceRulesJSON: oldSource.TokenPriceRulesJSON,
		InputPricePerMillion: f.config.InputPricePerMillion,
		OutputPricePerMillion: f.config.OutputPricePerMillion,
		IdentityUpdatedAt: f.identity.UpdatedAt, CreatedAt: common.GetTimestamp(),
		CacheReadRatio:       f.config.CacheReadRatio,
		CacheCreationRatio:   f.config.CacheCreationRatio,
		CacheCreation5mRatio: f.config.CacheCreation5mRatio,
		CacheCreation1hRatio: f.config.CacheCreation1hRatio,
		ImageRatio: f.config.ImageRatio, AudioRatio: f.config.AudioRatio,
		AudioCompletionRatio: f.config.AudioCompletionRatio,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&previous).Error)
	require.NoError(t, db.Model(&f.config).Update("publication_snapshot_id", previous.ID).Error)
	baseURL := "https://ai.yunxinapi.com/hub"
	pool := Channel{
		Name: "pool", Type: constant.ChannelTypeOpenAI, Key: "synthetic-pool-key",
		BaseURL: &baseURL, Status: common.ChannelStatusEnabled, Models: "gpt-5.6-sol",
		Group: "default", ZTAPIManaged: true,
	}
	require.NoError(t, db.Create(&pool).Error)
	require.Equal(t, 2, pool.Id)
	require.NoError(t, pool.AddAbilities(db))
	discovery := ZTAPIDiscoverySnapshot{
		ChannelID: pool.Id, ModelListHash: strings.Repeat("b", 64),
		ModelCount: 1, FetchedAt: time.Now().UTC().Unix(),
	}
	require.NoError(t, db.Create(&discovery).Error)
	require.NoError(t, db.Create(&ZTAPIDiscoveredModel{
		SnapshotID: discovery.ID, ChannelID: pool.Id, SourceModel: f.config.SourceModel,
	}).Error)
	active, activeErr := loadZTAPIActivePublications(db)
	require.NoError(t, activeErr)
	require.Len(t, active, 1)
	checksums, checksumErr := ztapiQuotationChecksums(db, []int64{previous.ID})
	require.NoError(t, checksumErr)
	require.Equal(t, quote.WorkbookSHA256, checksums[previous.ID])

	_, err = ApplyZTAPIABPoolOfficial78Pricing(7, quote.WorkbookSHA256, []string{"GPT 5.6 Sol"})
	require.ErrorContains(t, err, "pool channel 2 is not a verified route")
	var before ZTAPIModelConfig
	require.NoError(t, db.First(&before, f.config.ID).Error)
	require.Equal(t, previous.ID, before.PublicationSnapshotID)

	verified := f.verification
	verified.ID = 0
	verified.ChannelID = pool.Id
	verified.VerifiedAt = time.Now().UTC().Unix()
	require.NoError(t, db.Create(&verified).Error)
	result, err := ApplyZTAPIABPoolOfficial78Pricing(7, quote.WorkbookSHA256, []string{"GPT 5.6 Sol"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Republished)
	var after ZTAPIModelConfig
	require.NoError(t, db.First(&after, f.config.ID).Error)
	require.NotEqual(t, previous.ID, after.PublicationSnapshotID)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, after.PublicationSnapshotID).Error)
	require.Equal(t, []int{pool.Id}, snapshot.ChannelIDs())
	require.Equal(t, string(ZTAPIPricePolicyPoolOfficial78Sep2026), snapshot.PricePolicy)
	require.Equal(t, `[2]`, snapshot.AllowedChannelIDs)
}
