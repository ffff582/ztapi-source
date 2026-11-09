package types

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestZTAPIImageSalePartialAdmission(t *testing.T) {
	raw := ZTAPIImageSalePartial20261109JSON()
	policy, err := ParseZTAPIImageSaleContract(raw)
	require.NoError(t, err)
	require.Equal(t, 2, policy.Version)
	require.Len(t, policy.AllowedRequests, 3)
	for _, allowed := range policy.AllowedRequests {
		selector := ZTAPIImageSelector{Operation: allowed.Operation, Size: allowed.Size, Quality: allowed.Quality, N: 1}
		refs := 0
		if allowed.Operation == "edit" {
			refs = 1
		}
		require.NoError(t, ValidateZTAPIImageSaleAdmission(raw, selector, 2000, int64(refs), refs))
		quote, err := QuoteZTAPIImageSale(raw, allowed.Resolution, 2000, int64(refs), 1)
		require.NoError(t, err)
		require.Equal(t, "0.3", quote.TotalUSD)
		require.Equal(t, 2, quote.Version)
		require.NoError(t, ValidateZTAPIImageSaleQuote(raw, quote))
	}
	base := ZTAPIImageSelector{Operation: "generation", Size: "1024x1024", Quality: "low", N: 1}
	for _, tc := range []struct {
		name          string
		selector      ZTAPIImageSelector
		tokens, units int64
		refs          int
	}{
		{"size", ZTAPIImageSelector{Operation: "generation", Size: "2048x2048", Quality: "low", N: 1}, 1, 0, 0},
		{"quality", ZTAPIImageSelector{Operation: "generation", Size: "1024x1024", Quality: "high", N: 1}, 1, 0, 0},
		{"batch", ZTAPIImageSelector{Operation: "generation", Size: "1024x1024", Quality: "low", N: 2}, 1, 0, 0},
		{"prompt", base, 2001, 0, 0}, {"generation_reference", base, 1, 1, 1},
		{"edit_missing", ZTAPIImageSelector{Operation: "edit", Size: "1024x1024", Quality: "low", N: 1}, 1, 0, 0},
		{"edit_extra", ZTAPIImageSelector{Operation: "edit", Size: "1024x1024", Quality: "low", N: 1}, 1, 2, 2},
		{"edit_pixels", ZTAPIImageSelector{Operation: "edit", Size: "1024x1024", Quality: "low", N: 1}, 1, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, ValidateZTAPIImageSaleAdmission(raw, tc.selector, tc.tokens, tc.units, tc.refs))
		})
	}
	for _, args := range []struct {
		resolution       string
		tokens, units, n int64
	}{{"4K", 1, 0, 1}, {"1K", 2001, 0, 1}, {"1K", 1, 2, 1}, {"1K", 1, 0, 2}} {
		_, err := QuoteZTAPIImageSale(raw, args.resolution, args.tokens, args.units, args.n)
		require.Error(t, err)
	}
	policy.MaxImages = 2
	changed, err := common.Marshal(policy)
	require.NoError(t, err)
	_, err = ParseZTAPIImageSaleContract(string(changed))
	require.Error(t, err)
	require.NoError(t, ValidateZTAPIImageSaleAdmission(ZTAPIImageSaleContract20261109JSON(), base, 2001, 2, 2))
}

func TestZTAPIImageSaleV1FrozenJSONUnchanged(t *testing.T) {
	require.Equal(t, `{"version":1,"billing_unit":"usd_per_image","base_prices":{"1K":"0.30","2K":"0.30","4K":"0.60"},"included_prompt_tokens":2000,"prompt_block_tokens":2000,"prompt_block_price":"0.02","included_reference_units":1,"reference_unit_pixels":4194304,"reference_unit_price":"0.05"}`, ZTAPIImageSaleContract20261109JSON())
	selector := ZTAPIImageSelector{N: 0}
	require.Error(t, ValidateZTAPIImageSaleAdmission(ZTAPIImageSaleContract20261109JSON(), selector, 0, 0, 0))
}
