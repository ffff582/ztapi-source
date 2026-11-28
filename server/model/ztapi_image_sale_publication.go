package model

import (
	"errors"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

const (
	ZTAPIImageSaleActionEnable             = "enable"
	ZTAPIImageSaleActionRestoreLegacyToken = "restore_legacy_token"
)

// This approval is bound to the reviewed publication, not to a mutable draft.
type ZTAPIImageSalePublicationRequest struct {
	Action                string `json:"action,omitempty"`
	Confirm               bool   `json:"confirm"`
	ExpectedModelConfigID int    `json:"expected_model_config_id"`
	ExpectedPriceSourceID int64  `json:"expected_price_source_id"`
	ExpectedSnapshotID    int64  `json:"expected_snapshot_id"`
	ExpectedVersion       uint64 `json:"expected_version"`
}

func (request ZTAPIImageSalePublicationRequest) Validate() error {
	if request.Action == ZTAPIImageSaleActionRestoreLegacyToken {
		if !request.Confirm || request.ExpectedModelConfigID != 85 || request.ExpectedPriceSourceID <= 0 || request.ExpectedSnapshotID <= 0 || request.ExpectedVersion == 0 {
			return errors.New("legacy retail restore requires confirmation and explicit current publication IDs/version")
		}
		return nil
	}
	if request.Action != "" && request.Action != ZTAPIImageSaleActionEnable {
		return errors.New("unsupported image sale publication action")
	}
	if !request.Confirm || request.ExpectedModelConfigID != 85 || request.ExpectedPriceSourceID != 1873 ||
		request.ExpectedSnapshotID != 1631 || request.ExpectedVersion != 27 {
		return errors.New("explicit confirmation of reviewed image model 85, source 1873, snapshot 1631 and version 27 required")
	}
	return nil
}

func validateZTAPIImageSaleSnapshot(snapshot *ZTAPIModelPublicationSnapshot) error {
	if snapshot.ImageSaleContractJSON == "" {
		return nil
	}
	if snapshot.ModelConfigID != 85 || snapshot.SourceModel != "gpt-image-2" || snapshot.PublicName != "zt-gp-image-2" ||
		snapshot.ProviderFamily != ZTAPIProviderOpenAI || snapshot.Protocol != ZTAPIProtocolOpenAICompatible {
		return errors.New("image sale contract is approved only for GPT Image 2")
	}
	canonical, err := types.CanonicalizeZTAPIImageSaleContract(snapshot.ImageSaleContractJSON)
	if err != nil {
		return err
	}
	if canonical != snapshot.ImageSaleContractJSON {
		return errors.New("image sale publication contract must be canonical")
	}
	return nil
}

func inheritZTAPIImageSaleContractTx(tx *gorm.DB, config *ZTAPIModelConfig, previousSnapshotID int64) (string, error) {
	if config.ID != 85 {
		return "", nil
	}
	var current ZTAPIModelConfig
	if err := tx.First(&current, config.ID).Error; err != nil {
		return "", err
	}
	if previousSnapshotID == 0 {
		previousSnapshotID = config.PublicationSnapshotID
	}
	if current.PublicationSnapshotID != previousSnapshotID {
		return "", errors.New("current image retail publication pointer must be preserved explicitly")
	}
	if previousSnapshotID == 0 {
		return "", nil
	}
	var previous ZTAPIModelPublicationSnapshot
	if err := tx.First(&previous, previousSnapshotID).Error; err != nil {
		return "", err
	}
	if previous.ModelConfigID != config.ID || previous.SourceModel != config.SourceModel || previous.PublicName != config.PublicNameValue() ||
		previous.Protocol != config.Protocol || previous.ProviderFamily != config.ProviderFamily || previous.ModelVersion > current.Version ||
		(current.Published && previous.ModelVersion != current.Version) || !validZTAPISnapshotPricing(&previous) {
		return "", errors.New("current image retail publication authority is invalid")
	}
	return previous.ImageSaleContractJSON, nil
}

// PublishZTAPIImageSale clones only immutable authority. Procurement, routing,
// historical publications and balances are never rewritten by this operation.
func PublishZTAPIImageSale(operatorID int, request ZTAPIImageSalePublicationRequest) (*ZTAPIModelPublicationSnapshot, error) {
	if DB == nil || operatorID <= 0 {
		return nil, errors.New("database and authenticated operator required")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	var result *ZTAPIModelPublicationSnapshot
	restoring := request.Action == ZTAPIImageSaleActionRestoreLegacyToken
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var config ZTAPIModelConfig
		if err := tx.First(&config, request.ExpectedModelConfigID).Error; err != nil {
			return err
		}
		if config.Version != request.ExpectedVersion || config.PublicationSnapshotID != request.ExpectedSnapshotID {
			return ErrZTAPIModelVersionConflict
		}
		if !config.Published || config.SourceModel != "gpt-image-2" || config.PublicNameValue() != "zt-gp-image-2" ||
			config.Protocol != ZTAPIProtocolOpenAICompatible || config.ProviderFamily != ZTAPIProviderOpenAI {
			return errors.New("current GPT Image 2 identity differs from the reviewed publication")
		}
		var previous ZTAPIModelPublicationSnapshot
		if err := tx.First(&previous, request.ExpectedSnapshotID).Error; err != nil {
			return err
		}
		if previous.ModelConfigID != config.ID || previous.ModelVersion != config.Version || previous.PriceSourceID != request.ExpectedPriceSourceID ||
			previous.SourceModel != config.SourceModel || previous.PublicName != config.PublicNameValue() ||
			previous.Protocol != config.Protocol || previous.ProviderFamily != config.ProviderFamily {
			return errors.New("reviewed image publication source, route or identity changed")
		}
		if restoring {
			if previous.ImageSaleContractJSON == "" || validateZTAPIImageSaleSnapshot(&previous) != nil {
				return errors.New("legacy restore requires a valid active picture retail contract")
			}
		} else if !reflect.DeepEqual(previous.ChannelIDs(), []int{1}) || previous.ImageSaleContractJSON != "" {
			return errors.New("image sale enable requires the reviewed legacy publication and channel 1")
		}
		active, err := loadZTAPIActivePublications(tx)
		if err != nil {
			return err
		}
		valid := false
		for _, publication := range active {
			valid = valid || publication.ModelConfigID == config.ID && publication.SnapshotID == previous.ID
		}
		if !valid {
			return errors.New("GPT Image 2 requires a valid current immutable publication")
		}
		next := previous
		next.ID, next.ModelVersion, next.CreatedAt = 0, config.Version+1, common.GetTimestamp()
		next.ImageSaleContractJSON = types.ZTAPIImageSalePartial20261109JSON()
		action, auditAction := ZTAPIImageSaleActionEnable, "model.image_sale_published"
		if restoring {
			next.ImageSaleContractJSON = ""
			action, auditAction = ZTAPIImageSaleActionRestoreLegacyToken, "model.image_sale_legacy_token_restored"
		}
		if err := tx.Create(&next).Error; err != nil {
			return err
		}
		updated := tx.Model(&ZTAPIModelConfig{}).
			Where("id = ? AND version = ? AND publication_snapshot_id = ? AND published = ?", config.ID, request.ExpectedVersion, previous.ID, true).
			Updates(map[string]any{"publication_snapshot_id": next.ID, "version": next.ModelVersion, "updated_at": next.CreatedAt})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrZTAPIModelVersionConflict
		}
		if err := tx.Create(&ZTAPIAuditEvent{Action: auditAction, ModelConfigID: config.ID,
			PublicName: next.PublicName, Version: next.ModelVersion, OperatorID: operatorID, CreatedAt: next.CreatedAt,
			Payload: common.MapToJsonStr(map[string]any{"previous_snapshot_id": previous.ID, "snapshot_id": next.ID,
				"price_source_id": previous.PriceSourceID, "allowed_channels": previous.ChannelIDs(),
				"image_sale_contract_json": next.ImageSaleContractJSON, "historical_repricing": false, "action": action, "rollback_only": restoring,
				"procurement_unchanged": true, "routes_unchanged": true})}).Error; err != nil {
			return err
		}
		result = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}

func applyZTAPIPublicImageSale(publication ZTAPIRuntimePublication, item *ZTAPIPublicCatalogItem) bool {
	identity := ZTAPIModelPublicationSnapshot{ModelConfigID: publication.ModelConfigID, SourceModel: publication.SourceModel,
		PublicName: publication.PublicName, ProviderFamily: publication.ProviderFamily, Protocol: publication.Protocol,
		ImageSaleContractJSON: publication.ImageSaleContractJSON}
	if publication.Modality != ZTAPIModalityImage || validateZTAPIImageSaleSnapshot(&identity) != nil {
		return false
	}
	contract, err := types.ParseZTAPIImageSaleContract(publication.ImageSaleContractJSON)
	if err != nil {
		return false
	}
	item.ImageSale = &contract
	item.BillingUnit, item.BillingRule = contract.BillingUnit, "per_image"
	item.BillingDimensions = []string{"images"}
	item.SaleUSD = map[string]string{"images": contract.BasePrices["1K"]}
	item.InputPricePerMillion, item.OutputPricePerMillion = "", ""
	item.TokenPriceRules, item.OfficialUSD, item.OfficialReference = nil, nil, nil
	item.PricingRules = make([]ZTAPIPublicPricingRule, 0, 3)
	for _, resolution := range []string{"1K", "2K", "4K"} {
		if contract.BasePrices[resolution] == "" {
			continue
		}
		item.PricingRules = append(item.PricingRules, ZTAPIPublicPricingRule{ID: "image_" + resolution,
			Conditions: map[string]string{"resolution": resolution}, BillingUnit: contract.BillingUnit,
			SaleUSD: map[string]string{"images": contract.BasePrices[resolution]}})
	}
	if contract.Version == 2 {
		item.PricingRules = nil
		for _, allowed := range contract.AllowedRequests {
			item.PricingRules = append(item.PricingRules, ZTAPIPublicPricingRule{
				ID:          "image_" + allowed.Operation + "_" + allowed.Resolution,
				Conditions:  map[string]string{"image_operation": allowed.Operation, "size": allowed.Size, "quality": allowed.Quality, "resolution": allowed.Resolution},
				BillingUnit: contract.BillingUnit, SaleUSD: map[string]string{"images": contract.BasePrices[allowed.Resolution]},
			})
		}
		if item.SupportedOptions == nil {
			item.SupportedOptions = &ZTAPIPublicSupportedOptions{}
		}
		options := *item.SupportedOptions
		options.AllowedRequests = append([]types.ZTAPIImageSaleAllowedRequest(nil), contract.AllowedRequests...)
		options.Sizes, options.Qualities, options.Resolutions = nil, nil, nil
		options.RenderOptions = nil
		seenSize, seenQuality := map[string]bool{}, map[string]bool{}
		for _, allowed := range contract.AllowedRequests {
			if !seenSize[allowed.Size] {
				options.Sizes = append(options.Sizes, allowed.Size)
				seenSize[allowed.Size] = true
			}
			if !seenQuality[allowed.Quality] {
				options.Qualities = append(options.Qualities, allowed.Quality)
				seenQuality[allowed.Quality] = true
			}
		}
		for _, option := range item.SupportedOptions.RenderOptions {
			for _, allowed := range contract.AllowedRequests {
				if option.Size == allowed.Size && option.Quality == allowed.Quality && option.Resolution == allowed.Resolution {
					options.RenderOptions = append(options.RenderOptions, option)
					break
				}
			}
		}
		options.MinCount, options.MaxCount = 1, int(contract.MaxImages)
		if options.EditInput != nil {
			edit := *options.EditInput
			edit.MaxFiles = int(contract.MaxReferenceImages)
			options.EditInput = &edit
		}
		item.SupportedOptions = &options
	}
	return true
}
