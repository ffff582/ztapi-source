package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"os"
	"strings"
	"sync"
	"testing"
)

func referralFloorFixture20261107(t *testing.T) (*gorm.DB, []ZTAPIPriceReductionSelection20261107) {
	t.Helper()
	db := reductionFixture20261107(t)
	_, err := ApplyZTAPIPriceReduction20261107(9, ApprovedZTAPIPriceReductionSelections20261107(), false)
	require.NoError(t, err)
	items := []ZTAPIPriceReductionSelection20261107{}
	for _, id := range ztapiReductionSortedIDs20261107() {
		b, _, _, _, err := ztapiReductionApproval20261107(id)
		require.NoError(t, err)
		if !ztapiReferralFloorModel20261107(b.SourceModel) {
			continue
		}
		var c ZTAPIModelConfig
		var p ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&c, id).Error)
		require.NoError(t, db.First(&p, c.PublicationSnapshotID).Error)
		items = append(items, ZTAPIPriceReductionSelection20261107{ModelID: id, Version: c.Version, SnapshotID: p.ID, SourceID: p.PriceSourceID})
	}
	require.Len(t, items, 7)
	return db, items
}

func TestReferralFloor20261107RestorePreservesInflightAndBlocksBinaryDowngrade(t *testing.T) {
	db, items := referralFloorFixture20261107(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIRequestSettlement{}))
	_, err := ApplyZTAPIReferralFloor20261107(9, items, false)
	require.NoError(t, err)
	status, err := CheckZTAPIReferralFloorDowngrade20261107()
	require.NoError(t, err)
	require.False(t, status.CanDowngrade)
	require.Len(t, status.ActiveModels, 7)
	restore := []ZTAPIPriceReductionSelection20261107{}
	for _, s := range items {
		var c ZTAPIModelConfig
		var p ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&c, s.ModelID).Error)
		require.NoError(t, db.First(&p, c.PublicationSnapshotID).Error)
		restore = append(restore, ZTAPIPriceReductionSelection20261107{ModelID: s.ModelID, Version: c.Version, SnapshotID: p.ID, SourceID: p.PriceSourceID, RestoreSnapshotID: s.SnapshotID})
	}
	raw, err := common.Marshal(map[string]any{"PricePolicy": string(ZTAPIPricePolicyReferralFloor20261107), "PriceSourceID": restore[0].SourceID})
	require.NoError(t, err)
	inflight := ZTAPIRequestSettlement{OperationID: "referral-inflight", RequestID: "referral-inflight", PublicModel: "zt-deepseek-v4-pro", Status: ZTAPISettlementPending, PriceSnapshotJSON: string(raw), UsageJSON: `{}`, ChargeDimensionsJSON: `[]`, MissingDimensionsJSON: `[]`}
	require.NoError(t, db.Create(&inflight).Error)
	require.NoError(t, db.First(&inflight, inflight.ID).Error)
	result, err := ApplyZTAPIReferralFloor20261107(9, restore, true)
	require.NoError(t, err)
	require.Equal(t, 7, result.Republished)
	result, err = ApplyZTAPIReferralFloor20261107(9, restore, true)
	require.NoError(t, err)
	require.Equal(t, 7, result.Unchanged)
	for _, s := range items {
		var c ZTAPIModelConfig
		var p, old ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&c, s.ModelID).Error)
		require.NoError(t, db.First(&p, c.PublicationSnapshotID).Error)
		require.NoError(t, db.First(&old, s.SnapshotID).Error)
		require.Equal(t, old.TokenPriceRulesJSON, p.TokenPriceRulesJSON)
		require.Equal(t, old.MediaPriceContractJSON, p.MediaPriceContractJSON)
		require.Equal(t, old.InputPricePerMillion, p.InputPricePerMillion)
		require.Equal(t, old.OutputPricePerMillion, p.OutputPricePerMillion)
	}
	var after ZTAPIRequestSettlement
	require.NoError(t, db.First(&after, inflight.ID).Error)
	require.Equal(t, inflight, after)
	status, err = CheckZTAPIReferralFloorDowngrade20261107()
	require.NoError(t, err)
	require.False(t, status.CanDowngrade)
	require.Empty(t, status.ActiveModels)
	require.Equal(t, []uint{inflight.ID}, status.UnsettledRequestIDs)
	require.NoError(t, db.Model(&inflight).Update("status", ZTAPISettlementSettled).Error)
	legacyRaw, err := common.Marshal(map[string]any{"PricePolicy": string(ZTAPIPricePolicyReduction20261107), "PriceSourceID": items[0].SourceID})
	require.NoError(t, err)
	legacy := ZTAPIRequestSettlement{OperationID: "referral-compatible-legacy", RequestID: "referral-compatible-legacy", PublicModel: "zt-deepseek-v4-pro", Status: ZTAPISettlementPending, PriceSnapshotJSON: string(legacyRaw), UsageJSON: `{}`, ChargeDimensionsJSON: `[]`, MissingDimensionsJSON: `[]`}
	require.NoError(t, db.Create(&legacy).Error)
	status, err = CheckZTAPIReferralFloorDowngrade20261107()
	require.NoError(t, err)
	require.True(t, status.CanDowngrade, "baseline e333 understands existing reduction policy")
}

func TestZTAPICommercialMySQLReferralFloor20261107ConcurrentIdempotency(t *testing.T) {
	required := strings.EqualFold(strings.TrimSpace(os.Getenv("ZTAPI_REQUIRE_COMMERCIAL_MYSQL")), "true")
	safe, err := commercialMySQLTestDSN(os.Getenv("ZTAPI_COMMERCIAL_MYSQL_TEST_DSN"), required)
	require.NoError(t, err)
	if safe == "" {
		t.Skip("dedicated loopback commercial MySQL DSN absent; referral concurrency NOT executed")
	}
	fixture, items := referralFloorFixture20261107(t)
	var configs []ZTAPIModelConfig
	var sources []ZTAPIModelPriceSource
	var snapshots []ZTAPIModelPublicationSnapshot
	var audits []ZTAPIAuditEvent
	require.NoError(t, fixture.Find(&configs).Error)
	require.NoError(t, fixture.Find(&sources).Error)
	require.NoError(t, fixture.Find(&snapshots).Error)
	require.NoError(t, fixture.Find(&audits).Error)
	db, err := gorm.Open(gormmysql.Open(safe), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(16)
	previousDB, previousRedis := DB, common.RedisEnabled
	DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		_ = sqlDB.Close()
		DB = previousDB
		common.RedisEnabled = previousRedis
		InvalidateZTAPIAliasCache()
	})
	// This guard permits only the existing dedicated CI database, never production.
	require.NoError(t, db.Migrator().DropTable(&ZTAPIModelPublicationSnapshot{}, &ZTAPIModelPriceSource{}, &ZTAPIModelConfig{}, &ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIModelPriceSource{}, &ZTAPIModelPublicationSnapshot{}, &ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	seed := db.Session(&gorm.Session{SkipHooks: true})
	require.NoError(t, seed.Create(&configs).Error)
	require.NoError(t, seed.Create(&sources).Error)
	require.NoError(t, seed.Create(&snapshots).Error)
	require.NoError(t, seed.Create(&audits).Error)
	var wg sync.WaitGroup
	results := make(chan ZTAPICommercialRepricingResult, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := ApplyZTAPIReferralFloor20261107(9, items, false)
			if e != nil {
				failures <- e
			} else {
				results <- r
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for failure := range failures {
		require.NoError(t, failure)
	}
	published := 0
	for r := range results {
		published += r.Republished
		require.Equal(t, 7, r.Republished+r.Unchanged)
	}
	require.Equal(t, 7, published)
	var count int64
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Where("action = ?", ztapiReferralFloorAction20261107(false)).Count(&count).Error)
	require.EqualValues(t, 7, count)
}

func TestReferralFloor20261107ExactThirtyPricesAndProcurementUnchanged(t *testing.T) {
	db, items := referralFloorFixture20261107(t)
	preview, err := PreviewZTAPIReferralFloor20261107(items, false)
	require.NoError(t, err)
	require.Len(t, preview, 7)
	count := 0
	for _, p := range preview {
		for _, r := range p.Dimensions {
			count++
			cost, err := ztapiReductionRowCost20261107(r)
			require.NoError(t, err)
			want, err := ztapiReductionSale20261107(cost, "0.15")
			require.NoError(t, err)
			require.Equal(t, want, r.ProposedSale)
			require.True(t, decimal.RequireFromString(want).GreaterThan(decimal.RequireFromString(r.OldSale)))
		}
		require.Equal(t, "0.15", p.TargetMargin)
	}
	require.Equal(t, 30, count)
	beforeSources := []ZTAPIModelPriceSource{}
	beforeSnapshots := []ZTAPIModelPublicationSnapshot{}
	configs := []ZTAPIModelConfig{}
	require.NoError(t, db.Order("id").Find(&beforeSources).Error)
	require.NoError(t, db.Order("id").Find(&beforeSnapshots).Error)
	require.NoError(t, db.Order("id").Find(&configs).Error)
	result, err := ApplyZTAPIReferralFloor20261107(9, items, false)
	require.NoError(t, err)
	require.Equal(t, 7, result.Republished)
	for _, s := range beforeSources {
		var got ZTAPIModelPriceSource
		require.NoError(t, db.First(&got, s.ID).Error)
		require.Equal(t, s, got)
	}
	for _, s := range beforeSnapshots {
		var got ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&got, s.ID).Error)
		require.Equal(t, s, got)
	}
	for _, c := range configs {
		var got ZTAPIModelConfig
		require.NoError(t, db.First(&got, c.ID).Error)
		if !ztapiReferralFloorModel20261107(c.SourceModel) {
			require.Equal(t, c, got)
			continue
		}
		require.True(t, got.Published)
		require.Equal(t, c.EnabledGroups, got.EnabledGroups)
		require.Equal(t, c.Version+1, got.Version)
		var snap ZTAPIModelPublicationSnapshot
		var src ZTAPIModelPriceSource
		require.NoError(t, db.First(&snap, got.PublicationSnapshotID).Error)
		require.NoError(t, db.First(&src, snap.PriceSourceID).Error)
		require.Equal(t, string(ZTAPIPricePolicyReferralFloor20261107), src.PricePolicy)
		require.NoError(t, ValidateZTAPIPriceReductionSource20261107(db, &src))
		var old ZTAPIModelPriceSource
		require.NoError(t, db.First(&old, src.CashMarginBaseSourceID).Error)
		require.Equal(t, ztapiPriceSourceValues(&old), ztapiPriceSourceValues(&src))
		var previous ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&previous, c.PublicationSnapshotID).Error)
		require.Equal(t, previous.AllowedChannelIDs, snap.AllowedChannelIDs)
		_, _, _, err = ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{Source: src, ExpectedVersion: got.Version, OperatorID: 9, Reason: "must use controlled publisher"})
		require.Error(t, err)
	}
	result, err = ApplyZTAPIReferralFloor20261107(9, items, false)
	require.NoError(t, err)
	require.Equal(t, 7, result.Unchanged)
	require.Zero(t, result.Republished)
	var audits []ZTAPIAuditEvent
	require.NoError(t, db.Where("action = ?", "model.referral_floor_20261107_published").Find(&audits).Error)
	require.Len(t, audits, 7)
	for _, a := range audits {
		var v map[string]any
		require.NoError(t, common.UnmarshalJsonStr(a.Payload, &v))
		require.Equal(t, "0.05", v["commission_rate"])
		require.Equal(t, false, v["campaign_enabled"])
	}
}

func TestReferralFloor20261107RejectsStaleRoutePausedAndPartialBatch(t *testing.T) {
	for _, kind := range []string{"stale", "route", "paused", "partial", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			db, items := referralFloorFixture20261107(t)
			switch kind {
			case "stale":
				items[6].Version++
			case "route":
				require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", items[6].SnapshotID).Update("allowed_channel_ids", "[1]").Error)
			case "paused":
				require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = ?", items[6].ModelID).Update("published", false).Error)
			case "partial":
				items = items[:6]
			case "duplicate":
				items[6] = items[0]
			}
			var sourceCount, snapshotCount int64
			require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&sourceCount).Error)
			require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&snapshotCount).Error)
			_, err := ApplyZTAPIReferralFloor20261107(9, items, false)
			require.Error(t, err)
			var after int64
			require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&after).Error)
			require.Equal(t, sourceCount, after)
			require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&after).Error)
			require.Equal(t, snapshotCount, after)
		})
	}
}
