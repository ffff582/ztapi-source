package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ZTAPIABQuotePricingReady struct {
	ModelName         string                      `json:"model_name"`
	PublicName        string                      `json:"public_name"`
	QuotationGrade    string                      `json:"quotation_grade"`
	QuotationCell     string                      `json:"quotation_cell"`
	PricePolicy       string                      `json:"price_policy"`
	BillingDimensions []string                    `json:"billing_dimensions"`
	SaleUSD           map[string]string           `json:"sale_usd"`
	TokenPriceRules   []ZTAPIPublicTokenPriceRule `json:"token_price_rules,omitempty"`
	PricingRules      []ZTAPIPublicPricingRule    `json:"pricing_rules,omitempty"`
}

type ZTAPIABQuotePricingBlocked struct {
	ModelName string `json:"model_name"`
	Reason    string `json:"reason"`
}

type ZTAPIABQuotePricingPreview struct {
	WorkbookSHA256 string                       `json:"workbook_sha256"`
	QuotedModels   int                          `json:"quoted_models"`
	PricingReady   []ZTAPIABQuotePricingReady   `json:"pricing_ready"`
	Blocked        []ZTAPIABQuotePricingBlocked `json:"blocked"`
}

func ztapiPool78KeepsOldPriceAndBonusMargin(oldSource, target *ZTAPIModelPriceSource, preview *ZTAPIModelPricePreview) error {
	if err := ztapiRepriceDoesNotRaise(oldSource, target); err != nil {
		return err
	}
	for dimension, costRaw := range preview.CostUSD {
		cost, costErr := decimal.NewFromString(costRaw)
		sale, saleErr := decimal.NewFromString(preview.SaleUSD[dimension])
		if costErr != nil || saleErr != nil || !sale.GreaterThan(cost.Mul(decimal.RequireFromString("1.05"))) {
			return fmt.Errorf("%s would not cover the 5%% recharge bonus", dimension)
		}
	}
	return nil
}

func ztapiRepriceDoesNotRaise(oldSource, target *ZTAPIModelPriceSource) error {
	if oldSource == nil || target == nil {
		return errors.New("pool price comparison lacks evidence")
	}
	var oldRules, newRules []ztapiABFrozenSaleRule
	if oldSource.TokenPriceRulesJSON != "" {
		if err := json.Unmarshal([]byte(oldSource.TokenPriceRulesJSON), &oldRules); err != nil {
			return fmt.Errorf("old price rules are unavailable: %w", err)
		}
	}
	if target.TokenPriceRulesJSON != "" {
		if err := json.Unmarshal([]byte(target.TokenPriceRulesJSON), &newRules); err != nil {
			return fmt.Errorf("new price rules are unavailable: %w", err)
		}
	}
	if len(oldRules) == 0 && len(newRules) == 0 {
		oldPreview, oldErr := BuildZTAPIModelPricePreview(oldSource)
		newPreview, newErr := BuildZTAPIModelPricePreview(target)
		if oldErr != nil || newErr != nil {
			return errors.New("unstructured price comparison is unavailable")
		}
		for dimension, raw := range newPreview.SaleUSD {
			old, oldErr := decimal.NewFromString(oldPreview.SaleUSD[dimension])
			newPrice, newErr := decimal.NewFromString(raw)
			if oldErr != nil || newErr != nil || newPrice.GreaterThan(old) {
				return fmt.Errorf("%s would increase the customer price", dimension)
			}
		}
		return nil
	}
	if len(oldRules) != len(newRules) || len(newRules) == 0 {
		return errors.New("old and new price tiers do not match")
	}
	oldMultiplier, err := ztapiNormalizedSaleMultiplier(oldSource.SaleMultiplier)
	if err != nil {
		return err
	}
	newMultiplier, err := ztapiNormalizedSaleMultiplier(target.SaleMultiplier)
	if err != nil {
		return err
	}
	for tier, rule := range newRules {
		if len(rule.Conditions) != len(oldRules[tier].Conditions) {
			return errors.New("old and new tier conditions do not match")
		}
		for i, condition := range rule.Conditions {
			if condition != oldRules[tier].Conditions[i] {
				return errors.New("old and new tier conditions do not match")
			}
		}
		for dimension, saleRaw := range rule.Sale {
			sale, saleErr := decimal.NewFromString(saleRaw)
			old, oldErr := decimal.NewFromString(oldRules[tier].Sale[dimension])
			if saleErr != nil || oldErr != nil || sale.Mul(newMultiplier).GreaterThan(old.Mul(oldMultiplier)) {
				return fmt.Errorf("%s tier %d would increase the customer price", dimension, tier)
			}
		}
	}
	return nil
}

// This is quotation readiness only. Live key balance and provider routes are separate gates.
func PreviewZTAPIABCommercialPricing() (ZTAPIABQuotePricingPreview, error) {
	result := ZTAPIABQuotePricingPreview{}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return result, err
	}
	result.WorkbookSHA256 = quote.WorkbookSHA256
	names := map[string]struct{}{}
	for _, row := range quote.Entries {
		names[row.ModelName] = struct{}{}
	}
	result.QuotedModels = len(names)
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		var source ZTAPIModelPriceSource
		var mediaRules []ZTAPIPublicPricingRule
		var buildErr error
		if name == "GPT Image 2" {
			source, mediaRules, buildErr = previewZTAPIABImage2PriceSource(quote)
		} else {
			source, buildErr = BuildZTAPIABTextPriceSource(quote, name, 1, 1, 1_789_000_000)
		}
		if buildErr != nil {
			result.Blocked = append(result.Blocked, ZTAPIABQuotePricingBlocked{ModelName: name, Reason: buildErr.Error()})
			continue
		}
		preview, previewErr := BuildZTAPIModelPricePreview(&source)
		if previewErr != nil {
			result.Blocked = append(result.Blocked, ZTAPIABQuotePricingBlocked{ModelName: name, Reason: previewErr.Error()})
			continue
		}
		identity, identityErr := ZTAPIQuotationEntries()
		if identityErr != nil {
			return result, identityErr
		}
		bridge, bridgeErr := BuildZTAPIABQuotationIdentityBridge(quote, identity)
		if bridgeErr != nil {
			return result, bridgeErr
		}
		publicName := ""
		for _, claim := range bridge.Mapped {
			if claim.ModelName == name {
				publicName = claim.PublicName
				break
			}
		}
		if publicName == "" {
			result.Blocked = append(result.Blocked, ZTAPIABQuotePricingBlocked{ModelName: name, Reason: "no verified public model identity"})
			continue
		}
		var rules []ZTAPIPublicTokenPriceRule
		if source.TokenPriceRulesJSON != "" {
			rules, err = ztapiPublicTokenPriceRules(source.TokenPriceRulesJSON, preview.BillingDimensions)
			if err != nil {
				result.Blocked = append(result.Blocked, ZTAPIABQuotePricingBlocked{ModelName: name, Reason: err.Error()})
				continue
			}
		}
		result.PricingReady = append(result.PricingReady, ZTAPIABQuotePricingReady{
			ModelName: name, PublicName: publicName, QuotationGrade: source.QuotationGrade,
			QuotationCell: source.QuotationCell, PricePolicy: source.PricePolicy,
			BillingDimensions: append([]string(nil), preview.BillingDimensions...),
			SaleUSD:           copyZTAPIStringMap(preview.SaleUSD), TokenPriceRules: rules,
			PricingRules: mediaRules,
		})
	}
	return result, nil
}

func previewZTAPIABImage2PriceSource(quote ZTAPIABQuotationManifest) (ZTAPIModelPriceSource, []ZTAPIPublicPricingRule, error) {
	if DB == nil {
		return ZTAPIModelPriceSource{}, nil, errors.New("gpt-image-2 frozen publication is unavailable")
	}
	publications, err := loadZTAPIQuotedPublications(DB)
	if err != nil {
		return ZTAPIModelPriceSource{}, nil, err
	}
	for _, publication := range publications {
		if publication.SourceModel != "gpt-image-2" || publication.ImageProtocolContract == nil {
			continue
		}
		var old ZTAPIModelPriceSource
		if err := DB.First(&old, publication.PriceSourceID).Error; err != nil {
			return ZTAPIModelPriceSource{}, nil, err
		}
		var source ZTAPIModelPriceSource
		if old.SourceDocumentChecksum == quote.WorkbookSHA256 {
			if err := validateZTAPIABPriceSource(&old); err != nil {
				return ZTAPIModelPriceSource{}, nil, err
			}
			source = old
		} else {
			source, err = BuildZTAPIABImage2PriceSource(quote, old, publication.ImageProtocolContract, 1, 1_789_000_000)
			if err != nil {
				return ZTAPIModelPriceSource{}, nil, err
			}
		}
		publication.MediaPriceContractJSON = source.MediaPriceContractJSON
		_, rules, _ := ztapiPublicMediaMetadata(publication)
		if len(rules) == 0 {
			return ZTAPIModelPriceSource{}, nil, errors.New("gpt-image-2 has no customer-visible price rules")
		}
		return source, rules, nil
	}
	return ZTAPIModelPriceSource{}, nil, errors.New("gpt-image-2 frozen publication is unavailable")
}

// ApplyZTAPIABCommercialPricing changes only explicitly named existing publications.
// Every selected source, snapshot and catalog version is written in one transaction.
func ApplyZTAPIABCommercialPricing(operatorID int, workbookSHA string, modelNames []string) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return result, err
	}
	if operatorID <= 0 || workbookSHA != quote.WorkbookSHA256 || len(modelNames) == 0 || DB == nil {
		return result, errors.New("A/B repricing requires an operator, exact workbook checksum, models and database")
	}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		fxParams, fxErr := currentZTAPIABFXParams(tx)
		if fxErr != nil {
			return fxErr
		}
		return applyZTAPIABCommercialPricingTx(tx, quote, operatorID, modelNames, fxParams, &result)
	})
	if err != nil {
		return ZTAPICommercialRepricingResult{}, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}

// ApplyZTAPIABPoolOfficial78Pricing publishes only verified pool routes. A
// failure rolls back the source and snapshot together; callers should submit
// independent models separately so one unavailable route does not stall others.
func ApplyZTAPIABPoolOfficial78Pricing(operatorID int, workbookSHA string, modelNames []string) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return result, err
	}
	if operatorID <= 0 || workbookSHA != quote.WorkbookSHA256 || len(modelNames) == 0 || DB == nil {
		return result, errors.New("pool repricing requires an operator, exact workbook checksum, models and database")
	}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		fxParams, fxErr := currentZTAPIABFXParams(tx)
		if fxErr != nil {
			return fxErr
		}
		return applyZTAPIABCommercialPricingTxMode(tx, quote, operatorID, modelNames, fxParams, &result, true, false)
	})
	if err != nil {
		return ZTAPICommercialRepricingResult{}, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}

func ApplyZTAPIABEnterprise15Pricing(operatorID int, workbookSHA string, modelNames []string) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return result, err
	}
	if operatorID <= 0 || workbookSHA != quote.WorkbookSHA256 || len(modelNames) == 0 || DB == nil {
		return result, errors.New("enterprise repricing requires an operator, exact workbook checksum, models and database")
	}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		fxParams, fxErr := currentZTAPIABFXParams(tx)
		if fxErr != nil {
			return fxErr
		}
		return applyZTAPIABCommercialPricingTxMode(tx, quote, operatorID, modelNames, fxParams, &result, false, true)
	})
	if err != nil {
		return ZTAPICommercialRepricingResult{}, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}

// applyZTAPIABCommercialPricingTx re-prices the named models inside an open
// catalog write, so callers can combine it with other catalog changes.
func applyZTAPIABCommercialPricingTx(tx *gorm.DB, quote ZTAPIABQuotationManifest, operatorID int,
	modelNames []string, fxParams ZTAPIABFXParams, result *ZTAPICommercialRepricingResult) error {
	return applyZTAPIABCommercialPricingTxMode(tx, quote, operatorID, modelNames, fxParams, result, false, false)
}

func applyZTAPIABCommercialPricingTxMode(tx *gorm.DB, quote ZTAPIABQuotationManifest, operatorID int,
	modelNames []string, fxParams ZTAPIABFXParams, result *ZTAPICommercialRepricingResult, pool78, enterprise15 bool) error {
	frozen, err := ZTAPIQuotationEntries()
	if err != nil {
		return err
	}
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	if err != nil {
		return err
	}
	byName := make(map[string]ZTAPIABModelIdentity, len(bridge.Mapped))
	for _, identity := range bridge.Mapped {
		byName[identity.ModelName] = identity
	}
	selected := make(map[string]ZTAPIABModelIdentity, len(modelNames))
	for _, name := range modelNames {
		if enterprise15 {
			if name == "GPT 5.4 Mini" {
				return errors.New("GPT 5.4 Mini keeps its current enterprise price")
			}
			rows, err := quote.EnterpriseBasis(name)
			if err != nil || len(rows) != 1 || rows[0].Modality != "text" {
				return fmt.Errorf("%s is not an enterprise-only text model", name)
			}
			for _, row := range quote.Entries {
				if row.ModelName == name && row.Grade == "B" && row.Active {
					return fmt.Errorf("%s also has an active B quotation", name)
				}
			}
		}
		identity, ok := byName[name]
		if !ok || identity.SourceModel == "" {
			return fmt.Errorf("A/B quotation model %q has no verified existing identity", name)
		}
		if _, duplicate := selected[name]; duplicate {
			return fmt.Errorf("A/B quotation model %q is repeated", name)
		}
		selected[name] = identity
	}
	orderedNames := append([]string(nil), modelNames...)
	sort.Strings(orderedNames)
	effectiveAt := time.Date(2026, time.September, 15, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)).Unix()
	var configs []ZTAPIModelConfig
	if err := tx.Where("published = ?", true).Find(&configs).Error; err != nil {
		return err
	}
	published := make(map[string]ZTAPIModelConfig, len(configs))
	for _, config := range configs {
		published[config.SourceModel] = config
	}
	publications, err := loadZTAPIRepriceablePublications(tx)
	if err != nil {
		return err
	}
	bound := make(map[int]ZTAPIRuntimePublication, len(publications))
	for _, publication := range publications {
		bound[publication.ModelConfigID] = publication
	}
	for _, name := range orderedNames {
		identity := selected[name]
		config, ok := published[identity.SourceModel]
		if !ok || config.PublicNameValue() != identity.PublicName || config.Protocol != identity.Protocol ||
			config.ProviderFamily != identity.ProviderFamily {
			return fmt.Errorf("%s has no matching published model and route identity", name)
		}
		publication, ok := bound[config.ID]
		if !ok || publication.SnapshotID != config.PublicationSnapshotID || publication.Version != config.Version {
			return fmt.Errorf("%s lacks an active frozen publication", name)
		}
		var previous ZTAPIModelPublicationSnapshot
		if err := tx.First(&previous, publication.SnapshotID).Error; err != nil {
			return err
		}
		if len(previous.ChannelIDs()) == 0 {
			return fmt.Errorf("%s lacks a previously verified route", name)
		}
		var oldSource ZTAPIModelPriceSource
		if err := tx.First(&oldSource, publication.PriceSourceID).Error; err != nil {
			return err
		}
		effectivePool78 := pool78 || oldSource.PricePolicy == string(ZTAPIPricePolicyPoolOfficial78Sep2026)
		firstPoolSwitch := effectivePool78 && oldSource.PricePolicy != string(ZTAPIPricePolicyPoolOfficial78Sep2026)
		// Media quotes keep the legacy USD contract until media FX is supported.
		targetFX := fxParams
		if name == ztapiImage2QuotationModel {
			targetFX = ZTAPIABFXParams{}
		}
		if oldSource.SourceDocumentChecksum == quote.WorkbookSHA256 &&
			(previous.TokenPriceRulesJSON != oldSource.TokenPriceRulesJSON ||
				previous.MediaPriceContractJSON != oldSource.MediaPriceContractJSON) {
			return fmt.Errorf("%s already has inconsistent A/B price evidence", name)
		}
		// The image quote can only be rebuilt from the first frozen quotation,
		// so an image price already published from this workbook stays as it is.
		if name == ztapiImage2QuotationModel && oldSource.SourceDocumentChecksum == quote.WorkbookSHA256 &&
			validateZTAPIABPriceSource(&oldSource) == nil {
			result.Unchanged++
			continue
		}
		var target ZTAPIModelPriceSource
		if name == "GPT Image 2" {
			if previous.MediaPriceContractJSON == "" || previous.MediaPriceContractJSON != oldSource.MediaPriceContractJSON {
				return fmt.Errorf("%s lacks frozen media price evidence", name)
			}
			image, canonical, parseErr := types.ParseZTAPIImageProtocolContract(previous.ImageProtocolContractJSON)
			if parseErr != nil || canonical != previous.ImageProtocolContractJSON {
				return fmt.Errorf("%s lacks frozen image protocol evidence", name)
			}
			oldContract, parseErr := types.ParseZTAPIMediaPriceContract(previous.MediaPriceContractJSON)
			if parseErr != nil || types.ValidateZTAPIImagePriceProtocolCompatibility(oldContract, image) != nil {
				return fmt.Errorf("%s frozen image pricing does not match protocol", name)
			}
			target, err = BuildZTAPIABImage2PriceSource(quote, oldSource, &image, operatorID, effectiveAt)
		} else if effectivePool78 {
			target, err = BuildZTAPIABPoolOfficial78TextPriceSourceWithFX(quote, name, config.ID, operatorID, effectiveAt, targetFX)
		} else {
			target, err = BuildZTAPIABTextPriceSourceWithFX(quote, name, config.ID, operatorID, effectiveAt, targetFX)
		}
		if errors.Is(err, ErrZTAPIUpstreamUSDRateUnset) {
			result.Skipped = append(result.Skipped, config.PublicNameValue())
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !firstPoolSwitch {
			target.SaleMultiplier = oldSource.SaleMultiplier
		}
		if enterprise15 {
			oldMultiplier, multiplierErr := ztapiNormalizedSaleMultiplier(oldSource.SaleMultiplier)
			if multiplierErr != nil {
				return multiplierErr
			}
			ceiling := decimal.RequireFromString("0.92")
			if oldMultiplier.LessThan(ceiling) {
				ceiling = oldMultiplier
			}
			target.SaleMultiplier = ceiling.StringFixed(10)
		}
		// Nothing to publish when the rebuilt price matches what customers pay.
		oldMultiplier, oldMultiplierErr := ztapiNormalizedSaleMultiplier(oldSource.SaleMultiplier)
		targetMultiplier, targetMultiplierErr := ztapiNormalizedSaleMultiplier(target.SaleMultiplier)
		if oldMultiplierErr != nil || targetMultiplierErr != nil {
			return fmt.Errorf("%s has an invalid sale multiplier", name)
		}
		if oldSource.SourceDocumentChecksum == quote.WorkbookSHA256 &&
			oldSource.MediaPriceContractJSON == target.MediaPriceContractJSON &&
			oldMultiplier.Equal(targetMultiplier) &&
			ztapiABPriceSourceEvidenceMismatch(&oldSource, &target) == "" &&
			validateZTAPIABPriceSource(&oldSource) == nil {
			result.Unchanged++
			continue
		}
		preview, err := BuildZTAPIModelPricePreview(&target)
		if err != nil {
			return fmt.Errorf("%s price preview: %w", name, err)
		}
		if err := ztapiPreviewKeepsMinimumMargin(preview); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if effectivePool78 {
			if err := ztapiPool78KeepsOldPriceAndBonusMargin(&oldSource, &target, preview); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		} else if enterprise15 {
			if err := ztapiRepriceDoesNotRaise(&oldSource, &target); err != nil {
				result.Unchanged++
				continue
			}
		}
		var latest uint64
		if err := tx.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).
			Select("COALESCE(MAX(version), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		target.Version = latest + 1
		target.CreatedAt = now
		if err := tx.Create(&target).Error; err != nil {
			return err
		}
		config.CacheReadRatio = previous.CacheReadRatio
		config.CacheCreationRatio = previous.CacheCreationRatio
		config.CacheCreation5mRatio = previous.CacheCreation5mRatio
		config.CacheCreation1hRatio = previous.CacheCreation1hRatio
		config.ImageRatio = previous.ImageRatio
		config.AudioRatio = previous.AudioRatio
		config.AudioCompletionRatio = previous.AudioCompletionRatio
		if err := applyZTAPICommercialPreview(&config, preview); err != nil {
			return err
		}
		var verificationIDs []int64
		allowedChannels := previous.ChannelIDs()
		if firstPoolSwitch {
			blocks, fresh, gateErr := ztapiPublicationBlockersTx(tx, &config)
			if gateErr != nil || len(blocks) != 0 {
				return fmt.Errorf("%s pool publication gate: %v %v", name, blocks, gateErr)
			}
			foundPool := false
			for _, channelID := range fresh.AllowedChannelIDs {
				if channelID == 2 {
					foundPool = true
				}
			}
			if !foundPool {
				return fmt.Errorf("%s pool channel 2 is not a verified route", name)
			}
			var latest ZTAPIModelVerification
			if err := tx.Where("model_config_id = ? AND channel_id = ?", config.ID, 2).
				Order("verified_at DESC, id DESC").First(&latest).Error; err != nil {
				return fmt.Errorf("%s lacks pool verification: %w", name, err)
			}
			if !latest.NonStreamingPassed || !latest.StreamingPassed || !latest.UsageReconciled || !latest.InvalidKeyClassified {
				return fmt.Errorf("%s latest pool verification did not pass", name)
			}
			allowedChannels = []int{2}
			verificationIDs = []int64{latest.ID}
		} else if err := json.Unmarshal([]byte(previous.VerificationIDs), &verificationIDs); err != nil {
			return fmt.Errorf("%s has invalid frozen verification evidence", name)
		}
		if enterprise15 {
			var enterpriseVerificationIDs []int64
			for _, id := range verificationIDs {
				var verification ZTAPIModelVerification
				if err := tx.First(&verification, id).Error; err != nil {
					return err
				}
				if verification.ChannelID == 1 {
					enterpriseVerificationIDs = append(enterpriseVerificationIDs, id)
				}
			}
			foundEnterprise := false
			for _, channelID := range allowedChannels {
				foundEnterprise = foundEnterprise || channelID == 1
			}
			if !foundEnterprise || len(enterpriseVerificationIDs) == 0 {
				return fmt.Errorf("%s lacks a verified enterprise route", name)
			}
			allowedChannels = []int{1}
			verificationIDs = enterpriseVerificationIDs
		}
		if effectivePool78 && (len(allowedChannels) != 1 || allowedChannels[0] != 2) {
			return fmt.Errorf("%s pool pricing cannot retain an enterprise route", name)
		}
		evidence := ztapiPublicationEvidence{
			AllowedChannelIDs: allowedChannels, VerificationIDs: verificationIDs,
			PriceSourceID: target.ID, IdentityUpdatedAt: previous.IdentityUpdatedAt,
			ImageProtocolContractJSON: previous.ImageProtocolContractJSON,
			VideoProtocolContractJSON: previous.VideoProtocolContractJSON,
		}
		nextVersion := config.Version + 1
		snapshot, err := createZTAPIModelPublicationSnapshotTx(tx, &config, nextVersion, evidence)
		if err != nil {
			return fmt.Errorf("%s snapshot: %w", name, err)
		}
		updates := map[string]any{
			"input_cost_per_million": config.InputCostPerMillion, "output_cost_per_million": config.OutputCostPerMillion,
			"input_price_per_million": config.InputPricePerMillion, "output_price_per_million": config.OutputPricePerMillion,
			"cache_read_ratio": config.CacheReadRatio, "cache_creation_ratio": config.CacheCreationRatio,
			"cache_creation_5m_ratio": config.CacheCreation5mRatio, "cache_creation_1h_ratio": config.CacheCreation1hRatio,
			"publication_snapshot_id": snapshot.ID, "version": nextVersion, "updated_at": now,
		}
		updated := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ?", config.ID, config.Version).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrZTAPIModelVersionConflict
		}
		payload := common.MapToJsonStr(map[string]any{
			"previous_price_source_id": oldSource.ID, "price_source_id": target.ID,
			"workbook_sha256": quote.WorkbookSHA256, "quotation_grade": target.QuotationGrade,
			"quotation_cell": target.QuotationCell, "price_policy": target.PricePolicy,
			"fx_mode": target.FXMode, "platform_cny_per_unit": target.PlatformCNYPerUnit,
			"upstream_cny_per_usd": target.UpstreamCNYPerUSD,
		})
		action := "model.ab_quote_published"
		if effectivePool78 {
			action = "model.pool_official_78_published"
		} else if enterprise15 {
			action = "model.enterprise_cost_plus_15_published"
		}
		if err := tx.Create(&ZTAPIAuditEvent{
			Action: action, ModelConfigID: config.ID,
			PublicName: config.PublicNameValue(), Version: nextVersion,
			OperatorID: operatorID, Payload: payload, CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		result.Imported++
		result.Republished++
		result.Models = append(result.Models, config.PublicNameValue())
	}
	return nil
}
