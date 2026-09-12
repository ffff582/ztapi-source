/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package model

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ztapiCatalogWriteMu sync.Mutex

type ZTAPICatalogLock struct {
	ID int `gorm:"primaryKey"`
}

func (ZTAPICatalogLock) TableName() string { return "ztapi_catalog_locks" }

func ensureZTAPICatalogLockRow() error {
	return DB.FirstOrCreate(&ZTAPICatalogLock{ID: 1}).Error
}

// withZTAPICatalogWrite serializes publication and route mutations in-process,
// and locks the persisted catalog rows so cooperating application instances
// cannot pass alias/source validation against stale catalog state.
func withZTAPICatalogWrite(fn func(tx *gorm.DB) error) error {
	ztapiCatalogWriteMu.Lock()
	defer ztapiCatalogWriteMu.Unlock()

	return DB.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(&ZTAPICatalogLock{}) {
			if err := tx.FirstOrCreate(&ZTAPICatalogLock{ID: 1}).Error; err != nil {
				return err
			}
			var catalogLock ZTAPICatalogLock
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&catalogLock, 1).Error; err != nil {
				return err
			}
		}
		var ids []int
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Model(&ZTAPIModelConfig{}).
			Order("id ASC").
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func countZTAPIReadyRoutesTx(tx *gorm.DB, source string, groups []string) (int64, error) {
	query := tx.Table("abilities").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.model = ? AND abilities.enabled = ? AND channels.status = ?", source, true, common.ChannelStatusEnabled)
	groups = normalizeZTAPIGroups(groups)
	if len(groups) > 0 {
		groupColumn := `abilities."group"`
		if common.UsingMySQL {
			groupColumn = "abilities.`group`"
		}
		query = query.Where(groupColumn+" IN ?", groups)
	}
	var count int64
	err := query.Distinct("abilities.channel_id").Count(&count).Error
	return count, err
}

func validateZTAPIPublicNameClaimTx(tx *gorm.DB, configID int, sourceModel, publicName string) error {
	publicName = strings.TrimSpace(publicName)
	if publicName == "" || publicName == strings.TrimSpace(sourceModel) {
		return nil
	}
	var count int64
	if err := tx.Model(&ZTAPIModelConfig{}).
		Where("id <> ? AND source_model = ?", configID, publicName).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("public alias conflicts with source model %s", publicName)
	}
	if err := tx.Model(&Ability{}).Where("model = ?", publicName).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("public alias conflicts with source model %s", publicName)
	}
	return nil
}

func ztapiPublishedConfigHasReadyRoutesTx(tx *gorm.DB, config *ZTAPIModelConfig) (bool, error) {
	if config.PublicationSnapshotID <= 0 {
		_, err := getZTAPITrustedRouteChannelIDsDB(tx, config.SourceModel, config.Family, config.Groups())
		return err == nil, nil
	}

	var snapshot ZTAPIModelPublicationSnapshot
	if err := tx.First(&snapshot, config.PublicationSnapshotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if snapshot.ModelConfigID != config.ID || snapshot.ModelVersion != config.Version {
		return false, nil
	}

	groups := snapshot.Groups()
	if err := validateZTAPIPublicationGroups(groups); err != nil {
		return false, nil
	}
	allowedChannelIDs := snapshot.ChannelIDs()
	if len(allowedChannelIDs) == 0 {
		return false, nil
	}

	routeConfig := &ZTAPIModelConfig{
		Protocol:       snapshot.Protocol,
		ProviderFamily: snapshot.ProviderFamily,
	}
	groupColumn := `abilities."group"`
	if common.UsingMySQL {
		groupColumn = "abilities.`group`"
	}
	for _, group := range groups {
		var routes []ztapiRouteAuthority
		err := tx.Table("abilities").
			Select("DISTINCT channels.id AS channel_id, channels.type AS channel_type, channels.base_url AS base_url, channels.ztapi_managed AS ztapi_managed, channels.ztapi_family AS ztapi_family").
			Joins("JOIN channels ON channels.id = abilities.channel_id").
			Where("abilities.model = ? AND abilities.enabled = ? AND channels.status = ?", snapshot.SourceModel, true, common.ChannelStatusEnabled).
			Where("channels.id IN ?", allowedChannelIDs).
			Where(groupColumn+" = ?", group).
			Scan(&routes).Error
		if err != nil {
			return false, err
		}
		ready := false
		for _, route := range routes {
			if ztapiRouteMatchesEvidence(route, routeConfig) {
				ready = true
				break
			}
		}
		if !ready {
			return false, nil
		}
	}
	return true, nil
}

// reconcileZTAPIPublishedRoutesTx prevents a legacy channel write from leaving
// a published catalog row without an enabled route. Invalid rows are
// unpublished atomically with the channel mutation and receive a new version.
func reconcileZTAPIPublishedRoutesTx(tx *gorm.DB) (bool, error) {
	var configs []ZTAPIModelConfig
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("published = ?", true).
		Order("id ASC").
		Find(&configs).Error; err != nil {
		return false, err
	}

	changed := false
	now := common.GetTimestamp()
	for i := range configs {
		ready, err := ztapiPublishedConfigHasReadyRoutesTx(tx, &configs[i])
		if err != nil {
			return false, err
		}
		if ready {
			continue
		}
		result := tx.Model(&ZTAPIModelConfig{}).
			Where("id = ? AND published = ? AND version = ?", configs[i].ID, true, configs[i].Version).
			Updates(map[string]interface{}{
				"published":  false,
				"version":    configs[i].Version + 1,
				"updated_at": now,
			})
		if result.Error != nil {
			return false, result.Error
		}
		if result.RowsAffected != 1 {
			return false, ErrZTAPIModelVersionConflict
		}
		changed = true
	}
	return changed, nil
}

func invalidateZTAPICatalogCaches() {
	InvalidatePricingCache()
	InvalidateZTAPIAliasCache()
}

func saveChannelStatusAndReconcile(channel *Channel, updateAbilities bool) error {
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		if err := tx.Omit("key").Save(channel).Error; err != nil {
			return err
		}
		if updateAbilities {
			if err := tx.Model(&Ability{}).
				Where("channel_id = ?", channel.Id).
				Select("enabled").
				Update("enabled", channel.Status == common.ChannelStatusEnabled).Error; err != nil {
				return err
			}
		}
		_, err := reconcileZTAPIPublishedRoutesTx(tx)
		return err
	})
	if err == nil {
		invalidateZTAPICatalogCaches()
	}
	return err
}

// SaveChannelUpstreamModelSettings commits the upstream snapshot and its
// derived abilities as one catalog mutation. A failed ability rebuild cannot
// leave channel.models ahead of the routable catalog.
func SaveChannelUpstreamModelSettings(channel *Channel, updateModels bool) error {
	if channel == nil {
		return fmt.Errorf("channel is nil")
	}
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		return saveChannelUpstreamModelSettingsTx(tx, channel, updateModels)
	})
	if err == nil {
		invalidateZTAPICatalogCaches()
	}
	return err
}

func saveChannelUpstreamModelSettingsTx(tx *gorm.DB, channel *Channel, updateModels bool) error {
	if tx == nil || channel == nil {
		return fmt.Errorf("channel upstream model transaction is not initialized")
	}
	updates := map[string]interface{}{"settings": channel.OtherSettings}
	if updateModels {
		updates["models"] = channel.Models
	}
	if err := tx.Model(&Channel{}).Where("id = ?", channel.Id).Updates(updates).Error; err != nil {
		return err
	}
	if updateModels {
		if err := channel.UpdateAbilities(tx); err != nil {
			return err
		}
	}
	_, err := reconcileZTAPIPublishedRoutesTx(tx)
	return err
}
