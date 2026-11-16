package model

import (
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"sort"
)

func ztapiReferralFloorModel20261107(name string) bool {
	switch name {
	case "deepseek-v4-pro", "glm-5", "glm-5.1", "kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code", "doubao-seedance-2-5":
		return true
	}
	return false
}

type ztapiReferralFloorAudit20261107 struct {
	ztapiReductionAudit
	CommissionRate  string `json:"commission_rate"`
	CampaignEnabled bool   `json:"campaign_enabled"`
}

func ztapiReferralFloorAction20261107(rollback bool) string {
	if rollback {
		return "model.referral_floor_20261107_restored"
	}
	return "model.referral_floor_20261107_published"
}

func ztapiReferralFloorSelections20261107(selected []ZTAPIPriceReductionSelection20261107, rollback bool) ([]ZTAPIPriceReductionSelection20261107, error) {
	if len(selected) != 7 {
		return nil, errors.New("select all seven approved referral-floor models")
	}
	items := append([]ZTAPIPriceReductionSelection20261107(nil), selected...)
	sort.Slice(items, func(i, j int) bool { return items[i].ModelID < items[j].ModelID })
	for i, s := range items {
		b, _, _, _, err := ztapiReductionApproval20261107(s.ModelID)
		if err != nil || !ztapiReferralFloorModel20261107(b.SourceModel) || s.Version == 0 || s.SnapshotID <= 0 || s.SourceID <= 0 || (i > 0 && s.ModelID == items[i-1].ModelID) || (!rollback && s.RestoreSnapshotID != 0) || (rollback && s.RestoreSnapshotID <= 0) {
			return nil, errors.New("invalid approved referral-floor selection")
		}
	}
	return items, nil
}

func ztapiReferralFloorCandidateTx20261107(tx *gorm.DB, s ZTAPIPriceReductionSelection20261107, operator int, rollback bool) (ztapiReductionCandidate, error) {
	var c ztapiReductionCandidate
	b, _, _, rows, err := ztapiReductionApproval20261107(s.ModelID)
	if err != nil {
		return c, err
	}
	if err = tx.Where("id = ? AND published = ?", s.ModelID, true).First(&c.config).Error; err != nil {
		return c, err
	}
	if err = tx.First(&c.previous, c.config.PublicationSnapshotID).Error; err != nil {
		return c, err
	}
	var current ZTAPIModelPriceSource
	if err = tx.First(&current, c.previous.PriceSourceID).Error; err != nil {
		return c, err
	}
	if err = ValidateZTAPIPriceReductionSource20261107(tx, &current); err != nil {
		return c, err
	}
	before, err := BuildZTAPIModelPricePreview(&current)
	if err != nil {
		return c, err
	}
	p, config := &c.previous, &c.config
	if config.SourceModel != b.SourceModel || config.PublicNameValue() != b.PublicName || p.ModelConfigID != config.ID || p.ModelVersion != config.Version || p.SourceModel != config.SourceModel || p.PublicName != config.PublicNameValue() || p.Protocol != config.Protocol || p.ProviderFamily != config.ProviderFamily || p.EnabledGroups != config.EnabledGroups || p.AllowedChannelIDs != b.Channels ||
		p.PricePolicy != current.PricePolicy || p.TokenPriceRulesJSON != current.TokenPriceRulesJSON || p.MediaPriceContractJSON != current.MediaPriceContractJSON || !ztapiSameDecimal(p.SaleMultiplier, current.SaleMultiplier) ||
		config.InputPricePerMillion != p.InputPricePerMillion || config.OutputPricePerMillion != p.OutputPricePerMillion || !ztapiStoredPriceMatchesPreview(p.InputPricePerMillion, before.InputSaleUSDPerMillion) || !ztapiStoredPriceMatchesPreview(p.OutputPricePerMillion, before.OutputSaleUSDPerMillion) || !ztapiCacheRatiosMatchPreview(config, before) {
		return c, errors.New("referral-floor active publication drift")
	}
	unchanged := false
	if config.Version != s.Version || p.ID != s.SnapshotID || current.ID != s.SourceID {
		var event ZTAPIAuditEvent
		var audit ztapiReferralFloorAudit20261107
		if config.Version != s.Version+1 || tx.Where("model_config_id = ? AND version = ? AND action = ?", config.ID, config.Version, ztapiReferralFloorAction20261107(rollback)).Order("id DESC").First(&event).Error != nil || common.UnmarshalJsonStr(event.Payload, &audit) != nil || audit.Selection != s || audit.SnapshotID != p.ID || audit.SourceID != current.ID || audit.CommissionRate != "0.05" || audit.CampaignEnabled {
			return c, ErrZTAPIModelVersionConflict
		}
		unchanged = true
	}
	c.template, c.target = *p, current
	c.change = ZTAPIPriceReductionChange20261107{ZTAPIPriceReductionSelection20261107: s, PublicName: b.PublicName, Policy: string(ZTAPIPricePolicyReferralFloor20261107), BonusRate: "0.05", TargetMargin: "0.15", AllowedChannels: p.ChannelIDs(), VerificationIDs: p.VerificationIDs, CostEvidence: "approved_native_quotes_bonus5_commission5_post_referral10_not_actual_supplier_debits", Before: before, Unchanged: unchanged, Rollback: rollback}
	if !rollback {
		if unchanged {
			if current.PricePolicy != string(ZTAPIPricePolicyReferralFloor20261107) {
				return c, errors.New("replayed referral price policy drift")
			}
		} else {
			if current.PricePolicy != string(ZTAPIPricePolicyReduction20261107) {
				return c, errors.New("referral floor requires current approved reduction publication")
			}
			// Reuse the previous controlled publisher's complete identity and route validation.
			old, err := ztapiReductionCandidateTx20261107(tx, ZTAPIPriceReductionSelection20261107{ModelID: b.ID, Version: b.Version, SnapshotID: b.SnapshotID, SourceID: b.SourceID}, operator, false)
			if err != nil || !old.change.Unchanged || old.previous.ID != p.ID {
				return c, errors.New("current reduction is not the audited approved baseline")
			}
			var base ZTAPIModelPriceSource
			if err = tx.First(&base, b.SourceID).Error; err != nil {
				return c, err
			}
			c.target, err = deriveZTAPIDatedRetail20261107(&base, operator, string(ZTAPIPricePolicyReferralFloor20261107))
			if err != nil {
				return c, err
			}
		}
		for _, row := range rows {
			cost, err := ztapiReductionRowCost20261107(row)
			if err != nil {
				return c, err
			}
			sale, err := ztapiReductionSale20261107(cost, "0.15")
			if err != nil {
				return c, err
			}
			row.OldSale, row.ProposedSale, row.Margin = row.ProposedSale, sale, "0.15"
			c.change.Dimensions = append(c.change.Dimensions, row)
		}
	} else {
		var applied ZTAPIAuditEvent
		var audit ztapiReferralFloorAudit20261107
		if tx.Where("model_config_id = ? AND version = ? AND action = ?", config.ID, s.Version, ztapiReferralFloorAction20261107(false)).First(&applied).Error != nil || common.UnmarshalJsonStr(applied.Payload, &audit) != nil || audit.SnapshotID != s.SnapshotID || audit.SourceID != s.SourceID || audit.Selection.SnapshotID != s.RestoreSnapshotID {
			return c, errors.New("restore requires original audited referral-floor publication")
		}
		var original ZTAPIModelPublicationSnapshot
		var source ZTAPIModelPriceSource
		if err = tx.First(&original, s.RestoreSnapshotID).Error; err != nil {
			return c, err
		}
		if err = tx.First(&source, original.PriceSourceID).Error; err != nil {
			return c, err
		}
		if original.ModelConfigID != config.ID || source.PricePolicy != string(ZTAPIPricePolicyReduction20261107) || original.ID != audit.Selection.SnapshotID || source.ID != audit.Selection.SourceID {
			return c, errors.New("restore baseline identity mismatch")
		}
		if !unchanged && current.PricePolicy != string(ZTAPIPricePolicyReferralFloor20261107) {
			return c, errors.New("active referral floor required before restore")
		}
		c.target, c.template = source, original
	}
	c.change.After, err = BuildZTAPIModelPricePreview(&c.target)
	return c, err
}

func PreviewZTAPIReferralFloor20261107(selected []ZTAPIPriceReductionSelection20261107, rollback bool) ([]ZTAPIPriceReductionChange20261107, error) {
	items, err := ztapiReferralFloorSelections20261107(selected, rollback)
	if err != nil {
		return nil, err
	}
	if DB == nil {
		return nil, errors.New("database unavailable")
	}
	result := []ZTAPIPriceReductionChange20261107{}
	err = DB.Transaction(func(tx *gorm.DB) error {
		for _, s := range items {
			c, err := ztapiReferralFloorCandidateTx20261107(tx, s, 1, rollback)
			if err != nil {
				return fmt.Errorf("model %d: %w", s.ModelID, err)
			}
			result = append(result, c.change)
		}
		return nil
	})
	return result, err
}

func ApplyZTAPIReferralFloor20261107(operator int, selected []ZTAPIPriceReductionSelection20261107, rollback bool) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{Models: []string{}}
	items, err := ztapiReferralFloorSelections20261107(selected, rollback)
	if err != nil {
		return result, err
	}
	if DB == nil || operator <= 0 {
		return result, errors.New("database and operator required")
	}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		for _, s := range items {
			c, err := ztapiReferralFloorCandidateTx20261107(tx, s, operator, rollback)
			if err != nil {
				return err
			}
			if c.change.Unchanged {
				result.Unchanged++
				continue
			}
			target, config, template := c.target, c.config, c.template
			if err = ValidateZTAPIPriceReductionSource20261107(tx, &target); err != nil {
				return err
			}
			if err = applyZTAPICommercialPreview(&config, c.change.After); err != nil {
				return err
			}
			var latest uint64
			if err = tx.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).Select("COALESCE(MAX(version),0)").Scan(&latest).Error; err != nil {
				return err
			}
			now := common.GetTimestamp()
			target.ID, target.Version, target.CreatedAt, target.OperatorID = 0, latest+1, now, operator
			if err = tx.Create(&target).Error; err != nil {
				return err
			}
			var verificationIDs []int64
			if err = common.UnmarshalJsonStr(template.VerificationIDs, &verificationIDs); err != nil {
				return err
			}
			snapshot, err := createZTAPIModelPublicationSnapshotTx(tx, &config, config.Version+1, ztapiPublicationEvidence{PriceSourceID: target.ID, AllowedChannelIDs: template.ChannelIDs(), VerificationIDs: verificationIDs, IdentityUpdatedAt: template.IdentityUpdatedAt, CacheWritePricingMode: template.CacheWritePricingMode, ImageProtocolContractJSON: template.ImageProtocolContractJSON, VideoProtocolContractJSON: template.VideoProtocolContractJSON})
			if err != nil {
				return err
			}
			updates := map[string]any{"version": config.Version + 1, "publication_snapshot_id": snapshot.ID, "updated_at": now, "input_price_per_million": config.InputPricePerMillion, "output_price_per_million": config.OutputPricePerMillion, "input_cost_per_million": config.InputCostPerMillion, "output_cost_per_million": config.OutputCostPerMillion, "cache_read_ratio": config.CacheReadRatio, "cache_creation_ratio": config.CacheCreationRatio, "cache_creation_5m_ratio": config.CacheCreation5mRatio, "cache_creation_1h_ratio": config.CacheCreation1hRatio, "image_ratio": config.ImageRatio, "audio_ratio": config.AudioRatio, "audio_completion_ratio": config.AudioCompletionRatio}
			update := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ? AND publication_snapshot_id = ?", config.ID, s.Version, s.SnapshotID).Updates(updates)
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return ErrZTAPIModelVersionConflict
			}
			payload, err := common.Marshal(ztapiReferralFloorAudit20261107{ztapiReductionAudit: ztapiReductionAudit{Selection: s, SnapshotID: snapshot.ID, SourceID: target.ID, Policy: target.PricePolicy, Bonus: "0.05", Dimensions: c.change.Dimensions}, CommissionRate: "0.05", CampaignEnabled: false})
			if err != nil {
				return err
			}
			if err = tx.Create(&ZTAPIAuditEvent{Action: ztapiReferralFloorAction20261107(rollback), ModelConfigID: config.ID, PublicName: config.PublicNameValue(), Version: config.Version + 1, OperatorID: operator, Payload: string(payload), CreatedAt: now}).Error; err != nil {
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

func CheckZTAPIReferralFloorDowngrade20261107() (ZTAPICashMarginDowngrade20261030, error) {
	names := []string{}
	for _, id := range ztapiReductionSortedIDs20261107() {
		b, _, _, _, err := ztapiReductionApproval20261107(id)
		if err == nil && ztapiReferralFloorModel20261107(b.SourceModel) {
			names = append(names, b.PublicName)
		}
	}
	return checkZTAPIPolicyDowngrade20261107([]string{string(ZTAPIPricePolicyReferralFloor20261107)}, names)
}
