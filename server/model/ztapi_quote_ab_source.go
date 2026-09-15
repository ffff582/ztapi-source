package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

type ztapiABFrozenSaleRule struct {
	Conditions []string          `json:"conditions"`
	Sale       map[string]string `json:"sale"`
}

var ztapiABTokenConditionPattern = regexp.MustCompile(`^(输入长度|输出长度)?=?(≤|≥|>|<)[0-9]+(?:\.[0-9]+)?[KM]$`)

func ztapiABTokenConditionSupported(condition string) bool {
	if condition == "输入类型=音频" || condition == "输入类型=文本/图像/视频" {
		return true
	}
	kind := ""
	for _, part := range strings.Split(condition, "且") {
		if !ztapiABTokenConditionPattern.MatchString(part) {
			return false
		}
		if strings.HasPrefix(part, "输入长度") {
			kind = "输入长度"
		} else if strings.HasPrefix(part, "输出长度") {
			kind = "输出长度"
		}
		if kind == "" {
			return false
		}
	}
	return true
}

// BuildZTAPIABTextPriceSource creates a new source; it never edits an old publication.
func BuildZTAPIABTextPriceSource(quote ZTAPIABQuotationManifest, modelName string, modelConfigID, operatorID int, effectiveAt int64) (ZTAPIModelPriceSource, error) {
	var source ZTAPIModelPriceSource
	if modelConfigID <= 0 || operatorID <= 0 || effectiveAt <= 0 {
		return source, errors.New("quotation source requires model, operator and effective time")
	}
	rows, policy, err := quote.PublishablePricingBasis(modelName)
	if err != nil {
		return source, fmt.Errorf("text quotation lacks a complete price basis for %q: %w", modelName, err)
	}
	if len(rows) != 1 {
		return source, fmt.Errorf("text quotation requires exactly one price basis for %q", modelName)
	}
	identities, err := ZTAPIQuotationEntries()
	if err != nil {
		return source, err
	}
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, identities)
	if err != nil {
		return source, err
	}
	identity := ZTAPIABModelIdentity{}
	for _, claim := range bridge.Mapped {
		if claim.ModelName == modelName {
			identity = claim
			break
		}
	}
	if identity.SourceModel == "" {
		return source, fmt.Errorf("quotation model %q has no exact frozen public/source identity", modelName)
	}
	row := rows[0]
	resource := "pool"
	if row.Grade == "A" {
		resource = "enterprise"
	}
	source = ZTAPIModelPriceSource{
		ModelConfigID: modelConfigID, SourceModel: identity.SourceModel,
		ResourceType: resource, PricePolicy: string(policy), SpendTier: row.Grade,
		Currency: row.TokenPriceRules[0].Currency, QuotationEffectiveAt: effectiveAt,
		SourceDocumentChecksum: quote.WorkbookSHA256, QuotationGrade: row.Grade,
		QuotationCell: row.QuotationCell, OfficialPriceCell: row.OfficialPriceCell,
		QuotationModelCode: row.ModelCode, OperatorID: operatorID,
		InputPerMillion: "0", OutputPerMillion: "0", CacheReadPerMillion: "0",
		CacheWritePerMillion: "0", CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
	}
	fx := decimal.NewFromInt(1)
	if source.Currency == "CNY" {
		source.CNYPerUSD = "7.2000000000"
		fx = decimal.RequireFromString(source.CNYPerUSD)
	} else if source.Currency != "USD" {
		return ZTAPIModelPriceSource{}, errors.New("text quotation has unsupported currency")
	}
	maxCost := map[string]decimal.Decimal{}
	frozen := make([]ztapiABFrozenSaleRule, 0, len(row.TokenPriceRules))
	for _, rule := range row.TokenPriceRules {
		if rule.Currency != source.Currency {
			return ZTAPIModelPriceSource{}, errors.New("text quotation mixes currencies across tiers")
		}
		for _, condition := range rule.Conditions {
			if !ztapiABTokenConditionSupported(condition) {
				return ZTAPIModelPriceSource{}, fmt.Errorf("unsupported quotation condition %q", condition)
			}
		}
		sale := map[string]string{}
		for dimension, raw := range rule.Cost {
			cost, err := decimal.NewFromString(raw)
			if err != nil || !cost.IsPositive() {
				return ZTAPIModelPriceSource{}, fmt.Errorf("invalid quoted cost for %s", dimension)
			}
			wantQuotedSale, err := CalculateZTAPISalePriceForPolicy(cost, policy)
			quotedSale, saleErr := decimal.NewFromString(rule.Sale[dimension])
			if err != nil || saleErr != nil || !wantQuotedSale.Equal(quotedSale) {
				return ZTAPIModelPriceSource{}, fmt.Errorf("quoted sale does not derive from %s cost", dimension)
			}
			if source.Currency == "CNY" {
				cost, err = ConvertZTAPICNYCostToUSD(cost, fx)
				if err != nil {
					return ZTAPIModelPriceSource{}, err
				}
			}
			final, err := CalculateZTAPISalePriceForPolicy(cost, policy)
			if err != nil {
				return ZTAPIModelPriceSource{}, err
			}
			sale[dimension] = final.StringFixed(10)
			originalCost := decimal.RequireFromString(raw)
			if originalCost.GreaterThan(maxCost[dimension]) {
				maxCost[dimension] = originalCost
			}
		}
		frozen = append(frozen, ztapiABFrozenSaleRule{Conditions: append([]string{}, rule.Conditions...), Sale: sale})
	}
	dimensions := make([]string, 0, len(maxCost))
	for name := range maxCost {
		dimensions = append(dimensions, name)
	}
	sort.Strings(dimensions)
	encodedDimensions, _ := json.Marshal(dimensions)
	source.BillingDimensions = string(encodedDimensions)
	if identity.Modality == ZTAPIModalityEmbedding {
		if len(frozen) != 1 || len(frozen[0].Conditions) != 0 || len(frozen[0].Sale) != 1 {
			return ZTAPIModelPriceSource{}, errors.New("embedding quotation must be one unconditional input-only price")
		}
	} else {
		encodedRules, _ := json.Marshal(frozen)
		source.TokenPriceRulesJSON = string(encodedRules)
	}
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

func validateZTAPIABPriceSource(source *ZTAPIModelPriceSource) error {
	if source == nil {
		return errors.New("A/B quote source is nil")
	}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return err
	}
	if source.SourceDocumentChecksum != quote.WorkbookSHA256 {
		return errors.New("A/B quote source has an unexpected workbook checksum")
	}
	if source.SourceModel == "gpt-image-2" {
		return validateZTAPIABImage2PriceSource(quote, source)
	}
	identities, err := ZTAPIQuotationEntries()
	if err != nil {
		return err
	}
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, identities)
	if err != nil {
		return err
	}
	modelName := ""
	for _, claim := range bridge.Mapped {
		if claim.SourceModel == source.SourceModel {
			modelName = claim.ModelName
			break
		}
	}
	if modelName == "" {
		return errors.New("A/B quote source has no exact model identity")
	}
	expected, err := BuildZTAPIABTextPriceSource(quote, modelName, source.ModelConfigID, source.OperatorID, source.QuotationEffectiveAt)
	if err != nil {
		return err
	}
	actualEvidence := []string{
		source.SourceModel, source.ResourceType, source.PricePolicy, source.SpendTier,
		source.Currency, source.CNYPerUSD, source.BillingDimensions, source.TokenPriceRulesJSON,
		source.SourceDocumentChecksum, source.QuotationGrade, source.QuotationCell,
		source.OfficialPriceCell, source.QuotationModelCode,
		source.InputPerMillion, source.OutputPerMillion, source.CacheReadPerMillion,
		source.CacheWritePerMillion, source.CacheWrite5mPerMillion, source.CacheWrite1hPerMillion,
		source.ImageUnitCost, source.AudioUnitCost, source.RequestUnitCost,
	}
	expectedEvidence := []string{
		expected.SourceModel, expected.ResourceType, expected.PricePolicy, expected.SpendTier,
		expected.Currency, expected.CNYPerUSD, expected.BillingDimensions, expected.TokenPriceRulesJSON,
		expected.SourceDocumentChecksum, expected.QuotationGrade, expected.QuotationCell,
		expected.OfficialPriceCell, expected.QuotationModelCode,
		expected.InputPerMillion, expected.OutputPerMillion, expected.CacheReadPerMillion,
		expected.CacheWritePerMillion, expected.CacheWrite5mPerMillion, expected.CacheWrite1hPerMillion,
		expected.ImageUnitCost, expected.AudioUnitCost, expected.RequestUnitCost,
	}
	fields := []string{
		"source_model", "resource_type", "price_policy", "spend_tier", "currency", "cny_per_usd",
		"billing_dimensions", "token_price_rules_json", "source_document_checksum", "quotation_grade",
		"quotation_cell", "official_price_cell", "quotation_model_code", "input_per_million",
		"output_per_million", "cache_read_per_million", "cache_write_per_million",
		"cache_write_5m_per_million", "cache_write_1h_per_million", "image_unit_cost",
		"audio_unit_cost", "request_unit_cost",
	}
	for i, actual := range actualEvidence {
		if i == 5 || i >= 13 {
			got, gotErr := decimal.NewFromString(actual)
			want, wantErr := decimal.NewFromString(expectedEvidence[i])
			if gotErr == nil && wantErr == nil && got.Equal(want) {
				continue
			}
		}
		if actual != expectedEvidence[i] {
			return fmt.Errorf("A/B quote source %s does not match the exact workbook", fields[i])
		}
	}
	return nil
}

func validateZTAPIABImage2PriceSource(quote ZTAPIABQuotationManifest, source *ZTAPIModelPriceSource) error {
	var row ZTAPIABQuotationEntry
	for _, candidate := range quote.Entries {
		if candidate.QuotationCell == "D100" {
			row = candidate
			break
		}
	}
	if row.ModelName != "GPT Image 2" || row.ModelCode != "zq-g-i-2" || !row.Active || row.Grade != "A" {
		return errors.New("gpt-image-2 has no exact active A quotation")
	}
	contract, err := buildZTAPIABImage2Contract(row)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		return err
	}
	want, err := canonicalizeZTAPIMediaPriceContract(string(raw))
	if err != nil {
		return err
	}
	if source.MediaPriceContractJSON != want || source.ResourceType != "enterprise" ||
		source.PricePolicy != string(ZTAPIPricePolicyEnterprise20Margin) || source.SpendTier != "A" ||
		source.Currency != "USD" || source.QuotationGrade != row.Grade ||
		source.QuotationCell != row.QuotationCell || source.OfficialPriceCell != row.OfficialPriceCell ||
		source.QuotationModelCode != row.ModelCode || source.BillingDimensions != `["input_tokens","output_tokens"]` ||
		source.TokenPriceRulesJSON != "" {
		return errors.New("gpt-image-2 A/B price source differs from the exact quotation")
	}
	values := ztapiPriceSourceValues(source)
	for dimension, raw := range values {
		wantCost := decimal.Zero
		if dimension == ZTAPIBillingDimensionInputTokens {
			wantCost = decimal.RequireFromString("3.9")
		} else if dimension == ZTAPIBillingDimensionOutputTokens {
			wantCost = decimal.RequireFromString("23.4")
		}
		cost, parseErr := decimal.NewFromString(raw)
		if parseErr != nil || !cost.Equal(wantCost) {
			return fmt.Errorf("gpt-image-2 A/B cost %s differs from the exact quotation", dimension)
		}
	}
	return nil
}
