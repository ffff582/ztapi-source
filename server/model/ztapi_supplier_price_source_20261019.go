package model

import (
	_ "embed"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const ZTAPISupplierWorkbook20261019SHA256 = "16171b71f8da8c8f52744f182278fa6ea02db61660bd54f6c7b61e9da990e9aa"

//go:embed ztapi_supplier_prices_20261019.json
var ztapiSupplierPrices20261019JSON []byte

type ztapiSupplierPriceRule20261019 struct {
	Conditions []string          `json:"conditions"`
	Official   map[string]string `json:"official"`
}

type ztapiSupplierPriceEntry20261019 struct {
	SourceModel   string                           `json:"source_model"`
	ModelCode     string                           `json:"model_code"`
	Grade         string                           `json:"grade"`
	Fraction      string                           `json:"fraction"`
	QuotationCell string                           `json:"quotation_cell"`
	OfficialURL   string                           `json:"official_url"`
	Currency      string                           `json:"currency"`
	RetailRule    string                           `json:"retail_rule"`
	Rules         []ztapiSupplierPriceRule20261019 `json:"rules"`
}

func ztapiSupplierPriceEntryFor20261019(sourceModel string) (ztapiSupplierPriceEntry20261019, error) {
	var manifest struct {
		WorkbookSHA256 string                            `json:"workbook_sha256"`
		Entries        []ztapiSupplierPriceEntry20261019 `json:"entries"`
	}
	if err := common.Unmarshal(ztapiSupplierPrices20261019JSON, &manifest); err != nil {
		return ztapiSupplierPriceEntry20261019{}, err
	}
	if manifest.WorkbookSHA256 != ZTAPISupplierWorkbook20261019SHA256 {
		return ztapiSupplierPriceEntry20261019{}, errors.New("supplier workbook checksum does not match 2026-10-19")
	}
	var found *ztapiSupplierPriceEntry20261019
	for i := range manifest.Entries {
		entry := &manifest.Entries[i]
		if entry.SourceModel == sourceModel {
			if found != nil {
				return ztapiSupplierPriceEntry20261019{}, errors.New("duplicate supplier quotation identity")
			}
			found = entry
		}
	}
	if found == nil {
		return ztapiSupplierPriceEntry20261019{}, ErrZTAPIQuotationModelNotQuoted
	}
	fraction, err := decimal.NewFromString(found.Fraction)
	if err != nil || !fraction.IsPositive() || fraction.GreaterThan(decimal.NewFromInt(1)) ||
		(found.Grade != "A" && found.Grade != "B") || found.ModelCode == "" || found.QuotationCell == "" || found.OfficialURL == "" ||
		(found.Currency != "USD" && found.Currency != "CNY") ||
		(found.RetailRule != "preserve" && found.RetailRule != "official_78") || len(found.Rules) == 0 {
		return ztapiSupplierPriceEntry20261019{}, errors.New("incomplete supplier quotation evidence")
	}
	seen := make(map[string]bool, len(found.Rules))
	for _, rule := range found.Rules {
		key, _ := common.Marshal(rule.Conditions)
		if seen[string(key)] || len(rule.Official) == 0 {
			return ztapiSupplierPriceEntry20261019{}, errors.New("duplicate or empty supplier quotation tier")
		}
		seen[string(key)] = true
		for _, condition := range rule.Conditions {
			if !ztapiABTokenConditionSupported(condition) {
				return ztapiSupplierPriceEntry20261019{}, fmt.Errorf("unsupported supplier condition %q", condition)
			}
		}
		for dim, raw := range rule.Official {
			price, parseErr := decimal.NewFromString(raw)
			if parseErr != nil || !price.IsPositive() || !ztapiSupplierTokenDimension20261019(dim) {
				return ztapiSupplierPriceEntry20261019{}, fmt.Errorf("invalid official supplier price for %s", dim)
			}
		}
	}
	return *found, nil
}

func ztapiSupplierTokenDimension20261019(dim string) bool {
	switch dim {
	case ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens,
		ZTAPIBillingDimensionCacheRead, ZTAPIBillingDimensionCacheWrite5m, ZTAPIBillingDimensionCacheWrite1h:
		return true
	}
	return false
}

// Draft sources have no ID yet; persisted sources must only reference an older,
// non-supplier source. The anchor is always loaded from the caller's database.
func ztapiSupplierRetailAnchor20261019(db *gorm.DB, source *ZTAPIModelPriceSource) (*ZTAPIModelPriceSource, error) {
	if db == nil || source == nil {
		return nil, errors.New("supplier retail anchor requires a source and database")
	}
	rules, _, err := ztapiSupplierFrozenRules20261019(source)
	if err != nil {
		return nil, err
	}
	basisID := rules[0].RetailBasisID
	if source.ID < 0 || (source.ID > 0 && basisID >= source.ID) {
		return nil, errors.New("supplier retail anchor must precede the new source")
	}
	var anchor ZTAPIModelPriceSource
	if err := db.First(&anchor, basisID).Error; err != nil {
		return nil, fmt.Errorf("load supplier retail anchor: %w", err)
	}
	if anchor.ID <= 0 || anchor.ModelConfigID != source.ModelConfigID || anchor.SourceModel != source.SourceModel ||
		anchor.PricePolicy == string(ZTAPIPricePolicyQuoteFrozenRetail20261019) ||
		anchor.SourceDocumentChecksum == ZTAPISupplierWorkbook20261019SHA256 {
		return nil, errors.New("supplier retail anchor has a different identity or is not an original source")
	}
	return &anchor, nil
}

func ztapiSupplierFrozenRules20261019(source *ZTAPIModelPriceSource) ([]ztapiABFrozenSaleRule, map[string]decimal.Decimal, error) {
	if source == nil || source.SourceDocumentChecksum != ZTAPISupplierWorkbook20261019SHA256 ||
		source.PricePolicy != string(ZTAPIPricePolicyQuoteFrozenRetail20261019) || source.MediaPriceContractJSON != "" {
		return nil, nil, errors.New("supplier frozen retail policy requires the exact 2026-10-19 token quotation")
	}
	multiplier, err := decimal.NewFromString(source.SaleMultiplier)
	if err != nil || !multiplier.Equal(decimal.NewFromInt(1)) {
		return nil, nil, errors.New("supplier frozen retail multiplier must be one")
	}
	entry, err := ztapiSupplierPriceEntryFor20261019(source.SourceModel)
	if err != nil {
		return nil, nil, err
	}
	var rules []ztapiABFrozenSaleRule
	if err := common.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules); err != nil || len(rules) != len(entry.Rules) {
		return nil, nil, errors.New("supplier frozen retail rules do not match quotation tiers")
	}
	dims, err := parseZTAPIBillingDimensions(source.BillingDimensions)
	if err != nil {
		return nil, nil, err
	}
	maxSale := map[string]decimal.Decimal{}
	basisID := int64(0)
	for tier, rule := range rules {
		if rule.RetailBasisID <= 0 || (basisID != 0 && basisID != rule.RetailBasisID) {
			return nil, nil, errors.New("supplier frozen retail tiers require one positive retail basis ID")
		}
		basisID = rule.RetailBasisID
		if !reflect.DeepEqual(rule.Conditions, entry.Rules[tier].Conditions) || len(rule.Sale) != len(entry.Rules[tier].Official) {
			return nil, nil, errors.New("supplier frozen retail tier conditions or dimensions differ from quotation")
		}
		for dim := range entry.Rules[tier].Official {
			sale, parseErr := decimal.NewFromString(rule.Sale[dim])
			if parseErr != nil || !sale.IsPositive() {
				return nil, nil, fmt.Errorf("supplier frozen retail requires positive %s sale", dim)
			}
			if sale.GreaterThan(maxSale[dim]) {
				maxSale[dim] = sale
			}
		}
	}
	if len(dims) != len(maxSale) {
		return nil, nil, errors.New("supplier frozen retail billing dimensions do not match rules")
	}
	for _, dim := range dims {
		if !maxSale[dim].IsPositive() {
			return nil, nil, fmt.Errorf("supplier frozen retail has no sale for billed dimension %s", dim)
		}
	}
	return rules, maxSale, nil
}

func BuildZTAPISupplierPriceSource20261019(old *ZTAPIModelPriceSource, operatorID int, effectiveAt int64, fx ZTAPIABFXParams) (ZTAPIModelPriceSource, error) {
	return BuildZTAPISupplierPriceSource20261019WithDB(DB, old, operatorID, effectiveAt, fx)
}

// Transactional callers must pass their transaction, including on repeat runs
// whose current source already uses the frozen-retail policy.
func BuildZTAPISupplierPriceSource20261019WithDB(db *gorm.DB, old *ZTAPIModelPriceSource, operatorID int, effectiveAt int64, fx ZTAPIABFXParams) (ZTAPIModelPriceSource, error) {
	if old == nil {
		return ZTAPIModelPriceSource{}, errors.New("supplier price requires an existing source")
	}
	anchor := old
	if old.PricePolicy == string(ZTAPIPricePolicyQuoteFrozenRetail20261019) {
		var err error
		anchor, err = ztapiSupplierRetailAnchor20261019(db, old)
		if err != nil {
			return ZTAPIModelPriceSource{}, err
		}
	}
	return buildZTAPISupplierPriceFromAnchor20261019(anchor, operatorID, effectiveAt, fx)
}

func buildZTAPISupplierPriceFromAnchor20261019(anchor *ZTAPIModelPriceSource, operatorID int, effectiveAt int64, fx ZTAPIABFXParams) (ZTAPIModelPriceSource, error) {
	if anchor == nil || anchor.ID <= 0 || operatorID <= 0 || effectiveAt <= 0 ||
		anchor.PricePolicy == string(ZTAPIPricePolicyQuoteFrozenRetail20261019) ||
		anchor.SourceDocumentChecksum == ZTAPISupplierWorkbook20261019SHA256 {
		return ZTAPIModelPriceSource{}, errors.New("supplier price requires an original persisted retail anchor, operator and effective time")
	}
	anchorCopy := *anchor
	if err := ValidateZTAPIModelPriceSource(&anchorCopy); err != nil {
		return ZTAPIModelPriceSource{}, fmt.Errorf("invalid supplier retail anchor: %w", err)
	}
	entry, err := ztapiSupplierPriceEntryFor20261019(anchor.SourceModel)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	if fx.Mode != "" && fx.Mode != ZTAPIFXModePlatformV1 {
		return ZTAPIModelPriceSource{}, errors.New("unsupported supplier FX mode")
	}
	if fx.Mode == ZTAPIFXModePlatformV1 {
		// Calculations must use the same precision as the frozen FX evidence.
		fx.PlatformRate = fx.PlatformRate.Round(10)
		fx.UpstreamUSDRate = fx.UpstreamUSDRate.Round(10)
		if !ztapiFXRateInRange(fx.PlatformRate) {
			return ZTAPIModelPriceSource{}, errors.New("supplier platform FX rate is outside the supported range")
		}
		if entry.Currency == "USD" && !fx.UpstreamUSDRate.IsPositive() {
			return ZTAPIModelPriceSource{}, ErrZTAPIUpstreamUSDRateUnset
		}
	} else if entry.Currency == "CNY" {
		return ZTAPIModelPriceSource{}, errors.New("CNY supplier quotation requires platform_v1 FX without a legacy buffer")
	}
	var oldRules []ztapiABFrozenSaleRule
	if anchor.TokenPriceRulesJSON != "" {
		if err := common.Unmarshal([]byte(anchor.TokenPriceRulesJSON), &oldRules); err != nil {
			return ZTAPIModelPriceSource{}, fmt.Errorf("invalid retail anchor tiers: %w", err)
		}
	} else {
		preview, err := BuildZTAPIModelPricePreview(&anchorCopy)
		if err != nil {
			return ZTAPIModelPriceSource{}, err
		}
		oldRules = []ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: preview.SaleUSD}}
	}
	if len(oldRules) != len(entry.Rules) {
		return ZTAPIModelPriceSource{}, errors.New("supplier quotation tiers do not match the retail anchor")
	}
	multiplier, err := ztapiNormalizedSaleMultiplier(anchor.SaleMultiplier)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	if anchor.TokenPriceRulesJSON == "" {
		multiplier = decimal.NewFromInt(1) // Preview already applied the anchor multiplier.
	}
	source := ZTAPIModelPriceSource{
		ModelConfigID: anchor.ModelConfigID, SourceModel: anchor.SourceModel,
		ResourceType: "pool", PricePolicy: string(ZTAPIPricePolicyQuoteFrozenRetail20261019),
		SpendTier: entry.Grade, SaleMultiplier: "1.0000000000", Currency: entry.Currency,
		QuotationEffectiveAt: effectiveAt, SourceDocumentChecksum: ZTAPISupplierWorkbook20261019SHA256,
		QuotationGrade: entry.Grade, QuotationCell: entry.QuotationCell, QuotationModelCode: entry.ModelCode,
		OperatorID: operatorID, CNYPerUSD: "0", PlatformCNYPerUnit: "0", UpstreamCNYPerUSD: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0",
	}
	if entry.Grade == "A" {
		source.ResourceType = "enterprise"
	}
	if fx.Mode == ZTAPIFXModePlatformV1 {
		source.Currency, source.FXMode = "CNY", ZTAPIFXModePlatformV1
		source.PlatformCNYPerUnit = fx.PlatformRate.StringFixed(10)
		source.CNYPerUSD = source.PlatformCNYPerUnit
		if entry.Currency == "USD" {
			source.UpstreamCNYPerUSD = fx.UpstreamUSDRate.StringFixed(10)
		}
	}
	fraction := decimal.RequireFromString(entry.Fraction)
	maxCost := map[string]decimal.Decimal{}
	rules := make([]ztapiABFrozenSaleRule, 0, len(entry.Rules))
	for tier, quote := range entry.Rules {
		if !reflect.DeepEqual(oldRules[tier].Conditions, quote.Conditions) || len(oldRules[tier].Sale) != len(quote.Official) {
			return ZTAPIModelPriceSource{}, errors.New("supplier quotation conditions or dimensions differ from retail anchor")
		}
		rule := ztapiABFrozenSaleRule{Conditions: append([]string{}, quote.Conditions...), Sale: map[string]string{}, RetailBasisID: anchor.ID}
		for dim, raw := range quote.Official {
			official := decimal.RequireFromString(raw)
			storedCost := official.Mul(fraction).Round(10)
			if fx.Mode == ZTAPIFXModePlatformV1 && entry.Currency == "USD" {
				storedCost = storedCost.Mul(fx.UpstreamUSDRate).Round(10)
			}
			cost := storedCost
			if fx.Mode == ZTAPIFXModePlatformV1 {
				cost = storedCost.Div(fx.PlatformRate).Round(10)
			}
			oldSale, err := decimal.NewFromString(oldRules[tier].Sale[dim])
			if err != nil || !oldSale.IsPositive() {
				return ZTAPIModelPriceSource{}, fmt.Errorf("retail anchor is missing positive %s sale", dim)
			}
			oldSale = oldSale.Mul(multiplier).Round(10)
			sale := oldSale
			if entry.RetailRule == "official_78" {
				if entry.Currency == "CNY" {
					official = official.Div(fx.PlatformRate)
				}
				sale = official.Mul(decimal.RequireFromString("0.78")).Round(10)
			}
			if sale.GreaterThan(oldSale) {
				return ZTAPIModelPriceSource{}, fmt.Errorf("supplier tier %d %s would raise the retail anchor", tier, dim)
			}
			if !cost.IsPositive() || sale.LessThan(cost.Mul(ztapiMinimumMarginMultiplier)) {
				return ZTAPIModelPriceSource{}, fmt.Errorf("supplier tier %d %s is below cost plus the minimum margin", tier, dim)
			}
			rule.Sale[dim] = sale.StringFixed(10)
			if storedCost.GreaterThan(maxCost[dim]) {
				maxCost[dim] = storedCost
			}
		}
		rules = append(rules, rule)
	}
	dimensions := make([]string, 0, len(maxCost))
	for dim := range maxCost {
		dimensions = append(dimensions, dim)
	}
	sort.Strings(dimensions)
	dimensionsJSON, err := common.Marshal(dimensions)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	rulesJSON, err := common.Marshal(rules)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	source.BillingDimensions, source.TokenPriceRulesJSON = string(dimensionsJSON), string(rulesJSON)
	source.InputPerMillion = maxCost[ZTAPIBillingDimensionInputTokens].StringFixed(10)
	source.OutputPerMillion = maxCost[ZTAPIBillingDimensionOutputTokens].StringFixed(10)
	source.CacheReadPerMillion = maxCost[ZTAPIBillingDimensionCacheRead].StringFixed(10)
	source.CacheWritePerMillion = maxCost[ZTAPIBillingDimensionCacheWrite].StringFixed(10)
	source.CacheWrite5mPerMillion = maxCost[ZTAPIBillingDimensionCacheWrite5m].StringFixed(10)
	source.CacheWrite1hPerMillion = maxCost[ZTAPIBillingDimensionCacheWrite1h].StringFixed(10)
	if err := ValidateZTAPIModelPriceSource(&source); err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	return source, nil
}

func ValidateZTAPISupplierPriceSource20261019(db *gorm.DB, source *ZTAPIModelPriceSource) error {
	if source == nil {
		return errors.New("supplier price source is missing")
	}
	copy := *source
	if err := ValidateZTAPIModelPriceSource(&copy); err != nil {
		return err
	}
	anchor, err := ztapiSupplierRetailAnchor20261019(db, &copy)
	if err != nil {
		return err
	}
	fx, err := ztapiABFXParamsFromSource(&copy)
	if err != nil {
		return err
	}
	expected, err := buildZTAPISupplierPriceFromAnchor20261019(anchor, copy.OperatorID, copy.QuotationEffectiveAt, fx)
	if err != nil {
		return err
	}
	if field := ztapiABPriceSourceEvidenceMismatch(source, &expected); field != "" {
		return fmt.Errorf("supplier 2026-10-19 source %s does not match quotation, anchor and FX", field)
	}
	if source.MediaPriceContractJSON != expected.MediaPriceContractJSON || !ztapiSameDecimal(source.SaleMultiplier, expected.SaleMultiplier) {
		return errors.New("supplier source media contract or multiplier differs from frozen retail")
	}
	return nil
}
