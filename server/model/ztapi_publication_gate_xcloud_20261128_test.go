package model

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func setupXcloudPublicationGate20261128(t *testing.T, source, group string, channelID int) ztapiPublicationGateFixture {
	t.Helper()
	f := setupZTAPIPublicationGateFixture(t)
	public, provider, protocol, err := ZTAPIXcloudPublicIdentity20261128(source)
	require.NoError(t, err)
	f.config.SourceModel, f.config.PublicName = source, &public
	f.config.ProviderFamily, f.config.Protocol = provider, protocol
	price, err := BuildZTAPIXcloudPriceSource20261128(f.config, group, 1, "")
	require.NoError(t, err)
	price.ID, price.Version = f.price.ID, f.price.Version
	preview, err := BuildZTAPIModelPricePreview(&price)
	require.NoError(t, err)
	require.NoError(t, applyZTAPICommercialPreview(&f.config, preview))
	require.NoError(t, f.db.Save(&f.config).Error)
	require.NoError(t, f.db.Save(&price).Error)
	f.price = price
	f.identity.PublicName, f.identity.ProviderFamily, f.identity.Protocol = public, provider, protocol
	require.NoError(t, f.db.Save(&f.identity).Error)

	require.NoError(t, f.db.Where("channel_id = ?", f.channel.Id).Delete(&Ability{}).Error)
	baseURL := "https://www.xcloudtoken.com"
	f.channel.Id, f.channel.Models, f.channel.BaseURL = channelID, source, &baseURL
	require.NoError(t, f.db.Create(&f.channel).Error)
	require.NoError(t, f.channel.AddAbilities(f.db))
	f.verification.ID, f.verification.ChannelID = 0, channelID
	require.NoError(t, f.db.Create(&f.verification).Error)
	f.snapshot.ChannelID = channelID
	require.NoError(t, f.db.Save(&f.snapshot).Error)
	require.NoError(t, f.db.Where("snapshot_id = ?", f.snapshot.ID).Delete(&ZTAPIDiscoveredModel{}).Error)
	require.NoError(t, f.db.Create(&ZTAPIDiscoveredModel{
		SnapshotID: f.snapshot.ID, ChannelID: channelID, SourceModel: source,
	}).Error)
	return f
}

func addXcloudPublicationRoute20261128(t *testing.T, f *ztapiPublicationGateFixture, channelID int, baseURL string) {
	t.Helper()
	channel := f.channel
	channel.Id, channel.BaseURL = channelID, &baseURL
	require.NoError(t, f.db.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(f.db))
	snapshot := ZTAPIDiscoverySnapshot{ChannelID: channelID, ModelCount: 1,
		ModelListHash: strings.Repeat("b", 64), FetchedAt: time.Now().UTC().Unix()}
	require.NoError(t, f.db.Create(&snapshot).Error)
	require.NoError(t, f.db.Create(&ZTAPIDiscoveredModel{
		SnapshotID: snapshot.ID, ChannelID: channelID, SourceModel: f.config.SourceModel,
	}).Error)
	verification := f.verification
	verification.ID, verification.ChannelID = 0, channelID
	require.NoError(t, f.db.Create(&verification).Error)
}

func requireXcloudPublicationDiscoveryBlocked20261128(t *testing.T, f *ztapiPublicationGateFixture) {
	t.Helper()
	blockers, evidence, err := ztapiPublicationBlockersTx(f.db, &f.config)
	require.NoError(t, err)
	require.Contains(t, blockers, ZTAPIPublicationBlockerDiscoveryStale)
	require.Contains(t, blockers, ZTAPIPublicationBlockerRouteUnavailable)
	require.Empty(t, evidence.AllowedChannelIDs)
	require.Empty(t, evidence.VerificationIDs)
}

func TestZTAPIPublicationGateXcloud20261128ExactGroups(t *testing.T) {
	for _, tc := range []struct {
		source, group string
		channel       int
	}{
		{"claude-opus-5-5", "claude-max", 4},
		{"claude-opus-5-5", "claude-kiro", 5},
		{"claude-opus-5-5", "new-vip", 6},
		{"gpt-6.2-sol", "gpt-plus", 7},
		{"glm-5.3-flash", "\u56fd\u4ea7\u5206\u7ec4", 8},
	} {
		t.Run(tc.group, func(t *testing.T) {
			f := setupXcloudPublicationGate20261128(t, tc.source, tc.group, tc.channel)
			blockers, evidence, err := ztapiPublicationBlockersTx(f.db, &f.config)
			require.NoError(t, err)
			require.Empty(t, blockers)
			require.Equal(t, []int{tc.channel}, evidence.AllowedChannelIDs)
			require.Equal(t, []int64{f.verification.ID}, evidence.VerificationIDs)
			require.Equal(t, f.price.ID, evidence.PriceSourceID)
		})
	}
}

func TestZTAPIPublicationGateXcloud20261128NoCrossGroupOrYunxinFallback(t *testing.T) {
	f := setupXcloudPublicationGate20261128(t, "claude-opus-5-5", "new-vip", 6)
	for _, id := range []int{4, 5, 99} {
		addXcloudPublicationRoute20261128(t, &f, id, "https://www.xcloudtoken.com")
	}
	addXcloudPublicationRoute20261128(t, &f, 2, "https://ai.yunxinapi.com/hub")
	blockers, evidence, err := ztapiPublicationBlockersTx(f.db, &f.config)
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.Equal(t, []int{6}, evidence.AllowedChannelIDs)
	require.Equal(t, []int64{f.verification.ID}, evidence.VerificationIDs)

	require.NoError(t, f.db.Model(&Channel{}).Where("id = ?", 6).Update("status", common.ChannelStatusManuallyDisabled).Error)
	requireXcloudPublicationDiscoveryBlocked20261128(t, &f)
}

func TestZTAPIPublicationGateXcloud20261128RejectsChangedChannelMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"disabled", "status", common.ChannelStatusManuallyDisabled},
		{"unmanaged", "ztapi_managed", false},
		{"wrong-protocol", "type", constant.ChannelTypeAnthropic},
		{"wrong-host", "base_url", "https://ai.yunxinapi.com/hub"},
		{"lookalike-host", "base_url", "https://www.xcloudtoken.com.invalid"},
		{"removed-list", "models", "gpt-6.1-sol"},
		{"changed-customer-group", "group", "vip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupXcloudPublicationGate20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
			require.NoError(t, f.db.Model(&Channel{}).Where("id = ?", 7).Update(tc.field, tc.value).Error)
			// Stale abilities and fresh historical discovery deliberately remain.
			requireXcloudPublicationDiscoveryBlocked20261128(t, &f)
		})
	}
}

func TestZTAPIPublicationGateXcloud20261128RejectsRenamedTerminalSKU(t *testing.T) {
	for _, tc := range []struct {
		name, mapping, discovered string
	}{
		{"renamed", `{"gpt-6.2-sol":"gpt-6.1-sol"}`, "gpt-6.1-sol"},
		{"terminal-renamed", `{"gpt-6.2-sol":"alias","alias":"gpt-6.1-sol"}`, "gpt-6.1-sol"},
		{"intermediate-only", `{"gpt-6.2-sol":"alias","alias":"gpt-6.1-sol"}`, "alias"},
		{"cycle", `{"gpt-6.2-sol":"alias","alias":"gpt-6.2-sol"}`, "gpt-6.2-sol"},
		{"invalid-json", `{`, "gpt-6.2-sol"},
		{"duplicate-entry", `{"gpt-6.2-sol":"wrong","gpt-6.2-sol":"gpt-6.2-sol"}`, "gpt-6.2-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupXcloudPublicationGate20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
			require.NoError(t, f.db.Model(&Channel{}).Where("id = ?", 7).Update("model_mapping", tc.mapping).Error)
			require.NoError(t, f.db.Model(&ZTAPIDiscoveredModel{}).Where("snapshot_id = ?", f.snapshot.ID).
				Update("source_model", tc.discovered).Error)
			requireXcloudPublicationDiscoveryBlocked20261128(t, &f)
		})
	}
}

func TestZTAPIPublicationGateXcloud20261128AllowsIdentityMapping(t *testing.T) {
	f := setupXcloudPublicationGate20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
	require.NoError(t, f.db.Model(&Channel{}).Where("id = ?", 7).
		Update("model_mapping", `{"gpt-6.2-sol":"gpt-6.2-sol","other":"wrong-sku"}`).Error)
	blockers, evidence, err := ztapiPublicationBlockersTx(f.db, &f.config)
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.Equal(t, []int{7}, evidence.AllowedChannelIDs)
}

func TestZTAPIPublicationGateXcloud20261128RejectsWrongOrUnprovisionedSpendTier(t *testing.T) {
	t.Run("different-existing-quoted-group", func(t *testing.T) {
		f := setupXcloudPublicationGate20261128(t, "claude-opus-5-5", "new-vip", 4)
		requireXcloudPublicationDiscoveryBlocked20261128(t, &f)
	})
	t.Run("test-group-has-no-authorized-channel", func(t *testing.T) {
		f := setupXcloudPublicationGate20261128(t, "claude-opus-5-5", "test", 6)
		requireXcloudPublicationDiscoveryBlocked20261128(t, &f)
	})
	t.Run("changed-source-group", func(t *testing.T) {
		f := setupXcloudPublicationGate20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
		require.NoError(t, f.db.Model(&f.price).Update("spend_tier", "new-vip").Error)
		requireXcloudPublicationDiscoveryBlocked20261128(t, &f)
	})
}

func TestZTAPIPublicationGateXcloud20261128RejectsInvalidDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"stale", "fetched_at", time.Now().Add(-48 * time.Hour).Unix()},
		{"future", "fetched_at", time.Now().Add(48 * time.Hour).Unix()},
		{"different-channel-snapshot", "channel_id", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupXcloudPublicationGate20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
			require.NoError(t, f.db.Model(&f.snapshot).Update(tc.field, tc.value).Error)
			requireXcloudPublicationDiscoveryBlocked20261128(t, &f)
		})
	}
}
