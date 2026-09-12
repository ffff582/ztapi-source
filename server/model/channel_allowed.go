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
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// GetChannelWithAllowedIDs selects only from the publication-authorized
// channels. An empty allowlist preserves the legacy unrestricted behavior.
func GetChannelWithAllowedIDs(group string, modelName string, retry int, allowedChannelIDs []int) (*Channel, error) {
	if len(allowedChannelIDs) == 0 {
		return GetChannel(group, modelName, retry)
	}

	newQuery := func() *gorm.DB {
		return DB.Model(&Ability{}).
			Where(commonGroupCol+" = ? AND model = ? AND enabled = ? AND channel_id IN ?", group, modelName, true, allowedChannelIDs)
	}
	var priorities []int64
	if err := newQuery().Distinct("priority").Order("priority DESC").Pluck("priority", &priorities).Error; err != nil {
		return nil, err
	}
	if len(priorities) == 0 {
		return nil, nil
	}
	if retry >= len(priorities) {
		retry = len(priorities) - 1
	}

	var abilities []Ability
	if err := newQuery().Where("priority = ?", priorities[retry]).Order("weight DESC").Find(&abilities).Error; err != nil {
		return nil, err
	}
	if len(abilities) == 0 {
		return nil, nil
	}

	weightSum := uint(0)
	for _, ability := range abilities {
		weightSum += ability.Weight + 10
	}
	weight := common.GetRandomInt(int(weightSum))
	channelID := 0
	for _, ability := range abilities {
		weight -= int(ability.Weight) + 10
		if weight <= 0 {
			channelID = ability.ChannelId
			break
		}
	}
	if channelID == 0 {
		return nil, nil
	}
	var channel Channel
	if err := DB.First(&channel, "id = ?", channelID).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}
