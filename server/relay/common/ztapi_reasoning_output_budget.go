package common

import (
	"errors"
	"fmt"
	"math"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/shopspring/decimal"
)

// ZTAPIReasoningOutputReservationQuota uses only the request's frozen quote.
func ZTAPIReasoningOutputReservationQuota(snapshot *ZTAPIPublicationSnapshot, promptTokens, outputBudget int) (int, error) {
	if snapshot == nil || outputBudget <= 0 || basecommon.QuotaPerUnit <= 0 ||
		math.IsNaN(basecommon.QuotaPerUnit) || math.IsInf(basecommon.QuotaPerUnit, 0) {
		return 0, errors.New("reasoning output reservation requires a valid frozen quote and budget")
	}
	selected, _, err := SelectZTAPIFrozenTokenTier(snapshot, &dto.Usage{
		PromptTokens: max(0, promptTokens), CompletionTokens: outputBudget,
	})
	if err != nil {
		return 0, err
	}
	prices := make([]decimal.Decimal, 0, 2)
	for i, name := range []string{"input_tokens", "output_tokens"} {
		var price decimal.Decimal
		if len(selected.SaleUSD) > 0 {
			price, err = decimal.NewFromString(selected.SaleUSD[name])
			if err != nil || price.IsNegative() {
				return 0, fmt.Errorf("frozen output reservation lacks a valid %s price", name)
			}
		} else {
			fallback := []float64{selected.InputPricePerMillion, selected.OutputPricePerMillion}[i]
			if fallback <= 0 || math.IsNaN(fallback) || math.IsInf(fallback, 0) {
				return 0, fmt.Errorf("frozen output reservation lacks a valid %s price", name)
			}
			price = decimal.NewFromFloat(fallback)
		}
		prices = append(prices, price)
	}
	quota := decimal.NewFromInt(int64(max(promptTokens, basecommon.PreConsumedQuota))).Mul(prices[0]).
		Add(decimal.NewFromInt(int64(outputBudget)).Mul(prices[1])).
		Mul(decimal.NewFromFloat(basecommon.QuotaPerUnit)).Div(decimal.NewFromInt(1_000_000)).Ceil()
	if quota.IsNegative() || quota.GreaterThan(decimal.NewFromInt(int64(^uint(0)>>1))) {
		return 0, errors.New("reasoning output reservation exceeds supported quota")
	}
	return int(quota.IntPart()), nil
}
