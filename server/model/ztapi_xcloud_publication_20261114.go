package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type ZTAPIXcloudPublication20261114 struct {
	SourceModel    string `json:"source_model"`
	Version        uint64 `json:"version"`
	SourceID       int64  `json:"source_id"`
	SnapshotID     int64  `json:"snapshot_id"`
	VerificationID int64  `json:"verification_id"`
}

type ztapiXcloudPublicationAudit20261114 struct {
	Original         ZTAPIModelConfig `json:"original"`
	OriginalGroups   string           `json:"original_groups"`
	OriginalSourceID int64            `json:"original_source_id"`
	NewSnapshotID    int64            `json:"new_snapshot_id"`
}

const ztapiXcloudPublishAction20261114 = "model.xcloud_20261114_published"

func IsZTAPIXcloudCohort20261114(source string) bool {
	_, err := ztapiXcloudQuote20261114(source)
	return err == nil
}

func CheckZTAPIXcloudDowngrade20261114() (ZTAPICashMarginDowngrade20261030, error) {
	return checkZTAPIPolicyDowngrade20261107([]string{string(ZTAPIPricePolicyXcloud20261114)}, []string{"zt-claude-sonnet-5", "zt-deepseek-v4.1-flash"})
}

func RecordZTAPIXcloudDiscovery20261114(source string, id int, visible []string, operator int, fetchedAt time.Time) (*ZTAPIDiscoveryResult, error) {
	q, err := ztapiXcloudQuote20261114(source)
	if err != nil || operator <= 0 || DB == nil || fetchedAt.IsZero() || fetchedAt.After(time.Now().Add(time.Minute)) {
		return nil, errors.New("invalid Xcloud discovery evidence")
	}
	ids, err := normalizeZTAPIDiscoveryModelIDs(visible)
	if err != nil {
		return nil, err
	}
	var result *ZTAPIDiscoveryResult
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.First(&channel, id).Error; err != nil {
			return err
		}
		physical, err := ztapiXcloudChannelPhysical20261114(&channel, source)
		if err != nil || !slices.Contains(ids, physical) || !slices.Contains(channel.GetModels(), source) {
			return errors.New("exact approved model is not advertised and configured on its channel")
		}
		var config ZTAPIModelConfig
		if err := tx.Where("id = ? AND source_model = ? AND public_name = ?", q.id, source, q.public).First(&config).Error; err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(physical))
		discovery := ZTAPIDiscoverySnapshot{ChannelID: id, ModelListHash: hex.EncodeToString(hash[:]), ModelCount: 1, FetchedAt: fetchedAt.Unix()}
		if err := tx.Create(&discovery).Error; err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIDiscoveredModel{SnapshotID: discovery.ID, ChannelID: id, SourceModel: physical}).Error; err != nil {
			return err
		}
		result = &ZTAPIDiscoveryResult{SnapshotID: discovery.ID, ChannelID: id, ModelListHash: discovery.ModelListHash, ModelIDs: []string{physical}, ImportedCount: 1, FetchedAt: discovery.FetchedAt}
		return tx.Create(&ZTAPIAuditEvent{Action: "model.xcloud_20261114_discovery", ModelConfigID: q.id, PublicName: q.public, Version: config.Version, OperatorID: operator,
			Payload: common.MapToJsonStr(map[string]any{"channel_id": id, "physical_model": physical, "discovery_id": discovery.ID}), CreatedAt: discovery.FetchedAt}).Error
	})
	return result, err
}

func ztapiXcloudCohortVerified(v ZTAPIModelVerification, config ZTAPIModelConfig, id int) bool {
	return v.ID > 0 && v.ModelConfigID == config.ID && v.ChannelID == id && v.Protocol == config.Protocol &&
		(v.Modality == "" || v.Modality == ZTAPIModalityText) && v.NonStreamingPassed && v.StreamingRequired &&
		v.StreamingPassed && v.UsageReconciled && v.InvalidKeyClassified && v.InsufficientBalanceClassified &&
		v.RateLimitClassified && v.TimeoutClassified
}

func ztapiXcloudEvidenceTx20261114(tx *gorm.DB, config ZTAPIModelConfig, q ztapiXcloudCohortQuote20261114, req ZTAPIXcloudPublication20261114, previous *ZTAPIModelPublicationSnapshot) (ztapiPublicationEvidence, error) {
	cutoff := time.Now().Add(-ztapiPublicationEvidenceMaxAge).Unix()
	allowed := map[int]struct{}{}
	priorities := map[int]int64{}
	for _, id := range q.channels() {
		var channel Channel
		if err := tx.First(&channel, id).Error; err != nil {
			return ztapiPublicationEvidence{}, err
		}
		physical, err := ztapiXcloudChannelPhysical20261114(&channel, q.source)
		if err != nil || !slices.Contains(channel.GetModels(), q.source) {
			return ztapiPublicationEvidence{}, errors.New("approved Xcloud cohort channel changed")
		}
		var count int64
		if err := tx.Table("ztapi_discovered_models AS m").Joins("JOIN ztapi_discovery_snapshots AS d ON d.id = m.snapshot_id AND d.channel_id = m.channel_id").
			Where("m.channel_id = ? AND m.source_model = ? AND d.fetched_at BETWEEN ? AND ?", id, physical, cutoff, common.GetTimestamp()).Count(&count).Error; err != nil {
			return ztapiPublicationEvidence{}, err
		}
		if count == 0 {
			return ztapiPublicationEvidence{}, errors.New("fresh exact supplier discovery is missing")
		}
		allowed[id], priorities[id] = struct{}{}, channel.GetPriority()
	}
	if q.backupModel != "" && priorities[q.primary] <= priorities[2] {
		return ztapiPublicationEvidence{}, errors.New("Xcloud primary does not outrank the Yunxin backup")
	}
	var primary ZTAPIModelVerification
	if err := tx.Where("model_config_id = ? AND channel_id = ?", q.id, q.primary).Order("verified_at DESC, id DESC").First(&primary).Error; err != nil {
		return ztapiPublicationEvidence{}, err
	}
	if primary.ID != req.VerificationID || primary.VerifiedAt < cutoff || primary.VerifiedAt > common.GetTimestamp() ||
		!ztapiXcloudCohortVerified(primary, config, q.primary) {
		return ztapiPublicationEvidence{}, errors.New("latest Xcloud route verification does not match")
	}
	if previous == nil {
		blockers, evidence, err := ztapiPublicationBlockersTx(tx, &config)
		if err == nil && len(blockers) > 0 {
			err = &ZTAPIPublicationBlockedError{Blockers: blockers}
		}
		if err == nil && (!reflect.DeepEqual(evidence.AllowedChannelIDs, q.channels()) || !slices.Contains(evidence.VerificationIDs, primary.ID)) {
			err = errors.New("publication gates did not approve the exact Xcloud route")
		}
		return evidence, err
	}
	// The price-only update preserves the previously published backup. Require
	// current discovery and no newer failed verification before retaining it.
	var backup ZTAPIModelVerification
	if err := tx.Where("model_config_id = ? AND channel_id = ?", q.id, 2).Order("verified_at DESC, id DESC").First(&backup).Error; err != nil {
		return ztapiPublicationEvidence{}, err
	}
	var originalIDs []int64
	if common.UnmarshalJsonStr(previous.VerificationIDs, &originalIDs) != nil || !slices.Contains(originalIDs, backup.ID) ||
		!ztapiXcloudCohortVerified(backup, config, 2) {
		return ztapiPublicationEvidence{}, errors.New("existing Yunxin backup is no longer verified")
	}
	routes, err := ztapiPublicationRouteIDsByGroupTx(tx, &config, allowed)
	if err != nil {
		return ztapiPublicationEvidence{}, err
	}
	for _, group := range config.Groups() {
		if !reflect.DeepEqual(uniqueSortedInts(routes[group]), q.channels()) {
			return ztapiPublicationEvidence{}, errors.New("Xcloud primary or Yunxin backup is unavailable to a published group")
		}
	}
	return ztapiPublicationEvidence{AllowedChannelIDs: q.channels(), VerificationIDs: uniqueSortedInt64s([]int64{backup.ID, primary.ID}),
		IdentityUpdatedAt: previous.IdentityUpdatedAt, CacheWritePricingMode: previous.CacheWritePricingMode}, nil
}

func ztapiXcloudUpdateConfig20261114(tx *gorm.DB, current, next ZTAPIModelConfig, snapshotID int64, now int64) error {
	update := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ? AND publication_snapshot_id = ?", current.ID, current.Version, current.PublicationSnapshotID).Updates(map[string]any{
		"version": current.Version + 1, "publication_snapshot_id": snapshotID, "published": snapshotID > 0, "updated_at": now,
		"input_price_per_million": next.InputPricePerMillion, "output_price_per_million": next.OutputPricePerMillion,
		"input_cost_per_million": next.InputCostPerMillion, "output_cost_per_million": next.OutputCostPerMillion,
		"cache_read_ratio": next.CacheReadRatio, "cache_creation_ratio": next.CacheCreationRatio, "cache_creation_5m_ratio": next.CacheCreation5mRatio,
		"cache_creation_1h_ratio": next.CacheCreation1hRatio, "image_ratio": next.ImageRatio, "audio_ratio": next.AudioRatio,
		"audio_completion_ratio": next.AudioCompletionRatio,
	})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return ErrZTAPIModelVersionConflict
	}
	return nil
}

func ApplyZTAPIXcloudPublication20261114(operator int, req ZTAPIXcloudPublication20261114) (*ZTAPIModelPublicationSnapshot, error) {
	return applyZTAPIXcloudPublication20261114(operator, req, nil)
}

// The operational publisher may stage the protected referral proof while the
// catalog lock is held. A failed check rolls back the catalog, not the old proof.
func ApplyZTAPIXcloudPublicationWithGate20261114(operator int, req ZTAPIXcloudPublication20261114, check func(*gorm.DB) error) (*ZTAPIModelPublicationSnapshot, error) {
	if check == nil {
		return nil, errors.New("publication requires a catalog cost check")
	}
	return applyZTAPIXcloudPublication20261114(operator, req, check)
}

func applyZTAPIXcloudPublication20261114(operator int, req ZTAPIXcloudPublication20261114, check func(*gorm.DB) error) (*ZTAPIModelPublicationSnapshot, error) {
	q, err := ztapiXcloudQuote20261114(req.SourceModel)
	if err != nil || DB == nil || operator <= 0 || req.Version == 0 || req.SourceID <= 0 || req.VerificationID <= 0 ||
		(q.backupModel == "" && req.SnapshotID != 0) || (q.backupModel != "" && req.SnapshotID <= 0) {
		return nil, errors.New("explicit Xcloud publication baseline is required")
	}
	var result *ZTAPIModelPublicationSnapshot
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var current ZTAPIModelConfig
		if err := tx.First(&current, q.id).Error; err != nil {
			return err
		}
		if current.Version != req.Version || current.PublicationSnapshotID != req.SnapshotID || current.Published != (req.SnapshotID > 0) {
			return ErrZTAPIModelVersionConflict
		}
		if current.SourceModel != q.source || current.PublicNameValue() != q.public || current.ProviderFamily != q.family ||
			current.Protocol != ZTAPIProtocolOpenAICompatible || !reflect.DeepEqual(current.Groups(), []string{"default"}) {
			return errors.New("Xcloud model identity or eligible group changed")
		}
		if err := validateZTAPIPublicNameClaimTx(tx, current.ID, q.source, q.public); err != nil {
			return err
		}
		var priorSource ZTAPIModelPriceSource
		if err := tx.Where("model_config_id = ?", current.ID).Order("version DESC, id DESC").First(&priorSource).Error; err != nil {
			return err
		}
		if priorSource.ID != req.SourceID || priorSource.SourceModel != q.source {
			return ErrZTAPIModelVersionConflict
		}
		var previous *ZTAPIModelPublicationSnapshot
		if current.Published {
			previous = &ZTAPIModelPublicationSnapshot{}
			if err := tx.First(previous, current.PublicationSnapshotID).Error; err != nil {
				return err
			}
			if previous.PriceSourceID != priorSource.ID || previous.ModelVersion != current.Version || previous.ModelConfigID != current.ID ||
				previous.SourceModel != current.SourceModel || previous.PublicName != current.PublicNameValue() ||
				previous.Protocol != current.Protocol || previous.ProviderFamily != current.ProviderFamily || previous.EnabledGroups != current.EnabledGroups ||
				!reflect.DeepEqual(previous.ChannelIDs(), q.channels()) || !isZTAPIDatedRetailPolicy20261107(priorSource.PricePolicy) ||
				!ztapiPublicationPriceBasisPermittedTx(tx, &priorSource) {
				return errors.New("active Sonnet publication differs from the reviewed price-only baseline")
			}
		}
		target, err := buildZTAPIXcloudSource20261114(q.source, operator)
		if err != nil {
			return err
		}
		preview, err := BuildZTAPIModelPricePreview(&target)
		if err != nil {
			return err
		}
		next := current
		if err := applyZTAPICommercialPreview(&next, preview); err != nil {
			return err
		}
		now := common.GetTimestamp()
		target.Version, target.CreatedAt = priorSource.Version+1, now
		if err := tx.Create(&target).Error; err != nil {
			return err
		}
		evidence, err := ztapiXcloudEvidenceTx20261114(tx, next, q, req, previous)
		if err != nil {
			return err
		}
		evidence.PriceSourceID = target.ID
		result, err = createZTAPIModelPublicationSnapshotTx(tx, &next, current.Version+1, evidence)
		if err != nil {
			return err
		}
		if err := ztapiXcloudUpdateConfig20261114(tx, current, next, result.ID, now); err != nil {
			return err
		}
		audit, err := common.Marshal(ztapiXcloudPublicationAudit20261114{current, current.EnabledGroups, priorSource.ID, result.ID})
		if err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIAuditEvent{Action: ztapiXcloudPublishAction20261114, ModelConfigID: q.id, PublicName: q.public, Version: result.ModelVersion, OperatorID: operator, Payload: string(audit), CreatedAt: now}).Error; err != nil {
			return err
		}
		if check != nil {
			return check(tx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}

func RestoreZTAPIXcloudPublication20261114(operator int, source string, version uint64, snapshotID int64) error {
	q, err := ztapiXcloudQuote20261114(source)
	if err != nil || DB == nil || operator <= 0 || version == 0 || snapshotID <= 0 {
		return errors.New("explicit Xcloud restore baseline is required")
	}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var current ZTAPIModelConfig
		if err := tx.First(&current, q.id).Error; err != nil {
			return err
		}
		if current.SourceModel != q.source || current.PublicNameValue() != q.public || current.Version != version || current.PublicationSnapshotID != snapshotID || !current.Published {
			return ErrZTAPIModelVersionConflict
		}
		var event ZTAPIAuditEvent
		if err := tx.Where("model_config_id = ? AND version = ? AND action = ?", q.id, version, ztapiXcloudPublishAction20261114).First(&event).Error; err != nil {
			return err
		}
		var audit ztapiXcloudPublicationAudit20261114
		if common.UnmarshalJsonStr(event.Payload, &audit) != nil || audit.NewSnapshotID != snapshotID || audit.Original.ID != q.id ||
			audit.Original.Version+1 != version || audit.Original.SourceModel != q.source || audit.Original.PublicNameValue() != q.public ||
			audit.OriginalGroups != current.EnabledGroups {
			return errors.New("Xcloud restore audit does not match the active publication")
		}
		var active ZTAPIModelPublicationSnapshot
		if err := tx.First(&active, snapshotID).Error; err != nil {
			return err
		}
		if active.PricePolicy != string(ZTAPIPricePolicyXcloud20261114) || active.ModelConfigID != q.id {
			return errors.New("active publication is not owned by the Xcloud release")
		}
		// Preserve both historical snapshots and sources. Restore the previous
		// price basis as the latest version so future draft edits see the right source.
		var prior, latest ZTAPIModelPriceSource
		if err := tx.Where("model_config_id = ?", q.id).Order("version DESC, id DESC").First(&latest).Error; err != nil {
			return err
		}
		if latest.ID != active.PriceSourceID {
			return ErrZTAPIModelVersionConflict
		}
		if err := tx.Where("model_config_id = ? AND version < ?", q.id, latest.Version).Order("version DESC, id DESC").First(&prior).Error; err != nil {
			return err
		}
		if prior.ID != audit.OriginalSourceID {
			return errors.New("original Xcloud publication price evidence changed")
		}
		now := common.GetTimestamp()
		originalSourceID := prior.ID
		prior.ID, prior.Version, prior.CreatedAt, prior.OperatorID = 0, latest.Version+1, now, operator
		if err := tx.Create(&prior).Error; err != nil {
			return err
		}
		restoredID := int64(0)
		if audit.Original.Published {
			var old ZTAPIModelPublicationSnapshot
			if err := tx.First(&old, audit.Original.PublicationSnapshotID).Error; err != nil {
				return err
			}
			if old.ModelVersion != audit.Original.Version || old.ModelConfigID != q.id || old.PriceSourceID != originalSourceID ||
				!reflect.DeepEqual(old.ChannelIDs(), q.channels()) || !ztapiPublicationPriceBasisPermittedTx(tx, &prior) {
				return errors.New("original Sonnet publication cannot be restored")
			}
			old.ID, old.ModelVersion, old.PriceSourceID, old.CreatedAt = 0, version+1, prior.ID, now
			if err := tx.Create(&old).Error; err != nil {
				return err
			}
			restoredID = old.ID
		}
		if err := ztapiXcloudUpdateConfig20261114(tx, current, audit.Original, restoredID, now); err != nil {
			return err
		}
		return tx.Create(&ZTAPIAuditEvent{Action: "model.xcloud_20261114_restored", ModelConfigID: q.id, PublicName: q.public, Version: version + 1, OperatorID: operator,
			Payload: common.MapToJsonStr(map[string]any{"replaced_snapshot_id": snapshotID, "restored_snapshot_id": restoredID}), CreatedAt: now}).Error
	})
	if err == nil {
		invalidateZTAPICatalogCaches()
	}
	return err
}
