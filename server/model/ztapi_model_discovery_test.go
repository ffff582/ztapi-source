package model

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPIDiscoveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-discovery.db")), &gorm.Config{})
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
		&Channel{}, &Ability{}, &ZTAPIModelConfig{}, &ZTAPIAuditEvent{},
		&ZTAPICatalogLock{}, &ZTAPIDiscoverySnapshot{}, &ZTAPIDiscoveredModel{},
	))
	return db
}

func createZTAPIDiscoveryChannel(t *testing.T, db *gorm.DB, name string) Channel {
	t.Helper()
	weight := uint(100)
	priority := int64(0)
	channel := Channel{
		Name: name, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusManuallyDisabled,
		Group: "default", ZTAPIManaged: true, Weight: &weight, Priority: &priority,
	}
	require.NoError(t, db.Create(&channel).Error)
	return channel
}

func TestZTAPIDiscoveryNormalizesSnapshotsAndKeepsImportsPrivate(t *testing.T) {
	db := setupZTAPIDiscoveryTestDB(t)
	channel := createZTAPIDiscoveryChannel(t, db, "yunxin-a")
	fetchedAt := time.Unix(1_787_000_000, 0).UTC()

	first, err := ImportZTAPIDiscovery(channel.Id, []string{" model-b ", "model-a"}, fetchedAt)
	require.NoError(t, err)
	require.Equal(t, []string{"model-a", "model-b"}, first.ModelIDs)
	require.Equal(t, 2, first.ImportedCount)
	require.Len(t, first.ModelListHash, 64)

	var stored Channel
	require.NoError(t, db.First(&stored, channel.Id).Error)
	require.Equal(t, "model-a,model-b", stored.Models)

	var configs []ZTAPIModelConfig
	require.NoError(t, db.Order("source_model ASC").Find(&configs).Error)
	require.Len(t, configs, 2)
	for _, config := range configs {
		require.False(t, config.Published)
		require.Empty(t, config.PublicNameValue())
		require.Equal(t, ZTAPIProtocolOpenAICompatible, config.Protocol)
		require.Empty(t, config.ProviderFamily)
	}

	var abilities []Ability
	require.NoError(t, db.Order("model ASC").Find(&abilities).Error)
	require.Len(t, abilities, 2)
	for _, ability := range abilities {
		require.False(t, ability.Enabled)
	}

	second, err := ImportZTAPIDiscovery(channel.Id, []string{"model-a", "model-b"}, fetchedAt.Add(time.Minute))
	require.NoError(t, err)
	require.NotEqual(t, first.SnapshotID, second.SnapshotID)
	require.Equal(t, first.ModelListHash, second.ModelListHash)

	var snapshotCount, configCount, membershipCount int64
	require.NoError(t, db.Model(&ZTAPIDiscoverySnapshot{}).Count(&snapshotCount).Error)
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).Count(&configCount).Error)
	require.NoError(t, db.Model(&ZTAPIDiscoveredModel{}).Count(&membershipCount).Error)
	require.EqualValues(t, 2, snapshotCount)
	require.EqualValues(t, 2, configCount)
	require.EqualValues(t, 4, membershipCount)
}

func TestZTAPIDiscoveryTracksPartiallyOverlappingChannels(t *testing.T) {
	db := setupZTAPIDiscoveryTestDB(t)
	firstChannel := createZTAPIDiscoveryChannel(t, db, "yunxin-a")
	secondChannel := createZTAPIDiscoveryChannel(t, db, "yunxin-b")
	now := time.Unix(1_787_000_000, 0).UTC()

	_, err := ImportZTAPIDiscovery(firstChannel.Id, []string{"model-a", "model-b"}, now)
	require.NoError(t, err)
	_, err = ImportZTAPIDiscovery(secondChannel.Id, []string{"model-b", "model-c"}, now.Add(time.Second))
	require.NoError(t, err)

	var configCount int64
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).Count(&configCount).Error)
	require.EqualValues(t, 3, configCount)

	var sharedMemberships int64
	require.NoError(t, db.Model(&ZTAPIDiscoveredModel{}).
		Where("source_model = ?", "model-b").Count(&sharedMemberships).Error)
	require.EqualValues(t, 2, sharedMemberships)
}

func TestZTAPIDiscoveryBackfillsProtocolForExistingPrivateDraft(t *testing.T) {
	db := setupZTAPIDiscoveryTestDB(t)
	channel := createZTAPIDiscoveryChannel(t, db, "yunxin-protocol-backfill")
	config := ZTAPIModelConfig{
		SourceModel: "legacy-private-model", EnabledGroups: "[]",
		Published: false, Version: 7, CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&config).Error)

	_, err := ImportZTAPIDiscovery(channel.Id, []string{config.SourceModel}, time.Now().UTC())
	require.NoError(t, err)

	var reloaded ZTAPIModelConfig
	require.NoError(t, db.First(&reloaded, config.ID).Error)
	require.Equal(t, ZTAPIProtocolOpenAICompatible, reloaded.Protocol)
	require.Equal(t, uint64(8), reloaded.Version)
	require.False(t, reloaded.Published)
	require.Empty(t, reloaded.PublicNameValue())
	require.Empty(t, reloaded.ProviderFamily)
}

func TestZTAPIDiscoveryRejectsMalformedListsAtomically(t *testing.T) {
	db := setupZTAPIDiscoveryTestDB(t)
	channel := createZTAPIDiscoveryChannel(t, db, "yunxin-invalid")
	now := time.Unix(1_787_000_000, 0).UTC()

	tests := []struct {
		name string
		ids  []string
	}{
		{name: "empty result", ids: nil},
		{name: "blank id", ids: []string{"model-a", "  "}},
		{name: "duplicate", ids: []string{"model-a", " model-a "}},
		{name: "oversized", ids: []string{strings.Repeat("x", 256)}},
		{name: "comma", ids: []string{"model,a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ImportZTAPIDiscovery(channel.Id, test.ids, now)
			require.Error(t, err)
		})
	}

	for _, value := range []struct {
		model any
	}{
		{&ZTAPIDiscoverySnapshot{}}, {&ZTAPIDiscoveredModel{}}, {&ZTAPIModelConfig{}}, {&Ability{}},
	} {
		var count int64
		require.NoError(t, db.Model(value.model).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestZTAPIDiscoveryNeverDeletesPublishedCatalogRows(t *testing.T) {
	db := setupZTAPIDiscoveryTestDB(t)
	channel := createZTAPIDiscoveryChannel(t, db, "yunxin-preserve")
	alias := "zt-existing"
	published := ZTAPIModelConfig{
		SourceModel: "existing-source", PublicName: &alias,
		Family: ZTAPIModelFamilyOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
		ProviderFamily: ZTAPIProviderOpenAI, EnabledGroups: `["default"]`,
		Published: true, Version: 4,
	}
	require.NoError(t, db.Create(&published).Error)

	_, err := ImportZTAPIDiscovery(channel.Id, []string{"new-source"}, time.Now().UTC())
	require.NoError(t, err)

	var got ZTAPIModelConfig
	require.NoError(t, db.First(&got, published.ID).Error)
	require.Equal(t, "existing-source", got.SourceModel)
	require.Equal(t, alias, got.PublicNameValue())
}

func TestZTAPIDiscoveryRefreshKeepsPublishedManagedProtocolRoute(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)

	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.NoError(t, err)
	require.True(t, committed.Published)
	require.Empty(t, committed.Family)
	require.Equal(t, ZTAPIProviderDeepSeek, committed.ProviderFamily)
	require.Equal(t, ZTAPIProtocolOpenAICompatible, committed.Protocol)

	_, err = ImportZTAPIDiscovery(
		fixture.channel.Id,
		[]string{fixture.config.SourceModel, "qwen-new-private-model"},
		time.Now().UTC().Add(time.Minute),
	)
	require.NoError(t, err)

	var reloaded ZTAPIModelConfig
	require.NoError(t, fixture.db.First(&reloaded, committed.ID).Error)
	require.True(t, reloaded.Published)
	require.Equal(t, committed.Version, reloaded.Version)
	require.Equal(t, committed.PublicationSnapshotID, reloaded.PublicationSnapshotID)
}

func TestZTAPIDiscoveryRefreshUnpublishesSnapshotWhenModelRouteIsRemoved(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)

	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.NoError(t, err)
	require.True(t, committed.Published)

	_, err = ImportZTAPIDiscovery(
		fixture.channel.Id,
		[]string{"qwen-replacement-private-model"},
		time.Now().UTC().Add(time.Minute),
	)
	require.NoError(t, err)

	var reloaded ZTAPIModelConfig
	require.NoError(t, fixture.db.First(&reloaded, committed.ID).Error)
	require.False(t, reloaded.Published)
	require.Equal(t, committed.Version+1, reloaded.Version)
}
