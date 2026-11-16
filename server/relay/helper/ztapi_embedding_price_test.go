package helper

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIEmbeddingPriceHelperAcceptsRealInputOnlyQuote(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{OriginModelName: "zt-text-embedding-3-small", ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
		PublicName: "zt-text-embedding-3-small", SourceModel: "text-embedding-3-small", Modality: "embedding",
		PriceSourceID: 12, PriceSourceVersion: 2,
		BillingDimensions: []string{"input_tokens"}, SaleUSD: map[string]string{"input_tokens": "0.05"},
		InputPricePerMillion: 0.05,
	}}
	price, err := ModelPriceHelper(c, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, 0.025, price.ModelRatio)
	require.Zero(t, price.CompletionRatio)
	require.False(t, price.UsePrice)
	require.NotContains(t, info.ZTAPIPublicationSnapshot.SaleUSD, "output_tokens")
}

func TestZTAPIEmbeddingPriceHelperDoesNotAllowUnquotedOrMismatchedInput(t *testing.T) {
	for _, tc := range []struct {
		name, modality, price string
		sourceID              int64
	}{
		{"text_still_requires_output", "text", "0.05", 12},
		{"no_provenance", "embedding", "0.05", 0},
		{"missing_input", "embedding", "", 12},
		{"mismatched_input", "embedding", "0.10", 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{OriginModelName: "zt-text-embedding-3-small", ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
				PublicName: "zt-text-embedding-3-small", Modality: tc.modality,
				PriceSourceID: tc.sourceID, PriceSourceVersion: 2,
				BillingDimensions: []string{"input_tokens"}, SaleUSD: map[string]string{"input_tokens": tc.price},
				InputPricePerMillion: 0.05,
			}}
			_, err := ModelPriceHelper(c, info, 1000, &types.TokenCountMeta{})
			require.Error(t, err)
		})
	}
}
