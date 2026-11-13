package model

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ZTAPIPriceReductionSelection20261107 struct {
	ModelID           int    `json:"model_id"`
	Version           uint64 `json:"version"`
	SnapshotID        int64  `json:"snapshot_id"`
	SourceID          int64  `json:"source_id"`
	RestoreSnapshotID int64  `json:"restore_snapshot_id,omitempty"`
}

type ZTAPIPriceReductionChange20261107 struct {
	ZTAPIPriceReductionSelection20261107
	PublicName      string                                 `json:"public_name"`
	Policy          string                                 `json:"policy"`
	BonusRate       string                                 `json:"bonus_rate"`
	TargetMargin    string                                 `json:"target_margin"`
	AllowedChannels []int                                  `json:"allowed_channels"`
	VerificationIDs string                                 `json:"verification_ids"`
	CostEvidence    string                                 `json:"cost_evidence"`
	Dimensions      []ZTAPIPriceReductionDimension20261107 `json:"dimensions"`
	Before          *ZTAPIModelPricePreview                `json:"before"`
	After           *ZTAPIModelPricePreview                `json:"after,omitempty"`
	Unchanged       bool                                   `json:"unchanged"`
	Skipped         bool                                   `json:"skipped"`
	SkipReason      string                                 `json:"skip_reason,omitempty"`
	Rollback        bool                                   `json:"rollback"`
}

type ztapiReductionCandidate struct {
	change   ZTAPIPriceReductionChange20261107
	config   ZTAPIModelConfig
	previous ZTAPIModelPublicationSnapshot
	template ZTAPIModelPublicationSnapshot
	target   ZTAPIModelPriceSource
}

type ztapiReductionAudit struct {
	Selection  ZTAPIPriceReductionSelection20261107   `json:"selection"`
	SnapshotID int64                                  `json:"snapshot_id"`
	SourceID   int64                                  `json:"source_id"`
	Policy     string                                 `json:"policy"`
	Bonus      string                                 `json:"bonus"`
	Dimensions []ZTAPIPriceReductionDimension20261107 `json:"dimensions"`
}

func ztapiReductionAction20261107(rollback bool) string {
	if rollback {
		return "model.price_reduction_20261107_restored"
	}
	return "model.price_reduction_20261107_published"
}

// ApprovedZTAPIPriceReductionSelections20261107 returns the frozen 16-model
// approval. Flash is present for explicit skip reporting, never for repricing.
func ApprovedZTAPIPriceReductionSelections20261107() []ZTAPIPriceReductionSelection20261107 {
	result := []ZTAPIPriceReductionSelection20261107{}
	for _, id := range ztapiReductionSortedIDs20261107() {
		b, _, _, _, err := ztapiReductionApproval20261107(id)
		if err == nil {
			result = append(result, ZTAPIPriceReductionSelection20261107{ModelID: id, Version: b.Version, SnapshotID: b.SnapshotID, SourceID: b.SourceID})
		}
	}
	return result
}

func ztapiReductionSelections20261107(selected []ZTAPIPriceReductionSelection20261107, rollback bool) ([]ZTAPIPriceReductionSelection20261107, error) {
	if len(selected) == 0 || len(selected) > 16 {
		return nil, errors.New("select 1 to 16 approved reduction baselines")
	}
	items := append([]ZTAPIPriceReductionSelection20261107(nil), selected...)
	sort.Slice(items, func(i, j int) bool { return items[i].ModelID < items[j].ModelID })
	for i, s := range items {
		b, _, _, _, err := ztapiReductionApproval20261107(s.ModelID)
		if err != nil || s.Version == 0 || s.SnapshotID <= 0 || s.SourceID <= 0 || (i > 0 && s.ModelID == items[i-1].ModelID) {
			return nil, errors.New("invalid or duplicate reduction baseline")
		}
		if !rollback && (s.Version != b.Version || s.SnapshotID != b.SnapshotID || s.SourceID != b.SourceID || s.RestoreSnapshotID != 0) {
			return nil, ErrZTAPIModelVersionConflict
		}
		if rollback && (s.ModelID == 5 || s.Version != b.Version+1 || s.RestoreSnapshotID != b.SnapshotID) {
			return nil, errors.New("rollback requires this operation's applied baseline and original snapshot")
		}
	}
	return items, nil
}

func ztapiReductionOriginalSnapshot20261107(p *ZTAPIModelPublicationSnapshot, b ztapiReductionBaseline, m ztapiReductionModel, base *ZTAPIModelPriceSource) error {
	if p.ID != b.SnapshotID || p.ModelConfigID != b.ID || p.ModelVersion != b.Version || p.PriceSourceID != b.SourceID || p.SourceModel != b.SourceModel ||
		p.PublicName != b.PublicName || p.ProviderFamily != m.Family || p.Protocol != m.Protocol || p.AllowedChannelIDs != b.Channels ||
		p.PricePolicy != b.PricePolicy || p.TokenPriceRulesJSON != m.Rules || p.MediaPriceContractJSON != m.Media || p.CacheWritePricingMode != m.CacheMode ||
		!ztapiSameDecimal(p.SaleMultiplier, "1") || !decimal.NewFromFloat(p.InputPricePerMillion).Equal(b.Input) || !decimal.NewFromFloat(p.OutputPricePerMillion).Equal(b.Output) ||
		p.CacheReadRatio != m.ReadRatio || p.CacheCreationRatio != m.WriteRatio || p.CacheCreation5mRatio != m.Write5mRatio || p.CacheCreation1hRatio != m.Write1hRatio ||
		p.TokenPriceRulesJSON != base.TokenPriceRulesJSON || p.MediaPriceContractJSON != base.MediaPriceContractJSON {
		return errors.New("original publication differs from frozen approval")
	}
	var verificationIDs []int64
	if common.UnmarshalJsonStr(p.VerificationIDs, &verificationIDs) != nil || len(verificationIDs) == 0 {
		return errors.New("missing original verification admission evidence")
	}
	return nil
}

func ztapiReductionNoIncrease20261107(base *ZTAPIModelPriceSource, before *ZTAPIModelPricePreview, rows []ZTAPIPriceReductionDimension20261107) error {
	var rules []ztapiABFrozenSaleRule
	if base.TokenPriceRulesJSON != "" {
		if err := common.UnmarshalJsonStr(base.TokenPriceRulesJSON, &rules); err != nil {
			return err
		}
	}
	var media ZTAPIMediaPriceContract
	if base.MediaPriceContractJSON != "" {
		var err error
		media, err = parseZTAPIMediaPriceContract(base.MediaPriceContractJSON)
		if err != nil {
			return err
		}
	}
	for _, r := range rows {
		old := before.SaleUSD[r.Dimension]
		if len(rules) > 0 {
			tier, err := strconv.Atoi(r.Tier)
			if err != nil || tier < 1 || tier > len(rules) {
				return errors.New("original tier missing")
			}
			old = rules[tier-1].Sale[r.Dimension]
		}
		if len(media.Rules) > 0 {
			old = ""
			for _, mr := range media.Rules {
				if mr.ID == r.Tier {
					old = mr.SaleUSD[r.Dimension]
				}
			}
		}
		o, err := decimal.NewFromString(old)
		if err != nil {
			return errors.New("original dimension missing")
		}
		reported, err := decimal.NewFromString(r.OldSale)
		if err != nil {
			return err
		}
		// Legacy ratio publications are limited to eight places; frozen tier
		// rules, when present, must match their ten-place amounts exactly.
		tolerance := decimal.RequireFromString("0.00000002")
		if len(rules) > 0 || len(media.Rules) > 0 {
			tolerance = decimal.Zero
		}
		if o.Sub(reported).Abs().GreaterThan(tolerance) {
			return fmt.Errorf("%s/%s old sale differs from current frozen preview", r.Tier, r.Dimension)
		}
		n := decimal.RequireFromString(r.ProposedSale)
		if !n.IsPositive() || n.GreaterThan(o) {
			return fmt.Errorf("%s/%s would increase a billing dimension", r.Tier, r.Dimension)
		}
	}
	return nil
}

func ztapiReductionCandidateTx20261107(tx *gorm.DB, s ZTAPIPriceReductionSelection20261107, operator int, rollback bool) (ztapiReductionCandidate, error) {
	var c ztapiReductionCandidate
	b, m, _, rows, err := ztapiReductionApproval20261107(s.ModelID)
	if err != nil {
		return c, err
	}
	if err := tx.Where("id = ? AND published = ?", s.ModelID, true).First(&c.config).Error; err != nil {
		return c, err
	}
	if err := tx.First(&c.previous, c.config.PublicationSnapshotID).Error; err != nil {
		return c, err
	}
	var original ZTAPIModelPublicationSnapshot
	if err := tx.First(&original, b.SnapshotID).Error; err != nil {
		return c, err
	}
	var base, current ZTAPIModelPriceSource
	if err := tx.First(&base, b.SourceID).Error; err != nil {
		return c, err
	}
	if err := ztapiReductionBase20261107(&base); err != nil {
		return c, err
	}
	if !ztapiPublicationPriceBasisPermittedTx(tx, &base) {
		return c, errors.New("original procurement chain fails financial validation")
	}
	if err := ztapiReductionOriginalSnapshot20261107(&original, b, m, &base); err != nil {
		return c, err
	}
	if err := tx.First(&current, c.previous.PriceSourceID).Error; err != nil {
		return c, err
	}
	p, config := &c.previous, &c.config
	if config.SourceModel != b.SourceModel || config.PublicNameValue() != b.PublicName || config.Protocol != m.Protocol || config.ProviderFamily != m.Family ||
		p.ModelConfigID != config.ID || p.ModelVersion != config.Version || p.SourceModel != config.SourceModel || p.PublicName != config.PublicNameValue() ||
		p.Protocol != config.Protocol || p.ProviderFamily != config.ProviderFamily || p.EnabledGroups != config.EnabledGroups ||
		p.EnabledGroups != original.EnabledGroups || p.AllowedChannelIDs != original.AllowedChannelIDs || p.VerificationIDs != original.VerificationIDs ||
		p.IdentityUpdatedAt != original.IdentityUpdatedAt || p.ImageProtocolContractJSON != original.ImageProtocolContractJSON || p.VideoProtocolContractJSON != original.VideoProtocolContractJSON || p.CacheWritePricingMode != original.CacheWritePricingMode ||
		config.InputPricePerMillion != p.InputPricePerMillion || config.OutputPricePerMillion != p.OutputPricePerMillion ||
		config.CacheReadRatio != p.CacheReadRatio || config.CacheCreationRatio != p.CacheCreationRatio || config.CacheCreation5mRatio != p.CacheCreation5mRatio || config.CacheCreation1hRatio != p.CacheCreation1hRatio ||
		config.ImageRatio != p.ImageRatio || config.AudioRatio != p.AudioRatio || config.AudioCompletionRatio != p.AudioCompletionRatio ||
		p.PricePolicy != current.PricePolicy || !ztapiSameDecimal(p.SaleMultiplier, current.SaleMultiplier) || p.TokenPriceRulesJSON != current.TokenPriceRulesJSON || p.MediaPriceContractJSON != current.MediaPriceContractJSON {
		return c, errors.New("active publication identity, admission, routing or price drift")
	}
	before, err := BuildZTAPIModelPricePreview(&current)
	if err != nil {
		return c, err
	}
	if !ztapiStoredPriceMatchesPreview(p.InputPricePerMillion, before.InputSaleUSDPerMillion) || !ztapiStoredPriceMatchesPreview(p.OutputPricePerMillion, before.OutputSaleUSDPerMillion) || !ztapiCacheRatiosMatchPreview(config, before) {
		return c, errors.New("active prices differ from bound source preview")
	}
	unchanged := false
	if config.Version != s.Version || p.ID != s.SnapshotID || current.ID != s.SourceID {
		var event ZTAPIAuditEvent
		var audit ztapiReductionAudit
		if config.Version != s.Version+1 || tx.Where("model_config_id = ? AND version = ? AND action = ?", config.ID, config.Version, ztapiReductionAction20261107(rollback)).Order("id DESC").First(&event).Error != nil ||
			common.UnmarshalJsonStr(event.Payload, &audit) != nil || audit.Selection != s || audit.SnapshotID != p.ID || audit.SourceID != current.ID || audit.Policy != string(ZTAPIPricePolicyReduction20261107) {
			return c, ErrZTAPIModelVersionConflict
		}
		unchanged = true
	}
	if current.PricePolicy == string(ZTAPIPricePolicyReduction20261107) {
		if err := ValidateZTAPIPriceReductionSource20261107(tx, &current); err != nil {
			return c, err
		}
	} else if !unchanged && current.ID != base.ID {
		return c, errors.New("active source is not the approved original")
	}
	basePreview, err := BuildZTAPIModelPricePreview(&base)
	if err != nil {
		return c, err
	}
	if b.ID != 5 {
		if err := ztapiReductionNoIncrease20261107(&base, basePreview, rows); err != nil {
			return c, err
		}
	}
	c.target, c.template = current, *p
	c.change = ZTAPIPriceReductionChange20261107{ZTAPIPriceReductionSelection20261107: s, PublicName: b.PublicName, Policy: string(ZTAPIPricePolicyReduction20261107), BonusRate: "0.05", AllowedChannels: original.ChannelIDs(), VerificationIDs: original.VerificationIDs,
		CostEvidence: "approved_frozen_supplier_quotations_and_original_persisted_native_costs_not_supplier_invoices", Dimensions: rows, Before: before, Unchanged: unchanged, Rollback: rollback}
	if b.ID == 5 {
		c.change.Skipped = true
		c.change.SkipReason = "Deepseek Flash excluded: uncertain non-Prime Flash alias; retain old price"
		c.change.After = before
		return c, nil
	}
	c.change.TargetMargin = rows[0].Margin
	if rollback {
		if !unchanged && current.PricePolicy != string(ZTAPIPricePolicyReduction20261107) {
			return c, errors.New("rollback requires this dated policy")
		}
		// Only the audited operation may restore the approved original.
		var event ZTAPIAuditEvent
		var applied ztapiReductionAudit
		if tx.Where("model_config_id = ? AND version = ? AND action = ?", config.ID, b.Version+1, ztapiReductionAction20261107(false)).First(&event).Error != nil || common.UnmarshalJsonStr(event.Payload, &applied) != nil ||
			applied.Selection.SnapshotID != b.SnapshotID || applied.Selection.SourceID != b.SourceID || applied.SnapshotID != s.SnapshotID || applied.SourceID != s.SourceID {
			return c, errors.New("rollback does not identify the audited reduction")
		}
		if unchanged && (ztapiABPriceSourceEvidenceMismatch(&current, &base) != "" || current.MediaPriceContractJSON != base.MediaPriceContractJSON || current.CashMarginBaseSourceID != base.CashMarginBaseSourceID || current.QuotationEffectiveAt != base.QuotationEffectiveAt) {
			return c, errors.New("restored original evidence drift")
		}
		c.target, c.template = base, original
		c.change.TargetMargin = ""
	} else if !unchanged {
		c.target, err = deriveZTAPIPriceReduction20261107(&base, operator)
		if err != nil {
			return c, err
		}
	}
	c.change.After, err = BuildZTAPIModelPricePreview(&c.target)
	return c, err
}

func PreviewZTAPIPriceReduction20261107(selected []ZTAPIPriceReductionSelection20261107, rollback bool) ([]ZTAPIPriceReductionChange20261107, error) {
	items, err := ztapiReductionSelections20261107(selected, rollback)
	if err != nil {
		return nil, err
	}
	if DB == nil {
		return nil, errors.New("database unavailable")
	}
	result := []ZTAPIPriceReductionChange20261107{}
	err = DB.Transaction(func(tx *gorm.DB) error {
		for _, s := range items {
			c, err := ztapiReductionCandidateTx20261107(tx, s, 1, rollback)
			if err != nil {
				return fmt.Errorf("model %d: %w", s.ModelID, err)
			}
			result = append(result, c.change)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ApplyZTAPIPriceReduction20261107(operator int, selected []ZTAPIPriceReductionSelection20261107, rollback bool) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{Models: []string{}}
	items, err := ztapiReductionSelections20261107(selected, rollback)
	if err != nil {
		return result, err
	}
	if DB == nil || operator <= 0 {
		return result, errors.New("database and operator required")
	}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		for _, s := range items {
			c, err := ztapiReductionCandidateTx20261107(tx, s, operator, rollback)
			if err != nil {
				return fmt.Errorf("model %d: %w", s.ModelID, err)
			}
			if c.change.Skipped {
				result.Skipped = append(result.Skipped, c.change.PublicName+": "+c.change.SkipReason)
				continue
			}
			if c.change.Unchanged {
				result.Unchanged++
				continue
			}
			target, config, template := c.target, c.config, c.template
			if !rollback {
				if err := ValidateZTAPIPriceReductionSource20261107(tx, &target); err != nil {
					return err
				}
			} else if !ztapiPublicationPriceBasisPermittedTx(tx, &target) {
				return errors.New("rollback original financial evidence invalid")
			}
			if err := applyZTAPICommercialPreview(&config, c.change.After); err != nil {
				return err
			}
			if rollback {
				config.InputPricePerMillion, config.OutputPricePerMillion = template.InputPricePerMillion, template.OutputPricePerMillion
				config.CacheReadRatio, config.CacheCreationRatio = template.CacheReadRatio, template.CacheCreationRatio
				config.CacheCreation5mRatio, config.CacheCreation1hRatio = template.CacheCreation5mRatio, template.CacheCreation1hRatio
				config.ImageRatio, config.AudioRatio, config.AudioCompletionRatio = template.ImageRatio, template.AudioRatio, template.AudioCompletionRatio
			}
			var latest uint64
			if err := tx.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).Select("COALESCE(MAX(version),0)").Scan(&latest).Error; err != nil {
				return err
			}
			now := common.GetTimestamp()
			target.ID, target.Version, target.CreatedAt, target.OperatorID = 0, latest+1, now, operator
			if err := tx.Create(&target).Error; err != nil {
				return err
			}
			var verificationIDs []int64
			if err := common.UnmarshalJsonStr(template.VerificationIDs, &verificationIDs); err != nil {
				return err
			}
			snapshot, err := createZTAPIModelPublicationSnapshotTx(tx, &config, config.Version+1, ztapiPublicationEvidence{PriceSourceID: target.ID, AllowedChannelIDs: template.ChannelIDs(), VerificationIDs: verificationIDs, IdentityUpdatedAt: template.IdentityUpdatedAt, CacheWritePricingMode: template.CacheWritePricingMode, ImageProtocolContractJSON: template.ImageProtocolContractJSON, VideoProtocolContractJSON: template.VideoProtocolContractJSON})
			if err != nil {
				return err
			}
			updates := map[string]any{"version": config.Version + 1, "publication_snapshot_id": snapshot.ID, "updated_at": now, "input_price_per_million": config.InputPricePerMillion, "output_price_per_million": config.OutputPricePerMillion, "input_cost_per_million": config.InputCostPerMillion, "output_cost_per_million": config.OutputCostPerMillion,
				"cache_read_ratio": config.CacheReadRatio, "cache_creation_ratio": config.CacheCreationRatio, "cache_creation_5m_ratio": config.CacheCreation5mRatio, "cache_creation_1h_ratio": config.CacheCreation1hRatio, "image_ratio": config.ImageRatio, "audio_ratio": config.AudioRatio, "audio_completion_ratio": config.AudioCompletionRatio}
			updated := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ? AND publication_snapshot_id = ?", config.ID, s.Version, s.SnapshotID).Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrZTAPIModelVersionConflict
			}
			payload, err := common.Marshal(ztapiReductionAudit{Selection: s, SnapshotID: snapshot.ID, SourceID: target.ID, Policy: string(ZTAPIPricePolicyReduction20261107), Bonus: "0.05", Dimensions: c.change.Dimensions})
			if err != nil {
				return err
			}
			if err := tx.Create(&ZTAPIAuditEvent{Action: ztapiReductionAction20261107(rollback), ModelConfigID: config.ID, PublicName: config.PublicNameValue(), Version: config.Version + 1, OperatorID: operator, Payload: string(payload), CreatedAt: now}).Error; err != nil {
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
