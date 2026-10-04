package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIRuntimeQuoteBindsPublishedPriceVersionAndCopies(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	var published ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&published, config.PublicationSnapshotID).Error)
	var bound ZTAPIModelPriceSource
	require.NoError(t, db.First(&bound, published.PriceSourceID).Error)
	later := bound
	later.ID, later.Version = 0, bound.Version+1
	later.InputPerMillion, later.OutputPerMillion = "30", "60"
	require.NoError(t, db.Create(&later).Error)

	first, err := GetZTAPIRuntimePublication("zt-gpt-5.5")
	require.NoError(t, err)
	require.Equal(t, bound.ID, first.PriceSourceID)
	require.Equal(t, bound.Version, first.PriceSourceVersion)
	require.Equal(t, "text", first.Modality)
	require.Equal(t, "1.6666666667", first.SaleUSD["input_tokens"])
	first.SaleUSD["input_tokens"] = "99"
	first.BillingDimensions[0] = "unquoted"
	second, err := GetZTAPIRuntimePublication("zt-gpt-5.5")
	require.NoError(t, err)
	require.Equal(t, "1.6666666667", second.SaleUSD["input_tokens"])
	require.Equal(t, []string{"input_tokens", "output_tokens"}, second.BillingDimensions)
	require.Equal(t, bound.ID, second.PriceSourceID)
	require.Equal(t, bound.Version, second.PriceSourceVersion)
}
