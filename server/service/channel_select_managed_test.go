package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestManagedRetryBudgetSurvivesGroupCounterReset(t *testing.T) {
	p := &RetryParam{Managed: true, AllowedChannelIDs: []int{17, 29, 41}}
	require.Equal(t, 1, p.RetryLimit())
	require.Error(t, p.RecordAttempt(99))
	require.NoError(t, p.RecordAttempt(17))
	require.Error(t, p.RecordAttempt(17))
	p.IncreaseRetry()
	p.SetRetry(0)
	p.ResetRetryNextTry()
	p.IncreaseRetry()
	require.Zero(t, p.GetRetry())
	require.True(t, p.HasMoreAttempts())
	require.NoError(t, p.RecordAttempt(29))
	p.SetRetry(0)
	require.False(t, p.HasMoreAttempts())
	require.Error(t, p.RecordAttempt(41))
	require.Equal(t, []int{17, 29}, p.AttemptedChannelIDs)
}

func TestManagedRetryCanReleaseLocallyRejectedCredentialAttempt(t *testing.T) {
	p := &RetryParam{Managed: true, AllowedChannelIDs: []int{17, 29}}
	require.NoError(t, p.RecordAttempt(17))
	p.ReleaseAttempt(17)
	require.Empty(t, p.AttemptedChannelIDs)
	require.True(t, p.HasMoreAttempts())
	require.NoError(t, p.RecordAttempt(17))
}

func TestManagedRetryAutoSelectionErrorIsFailClosedWithoutChangingLegacy(t *testing.T) {
	oldDB, oldCache := model.DB, common.MemoryCacheEnabled
	oldGroups := setting.AutoGroups2JsonString()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	model.DB, common.MemoryCacheEnabled = db, false
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default"]`))
	t.Cleanup(func() {
		model.DB, common.MemoryCacheEnabled = oldDB, oldCache
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(oldGroups))
		_ = sqlDB.Close()
	})
	for _, managed := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		channel, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: c, TokenGroup: "auto", ModelName: "gpt-5.5", Managed: managed, AllowedGroups: []string{"default"}, AllowedChannelIDs: []int{17}})
		require.Nil(t, channel)
		if managed {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestManagedRetryEmptyAndLegacyAuthority(t *testing.T) {
	for _, ids := range [][]int{nil, {}, {0, -1}} {
		p := &RetryParam{Managed: true, AllowedChannelIDs: ids}
		require.False(t, p.HasMoreAttempts())
		require.Error(t, p.RecordAttempt(17))
	}
	p := &RetryParam{}
	require.Equal(t, common.RetryTimes, p.RetryLimit())
	for i := 0; i < 5; i++ {
		require.NoError(t, p.RecordAttempt(17))
		require.True(t, p.HasMoreAttempts())
	}
	require.Empty(t, p.AttemptedChannelIDs)
}
