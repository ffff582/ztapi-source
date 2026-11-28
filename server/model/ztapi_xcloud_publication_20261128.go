package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ZTAPIXcloudStage20261128 struct {
	SourceModel string `json:"source_model"`
	Group       string `json:"group"`
	ChannelID   int    `json:"channel_id"`
	ConfigID    int    `json:"config_id"`
	Version     uint64 `json:"version"`
	SnapshotID  int64  `json:"snapshot_id"`
	SourceID    int64  `json:"source_id"`
}

type ZTAPIXcloudStaged20261128 struct {
	Config      ZTAPIModelConfig        `json:"config"`
	Source      ZTAPIModelPriceSource   `json:"source"`
	Preview     *ZTAPIModelPricePreview `json:"preview"`
	DiscoveryID int64                   `json:"discovery_id"`
}

type ZTAPIXcloudPublish20261128 struct {
	ConfigID       int    `json:"config_id"`
	Version        uint64 `json:"version"`
	SnapshotID     int64  `json:"snapshot_id"`
	SourceID       int64  `json:"source_id"`
	VerificationID int64  `json:"verification_id"`
}

type ztapiXcloudPublicationAudit20261128 struct {
	Original         ZTAPIModelConfig `json:"original"`
	OriginalGroups   string           `json:"original_groups"`
	OriginalSourceID int64            `json:"original_source_id"`
	NewSnapshotID    int64            `json:"new_snapshot_id"`
}

const ztapiXcloudPublishAction20261128 = "model.xcloud_20261128_published"

func ztapiXcloudPublishedBaseline20261128(tx *gorm.DB, config ZTAPIModelConfig) (*ZTAPIModelPublicationSnapshot, error) {
	if !config.Published {
		if config.PublicationSnapshotID != 0 {
			return nil, errors.New("unpublished Xcloud config has an unexpected snapshot")
		}
		return nil, nil
	}
	var old ZTAPIModelPublicationSnapshot
	if err := tx.First(&old, config.PublicationSnapshotID).Error; err != nil {
		return nil, err
	}
	if old.ModelConfigID != config.ID || old.ModelVersion != config.Version || old.SourceModel != config.SourceModel ||
		old.PublicName != config.PublicNameValue() || old.EnabledGroups != config.EnabledGroups || len(old.ChannelIDs()) != 1 ||
		old.ChannelIDs()[0] < 4 || old.ChannelIDs()[0] > 8 || old.CacheWritePricingMode != "" {
		return nil, errors.New("existing publication is not an Xcloud-only baseline; Yunxin routes remain unchanged")
	}
	return &old, nil
}

func ztapiXcloudPriorRetailTx20261128(tx *gorm.DB, config ZTAPIModelConfig, old *ZTAPIModelPublicationSnapshot) (string, error) {
	if old != nil && old.TokenPriceRulesJSON != "" {
		return old.TokenPriceRulesJSON, nil
	}
	if !config.Published {
		return "", nil
	}
	return "", errors.New("published Xcloud retail lacks frozen tier evidence")
}

// Discovery is supplied only by the authenticated controller's live upstream
// fetch. This operation never imports the remainder of that supplier model list.
func StageZTAPIXcloud20261128(operator int, req ZTAPIXcloudStage20261128, visible []string, fetchedAt time.Time) (*ZTAPIXcloudStaged20261128, error) {
	public, provider, protocol, err := ZTAPIXcloudPublicIdentity20261128(req.SourceModel)
	group, known := ZTAPIXcloudChannelGroup20261128(req.ChannelID)
	now := time.Now().UTC()
	if err != nil || DB == nil || operator <= 0 || !known || group != req.Group || fetchedAt.IsZero() ||
		fetchedAt.After(now.Add(time.Minute)) || fetchedAt.Before(now.Add(-ztapiPublicationEvidenceMaxAge)) ||
		!slices.Contains(visible, req.SourceModel) {
		return nil, errors.New("fresh exact Xcloud discovery and approved group are required")
	}
	if _, err = ZTAPIXcloudQuoteFor20261128(req.SourceModel, req.Group, 0); err != nil {
		return nil, err
	}
	var result ZTAPIXcloudStaged20261128
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.First(&channel, req.ChannelID).Error; err != nil {
			return err
		}
		if _, err := ztapiXcloudChannelPhysical20261128(&channel, req.SourceModel, req.Group); err != nil {
			return err
		}
		if !slices.Contains(channel.GetGroups(), "default") {
			return errors.New("Xcloud channel is not available to the default publication group")
		}
		var configs []ZTAPIModelConfig
		if err := tx.Find(&configs).Error; err != nil {
			return err
		}
		plans, err := BuildZTAPIXcloudIdentityPlan20261128(configs)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(plans, func(p ZTAPIXcloudIdentityPlan20261128) bool { return p.SourceModel == req.SourceModel })
		if idx < 0 {
			return errors.New("missing Xcloud identity plan")
		}
		plan := plans[idx]
		if plan.ExistingConfigID != req.ConfigID || plan.ExistingVersion != req.Version {
			return ErrZTAPIModelVersionConflict
		}
		var config ZTAPIModelConfig
		if req.ConfigID == 0 {
			if req.SnapshotID != 0 || req.SourceID != 0 {
				return ErrZTAPIModelVersionConflict
			}
			config = ZTAPIModelConfig{SourceModel: req.SourceModel, PublicName: &public, Protocol: protocol, ProviderFamily: provider,
				Family: ztapiLegacyFamilyForProvider(provider), EnabledGroups: `["default"]`, Version: 1, CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
				ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1}
			if err := tx.Create(&config).Error; err != nil {
				return err
			}
		} else {
			if err := tx.First(&config, req.ConfigID).Error; err != nil {
				return err
			}
			if config.PublicationSnapshotID != req.SnapshotID || (config.PublicNameValue() != "" && config.PublicNameValue() != public) ||
				(config.ProviderFamily != "" && config.ProviderFamily != provider) || (config.Protocol != "" && config.Protocol != protocol) ||
				!reflect.DeepEqual(config.Groups(), []string{"default"}) {
				return ErrZTAPIModelVersionConflict
			}
		}
		old, err := ztapiXcloudPublishedBaseline20261128(tx, config)
		if err != nil {
			return err
		}
		var latest ZTAPIModelPriceSource
		if err := tx.Where("model_config_id = ?", config.ID).Order("version DESC, id DESC").Limit(1).Find(&latest).Error; err != nil {
			return err
		}
		if latest.ID != req.SourceID {
			return ErrZTAPIModelVersionConflict
		}
		if old != nil && old.PriceSourceID != latest.ID {
			return errors.New("another price candidate is already staged for this publication")
		}
		config.PublicName, config.Protocol, config.ProviderFamily = &public, protocol, provider
		prior, err := ztapiXcloudPriorRetailTx20261128(tx, config, old)
		if err != nil {
			return err
		}
		source, err := BuildZTAPIXcloudPriceSource20261128(config, req.Group, operator, prior)
		if err != nil {
			return err
		}
		source.Version, source.CreatedAt = latest.Version+1, now.Unix()
		preview, err := BuildZTAPIModelPricePreview(&source)
		if err != nil {
			return err
		}
		if err := tx.Create(&source).Error; err != nil {
			return err
		}
		if !config.Published {
			if err := applyZTAPICommercialPreview(&config, preview); err != nil {
				return err
			}
			// Legacy ratio columns require positive placeholders. Only the frozen
			// source dimensions authorize charges; missing cache quotes stay absent.
			if _, err := applyZTAPIDefaultPricingRatios(&config); err != nil {
				return err
			}
			config.Family = ztapiLegacyFamilyForProvider(provider)
			if req.ConfigID != 0 {
				config.Version++
			}
			config.UpdatedAt = now.Unix()
			if err := tx.Save(&config).Error; err != nil {
				return err
			}
		}
		identity := ZTAPIModelIdentity{ModelConfigID: config.ID, PublicName: public, Protocol: protocol, ProviderFamily: provider,
			SourceReference: "https://www.xcloudtoken.com/pricing#" + ZTAPIXcloudQuoteSHA25620261128, VerificationState: "mapped", OperatorID: operator, UpdatedAt: now.Unix()}
		if !config.Published {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model_config_id"}}, DoUpdates: clause.AssignmentColumns([]string{"public_name", "protocol", "provider_family", "source_reference", "verification_status", "operator_id", "updated_at"})}).Create(&identity).Error; err != nil {
				return err
			}
		}
		hash := sha256.Sum256([]byte(req.SourceModel))
		discovery := ZTAPIDiscoverySnapshot{ChannelID: channel.Id, ModelListHash: hex.EncodeToString(hash[:]), ModelCount: 1, FetchedAt: fetchedAt.Unix()}
		if err := tx.Create(&discovery).Error; err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIDiscoveredModel{SnapshotID: discovery.ID, ChannelID: channel.Id, SourceModel: req.SourceModel}).Error; err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIAuditEvent{Action: "model.xcloud_20261128_staged", ModelConfigID: config.ID, PublicName: public, Version: config.Version,
			OperatorID: operator, CreatedAt: now.Unix(), Payload: common.MapToJsonStr(map[string]any{"source_id": source.ID, "channel_id": channel.Id, "group": req.Group, "discovery_id": discovery.ID, "baseline_snapshot_id": req.SnapshotID, "baseline_source_id": req.SourceID})}).Error; err != nil {
			return err
		}
		result = ZTAPIXcloudStaged20261128{config, source, preview, discovery.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	invalidateZTAPICatalogCaches()
	return &result, nil
}

func ApplyZTAPIXcloudPublication20261128(operator int, req ZTAPIXcloudPublish20261128, check func(*gorm.DB) error) (*ZTAPIModelPublicationSnapshot, error) {
	return applyZTAPIXcloudPublicationChecks20261128(operator, req, nil, check)
}

func applyZTAPIXcloudPublicationChecks20261128(operator int, req ZTAPIXcloudPublish20261128, before, check func(*gorm.DB) error) (*ZTAPIModelPublicationSnapshot, error) {
	if DB == nil || operator <= 0 || req.ConfigID <= 0 || req.Version == 0 || req.SourceID <= 0 || req.VerificationID <= 0 || check == nil {
		return nil, errors.New("exact Xcloud baseline, verification and protected cost gate are required")
	}
	var result *ZTAPIModelPublicationSnapshot
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var current ZTAPIModelConfig
		if err := tx.First(&current, req.ConfigID).Error; err != nil {
			return err
		}
		if current.Version != req.Version || current.PublicationSnapshotID != req.SnapshotID {
			return ErrZTAPIModelVersionConflict
		}
		if before != nil {
			if err := before(tx); err != nil {
				return err
			}
		}
		old, err := ztapiXcloudPublishedBaseline20261128(tx, current)
		if err != nil {
			return err
		}
		var source ZTAPIModelPriceSource
		if err := tx.Where("model_config_id = ?", current.ID).Order("version DESC, id DESC").First(&source).Error; err != nil {
			return err
		}
		if source.ID != req.SourceID || source.SourceModel != current.SourceModel {
			return ErrZTAPIModelVersionConflict
		}
		if _, err := ztapiXcloudRules20261128(&source); err != nil {
			return err
		}
		if err := ztapiXcloudPrepareRoute20261128(tx, current, &source); err != nil {
			return err
		}
		preview, err := BuildZTAPIModelPricePreview(&source)
		if err != nil {
			return err
		}
		next := current
		if err := applyZTAPICommercialPreview(&next, preview); err != nil {
			return err
		}
		blockers, evidence, err := ztapiPublicationBlockersTx(tx, &next)
		if err != nil {
			return err
		}
		if len(blockers) > 0 {
			return &ZTAPIPublicationBlockedError{Blockers: blockers}
		}
		if len(evidence.AllowedChannelIDs) != 1 || !slices.Contains(evidence.VerificationIDs, req.VerificationID) {
			return errors.New("verification does not match the exact Xcloud route")
		}
		var verification ZTAPIModelVerification
		if err := tx.First(&verification, req.VerificationID).Error; err != nil {
			return err
		}
		if verification.VerifiedAt < source.CreatedAt || verification.VerifiedAt > common.GetTimestamp() ||
			!ztapiXcloudCohortVerified(verification, next, evidence.AllowedChannelIDs[0]) {
			return errors.New("Xcloud verification predates the staged quote or is incomplete")
		}
		evidence.PriceSourceID, evidence.PreviousPublicationSnapshotID = source.ID, req.SnapshotID
		result, err = createZTAPIModelPublicationSnapshotTx(tx, &next, current.Version+1, evidence)
		if err != nil {
			return err
		}
		if err := ztapiXcloudUpdateConfig20261114(tx, current, next, result.ID, common.GetTimestamp()); err != nil {
			return err
		}
		priorSourceID := int64(0)
		if old != nil {
			priorSourceID = old.PriceSourceID
		}
		audit := ztapiXcloudPublicationAudit20261128{current, current.EnabledGroups, priorSourceID, result.ID}
		auditJSON, err := common.Marshal(audit)
		if err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIAuditEvent{Action: ztapiXcloudPublishAction20261128, ModelConfigID: current.ID, PublicName: current.PublicNameValue(), Version: result.ModelVersion,
			OperatorID: operator, CreatedAt: common.GetTimestamp(), Payload: string(auditJSON)}).Error; err != nil {
			return err
		}
		rows, err := loadZTAPIQuotedPublications(tx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(buildZTAPIPublicCatalog(rows), func(item ZTAPIPublicCatalogItem) bool {
			return item.ModelName == current.PublicNameValue() && item.PricingVersion == fmt.Sprintf("ztapi-snapshot-%d", result.ID)
		}) {
			return errors.New("new Xcloud publication is missing from public price projection")
		}
		return check(tx)
	})
	if err != nil {
		return nil, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}

func ztapiXcloudPrepareRoute20261128(tx *gorm.DB, config ZTAPIModelConfig, source *ZTAPIModelPriceSource) error {
	channelID := 0
	for _, id := range []int{4, 5, 6, 7, 8} {
		if group, _ := ZTAPIXcloudChannelGroup20261128(id); group == source.SpendTier {
			channelID = id
		}
	}
	var channel Channel
	if channelID == 0 {
		return errors.New("unconfigured Xcloud quotation group")
	}
	if err := tx.First(&channel, channelID).Error; err != nil {
		return err
	}
	if _, err := ztapiXcloudChannelPhysical20261128(&channel, config.SourceModel, source.SpendTier); err != nil {
		return err
	}
	if !slices.Contains(channel.GetGroups(), "default") {
		return errors.New("selected Xcloud publication group is unavailable")
	}
	if !slices.Contains(channel.GetModels(), config.SourceModel) {
		models := append(channel.GetModels(), config.SourceModel)
		updated := tx.Model(&Channel{}).Where("id = ? AND models = ?", channel.Id, channel.Models).Update("models", strings.Join(models, ","))
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrZTAPIModelVersionConflict
		}
	}
	ability := Ability{Group: "default", Model: config.SourceModel, ChannelId: channel.Id, Enabled: true, Priority: channel.Priority, Weight: uint(channel.GetWeight()), Tag: channel.Tag}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ability).Error; err != nil {
		return err
	}
	var configured Ability
	if err := tx.Where("channel_id = ? AND model = ?", channel.Id, config.SourceModel).Where(map[string]any{"group": "default"}).First(&configured).Error; err != nil {
		return err
	}
	if !configured.Enabled {
		return errors.New("existing selected ability is disabled; it will not be enabled implicitly")
	}
	return nil
}

func RestoreZTAPIXcloudPublication20261128(operator, configID int, version uint64, snapshotID int64, check func(*gorm.DB) error) error {
	if DB == nil || operator <= 0 || configID <= 0 || version == 0 || snapshotID <= 0 || check == nil {
		return errors.New("exact Xcloud rollback baseline and protected cost gate are required")
	}
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var current ZTAPIModelConfig
		if err := tx.First(&current, configID).Error; err != nil {
			return err
		}
		if current.Version != version || current.PublicationSnapshotID != snapshotID || !current.Published {
			return ErrZTAPIModelVersionConflict
		}
		var event ZTAPIAuditEvent
		if err := tx.Where("model_config_id = ? AND version = ? AND action = ?", configID, version, ztapiXcloudPublishAction20261128).First(&event).Error; err != nil {
			return err
		}
		var audit ztapiXcloudPublicationAudit20261128
		if common.UnmarshalJsonStr(event.Payload, &audit) != nil || audit.Original.ID != configID || audit.Original.Version+1 != version || audit.NewSnapshotID != snapshotID ||
			audit.Original.SourceModel != current.SourceModel || audit.Original.PublicNameValue() != current.PublicNameValue() || audit.OriginalGroups != current.EnabledGroups {
			return errors.New("Xcloud rollback audit mismatch")
		}
		audit.Original.EnabledGroups = audit.OriginalGroups
		var active ZTAPIModelPublicationSnapshot
		if err := tx.First(&active, snapshotID).Error; err != nil {
			return err
		}
		if active.ModelConfigID != configID || active.PricePolicy != string(ZTAPIPricePolicyXcloud20261128) {
			return errors.New("active snapshot is not owned by this Xcloud release")
		}
		var latest ZTAPIModelPriceSource
		if err := tx.Where("model_config_id = ?", configID).Order("version DESC, id DESC").First(&latest).Error; err != nil {
			return err
		}
		if latest.ID != active.PriceSourceID {
			return ErrZTAPIModelVersionConflict
		}
		restoredID := int64(0)
		if audit.Original.Published {
			var prior ZTAPIModelPriceSource
			if err := tx.First(&prior, audit.OriginalSourceID).Error; err != nil {
				return err
			}
			prior.ID, prior.Version, prior.CreatedAt, prior.OperatorID = 0, latest.Version+1, common.GetTimestamp(), operator
			if err := tx.Create(&prior).Error; err != nil {
				return err
			}
			var old ZTAPIModelPublicationSnapshot
			if err := tx.First(&old, audit.Original.PublicationSnapshotID).Error; err != nil {
				return err
			}
			if old.ModelConfigID != configID || old.ModelVersion != audit.Original.Version || old.PriceSourceID != audit.OriginalSourceID {
				return errors.New("original publication evidence changed")
			}
			old.ID, old.ModelVersion, old.PriceSourceID, old.CreatedAt = 0, version+1, prior.ID, common.GetTimestamp()
			if err := tx.Create(&old).Error; err != nil {
				return err
			}
			restoredID = old.ID
		}
		if err := ztapiXcloudUpdateConfig20261114(tx, current, audit.Original, restoredID, common.GetTimestamp()); err != nil {
			return err
		}
		if err := check(tx); err != nil {
			return err
		}
		return tx.Create(&ZTAPIAuditEvent{Action: "model.xcloud_20261128_restored", ModelConfigID: configID, PublicName: current.PublicNameValue(), Version: version + 1, OperatorID: operator,
			CreatedAt: common.GetTimestamp(), Payload: common.MapToJsonStr(map[string]any{"replaced_snapshot_id": snapshotID, "restored_snapshot_id": restoredID})}).Error
	})
	if err == nil {
		invalidateZTAPICatalogCaches()
	}
	return err
}
