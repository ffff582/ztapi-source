package model

import (
	"errors"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// CashMarginPreviewRequest is caller-supplied evidence, not an authorization
// to publish or a statement that any channel or quotation is current.
type CashMarginPreviewRequest struct {
	BonusRate    string
	TargetMargin string
	SaleScale    int32
	Models       []CashMarginModel
}

type CashMarginModel struct {
	Name       string
	Dimensions []string
	Channels   []CashMarginChannel
}

type CashMarginChannel struct {
	ID              string
	UpstreamModel   string
	Currency        string
	CashUSDTPerUnit string
	GroupFactor     string
	CashFactor      string
	Tiers           []CashMarginTier
}

// Input intervals are [MinInputTokens, MaxInputTokens), nil means unbounded.
// They refer to total prompt length; every channel must cover [0, infinity).
// Prices are currency units per million tokens, before the channel factors.
// Other tier axes and free or unknown prices are not supported.
type CashMarginTier struct {
	MinInputTokens uint64
	MaxInputTokens *uint64
	Source         string
	Prices         map[string]string
}

type CashMarginBlocker struct {
	Code      string
	ChannelID string
	Tier      int
	Dimension string
}

type CashMarginCostEvidence struct {
	ChannelID              string
	UpstreamModel          string
	Currency               string
	CashUSDTPerUnit        string
	GroupFactor            string
	CashFactor             string
	Tier                   int
	MinInputTokens         uint64
	MaxInputTokens         *uint64
	Source                 string
	Dimension              string
	QuotedPrice            string
	CashCostUSDTPerMillion string
}

type CashMarginModelPreview struct {
	Name                string
	FloorUSDTPerMillion map[string]string
	Costs               []CashMarginCostEvidence
	Blockers            []CashMarginBlocker
}

type CashMarginPreview struct {
	BonusRate    string
	TargetMargin string
	SaleScale    int32
	Disclaimer   string
	Models       []CashMarginModelPreview
}

var cashMarginNumber = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func cashMarginDecimal(raw string) (decimal.Decimal, error) {
	if len(raw) > 40 || !cashMarginNumber.MatchString(raw) {
		return decimal.Zero, errors.New("invalid decimal")
	}
	return decimal.NewFromString(raw)
}

func cashMarginFloor(cost, bonus, margin decimal.Decimal, scale int32) string {
	// Rational arithmetic prevents division precision from reducing the floor.
	numerator, _ := new(big.Rat).SetString(cost.Mul(decimal.NewFromInt(1).Add(bonus)).String())
	denominator, _ := new(big.Rat).SetString(decimal.NewFromInt(1).Sub(margin).String())
	value := new(big.Rat).Quo(numerator, denominator)
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	value.Mul(value, new(big.Rat).SetInt(power))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(value.Num(), value.Denom(), rem)
	if rem.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return decimal.NewFromBigInt(q, -scale).String()
}

// PreviewCashMargin never reads a database or changes catalog state. A computed
// floor is conditional on caller-declared coverage and supplier evidence.
func PreviewCashMargin(request CashMarginPreviewRequest) (CashMarginPreview, error) {
	bonus, err := cashMarginDecimal(request.BonusRate)
	if err != nil || bonus.IsNegative() || bonus.GreaterThan(decimal.NewFromInt(1)) {
		return CashMarginPreview{}, errors.New("invalid bonus rate")
	}
	margin, err := cashMarginDecimal(request.TargetMargin)
	if err != nil || margin.IsNegative() || !margin.LessThan(decimal.NewFromInt(1)) || request.SaleScale < 0 || request.SaleScale > 10 {
		return CashMarginPreview{}, errors.New("invalid margin or sale precision")
	}
	if len(request.Models) == 0 {
		return CashMarginPreview{}, errors.New("missing models")
	}
	result := CashMarginPreview{BonusRate: bonus.String(), TargetMargin: margin.String(), SaleScale: request.SaleScale,
		Disclaimer: "Conditional estimates only: assumptions, evidence, route, dimensions and tier completeness must be independently verified; fees, retries, free balances and other losses are excluded. No profit guarantee or publication authorization.",
		Models:     make([]CashMarginModelPreview, 0, len(request.Models))}
	counts := map[string]int{}
	for _, m := range request.Models {
		counts[strings.TrimSpace(m.Name)]++
	}
	for _, m := range request.Models {
		p := CashMarginModelPreview{Name: m.Name, Blockers: []CashMarginBlocker{}, Costs: []CashMarginCostEvidence{}}
		block := func(code, channel string, tier int, dim string) {
			p.Blockers = append(p.Blockers, CashMarginBlocker{Code: code, ChannelID: channel, Tier: tier, Dimension: dim})
		}
		if strings.TrimSpace(m.Name) == "" {
			block("missing_model", "", -1, "")
		}
		if counts[strings.TrimSpace(m.Name)] > 1 {
			block("duplicate_model", "", -1, "")
		}
		dims := map[string]bool{}
		for _, dim := range m.Dimensions {
			if dims[dim] {
				block("duplicate_dimension", "", -1, dim)
			}
			dims[dim] = true
			switch dim {
			case "input_tokens", "output_tokens", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h":
			default:
				block("unsupported_dimension", "", -1, dim)
			}
		}
		for _, dim := range []string{"input_tokens", "output_tokens"} {
			if !dims[dim] {
				block("missing_dimension", "", -1, dim)
			}
		}
		if dims["cache_write"] && (dims["cache_write_5m"] || dims["cache_write_1h"]) {
			block("overlapping_cache_dimensions", "", -1, "cache_write")
		}
		if dims["cache_write_5m"] != dims["cache_write_1h"] {
			block("incomplete_ttl_dimensions", "", -1, "cache_write")
		}
		if len(m.Channels) == 0 {
			block("missing_channel", "", -1, "")
		}
		seen := map[string]bool{}
		maxCosts := map[string]decimal.Decimal{}
		for _, c := range m.Channels {
			id := strings.TrimSpace(c.ID)
			if id == "" || strings.TrimSpace(c.UpstreamModel) == "" {
				block("missing_route_identity", id, -1, "")
			}
			if seen[id] {
				block("duplicate_channel", id, -1, "")
			}
			seen[id] = true
			if c.Currency != "USD" && c.Currency != "USDT" {
				block("unsupported_currency", id, -1, "")
			}
			fx, fxErr := cashMarginDecimal(c.CashUSDTPerUnit)
			group, groupErr := cashMarginDecimal(c.GroupFactor)
			factor, factorErr := cashMarginDecimal(c.CashFactor)
			validFactors := fxErr == nil && groupErr == nil && factorErr == nil && fx.IsPositive() && group.IsPositive() && factor.IsPositive() && !factor.GreaterThan(decimal.NewFromInt(1))
			if !validFactors {
				block("invalid_cost_factors", id, -1, "")
			}
			if len(c.Tiers) == 0 {
				block("tier_coverage", id, -1, "")
			}
			var next uint64
			for i, tier := range c.Tiers {
				if tier.MinInputTokens != next || (tier.MaxInputTokens == nil && i != len(c.Tiers)-1) ||
					(tier.MaxInputTokens != nil && (*tier.MaxInputTokens <= tier.MinInputTokens || i == len(c.Tiers)-1)) {
					block("tier_coverage", id, i, "")
				}
				if tier.MaxInputTokens != nil {
					next = *tier.MaxInputTokens
				}
				if strings.TrimSpace(tier.Source) == "" {
					block("missing_source", id, i, "")
				}
				priceDimensions := make([]string, 0, len(tier.Prices))
				for dim := range tier.Prices {
					priceDimensions = append(priceDimensions, dim)
				}
				sort.Strings(priceDimensions)
				for _, dim := range priceDimensions {
					if !dims[dim] {
						block("unexpected_dimension", id, i, dim)
					}
				}
				for _, dim := range m.Dimensions {
					price, priceErr := cashMarginDecimal(tier.Prices[dim])
					if priceErr != nil || !price.IsPositive() {
						block("missing_price", id, i, dim)
						continue
					}
					if !validFactors {
						continue
					}
					cost := price.Mul(group).Mul(factor).Mul(fx)
					if cost.GreaterThan(maxCosts[dim]) {
						maxCosts[dim] = cost
					}
					var upper *uint64
					if tier.MaxInputTokens != nil {
						v := *tier.MaxInputTokens
						upper = &v
					}
					p.Costs = append(p.Costs, CashMarginCostEvidence{ChannelID: id, UpstreamModel: c.UpstreamModel, Currency: c.Currency,
						CashUSDTPerUnit: fx.String(), GroupFactor: group.String(), CashFactor: factor.String(), Tier: i,
						MinInputTokens: tier.MinInputTokens, MaxInputTokens: upper, Source: tier.Source, Dimension: dim,
						QuotedPrice: price.String(), CashCostUSDTPerMillion: cost.String()})
				}
			}
		}
		if len(p.Blockers) == 0 {
			p.FloorUSDTPerMillion = make(map[string]string, len(dims))
			for dim := range dims {
				p.FloorUSDTPerMillion[dim] = cashMarginFloor(maxCosts[dim], bonus, margin, request.SaleScale)
			}
		}
		result.Models = append(result.Models, p)
	}
	return result, nil
}
