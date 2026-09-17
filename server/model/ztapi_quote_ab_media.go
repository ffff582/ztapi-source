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
	// The original-resource video rows state their unit, so the quoted figure
	// reads without outside evidence: "分辨率=X｜输入不含视频=A 元/百万 Token｜
	// 输入包含视频=B 元/百万 Token".
	ztapiABVideoCNYPrice = regexp.MustCompile(`^分辨率=(480p / 720p|1080p|4K)｜输入不含视频=([0-9]+(?:\.[0-9]+)?) 元/百万 Token｜输入包含视频=([0-9]+(?:\.[0-9]+)?) 元/百万 Token$`)
)

// ztapiABSeedanceOriginalResourceModels binds each original-resource Seedance
// quotation cell to the provider model it prices. A cell prices one model only.
var ztapiABSeedanceOriginalResourceModels = map[string]string{
	"D52": "doubao-seedance-2-5",
}

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

// BuildZTAPIABSeedanceOriginalResourceContract prices one exact active A
// original-resource Seedance row. These rows name their own unit — 元/百万
// Token — so the quoted figure needs no outside unit evidence, unlike the
// gateway rows. The CNY they quote converts at the platform rate the price
// source freezes, which is the rate every other model from this workbook is
// priced at. It authorizes no route, no paid verification and no publication.
func BuildZTAPIABSeedanceOriginalResourceContract(row ZTAPIABQuotationEntry, video *types.ZTAPIVideoProtocolContract, platformRate decimal.Decimal) (types.ZTAPIMediaPriceContract, error) {
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
	if !verified || row.Grade != "A" || !row.Active || !row.PricingBasis || row.Modality != "video" {
		return empty, errors.New("Seedance quotation must be one exact active enterprise A video basis row")
	}
	if row.ResourceType != "原厂转发" {
		return empty, errors.New("Seedance original-resource pricing requires an exact 原厂转发 row")
	}
	provider, adapted := ztapiABSeedanceOriginalResourceModels[row.QuotationCell]
	if !adapted {
		return empty, errors.New("Seedance quotation row has no verified adaptation")
	}
	if video == nil || video.ProviderModel != provider {
		return empty, fmt.Errorf("%s exact paid-verified video protocol is required", provider)
	}
	frozenProtocol, _, protocolErr := types.BuildZTAPISeedanceProtocolContract(provider)
	if protocolErr != nil || !reflect.DeepEqual(frozenProtocol, *video) {
		return empty, fmt.Errorf("%s video protocol differs from paid-verified evidence", provider)
	}
	if !ztapiFXRateInRange(platformRate) {
		return empty, errors.New("platform FX rate is out of the supported range")
	}
	fraction, err := decimal.NewFromString(row.QuotedFraction)
	if err != nil || !fraction.IsPositive() || fraction.GreaterThan(decimal.NewFromInt(1)) {
		return empty, errors.New("media A quotation fraction is invalid")
	}
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "video"}
	for _, part := range strings.Split(row.OfficialPriceText, "；") {
		match := ztapiABVideoCNYPrice.FindStringSubmatch(strings.TrimSpace(part))
		if len(match) != 4 {
			return empty, errors.New("Seedance original-resource quotation has an unsupported condition or missing token unit")
		}
		resolutions := []string{strings.ToLower(match[1])}
		if match[1] == "480p / 720p" {
			resolutions = []string{"480p", "720p"}
		}
		for _, resolution := range resolutions {
			for _, priced := range []struct{ containsVideo, official string }{
				{"false", match[2]}, {"true", match[3]},
			} {
				rule, ruleErr := ztapiABVideoCNYRule(row, resolution, priced.containsVideo, priced.official, fraction, platformRate)
				if ruleErr != nil {
					return empty, ruleErr
				}
				contract.Rules = append(contract.Rules, rule)
			}
		}
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		return empty, err
	}
	contract, err = types.ParseZTAPIMediaPriceContract(string(raw))
	if err != nil {
		return empty, fmt.Errorf("media quote does not form a complete price matrix: %w", err)
	}
	if err := types.ValidateZTAPIVideoPriceProtocolCompatibility(contract, *video); err != nil {
		return empty, fmt.Errorf("media quote does not match protocol usage dimensions: %w", err)
	}
	return contract, nil
}

func ztapiABVideoCNYRule(row ZTAPIABQuotationEntry, resolution, containsVideo, officialRaw string,
	fraction, platformRate decimal.Decimal) (types.ZTAPIMediaPriceRule, error) {
	official, err := decimal.NewFromString(officialRaw)
	if err != nil || !official.IsPositive() {
		return types.ZTAPIMediaPriceRule{}, errors.New("media official unit price is invalid")
	}
	cost := official.Mul(fraction).Div(platformRate).Round(10)
	sale, err := CalculateZTAPISalePriceForPolicy(cost, ZTAPIPricePolicyEnterprise20Margin)
	if err != nil {
		return types.ZTAPIMediaPriceRule{}, err
	}
	return types.ZTAPIMediaPriceRule{
		ID:          resolution + "_video_" + containsVideo,
		Conditions:  map[string]string{"resolution": resolution, "contains_video_input": containsVideo},
		BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
		CostUSD:     map[string]string{ZTAPIBillingDimensionInputTokens: cost.String()},
		SaleUSD:     map[string]string{ZTAPIBillingDimensionInputTokens: sale.String()},
		SourceCells: map[string]string{ZTAPIBillingDimensionInputTokens: row.OfficialPriceCell},
	}, nil
}

// BuildZTAPIABSeedanceOriginalResourcePriceSource prices a Seedance model from
// the A/B workbook alone. The upstream CNY cost is what the source stores; the
// contract it carries holds the billing-unit prices the platform rate derives.
func BuildZTAPIABSeedanceOriginalResourcePriceSource(quote ZTAPIABQuotationManifest, modelName string,
	video *types.ZTAPIVideoProtocolContract, modelConfigID, operatorID int, effectiveAt int64,
	fxParams ZTAPIABFXParams) (ZTAPIModelPriceSource, error) {
	if modelConfigID <= 0 || operatorID <= 0 || effectiveAt <= 0 {
		return ZTAPIModelPriceSource{}, errors.New("quotation source requires model, operator and effective time")
	}
	current, err := ZTAPIQuotationABEntries()
	if err != nil || quote.WorkbookSHA256 != current.WorkbookSHA256 {
		return ZTAPIModelPriceSource{}, errors.New("A/B workbook checksum differs from the frozen quotation")
	}
	if fxParams.Mode != ZTAPIFXModePlatformV1 {
		return ZTAPIModelPriceSource{}, fmt.Errorf("unsupported FX mode %q", fxParams.Mode)
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
	if identity.SourceModel == "" || identity.Modality != ZTAPIModalityVideo {
		return ZTAPIModelPriceSource{}, fmt.Errorf("quotation model %q has no exact video identity", modelName)
	}
	var row ZTAPIABQuotationEntry
	for _, candidate := range quote.Entries {
		if candidate.ModelName != modelName || candidate.ResourceType != "原厂转发" {
			continue
		}
		if row.QuotationCell != "" {
			return ZTAPIModelPriceSource{}, fmt.Errorf("quotation model %q has more than one original-resource row", modelName)
		}
		row = candidate
	}
	if row.QuotationCell == "" {
		return ZTAPIModelPriceSource{}, fmt.Errorf("quotation model %q has no original-resource row", modelName)
	}
	if ztapiABSeedanceOriginalResourceModels[row.QuotationCell] != identity.SourceModel {
		return ZTAPIModelPriceSource{}, fmt.Errorf("quotation %s does not price %s", row.QuotationCell, identity.SourceModel)
	}
	contract, err := BuildZTAPIABSeedanceOriginalResourceContract(row, video, fxParams.PlatformRate)
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
	// The stored cost is the dearest quoted bucket, so a reservation can never
	// be cheaper than what the upstream can bill.
	upstreamCNY := decimal.Zero
	fraction := decimal.RequireFromString(row.QuotedFraction)
	for _, part := range strings.Split(row.OfficialPriceText, "；") {
		match := ztapiABVideoCNYPrice.FindStringSubmatch(strings.TrimSpace(part))
		if len(match) != 4 {
			return ZTAPIModelPriceSource{}, errors.New("Seedance original-resource quotation has an unsupported condition or missing token unit")
		}
		for _, officialRaw := range match[2:4] {
			official, parseErr := decimal.NewFromString(officialRaw)
			if parseErr != nil {
				return ZTAPIModelPriceSource{}, errors.New("media official unit price is invalid")
			}
			if cost := official.Mul(fraction).Round(10); cost.GreaterThan(upstreamCNY) {
				upstreamCNY = cost
			}
		}
	}
	source := ZTAPIModelPriceSource{
		ModelConfigID: modelConfigID, SourceModel: identity.SourceModel,
		ResourceType: "original_resource", PricePolicy: string(ZTAPIPricePolicyEnterprise20Margin),
		SpendTier: row.Grade, Currency: "CNY", QuotationEffectiveAt: effectiveAt,
		SourceDocumentChecksum: quote.WorkbookSHA256, QuotationGrade: row.Grade,
		QuotationCell: row.QuotationCell, OfficialPriceCell: row.OfficialPriceCell,
		QuotationModelCode: row.ModelCode, OperatorID: operatorID,
		BillingDimensions:      `["` + ZTAPIBillingDimensionInputTokens + `"]`,
		MediaPriceContractJSON: canonical, FXMode: ZTAPIFXModePlatformV1,
		PlatformCNYPerUnit: fxParams.PlatformRate.StringFixed(10),
		CNYPerUSD:          fxParams.PlatformRate.StringFixed(10),
		UpstreamCNYPerUSD:  decimal.Zero.StringFixed(10),
	}
	clearZTAPIPriceSourceCosts(&source)
	source.InputPerMillion = upstreamCNY.StringFixed(10)
	if err := ValidateZTAPIModelPriceSource(&source); err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	return source, nil
}
