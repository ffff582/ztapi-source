package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"testing"
)

func TestZTAPIChargeDimensionsUseFrozenDecimalPriceAndExactRounding(t *testing.T) {
	result := relaycommon.ZTAPIUsageDimensionResult{Managed: true, Dimensions: []relaycommon.ZTAPIUsageDimension{
		{Dimension: "input_tokens", Quantity: 1, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateQuoted, UnitPriceUSD: "0.000000026"},
		{Dimension: "output_tokens", Quantity: 1, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateQuoted, UnitPriceUSD: "0.0000001"},
	}}
	quota, dims, err := calculateZTAPIChargeDimensions(result)
	if err != nil || quota != 1 || len(dims) != 2 {
		t.Fatalf("minimum charge: %d %+v %v", quota, dims, err)
	}
	var allocated int64
	for _, d := range dims {
		allocated += d.ChargedQuota
	}
	if allocated != int64(quota) {
		t.Fatal("rounding not allocated exactly")
	}
	result.Pending = true
	if _, _, err = calculateZTAPIChargeDimensions(result); err == nil {
		t.Fatal("unquoted usage got a price")
	}
}
