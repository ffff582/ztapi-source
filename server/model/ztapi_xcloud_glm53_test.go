package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestXcloudGLM53PriceContract(t *testing.T) {
	source := ZTAPIModelPriceSource{
		ModelConfigID: 29, SourceModel: "glm-5.3", ResourceType: "pool",
		PricePolicy: string(ZTAPIPricePolicyXcloudGLM53), SpendTier: "xcloud-cn",
		Currency: "USD", BillingDimensions: `["cache_read","input_tokens","output_tokens"]`,
		InputPerMillion: "2.4", OutputPerMillion: "8.4", CacheReadPerMillion: "0.6",
		CacheWritePerMillion: "0", CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
		SaleMultiplier: "1", QuotationEffectiveAt: 1793318400, OperatorID: 1,
		SourceDocumentChecksum: ZTAPIXcloudGLM53EvidenceSHA256,
	}
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "3.1500000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "11.0250000000", preview.OutputSaleUSDPerMillion)
	require.Equal(t, "0.7875000000", preview.SaleUSD[ZTAPIBillingDimensionCacheRead])
	require.True(t, ztapiPublicationPriceBasisPermittedTx(nil, &source))
	require.NoError(t, ValidateZTAPIQuotationIdentity("glm-5.3", "zt-glm-5.3", ZTAPIProtocolOpenAICompatible, ZTAPIProviderGLM, ZTAPIXcloudGLM53EvidenceSHA256))
	for name, mutate := range map[string]func(*ZTAPIModelPriceSource){
		"cost":          func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "0.1" },
		"discount":      func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "0.9" },
		"model":         func(s *ZTAPIModelPriceSource) { s.SourceModel = "glm-5.2" },
		"checksum":      func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = ZTAPIQuotationSHA256 },
		"missing_cache": func(s *ZTAPIModelPriceSource) { s.BillingDimensions = `["input_tokens","output_tokens"]` },
	} {
		t.Run(name, func(t *testing.T) {
			bad := source
			mutate(&bad)
			require.Error(t, ValidateZTAPIModelPriceSource(&bad))
			require.False(t, ztapiPublicationPriceBasisPermittedTx(nil, &bad))
		})
	}
	require.Error(t, ValidateZTAPIQuotationIdentity("glm-5.3", "zt-glm-5.3", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOther, ZTAPIXcloudGLM53EvidenceSHA256))
}

func TestXcloudGLM53PublicationAndWarmRead(t *testing.T) {
	f := setupZTAPIPublicationGateFixture(t)
	url := "https://www.xcloudtoken.com"
	channel := f.channel
	channel.Id, channel.BaseURL, channel.Models = 8, &url, "glm-5.3"
	require.NoError(t, f.db.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(f.db))
	alias := "zt-glm-5.3"
	f.config.SourceModel, f.config.PublicName, f.config.ProviderFamily = "glm-5.3", &alias, ZTAPIProviderGLM
	f.config.InputCostPerMillion, f.config.OutputCostPerMillion = 2.4, 8.4
	f.config.InputPricePerMillion, f.config.OutputPricePerMillion = 3.15, 11.025
	f.config.CacheReadRatio = .25
	require.NoError(t, f.db.Save(&f.config).Error)
	require.NoError(t, f.db.Model(&f.identity).Updates(map[string]any{"public_name": alias, "provider_family": ZTAPIProviderGLM}).Error)
	source := ZTAPIModelPriceSource{
		ModelConfigID: f.config.ID, SourceModel: "glm-5.3", ResourceType: "pool", PricePolicy: string(ZTAPIPricePolicyXcloudGLM53),
		SpendTier: "xcloud-cn", Currency: "USD", BillingDimensions: `["cache_read","input_tokens","output_tokens"]`,
		InputPerMillion: "2.4", OutputPerMillion: "8.4", CacheReadPerMillion: "0.6", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0", ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0",
		CNYPerUSD: "0", QuotationEffectiveAt: time.Now().Unix(), OperatorID: 1,
		SourceDocumentChecksum: ZTAPIXcloudGLM53EvidenceSHA256,
	}
	imported, persisted, preview, err := ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{
		Source: source, ExpectedVersion: f.config.Version, OperatorID: 1, Reason: "xcloud pricing test",
	})
	require.NoError(t, err)
	require.Equal(t, "3.1500000000", preview.InputSaleUSDPerMillion)
	require.True(t, ztapiSameDecimal("1", persisted.SaleMultiplier))
	f.config = *imported
	s := ZTAPIDiscoverySnapshot{ChannelID: 8, ModelListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ModelCount: 1, FetchedAt: time.Now().Unix()}
	require.NoError(t, f.db.Create(&s).Error)
	require.NoError(t, f.db.Create(&ZTAPIDiscoveredModel{SnapshotID: s.ID, ChannelID: 8, SourceModel: "glm-5.3"}).Error)
	v := f.verification
	v.ID, v.ChannelID, v.VerifiedAt = 0, 8, time.Now().Unix()
	require.NoError(t, f.db.Create(&v).Error)
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Empty(t, blockers)
	next := f.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, f.config.Version, nil)
	require.NoError(t, err)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, f.db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.Equal(t, "[8]", snapshot.AllowedChannelIDs)
	require.Equal(t, 3.15, snapshot.InputPricePerMillion)
	require.Equal(t, 11.025, snapshot.OutputPricePerMillion)
	require.Equal(t, .25, snapshot.CacheReadRatio)
	checksums, err := ztapiQuotationChecksums(f.db, []int64{snapshot.ID})
	require.NoError(t, err)
	require.Equal(t, ZTAPIXcloudGLM53EvidenceSHA256, checksums[snapshot.ID])
}
