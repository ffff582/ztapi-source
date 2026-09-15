package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
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
	frozen, err := ZTAPIQuotationEntries()
	if err != nil {
		return result, err
	}
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	if err != nil {
		return result, err
	}
	byName := make(map[string]ZTAPIABModelIdentity, len(bridge.Mapped))
	for _, identity := range bridge.Mapped {
		byName[identity.ModelName] = identity
	}
	selected := make(map[string]ZTAPIABModelIdentity, len(modelNames))
	for _, name := range modelNames {
		identity, ok := byName[name]
		if !ok || identity.SourceModel == "" {
			return result, fmt.Errorf("A/B quotation model %q has no verified existing identity", name)
		}
		if _, duplicate := selected[name]; duplicate {
			return result, fmt.Errorf("A/B quotation model %q is repeated", name)
		}
		selected[name] = identity
	}
	orderedNames := append([]string(nil), modelNames...)
	sort.Strings(orderedNames)
	effectiveAt := time.Date(2026, time.September, 15, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)).Unix()
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var configs []ZTAPIModelConfig
		if err := tx.Where("published = ?", true).Find(&configs).Error; err != nil {
			return err
		}
		published := make(map[string]ZTAPIModelConfig, len(configs))
		for _, config := range configs {
			published[config.SourceModel] = config
		}
		publications, err := loadZTAPIQuotedPublications(tx)
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
			if oldSource.SourceDocumentChecksum == quote.WorkbookSHA256 {
				if err := validateZTAPIABPriceSource(&oldSource); err != nil || previous.TokenPriceRulesJSON != oldSource.TokenPriceRulesJSON ||
					previous.MediaPriceContractJSON != oldSource.MediaPriceContractJSON {
					return fmt.Errorf("%s already has inconsistent A/B price evidence", name)
				}
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
			} else {
				target, err = BuildZTAPIABTextPriceSource(quote, name, config.ID, operatorID, effectiveAt)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			preview, err := BuildZTAPIModelPricePreview(&target)
			if err != nil {
				return fmt.Errorf("%s price preview: %w", name, err)
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
			if err := json.Unmarshal([]byte(previous.VerificationIDs), &verificationIDs); err != nil {
				return fmt.Errorf("%s has invalid frozen verification evidence", name)
			}
			evidence := ztapiPublicationEvidence{
				AllowedChannelIDs: previous.ChannelIDs(), VerificationIDs: verificationIDs,
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
			})
			if err := tx.Create(&ZTAPIAuditEvent{
				Action: "model.ab_quote_published", ModelConfigID: config.ID,
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
	})
	if err != nil {
		return ZTAPICommercialRepricingResult{}, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}
