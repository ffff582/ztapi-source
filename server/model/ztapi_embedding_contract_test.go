package model

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestZTAPIEmbeddingExactQuotationMappings(t *testing.T) {
	entries, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	want := map[string]string{"text-emb-ada-002": "text-embedding-ada-002", "text-emb-3-small": "text-embedding-3-small"}
	for _, entry := range entries {
		if source, ok := want[entry.Label]; ok {
			require.Equal(t, source, entry.SourceModel)
			require.Equal(t, "zt-"+source, entry.PublicName)
			require.Equal(t, "embedding", entry.Modality)
			require.NoError(t, ValidateZTAPIQuotationIdentity(source, entry.PublicName, ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, ZTAPIQuotationSHA256))
			delete(want, entry.Label)
		}
	}
	require.Empty(t, want)
}

func TestZTAPIEmbeddingInputOnlyPricing(t *testing.T) {
	for _, tc := range []struct {
		source, cost, sale string
		price              float64
	}{
		{"text-embedding-ada-002", "0.078", "0.1300000000", .13},
		{"text-embedding-3-small", "0.0156", "0.0260000000", .026},
	} {
		t.Run(tc.source, func(t *testing.T) {
			source := validZTAPIPriceSourceForTest()
			source.SourceModel, source.InputPerMillion, source.OutputPerMillion = tc.source, tc.cost, "0"
			source.BillingDimensions = `["input_tokens"]`
			preview, err := BuildZTAPIModelPricePreview(&source)
			require.NoError(t, err)
			require.Equal(t, tc.sale, preview.InputSaleUSDPerMillion)
			require.Equal(t, "0.0000000000", preview.OutputSaleUSDPerMillion)
			require.Equal(t, []string{"input_tokens"}, preview.BillingDimensions)
			require.NotContains(t, preview.SaleUSD, "output_tokens")
			config := ZTAPIModelConfig{SourceModel: tc.source, InputPricePerMillion: tc.price}
			require.True(t, config.HasCompletePricing(), "embedding has no output/cache/media price dimensions")
			for _, output := range []float64{1, -1, math.NaN(), math.Inf(1)} {
				config.OutputPricePerMillion = output
				require.False(t, config.HasCompletePricing())
			}
			source.OutputPerMillion, source.BillingDimensions = "1", `["input_tokens","output_tokens"]`
			require.Error(t, ValidateZTAPIModelPriceSource(&source))
		})
	}
	text := ZTAPIModelConfig{SourceModel: "gpt-5.5", InputPricePerMillion: 1}
	require.False(t, text.HasCompletePricing())
}

func TestZTAPIEmbeddingCatalogEndpoint(t *testing.T) {
	require.Equal(t, []constant.EndpointType{constant.EndpointTypeEmbeddings}, ztapiEndpointTypes(ZTAPIProtocolOpenAICompatible, "text-embedding-ada-002"))
	require.Equal(t, "input_only", ztapiBillingRule([]string{"input_tokens"}))
}

func embeddingPublicationFixture(t *testing.T) ztapiPublicationGateFixture {
	t.Helper()
	f := setupZTAPIPublicationGateFixture(t)
	source, alias := "text-embedding-ada-002", "zt-text-embedding-ada-002"
	require.NoError(t, f.db.Model(&Ability{}).Where("channel_id = ?", f.channel.Id).Update("model", source).Error)
	require.NoError(t, f.db.Model(&f.channel).Update("models", source).Error)
	require.NoError(t, f.db.Model(&ZTAPIDiscoveredModel{}).Where("snapshot_id = ?", f.snapshot.ID).Update("source_model", source).Error)
	require.NoError(t, f.db.Model(&f.identity).Updates(map[string]any{"public_name": alias, "provider_family": ZTAPIProviderOpenAI}).Error)
	require.NoError(t, f.db.Model(&f.config).Updates(map[string]any{"source_model": source, "public_name": alias, "provider_family": ZTAPIProviderOpenAI, "input_cost_per_million": .078, "output_cost_per_million": 0, "input_price_per_million": .13, "output_price_per_million": 0}).Error)
	require.NoError(t, f.db.Model(&f.price).Updates(map[string]any{"source_model": source, "input_per_million": "0.078", "output_per_million": "0", "billing_dimensions": `["input_tokens"]`}).Error)
	require.NoError(t, f.db.Delete(&f.verification).Error)
	f.verification.ID = 0
	f.verification.Modality = ZTAPIModalityEmbedding
	f.verification.StreamingRequired = false
	f.verification.StreamingPassed = false
	f.verification.PromptTokens = 3
	f.verification.CompletionTokens = 0
	f.verification.TotalTokens = 3
	require.NoError(t, f.db.Create(&f.verification).Error)
	require.NoError(t, f.db.First(&f.config, f.config.ID).Error)
	return f
}

func TestZTAPIEmbeddingPublicationAndSnapshot(t *testing.T) {
	f := embeddingPublicationFixture(t)
	// Verification modality must be durable, not inferred from a success boolean.
	require.True(t, f.db.Migrator().HasColumn(&ZTAPIModelVerification{}, "modality"))
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Empty(t, blockers)
	next := f.config
	next.Published = true
	published, err := UpdateZTAPIModelConfigAndBilling(&next, next.Version, nil)
	require.NoError(t, err)
	require.Positive(t, published.PublicationSnapshotID)
	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	require.Equal(t, "embedding", catalog[0].Modality)
	require.Equal(t, "input_only", catalog[0].BillingRule)
	require.Equal(t, []string{"input_tokens"}, catalog[0].BillingDimensions)
	require.Equal(t, "0.0000000000", catalog[0].OutputPricePerMillion)
	require.NotContains(t, catalog[0].SaleUSD, "output_tokens")
	input, output, ok, err := GetZTAPIPublishedSalePrice(published.PublicNameValue())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, .13, input)
	require.Zero(t, output)
}

func TestZTAPIEmbeddingRejectsTextVerification(t *testing.T) {
	f := embeddingPublicationFixture(t)
	require.NoError(t, f.db.Delete(&f.verification).Error)
	f.verification.ID = 0
	f.verification.Modality = ZTAPIModalityText
	require.NoError(t, f.db.Create(&f.verification).Error)
	blockers, err := ZTAPIPublicationBlockers(f.config.ID)
	require.NoError(t, err)
	require.Contains(t, blockers, ZTAPIPublicationBlockerNonStreaming)
}

func TestZTAPIEmbeddingProbeSchedulingContract(t *testing.T) {
	target := ZTAPIProbeTarget{ModelID: 1, Generation: 1, ConfigVersion: 1, SourceModel: "text-embedding-ada-002", PublicModel: "zt-text-embedding-ada-002", Protocol: "embeddings", InputNanoUSDPerMillion: 78_000_000}
	require.True(t, validZTAPIProbeTarget(target))
	target.Stream = true
	require.False(t, validZTAPIProbeTarget(target))
	target.Stream = false
	target.OutputNanoUSDPerMillion = 1
	require.False(t, validZTAPIProbeTarget(target))
}

func TestZTAPIEmbeddingBackfillDoesNotInventRatios(t *testing.T) {
	config := ZTAPIModelConfig{SourceModel: "text-embedding-ada-002", Published: true, InputPricePerMillion: .13}
	changed, err := applyZTAPIDefaultPricingRatios(&config)
	require.NoError(t, err)
	require.False(t, changed)
	require.Zero(t, config.OutputPricePerMillion)
	require.Zero(t, config.CacheReadRatio)
	require.Zero(t, config.ImageRatio)
}

func TestZTAPIEmbeddingImportPreservesInputOnlyDimensions(t *testing.T) {
	f := embeddingPublicationFixture(t)
	source := validZTAPIPriceSourceForTest()
	source.ModelConfigID, source.SourceModel = f.config.ID, f.config.SourceModel
	source.InputPerMillion, source.OutputPerMillion, source.BillingDimensions = "0.078", "0", `["input_tokens"]`
	config, _, preview, err := ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{Source: source, ExpectedVersion: f.config.Version, OperatorID: 1, Reason: "offline embedding contract"})
	require.NoError(t, err)
	require.Equal(t, .13, config.InputPricePerMillion)
	require.Zero(t, config.OutputPricePerMillion)
	require.Equal(t, []string{"input_tokens"}, preview.BillingDimensions)
}

func TestZTAPIEmbeddingPublicationKeepsEnterpriseAndHealthGates(t *testing.T) {
	for _, tc := range []string{"pool price", "health open", "fake streaming", "nonzero output usage"} {
		t.Run(tc, func(t *testing.T) {
			f := embeddingPublicationFixture(t)
			blocker := ZTAPIPublicationBlockerNonStreaming
			switch tc {
			case "pool price":
				require.NoError(t, f.db.Model(&f.price).Update("resource_type", "pool").Error)
				blocker = ZTAPIPublicationBlockerEnterprisePrice
			case "health open":
				require.NoError(t, f.db.AutoMigrate(&ZTAPIHealthState{}))
				require.NoError(t, f.db.Create(&ZTAPIHealthState{ModelID: f.config.ID, Generation: 1, Open: true}).Error)
				blocker = ZTAPIPublicationBlockerHealthCircuitOpen
			case "fake streaming":
				require.NoError(t, f.db.Model(&f.verification).Update("streaming_passed", true).Error)
			case "nonzero output usage":
				require.NoError(t, f.db.Model(&f.verification).Update("completion_tokens", 1).Error)
			}
			blockers, err := ZTAPIPublicationBlockers(f.config.ID)
			require.NoError(t, err)
			require.Contains(t, blockers, blocker)
		})
	}
}
