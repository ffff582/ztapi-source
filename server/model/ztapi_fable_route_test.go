package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIFableXcloudOnlyKeepsRetailAndHistory(t *testing.T) {
	db := reductionFixture20261107(t)
	var config ZTAPIModelConfig
	require.NoError(t, db.Where("source_model = ?", "claude-fable-5").First(&config).Error)
	selection := ApprovedZTAPIPriceReductionSelections20261107()
	for _, item := range selection {
		if item.ModelID == config.ID {
			_, err := ApplyZTAPIPriceReduction20261107(7, []ZTAPIPriceReductionSelection20261107{item}, false)
			require.NoError(t, err)
		}
	}
	require.NoError(t, db.First(&config, config.ID).Error)
	var before ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&before, config.PublicationSnapshotID).Error)
	old, err := common.Marshal(before)
	require.NoError(t, err)
	result, err := PublishZTAPIFableXcloudOnly(7, config.Version)
	require.NoError(t, err)
	require.Equal(t, []int{4}, result.ChannelIDs())
	require.Equal(t, before.InputPricePerMillion, result.InputPricePerMillion)
	require.Equal(t, before.OutputPricePerMillion, result.OutputPricePerMillion)
	require.Equal(t, before.PriceSourceID, result.PriceSourceID)
	require.Equal(t, before.TokenPriceRulesJSON, result.TokenPriceRulesJSON)
	require.Equal(t, before.CacheWritePricingMode, result.CacheWritePricingMode)
	var historical ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&historical, before.ID).Error)
	retained, err := common.Marshal(historical)
	require.NoError(t, err)
	require.Equal(t, string(old), string(retained))
	_, err = PublishZTAPIFableXcloudOnly(7, config.Version)
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	again, err := PublishZTAPIFableXcloudOnly(7, result.ModelVersion)
	require.NoError(t, err)
	require.Equal(t, result.ID, again.ID)
}
