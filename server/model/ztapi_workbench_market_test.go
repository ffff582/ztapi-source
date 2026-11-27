package model

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIWorkbenchModelSignals(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	require.NoError(t, db.AutoMigrate(&ZTAPIHealthState{}, &ZTAPIHealthEvent{}))
	now := time.Unix(1795760000, 0)
	publications := []ZTAPIRuntimePublication{
		{ModelConfigID: 11, Version: 2, PublicName: "zt-green", AllowedChannelIDs: []int{5}},
		{ModelConfigID: 12, Version: 2, PublicName: "zt-red", AllowedChannelIDs: []int{5}},
		{ModelConfigID: 13, Version: 2, PublicName: "zt-unknown", AllowedChannelIDs: []int{5}},
		{ModelConfigID: 14, Version: 2, PublicName: "zt-new-problem", AllowedChannelIDs: []int{5}},
	}
	require.NoError(t, db.Create(&[]ZTAPIHealthState{{ModelID: 11, Generation: 3}, {ModelID: 12, Generation: 3, Open: true}, {ModelID: 13, Generation: 3}, {ModelID: 14, Generation: 3}}).Error)
	event := func(id int, name, execution string, seq int) ZTAPIHealthEvent {
		return ZTAPIHealthEvent{ExecutionID: execution, ModelID: id, PublicModel: name, ConfigVersion: 2, Generation: 3, ChannelID: 5,
			CompletedAt: now.Unix() - 150, CompletionSequence: uint64(seq), Source: "real", Result: "success", ResultValid: true}
	}
	green, red := event(11, "zt-green", "good", 1), event(12, "zt-red", "tripped", 1)
	old, wrong, stale, removed, suspect := event(13, "zt-unknown", "old", 1), event(13, "zt-unknown", "version", 2),
		event(13, "zt-unknown", "stale", 3), event(13, "zt-unknown", "route", 4), event(13, "zt-unknown", "suspect", 5)
	old.CompletedAt = now.Add(-2 * time.Hour).Unix()
	wrong.ConfigVersion = 1
	stale.Generation = 2
	removed.ChannelID = 7
	suspect.Result = "suspected"
	beforeProblem, problem := event(14, "zt-new-problem", "before-problem", 1), event(14, "zt-new-problem", "after-success", 2)
	problem.Result = "suspected"
	problem.CompletedAt++
	require.NoError(t, db.Create(&[]ZTAPIHealthEvent{green, red, old, wrong, stale, removed, suspect, beforeProblem, problem}).Error)
	got := workbenchModelSignals(context.Background(), db, publications, now)
	require.Equal(t, map[string]string{"zt-green": "available", "zt-red": "unavailable"}, got)
}
