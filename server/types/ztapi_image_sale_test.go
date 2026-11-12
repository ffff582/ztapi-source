package types

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestZTAPIImageSaleContractStrict(t *testing.T) {
	raw := ZTAPIImageSaleContract20261109JSON()
	contract, err := ParseZTAPIImageSaleContract(raw)
	require.NoError(t, err)
	require.Equal(t, "0.60", contract.BasePrices["4K"])
	canonical, err := CanonicalizeZTAPIImageSaleContract(raw)
	require.NoError(t, err)
	require.Equal(t, raw, canonical)
	for _, bad := range []string{
		strings.Replace(raw, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(raw, `"version":1`, `"extra":true,"version":1`, 1),
		strings.Replace(raw, `"0.30"`, `"0.01"`, 1),
		strings.Replace(raw, `"included_prompt_tokens":2000`, `"included_prompt_tokens":-1`, 1),
	} {
		_, err := ParseZTAPIImageSaleContract(bad)
		require.Error(t, err, bad)
	}
}

func TestZTAPIImageSaleQuoteBoundaries(t *testing.T) {
	for _, tc := range []struct {
		tokens, refs, n   int64
		resolution, total string
	}{
		{0, 0, 1, "1K", "0.3"}, {2000, 1, 1, "2K", "0.3"},
		{2001, 1, 1, "1K", "0.32"}, {4000, 1, 1, "1K", "0.32"},
		{4001, 2, 2, "4K", "1.38"}, {2000, 3, 10, "2K", "4"},
	} {
		quote, err := QuoteZTAPIImageSale(ZTAPIImageSaleContract20261109JSON(), tc.resolution, tc.tokens, tc.refs, tc.n)
		require.NoError(t, err)
		require.Equal(t, tc.total, quote.TotalUSD)
		require.NoError(t, ValidateZTAPIImageSaleQuote(ZTAPIImageSaleContract20261109JSON(), quote))
		quote.TotalUSD = "0.01"
		require.Error(t, ValidateZTAPIImageSaleQuote(ZTAPIImageSaleContract20261109JSON(), quote))
	}
	for _, tc := range []struct {
		tokens, refs, n int64
		resolution      string
	}{{-1, 0, 1, "1K"}, {1, -1, 1, "1K"}, {1, 1, 0, "1K"}, {1, 1, 11, "1K"}, {1, 1, 1, "8K"}, {9223372036854775807, 1, 1, "4K"}} {
		_, err := QuoteZTAPIImageSale(ZTAPIImageSaleContract20261109JSON(), tc.resolution, tc.tokens, tc.refs, tc.n)
		require.Error(t, err)
	}
}

func TestZTAPIImageReferenceUnits(t *testing.T) {
	for _, tc := range []struct{ w, h, units int64 }{{1024, 1024, 1}, {2048, 2048, 1}, {2049, 2048, 2}, {4096, 4096, 4}, {4096, 1024, 1}} {
		units, err := ZTAPIImageReferenceUnits(tc.w, tc.h)
		require.NoError(t, err)
		require.Equal(t, tc.units, units)
	}
	for _, tc := range [][2]int64{{0, 1}, {-1, 4}, {9223372036854775807, 2}} {
		_, err := ZTAPIImageReferenceUnits(tc[0], tc[1])
		require.Error(t, err)
	}
}
