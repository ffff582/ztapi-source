/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package model

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestZTAPIModelConfigTextColumnsDoNotDeclareDatabaseDefaults(t *testing.T) {
	parsed, err := schema.Parse(&ZTAPIModelConfig{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	enabledGroups := parsed.LookUpField("EnabledGroups")
	require.NotNil(t, enabledGroups)
	require.False(t, enabledGroups.HasDefaultValue,
		"MySQL rejects defaults on TEXT columns during AutoMigrate")
}

func TestZTAPIEnsureAndPublishCannotCommitCrossColumnNameConflict(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-name-lock.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ZTAPIModelConfig{}, &ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	previousDB := DB
	previousUsingSQLite := common.UsingSQLite
	DB = db
	common.UsingSQLite = true
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		common.UsingSQLite = previousUsingSQLite
		_ = sqlDB.Close()
	})

	priority := int64(10)
	weight := uint(100)
	for _, source := range []string{"gpt-claimed-source", "gpt-publisher-source"} {
		channel := Channel{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&Ability{
			Group: "default", Model: source, ChannelId: channel.Id, Enabled: true,
			Priority: &priority, Weight: weight,
		}).Error)
	}
	now := common.GetTimestamp()
	config := ZTAPIModelConfig{
		SourceModel: "gpt-publisher-source", Family: ZTAPIModelFamilyOpenAI,
		InputCostPerMillion: 1, OutputCostPerMillion: 2, InputPricePerMillion: 3, OutputPricePerMillion: 6,
		EnabledGroups: `["default"]`, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&config).Error)
	alias := "gpt-claimed-source"
	next := config
	next.PublicName = &alias
	next.Published = true

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results <- EnsureZTAPIModelConfigsForEnabledAbilities()
	}()
	go func() {
		defer wg.Done()
		<-start
		_, updateErr := UpdateZTAPIModelConfigAndBilling(&next, config.Version, nil)
		results <- updateErr
	}()
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)

	var conflictingSources int64
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).
		Where("source_model = ? AND id <> ?", alias, config.ID).
		Count(&conflictingSources).Error)
	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.False(t, reloaded.Published && conflictingSources > 0)
}

func TestEnsureZTAPIModelConfigsAppliesSafeRatioDefaultsWithoutPublishing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-new-model-defaults.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ZTAPIModelConfig{}, &ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	previousDB := DB
	previousUsingSQLite := common.UsingSQLite
	DB = db
	common.UsingSQLite = true
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		common.UsingSQLite = previousUsingSQLite
		_ = sqlDB.Close()
	})

	channel := Channel{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	priority := int64(10)
	require.NoError(t, db.Create(&Ability{
		Group: "default", Model: "gpt-new-defaults", ChannelId: channel.Id,
		Enabled: true, Priority: &priority, Weight: 100,
	}).Error)

	require.NoError(t, EnsureZTAPIModelConfigsForEnabledAbilities())
	var config ZTAPIModelConfig
	require.NoError(t, db.Where("source_model = ?", "gpt-new-defaults").First(&config).Error)
	require.False(t, config.Published)
	require.False(t, config.HasCompletePricing(), "zero base prices must still block publication")
	require.InDelta(t, 0.1, config.CacheReadRatio, 0.000001)
	require.InDelta(t, 1.25, config.CacheCreationRatio, 0.000001)
	require.InDelta(t, 1.25, config.CacheCreation5mRatio, 0.000001)
	require.InDelta(t, 2.0, config.CacheCreation1hRatio, 0.000001)
	require.InDelta(t, 1.0, config.ImageRatio, 0.000001)
	require.InDelta(t, 1.0, config.AudioRatio, 0.000001)
	require.InDelta(t, 1.0, config.AudioCompletionRatio, 0.000001)
}

func TestZTAPIPublicationCacheRefreshesAfterExternalRename(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-cache.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIModelPriceSource{}, &ZTAPIModelPublicationSnapshot{}))
	previousDB := DB
	previousUsingSQLite := common.UsingSQLite
	previousTTL := ztapiPublicationCacheTTL
	DB = db
	common.UsingSQLite = true
	ztapiPublicationCacheTTL = 20 * time.Millisecond
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		common.UsingSQLite = previousUsingSQLite
		ztapiPublicationCacheTTL = previousTTL
		InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})

	oldAlias := "zt-gpt-5.5"
	config := seedZTAPIPublicCatalogRecord(
		t, db, "gpt-5.5", oldAlias,
		ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens},
	)
	source, ok := ResolveZTAPIPublicAlias(oldAlias)
	require.True(t, ok)
	require.Equal(t, "gpt-5.5", source)

	newAlias := "zt-gpt-new"
	newVersion := config.Version + 1
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = ?", config.ID).Updates(map[string]any{
		"public_name": newAlias,
		"version":     newVersion,
	}).Error)
	time.Sleep(30 * time.Millisecond)

	_, oldOK := ResolveZTAPIPublicAlias(oldAlias)
	require.False(t, oldOK)
	_, newOK := ResolveZTAPIPublicAlias(newAlias)
	require.False(t, newOK, "mutable rename must remain private until a new snapshot is attached")

	var replacement ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&replacement, config.PublicationSnapshotID).Error)
	replacement.ID = 0
	replacement.ModelVersion = newVersion
	replacement.PublicName = newAlias
	replacement.CreatedAt++
	require.NoError(t, db.Create(&replacement).Error)
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = ?", config.ID).
		Update("publication_snapshot_id", replacement.ID).Error)
	time.Sleep(30 * time.Millisecond)

	_, newOK = ResolveZTAPIPublicAlias(newAlias)
	require.False(t, newOK, "a replacement snapshot cannot authorize an alias outside the quotation")

	replacement.ID = 0
	replacement.PublicName = oldAlias
	require.NoError(t, db.Create(&replacement).Error)
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = ?", config.ID).
		Update("publication_snapshot_id", replacement.ID).Error)
	time.Sleep(30 * time.Millisecond)
	newSource, newOK := ResolveZTAPIPublicAlias(oldAlias)
	require.True(t, newOK)
	require.Equal(t, "gpt-5.5", newSource)
}

func TestZTAPIIncompleteModalityPricingIsFailClosed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-incomplete-pricing.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}))
	previousDB := DB
	DB = db
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})

	alias := "zt-gpt-incomplete-pricing"
	require.NoError(t, db.Create(&ZTAPIModelConfig{
		SourceModel: "gpt-incomplete-pricing", PublicName: &alias, Family: ZTAPIModelFamilyOpenAI,
		InputCostPerMillion: 1, OutputCostPerMillion: 2, InputPricePerMillion: 3, OutputPricePerMillion: 6,
		EnabledGroups: `["default"]`, Published: true, Version: 1,
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)

	_, err = ResolveZTAPIRequestModel(alias, "default")
	require.ErrorIs(t, err, ErrZTAPIModelNotPublic)
	_, _, ok, err := GetZTAPIPublishedSalePrice(alias)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestBackfillZTAPIPricingRatiosKeepsLegacyModelPrivateUntilRepublished(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-pricing-backfill.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	previousDB := DB
	previousUsingSQLite := common.UsingSQLite
	DB = db
	common.UsingSQLite = true
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		common.UsingSQLite = previousUsingSQLite
		InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})

	alias := "zt-gpt-legacy-pricing"
	legacy := ZTAPIModelConfig{
		SourceModel:           "gpt-legacy-pricing",
		PublicName:            &alias,
		Family:                ZTAPIModelFamilyOpenAI,
		InputCostPerMillion:   1,
		OutputCostPerMillion:  2,
		InputPricePerMillion:  2,
		OutputPricePerMillion: 6,
		EnabledGroups:         `["default"]`,
		Published:             true,
		Version:               4,
		CreatedAt:             common.GetTimestamp(),
		UpdatedAt:             common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&legacy).Error)
	require.False(t, legacy.HasCompletePricing())

	require.NoError(t, backfillZTAPIPricingRatios())

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, legacy.ID).Error)
	require.True(t, reloaded.HasCompletePricing())
	require.InDelta(t, 0.1, reloaded.CacheReadRatio, 0.000001)
	require.InDelta(t, 1.25, reloaded.CacheCreationRatio, 0.000001)
	require.InDelta(t, 1.25, reloaded.CacheCreation5mRatio, 0.000001)
	require.InDelta(t, 2.0, reloaded.CacheCreation1hRatio, 0.000001)
	require.InDelta(t, 1.0, reloaded.ImageRatio, 0.000001)
	require.InDelta(t, 1.0, reloaded.AudioRatio, 0.000001)
	require.InDelta(t, 3.0, reloaded.AudioCompletionRatio, 0.000001)
	require.Equal(t, uint64(5), reloaded.Version)

	var events []ZTAPIAuditEvent
	require.NoError(t, db.Where("model_config_id = ? AND action = ?", legacy.ID, "model.pricing_backfill").Find(&events).Error)
	require.Len(t, events, 1)
	require.Contains(t, events[0].Payload, `"migration":"ztapi_multimodal_ratio_v1"`)

	require.NoError(t, backfillZTAPIPricingRatios())
	var eventCount int64
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).
		Where("model_config_id = ? AND action = ?", legacy.ID, "model.pricing_backfill").
		Count(&eventCount).Error)
	require.Equal(t, int64(1), eventCount)

	InvalidateZTAPIAliasCache()
	_, ok := ResolveZTAPIPublicAlias(alias)
	require.False(t, ok, "legacy published rows without immutable snapshots must fail closed")
}

func TestBackfillZTAPIPricingRatiosRejectsPublishedModelWithoutBasePrices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-pricing-backfill-invalid.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	previousDB := DB
	DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		_ = sqlDB.Close()
	})

	alias := "zt-invalid-base-price"
	require.NoError(t, db.Create(&ZTAPIModelConfig{
		SourceModel:   "gpt-invalid-base-price",
		PublicName:    &alias,
		Family:        ZTAPIModelFamilyOpenAI,
		EnabledGroups: `["default"]`,
		Published:     true,
		Version:       1,
		CreatedAt:     common.GetTimestamp(),
		UpdatedAt:     common.GetTimestamp(),
	}).Error)

	err = backfillZTAPIPricingRatios()
	require.Error(t, err)
	require.Contains(t, err.Error(), "positive input and output prices")
}

func TestZTAPIExistingEmptyCatalogRejectsDirectSource(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-empty.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}))
	previousDB := DB
	DB = db
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})

	_, err = ResolveZTAPIRequestModel("gpt-direct-source", "default")
	require.ErrorIs(t, err, ErrZTAPIModelNotPublic)
}

func TestZTAPIExistingWildcardPublicationIsFailClosed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-wildcard.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}))
	previousDB := DB
	DB = db
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})

	alias := "zt-gpt-legacy-wildcard"
	require.NoError(t, db.Create(&ZTAPIModelConfig{
		SourceModel: "gpt-legacy-wildcard", PublicName: &alias, Family: ZTAPIModelFamilyOpenAI,
		InputCostPerMillion: 1, OutputCostPerMillion: 2, InputPricePerMillion: 3, OutputPricePerMillion: 6,
		EnabledGroups: `["all"]`, Published: true, Version: 1,
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)
	InvalidateZTAPIAliasCache()

	_, err = ResolveZTAPIRequestModel(alias, "default")
	require.ErrorIs(t, err, ErrZTAPIModelNotPublic)
}

func TestZTAPIChannelAbilityCannotCollideWithPublishedAlias(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-source-collision.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ZTAPIModelConfig{}))
	previousDB := DB
	DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		_ = sqlDB.Close()
	})

	alias := "zt-public-alias"
	require.NoError(t, db.Create(&ZTAPIModelConfig{
		SourceModel: "gpt-real-source", PublicName: &alias, Published: true,
		EnabledGroups: `["default"]`, Version: 1,
	}).Error)
	weight := uint(100)
	priority := int64(10)
	channel := Channel{
		Id: 42, Key: "test", Status: common.ChannelStatusEnabled, Models: alias,
		Group: "default", Weight: &weight, Priority: &priority,
	}
	err = channel.AddAbilities(db)
	require.ErrorContains(t, err, "conflicts with published ZTAPI alias")
	var count int64
	require.NoError(t, db.Model(&Ability{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestZTAPIProtocolMetadataIsFamilyAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-family.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	previousDB := DB
	DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		_ = sqlDB.Close()
	})

	weight := uint(100)
	priority := int64(10)
	channel := Channel{Type: constant.ChannelTypeDeepSeek, Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&Ability{
		Group: "default", Model: "gpt-private", ChannelId: channel.Id, Enabled: true,
		Priority: &priority, Weight: weight,
	}).Error)
	family, ok, err := ResolveZTAPIModelFamilyFromRoutes("gpt-private")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, family)

	_, nameLooksSupported := InferZTAPIModelFamily("gpt-private")
	require.True(t, nameLooksSupported)
}

func TestZTAPIOpenAICompatibleAggregatorIsNotFamilyAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-openai-compatible.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	previousDB := DB
	DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		_ = sqlDB.Close()
	})

	weight := uint(100)
	priority := int64(10)
	aggregatorURL := "https://aggregator.example.com/v1"
	channel := Channel{
		Type:    constant.ChannelTypeOpenAI,
		BaseURL: &aggregatorURL,
		Status:  common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&Ability{
		Group: "default", Model: "deepseek-chat", ChannelId: channel.Id, Enabled: true,
		Priority: &priority, Weight: weight,
	}).Error)

	family, ok, err := ResolveZTAPIModelFamilyFromRoutes("deepseek-chat")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, family)
}

func TestZTAPIManagedAggregatorUsesServerOwnedFamilyMetadata(t *testing.T) {
	t.Setenv(ztapiUpstreamMasterKeyEnv, ztapiChannelSecretTestMasterKey)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-managed-aggregator.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	previousDB := DB
	DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		_ = sqlDB.Close()
	})

	weight := uint(100)
	priority := int64(10)
	aggregatorURL := "https://approved-upstream.example.com/v1"
	channel := Channel{
		Type: constant.ChannelTypeOpenAI, BaseURL: &aggregatorURL,
		Status: common.ChannelStatusEnabled, Key: "managed-aggregator-test-key",
		ZTAPIManaged: true, ZTAPIFamily: ZTAPIModelFamilyOpenAI,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&Ability{
		Group: "default", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true,
		Priority: &priority, Weight: weight,
	}).Error)

	family, ok, err := ResolveZTAPIModelFamilyFromRoutes("gpt-4o")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ZTAPIModelFamilyOpenAI, family)

	require.NoError(t, db.Create(&Ability{
		Group: "default", Model: "claude-3-7-sonnet", ChannelId: channel.Id, Enabled: true,
		Priority: &priority, Weight: weight,
	}).Error)
	family, ok, err = ResolveZTAPIModelFamilyFromRoutes("claude-3-7-sonnet")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ZTAPIModelFamilyClaude, family)
}

func TestZTAPITrustedRouteIDsExcludeUnmanagedAggregatorsAndCoverEveryGroup(t *testing.T) {
	t.Setenv(ztapiUpstreamMasterKeyEnv, ztapiChannelSecretTestMasterKey)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-trusted-routes.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	previousDB := DB
	DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		_ = sqlDB.Close()
	})

	weight := uint(100)
	priority := int64(10)
	trustedURL := "https://approved-upstream.example.com/v1"
	untrustedURL := "https://legacy-upstream.example.com/v1"
	trusted := Channel{
		Type: constant.ChannelTypeOpenAI, BaseURL: &trustedURL, Status: common.ChannelStatusEnabled,
		Key: "trusted-route-test-key", ZTAPIManaged: true, ZTAPIFamily: ZTAPIModelFamilyOpenAI,
	}
	untrusted := Channel{Type: constant.ChannelTypeOpenAI, BaseURL: &untrustedURL, Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&trusted).Error)
	require.NoError(t, db.Create(&untrusted).Error)
	for _, group := range []string{"default", "vip"} {
		for _, channelID := range []int{trusted.Id, untrusted.Id} {
			require.NoError(t, db.Create(&Ability{
				Group: group, Model: "gpt-4o", ChannelId: channelID, Enabled: true,
				Priority: &priority, Weight: weight,
			}).Error)
		}
	}

	ids, err := GetZTAPITrustedRouteChannelIDs("gpt-4o", ZTAPIModelFamilyOpenAI, []string{"default", "vip"})
	require.NoError(t, err)
	require.Equal(t, []int{trusted.Id}, ids)

	require.NoError(t, db.Where("channel_id = ? AND "+commonGroupCol+" = ?", trusted.Id, "vip").Delete(&Ability{}).Error)
	_, err = GetZTAPITrustedRouteChannelIDs("gpt-4o", ZTAPIModelFamilyOpenAI, []string{"default", "vip"})
	require.ErrorContains(t, err, "vip")
}

func TestZTAPIOfficialDirectChannelsAreFamilyAuthority(t *testing.T) {
	testCases := []struct {
		name        string
		channelType int
		baseURL     *string
		model       string
		wantFamily  string
	}{
		{name: "OpenAI", channelType: constant.ChannelTypeOpenAI, model: "gpt-4o", wantFamily: ZTAPIModelFamilyOpenAI},
		{name: "Anthropic", channelType: constant.ChannelTypeAnthropic, model: "claude-3-7-sonnet", wantFamily: ZTAPIModelFamilyClaude},
		{name: "Gemini", channelType: constant.ChannelTypeGemini, model: "gemini-2.5-pro", wantFamily: ZTAPIModelFamilyGemini},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-official.db")), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
			previousDB := DB
			DB = db
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() {
				DB = previousDB
				_ = sqlDB.Close()
			})

			weight := uint(100)
			priority := int64(10)
			channel := Channel{
				Type: testCase.channelType, BaseURL: testCase.baseURL, Status: common.ChannelStatusEnabled,
			}
			require.NoError(t, db.Create(&channel).Error)
			require.NoError(t, db.Create(&Ability{
				Group: "default", Model: testCase.model, ChannelId: channel.Id, Enabled: true,
				Priority: &priority, Weight: weight,
			}).Error)

			family, ok, err := ResolveZTAPIModelFamilyFromRoutes(testCase.model)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, testCase.wantFamily, family)
		})
	}
}

func TestZTAPIMissingCatalogRejectsDirectSourceOnNonMasterNode(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-missing-catalog.db")), &gorm.Config{})
	require.NoError(t, err)
	previousDB := DB
	previousMaster := common.IsMasterNode
	DB = db
	common.IsMasterNode = false
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previousDB
		common.IsMasterNode = previousMaster
		InvalidateZTAPIAliasCache()
		_ = sqlDB.Close()
	})

	resolved, err := ResolveZTAPIRequestModel("gpt-direct-source", "default")
	require.ErrorIs(t, err, ErrZTAPIModelNotPublic)
	require.Empty(t, resolved)
}

func TestZTAPICatalogReadFailureRejectsDirectSource(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-read-failure.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}))
	previousDB := DB
	DB = db
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	t.Cleanup(func() {
		DB = previousDB
		InvalidateZTAPIAliasCache()
	})

	resolved, err := ResolveZTAPIRequestModel("gpt-direct-source", "default")
	require.Error(t, err)
	require.Empty(t, resolved)
}
