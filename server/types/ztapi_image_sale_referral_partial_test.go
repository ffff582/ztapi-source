package types

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestImageSaleReferralPartialOnlyAllowedOperationTiers(t *testing.T) {
	cost := ZTAPIMediaPriceContract{Version: 1, Modality: "image"}
	for _, bucket := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		cost.Rules = append(cost.Rules, ZTAPIMediaPriceRule{ID: bucket, Conditions: map[string]string{"token_bucket": bucket}, BillingUnit: ZTAPIMediaBillingUnitUSDPerMillionTokens, CostUSD: map[string]string{bucket: "0.6"}, SaleUSD: map[string]string{bucket: "1"}, SourceCells: map[string]string{bucket: "A1"}})
	}
	raw, err := common.Marshal(cost)
	require.NoError(t, err)
	for operation, keys := range map[string][]string{"generation": {"image_sale/1K", "image_sale/2K"}, "edit": {"image_sale/1K"}} {
		quote, err := QuoteImageSaleReferralCosts(ZTAPIImageSalePartial20261109JSON(), string(raw), operation)
		require.NoError(t, err)
		require.True(t, quote.QuoteOnly)
		require.False(t, quote.SupplierHardCapKnown)
		var actual []string
		for _, item := range quote.Items {
			actual = append(actual, item.Key)
			_, err := BuildZTAPIImageSaleReferralEvidence(quote, operation, item.Key)
			require.NoError(t, err)
		}
		require.Equal(t, keys, actual)
		legacy, err := QuoteImageSaleReferralCosts(ZTAPIImageSaleContract20261109JSON(), string(raw), operation)
		require.NoError(t, err)
		require.Len(t, legacy.Items, 5)
		require.NotEqual(t, legacy.FingerprintSHA256, quote.FingerprintSHA256)
	}
}
