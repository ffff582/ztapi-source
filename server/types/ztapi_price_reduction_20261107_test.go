package types

import (
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPriceReduction20261107MediaShapeKeepsKnownPoliciesStrict(t *testing.T) {
	for _, sale := range []string{"1.2353000000", "1.1932000000"} {
		r := ZTAPIMediaPriceRule{CostUSD: map[string]string{"input_tokens": "1.0000000000"}, SaleUSD: map[string]string{"input_tokens": sale}, SourceCells: map[string]string{"input_tokens": "F100"}}
		_, err := validateZTAPIMediaPriceMaps(r, nil)
		require.NoError(t, err)
		r.SaleUSD["input_tokens"] = decimal.RequireFromString(sale).Sub(decimal.RequireFromString("0.0001")).StringFixed(10)
		_, err = validateZTAPIMediaPriceMaps(r, nil)
		require.Error(t, err)
	}
}
