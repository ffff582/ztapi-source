package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

func ztapiCashImageTemplate20261030(fx ZTAPIABFXParams) (ZTAPIModelPriceSource, map[string]decimal.Decimal, error) {
	if fx.Mode != ZTAPIFXModePlatformV1 || !ztapiFXRateInRange(fx.PlatformRate) ||
		!fx.PlatformRate.Equal(fx.PlatformRate.Round(10)) {
		return ZTAPIModelPriceSource{}, nil, errors.New("cash image requires a representable frozen platform FX rate")
	}
	if !fx.UpstreamUSDRate.IsPositive() {
		return ZTAPIModelPriceSource{}, nil, ErrZTAPIUpstreamUSDRateUnset
	}
	if !fx.UpstreamUSDRate.Equal(fx.UpstreamUSDRate.Round(10)) {
		return ZTAPIModelPriceSource{}, nil, errors.New("cash image upstream FX rate exceeds frozen precision")
	}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return ZTAPIModelPriceSource{}, nil, err
	}
	var row ZTAPIABQuotationEntry
	for _, candidate := range quote.Entries {
		if candidate.QuotationCell == "D100" {
			row = candidate
			break
		}
	}
	if row.ModelName != "GPT Image 2" || row.ModelCode != "zq-g-i-2" || row.Grade != "A" ||
		row.ResourceType != "\u7f51\u5173" || row.Modality != "image" || !row.Active || !row.PricingBasis {
		return ZTAPIModelPriceSource{}, nil, errors.New("cash image pricing requires the canonical D100 enterprise A quotation")
	}
	contract, err := buildZTAPIABImage2Contract(row, true)
	if err != nil {
		return ZTAPIModelPriceSource{}, nil, err
	}
	legacyRow, ok := ztapiMediaPriceRowFromManifest(ztapiQuotation, "gpt-image-2")
	if !ok {
		return ZTAPIModelPriceSource{}, nil, errors.New("cash image original quotation is missing")
	}
	legacy, err := parseZTAPIMediaPriceContract(legacyRow.MediaPriceContractJSON)
	if err != nil {
		return ZTAPIModelPriceSource{}, nil, err
	}
	previous := map[string]decimal.Decimal{}
	for _, rule := range legacy.Rules {
		bucket := rule.Conditions["token_bucket"]
		previous[bucket] = decimal.RequireFromString(rule.SaleUSD[bucket])
	}
	source := ZTAPIModelPriceSource{
		SourceModel: "gpt-image-2", ResourceType: "enterprise", PricePolicy: string(ZTAPIPricePolicyCashEnterprise20261030),
		SpendTier: row.Grade, SaleMultiplier: "1.0000000000", Currency: "CNY", CNYPerUSD: fx.PlatformRate.StringFixed(10),
		FXMode: ZTAPIFXModePlatformV1, PlatformCNYPerUnit: fx.PlatformRate.StringFixed(10),
		UpstreamCNYPerUSD: fx.UpstreamUSDRate.StringFixed(10), BillingDimensions: `["input_tokens","output_tokens"]`,
		SourceDocumentChecksum: quote.WorkbookSHA256, QuotationGrade: row.Grade, QuotationCell: row.QuotationCell,
		OfficialPriceCell: row.OfficialPriceCell, QuotationModelCode: row.ModelCode,
	}
	clearZTAPIPriceSourceCosts(&source)
	maximum := map[string]decimal.Decimal{}
	for index := range contract.Rules {
		rule := &contract.Rules[index]
		bucket := rule.Conditions["token_bucket"]
		costCNY := decimal.RequireFromString(rule.CostUSD[bucket]).Mul(fx.UpstreamUSDRate).Round(10)
		costU := costCNY.DivRound(fx.PlatformRate, 10)
		sale := decimal.RequireFromString(ztapiCashSale20261030(costU, decimal.NewFromInt(1), "0.20", 8))
		if !previous[bucket].IsPositive() {
			return ZTAPIModelPriceSource{}, nil, fmt.Errorf("cash image %s has no frozen original retail price", bucket)
		}
		sale = decimal.Max(sale, previous[bucket].RoundCeil(8))
		rule.CostUSD[bucket] = costU.StringFixed(10)
		rule.SaleUSD[bucket] = sale.StringFixed(10)
		dimension := ZTAPIBillingDimensionInputTokens
		if bucket == "image_output" {
			dimension = ZTAPIBillingDimensionOutputTokens
			source.OutputPerMillion = costCNY.StringFixed(10)
		} else if bucket == "text_input" {
			source.InputPerMillion = costCNY.StringFixed(10)
		}
		maximum[dimension] = decimal.Max(maximum[dimension], sale)
	}
	sort.Slice(contract.Rules, func(i, j int) bool { return contract.Rules[i].ID < contract.Rules[j].ID })
	// Freeze the rebuilt contract directly. The shared media parser's legacy
	// margin whitelist is not authority for this separate cash-margin policy.
	raw, err := common.Marshal(contract)
	if err != nil {
		return ZTAPIModelPriceSource{}, nil, err
	}
	source.MediaPriceContractJSON = string(raw)
	return source, maximum, nil
}

func validateZTAPICashImageBase20261030(base *ZTAPIModelPriceSource) error {
	if base == nil || base.ID <= 0 || base.ModelConfigID <= 0 || base.QuotationEffectiveAt <= 0 ||
		base.SourceModel != "gpt-image-2" || base.ResourceType != "pool" || base.PricePolicy != string(ZTAPIPricePolicyPoolOfficial80) ||
		base.SourceDocumentChecksum != ZTAPIQuotationSHA256 || base.Currency != "USD" || base.CashMarginBaseSourceID != 0 ||
		base.FXMode != "" || base.TokenPriceRulesJSON != "" || base.BillingDimensions != `["input_tokens","output_tokens"]` ||
		strings.TrimSpace(base.SpendTier) == "" {
		return errors.New("cash image derivation requires an original legacy Image2 pool quotation")
	}
	if ztapiQuotationLoadError != nil {
		return ztapiQuotationLoadError
	}
	multiplier := base.SaleMultiplier
	if multiplier == "" {
		multiplier = "1"
	}
	for _, pair := range [][2]string{{multiplier, "1"}, {ztapiDecimalOrZero(base.CNYPerUSD), "0"},
		{ztapiDecimalOrZero(base.PlatformCNYPerUnit), "0"}, {ztapiDecimalOrZero(base.UpstreamCNYPerUSD), "0"}} {
		value, err := decimal.NewFromString(pair[0])
		if err != nil || !value.Equal(decimal.RequireFromString(pair[1])) {
			return errors.New("cash image original multiplier or FX differs from its frozen USD quotation")
		}
	}
	row, ok := ztapiMediaPriceRowFromManifest(ztapiQuotation, base.SourceModel)
	if !ok {
		return errors.New("cash image original media quotation is missing")
	}
	canonical, err := deriveZTAPIGPTImage2OperationAwarePriceContract(row.MediaPriceContractJSON)
	if err != nil || base.MediaPriceContractJSON != canonical {
		return errors.New("cash image original operation-aware matrix differs from the frozen quotation")
	}
	official, discount, ok := ztapiPoolOfficialPriceContractFromManifest(ztapiQuotation, base.SourceModel)
	if !ok {
		return errors.New("cash image original procurement quotation is missing")
	}
	for dimension, raw := range ztapiPriceSourceValues(base) {
		cost, err := decimal.NewFromString(raw)
		if err != nil || !cost.Equal(official[dimension].Mul(discount)) {
			return fmt.Errorf("cash image original %s cost differs from canonical procurement", dimension)
		}
	}
	return nil
}

func deriveZTAPICashImageSource20261030(base *ZTAPIModelPriceSource, operator int, fx ZTAPIABFXParams) (ZTAPIModelPriceSource, error) {
	if operator <= 0 {
		return ZTAPIModelPriceSource{}, errors.New("cash image derivation requires an operator")
	}
	if err := validateZTAPICashImageBase20261030(base); err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	source, _, err := ztapiCashImageTemplate20261030(fx)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	source.ModelConfigID, source.OperatorID = base.ModelConfigID, operator
	source.CashMarginBaseSourceID, source.QuotationEffectiveAt = base.ID, base.QuotationEffectiveAt
	return source, nil
}

// Shape checks never authorize a parent row. The caller must derive again from
// its persisted original source and compare all parent-bound fields in its TX.
func validateZTAPICashImageShape20261030(source *ZTAPIModelPriceSource) (map[string]decimal.Decimal, error) {
	if source == nil || source.ID < 0 || source.ModelConfigID <= 0 || source.OperatorID <= 0 || source.QuotationEffectiveAt <= 0 ||
		source.CashMarginBaseSourceID <= 0 || (source.ID > 0 && source.CashMarginBaseSourceID >= source.ID) {
		return nil, errors.New("cash image source requires a valid original parent and model identity")
	}
	multiplier, err := decimal.NewFromString(source.SaleMultiplier)
	if err != nil || !multiplier.Equal(decimal.NewFromInt(1)) {
		return nil, errors.New("cash image source cannot discount frozen retail prices")
	}
	fx, err := ztapiABFXParamsFromSource(source)
	if err != nil {
		return nil, err
	}
	expected, maximum, err := ztapiCashImageTemplate20261030(fx)
	if err != nil {
		return nil, err
	}
	if field := ztapiABPriceSourceEvidenceMismatch(source, &expected); field != "" {
		return nil, fmt.Errorf("cash image derived %s differs from the canonical enterprise quotation", field)
	}
	if source.MediaPriceContractJSON != expected.MediaPriceContractJSON {
		return nil, errors.New("cash image cost, sale or operation matrix differs from the canonical frozen contract")
	}
	return maximum, nil
}
