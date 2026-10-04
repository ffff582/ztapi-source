package model

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestZTAPIProbePayloadTextColumnHasNoMySQLIllegalLiteralDefault(t *testing.T) {
	field, ok := reflect.TypeOf(ZTAPIProbeJob{}).FieldByName("ProbePayloadJSON")
	require.True(t, ok)
	require.NotContains(t, strings.ToLower(field.Tag.Get("gorm")), "default:")
}

func probeScheduleFixture(t *testing.T, allocation int64) (*ZTAPIProbeStore, time.Time, ZTAPIProbeTarget) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "probes.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(ZTAPIProbeMigrationTypes()...))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		applied, err := InitializeZTAPIProbeAllocation(context.Background(), tx, "r7-transfer-1", allocation)
		require.True(t, applied)
		return err
	}))
	return &ZTAPIProbeStore{DB: db}, time.Unix(2_000_000_000, 0), ZTAPIProbeTarget{
		ModelID: 1, SourceModel: "source", PublicModel: "public", Protocol: "chat", Generation: 2, ConfigVersion: 3,
		InputNanoUSDPerMillion: 1_000_000_000, OutputNanoUSDPerMillion: 2_000_000_000,
	}
}

func probeAllow(_ context.Context, _ *gorm.DB, target ZTAPIProbeTarget, _ time.Time) (ZTAPIProbeAdmission, error) {
	return ZTAPIProbeAdmission{Target: target, Active: true}, nil
}

func probeClaim(t *testing.T, s *ZTAPIProbeStore, target ZTAPIProbeTarget, now time.Time) ZTAPIProbeJob {
	t.Helper()
	_, err := s.Enqueue(context.Background(), target, now)
	require.NoError(t, err)
	job, err := s.Claim(context.Background(), now, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, job)
	return *job
}

func TestZTAPIProbeScheduleCoverageAndGenerationCancel(t *testing.T) {
	for _, reason := range []string{"real", "unpublished", "generation", "price", "version", "source"} {
		t.Run(reason, func(t *testing.T) {
			s, now, target := probeScheduleFixture(t, 10_000_000)
			job := probeClaim(t, s, target, now)
			check := func(_ context.Context, _ *gorm.DB, current ZTAPIProbeTarget, since time.Time) (ZTAPIProbeAdmission, error) {
				require.Equal(t, now.Add(-time.Hour), since)
				a := ZTAPIProbeAdmission{Target: current, Active: true}
				switch reason {
				case "real":
					a.RealCoverage = true
				case "unpublished":
					a.Active = false
				case "generation":
					a.Target.Generation++
				case "price":
					a.Target.InputNanoUSDPerMillion++
				case "version":
					a.Target.ConfigVersion++
				case "source":
					a.Target.SourceModel = "changed"
				}
				return a, nil
			}
			result, send, err := s.Dispatch(context.Background(), job.ID, job.LeaseToken, now, time.Minute, check)
			require.NoError(t, err)
			require.False(t, send)
			require.Equal(t, "cancelled", result.State)
			budget, err := s.Budget(context.Background())
			require.NoError(t, err)
			require.Zero(t, budget.AccountedNanoUSD)
		})
	}
}

func TestZTAPIProbeScheduleUniqueLeaseRestartAndCooldown(t *testing.T) {
	s, now, target := probeScheduleFixture(t, 100_000_000)
	ctx := context.Background()
	job := probeClaim(t, s, target, now)
	duplicate, err := s.Enqueue(ctx, target, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, job.ID, duplicate.ID)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next, err := s.Claim(ctx, now, time.Minute)
			require.NoError(t, err)
			require.Nil(t, next)
		}()
	}
	wg.Wait()
	restarted := &ZTAPIProbeStore{DB: s.DB}
	reclaimed, err := restarted.Claim(ctx, now.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, job.LeaseToken, reclaimed.LeaseToken)
	_, _, err = s.Dispatch(ctx, job.ID, job.LeaseToken, now.Add(time.Minute), time.Minute, probeAllow)
	require.ErrorIs(t, err, ErrZTAPIProbeLease)
	job = *reclaimed
	dispatchedAt := now.Add(time.Minute)
	_, send, err := restarted.Dispatch(ctx, job.ID, job.LeaseToken, dispatchedAt, time.Minute, probeAllow)
	require.NoError(t, err)
	require.True(t, send)
	expired, err := restarted.ExpireDispatches(ctx, dispatchedAt.Add(time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	require.Equal(t, "unknown", expired[0].State)
	next, err := restarted.Claim(ctx, dispatchedAt.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Nil(t, next)
	// A new UTC bucket is not permission to send less than 60 minutes later.
	boundary := now.Truncate(time.Hour).Add(time.Hour)
	job = probeClaim(t, s, target, boundary)
	_, send, err = s.Dispatch(ctx, job.ID, job.LeaseToken, boundary, time.Minute, probeAllow)
	require.NoError(t, err)
	require.False(t, send)
	job = probeClaim(t, s, target, dispatchedAt.Add(time.Hour))
	_, send, err = s.Dispatch(ctx, job.ID, job.LeaseToken, dispatchedAt.Add(time.Hour), time.Minute, probeAllow)
	require.NoError(t, err)
	require.True(t, send)
}

func TestZTAPIProbeScheduleExactBudgetOverrunAndInitialization(t *testing.T) {
	const reserve = int64(4_048_000)
	s, now, target := probeScheduleFixture(t, reserve)
	ctx := context.Background()
	job := probeClaim(t, s, target, now)
	job, send, err := s.Dispatch(ctx, job.ID, job.LeaseToken, now, time.Minute, probeAllow)
	require.NoError(t, err)
	require.True(t, send)
	require.Equal(t, reserve, job.ReservedNanoUSD)
	require.NoError(t, s.Finish(ctx, job.ID, job.LeaseToken, "completed", "functional_pass", reserve+10))
	require.NoError(t, s.Finish(ctx, job.ID, job.LeaseToken, "completed", "functional_pass", reserve+10))
	budget, err := s.Budget(ctx)
	require.NoError(t, err)
	require.Equal(t, reserve+10, budget.AccountedNanoUSD)
	applied, err := InitializeZTAPIProbeAllocation(ctx, s.DB, "r7-transfer-1", reserve)
	require.NoError(t, err)
	require.False(t, applied)
	_, err = InitializeZTAPIProbeAllocation(ctx, s.DB, "r7-transfer-2", reserve)
	require.Error(t, err)
	target.Stream = true
	job = probeClaim(t, s, target, now)
	_, send, err = s.Dispatch(ctx, job.ID, job.LeaseToken, now, time.Minute, probeAllow)
	require.NoError(t, err)
	require.False(t, send)
}

func TestZTAPIProbeScheduleP95AndIntegerCeiling(t *testing.T) {
	samples := make([]ZTAPIProbeInputSample, 0)
	for i := int64(1); i <= 20; i++ {
		samples = append(samples, ZTAPIProbeInputSample{ModelID: 1, Source: "real", InputTokens: i, ConsumptionNanoUSD: 1})
	}
	samples = append(samples, ZTAPIProbeInputSample{ModelID: 2, Source: "real", InputTokens: 10000, ConsumptionNanoUSD: 1}, ZTAPIProbeInputSample{ModelID: 1, Source: "probe", InputTokens: 10000, ConsumptionNanoUSD: 1}, ZTAPIProbeInputSample{ModelID: 1, Source: "real", InputTokens: 10000})
	require.EqualValues(t, 19, ZTAPIProbeP95Input(1, samples))
	require.EqualValues(t, 2000, ZTAPIProbeP95Input(9, samples))
	n, err := ZTAPIProbeEstimateNanoUSD(1, 1, 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	_, err = ZTAPIProbeEstimateNanoUSD(-1, 1, 1, 1)
	require.Error(t, err)
}

func TestZTAPIProbeScheduleMediaUsesFrozenMinimumCostReservation(t *testing.T) {
	s, now, target := probeScheduleFixture(t, 9_000_000)
	target.Protocol = "images"
	target.Modality = ZTAPIModalityImage
	target.Operation = "image_generate"
	target.Stream = false
	target.InputNanoUSDPerMillion = 0
	target.OutputNanoUSDPerMillion = 0
	target.FixedCostNanoUSD = 7_500_000
	target.ProbePayloadJSON = `{"n":1,"quality":"standard","response_format":"url","size":"1024x1024"}`
	job := probeClaim(t, s, target, now)
	dispatched, send, err := s.Dispatch(context.Background(), job.ID, job.LeaseToken, now, time.Minute, probeAllow)
	require.NoError(t, err)
	require.True(t, send)
	require.EqualValues(t, 7_500_000, dispatched.ReservedNanoUSD)
	budget, err := s.Budget(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 7_500_000, budget.AccountedNanoUSD)
}

func TestZTAPIProbeScheduleReservationAtomicAcrossModes(t *testing.T) {
	s, now, target := probeScheduleFixture(t, 4_048_000)
	first := probeClaim(t, s, target, now)
	target.Stream = true
	second := probeClaim(t, s, target, now)
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	for _, job := range []ZTAPIProbeJob{first, second} {
		wg.Add(1)
		go func(job ZTAPIProbeJob) {
			defer wg.Done()
			_, sent, err := s.Dispatch(context.Background(), job.ID, job.LeaseToken, now, time.Minute, probeAllow)
			require.NoError(t, err)
			results <- sent
		}(job)
	}
	wg.Wait()
	close(results)
	sends := 0
	for sent := range results {
		if sent {
			sends++
		}
	}
	require.Equal(t, 1, sends)
	budget, err := s.Budget(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 4_048_000, budget.AccountedNanoUSD)
}

func TestZTAPIProbeScheduleCheckFailureNoSpendAndLateUnknownAccounting(t *testing.T) {
	s, now, target := probeScheduleFixture(t, 10_000_000)
	job := probeClaim(t, s, target, now)
	_, sent, err := s.Dispatch(context.Background(), job.ID, job.LeaseToken, now, time.Minute, func(context.Context, *gorm.DB, ZTAPIProbeTarget, time.Time) (ZTAPIProbeAdmission, error) {
		return ZTAPIProbeAdmission{}, context.Canceled
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, sent)
	budget, err := s.Budget(context.Background())
	require.NoError(t, err)
	require.Zero(t, budget.AccountedNanoUSD)
	_, sent, err = s.Dispatch(context.Background(), job.ID, job.LeaseToken, now, time.Minute, probeAllow)
	require.NoError(t, err)
	require.True(t, sent)
	_, err = s.ExpireDispatches(context.Background(), now.Add(time.Minute), 1)
	require.NoError(t, err)
	require.NoError(t, s.Finish(context.Background(), job.ID, job.LeaseToken, "completed", "functional_pass", 5_000_000))
	require.NoError(t, s.DB.First(&job, "id = ?", job.ID).Error)
	require.Equal(t, "unknown", job.State)
	require.EqualValues(t, 952_000, job.ExcessNanoUSD)
	budget, err = s.Budget(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 5_000_000, budget.AccountedNanoUSD)
}

func TestZTAPIProbeScheduleNormalAndFastMigrations(t *testing.T) {
	for name, migrate := range map[string]func() error{"normal": migrateDB, "fast": migrateDBFast} {
		t.Run(name, func(t *testing.T) {
			oldDB, oldSQLite, oldMySQL, oldPG := DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = db, true, false, false
			initCol()
			t.Cleanup(func() {
				_ = sqlDB.Close()
				DB, common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = oldDB, oldSQLite, oldMySQL, oldPG
				initCol()
			})
			require.NoError(t, migrate())
			for _, table := range ZTAPIProbeMigrationTypes() {
				require.True(t, db.Migrator().HasTable(table), "%T missing", table)
			}
			require.True(t, db.Migrator().HasTable(&ZTAPIHealthWorkerStatus{}))
			require.True(t, db.Migrator().HasIndex(&ZTAPIProbeJob{}, "idx_zt_probe_hour"))
		})
	}
}
