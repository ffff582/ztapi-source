package model

import (
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func ValidateZTAPIFablePublishedRoute(channelID int, baseURL, physical string, allowed []int) error {
	baseURL = strings.TrimRight(baseURL, "/")
	if slices.Contains(allowed, channelID) &&
		((channelID == 4 && baseURL == "https://www.xcloudtoken.com" && physical == "claude-fable-5") ||
			(channelID == 2 && baseURL == "https://ai.yunxinapi.com/hub" && physical == "claude-fable-5-hc")) {
		return nil
	}
	return errors.New("fable_published_supplier_route_mismatch")
}

// Bind new probe evidence to the exact single-credential route without retaining a key.
func ZTAPIFableVerificationRouteFingerprint(channel *Channel, physical string) (string, error) {
	if channel == nil || channel.ChannelInfo.IsMultiKey ||
		ValidateZTAPIFablePublishedRoute(channel.Id, channel.GetBaseURL(), physical, []int{2, 4}) != nil {
		return "", errors.New("Fable verification requires an approved single-credential supplier route")
	}
	sealed, err := common.Marshal([]any{channel.Id, channel.Type, channel.GetBaseURL(), physical, channel.GetModelMapping(),
		channel.Key, channel.ZTAPIKeyCiphertext, channel.HeaderOverride, channel.ParamOverride, channel.Setting, channel.OtherSettings})
	if err != nil {
		return "", err
	}
	fingerprint, err := FingerprintZTAPICredential(string(sealed))
	return fingerprint.String(), err
}

// Narrow one immutable publication; no price, credential or wallet mutation.
func PublishZTAPIFableXcloudOnly(operatorID int, expectedVersion uint64) (*ZTAPIModelPublicationSnapshot, error) {
	if DB == nil || operatorID <= 0 || expectedVersion == 0 {
		return nil, errors.New("operator and expected version required")
	}
	var result *ZTAPIModelPublicationSnapshot
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var config ZTAPIModelConfig
		if err := tx.Where("source_model = ? AND public_name = ? AND published = ?", "claude-fable-5", "zt-claude-fable-5", true).First(&config).Error; err != nil {
			return err
		}
		if config.Version != expectedVersion {
			return ErrZTAPIModelVersionConflict
		}
		var previous ZTAPIModelPublicationSnapshot
		if err := tx.First(&previous, config.PublicationSnapshotID).Error; err != nil {
			return err
		}
		if previous.ModelVersion != config.Version || previous.SourceModel != config.SourceModel || previous.PublicName != config.PublicNameValue() || !isZTAPIDatedRetailPolicy20261107(previous.PricePolicy) {
			return errors.New("Fable publication no longer matches the reviewed version")
		}
		if reflect.DeepEqual(previous.ChannelIDs(), []int{4}) {
			result = &previous
			return nil
		}
		if !reflect.DeepEqual(previous.ChannelIDs(), []int{2, 4}) {
			return errors.New("Fable restriction only permits the reviewed 2,4 to 4 narrowing")
		}
		next := previous
		next.ID, next.ModelVersion = 0, config.Version+1
		next.AllowedChannelIDs, next.CreatedAt = "[4]", common.GetTimestamp()
		if err := tx.Create(&next).Error; err != nil {
			return err
		}
		updated := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ? AND publication_snapshot_id = ?", config.ID, config.Version, previous.ID).
			Updates(map[string]any{"publication_snapshot_id": next.ID, "version": next.ModelVersion, "updated_at": next.CreatedAt})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrZTAPIModelVersionConflict
		}
		if err := tx.Create(&ZTAPIAuditEvent{Action: "model.fable_xcloud_only_published", ModelConfigID: config.ID, PublicName: next.PublicName,
			Version: next.ModelVersion, OperatorID: operatorID, CreatedAt: next.CreatedAt,
			Payload: common.MapToJsonStr(map[string]any{"previous_snapshot_id": previous.ID, "snapshot_id": next.ID, "allowed_channels": []int{4}, "prices_unchanged": true,
				"reason": "owner-approved 2026-11-08 removal of unconfirmed Yunxin Fable fallback cost"})}).Error; err != nil {
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

// RestoreZTAPIFableYunxinBackup only widens the reviewed Fable 5 snapshot.
// The operator must validate the prospective protected cost proof in check.
func RestoreZTAPIFableYunxinBackup(operator int, version uint64, snapshotID, primaryID, backupID int64, check func(*gorm.DB) error) (*ZTAPIModelPublicationSnapshot, error) {
	if DB == nil || operator <= 0 || version == 0 || snapshotID <= 0 || primaryID <= 0 || backupID <= 0 || primaryID == backupID || check == nil {
		return nil, errors.New("Fable backup requires explicit baseline, route evidence, and cost proof")
	}
	const source = "claude-fable-5"
	var result *ZTAPIModelPublicationSnapshot
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var config ZTAPIModelConfig
		if err := tx.Where("source_model = ? AND public_name = ? AND published = ?", source, "zt-claude-fable-5", true).First(&config).Error; err != nil {
			return err
		}
		if config.Version != version || config.PublicationSnapshotID != snapshotID {
			return ErrZTAPIModelVersionConflict
		}
		var previous ZTAPIModelPublicationSnapshot
		if err := tx.First(&previous, snapshotID).Error; err != nil {
			return err
		}
		if previous.ModelConfigID != config.ID || previous.ModelVersion != version || previous.SourceModel != source ||
			previous.PublicName != config.PublicNameValue() || previous.Protocol != config.Protocol ||
			previous.ProviderFamily != config.ProviderFamily || previous.EnabledGroups != config.EnabledGroups ||
			!isZTAPIDatedRetailPolicy20261107(previous.PricePolicy) || previous.CacheWritePricingMode != ZTAPIFableCashFloorCacheWriteMode ||
			!slices.Equal(previous.ChannelIDs(), []int{4}) {
			return errors.New("Fable backup requires the current Xcloud-only cash-floor snapshot")
		}
		mapped, err := ztapiMappedUpstreamModelByChannelTx(tx, source, true)
		if err != nil {
			return err
		}
		priorities := make(map[int]int64, 2)
		for _, route := range []struct {
			id             int
			verificationID int64
		}{
			{4, primaryID},
			{2, backupID},
		} {
			var channel Channel
			if err := tx.First(&channel, route.id).Error; err != nil {
				return err
			}
			physical := mapped[route.id]
			if physical == "" {
				physical = source
			}
			if !channel.ZTAPIManaged || channel.Status != common.ChannelStatusEnabled ||
				!slices.Contains(channel.GetModels(), source) ||
				ValidateZTAPIFablePublishedRoute(route.id, channel.GetBaseURL(), physical, []int{2, 4}) != nil {
				return errors.New("Fable backup route does not match the reviewed supplier model")
			}
			priorities[route.id] = channel.GetPriority()
			var v ZTAPIModelVerification
			if err := tx.Where("model_config_id = ? AND channel_id = ?", config.ID, route.id).
				Order("verified_at DESC, id DESC").First(&v).Error; err != nil {
				return err
			}
			fingerprint, err := ZTAPIFableVerificationRouteFingerprint(&channel, physical)
			if err != nil || v.FableRouteFingerprint == "" || v.FableRouteFingerprint != fingerprint ||
				v.ID != route.verificationID || v.VerifiedAt > common.GetTimestamp() || !ztapiXcloudCohortVerified(v, config, route.id) {
				return errors.New("latest Fable route verification does not match")
			}
		}
		if priorities[4] <= priorities[2] {
			return errors.New("Xcloud must outrank the Yunxin backup")
		}
		blockers, evidence, err := ztapiPublicationBlockersTx(tx, &config)
		if err != nil {
			return err
		}
		if len(blockers) > 0 {
			return &ZTAPIPublicationBlockedError{Blockers: blockers}
		}
		if evidence.PriceSourceID != previous.PriceSourceID || !slices.Equal(evidence.AllowedChannelIDs, []int{2, 4}) ||
			!slices.Equal(evidence.VerificationIDs, uniqueSortedInt64s([]int64{primaryID, backupID})) {
			return errors.New("publication gates did not approve both exact Fable routes on the current quote")
		}
		var price ZTAPIModelPriceSource
		if err := tx.First(&price, previous.PriceSourceID).Error; err != nil {
			return err
		}
		if price.PricePolicy != previous.PricePolicy || ValidateZTAPIPriceReductionRoute20261107(&price, previous.PublicName, evidence.AllowedChannelIDs) != nil {
			return errors.New("Fable backup is not covered by the frozen price basis")
		}
		verified, err := common.Marshal(evidence.VerificationIDs)
		if err != nil {
			return err
		}
		next := previous
		next.ID, next.ModelVersion, next.CreatedAt = 0, version+1, common.GetTimestamp()
		next.AllowedChannelIDs, next.VerificationIDs, next.IdentityUpdatedAt = "[2,4]", string(verified), evidence.IdentityUpdatedAt
		if err := tx.Create(&next).Error; err != nil {
			return err
		}
		if err := ztapiXcloudUpdateConfig20261114(tx, config, config, next.ID, next.CreatedAt); err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIAuditEvent{Action: "model.fable_yunxin_backup_restored", ModelConfigID: config.ID, PublicName: next.PublicName,
			Version: next.ModelVersion, OperatorID: operator, CreatedAt: next.CreatedAt,
			Payload: common.MapToJsonStr(map[string]any{"previous_snapshot_id": previous.ID, "snapshot_id": next.ID,
				"verification_ids": evidence.VerificationIDs, "allowed_channels": evidence.AllowedChannelIDs, "prices_unchanged": true})}).Error; err != nil {
			return err
		}
		if err := check(tx); err != nil {
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
