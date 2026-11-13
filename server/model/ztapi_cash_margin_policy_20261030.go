package model

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ztapiCashContract20261030 struct {
	PublicName string
	Resource   string
	Channel    int
	Policy     ZTAPIPricePolicy
	Margin     string
	BasePolicy ZTAPIPricePolicy
	Channels   []int
	Extended   bool
	Image      bool
}

func ztapiCashContractFor20261030(source string) (ztapiCashContract20261030, error) {
	switch source {
	case "gemini-3-flash-preview":
		return ztapiCashContract20261030{PublicName: "zt-gemini-3-flash-preview", Resource: "enterprise", Channel: 1, Policy: ZTAPIPricePolicyCashEnterprise20261030, Margin: "0.20", BasePolicy: ZTAPIPricePolicyQuoteFrozenRetail20261019}, nil
	case "gemini-3.1-pro-preview":
		return ztapiCashContract20261030{PublicName: "zt-gemini-3.1-pro-preview", Resource: "enterprise", Channel: 2, Policy: ZTAPIPricePolicyCashPool20261030, Margin: "0.30", BasePolicy: ZTAPIPricePolicyQuoteFrozenRetail20261019}, nil
	case "kimi-k2.7-code":
		return ztapiCashContract20261030{PublicName: "zt-kimi-k2.7-code", Resource: "pool", Channel: 2, Policy: ZTAPIPricePolicyCashPool20261030, Margin: "0.30", BasePolicy: ZTAPIPricePolicyQuoteFrozenRetail20261019}, nil
	case "gpt-4.1-mini", "gpt-4.1-nano", "gpt-5-nano", "gemini-3.1-flash-lite-preview", "gemini-3.5-flash", "text-embedding-3-small", "text-embedding-ada-002":
		return ztapiCashContract20261030{PublicName: "zt-" + source, Resource: "enterprise", Channel: 1, Policy: ZTAPIPricePolicyCashEnterprise20261030, Margin: "0.20", BasePolicy: ZTAPIPricePolicyEnterprise20Margin, Extended: true}, nil
	case "gpt-4o-mini":
		return ztapiCashContract20261030{PublicName: "zt-gpt-4o-mini-2024-07-18", Resource: "enterprise", Channel: 1, Policy: ZTAPIPricePolicyCashEnterprise20261030, Margin: "0.20", BasePolicy: ZTAPIPricePolicyEnterprise20Margin, Extended: true}, nil
	case "claude-fable-5":
		return ztapiCashContract20261030{PublicName: "zt-claude-fable-5", Resource: "pool", Policy: ZTAPIPricePolicyCashPool20261030, Margin: "0.30", BasePolicy: ZTAPIPricePolicyPool30Margin, Channels: []int{2, 4}, Extended: true}, nil
	case "claude-sonnet-5":
		return ztapiCashContract20261030{PublicName: "zt-claude-sonnet-5", Resource: "pool", Policy: ZTAPIPricePolicyCashPool20261030, Margin: "0.30", BasePolicy: ZTAPIPricePolicyQuoteFrozenRetail20261019, Channels: []int{2, 4}, Extended: true}, nil
	case "gpt-image-2":
		return ztapiCashContract20261030{PublicName: "zt-gp-image-2", Resource: "enterprise", Channel: 1, Policy: ZTAPIPricePolicyCashEnterprise20261030, Margin: "0.20", BasePolicy: ZTAPIPricePolicyPoolOfficial80, Extended: true, Image: true}, nil
	}
	return ztapiCashContract20261030{}, errors.New("cash margin model is not allowlisted")
}

func (c ztapiCashContract20261030) channelIDs() []int {
	if len(c.Channels) > 0 {
		return append([]int{}, c.Channels...)
	}
	return []int{c.Channel}
}

var ztapiCashMarginSources20261030 = []string{
	"gemini-3-flash-preview", "gemini-3.1-pro-preview", "kimi-k2.7-code",
	"gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o-mini", "gpt-5-nano",
	"gemini-3.1-flash-lite-preview", "gemini-3.5-flash", "text-embedding-3-small", "text-embedding-ada-002",
	"claude-fable-5", "claude-sonnet-5",
	"gpt-image-2",
}

func isZTAPICashMarginPolicy20261030(policy string) bool {
	return policy == string(ZTAPIPricePolicyCashEnterprise20261030) || policy == string(ZTAPIPricePolicyCashPool20261030)
}

// Use exact rationals through the final upward rounding. Prices must survive
// the eight-place publication columns without falling below the approved floor.
func ztapiCashSale20261030(cost, fx decimal.Decimal, margin string, scale int32) string {
	n, _ := new(big.Rat).SetString(cost.String())
	d, _ := new(big.Rat).SetString(fx.String())
	share := decimal.NewFromInt(1).Sub(decimal.RequireFromString(margin))
	s, _ := new(big.Rat).SetString(share.String())
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	n.Quo(n, d).Mul(n, big.NewRat(105, 100)).Quo(n, s).Mul(n, new(big.Rat).SetInt(power))
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(n.Num(), n.Denom(), r)
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return decimal.NewFromBigInt(q, -scale).StringFixed(10)
}

func deriveZTAPICashMarginSource20261030(base *ZTAPIModelPriceSource, operator int) (ZTAPIModelPriceSource, error) {
	if base != nil {
		if contract, err := ztapiCashContractFor20261030(base.SourceModel); err == nil && contract.Extended {
			return deriveZTAPICashPortfolioSource20261030(base, operator)
		}
	}
	if base == nil || base.ID <= 0 || operator <= 0 || base.PricePolicy != string(ZTAPIPricePolicyQuoteFrozenRetail20261019) ||
		base.CashMarginBaseSourceID != 0 || base.SourceDocumentChecksum != ZTAPISupplierWorkbook20261019SHA256 ||
		base.Currency != "CNY" || base.FXMode != ZTAPIFXModePlatformV1 || base.MediaPriceContractJSON != "" {
		return ZTAPIModelPriceSource{}, errors.New("cash margin requires a frozen supplier procurement base")
	}
	contract, err := ztapiCashContractFor20261030(base.SourceModel)
	if err != nil || base.ResourceType != contract.Resource {
		return ZTAPIModelPriceSource{}, errors.New("cash margin procurement identity or resource mismatch")
	}
	entry, err := ztapiSupplierPriceEntryFor20261019(base.SourceModel)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	fx, err := ztapiABFXParamsFromSource(base)
	if err != nil || !fx.PlatformRate.IsPositive() || (entry.Currency == "USD" && !fx.UpstreamUSDRate.IsPositive()) {
		return ZTAPIModelPriceSource{}, errors.New("cash margin requires the original frozen FX")
	}
	maxCost := map[string]decimal.Decimal{}
	rules := make([]ztapiABFrozenSaleRule, 0, len(entry.Rules))
	for _, tier := range entry.Rules {
		rule := ztapiABFrozenSaleRule{Conditions: append([]string{}, tier.Conditions...), RetailBasisID: base.ID, Sale: map[string]string{}}
		for dimension, raw := range tier.Official {
			cost := decimal.RequireFromString(raw).Mul(decimal.RequireFromString(entry.Fraction)).Round(10)
			if entry.Currency == "USD" {
				cost = cost.Mul(fx.UpstreamUSDRate).Round(10)
			}
			rule.Sale[dimension] = ztapiCashSale20261030(cost, fx.PlatformRate, contract.Margin, 8)
			if cost.GreaterThan(maxCost[dimension]) {
				maxCost[dimension] = cost
			}
		}
		rules = append(rules, rule)
	}
	dimensions, err := parseZTAPIBillingDimensions(base.BillingDimensions)
	if err != nil || len(dimensions) != len(maxCost) {
		return ZTAPIModelPriceSource{}, errors.New("cash margin dimensions do not match canonical quotation")
	}
	for dim, raw := range ztapiPriceSourceValues(base) {
		value, err := decimal.NewFromString(raw)
		if err != nil || !value.Equal(maxCost[dim]) {
			return ZTAPIModelPriceSource{}, fmt.Errorf("cash margin %s cost differs from canonical procurement", dim)
		}
	}
	for _, dim := range dimensions {
		if !maxCost[dim].IsPositive() {
			return ZTAPIModelPriceSource{}, errors.New("cash margin missing canonical dimension")
		}
	}
	raw, err := common.Marshal(rules)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	target := *base
	target.ID, target.Version, target.CreatedAt = 0, 0, 0
	target.PricePolicy, target.SaleMultiplier = string(contract.Policy), "1.0000000000"
	target.CashMarginBaseSourceID, target.OperatorID = base.ID, operator
	target.TokenPriceRulesJSON = string(raw)
	return target, nil
}

// Shape validation is pure; trusted publication paths additionally verify the
// persisted parent through their own DB/transaction, never the global handle.
func ztapiCashMarginRules20261030(source *ZTAPIModelPriceSource) ([]ztapiABFrozenSaleRule, map[string]decimal.Decimal, error) {
	if source == nil || !isZTAPICashMarginPolicy20261030(source.PricePolicy) || source.CashMarginBaseSourceID <= 0 ||
		(source.ID > 0 && source.CashMarginBaseSourceID >= source.ID) || !ztapiSameDecimal(source.SaleMultiplier, "1") {
		return nil, nil, errors.New("invalid cash margin parent or multiplier")
	}
	if source.SourceModel == "gpt-image-2" {
		prices, err := validateZTAPICashImageShape20261030(source)
		return nil, prices, err
	}
	base := *source
	contract, err := ztapiCashContractFor20261030(source.SourceModel)
	if err != nil {
		return nil, nil, err
	}
	base.ID, base.PricePolicy, base.CashMarginBaseSourceID = source.CashMarginBaseSourceID, string(contract.BasePolicy), 0
	if contract.Extended {
		if err := reconstructZTAPICashPortfolioBase20261030(&base); err != nil {
			return nil, nil, err
		}
	}
	expected, err := deriveZTAPICashMarginSource20261030(&base, source.OperatorID)
	if err != nil {
		return nil, nil, err
	}
	if field := ztapiABPriceSourceEvidenceMismatch(source, &expected); field != "" {
		return nil, nil, fmt.Errorf("cash margin derived %s mismatch", field)
	}
	var rules []ztapiABFrozenSaleRule
	if err := common.Unmarshal([]byte(expected.TokenPriceRulesJSON), &rules); err != nil {
		return nil, nil, err
	}
	maximum := map[string]decimal.Decimal{}
	for _, rule := range rules {
		for dim, raw := range rule.Sale {
			value := decimal.RequireFromString(raw)
			if value.GreaterThan(maximum[dim]) {
				maximum[dim] = value
			}
		}
	}
	return rules, maximum, nil
}

func BuildZTAPICashMarginSource20261030(db *gorm.DB, base *ZTAPIModelPriceSource, operator int) (ZTAPIModelPriceSource, error) {
	if db == nil || base == nil || base.ID <= 0 {
		return ZTAPIModelPriceSource{}, errors.New("cash margin base is missing")
	}
	var persisted ZTAPIModelPriceSource
	if err := db.First(&persisted, base.ID).Error; err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	if base.ModelConfigID != persisted.ModelConfigID || base.QuotationEffectiveAt != persisted.QuotationEffectiveAt ||
		base.CashMarginBaseSourceID != 0 || ztapiABPriceSourceEvidenceMismatch(base, &persisted) != "" || base.MediaPriceContractJSON != persisted.MediaPriceContractJSON || !ztapiSameDecimal(base.SaleMultiplier, persisted.SaleMultiplier) {
		return ZTAPIModelPriceSource{}, errors.New("cash margin base differs from persisted evidence")
	}
	if err := validateZTAPICashMarginBase20261030(db, &persisted); err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	if persisted.SourceModel == "gpt-image-2" {
		fx, err := currentZTAPIABFXParams(db)
		if err != nil {
			return ZTAPIModelPriceSource{}, err
		}
		return deriveZTAPICashImageSource20261030(&persisted, operator, fx)
	}
	return deriveZTAPICashMarginSource20261030(&persisted, operator)
}

func ValidateZTAPICashMarginSource20261030(db *gorm.DB, source *ZTAPIModelPriceSource) error {
	if _, _, err := ztapiCashMarginRules20261030(source); err != nil {
		return err
	}
	if db == nil {
		return errors.New("cash margin validation requires a database")
	}
	var base ZTAPIModelPriceSource
	if err := db.First(&base, source.CashMarginBaseSourceID).Error; err != nil {
		return err
	}
	var expected ZTAPIModelPriceSource
	var err error
	if source.SourceModel == "gpt-image-2" {
		fx, fxErr := ztapiABFXParamsFromSource(source)
		if fxErr != nil {
			return fxErr
		}
		expected, err = deriveZTAPICashImageSource20261030(&base, source.OperatorID, fx)
	} else {
		expected, err = BuildZTAPICashMarginSource20261030(db, &base, source.OperatorID)
	}
	if err != nil {
		return err
	}
	if source.ModelConfigID != base.ModelConfigID || source.QuotationEffectiveAt != base.QuotationEffectiveAt ||
		ztapiABPriceSourceEvidenceMismatch(source, &expected) != "" || source.MediaPriceContractJSON != expected.MediaPriceContractJSON {
		return errors.New("cash margin source differs from verified parent")
	}
	return nil
}

func validateZTAPICashMarginRoute20261030(source *ZTAPIModelPriceSource, publicName string, channels []int) error {
	contract, err := ztapiCashContractFor20261030(source.SourceModel)
	if err == nil && source.SourceModel == "gemini-3.1-pro-preview" && reflect.DeepEqual(uniqueSortedInts(channels), []int{1}) &&
		publicName == contract.PublicName && source.PricePolicy == string(contract.Policy) {
		return nil
	}
	if err != nil || publicName != contract.PublicName || source.PricePolicy != string(contract.Policy) ||
		!reflect.DeepEqual(uniqueSortedInts(channels), contract.channelIDs()) {
		return errors.New("cash margin actual route does not match approved margin contract")
	}
	return nil
}
