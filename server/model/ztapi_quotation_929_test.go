package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPI929Opus5PriceSourceUsesPoolQuote(t *testing.T) {
	source, err := BuildZTAPI929QuotedModelPriceSource("Claude Opus 5", 53, 1, 1790890000)
	require.NoError(t, err)
	require.Equal(t, ZTAPIQuotation929SHA256, source.SourceDocumentChecksum)
	require.Equal(t, "claude-opus-5", source.SourceModel)
	require.Equal(t, "pool", source.ResourceType)
	require.Equal(t, string(ZTAPIPricePolicyPool30Margin), source.PricePolicy)
	require.Equal(t, "B", source.SpendTier)
	require.Equal(t, "zq-c-o5（hc）", source.QuotationModelCode)
	require.NoError(t, validateZTAPI929PriceSource(&source))

	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "2.2000000000", preview.CostUSD[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "11.0000000000", preview.CostUSD[ZTAPIBillingDimensionOutputTokens])
	require.Equal(t, "3.1428571429", preview.SaleUSD[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "15.7142857143", preview.SaleUSD[ZTAPIBillingDimensionOutputTokens])
}

func TestZTAPI929PriceSourceRejectsTamperedCost(t *testing.T) {
	source, err := BuildZTAPI929QuotedModelPriceSource("Claude Opus 5", 53, 1, 1790890000)
	require.NoError(t, err)
	source.InputPerMillion = "2.2000000001"
	require.Error(t, validateZTAPI929PriceSource(&source))
}

func TestZTAPI929IdentityRequiresExactClaim(t *testing.T) {
	require.NoError(t, ValidateZTAPIQuotationIdentity(
		"claude-opus-5", "zt-claude-opus-5", ZTAPIProtocolOpenAICompatible,
		ZTAPIProviderAnthropic, ZTAPIQuotation929SHA256,
	))
	require.Error(t, ValidateZTAPIQuotationIdentity(
		"claude-opus-5-hc", "zt-claude-opus-5", ZTAPIProtocolOpenAICompatible,
		ZTAPIProviderAnthropic, ZTAPIQuotation929SHA256,
	))
}
