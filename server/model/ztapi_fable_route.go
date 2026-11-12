package model

import (
	"errors"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

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
