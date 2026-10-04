package service

import (
	"math"
	"sort"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/shopspring/decimal"
)

func calculateZTAPIChargeDimensions(result relaycommon.ZTAPIUsageDimensionResult) (int, []model.ZTAPISupplierRefundDimension, error) {
	if !result.Managed || result.Pending {
		return 0, nil, model.ErrZTAPISettlementPending
	}
	dims := make([]model.ZTAPISupplierRefundDimension, 0, len(result.Dimensions))
	fractions := make([]decimal.Decimal, 0, len(result.Dimensions))
	total := decimal.Zero
	var floors int64
	for _, d := range result.Dimensions {
		rate, err := decimal.NewFromString(d.UnitPriceUSD)
		if err != nil || rate.IsNegative() || d.Quantity <= 0 || d.QuoteState == relaycommon.ZTAPIQuoteStateMissing {
			return 0, nil, model.ErrZTAPISettlementPending
		}
		unitQuota := rate.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		amount := unitQuota.Mul(decimal.NewFromInt(d.Quantity))
		if amount.GreaterThan(decimal.NewFromInt(math.MaxInt32)) {
			return 0, nil, model.ErrBalanceLedgerOverflow
		}
		floor := amount.Floor().IntPart()
		dims = append(dims, model.ZTAPISupplierRefundDimension{Dimension: d.Dimension, Units: strconv.FormatInt(d.Quantity, 10), UnitQuota: unitQuota.String(), ChargedQuota: floor})
		fractions = append(fractions, amount.Sub(decimal.NewFromInt(floor)))
		floors += floor
		total = total.Add(amount)
	}
	quota := total.Round(0).IntPart()
	if quota == 0 && total.IsPositive() {
		quota = 1
	}
	if quota > math.MaxInt32 {
		return 0, nil, model.ErrBalanceLedgerOverflow
	}
	indices := make([]int, len(dims))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool { return fractions[indices[i]].GreaterThan(fractions[indices[j]]) })
	for i := int64(0); i < quota-floors; i++ {
		if i >= int64(len(indices)) {
			return 0, nil, model.ErrZTAPISettlementInvalid
		}
		dims[indices[i]].ChargedQuota++
	}
	return int(quota), dims, nil
}
