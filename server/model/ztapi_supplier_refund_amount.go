package model

import (
	"errors"
	"math"
	"math/big"
	"regexp"
	"sort"
)

// ChargedQuota includes the explicit original settlement rounding allocation.
// Units and UnitQuota are finite base-10 strings, never supplier dollar amounts.
type ZTAPISupplierRefundDimension struct {
	Dimension         string `json:"dimension"`
	Units             string `json:"units"`
	UnitQuota         string `json:"unit_quota"`
	ChargedQuota      int64  `json:"charged_quota"`
	TokenChargedQuota int64  `json:"token_charged_quota"`
}

type ZTAPISupplierRefundUnits struct {
	Dimension string `json:"dimension"`
	Units     string `json:"units"`
}

type ZTAPISupplierRefundProgress struct {
	Dimension  string `json:"dimension"`
	Units      string `json:"units"`
	Quota      int64  `json:"quota"`
	TokenQuota int64  `json:"token_quota"`
}

type ztapiSupplierRefundPlan struct {
	Quota      int64
	TokenQuota int64
	Progress   []ZTAPISupplierRefundProgress
}

var ErrZTAPISupplierRefundEvidence = errors.New("supplier refund requires unambiguous original charge dimensions and reversal units")

var ztapiSupplierRefundDecimal = regexp.MustCompile(`^[0-9]{1,30}(\.[0-9]{1,18})?$`)

func ztapiSupplierRefundNumber(value string) (*big.Rat, bool) {
	if !ztapiSupplierRefundDecimal.MatchString(value) {
		return nil, false
	}
	return new(big.Rat).SetString(value)
}

func ztapiSupplierRefundTarget(units, original, rate *big.Rat, allocated int64) int64 {
	if units.Cmp(original) == 0 {
		return allocated
	}
	amount := new(big.Rat).Mul(units, rate)
	floor := new(big.Int).Quo(amount.Num(), amount.Denom())
	if !floor.IsInt64() || floor.Int64() > allocated {
		return allocated
	}
	return floor.Int64()
}

func planZTAPISupplierRefund(charge, tokenCharge int64, dimensions []ZTAPISupplierRefundDimension, previous []ZTAPISupplierRefundProgress, mode string, lines []ZTAPISupplierRefundUnits) (ztapiSupplierRefundPlan, error) {
	bad := func() (ztapiSupplierRefundPlan, error) {
		return ztapiSupplierRefundPlan{}, ErrZTAPISupplierRefundEvidence
	}
	if charge < 0 || charge > math.MaxInt32 || tokenCharge < 0 || tokenCharge > charge ||
		(mode != "full" && mode != "partial") || (mode == "full" && len(lines) > 0) || (mode == "partial" && len(lines) == 0) {
		return bad()
	}
	if len(dimensions) == 0 {
		if charge == 0 && tokenCharge == 0 && mode == "full" && len(previous) == 0 {
			return ztapiSupplierRefundPlan{}, nil
		}
		return bad()
	}
	dims := make(map[string]ZTAPISupplierRefundDimension)
	progress := make(map[string]ZTAPISupplierRefundProgress)
	var total, tokenTotal int64
	for _, d := range dimensions {
		units, ok := ztapiSupplierRefundNumber(d.Units)
		rate, rateOK := ztapiSupplierRefundNumber(d.UnitQuota)
		if d.Dimension == "" || len(d.Dimension) > 128 || !ok || units.Sign() <= 0 || !rateOK ||
			d.ChargedQuota < 0 || d.ChargedQuota > charge || d.TokenChargedQuota < 0 || d.TokenChargedQuota > d.ChargedQuota {
			return bad()
		}
		if _, duplicate := dims[d.Dimension]; duplicate {
			return bad()
		}
		product := new(big.Rat).Mul(units, rate)
		floor := new(big.Int).Quo(product.Num(), product.Denom())
		if !floor.IsInt64() || floor.Int64() > d.ChargedQuota || d.ChargedQuota-floor.Int64() > 1 {
			return bad()
		}
		dims[d.Dimension] = d
		total += d.ChargedQuota
		tokenTotal += d.TokenChargedQuota
	}
	if total != charge || tokenTotal != tokenCharge {
		return bad()
	}
	for _, p := range previous {
		d, exists := dims[p.Dimension]
		units, ok := ztapiSupplierRefundNumber(p.Units)
		original, _ := ztapiSupplierRefundNumber(d.Units)
		if !exists || !ok || units.Cmp(original) > 0 || p.Quota < 0 || p.Quota > d.ChargedQuota || p.TokenQuota < 0 || p.TokenQuota > d.TokenChargedQuota {
			return bad()
		}
		if _, duplicate := progress[p.Dimension]; duplicate {
			return bad()
		}
		rate, _ := ztapiSupplierRefundNumber(d.UnitQuota)
		if p.Quota != ztapiSupplierRefundTarget(units, original, rate, d.ChargedQuota) {
			return bad()
		}
		if d.TokenChargedQuota == d.ChargedQuota && p.TokenQuota != p.Quota || d.TokenChargedQuota == 0 && p.TokenQuota != 0 {
			return bad()
		}
		progress[p.Dimension] = p
	}
	requested := make(map[string]*big.Rat)
	for _, line := range lines {
		units, ok := ztapiSupplierRefundNumber(line.Units)
		if _, exists := dims[line.Dimension]; !exists || !ok || units.Sign() <= 0 {
			return bad()
		}
		if _, duplicate := requested[line.Dimension]; duplicate {
			return bad()
		}
		requested[line.Dimension] = units
	}
	plan := ztapiSupplierRefundPlan{}
	for name, d := range dims {
		old := progress[name]
		if old.Units == "" {
			old = ZTAPISupplierRefundProgress{Dimension: name, Units: "0"}
		}
		units, _ := ztapiSupplierRefundNumber(old.Units)
		original, _ := ztapiSupplierRefundNumber(d.Units)
		rate, _ := ztapiSupplierRefundNumber(d.UnitQuota)
		if mode == "full" {
			units.Set(original)
		} else if delta, exists := requested[name]; exists {
			units.Add(units, delta)
		}
		if units.Cmp(original) > 0 {
			return bad()
		}
		quota := ztapiSupplierRefundTarget(units, original, rate, d.ChargedQuota)
		tokenQuota := int64(0)
		if d.TokenChargedQuota == d.ChargedQuota {
			tokenQuota = quota
		} else if units.Cmp(original) == 0 {
			tokenQuota = d.TokenChargedQuota
		} else if d.TokenChargedQuota != 0 && requested[name] != nil {
			return bad()
		} else {
			tokenQuota = old.TokenQuota
		}
		if quota < old.Quota || tokenQuota < old.TokenQuota {
			return bad()
		}
		plan.Quota += quota - old.Quota
		plan.TokenQuota += tokenQuota - old.TokenQuota
		plan.Progress = append(plan.Progress, ZTAPISupplierRefundProgress{Dimension: name, Units: units.FloatString(18), Quota: quota, TokenQuota: tokenQuota})
	}
	sort.Slice(plan.Progress, func(i, j int) bool { return plan.Progress[i].Dimension < plan.Progress[j].Dimension })
	return plan, nil
}
