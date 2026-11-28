package finance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func xcloudSource20261128(t *testing.T) model.ZTAPIModelPriceSource {
	t.Helper()
	public := "zt-gpt-6.2-sol"
	source, err := model.BuildZTAPIXcloudPriceSource20261128(model.ZTAPIModelConfig{ID: 901, SourceModel: "gpt-6.2-sol", PublicName: &public,
		ProviderFamily: model.ZTAPIProviderOpenAI, Protocol: model.ZTAPIProtocolOpenAICompatible}, "gpt-plus", 9, "")
	require.NoError(t, err)
	source.ID, source.Version, source.CreatedAt = 9901, 1, model.ZTAPIXcloudQuoteObservedAt20261128
	return source
}

func TestFinanceXcloud20261128UsesOnlyFrozenExactSource(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*model.ZTAPIModelPriceSource)
		want   string
	}{
		{"valid", nil, "0.0015225"},
		{"wrong version", func(s *model.ZTAPIModelPriceSource) { s.Version++ }, ""},
		{"wrong rate", func(s *model.ZTAPIModelPriceSource) { s.InputPerMillion = "0.01" }, ""},
		{"wrong group", func(s *model.ZTAPIModelPriceSource) { s.SpendTier = "new-vip" }, ""},
		{"future source", func(s *model.ZTAPIModelPriceSource) { s.CreatedAt += 2 }, ""},
	} {
		t.Run(change.name, func(t *testing.T) {
			source := xcloudSource20261128(t)
			raw, err := json.Marshal(map[string]any{"SourceModel": source.SourceModel, "PublicName": "zt-gpt-6.2-sol", "PublicationID": source.ModelConfigID,
				"PricePolicy": source.PricePolicy, "PriceSourceID": source.ID, "PriceSourceVersion": source.Version,
				"TokenPriceRulesJSON": source.TokenPriceRulesJSON, "AllowedChannelIDs": []int{7}})
			require.NoError(t, err)
			settlement := Settlement{PublicModel: "zt-gpt-6.2-sol", PriceSnapshotJSON: string(raw), CreatedAt: time.Unix(source.CreatedAt+1, 0)}
			if change.mutate != nil {
				change.mutate(&source)
			}
			result, err := estimateFrozenXcloudUsage(settlement, 7, "https://www.xcloudtoken.com", []UsageDimension{{"input_tokens", 1000}, {"output_tokens", 100}, {"cache_read", 200}, {"cache_write", 10}}, map[int64]model.ZTAPIModelPriceSource{source.ID: source})
			if change.want == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, change.want, result.CostUSDT)
			require.True(t, result.IsEstimate)
			require.Equal(t, "0.5", result.DiscountFactor)
		})
	}
}

func TestFinanceReaderLoadsBothImmutableXcloudGenerations(t *testing.T) {
	db := readerSnapshotFixture(t)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelPriceSource{}))
	current := xcloudSource20261128(t)
	require.NoError(t, db.Create(&current).Error)
	legacy, err := model.ZTAPIXcloudPriceEvidence20261114("claude-opus-5-5", 9)
	require.NoError(t, err)
	legacy.Version, legacy.CreatedAt = 1, moment("2026-11-14T00:00:00Z").Unix()
	require.NoError(t, db.Create(&legacy).Error)
	reader := &Reader{DB: db}
	result, err := reader.Load(context.Background(), moment("2026-11-29T00:00:00Z"))
	require.NoError(t, err)
	require.Len(t, result.XcloudPriceSources, 2)
	policies := map[string]bool{}
	for _, row := range result.XcloudPriceSources {
		policies[row.PricePolicy] = true
	}
	require.True(t, policies[string(model.ZTAPIPricePolicyXcloud20261114)])
	require.True(t, policies[string(model.ZTAPIPricePolicyXcloud20261128)])
}
