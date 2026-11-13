// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXcloudAbilityPriorityWinsAndYunxinRemainsFallback(t *testing.T) {
	db := setupZTAPIChannelInvariantDB(t)
	modelName := "claude-opus-4-8"
	weight := uint(100)
	primaryPriority := int64(30)
	backupPriority := int64(20)

	primary := Channel{Name: "xcloud-primary", Models: modelName, Group: "default", Status: 1, Weight: &weight, Priority: &primaryPriority}
	backup := Channel{Name: "yunxin-backup", Models: modelName, Group: "default", Status: 1, Weight: &weight, Priority: &backupPriority}
	require.NoError(t, db.Create(&primary).Error)
	require.NoError(t, db.Create(&backup).Error)
	require.NoError(t, db.Create(&Ability{Group: "default", Model: modelName, ChannelId: primary.Id, Enabled: true, Priority: &primaryPriority, Weight: weight}).Error)
	require.NoError(t, db.Create(&Ability{Group: "default", Model: modelName, ChannelId: backup.Id, Enabled: true, Priority: &backupPriority, Weight: weight}).Error)

	selected, err := GetChannel("default", modelName, 0)
	require.NoError(t, err)
	require.Equal(t, primary.Id, selected.Id)

	require.NoError(t, db.Model(&Ability{}).Where("channel_id = ?", primary.Id).Update("enabled", false).Error)
	selected, err = GetChannel("default", modelName, 0)
	require.NoError(t, err)
	require.Equal(t, backup.Id, selected.Id)
}
