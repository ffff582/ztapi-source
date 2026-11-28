package main

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestReviewPartialImageSaleCostsOnlyApprovedThreeItems(t *testing.T) {
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image"}
	for _, operation := range []string{"generation", "edit"} {
		for _, bucket := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
			contract.Rules = append(contract.Rules, types.ZTAPIMediaPriceRule{ID: operation + "_" + bucket,
				Conditions:  map[string]string{"image_operation": operation, "token_bucket": bucket},
				BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens, CostUSD: map[string]string{bucket: "0.6"},
				SaleUSD: map[string]string{bucket: "1"}, SourceCells: map[string]string{bucket: "A1"}})
		}
	}
	raw, err := common.Marshal(contract)
	require.NoError(t, err)
	result, err := review(string(raw))
	require.NoError(t, err)
	require.Len(t, result.Items, 3)
	for _, item := range result.Items {
		require.Equal(t, "0.3", item.CustomerRevenue)
		require.NotContains(t, item.Key, "4k")
		require.NotContains(t, item.Key, "edit_2k")
		require.NotContains(t, item.Key, "extra_")
		require.True(t, strings.HasPrefix(item.Key, "gpt-image-2/openai_compatible/channel:1/"))
		require.False(t, item.ModelMappingKnown)
		require.False(t, item.PlatformFeeKnown)
		require.Empty(t, item.SupplierCostUpperBound)
		evidence, parseErr := types.ParseZTAPIImageSaleReferralEvidence(item.Evidence)
		require.NoError(t, parseErr)
		require.True(t, evidence.QuoteOnly)
		require.False(t, evidence.HardCapKnown)
	}
}

func TestReviewPartialImageSaleRejectsMissingProcurement(t *testing.T) {
	_, err := review("{}")
	require.Error(t, err)
}
