package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestManagedChannelSelectionExcludesBeforePriority(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint(cached), func(t *testing.T) {
			db := setupZTAPIChannelInvariantDB(t)
			oldCache, oldPostgres := common.MemoryCacheEnabled, common.UsingPostgreSQL
			oldGroup, oldKey, oldTrue, oldFalse := commonGroupCol, commonKeyCol, commonTrueVal, commonFalseVal
			oldLogGroup, oldLogKey := logGroupCol, logKeyCol
			channelSyncLock.Lock()
			oldIndex, oldChannels := group2model2channels, channelsIDM
			channelSyncLock.Unlock()
			t.Cleanup(func() {
				common.MemoryCacheEnabled, common.UsingPostgreSQL = oldCache, oldPostgres
				commonGroupCol, commonKeyCol, commonTrueVal, commonFalseVal = oldGroup, oldKey, oldTrue, oldFalse
				logGroupCol, logKeyCol = oldLogGroup, oldLogKey
				channelSyncLock.Lock()
				group2model2channels, channelsIDM = oldIndex, oldChannels
				channelSyncLock.Unlock()
			})
			common.MemoryCacheEnabled, common.UsingPostgreSQL = cached, false
			initCol()
			ids := make([]int, 0, 4)
			for _, priority := range []int64{100, 20, 20, 10} {
				channel := newZTAPITestChannel("gpt-5.5", "default")
				channel.Priority = common.GetPointer(priority)
				require.NoError(t, db.Create(&channel).Error)
				require.NoError(t, db.Create(&Ability{Group: "default", Model: "gpt-5.5", ChannelId: channel.Id, Enabled: true, Priority: channel.Priority, Weight: 100}).Error)
				ids = append(ids, channel.Id)
			}
			InitChannelCache()
			allowed := ids[1:]
			first, err := GetUntriedSatisfiedChannel("default", "gpt-5.5", allowed, nil)
			require.NoError(t, err)
			require.NotNil(t, first)
			require.Contains(t, ids[1:3], first.Id)
			second, err := GetUntriedSatisfiedChannel("default", "gpt-5.5", allowed, []int{first.Id})
			require.NoError(t, err)
			require.NotNil(t, second)
			require.Contains(t, ids[1:3], second.Id)
			require.NotEqual(t, first.Id, second.Id)
			third, err := GetUntriedSatisfiedChannel("default", "gpt-5.5", allowed, []int{first.Id, second.Id})
			require.NoError(t, err)
			require.Equal(t, ids[3], third.Id)
			for _, authority := range [][]int{nil, {}, allowed} {
				none, err := GetUntriedSatisfiedChannel("default", "gpt-5.5", authority, allowed)
				require.NoError(t, err)
				require.Nil(t, none, "exhausted authority must not select the unauthorized highest priority")
			}
		})
	}
}
