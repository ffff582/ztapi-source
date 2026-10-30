package model

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

type ztapiCashBasisTier20261030 struct {
	Conditions []string
	CostCNY    map[string]decimal.Decimal
}

// Costs are procurement CNY per million tokens, before platform FX, retail
// floors, customer bonuses or mixed-channel costs. This validates, never repairs.
func ztapiCashABSourceTiers20261030(base *ZTAPIModelPriceSource) ([]ztapiCashBasisTier20261030, error) {
	if base == nil || base.CashMarginBaseSourceID != 0 || base.MediaPriceContractJSON != "" {
		return nil, errors.New("cash A/B tiers require an original token procurement source")
	}
	canonical, err := ztapiCashRebuildABBase20261030(base)
	if err != nil {
		return nil, err
	}
	if base.PricePolicy != canonical.PricePolicy {
		return nil, errors.New("cash A/B procurement policy differs from the selected quotation")
	}
	if err := validateZTAPIABPriceSource(base); err != nil {
		return nil, fmt.Errorf("invalid cash A/B procurement source: %w", err)
	}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return nil, err
	}
	var row ZTAPIABQuotationEntry
	for _, entry := range quote.Entries {
		if entry.QuotationCell == canonical.QuotationCell && entry.ModelCode == canonical.QuotationModelCode && entry.Grade == canonical.QuotationGrade {
			row = entry
			break
		}
	}
	if len(row.TokenPriceRules) == 0 {
		return nil, errors.New("cash A/B procurement quotation has no token tiers")
	}
	fx, err := ztapiABFXParamsFromSource(base)
	if err != nil {
		return nil, err
	}
	maximum := map[string]decimal.Decimal{}
	tiers := make([]ztapiCashBasisTier20261030, 0, len(row.TokenPriceRules))
	for _, rule := range row.TokenPriceRules {
		tier := ztapiCashBasisTier20261030{
			Conditions: append([]string{}, rule.Conditions...),
			CostCNY:    make(map[string]decimal.Decimal, len(rule.Cost)),
		}
		for dimension, raw := range rule.Cost {
			cost, err := decimal.NewFromString(raw)
			if err != nil || !cost.IsPositive() {
				return nil, fmt.Errorf("invalid cash A/B quotation cost for %s", dimension)
			}
			switch rule.Currency {
			case "USD":
				cost = cost.Mul(fx.UpstreamUSDRate).Round(10)
			case "CNY":
			default:
				return nil, errors.New("unsupported cash A/B quotation currency")
			}
			tier.CostCNY[dimension] = cost
			if cost.GreaterThan(maximum[dimension]) {
				maximum[dimension] = cost
			}
		}
		tiers = append(tiers, tier)
	}
	dimensions, err := parseZTAPIBillingDimensions(base.BillingDimensions)
	if err != nil || len(dimensions) != len(maximum) {
		return nil, errors.New("cash A/B dimensions differ from canonical procurement")
	}
	for _, dimension := range dimensions {
		if !maximum[dimension].IsPositive() {
			return nil, fmt.Errorf("cash A/B procurement is missing %s", dimension)
		}
	}
	// Unbilled source columns must still equal canonical zero, notably embedding
	// output/cache and Fable's generic (non-TTL) cache-write column.
	for dimension, raw := range ztapiPriceSourceValues(base) {
		cost, err := decimal.NewFromString(raw)
		if err != nil || !cost.Equal(maximum[dimension]) {
			return nil, fmt.Errorf("cash A/B %s cost differs from canonical procurement", dimension)
		}
	}
	return tiers, nil
}

// Rebuild reconstructs quotation evidence, not authorization. A derived clone's
// rules (and costs/policy/grade) are not trusted; callers may copy back only the
// original rules, then validate all original fields and the persisted parent.
func ztapiCashRebuildABBase20261030(base *ZTAPIModelPriceSource) (ZTAPIModelPriceSource, error) {
	if base == nil || base.Currency != "CNY" || base.FXMode != ZTAPIFXModePlatformV1 {
		return ZTAPIModelPriceSource{}, errors.New("cash A/B reconstruction requires frozen CNY platform FX")
	}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	if base.SourceDocumentChecksum != quote.WorkbookSHA256 {
		return ZTAPIModelPriceSource{}, errors.New("cash A/B reconstruction requires the canonical workbook checksum")
	}
	frozen, err := ZTAPIQuotationEntries()
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	var identity ZTAPIABModelIdentity
	for _, claim := range bridge.Mapped {
		if claim.SourceModel == base.SourceModel {
			identity = claim
			break
		}
	}
	// The whitelist is by quoted model name. In particular, the verified GPT 4o
	// Mini pair has source gpt-4o-mini and public zt-gpt-4o-mini-2024-07-18.
	policy, grade, resource := ZTAPIPricePolicyEnterprise20Margin, "A", "\u7f51\u5173"
	switch identity.ModelName {
	case "GPT 4.1 Mini", "GPT 4.1 Nano", "GPT 4o Mini", "GPT 5 Nano",
		"Gemini 3.1 Flash Lite Preview", "Gemini 3.5 Flash",
		"Text Embedding 3 Small", "Text Embedding Ada 002":
	case "Claude Fable 5":
		policy, grade, resource = ZTAPIPricePolicyPool30Margin, "B", "\u53f7\u6c60"
	default:
		return ZTAPIModelPriceSource{}, errors.New("cash A/B procurement identity is not allowlisted")
	}
	rows, selectedPolicy, err := quote.PublishablePricingBasis(identity.ModelName)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	if len(rows) != 1 || selectedPolicy != policy || rows[0].Grade != grade || rows[0].ResourceType != resource {
		return ZTAPIModelPriceSource{}, errors.New("cash A/B selected procurement resource or grade differs from its contract")
	}
	fx, err := ztapiABFXParamsFromSource(base)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	platform, err := decimal.NewFromString(base.CNYPerUSD)
	if err != nil || !platform.Equal(fx.PlatformRate) {
		return ZTAPIModelPriceSource{}, errors.New("cash A/B source has mismatched frozen platform FX")
	}
	for _, rule := range rows[0].TokenPriceRules {
		if rule.Currency == "USD" && !fx.UpstreamUSDRate.IsPositive() {
			return ZTAPIModelPriceSource{}, ErrZTAPIUpstreamUSDRateUnset
		}
	}
	return BuildZTAPIABTextPriceSourceWithFX(quote, identity.ModelName,
		base.ModelConfigID, base.OperatorID, base.QuotationEffectiveAt, fx)
}
