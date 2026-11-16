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
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

type ztapiRouteAuthority struct {
	ChannelID   int    `gorm:"column:channel_id"`
	ChannelType int    `gorm:"column:channel_type"`
	BaseURL     string `gorm:"column:base_url"`
	Managed     bool   `gorm:"column:ztapi_managed"`
	Family      string `gorm:"column:ztapi_family"`
}

func validateZTAPIPublicationGroups(groups []string) error {
	groups = normalizeZTAPIGroups(groups)
	if len(groups) == 0 {
		return fmt.Errorf("published ZTAPI model requires at least one group")
	}
	for _, group := range groups {
		if group == "all" {
			return fmt.Errorf("published ZTAPI model requires explicit groups; wildcard all is not allowed")
		}
	}
	return nil
}

func ztapiManagedAdapterSupportsFamily(channelType int, family string) bool {
	switch channelType {
	case constant.ChannelTypeOpenAI:
		return IsSupportedZTAPIModelFamily(family)
	case constant.ChannelTypeAnthropic:
		return family == ZTAPIModelFamilyClaude
	case constant.ChannelTypeGemini:
		return family == ZTAPIModelFamilyGemini
	default:
		return false
	}
}

func trustedZTAPIRouteIDsForGroupDB(db *gorm.DB, sourceModel, family, group string) ([]int, error) {
	nameFamily, supported := InferZTAPIModelFamily(sourceModel)
	if !supported || nameFamily != family {
		return nil, nil
	}

	query := db.Table("abilities").
		Select("DISTINCT channels.id AS channel_id, channels.type AS channel_type, channels.base_url AS base_url, channels.ztapi_managed AS ztapi_managed, channels.ztapi_family AS ztapi_family").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.model = ? AND abilities.enabled = ? AND channels.status = ?", sourceModel, true, common.ChannelStatusEnabled)
	if group != "" && group != "all" {
		groupColumn := `abilities."group"`
		if common.UsingMySQL {
			groupColumn = "abilities.`group`"
		}
		query = query.Where(groupColumn+" = ?", group)
	}

	var routes []ztapiRouteAuthority
	if err := query.Scan(&routes).Error; err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(routes))
	seen := make(map[int]struct{}, len(routes))
	for _, route := range routes {
		trusted := route.Managed && ztapiManagedAdapterSupportsFamily(route.ChannelType, family)
		if !trusted {
			routeFamily, ok := ztapiFamilyForTrustedRoute(route.ChannelType, route.BaseURL, false, route.Family)
			trusted = ok && routeFamily == family
		}
		if !trusted {
			continue
		}
		if _, exists := seen[route.ChannelID]; exists {
			continue
		}
		seen[route.ChannelID] = struct{}{}
		ids = append(ids, route.ChannelID)
	}
	sort.Ints(ids)
	return ids, nil
}

func getZTAPITrustedRouteChannelIDsDB(db *gorm.DB, sourceModel, family string, groups []string) ([]int, error) {
	groups = normalizeZTAPIGroups(groups)
	if err := validateZTAPIPublicationGroups(groups); err != nil {
		return nil, err
	}

	allIDs := make(map[int]struct{})
	for _, group := range groups {
		ids, err := trustedZTAPIRouteIDsForGroupDB(db, sourceModel, family, group)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("published ZTAPI model has no trusted route for group %s", group)
		}
		for _, channelID := range ids {
			allIDs[channelID] = struct{}{}
		}
	}
	ids := make([]int, 0, len(allIDs))
	for channelID := range allIDs {
		ids = append(ids, channelID)
	}
	sort.Ints(ids)
	return ids, nil
}

// GetZTAPITrustedRouteChannelIDs validates every explicitly published group
// and returns the server-authorized channel set for the request snapshot.
func GetZTAPITrustedRouteChannelIDs(sourceModel, family string, groups []string) ([]int, error) {
	if DB == nil {
		return nil, fmt.Errorf("ZTAPI database is not initialized")
	}
	var config ZTAPIModelConfig
	if err := DB.Where("source_model = ?", sourceModel).First(&config).Error; err == nil &&
		config.Published && config.PublicationSnapshotID > 0 {
		var snapshot ZTAPIModelPublicationSnapshot
		if err := DB.First(&snapshot, config.PublicationSnapshotID).Error; err != nil {
			return nil, fmt.Errorf("load ZTAPI publication snapshot: %w", err)
		}
		requestedGroups := normalizeZTAPIGroups(groups)
		var frozenGroups []string
		if err := common.Unmarshal([]byte(snapshot.EnabledGroups), &frozenGroups); err != nil {
			return nil, fmt.Errorf("decode ZTAPI publication groups: %w", err)
		}
		frozenSet := make(map[string]struct{}, len(frozenGroups))
		for _, group := range normalizeZTAPIGroups(frozenGroups) {
			frozenSet[group] = struct{}{}
		}
		for _, group := range requestedGroups {
			if _, ok := frozenSet[group]; !ok {
				return nil, fmt.Errorf("group %s is not present in the publication snapshot", group)
			}
		}
		ids := snapshot.ChannelIDs()
		if len(ids) == 0 {
			return nil, fmt.Errorf("publication snapshot has no allowed channels")
		}
		return ids, nil
	}
	return getZTAPITrustedRouteChannelIDsDB(DB, sourceModel, family, groups)
}
