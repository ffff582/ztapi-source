package finance

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestImageSaleReferralQuoteUsesPerImageRevenueAndTokenCostAssumptions(t *testing.T) {
	quote, err := QuoteImageSaleReferralCosts(types.ZTAPIImageSaleContract20261109JSON(), imageSaleCostContract(t, "0.6", "1"), "generation")
	require.NoError(t, err)
	require.True(t, quote.QuoteOnly)
	require.False(t, quote.SupplierHardCapKnown)
	require.Equal(t, "quote_assumptions_only", quote.Scope)
	require.Equal(t, "usd_per_image", quote.RevenueUnit)
	require.Equal(t, types.ZTAPIMediaBillingUnitUSDPerMillionTokens, quote.SupplierPriceUnit)
	require.Len(t, quote.FingerprintSHA256, 64)
	require.True(t, quote.AllQuoteMarginsPass)
	require.Len(t, quote.Items, 5)
	wants := []struct {
		key, price, cost string
		tokens           map[string]int64
	}{
		{"image_sale/1K", "0.3", "0.0085728", map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 8192}},
		{"image_sale/2K", "0.3", "0.0085728", map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 8192}},
		{"image_sale/4K", "0.6", "0.013488", map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 16384}},
		{"image_sale/extra_prompt_block", "0.02", "0.0012", map[string]int64{"text_input": 2000}},
		{"image_sale/extra_reference_unit", "0.05", "0.0024576", map[string]int64{"image_input": 4096}},
	}
	for i, want := range wants {
		item := quote.Items[i]
		require.Equal(t, want.key, item.Key)
		require.Equal(t, want.price, item.CustomerRevenueUSD)
		require.Equal(t, want.cost, item.QuotedSupplierCostUSD)
		require.Equal(t, want.tokens, item.AssumedTokensPerImage)
		require.True(t, item.QuoteMarginPass)
	}
}

func TestImageSaleReferralQuoteChecksBaseAndEachIncrementIndependently(t *testing.T) {
	for _, tc := range []struct{ cost, sale, failed string }{
		{"22", "27.5", "image_sale/4K"},
		{"20", "25", "image_sale/extra_prompt_block"},
		{"10", "12.5", "image_sale/extra_reference_unit"},
	} {
		t.Run(tc.failed, func(t *testing.T) {
			quote, err := QuoteImageSaleReferralCosts(types.ZTAPIImageSaleContract20261109JSON(), imageSaleCostContract(t, tc.cost, tc.sale), "edit")
			require.NoError(t, err)
			require.False(t, quote.AllQuoteMarginsPass)
			for _, item := range quote.Items {
				if item.Key == tc.failed {
					require.False(t, item.QuoteMarginPass)
				}
				price := decimal.RequireFromString(item.CustomerRevenueUSD)
				cost := decimal.RequireFromString(item.QuotedSupplierCostUSD)
				want := !cost.Mul(decimal.RequireFromString("1.05")).GreaterThan(price.Mul(decimal.RequireFromString("0.85")))
				require.Equal(t, want, item.QuoteMarginPass, item.Key)
			}
		})
	}
}

func TestImageSaleReferralQuoteDoesNotRoundAwayPromptMarginBoundary(t *testing.T) {
	for _, tc := range []struct {
		cost string
		pass bool
	}{{"8.0952380952", true}, {"8.0952380953", false}} {
		t.Run(tc.cost, func(t *testing.T) {
			var contract types.ZTAPIMediaPriceContract
			require.NoError(t, common.UnmarshalJsonStr(imageSaleCostContract(t, "0.6", "1"), &contract))
			for i := range contract.Rules {
				if contract.Rules[i].ID == "text_input" {
					contract.Rules[i].CostUSD["text_input"] = tc.cost
					contract.Rules[i].SaleUSD["text_input"] = decimal.RequireFromString(tc.cost).Div(decimal.RequireFromString("0.6")).Round(10).String()
				}
			}
			cost, err := common.Marshal(contract)
			require.NoError(t, err)
			quote, err := QuoteImageSaleReferralCosts(types.ZTAPIImageSaleContract20261109JSON(), string(cost), "generation")
			require.NoError(t, err)
			require.Equal(t, tc.pass, quote.Items[3].QuoteMarginPass)
			require.Equal(t, tc.pass, quote.AllQuoteMarginsPass)
		})
	}
}

func TestImageSaleReferralQuoteFingerprintPinsRetailCostAndUnitAssumptions(t *testing.T) {
	retail := types.ZTAPIImageSaleContract20261109JSON()
	cost := imageSaleCostContract(t, "0.6", "1")
	first, err := QuoteImageSaleReferralCosts(retail, cost, "generation")
	require.NoError(t, err)
	second, err := QuoteImageSaleReferralCosts(" \n"+retail, " \n"+cost, "generation")
	require.NoError(t, err)
	require.Equal(t, first.FingerprintSHA256, second.FingerprintSHA256, "JSON whitespace is not a price change")
	second, err = QuoteImageSaleReferralCosts(retail, imageSaleCostContract(t, "1.2", "2"), "generation")
	require.NoError(t, err)
	require.NotEqual(t, first.FingerprintSHA256, second.FingerprintSHA256)
	second, err = QuoteImageSaleReferralCosts(retail, cost, "edit")
	require.NoError(t, err)
	require.NotEqual(t, first.FingerprintSHA256, second.FingerprintSHA256)
	encoded, err := common.Marshal(first)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "image_sale")
	var fields map[string]any
	require.NoError(t, common.Unmarshal(encoded, &fields))
	require.Equal(t, "usd_per_image", fields["quoted_cost_unit"])
	require.NotContains(t, string(encoded), "SupplierCostUpperBound", "quote assumptions cannot masquerade as a hard-cap attestation")
}

func TestImageSaleReferralQuoteRejectsUnknownPricesAndUnits(t *testing.T) {
	retail, cost := types.ZTAPIImageSaleContract20261109JSON(), imageSaleCostContract(t, "0.6", "1")
	for name, args := range map[string][3]string{
		"missing retail": {"", cost, "generation"}, "missing cost": {retail, "", "generation"},
		"wrong retail unit": {strings.ReplaceAll(retail, "usd_per_image", "usd_per_million_tokens"), cost, "generation"},
		"unapproved price":  {strings.ReplaceAll(retail, "0.30", "0.01"), cost, "generation"},
		"bad operation":     {retail, cost, "unknown"}, "incomplete matrix": {retail, `{"version":1,"modality":"image","rules":[]}`, "generation"},
	} {
		t.Run(name, func(t *testing.T) {
			quote, err := QuoteImageSaleReferralCosts(args[0], args[1], args[2])
			require.Error(t, err)
			require.Empty(t, quote.Items, "bad quotes cannot return partially successful readiness evidence")
			require.False(t, quote.AllQuoteMarginsPass)
		})
	}
}
