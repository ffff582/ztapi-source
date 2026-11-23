package model

import (
	"errors"
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type ZTAPISupplierPriceChange20261019 struct {
	PublicName       string                      `json:"public_name"`
	Version          uint64                      `json:"version"`
	PreviousSourceID int64                       `json:"previous_price_source_id"`
	Channels         []int                       `json:"allowed_channels"`
	Before           *ZTAPIModelPricePreview     `json:"before"`
	After            *ZTAPIModelPricePreview     `json:"after"`
	TokenPriceRules  []ZTAPIPublicTokenPriceRule `json:"token_price_rules"`
}

type ZTAPISupplierPricingPreview20261019 struct {
	WorkbookSHA256 string                             `json:"workbook_sha256"`
	Ready          []ZTAPISupplierPriceChange20261019 `json:"ready"`
	Blocked        []ZTAPIABQuotePricingBlocked       `json:"blocked"`
}

func ztapiSupplierRepriceDoesNotRaise20261019(tx *gorm.DB, old, target *ZTAPIModelPriceSource) error {
	if old == nil || target == nil || old.SourceModel != "kimi-k3" || target.SourceModel != "kimi-k3" ||
		old.ModelConfigID != target.ModelConfigID || old.ResourceType != "pool" ||
		target.SourceDocumentChecksum != ZTAPISupplierWorkbook20261019SHA256 ||
		target.PricePolicy != string(ZTAPIPricePolicyQuoteFrozenRetail20261019) {
		return ztapiRepriceDoesNotRaise(old, target)
	}
	if err := ValidateZTAPISupplierPriceSource20261019(tx, target); err != nil {
		return err
	}
	var oldRules, newRules []ztapiABFrozenSaleRule
	if err := common.Unmarshal([]byte(old.TokenPriceRulesJSON), &oldRules); err != nil {
		return err
	}
	if err := common.Unmarshal([]byte(target.TokenPriceRulesJSON), &newRules); err != nil {
		return err
	}
	if len(oldRules) != len(newRules) || len(oldRules) == 0 {
		return ztapiRepriceDoesNotRaise(old, target)
	}
	for tier, rule := range oldRules {
		// Only the first migration from K3's complete three-dimension retail
		// contract may add the two independently rebuilt TTL prices.
		if len(rule.Sale) != 3 {
			return ztapiRepriceDoesNotRaise(old, target)
		}
		for _, dimension := range []string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens, ZTAPIBillingDimensionCacheRead} {
			if _, found := rule.Sale[dimension]; !found {
				return ztapiRepriceDoesNotRaise(old, target)
			}
		}
		if newRules[tier].RetailBasisID != old.ID {
			return errors.New("K3 TTL addition requires the current original retail anchor")
		}
		delete(newRules[tier].Sale, ZTAPIBillingDimensionCacheWrite5m)
		delete(newRules[tier].Sale, ZTAPIBillingDimensionCacheWrite1h)
	}
	raw, err := common.Marshal(newRules)
	if err != nil {
		return err
	}
	comparison := *target
	comparison.TokenPriceRulesJSON = string(raw)
	return ztapiRepriceDoesNotRaise(old, &comparison)
}

func supplierPriceCandidate20261019(tx *gorm.DB, config ZTAPIModelConfig, operatorID int, effectiveAt int64,
	fx ZTAPIABFXParams) (ZTAPIModelPublicationSnapshot, ZTAPIModelPriceSource, ZTAPIModelPriceSource, error) {
	var previous ZTAPIModelPublicationSnapshot
	var old, target ZTAPIModelPriceSource
	if !config.Published || config.PublicationSnapshotID <= 0 {
		return previous, old, target, errors.New("only existing published models may be repriced")
	}
	if err := tx.First(&previous, config.PublicationSnapshotID).Error; err != nil {
		return previous, old, target, err
	}
	if previous.ModelConfigID != config.ID || previous.ModelVersion != config.Version ||
		previous.SourceModel != config.SourceModel || previous.PublicName != config.PublicNameValue() ||
		previous.Protocol != config.Protocol || previous.ProviderFamily != config.ProviderFamily || len(previous.ChannelIDs()) == 0 {
		return previous, old, target, errors.New("current frozen publication does not match model identity")
	}
	if err := tx.First(&old, previous.PriceSourceID).Error; err != nil {
		return previous, old, target, err
	}
	if old.ModelConfigID != config.ID || old.SourceModel != config.SourceModel || previous.PricePolicy != old.PricePolicy ||
		previous.TokenPriceRulesJSON != old.TokenPriceRulesJSON || previous.MediaPriceContractJSON != old.MediaPriceContractJSON {
		return previous, old, target, errors.New("current frozen source does not match publication")
	}
	if err := ValidateZTAPIQuotationIdentity(config.SourceModel, config.PublicNameValue(), config.Protocol, config.ProviderFamily, old.SourceDocumentChecksum); err != nil {
		return previous, old, target, err
	}
	var err error
	anchor := &old
	if old.SourceDocumentChecksum == ZTAPISupplierWorkbook20261019SHA256 {
		if err := ValidateZTAPISupplierPriceSource20261019(tx, &old); err != nil {
			return previous, old, target, err
		}
		anchor, err = ztapiSupplierRetailAnchor20261019(tx, &old)
		if err != nil {
			return previous, old, target, err
		}
	} else if !ztapiPublicationPriceBasisPermitted(&old) {
		return previous, old, target, errors.New("current price basis is not independently validated")
	}
	oldPreview, err := BuildZTAPIModelPricePreview(&old)
	if err != nil || !ztapiStoredPriceMatchesPreview(previous.InputPricePerMillion, oldPreview.InputSaleUSDPerMillion) ||
		!ztapiStoredPriceMatchesPreview(previous.OutputPricePerMillion, oldPreview.OutputSaleUSDPerMillion) {
		return previous, old, target, errors.New("current snapshot retail differs from its source")
	}
	target, err = BuildZTAPISupplierPriceSource20261019(anchor, operatorID, effectiveAt, fx)
	if err != nil {
		return previous, old, target, err
	}
	// A procurement quote is never permission to change or expand a route.
	if target.QuotationGrade == "B" && target.ResourceType == "pool" &&
		(config.ProviderFamily == ZTAPIProviderAnthropic || config.ProviderFamily == ZTAPIProviderOpenAI) &&
		(len(previous.ChannelIDs()) != 1 || previous.ChannelIDs()[0] != 2) {
		return previous, old, target, errors.New("pool GPT/Claude price requires the already frozen pool-only route")
	}
	if err := ztapiSupplierRepriceDoesNotRaise20261019(tx, &old, &target); err != nil {
		return previous, old, target, err
	}
	preview, err := BuildZTAPIModelPricePreview(&target)
	if err == nil {
		err = ztapiPreviewKeepsMinimumMargin(preview)
	}
	return previous, old, target, err
}

func PreviewZTAPISupplierPricing20261019() (ZTAPISupplierPricingPreview20261019, error) {
	result := ZTAPISupplierPricingPreview20261019{WorkbookSHA256: ZTAPISupplierWorkbook20261019SHA256,
		Ready: []ZTAPISupplierPriceChange20261019{}, Blocked: []ZTAPIABQuotePricingBlocked{}}
	if DB == nil {
		return result, errors.New("database is not initialized")
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		fx, err := currentZTAPIABFXParams(tx)
		if err != nil {
			return err
		}
		var configs []ZTAPIModelConfig
		if err := tx.Where("published = ?", true).Order("id ASC").Find(&configs).Error; err != nil {
			return err
		}
		for _, config := range configs {
			previous, old, target, err := supplierPriceCandidate20261019(tx, config, 1, common.GetTimestamp(), fx)
			if err != nil {
				result.Blocked = append(result.Blocked, ZTAPIABQuotePricingBlocked{ModelName: config.PublicNameValue(), Reason: err.Error()})
				continue
			}
			before, err := BuildZTAPIModelPricePreview(&old)
			if err != nil {
				return err
			}
			after, err := BuildZTAPIModelPricePreview(&target)
			if err != nil {
				return err
			}
			rules, err := ztapiPublicTokenPriceRules(target.TokenPriceRulesJSON, after.BillingDimensions)
			if err != nil {
				return err
			}
			result.Ready = append(result.Ready, ZTAPISupplierPriceChange20261019{
				PublicName: config.PublicNameValue(), Version: config.Version, PreviousSourceID: old.ID,
				Channels: previous.ChannelIDs(), Before: before, After: after, TokenPriceRules: rules,
			})
		}
		return nil
	})
	return result, err
}

func ApplyZTAPISupplierPricing20261019(operatorID int, checksum string, names []string, expectedVersions map[string]uint64) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{}
	if operatorID <= 0 || DB == nil || checksum != ZTAPISupplierWorkbook20261019SHA256 || len(names) == 0 || len(names) != len(expectedVersions) {
		return result, errors.New("repricing requires exact quote, operator, selected models and expected versions")
	}
	selected := append([]string(nil), names...)
	sort.Strings(selected)
	for i, name := range selected {
		if name == "" || expectedVersions[name] == 0 || (i > 0 && name == selected[i-1]) {
			return result, errors.New("model list or expected versions are invalid")
		}
	}
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		fx, err := currentZTAPIABFXParams(tx)
		if err != nil {
			return err
		}
		now := common.GetTimestamp()
		for _, name := range selected {
			var config ZTAPIModelConfig
			if err := tx.Where("public_name = ? AND published = ?", name, true).First(&config).Error; err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if config.Version != expectedVersions[name] {
				return ErrZTAPIModelVersionConflict
			}
			previous, old, target, err := supplierPriceCandidate20261019(tx, config, operatorID, now, fx)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if old.SourceDocumentChecksum == checksum && ztapiABPriceSourceEvidenceMismatch(&old, &target) == "" && ztapiSameDecimal(old.SaleMultiplier, target.SaleMultiplier) {
				result.Unchanged++
				continue
			}
			var latest uint64
			if err := tx.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).
				Select("COALESCE(MAX(version), 0)").Scan(&latest).Error; err != nil {
				return err
			}
			target.Version, target.CreatedAt = latest+1, now
			if err := ValidateZTAPISupplierPriceSource20261019(tx, &target); err != nil {
				return err
			}
			if err := tx.Create(&target).Error; err != nil {
				return err
			}
			preview, err := BuildZTAPIModelPricePreview(&target)
			if err != nil {
				return err
			}
			config.CacheReadRatio, config.CacheCreationRatio = previous.CacheReadRatio, previous.CacheCreationRatio
			config.CacheCreation5mRatio, config.CacheCreation1hRatio = previous.CacheCreation5mRatio, previous.CacheCreation1hRatio
			config.ImageRatio, config.AudioRatio, config.AudioCompletionRatio = previous.ImageRatio, previous.AudioRatio, previous.AudioCompletionRatio
			if err := applyZTAPICommercialPreview(&config, preview); err != nil {
				return err
			}
			var verificationIDs []int64
			if err := common.Unmarshal([]byte(previous.VerificationIDs), &verificationIDs); err != nil {
				return err
			}
			evidence := ztapiPublicationEvidence{AllowedChannelIDs: previous.ChannelIDs(), VerificationIDs: verificationIDs,
				PriceSourceID: target.ID, IdentityUpdatedAt: previous.IdentityUpdatedAt,
				ImageProtocolContractJSON: previous.ImageProtocolContractJSON, VideoProtocolContractJSON: previous.VideoProtocolContractJSON}
			nextVersion := config.Version + 1
			snapshot, err := createZTAPIModelPublicationSnapshotTx(tx, &config, nextVersion, evidence)
			if err != nil {
				return err
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
			payload := common.MapToJsonStr(map[string]any{"previous_price_source_id": old.ID, "price_source_id": target.ID,
				"workbook_sha256": checksum, "quotation_grade": target.QuotationGrade, "quotation_cell": target.QuotationCell,
				"allowed_channels": previous.ChannelIDs(), "price_policy": target.PricePolicy})
			if err := tx.Create(&ZTAPIAuditEvent{Action: "model.supplier_quote_20261019_published", ModelConfigID: config.ID,
				PublicName: name, Version: nextVersion, OperatorID: operatorID, Payload: payload, CreatedAt: now}).Error; err != nil {
				return err
			}
			result.Imported++
			result.Republished++
			result.Models = append(result.Models, name)
		}
		return nil
	})
	if err != nil {
		return ZTAPICommercialRepricingResult{}, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}
