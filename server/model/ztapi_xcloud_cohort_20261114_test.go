package model

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIXcloud20261114ExactRetailAndCosts(t *testing.T) {
	cases := []struct {
		model                            string
		inputCost, inputSale, outputSale string
		cacheRead, write5m, write1h      string
	}{
		{"claude-sonnet-5", "1.0000000000", "1.2353000000", "6.4853000000", "0.1390000000", "1.7603000000", "2.5015000000"},
		{"deepseek-v4.1-flash", "0.0420000000", "0.0655200000", "0.2340000000", "0.0023400000", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			source, err := buildZTAPIXcloudSource20261114(tc.model, 9)
			require.NoError(t, err)
			preview, err := BuildZTAPIModelPricePreview(&source)
			require.NoError(t, err)
			require.Equal(t, tc.inputCost, preview.InputCostUSDPerMillion)
			require.Equal(t, tc.inputSale, preview.InputSaleUSDPerMillion)
			require.Equal(t, tc.outputSale, preview.OutputSaleUSDPerMillion)
			require.Equal(t, tc.cacheRead, preview.SaleUSD[ZTAPIBillingDimensionCacheRead])
			require.Equal(t, tc.write5m, preview.SaleUSD[ZTAPIBillingDimensionCacheWrite5m])
			require.Equal(t, tc.write1h, preview.SaleUSD[ZTAPIBillingDimensionCacheWrite1h])
			source.TokenPriceRulesJSON = ""
			require.Error(t, ValidateZTAPIModelPriceSource(&source))
		})
	}
	_, err := buildZTAPIXcloudSource20261114("claude-opus-5-5", 9)
	require.Error(t, err)
}

func TestZTAPIXcloud20261114PublishDraftAndRestore(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	q, err := ztapiXcloudQuote20261114("deepseek-v4.1-flash")
	require.NoError(t, err)
	url, priority := "https://www.xcloudtoken.com", int64(30)
	channel := f.channel
	channel.Id, channel.BaseURL, channel.Models, channel.Priority = q.primary, &url, q.source, &priority
	require.NoError(t, f.db.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(f.db))
	config := f.config
	config.ID, config.SourceModel, config.PublicName = q.id, q.source, &q.public
	require.NoError(t, f.db.Create(&config).Error)
	identity := f.identity
	identity.ModelConfigID, identity.PublicName = config.ID, q.public
	require.NoError(t, f.db.Create(&identity).Error)
	previous := f.price
	previous.ID, previous.ModelConfigID, previous.SourceModel = 0, q.id, q.source
	require.NoError(t, f.db.Create(&previous).Error)
	v := f.verification
	v.ID, v.ModelConfigID, v.ChannelID = 0, q.id, channel.Id
	require.NoError(t, f.db.Create(&v).Error)
	req := ZTAPIXcloudPublication20261114{SourceModel: q.source, Version: config.Version, SourceID: previous.ID, VerificationID: v.ID}
	_, err = ApplyZTAPIXcloudPublication20261114(9, req)
	require.Error(t, err, "fresh discovery is required")
	var sources int64
	require.NoError(t, f.db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", q.id).Count(&sources).Error)
	require.EqualValues(t, 1, sources, "a rejected publication must not leave a new price source")
	_, err = RecordZTAPIXcloudDiscovery20261114(q.source, channel.Id, []string{f.config.SourceModel}, 9, time.Now())
	require.Error(t, err)
	_, err = RecordZTAPIXcloudDiscovery20261114(q.source, channel.Id, []string{q.source}, 9, time.Now())
	require.NoError(t, err)
	blocked := errors.New("prospective cost proof does not match")
	_, err = ApplyZTAPIXcloudPublicationWithGate20261114(9, req, func(tx *gorm.DB) error {
		publications, err := loadZTAPIActivePublications(tx)
		require.NoError(t, err)
		require.Len(t, publications, 1)
		require.Equal(t, q.public, publications[0].PublicName)
		return blocked
	})
	require.ErrorIs(t, err, blocked)
	require.NoError(t, f.db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", q.id).Count(&sources).Error)
	require.EqualValues(t, 1, sources)
	result, err := ApplyZTAPIXcloudPublication20261114(9, req)
	require.NoError(t, err)
	require.Equal(t, []int{8}, result.ChannelIDs())
	require.Equal(t, "xcloud_verified_20261114", result.PricePolicy)
	publications, err := loadZTAPIActivePublications(f.db)
	require.NoError(t, err)
	require.Len(t, publications, 1)
	require.Equal(t, q.public, publications[0].PublicName)
	_, err = ApplyZTAPIXcloudPublication20261114(9, req)
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	require.NoError(t, RestoreZTAPIXcloudPublication20261114(9, q.source, result.ModelVersion, result.ID))
	require.NoError(t, f.db.First(&config, q.id).Error)
	require.False(t, config.Published)
	require.NoError(t, f.db.First(&ZTAPIModelPublicationSnapshot{}, result.ID).Error, "in-flight snapshots remain readable")
}

func TestZTAPIXcloud20261114RouteRequiresExactSupplierProduct(t *testing.T) {
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("claude-sonnet-5", 4, "https://www.xcloudtoken.com", "claude-sonnet-5"))
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("claude-sonnet-5", 2, "https://ai.yunxinapi.com/hub", "claude-sonnet-5-hc"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-sonnet-5", 2, "https://ai.yunxinapi.com/hub", "claude-sonnet-5"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-sonnet-5", 4, "https://www.xcloudtoken.com/other", "claude-sonnet-5"))
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("deepseek-v4.1-flash", 8, "https://www.xcloudtoken.com", "deepseek-v4.1-flash"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("deepseek-v4.1-flash", 8, "https://www.xcloudtoken.com", "deepseek-v4-flash"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("deepseek-v4.1-flash", 2, "https://ai.yunxinapi.com/hub", "deepseek-v4.1-flash"))
}

func TestZTAPIXcloud20261114PriceImportRequiresControlledPublisher(t *testing.T) {
	source, err := buildZTAPIXcloudSource20261114("deepseek-v4.1-flash", 9)
	require.NoError(t, err)
	config, persisted, preview, err := ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{Source: source, OperatorID: 9})
	require.ErrorContains(t, err, "Xcloud price source requires its controlled publisher")
	require.Nil(t, config)
	require.Nil(t, persisted)
	require.Nil(t, preview)
}
