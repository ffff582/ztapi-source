package model

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPIVideoPublicationContractDB(t *testing.T) (*gorm.DB, ZTAPIModelConfig, ZTAPIModelPriceSource) {
	t.Helper()
	originalQuotation := ztapiQuotation
	ztapiQuotation.Entries = append(append([]ZTAPIQuotationEntry(nil), ztapiQuotation.Entries...), ZTAPIQuotationEntry{
		Label: "synthetic-video", SourceModel: "provider-video-exact", PublicName: "zt-seedance-2-fast",
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderSeedance,
		Modality: ZTAPIModalityVideo, Status: "mapped", QuoteRows: []ZTAPIQuotationRow{{Resource: "企业资源"}},
	})
	t.Cleanup(func() { ztapiQuotation = originalQuotation })
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "video-publication.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIModelPriceSource{}, &ZTAPIModelVerification{}, &ZTAPIModelPublicationSnapshot{}))
	alias := "zt-seedance-2-fast"
	config := ZTAPIModelConfig{
		SourceModel: "provider-video-exact", PublicName: &alias, Protocol: ZTAPIProtocolOpenAICompatible,
		ProviderFamily: ZTAPIProviderSeedance, EnabledGroups: `["default"]`, Version: 1,
	}
	require.NoError(t, db.Create(&config).Error)
	source := validZTAPIPriceSourceForTest()
	source.ModelConfigID = config.ID
	source.SourceModel = config.SourceModel
	source.BillingDimensions = `["input_tokens"]`
	source.PricePolicy = string(ZTAPIPricePolicyEnterprise20Margin)
	source.MediaPriceContractJSON = mustCanonicalZTAPIMediaPriceContract(t, seedanceContractForTest(t))
	require.NoError(t, db.Create(&source).Error)
	return db, config, source
}

func authorizeFixtureSourceAsVideo(t *testing.T, fixture *ztapiPublicationGateFixture) {
	t.Helper()
	original := ztapiQuotation
	entries := append([]ZTAPIQuotationEntry(nil), original.Entries...)
	found := false
	for index := range entries {
		if entries[index].SourceModel == fixture.config.SourceModel {
			entries[index].Modality = ZTAPIModalityVideo
			entries[index].ProviderFamily = ZTAPIProviderSeedance
			found = true
			break
		}
	}
	require.True(t, found)
	ztapiQuotation.Entries = entries
	t.Cleanup(func() { ztapiQuotation = original })

	fixture.config.ProviderFamily = ZTAPIProviderSeedance
	fixture.config.InputPricePerMillion = 1.25
	fixture.config.OutputPricePerMillion = 2.5
	require.NoError(t, fixture.db.Model(&fixture.config).Update("provider_family", fixture.config.ProviderFamily).Error)
	require.NoError(t, fixture.db.Model(&fixture.config).Updates(map[string]any{
		"input_price_per_million":  fixture.config.InputPricePerMillion,
		"output_price_per_million": fixture.config.OutputPricePerMillion,
	}).Error)
	require.NoError(t, fixture.db.Model(&fixture.identity).Updates(map[string]any{
		"provider_family": fixture.config.ProviderFamily,
		"updated_at":      time.Now().UTC().Unix(),
	}).Error)
	fixture.price.PricePolicy = string(ZTAPIPricePolicyEnterprise20Margin)
	require.NoError(t, fixture.db.Model(&fixture.price).Updates(map[string]any{
		"price_policy":              fixture.price.PricePolicy,
		"media_price_contract_json": mustCanonicalZTAPIMediaPriceContract(t, seedanceContractForTest(t)),
	}).Error)
}

func addVerifiedVideoEvidence(t *testing.T, fixture *ztapiPublicationGateFixture, contractJSON string) ZTAPIModelVerification {
	return addVerifiedVideoEvidenceForChannel(t, fixture, fixture.channel.Id, contractJSON)
}

func addVerifiedVideoEvidenceForChannel(t *testing.T, fixture *ztapiPublicationGateFixture, channelID int, contractJSON string) ZTAPIModelVerification {
	t.Helper()
	verification := fixture.verification
	verification.ID = 0
	verification.ChannelID = channelID
	verification.Modality = ZTAPIModalityVideo
	verification.StreamingRequired = false
	verification.StreamingPassed = false
	verification.MediaResultValid = true
	verification.VideoCreatePassed = true
	verification.VideoFetchPassed = true
	verification.VideoTerminalPassed = true
	verification.VideoRestartRecoveryPassed = true
	verification.VideoSettlementIdempotencePassed = true
	verification.VideoProtocolContractJSON = contractJSON
	verification.VerifiedAt++
	require.NoError(t, fixture.db.Create(&verification).Error)
	return verification
}

func setupZTAPIVideoPublicationGateFixture(t *testing.T) (ztapiPublicationGateFixture, ZTAPIModelVerification) {
	t.Helper()
	fixture := setupZTAPIPublicationGateFixture(t)
	authorizeFixtureSourceAsVideo(t, &fixture)
	verification := addVerifiedVideoEvidence(t, &fixture, canonicalZTAPIVideoProtocolForPublicationTest(t, fixture.config.SourceModel))
	return fixture, verification
}

func TestZTAPIMediaPublicationBlocksIncompleteVideoLifecycleEvidence(t *testing.T) {
	tests := []struct {
		name    string
		column  string
		blocker string
	}{
		{name: "usable result", column: "media_result_valid", blocker: ZTAPIPublicationBlockerMediaResult},
		{name: "create", column: "video_create_passed", blocker: ZTAPIPublicationBlockerVideoCreate},
		{name: "fetch", column: "video_fetch_passed", blocker: ZTAPIPublicationBlockerVideoFetch},
		{name: "successful terminal", column: "video_terminal_passed", blocker: ZTAPIPublicationBlockerVideoTerminal},
		{name: "restart recovery", column: "video_restart_recovery_passed", blocker: ZTAPIPublicationBlockerVideoRestartRecovery},
		{name: "settlement idempotence", column: "video_settlement_idempotence_passed", blocker: ZTAPIPublicationBlockerVideoSettlementIdempotence},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture, verification := setupZTAPIVideoPublicationGateFixture(t)
			require.NoError(t, fixture.db.Model(&verification).Update(tc.column, false).Error)
			blockers, err := ZTAPIPublicationBlockers(fixture.config.ID)
			require.NoError(t, err)
			require.Contains(t, blockers, tc.blocker)
		})
	}
}

func TestZTAPIMediaPublicationDirectoryPriceOrOneSuccessCannotPublishVideo(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	authorizeFixtureSourceAsVideo(t, &fixture)
	oneSuccess := fixture.verification
	oneSuccess.ID = 0
	oneSuccess.Modality = ZTAPIModalityVideo
	oneSuccess.StreamingRequired = false
	oneSuccess.StreamingPassed = false
	oneSuccess.VideoCreatePassed = true
	oneSuccess.VideoProtocolContractJSON = canonicalZTAPIVideoProtocolForPublicationTest(t, fixture.config.SourceModel)
	oneSuccess.VerifiedAt++
	require.NoError(t, fixture.db.Create(&oneSuccess).Error)

	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.Error(t, err)
	require.Zero(t, committed.PublicationSnapshotID)
	for _, blocker := range []string{
		ZTAPIPublicationBlockerMediaResult,
		ZTAPIPublicationBlockerVideoFetch,
		ZTAPIPublicationBlockerVideoTerminal,
		ZTAPIPublicationBlockerVideoRestartRecovery,
		ZTAPIPublicationBlockerVideoSettlementIdempotence,
	} {
		require.Contains(t, err.Error(), blocker)
	}
}

func TestZTAPIMediaPublicationFreezesCompleteVideoEvidence(t *testing.T) {
	fixture, verification := setupZTAPIVideoPublicationGateFixture(t)

	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.NoError(t, err)
	require.NotZero(t, committed.PublicationSnapshotID)

	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, fixture.db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.Equal(t, verification.VideoProtocolContractJSON, snapshot.VideoProtocolContractJSON)
	require.Equal(t, mustCanonicalZTAPIMediaPriceContract(t, seedanceContractForTest(t)), snapshot.MediaPriceContractJSON)
}

func TestZTAPIMediaPublicationRequiresSameVideoContractAcrossEligibleChannels(t *testing.T) {
	fixture, _ := setupZTAPIVideoPublicationGateFixture(t)
	weight := uint(100)
	priority := int64(0)
	second := Channel{
		Name: "managed-video-second", Type: constant.ChannelTypeOpenAI,
		Key: "synthetic-second-key", Status: common.ChannelStatusEnabled,
		Models: fixture.config.SourceModel, Group: "default", ZTAPIManaged: true,
		Weight: &weight, Priority: &priority,
	}
	require.NoError(t, fixture.db.Create(&second).Error)
	require.NoError(t, second.AddAbilities(fixture.db))
	now := time.Now().UTC().Unix()
	discovery := ZTAPIDiscoverySnapshot{ChannelID: second.Id, ModelListHash: strings.Repeat("b", 64), ModelCount: 1, FetchedAt: now}
	require.NoError(t, fixture.db.Create(&discovery).Error)
	require.NoError(t, fixture.db.Create(&ZTAPIDiscoveredModel{SnapshotID: discovery.ID, ChannelID: second.Id, SourceModel: fixture.config.SourceModel}).Error)

	contract, _, err := types.ParseZTAPIVideoProtocolContract(canonicalZTAPIVideoProtocolForPublicationTest(t, fixture.config.SourceModel))
	require.NoError(t, err)
	contract.Create.Path = "/hub/v1/video/tasks-alt"
	_, differentContract, err := types.SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	addVerifiedVideoEvidenceForChannel(t, &fixture, second.Id, differentContract)

	blockers, err := ZTAPIPublicationBlockers(fixture.config.ID)
	require.NoError(t, err)
	require.Contains(t, blockers, ZTAPIPublicationBlockerVideoProtocol)
}

func TestZTAPIMediaPublicationCannotSpliceLifecycleEvidenceAcrossChannels(t *testing.T) {
	fixture, routeVerification := setupZTAPIVideoPublicationGateFixture(t)
	require.NoError(t, fixture.db.Model(&routeVerification).Update("media_result_valid", false).Error)
	weight := uint(100)
	priority := int64(0)
	second := Channel{Name: "managed-video-complete", Type: constant.ChannelTypeOpenAI, Key: "synthetic-complete-key",
		Status: common.ChannelStatusEnabled, Models: fixture.config.SourceModel, Group: "default", ZTAPIManaged: true,
		Weight: &weight, Priority: &priority}
	require.NoError(t, fixture.db.Create(&second).Error)
	require.NoError(t, second.AddAbilities(fixture.db))
	now := time.Now().UTC().Unix()
	discovery := ZTAPIDiscoverySnapshot{ChannelID: second.Id, ModelListHash: strings.Repeat("c", 64), ModelCount: 1, FetchedAt: now}
	require.NoError(t, fixture.db.Create(&discovery).Error)
	require.NoError(t, fixture.db.Create(&ZTAPIDiscoveredModel{SnapshotID: discovery.ID, ChannelID: second.Id, SourceModel: fixture.config.SourceModel}).Error)
	addVerifiedVideoEvidenceForChannel(t, &fixture, second.Id, canonicalZTAPIVideoProtocolForPublicationTest(t, fixture.config.SourceModel))

	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.NoError(t, err)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, fixture.db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.Equal(t, []int{second.Id}, snapshot.ChannelIDs(),
		"only the channel with its own complete lifecycle evidence may enter the frozen route set")
}

func canonicalZTAPIVideoProtocolForPublicationTest(t *testing.T, providerModel string) string {
	t.Helper()
	contract, _, err := types.ParseZTAPIVideoProtocolContract(ztapiVideoProtocolForMediaTaskTest(t))
	require.NoError(t, err)
	contract.ProviderModel = providerModel
	_, canonical, err := types.SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	return canonical
}

func TestZTAPIVideoVerificationContractIsCanonicalModelBoundAndImmutable(t *testing.T) {
	db, config, _ := setupZTAPIVideoPublicationContractDB(t)
	contractJSON := canonicalZTAPIVideoProtocolForPublicationTest(t, config.SourceModel)
	verification := ZTAPIModelVerification{
		ModelConfigID: config.ID, ChannelID: 7, Protocol: config.Protocol, Modality: ZTAPIModalityVideo,
		VideoProtocolContractJSON: contractJSON, OperatorID: 1, VerifiedAt: common.GetTimestamp(),
	}
	require.NoError(t, db.Create(&verification).Error)

	changed := canonicalZTAPIVideoProtocolForPublicationTest(t, "another-provider-model")
	require.ErrorIs(t, db.Model(&verification).Update("video_protocol_contract_json", changed).Error, ErrZTAPIVideoProtocolEvidenceImmutable)
	mismatch := verification
	mismatch.ID = 0
	mismatch.VideoProtocolContractJSON = changed
	require.Error(t, db.Create(&mismatch).Error)
}

func TestZTAPIVideoPublicationSnapshotRequiresAndFreezesExactContract(t *testing.T) {
	db, config, source := setupZTAPIVideoPublicationContractDB(t)
	base := ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version, SourceModel: config.SourceModel,
		PublicName: config.PublicNameValue(), Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		EnabledGroups: `["default"]`, AllowedChannelIDs: `[7]`, PriceSourceID: source.ID,
		VerificationIDs: `[1]`, IdentityUpdatedAt: 1, CreatedAt: common.GetTimestamp(),
	}
	require.Error(t, db.Create(&base).Error)

	base.ID = 0
	base.VideoProtocolContractJSON = canonicalZTAPIVideoProtocolForPublicationTest(t, config.SourceModel)
	require.NoError(t, db.Create(&base).Error)
	require.Equal(t, source.MediaPriceContractJSON, base.MediaPriceContractJSON)

	require.ErrorIs(t, db.Model(&base).Update("video_protocol_contract_json", "{}").Error, ErrZTAPIImageProtocolSnapshotImmutable)
}
