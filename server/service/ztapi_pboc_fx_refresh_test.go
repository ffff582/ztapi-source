package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupZTAPIFXRefreshTest(t *testing.T, source, rate, rateDate string) *gorm.DB {
	t.Helper()
	originalDB := model.DB
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.ZTAPIFXPolicy{}))
	model.DB = db
	t.Cleanup(func() { model.DB = originalDB })
	root := model.User{Username: "root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&root).Error)
	_, err = model.CreateZTAPIFXPolicy(model.ZTAPIFXPolicyInput{
		MarketCNYPerUSDT: "6.66", StopLossCNY: "0.03", UpstreamCNYPerUSD: rate,
		UpstreamSource: source, UpstreamRateDate: rateDate,
		MarketSource: model.ZTAPIFXMarketSourceOKXAlipay,
		Reason:       "seed", OperatorID: root.Id,
	})
	require.NoError(t, err)
	return db
}

func TestRefreshZTAPIFXRateStoresNewPBOCRateAndReprices(t *testing.T) {
	db := setupZTAPIFXRefreshTest(t, model.ZTAPIFXUpstreamSourcePBOCMid, "6.7521", "2026-09-18")
	repriced := 0
	result, err := runZTAPIFXRefreshOnce(
		context.Background(),
		func(context.Context) (*ZTAPIPBOCMidRate, error) {
			return &ZTAPIPBOCMidRate{CNYPerUSD: "6.7654", RateDate: "2026-09-21", Source: model.ZTAPIFXUpstreamSourcePBOCMid}, nil
		},
		func(int) error { repriced++; return nil },
	)
	require.NoError(t, err)
	require.True(t, result.Updated)
	require.Equal(t, 1, repriced)

	policy, err := model.CurrentZTAPIFXPolicy(db)
	require.NoError(t, err)
	storedRate, err := decimal.NewFromString(policy.UpstreamCNYPerUSD)
	require.NoError(t, err)
	require.True(t, storedRate.Equal(decimal.RequireFromString("6.7654")))
	require.Equal(t, "2026-09-21", policy.UpstreamRateDate)
}

func TestRefreshZTAPIFXRateNeverOverwritesManualPolicy(t *testing.T) {
	db := setupZTAPIFXRefreshTest(t, model.ZTAPIFXUpstreamSourceManual, "6.8000", "2026-09-19")
	fetched, repriced := 0, 0
	result, err := runZTAPIFXRefreshOnce(
		context.Background(),
		func(context.Context) (*ZTAPIPBOCMidRate, error) { fetched++; return nil, nil },
		func(int) error { repriced++; return nil },
	)
	require.NoError(t, err)
	require.True(t, result.SkippedManual)
	require.Zero(t, fetched)
	require.Zero(t, repriced)
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIFXPolicy{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestNextZTAPIFXRefreshRunsAtTenBeijingAndRetriesFailures(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Date(2026, time.September, 19, 11, 30, 0, 0, location)
	require.Equal(t, time.Date(2026, time.September, 20, 10, 0, 0, 0, location), nextZTAPIFXRefresh(now, nil))
	require.Equal(t, now.Add(time.Hour), nextZTAPIFXRefresh(now, context.DeadlineExceeded))
}
