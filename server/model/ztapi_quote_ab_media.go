package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

var (
	ztapiABImageTextPrice  = regexp.MustCompile(`^输入类型=文本｜输入单价=\$([0-9]+(?:\.[0-9]+)?)｜缓存命中=\$([0-9]+(?:\.[0-9]+)?)$`)
	ztapiABImagePhotoPrice = regexp.MustCompile(`^输入类型=图片｜输入单价=\$([0-9]+(?:\.[0-9]+)?)｜缓存命中=\$([0-9]+(?:\.[0-9]+)?)｜输出=\$([0-9]+(?:\.[0-9]+)?)$`)
	ztapiABVideoPrice      = regexp.MustCompile(`^输出分辨率=(480p/720p|1080p|4K)｜输入条件=(不含视频|包含视频)｜Token单价=\$([0-9]+(?:\.[0-9]+)?)$`)
)

// BuildZTAPIABMediaPriceContract adapts one exact, active A gateway quotation row.
// It does not authorize a provider route, paid verification, or publication.
func BuildZTAPIABMediaPriceContract(row ZTAPIABQuotationEntry, image *types.ZTAPIImageProtocolContract, video *types.ZTAPIVideoProtocolContract) (types.ZTAPIMediaPriceContract, error) {
	var empty types.ZTAPIMediaPriceContract
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return empty, err
	}
	verified := false
	for _, source := range quote.Entries {
		if source.QuotationCell == row.QuotationCell && reflect.DeepEqual(source, row) {
			verified = true
			break
		}
	}
	if !verified || row.Grade != "A" || !row.Active || !row.PricingBasis {
		return empty, errors.New("media quotation must be one exact active enterprise A basis row")
	}
	if row.ResourceType != "网关" {
		return empty, errors.New("media quotation requires an exact A gateway row; other resource units or routes are unverified")
	}
	var contract types.ZTAPIMediaPriceContract
	switch row.QuotationCell {
	case "D100":
		if image == nil || video != nil || image.ProviderModel != "gpt-image-2" {
			return empty, errors.New("gpt-image-2 image protocol evidence is required")
		}
		protocolJSON, marshalErr := json.Marshal(image)
		if marshalErr != nil {
			return empty, marshalErr
		}
		if _, _, protocolErr := types.ParseZTAPIImageProtocolContract(string(protocolJSON)); protocolErr != nil {
			return empty, fmt.Errorf("gpt-image-2 frozen image protocol evidence is invalid: %w", protocolErr)
		}
		contract, err = buildZTAPIABImage2Contract(row)
	case "D114":
		return empty, errors.New("Gemini 2.5 Flash Image quotation is incomplete: only <=200K input is priced, text/image output prices differ, and the required >200K matrix is absent")
	case "D116", "D117", "D118":
		provider := map[string]string{
			"D116": "doubao-seedance-2.0", "D117": "doubao-seedance-2-0-fast", "D118": "doubao-seedance-2-0-mini",
		}[row.QuotationCell]
		if video == nil || image != nil || video.ProviderModel != provider {
			return empty, fmt.Errorf("%s exact paid-verified video protocol is required", provider)
		}
		frozenProtocol, _, protocolErr := types.BuildZTAPISeedanceProtocolContract(provider)
		if protocolErr != nil || !reflect.DeepEqual(frozenProtocol, *video) {
			return empty, fmt.Errorf("%s video protocol differs from paid-verified evidence", provider)
		}
		return empty, errors.New("Seedance gateway Token unit is unconfirmed (USD per token vs per million tokens); limited-time prices also require an expiry gate")
	case "D115":
		return empty, errors.New("Seedance 2.5 has no paid-verified provider protocol or complete resolution matrix")
	default:
		return empty, errors.New("media quotation row has no verified adaptation")
	}
	if err != nil {
		return empty, err
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		return empty, err
	}
	contract, err = types.ParseZTAPIMediaPriceContract(string(raw))
	if err != nil {
		return empty, fmt.Errorf("media quote does not form a complete price matrix: %w", err)
	}
	if image != nil {
		err = types.ValidateZTAPIImagePriceProtocolCompatibility(contract, *image)
	} else {
		err = types.ValidateZTAPIVideoPriceProtocolCompatibility(contract, *video)
	}
	if err != nil {
		return empty, fmt.Errorf("media quote does not match protocol usage dimensions: %w", err)
	}
	return contract, nil
}

func buildZTAPIABImage2Contract(row ZTAPIABQuotationEntry) (types.ZTAPIMediaPriceContract, error) {
	parts := strings.Split(row.OfficialPriceText, "；")
	if len(parts) != 2 {
		return types.ZTAPIMediaPriceContract{}, errors.New("gpt-image-2 quotation must contain exact text and image buckets")
	}
	text := ztapiABImageTextPrice.FindStringSubmatch(parts[0])
	photo := ztapiABImagePhotoPrice.FindStringSubmatch(parts[1])
	if len(text) != 3 || len(photo) != 4 {
		return types.ZTAPIMediaPriceContract{}, errors.New("gpt-image-2 quotation buckets are incomplete")
	}
	prices := []struct{ id, official string }{
		{"text_input", text[1]}, {"text_cached_input", text[2]},
		{"image_input", photo[1]}, {"image_cached_input", photo[2]}, {"image_output", photo[3]},
	}
	if ztapiQuotationLoadError != nil {
		return types.ZTAPIMediaPriceContract{}, ztapiQuotationLoadError
	}
	frozenRow, ok := ztapiMediaPriceRowFromManifest(ztapiQuotation, "gpt-image-2")
	if !ok || frozenRow.MediaPriceContractJSON == "" {
		return types.ZTAPIMediaPriceContract{}, errors.New("gpt-image-2 has no frozen per-million-token unit evidence")
	}
	frozenContract, err := types.ParseZTAPIMediaPriceContract(frozenRow.MediaPriceContractJSON)
	if err != nil {
		return types.ZTAPIMediaPriceContract{}, err
	}
	frozenOfficial := make(map[string]decimal.Decimal, len(frozenContract.Rules))
	for _, rule := range frozenContract.Rules {
		sale, parseErr := decimal.NewFromString(rule.SaleUSD[rule.ID])
		if parseErr != nil {
			return types.ZTAPIMediaPriceContract{}, parseErr
		}
		frozenOfficial[rule.ID] = sale.Div(decimal.RequireFromString("0.8"))
	}
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image"}
	for _, price := range prices {
		official, parseErr := decimal.NewFromString(price.official)
		old, exists := frozenOfficial[price.id]
		if parseErr != nil || !exists || !official.Equal(old) {
			return types.ZTAPIMediaPriceContract{}, fmt.Errorf("gpt-image-2 %s price does not match frozen per-million-token unit evidence", price.id)
		}
		rule, err := ztapiABMediaRule(row, price.id, map[string]string{"token_bucket": price.id}, price.official, price.id)
		if err != nil {
			return types.ZTAPIMediaPriceContract{}, err
		}
		contract.Rules = append(contract.Rules, rule)
	}
	return contract, nil
}

// BuildZTAPIABImage2PriceSource rebinds only the frozen gpt-image-2 buckets.
func BuildZTAPIABImage2PriceSource(quote ZTAPIABQuotationManifest, old ZTAPIModelPriceSource, image *types.ZTAPIImageProtocolContract, operatorID int, effectiveAt int64) (ZTAPIModelPriceSource, error) {
	if old.ModelConfigID <= 0 || old.SourceModel != "gpt-image-2" || old.SourceDocumentChecksum != ZTAPIQuotationSHA256 ||
		operatorID <= 0 || effectiveAt <= 0 {
		return ZTAPIModelPriceSource{}, errors.New("gpt-image-2 requires an existing frozen quotation and operator")
	}
	current, err := ZTAPIQuotationABEntries()
	if err != nil || quote.WorkbookSHA256 != current.WorkbookSHA256 {
		return ZTAPIModelPriceSource{}, errors.New("gpt-image-2 A/B workbook checksum differs from the frozen quotation")
	}
	var row ZTAPIABQuotationEntry
	for _, candidate := range quote.Entries {
		if candidate.QuotationCell == "D100" {
			row = candidate
			break
		}
	}
	if row.QuotationCell != "D100" || row.ModelName != "GPT Image 2" || row.ModelCode != "zq-g-i-2" {
		return ZTAPIModelPriceSource{}, errors.New("gpt-image-2 has no exact enterprise A quotation identity")
	}
	contract, err := BuildZTAPIABMediaPriceContract(row, image, nil)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	canonical, err := types.CanonicalizeZTAPIMediaPriceContract(string(raw))
	if err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	values := make(map[string]types.ZTAPIMediaPriceRule, len(contract.Rules))
	for _, rule := range contract.Rules {
		values[rule.ID] = rule
	}
	input, inputOK := values["text_input"]
	output, outputOK := values["image_output"]
	if !inputOK || !outputOK {
		return ZTAPIModelPriceSource{}, errors.New("gpt-image-2 quoted input or output bucket is missing")
	}
	next := old
	next.ID, next.Version, next.CreatedAt = 0, 0, 0
	next.ResourceType, next.PricePolicy, next.SpendTier = "enterprise", string(ZTAPIPricePolicyEnterprise20Margin), "A"
	next.Currency, next.CNYPerUSD = "USD", "0"
	next.SourceDocumentChecksum, next.QuotationGrade, next.QuotationCell = quote.WorkbookSHA256, row.Grade, row.QuotationCell
	next.OfficialPriceCell, next.QuotationModelCode = row.OfficialPriceCell, row.ModelCode
	next.QuotationEffectiveAt, next.OperatorID = effectiveAt, operatorID
	next.BillingDimensions, next.TokenPriceRulesJSON = `["input_tokens","output_tokens"]`, ""
	next.MediaPriceContractJSON = canonical
	clearZTAPIPriceSourceCosts(&next)
	next.InputPerMillion = decimal.RequireFromString(input.CostUSD["text_input"]).StringFixed(10)
	next.OutputPerMillion = decimal.RequireFromString(output.CostUSD["image_output"]).StringFixed(10)
	if err := ValidateZTAPIModelPriceSource(&next); err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	return next, nil
}

// The draft checks the quoted condition matrix; its USD-per-million unit is not confirmed.
// Callers must not publish the draft without written unit and validity evidence.
func buildZTAPIABSeedanceGatewayContract(row ZTAPIABQuotationEntry) (types.ZTAPIMediaPriceContract, error) {
	parts := strings.Split(row.OfficialPriceText, "；")
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "video"}
	for _, part := range parts {
		match := ztapiABVideoPrice.FindStringSubmatch(part)
		if len(match) != 4 {
			return types.ZTAPIMediaPriceContract{}, errors.New("Seedance gateway quotation has an unsupported condition or missing token unit")
		}
		containsVideo := "false"
		if match[2] == "包含视频" {
			containsVideo = "true"
		}
		resolutions := []string{strings.ToLower(match[1])}
		if match[1] == "480p/720p" {
			resolutions = []string{"480p", "720p"}
		}
		for _, resolution := range resolutions {
			id := resolution + "_video_" + containsVideo
			condition := map[string]string{"resolution": resolution, "contains_video_input": containsVideo}
			if row.QuotationCell == "D117" || row.QuotationCell == "D118" {
				if resolution != "480p" && resolution != "720p" {
					return types.ZTAPIMediaPriceContract{}, errors.New("Seedance Fast/Mini quotation covers only 480p/720p")
				}
				if resolution == "480p" {
					continue
				}
				condition = map[string]string{"contains_video_input": containsVideo}
				id = "without_video_input"
				if containsVideo == "true" {
					id = "with_video_input"
				}
			}
			rule, err := ztapiABMediaRule(row, id, condition, match[3], "input_tokens")
			if err != nil {
				return types.ZTAPIMediaPriceContract{}, err
			}
			contract.Rules = append(contract.Rules, rule)
		}
	}
	return contract, nil
}

func ztapiABMediaRule(row ZTAPIABQuotationEntry, id string, conditions map[string]string, officialRaw, dimension string) (types.ZTAPIMediaPriceRule, error) {
	official, err := decimal.NewFromString(officialRaw)
	if err != nil || !official.IsPositive() {
		return types.ZTAPIMediaPriceRule{}, errors.New("media official unit price is invalid")
	}
	fraction, err := decimal.NewFromString(row.QuotedFraction)
	if err != nil || !fraction.IsPositive() || fraction.GreaterThan(decimal.NewFromInt(1)) {
		return types.ZTAPIMediaPriceRule{}, errors.New("media A quotation fraction is invalid")
	}
	cost := official.Mul(fraction)
	sale := cost.Div(decimal.RequireFromString("0.8")).Round(10)
	return types.ZTAPIMediaPriceRule{
		ID: id, Conditions: conditions, BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
		CostUSD: map[string]string{dimension: cost.String()}, SaleUSD: map[string]string{dimension: sale.String()},
		SourceCells: map[string]string{dimension: row.OfficialPriceCell},
	}, nil
}
