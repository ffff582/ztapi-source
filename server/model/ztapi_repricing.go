package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ZTAPICommercialRepricingResult struct {
	Imported    int      `json:"imported"`
	Republished int      `json:"republished"`
	Unchanged   int      `json:"unchanged"`
	Models      []string `json:"models"`
}

func clearZTAPIPriceSourceCosts(source *ZTAPIModelPriceSource) {
	source.InputPerMillion = "0"
	source.OutputPerMillion = "0"
	source.CacheReadPerMillion = "0"
	source.CacheWritePerMillion = "0"
	source.CacheWrite5mPerMillion = "0"
	source.CacheWrite1hPerMillion = "0"
	source.ImageUnitCost = "0"
	source.AudioUnitCost = "0"
	source.RequestUnitCost = "0"
}

func assignZTAPIPriceSourceCost(source *ZTAPIModelPriceSource, dimension string, value decimal.Decimal) error {
	raw := value.StringFixed(10)
	switch dimension {
	case ZTAPIBillingDimensionInputTokens:
		source.InputPerMillion = raw
	case ZTAPIBillingDimensionOutputTokens:
		source.OutputPerMillion = raw
	case ZTAPIBillingDimensionCacheRead:
		source.CacheReadPerMillion = raw
	case ZTAPIBillingDimensionCacheWrite:
		source.CacheWritePerMillion = raw
	case ZTAPIBillingDimensionCacheWrite5m:
		source.CacheWrite5mPerMillion = raw
	case ZTAPIBillingDimensionCacheWrite1h:
		source.CacheWrite1hPerMillion = raw
	case ZTAPIBillingDimensionImage:
		source.ImageUnitCost = raw
	case ZTAPIBillingDimensionAudio:
		source.AudioUnitCost = raw
	case ZTAPIBillingDimensionRequest:
		source.RequestUnitCost = raw
	default:
		return fmt.Errorf("unsupported ZTAPI billing dimension %q", dimension)
	}
	return nil
}

func buildZTAPICommercialPriceSourceV2(current ZTAPIModelPriceSource) (ZTAPIModelPriceSource, *ZTAPIModelPricePreview, error) {
	next := current
	next.ID = 0
	next.Version = 0
	next.CreatedAt = 0

	official, discount, poolQuoted := ztapiPoolOfficialPriceContractFromManifest(ztapiQuotation, current.SourceModel)
	if poolQuoted {
		next.ResourceType = "pool"
		next.PricePolicy = string(ZTAPIPricePolicyPoolOfficial80)
		next.Currency = "USD"
		next.CNYPerUSD = "0"
		next.SourceDocumentChecksum = ZTAPIQuotationSHA256
		clearZTAPIPriceSourceCosts(&next)
		dimensions := make([]string, 0, len(official))
		for dimension, price := range official {
			dimensions = append(dimensions, dimension)
			if err := assignZTAPIPriceSourceCost(&next, dimension, price.Mul(discount)); err != nil {
				return ZTAPIModelPriceSource{}, nil, err
			}
		}
		sort.Strings(dimensions)
		raw, _ := json.Marshal(dimensions)
		next.BillingDimensions = string(raw)
	} else {
		next.PricePolicy = string(ZTAPIPricePolicyEnterprise20Margin)
	}
	if mediaRow, ok := ztapiMediaPriceRowFromManifest(ztapiQuotation, current.SourceModel); ok {
		next.MediaPriceContractJSON = mediaRow.MediaPriceContractJSON
	}

	preview, err := BuildZTAPIModelPricePreview(&next)
	if err != nil {
		return ZTAPIModelPriceSource{}, nil, err
	}
	return next, preview, nil
}

func ztapiPriceSourceAlreadyMatchesV2(current, target ZTAPIModelPriceSource) bool {
	if current.ResourceType != target.ResourceType || current.PricePolicy != target.PricePolicy ||
		current.BillingDimensions != target.BillingDimensions || current.Currency != target.Currency ||
		current.CNYPerUSD != target.CNYPerUSD || current.SourceDocumentChecksum != target.SourceDocumentChecksum ||
		current.MediaPriceContractJSON != target.MediaPriceContractJSON {
		return false
	}
	left := ztapiPriceSourceValues(&current)
	right := ztapiPriceSourceValues(&target)
	for dimension, value := range left {
		l, lerr := decimal.NewFromString(strings.TrimSpace(value))
		r, rerr := decimal.NewFromString(strings.TrimSpace(right[dimension]))
		if lerr != nil || rerr != nil || !l.Equal(r) {
			return false
		}
	}
	return true
}

func applyZTAPICommercialPreview(config *ZTAPIModelConfig, preview *ZTAPIModelPricePreview) error {
	inputCost, err := decimal.NewFromString(preview.InputCostUSDPerMillion)
	if err != nil {
		return err
	}
	outputCost, err := decimal.NewFromString(preview.OutputCostUSDPerMillion)
	if err != nil {
		return err
	}
	inputSale, err := decimal.NewFromString(preview.InputSaleUSDPerMillion)
	if err != nil {
		return err
	}
	outputSale, err := decimal.NewFromString(preview.OutputSaleUSDPerMillion)
	if err != nil {
		return err
	}
	config.InputCostPerMillion = inputCost.InexactFloat64()
	config.OutputCostPerMillion = outputCost.InexactFloat64()
	config.InputPricePerMillion = inputSale.InexactFloat64()
	config.OutputPricePerMillion = outputSale.InexactFloat64()
	for column, ratio := range ztapiQuotedCacheRatios(preview) {
		switch column {
		case "cache_read_ratio":
			config.CacheReadRatio = ratio
		case "cache_creation_ratio":
			config.CacheCreationRatio = ratio
		case "cache_creation_5m_ratio":
			config.CacheCreation5mRatio = ratio
		case "cache_creation_1h_ratio":
			config.CacheCreation1hRatio = ratio
		}
	}
	return nil
}

func ApplyZTAPICommercialPricingV2(operatorID int) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{}
	if operatorID <= 0 {
		return result, errors.New("commercial repricing requires an operator")
	}
	if DB == nil {
		return result, errors.New("ZTAPI database is not initialized")
	}
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var configs []ZTAPIModelConfig
		if err := tx.Where("published = ?", true).Order("source_model ASC").Find(&configs).Error; err != nil {
			return err
		}
		publications, err := loadZTAPIQuotedPublications(tx)
		if err != nil {
			return err
		}
		publicationByModel := make(map[int]ZTAPIRuntimePublication, len(publications))
		for _, publication := range publications {
			publicationByModel[publication.ModelConfigID] = publication
		}
		if len(publicationByModel) != len(configs) {
			return errors.New("commercial repricing requires every published model to have a valid frozen publication")
		}

		for i := range configs {
			config := configs[i]
			publication, ok := publicationByModel[config.ID]
			if !ok || publication.SnapshotID != config.PublicationSnapshotID || publication.Version != config.Version {
				return fmt.Errorf("published evidence is not active for %s", config.SourceModel)
			}
			var currentSource ZTAPIModelPriceSource
			if err := tx.First(&currentSource, publication.PriceSourceID).Error; err != nil {
				return fmt.Errorf("load bound price source for %s: %w", config.SourceModel, err)
			}
			target, preview, err := buildZTAPICommercialPriceSourceV2(currentSource)
			if err != nil {
				return fmt.Errorf("build commercial price for %s: %w", config.SourceModel, err)
			}
			if ztapiPriceSourceAlreadyMatchesV2(currentSource, target) {
				result.Unchanged++
				continue
			}

			var previousSnapshot ZTAPIModelPublicationSnapshot
			if err := tx.First(&previousSnapshot, publication.SnapshotID).Error; err != nil {
				return err
			}
			config.CacheReadRatio = previousSnapshot.CacheReadRatio
			config.CacheCreationRatio = previousSnapshot.CacheCreationRatio
			config.CacheCreation5mRatio = previousSnapshot.CacheCreation5mRatio
			config.CacheCreation1hRatio = previousSnapshot.CacheCreation1hRatio
			config.ImageRatio = previousSnapshot.ImageRatio
			config.AudioRatio = previousSnapshot.AudioRatio
			config.AudioCompletionRatio = previousSnapshot.AudioCompletionRatio
			var latestVersion uint64
			if err := tx.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).
				Select("COALESCE(MAX(version), 0)").Scan(&latestVersion).Error; err != nil {
				return err
			}
			now := common.GetTimestamp()
			target.ModelConfigID = config.ID
			target.SourceModel = config.SourceModel
			target.OperatorID = operatorID
			target.Version = latestVersion + 1
			target.CreatedAt = now
			if err := tx.Create(&target).Error; err != nil {
				return err
			}
			if err := applyZTAPICommercialPreview(&config, preview); err != nil {
				return err
			}
			var verificationIDs []int64
			if err := json.Unmarshal([]byte(previousSnapshot.VerificationIDs), &verificationIDs); err != nil {
				return errors.New("published verification evidence is invalid")
			}
			evidence := ztapiPublicationEvidence{
				AllowedChannelIDs:         previousSnapshot.ChannelIDs(),
				VerificationIDs:           verificationIDs,
				PriceSourceID:             target.ID,
				IdentityUpdatedAt:         previousSnapshot.IdentityUpdatedAt,
				ImageProtocolContractJSON: previousSnapshot.ImageProtocolContractJSON,
				VideoProtocolContractJSON: previousSnapshot.VideoProtocolContractJSON,
			}
			nextVersion := config.Version + 1
			snapshot, err := createZTAPIModelPublicationSnapshotTx(tx, &config, nextVersion, evidence)
			if err != nil {
				return fmt.Errorf("create commercial snapshot for %s: %w", config.SourceModel, err)
			}
			updates := map[string]any{
				"input_cost_per_million": config.InputCostPerMillion, "output_cost_per_million": config.OutputCostPerMillion,
				"input_price_per_million": config.InputPricePerMillion, "output_price_per_million": config.OutputPricePerMillion,
				"cache_read_ratio": config.CacheReadRatio, "cache_creation_ratio": config.CacheCreationRatio,
				"cache_creation_5m_ratio": config.CacheCreation5mRatio, "cache_creation_1h_ratio": config.CacheCreation1hRatio,
				"image_ratio": config.ImageRatio, "audio_ratio": config.AudioRatio,
				"audio_completion_ratio":  config.AudioCompletionRatio,
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
				"previous_price_source_id": currentSource.ID, "price_source_id": target.ID,
				"price_source_version": target.Version, "price_policy": target.PricePolicy,
				"resource_type": target.ResourceType, "reason": "apply approved commercial pricing v2",
			})
			if err := tx.Create(&ZTAPIAuditEvent{
				Action: "model.commercial_pricing_v2_published", ModelConfigID: config.ID,
				PublicName: config.PublicNameValue(), Version: nextVersion, OperatorID: operatorID,
				Payload: payload, CreatedAt: now,
			}).Error; err != nil {
				return err
			}
			result.Imported++
			result.Republished++
			result.Models = append(result.Models, config.PublicNameValue())
		}
		return nil
	})
	if err != nil {
		return ZTAPICommercialRepricingResult{}, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}
