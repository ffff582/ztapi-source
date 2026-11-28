package model

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIXcloudPublication20261128StagingPreservesLiveHaikuAndRollback(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	url := "https://www.xcloudtoken.com"
	for _, id := range []int{4, 5} {
		c := f.channel
		c.Id, c.BaseURL, c.Models = id, &url, "claude-haiku-4-5-20251001"
		require.NoError(t, f.db.Create(&c).Error)
		require.NoError(t, c.AddAbilities(f.db))
	}
	public := "zt-claude-haiku-4.5"
	config := f.config
	config.ID, config.SourceModel, config.PublicName, config.ProviderFamily = 49, "claude-haiku-4-5-20251001", &public, ZTAPIProviderAnthropic
	oldSource, err := buildZTAPIXcloudSource20261114(config.SourceModel, 9)
	require.NoError(t, err)
	oldSource.Version = 1
	preview, err := BuildZTAPIModelPricePreview(&oldSource)
	require.NoError(t, err)
	require.NoError(t, applyZTAPICommercialPreview(&config, preview))
	require.NoError(t, f.db.Create(&config).Error)
	require.NoError(t, f.db.Create(&oldSource).Error)
	identity := f.identity
	identity.ModelConfigID, identity.PublicName, identity.ProviderFamily = config.ID, public, config.ProviderFamily
	require.NoError(t, f.db.Create(&identity).Error)
	old, err := createZTAPIModelPublicationSnapshotTx(f.db, &config, 1, ztapiPublicationEvidence{PriceSourceID: oldSource.ID, AllowedChannelIDs: []int{4}, IdentityUpdatedAt: identity.UpdatedAt})
	require.NoError(t, err)
	config.Published, config.PublicationSnapshotID = true, old.ID
	require.NoError(t, f.db.Save(&config).Error)
	stage, err := StageZTAPIXcloud20261128(9, ZTAPIXcloudStage20261128{SourceModel: config.SourceModel, Group: "claude-kiro", ChannelID: 5, ConfigID: 49, Version: 1, SnapshotID: old.ID, SourceID: oldSource.ID}, []string{config.SourceModel}, time.Now())
	require.NoError(t, err)
	var stillLive ZTAPIModelConfig
	require.NoError(t, f.db.First(&stillLive, 49).Error)
	require.Equal(t, config, stillLive, "staging cannot change live version, snapshot or price")
	rows, err := loadZTAPIQuotedPublications(f.db)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, old.ID, rows[0].SnapshotID)
	v := f.verification
	v.ID, v.ModelConfigID, v.ChannelID, v.VerifiedAt = 0, 49, 5, common.GetTimestamp()
	require.NoError(t, f.db.Create(&v).Error)
	req := ZTAPIXcloudPublish20261128{ConfigID: 49, Version: 1, SnapshotID: old.ID, SourceID: stage.Source.ID, VerificationID: v.ID}
	denied := errors.New("prospective cost evidence is not ready")
	_, err = ApplyZTAPIXcloudPublication20261128(9, req, func(*gorm.DB) error { return denied })
	require.ErrorIs(t, err, denied)
	var snapshots int64
	require.NoError(t, f.db.Model(&ZTAPIModelPublicationSnapshot{}).Where("model_config_id = ?", 49).Count(&snapshots).Error)
	require.EqualValues(t, 1, snapshots, "a rejected cost gate cannot leave a candidate snapshot")
	published, err := ApplyZTAPIXcloudPublication20261128(9, req, func(*gorm.DB) error { return nil })
	require.NoError(t, err)
	require.Equal(t, []int{5}, published.ChannelIDs())
	require.Equal(t, old.TokenPriceRulesJSON, published.TokenPriceRulesJSON, "unchanged cost does not reduce existing retail")
	require.ErrorIs(t, RestoreZTAPIXcloudPublication20261128(9, 49, published.ModelVersion, published.ID, func(tx *gorm.DB) error {
		var candidate ZTAPIModelConfig
		require.NoError(t, tx.First(&candidate, 49).Error)
		var snapshot ZTAPIModelPublicationSnapshot
		require.NoError(t, tx.First(&snapshot, candidate.PublicationSnapshotID).Error)
		require.Equal(t, []int{4}, snapshot.ChannelIDs())
		return denied
	}), denied)
	var rejected ZTAPIModelConfig
	require.NoError(t, f.db.First(&rejected, 49).Error)
	require.Equal(t, published.ID, rejected.PublicationSnapshotID, "failed rollback proof must preserve the active publication")
	require.NoError(t, RestoreZTAPIXcloudPublication20261128(9, 49, published.ModelVersion, published.ID, func(*gorm.DB) error { return nil }))
	var restored ZTAPIModelConfig
	require.NoError(t, f.db.First(&restored, 49).Error)
	require.True(t, restored.Published)
	var restoredSnapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, f.db.First(&restoredSnapshot, restored.PublicationSnapshotID).Error)
	require.Equal(t, []int{4}, restoredSnapshot.ChannelIDs())
	require.Equal(t, old.TokenPriceRulesJSON, restoredSnapshot.TokenPriceRulesJSON)
	require.NoError(t, f.db.First(&ZTAPIModelPublicationSnapshot{}, published.ID).Error)
}

func xcloudStageFixture20261128(t *testing.T) ztapiPublicationGateFixture {
	t.Helper()
	f := setupZTAPIPublicationGateFixture(t)
	url := "https://www.xcloudtoken.com"
	c := f.channel
	c.Id, c.BaseURL, c.Models = 7, &url, "gpt-6-sol"
	require.NoError(t, f.db.Create(&c).Error)
	require.NoError(t, c.AddAbilities(f.db))
	return f
}

func stageXcloudNew20261128(t *testing.T, f ztapiPublicationGateFixture) *ZTAPIXcloudStaged20261128 {
	t.Helper()
	s, err := StageZTAPIXcloud20261128(9, ZTAPIXcloudStage20261128{SourceModel: "gpt-6.2-sol", Group: "gpt-plus", ChannelID: 7},
		[]string{"gpt-6.2-sol", "unrelated-advertised-model"}, time.Now())
	require.NoError(t, err)
	require.False(t, s.Config.Published)
	require.Zero(t, s.Config.PublicationSnapshotID)
	require.Equal(t, "zt-gpt-6.2-sol", s.Config.PublicNameValue())
	var c Channel
	require.NoError(t, f.db.First(&c, 7).Error)
	require.Equal(t, "gpt-6-sol", c.Models, "stage must not invalidate existing channel cost evidence")
	var abilities int64
	require.NoError(t, f.db.Model(&Ability{}).Where("channel_id = ? AND model = ?", 7, "gpt-6.2-sol").Count(&abilities).Error)
	require.Zero(t, abilities, "private staging cannot expose a new route")
	return s
}

func TestZTAPIXcloudPublication20261128StageIsPrivateAndCASGuarded(t *testing.T) {
	f := xcloudStageFixture20261128(t)
	stage := stageXcloudNew20261128(t, f)
	_, err := StageZTAPIXcloud20261128(9, ZTAPIXcloudStage20261128{SourceModel: "gpt-6.2-sol", Group: "gpt-plus", ChannelID: 7}, []string{"gpt-6.2-sol"}, time.Now())
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	var original ZTAPIModelConfig
	require.NoError(t, f.db.First(&original, f.config.ID).Error)
	require.Equal(t, f.config, original)
	require.Equal(t, string(ZTAPIPricePolicyXcloud20261128), stage.Source.PricePolicy)
	var count int64
	require.NoError(t, f.db.Model(&ZTAPIModelConfig{}).Where("source_model = ?", stage.Config.SourceModel).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestZTAPIXcloudPublication20261128StageRejectsUnadvertisedOrWrongGroup(t *testing.T) {
	for _, tc := range []struct {
		name, source, group string
		channel             int
		visible             []string
		age                 time.Duration
	}{
		{"wrong_group", "gpt-6.2-sol", "new-vip", 7, []string{"gpt-6.2-sol"}, 0},
		{"unadvertised", "gpt-6.2-sol", "gpt-plus", 7, []string{"gpt-6-sol"}, 0},
		{"stale", "gpt-6.2-sol", "gpt-plus", 7, []string{"gpt-6.2-sol"}, -25 * time.Hour},
		{"removed", "gpt-5.6-sol", "gpt-plus", 7, []string{"gpt-5.6-sol"}, 0},
		{"unconfigured_test", "claude-fable-5", "test", 7, []string{"claude-fable-5"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := xcloudStageFixture20261128(t)
			_, err := StageZTAPIXcloud20261128(9, ZTAPIXcloudStage20261128{SourceModel: tc.source, Group: tc.group, ChannelID: tc.channel}, tc.visible, time.Now().Add(tc.age))
			require.Error(t, err)
			var c Channel
			require.NoError(t, f.db.First(&c, 7).Error)
			require.Equal(t, "gpt-6-sol", c.Models)
		})
	}
}

func TestZTAPIXcloudPublication20261128ReuseDraftAndRejectYunxinPublication(t *testing.T) {
	f := xcloudStageFixture20261128(t)
	config := f.config
	config.ID, config.SourceModel, config.PublicName = 137, "gpt-6.1-sol", nil
	config.ProviderFamily = ZTAPIProviderOpenAI
	require.NoError(t, f.db.Create(&config).Error)
	req := ZTAPIXcloudStage20261128{SourceModel: config.SourceModel, Group: "gpt-plus", ChannelID: 7, ConfigID: 137, Version: 1}
	stage, err := StageZTAPIXcloud20261128(9, req, []string{config.SourceModel}, time.Now())
	require.NoError(t, err)
	require.Equal(t, 137, stage.Config.ID)
	require.Equal(t, "zt-gpt-6.1-sol", stage.Config.PublicNameValue())
	// A currently published Yunxin identity is not a migration permission.
	require.NoError(t, f.db.Model(&ZTAPIModelConfig{}).Where("id = ?", 137).Updates(map[string]any{"published": true, "publication_snapshot_id": 987}).Error)
	req.Version, req.SnapshotID, req.SourceID = stage.Config.Version, 987, stage.Source.ID
	_, err = StageZTAPIXcloud20261128(9, req, []string{config.SourceModel}, time.Now())
	require.Error(t, err)
}

func TestZTAPIXcloudPublication20261128PublishRequiresFreshCompleteVerificationAndCostGate(t *testing.T) {
	f := xcloudStageFixture20261128(t)
	stage := stageXcloudNew20261128(t, f)
	v := f.verification
	v.ID, v.ModelConfigID, v.ChannelID = 0, stage.Config.ID, 7
	v.VerifiedAt = common.GetTimestamp()
	require.NoError(t, f.db.Create(&v).Error)
	next := stage.Config
	next.Published = true
	_, err := UpdateZTAPIModelConfigAndBilling(&next, next.Version, nil)
	require.ErrorContains(t, err, "controlled publisher", "generic update must not bypass protected cost or post-quote validation")
	req := ZTAPIXcloudPublish20261128{ConfigID: stage.Config.ID, Version: stage.Config.Version, SourceID: stage.Source.ID, VerificationID: v.ID}
	_, err = ApplyZTAPIXcloudPublication20261128(9, req, nil)
	require.Error(t, err, "a protected production cost check cannot be omitted")
	require.NoError(t, f.db.Model(&v).Update("usage_reconciled", false).Error)
	_, err = ApplyZTAPIXcloudPublication20261128(9, req, func(*gorm.DB) error { return nil })
	require.Error(t, err)
	require.NoError(t, f.db.Model(&v).Update("usage_reconciled", true).Error)
	result, err := ApplyZTAPIXcloudPublication20261128(9, req, func(tx *gorm.DB) error {
		var published ZTAPIModelConfig
		require.NoError(t, tx.First(&published, stage.Config.ID).Error)
		require.True(t, published.Published)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []int{7}, result.ChannelIDs())
	require.Equal(t, string(ZTAPIPricePolicyXcloud20261128), result.PricePolicy)
	_, err = ApplyZTAPIXcloudPublication20261128(9, req, func(*gorm.DB) error { return nil })
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	require.Error(t, RestoreZTAPIXcloudPublication20261128(9, stage.Config.ID, result.ModelVersion, result.ID, nil), "rollback must not bypass protected catalog cost proof")
	var unchanged ZTAPIModelConfig
	require.NoError(t, f.db.First(&unchanged, stage.Config.ID).Error)
	require.Equal(t, result.ID, unchanged.PublicationSnapshotID)
	require.True(t, unchanged.Published)
	require.NoError(t, RestoreZTAPIXcloudPublication20261128(9, stage.Config.ID, result.ModelVersion, result.ID, func(*gorm.DB) error { return nil }))
	var restored ZTAPIModelConfig
	require.NoError(t, f.db.First(&restored, stage.Config.ID).Error)
	require.False(t, restored.Published)
	require.NoError(t, f.db.First(&ZTAPIModelPublicationSnapshot{}, result.ID).Error, "in-flight snapshot must survive rollback")
}
