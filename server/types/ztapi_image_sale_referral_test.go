package types

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageSaleQuotedEvidenceKeepsHardCapsUnknown(t *testing.T) {
	quote := ImageSaleReferralQuote{Scope: "quote_assumptions_only", QuoteOnly: true, RevenueUnit: ZTAPIImageSaleBillingUnit, QuotedCostUnit: ZTAPIImageSaleBillingUnit,
		FingerprintSHA256: strings.Repeat("a", 64), Items: []ImageSaleReferralQuoteItem{{Key: "image_sale/1K", CustomerRevenueUSD: "0.3", QuotedSupplierCostUSD: "0.2", QuoteMarginPass: true,
			AssumedTokensPerImage: map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 8192}}}}
	evidence, err := BuildZTAPIImageSaleReferralEvidence(quote, "generation", "image_sale/1K")
	require.NoError(t, err)
	raw, err := common.Marshal(evidence)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"hard_cap_known":false`)
	parsed, err := ParseZTAPIImageSaleReferralEvidence(string(raw))
	require.NoError(t, err)
	require.Equal(t, evidence, parsed)
	for _, malformed := range []string{
		strings.Replace(string(raw), `"hard_cap_known":false`, `"hard_cap_known":true`, 1),
		strings.Replace(string(raw), `"hard_cap_known":false`, `"Hard_Cap_Known":false`, 1),
		strings.Replace(string(raw), `"image_output":8192`, `"image_output":1`, 1),
		strings.Replace(string(raw), `"customer_revenue_usd":"0.3"`, `"customer_revenue_usd":"100"`, 1),
		strings.Replace(string(raw), `"quote_only":true`, `"quote_only":false`, 1),
	} {
		_, err := ParseZTAPIImageSaleReferralEvidence(malformed)
		require.Error(t, err)
	}
	dimension, err := ZTAPIImageSaleReferralDimension("generation", "image_sale/1K")
	require.NoError(t, err)
	require.Equal(t, "spec:image_sale_generation_1k/images", dimension)
}
