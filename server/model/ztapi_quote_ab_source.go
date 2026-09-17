package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/types"
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

// BuildZTAPIABTextPriceSource creates a new source under the legacy FX; it
// never edits an old publication.
func BuildZTAPIABTextPriceSource(quote ZTAPIABQuotationManifest, modelName string, modelConfigID, operatorID int, effectiveAt int64) (ZTAPIModelPriceSource, error) {
	return BuildZTAPIABTextPriceSourceWithFX(quote, modelName, modelConfigID, operatorID, effectiveAt, ZTAPIABFXParams{})
}

// BuildZTAPIABTextPriceSourceWithFX prices a quoted model under the given FX.
// In platform_v1 mode every quoted cost is first expressed in CNY (USD quotes
// at the upstream settlement rate), then converted to the billing unit at the
// platform rate frozen on the source, without the legacy 3% buffer.
func BuildZTAPIABTextPriceSourceWithFX(quote ZTAPIABQuotationManifest, modelName string, modelConfigID, operatorID int, effectiveAt int64, fxParams ZTAPIABFXParams) (ZTAPIModelPriceSource, error) {
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
		PlatformCNYPerUnit: "0", UpstreamCNYPerUSD: "0",
	}
	quoteCurrency := source.Currency
	fx := decimal.NewFromInt(1)
	if quoteCurrency == "CNY" {
		source.CNYPerUSD = "7.2000000000"
		fx = decimal.RequireFromString(source.CNYPerUSD)
	} else if quoteCurrency != "USD" {
		return ZTAPIModelPriceSource{}, errors.New("text quotation has unsupported currency")
	}
	platformV1 := fxParams.Mode == ZTAPIFXModePlatformV1
	upstreamUSD := decimal.Zero
	if fxParams.Mode != "" && !platformV1 {
		return ZTAPIModelPriceSource{}, fmt.Errorf("unsupported FX mode %q", fxParams.Mode)
	}
	if platformV1 {
		if !ztapiFXRateInRange(fxParams.PlatformRate) {
			return ZTAPIModelPriceSource{}, errors.New("platform FX rate is out of the supported range")
		}
		if quoteCurrency == "USD" {
			if !fxParams.UpstreamUSDRate.IsPositive() {
				return ZTAPIModelPriceSource{}, ErrZTAPIUpstreamUSDRateUnset
			}
			upstreamUSD = fxParams.UpstreamUSDRate
		}
		source.Currency = "CNY"
		source.FXMode = ZTAPIFXModePlatformV1
		source.PlatformCNYPerUnit = fxParams.PlatformRate.StringFixed(10)
		source.CNYPerUSD = source.PlatformCNYPerUnit
		source.UpstreamCNYPerUSD = upstreamUSD.StringFixed(10)
	}
	maxCost := map[string]decimal.Decimal{}
	frozen := make([]ztapiABFrozenSaleRule, 0, len(row.TokenPriceRules))
	for _, rule := range row.TokenPriceRules {
		if rule.Currency != quoteCurrency {
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
			// storedCost is what the source records: the upstream bill in CNY
			// under platform_v1, the quoted amount under the legacy FX.
			storedCost := decimal.RequireFromString(raw)
			if platformV1 {
				if quoteCurrency == "USD" {
					storedCost = storedCost.Mul(upstreamUSD).Round(10)
				}
				cost = storedCost.Div(fxParams.PlatformRate).Round(10)
			} else if quoteCurrency == "CNY" {
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
			if storedCost.GreaterThan(maxCost[dimension]) {
				maxCost[dimension] = storedCost
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
	if source.MediaPriceContractJSON != "" && ZTAPIModelModality(source.SourceModel) == ZTAPIModalityVideo {
		return validateZTAPIABVideoPriceSource(quote, source)
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
	fxParams, err := ztapiABFXParamsFromSource(source)
	if err != nil {
		return err
	}
	expected, err := BuildZTAPIABTextPriceSourceWithFX(quote, modelName, source.ModelConfigID, source.OperatorID, source.QuotationEffectiveAt, fxParams)
	if err != nil {
		return err
	}
	if field := ztapiABPriceSourceEvidenceMismatch(source, &expected); field != "" {
		return fmt.Errorf("A/B quote source %s does not match the exact workbook", field)
	}
	return nil
}

// ztapiABPriceSourceEvidenceMismatch names the first field where two A/B price
// sources differ. Decimal columns are compared by value, because a database
// round trip can drop trailing zeros.
func ztapiABPriceSourceEvidenceMismatch(actual, expected *ZTAPIModelPriceSource) string {
	evidence := func(source *ZTAPIModelPriceSource) []string {
		return []string{
			source.SourceModel, source.ResourceType, source.PricePolicy, source.SpendTier,
			source.Currency, source.CNYPerUSD, source.BillingDimensions, source.TokenPriceRulesJSON,
			source.SourceDocumentChecksum, source.QuotationGrade, source.QuotationCell,
			source.OfficialPriceCell, source.QuotationModelCode,
			source.InputPerMillion, source.OutputPerMillion, source.CacheReadPerMillion,
			source.CacheWritePerMillion, source.CacheWrite5mPerMillion, source.CacheWrite1hPerMillion,
			source.ImageUnitCost, source.AudioUnitCost, source.RequestUnitCost,
			ztapiDecimalOrZero(source.PlatformCNYPerUnit), ztapiDecimalOrZero(source.UpstreamCNYPerUSD),
			source.FXMode,
		}
	}
	left := evidence(actual)
	right := evidence(expected)
	fields := []string{
		"source_model", "resource_type", "price_policy", "spend_tier", "currency", "cny_per_usd",
		"billing_dimensions", "token_price_rules_json", "source_document_checksum", "quotation_grade",
		"quotation_cell", "official_price_cell", "quotation_model_code", "input_per_million",
		"output_per_million", "cache_read_per_million", "cache_write_per_million",
		"cache_write_5m_per_million", "cache_write_1h_per_million", "image_unit_cost",
		"audio_unit_cost", "request_unit_cost", "platform_cny_per_unit",
		"upstream_cny_per_usd", "fx_mode",
	}
	for i := range left {
		if i == 5 || i >= 13 {
			got, gotErr := decimal.NewFromString(left[i])
			want, wantErr := decimal.NewFromString(right[i])
			if gotErr == nil && wantErr == nil && got.Equal(want) {
				continue
			}
		}
		if left[i] != right[i] {
			return fields[i]
		}
	}
	return ""
}

// validateZTAPIABVideoPriceSource rebuilds a stored video price from the
// workbook and the rate the source itself froze, so an imported price can
// never say something the quotation does not.
func validateZTAPIABVideoPriceSource(quote ZTAPIABQuotationManifest, source *ZTAPIModelPriceSource) error {
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
		return errors.New("A/B video price source has no exact model identity")
	}
	fxParams, err := ztapiABFXParamsFromSource(source)
	if err != nil {
		return err
	}
	video, _, err := types.BuildZTAPISeedanceProtocolContract(source.SourceModel)
	if err != nil {
		return err
	}
	expected, err := BuildZTAPIABSeedanceOriginalResourcePriceSource(quote, modelName, &video,
		source.ModelConfigID, source.OperatorID, source.QuotationEffectiveAt, fxParams)
	if err != nil {
		return err
	}
	if source.MediaPriceContractJSON != expected.MediaPriceContractJSON {
		return errors.New("A/B video price source media contract does not match the exact workbook")
	}
	if field := ztapiABPriceSourceEvidenceMismatch(source, &expected); field != "" {
		return fmt.Errorf("A/B quote source %s does not match the exact workbook", field)
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

// BuildZTAPIQuotedModelPriceSource derives one quoted model's price from the
// A/B workbook and the platform FX policy in force. It is the only way a new
// quoted model's price enters the catalog: the caller names the model, and
// every figure comes from the audited workbook rather than from the caller.
func BuildZTAPIQuotedModelPriceSource(modelName string, modelConfigID, operatorID int, effectiveAt int64) (ZTAPIModelPriceSource, error) {
	if DB == nil {
		return ZTAPIModelPriceSource{}, errors.New("ZTAPI database is not initialized")
	}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	identities, err := ZTAPIQuotationEntries()
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, identities)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	identity := ZTAPIABModelIdentity{}
	for _, claim := range bridge.Mapped {
		if claim.ModelName == modelName {
			identity = claim
			break
		}
	}
	if identity.SourceModel == "" {
		return ZTAPIModelPriceSource{}, fmt.Errorf("quotation model %q has no exact identity", modelName)
	}
	fxParams, err := currentZTAPIABFXParams(DB)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	switch identity.Modality {
	case ZTAPIModalityText, ZTAPIModalityEmbedding:
		return BuildZTAPIABTextPriceSourceWithFX(quote, modelName, modelConfigID, operatorID, effectiveAt, fxParams)
	case ZTAPIModalityVideo:
		video, _, protocolErr := types.BuildZTAPISeedanceProtocolContract(identity.SourceModel)
		if protocolErr != nil {
			return ZTAPIModelPriceSource{}, protocolErr
		}
		return BuildZTAPIABSeedanceOriginalResourcePriceSource(quote, modelName, &video,
			modelConfigID, operatorID, effectiveAt, fxParams)
	default:
		// Image pricing keeps its own frozen bucket evidence.
		return ZTAPIModelPriceSource{}, fmt.Errorf("quotation model %q has no derived price path", modelName)
	}
}
