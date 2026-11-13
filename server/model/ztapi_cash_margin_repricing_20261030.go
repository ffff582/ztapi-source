package model

import (
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type ZTAPICashMarginSelection20261030 struct {
	ModelID           int    `json:"model_id"`
	Version           uint64 `json:"version"`
	SnapshotID        int64  `json:"snapshot_id"`
	RestoreSnapshotID int64  `json:"restore_snapshot_id,omitempty"`
}

type ZTAPICashMarginChange20261030 struct {
	ZTAPICashMarginSelection20261030
	PublicName              string                      `json:"public_name"`
	BaseSourceID            int64                       `json:"base_source_id"`
	ProcurementResourceType string                      `json:"procurement_resource_type"`
	AllowedChannels         []int                       `json:"allowed_channels"`
	TargetMargin            string                      `json:"target_margin,omitempty"`
	RestoringOriginalPrices bool                        `json:"restoring_original_prices"`
	BonusRate               string                      `json:"bonus_rate"`
	CostEvidence            string                      `json:"cost_evidence"`
	Unchanged               bool                        `json:"unchanged"`
	Before                  *ZTAPIModelPricePreview     `json:"before"`
	After                   *ZTAPIModelPricePreview     `json:"after"`
	TokenPriceRules         []ZTAPIPublicTokenPriceRule `json:"token_price_rules"`
	CoveredCostUSD          map[string]string           `json:"covered_cost_usd,omitempty"`
	SelectedXcloudGroup     string                      `json:"selected_xcloud_group,omitempty"`
	XcloudCashFactor        string                      `json:"xcloud_cash_factor,omitempty"`
}

type ztapiCashMarginCandidate struct {
	change   ZTAPICashMarginChange20261030
	config   ZTAPIModelConfig
	previous ZTAPIModelPublicationSnapshot
	template ZTAPIModelPublicationSnapshot
	target   ZTAPIModelPriceSource
}

type ztapiCashMarginAudit struct {
	PreviousSnapshotID int64  `json:"previous_snapshot_id"`
	SnapshotID         int64  `json:"snapshot_id"`
	RestoreSnapshotID  int64  `json:"restore_snapshot_id"`
	BaseSourceID       int64  `json:"base_source_id"`
	RequestVersion     uint64 `json:"request_version"`
}

func ztapiCashMarginAction(rollback bool) string {
	if rollback {
		return "model.cash_margin_20261030_restored"
	}
	return "model.cash_margin_20261030_published"
}

func ztapiCashMarginSelections(selected []ZTAPICashMarginSelection20261030, rollback bool) ([]ZTAPICashMarginSelection20261030, error) {
	if len(selected) == 0 || len(selected) > len(ztapiCashMarginSources20261030) {
		return nil, errors.New("select only the approved cash margin models")
	}
	items := append([]ZTAPICashMarginSelection20261030(nil), selected...)
	sort.Slice(items, func(i, j int) bool { return items[i].ModelID < items[j].ModelID })
	for i, item := range items {
		if item.ModelID <= 0 || item.Version == 0 || item.SnapshotID <= 0 || (i > 0 && item.ModelID == items[i-1].ModelID) ||
			(rollback && item.RestoreSnapshotID <= 0) || (!rollback && item.RestoreSnapshotID != 0) {
			return nil, errors.New("invalid cash margin approval baseline")
		}
	}
	return items, nil
}

func ztapiCashMarginCandidateTx(tx *gorm.DB, selection ZTAPICashMarginSelection20261030, operator int, rollback bool) (ztapiCashMarginCandidate, error) {
	var result ztapiCashMarginCandidate
	config := &result.config
	if err := tx.Where("id = ? AND published = ?", selection.ModelID, true).First(config).Error; err != nil {
		return result, err
	}
	contract, err := ztapiCashContractFor20261030(config.SourceModel)
	if err != nil || config.PublicNameValue() != contract.PublicName {
		return result, errors.New("model is outside the approved cash margin catalog")
	}
	previous := &result.previous
	if err := tx.First(previous, config.PublicationSnapshotID).Error; err != nil {
		return result, err
	}
	channelsMatch := reflect.DeepEqual(uniqueSortedInts(previous.ChannelIDs()), contract.channelIDs()) ||
		(config.SourceModel == "gemini-3.1-pro-preview" && reflect.DeepEqual(uniqueSortedInts(previous.ChannelIDs()), []int{1}))
	if previous.ModelConfigID != config.ID || previous.ModelVersion != config.Version || previous.PublicName != config.PublicNameValue() ||
		previous.SourceModel != config.SourceModel || previous.Protocol != config.Protocol || previous.ProviderFamily != config.ProviderFamily ||
		previous.EnabledGroups != config.EnabledGroups || !channelsMatch {
		return result, errors.New("active cash margin snapshot identity or route mismatch")
	}
	var current ZTAPIModelPriceSource
	if err := tx.First(&current, previous.PriceSourceID).Error; err != nil {
		return result, err
	}
	resourceMatches := current.ResourceType == contract.Resource || (contract.Image && current.ResourceType == "pool" && current.PricePolicy == string(contract.BasePolicy))
	if current.ModelConfigID != config.ID || current.SourceModel != config.SourceModel || !resourceMatches ||
		previous.PricePolicy != current.PricePolicy || previous.TokenPriceRulesJSON != current.TokenPriceRulesJSON ||
		!ztapiSameDecimal(previous.SaleMultiplier, current.SaleMultiplier) {
		return result, errors.New("active procurement binding mismatch")
	}
	if err := ValidateZTAPIQuotationIdentity(config.SourceModel, config.PublicNameValue(), config.Protocol, config.ProviderFamily, current.SourceDocumentChecksum); err != nil {
		return result, err
	}
	if isZTAPICashMarginPolicy20261030(current.PricePolicy) {
		if err := ValidateZTAPICashMarginSource20261030(tx, &current); err != nil {
			return result, err
		}
		if err := validateZTAPICashMarginRoute20261030(&current, previous.PublicName, previous.ChannelIDs()); err != nil {
			return result, err
		}
	} else if err := validateZTAPICashMarginBase20261030(tx, &current); err != nil {
		return result, err
	}
	if config.SourceModel == "claude-fable-5" {
		expectedMode := ZTAPIFableUniformCacheWriteMode
		if isZTAPICashMarginPolicy20261030(current.PricePolicy) {
			expectedMode = ZTAPIFableCashFloorCacheWriteMode
		}
		if previous.CacheWritePricingMode != expectedMode {
			return result, errors.New("Fable cache mode differs from its approved price version")
		}
	}
	before, err := BuildZTAPIModelPricePreview(&current)
	if err != nil {
		return result, err
	}
	if !ztapiStoredPriceMatchesPreview(previous.InputPricePerMillion, before.InputSaleUSDPerMillion) ||
		!ztapiStoredPriceMatchesPreview(previous.OutputPricePerMillion, before.OutputSaleUSDPerMillion) ||
		!ztapiCacheRatiosMatchPreview(config, before) {
		return result, errors.New("active prices differ from bound evidence")
	}
	unchanged := !rollback && isZTAPICashMarginPolicy20261030(current.PricePolicy)
	baseID := current.CashMarginBaseSourceID
	if baseID == 0 {
		baseID = current.ID
	}
	if rollback && !isZTAPICashMarginPolicy20261030(current.PricePolicy) {
		var restoredFrom ZTAPIModelPublicationSnapshot
		if err := tx.First(&restoredFrom, selection.RestoreSnapshotID).Error; err != nil {
			return result, err
		}
		var originalSource ZTAPIModelPriceSource
		if err := tx.First(&originalSource, restoredFrom.PriceSourceID).Error; err != nil {
			return result, err
		}
		if restoredFrom.ModelConfigID != config.ID || originalSource.ModelConfigID != config.ID ||
			ztapiABPriceSourceEvidenceMismatch(&current, &originalSource) != "" {
			return result, errors.New("restored source differs from the original rollback evidence")
		}
		baseID = originalSource.ID
	}
	if config.Version != selection.Version || previous.ID != selection.SnapshotID {
		var event ZTAPIAuditEvent
		var audit ztapiCashMarginAudit
		err := tx.Where("model_config_id = ? AND version = ? AND action = ?", config.ID, config.Version, ztapiCashMarginAction(rollback)).Order("id DESC").First(&event).Error
		if err != nil || common.Unmarshal([]byte(event.Payload), &audit) != nil || config.Version != selection.Version+1 ||
			audit.RequestVersion != selection.Version || audit.PreviousSnapshotID != selection.SnapshotID || audit.SnapshotID != previous.ID ||
			audit.RestoreSnapshotID != selection.RestoreSnapshotID || audit.BaseSourceID != baseID || (!rollback && !unchanged) || (rollback && isZTAPICashMarginPolicy20261030(current.PricePolicy)) {
			return result, ErrZTAPIModelVersionConflict
		}
		unchanged = true
	}
	result.target, result.template = current, *previous
	if !unchanged && !rollback {
		result.target, err = BuildZTAPICashMarginSource20261030(tx, &current, operator)
		if err != nil {
			return result, err
		}
		if config.SourceModel == "claude-fable-5" {
			result.template.CacheWritePricingMode = ZTAPIFableCashFloorCacheWriteMode
		}
	}
	if rollback && !unchanged {
		if !isZTAPICashMarginPolicy20261030(current.PricePolicy) {
			return result, errors.New("rollback requires an active derived cash margin policy")
		}
		var original ZTAPIModelPublicationSnapshot
		if err := tx.First(&original, selection.RestoreSnapshotID).Error; err != nil {
			return result, err
		}
		if original.ID >= previous.ID || original.ModelConfigID != config.ID || original.PriceSourceID != baseID ||
			original.SourceModel != previous.SourceModel || original.PublicName != previous.PublicName || original.Protocol != previous.Protocol ||
			original.ProviderFamily != previous.ProviderFamily || original.EnabledGroups != previous.EnabledGroups ||
			!reflect.DeepEqual(original.ChannelIDs(), previous.ChannelIDs()) || original.VerificationIDs != previous.VerificationIDs ||
			original.PricePolicy != string(contract.BasePolicy) {
			return result, errors.New("rollback snapshot is not the unchanged original procurement publication")
		}
		result.target = ZTAPIModelPriceSource{}
		if err := tx.First(&result.target, baseID).Error; err != nil {
			return result, err
		}
		if err := validateZTAPICashMarginBase20261030(tx, &result.target); err != nil {
			return result, err
		}
		oldPreview, err := BuildZTAPIModelPricePreview(&result.target)
		if err != nil || original.TokenPriceRulesJSON != result.target.TokenPriceRulesJSON || !ztapiSameDecimal(original.SaleMultiplier, result.target.SaleMultiplier) ||
			!ztapiStoredPriceMatchesPreview(original.InputPricePerMillion, oldPreview.InputSaleUSDPerMillion) ||
			!ztapiStoredPriceMatchesPreview(original.OutputPricePerMillion, oldPreview.OutputSaleUSDPerMillion) {
			return result, errors.New("rollback historical price evidence mismatch")
		}
		result.template = original
	}
	after, err := BuildZTAPIModelPricePreview(&result.target)
	if err != nil {
		return result, err
	}
	var rules []ZTAPIPublicTokenPriceRule
	if result.target.TokenPriceRulesJSON != "" {
		rules, err = ztapiPublicTokenPriceRules(result.target.TokenPriceRulesJSON, after.BillingDimensions)
		if err != nil {
			return result, err
		}
	}
	result.change = ZTAPICashMarginChange20261030{ZTAPICashMarginSelection20261030: selection, PublicName: config.PublicNameValue(),
		BaseSourceID: baseID, ProcurementResourceType: result.target.ResourceType, AllowedChannels: previous.ChannelIDs(), TargetMargin: contract.Margin,
		BonusRate: "0.05", CostEvidence: "frozen_procurement_basis_not_verified_actual_supplier_cost", Unchanged: unchanged,
		Before: before, After: after, TokenPriceRules: rules}
	if contract.Extended {
		var costBase ZTAPIModelPriceSource
		if err := tx.First(&costBase, baseID).Error; err != nil {
			return result, err
		}
		if contract.Image {
			media, parseErr := parseZTAPIMediaPriceContract(result.target.MediaPriceContractJSON)
			if parseErr != nil {
				return result, parseErr
			}
			result.change.CoveredCostUSD = map[string]string{}
			for _, rule := range media.Rules {
				for dimension, value := range rule.CostUSD {
					result.change.CoveredCostUSD[dimension] = value
				}
			}
			result.change.CostEvidence = "frozen_D100_enterprise_quotation_and_frozen_fx_not_supplier_invoice"
		} else {
			result.change.CoveredCostUSD, err = ztapiCashCoveredCostUSD20261030(&costBase)
			if err != nil {
				return result, err
			}
		}
		if _, mixed := ztapiCashXcloudGroupQuotes20261030[config.SourceModel]; mixed {
			result.change.SelectedXcloudGroup, result.change.XcloudCashFactor = "claude-max", "0.5"
			result.change.CostEvidence = "frozen_procurement_and_selected_xcloud_group_quotes_not_supplier_invoices"
		}
	}
	if rollback {
		result.change.TargetMargin, result.change.RestoringOriginalPrices = "", true
	}
	return result, nil
}

func PreviewZTAPICashMargin20261030(selected []ZTAPICashMarginSelection20261030, rollback bool) ([]ZTAPICashMarginChange20261030, error) {
	items, err := ztapiCashMarginSelections(selected, rollback)
	if err != nil || DB == nil {
		return nil, errors.New("cash margin preview requires a database and valid selections")
	}
	result := make([]ZTAPICashMarginChange20261030, 0, len(items))
	err = DB.Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			candidate, err := ztapiCashMarginCandidateTx(tx, item, 1, rollback)
			if err != nil {
				return err
			}
			result = append(result, candidate.change)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ApplyZTAPICashMargin20261030(operator int, selected []ZTAPICashMarginSelection20261030, rollback bool) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{Models: []string{}}
	items, err := ztapiCashMarginSelections(selected, rollback)
	if err != nil || DB == nil || operator <= 0 {
		return result, errors.New("cash margin apply requires an operator, database and valid selections")
	}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		for _, item := range items {
			candidate, err := ztapiCashMarginCandidateTx(tx, item, operator, rollback)
			if err != nil {
				return fmt.Errorf("model %d: %w", item.ModelID, err)
			}
			if candidate.change.Unchanged {
				result.Unchanged++
				continue
			}
			target, config, template := candidate.target, candidate.config, candidate.template
			now := common.GetTimestamp()
			var latest uint64
			if err := tx.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).Select("COALESCE(MAX(version), 0)").Scan(&latest).Error; err != nil {
				return err
			}
			if !rollback {
				if err := ValidateZTAPICashMarginSource20261030(tx, &target); err != nil {
					return err
				}
				if err := applyZTAPICommercialPreview(&config, candidate.change.After); err != nil {
					return err
				}
			} else {
				if err := validateZTAPICashMarginBase20261030(tx, &target); err != nil {
					return err
				}
				if err := applyZTAPICommercialPreview(&config, candidate.change.After); err != nil {
					return err
				}
				config.InputPricePerMillion, config.OutputPricePerMillion = template.InputPricePerMillion, template.OutputPricePerMillion
				config.CacheReadRatio, config.CacheCreationRatio = template.CacheReadRatio, template.CacheCreationRatio
				config.CacheCreation5mRatio, config.CacheCreation1hRatio = template.CacheCreation5mRatio, template.CacheCreation1hRatio
				config.ImageRatio, config.AudioRatio, config.AudioCompletionRatio = template.ImageRatio, template.AudioRatio, template.AudioCompletionRatio
			}
			target.ID, target.Version, target.CreatedAt, target.OperatorID = 0, latest+1, now, operator
			if err := tx.Create(&target).Error; err != nil {
				return err
			}
			result.Imported++
			var verifications []int64
			if err := common.Unmarshal([]byte(template.VerificationIDs), &verifications); err != nil || len(verifications) == 0 {
				return errors.New("missing frozen verification IDs")
			}
			nextVersion := config.Version + 1
			snapshot, err := createZTAPIModelPublicationSnapshotTx(tx, &config, nextVersion, ztapiPublicationEvidence{
				PriceSourceID: target.ID, AllowedChannelIDs: template.ChannelIDs(), VerificationIDs: verifications, IdentityUpdatedAt: template.IdentityUpdatedAt,
				CacheWritePricingMode: template.CacheWritePricingMode, ImageProtocolContractJSON: template.ImageProtocolContractJSON, VideoProtocolContractJSON: template.VideoProtocolContractJSON})
			if err != nil {
				return err
			}
			updates := map[string]any{"input_price_per_million": config.InputPricePerMillion, "output_price_per_million": config.OutputPricePerMillion,
				"input_cost_per_million": config.InputCostPerMillion, "output_cost_per_million": config.OutputCostPerMillion,
				"cache_read_ratio": config.CacheReadRatio, "cache_creation_ratio": config.CacheCreationRatio,
				"cache_creation_5m_ratio": config.CacheCreation5mRatio, "cache_creation_1h_ratio": config.CacheCreation1hRatio,
				"image_ratio": config.ImageRatio, "audio_ratio": config.AudioRatio, "audio_completion_ratio": config.AudioCompletionRatio,
				"publication_snapshot_id": snapshot.ID, "version": nextVersion, "updated_at": now}
			updated := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ? AND publication_snapshot_id = ?", config.ID, item.Version, item.SnapshotID).Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrZTAPIModelVersionConflict
			}
			payload, err := common.Marshal(ztapiCashMarginAudit{PreviousSnapshotID: item.SnapshotID, SnapshotID: snapshot.ID,
				RestoreSnapshotID: item.RestoreSnapshotID, BaseSourceID: candidate.change.BaseSourceID, RequestVersion: item.Version})
			if err != nil {
				return err
			}
			if err := tx.Create(&ZTAPIAuditEvent{Action: ztapiCashMarginAction(rollback), ModelConfigID: config.ID,
				PublicName: config.PublicNameValue(), Version: nextVersion, OperatorID: operator, Payload: string(payload), CreatedAt: now}).Error; err != nil {
				return err
			}
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
