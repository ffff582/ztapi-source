package model

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/types"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestZTAPIHealthMySQLTestAlertProcess(t *testing.T) {
	dsn := os.Getenv("ZTAPI_HEALTH_MYSQL_TEST_DSN")
	operationID := os.Getenv("ZTAPI_HEALTH_MYSQL_TEST_ALERT_OPERATION")
	startValue := os.Getenv("ZTAPI_HEALTH_MYSQL_TEST_ALERT_START")
	if dsn == "" || operationID == "" || startValue == "" {
		t.Skip("subprocess helper")
	}
	start, err := strconv.ParseInt(startValue, 10, 64)
	require.NoError(t, err)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	time.Sleep(time.Until(time.Unix(0, start)))
	job, err := NewZTAPIHealthStore(db).EnqueueTestAlert(context.Background(), operationID, 42)
	require.NoError(t, err)
	fmt.Printf("RESULT:job:%d\n", job.ID)
}

func TestZTAPIHealthTextColumnsHaveNoMySQLDefaults(t *testing.T) {
	for _, value := range []any{&ZTAPIHealthState{}, &ZTAPIHealthRequest{}, &ZTAPIHealthEvent{}, &ZTAPIHealthIncident{}, &ZTAPIHealthOutbox{}} {
		parsed, err := schema.Parse(value, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		for _, field := range parsed.Fields {
			if field.DataType == "text" {
				require.False(t, field.HasDefaultValue, "%s.%s", parsed.Table, field.DBName)
			}
		}
	}
}

// Explicit opt-in only. Never discovers credentials or accepts production DSNs.
// This uses a dedicated loopback database and leaves uniquely named evidence rows.
func TestZTAPIHealthMySQLDurableConcurrentTrip(t *testing.T) {
	dsn := os.Getenv("ZTAPI_HEALTH_MYSQL_TEST_DSN")
	if dsn == "" {
		if os.Getenv("ZTAPI_REQUIRE_HEALTH_MYSQL_INTEGRATION") == "true" {
			t.Fatal("mandatory MySQL health integration DSN is missing")
		}
		t.Skip("dedicated loopback MySQL health test database not configured")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	require.Equal(t, "tcp", cfg.Net)
	host, _, err := net.SplitHostPort(cfg.Addr)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)
	require.Equal(t, "ztapi_health_test", cfg.DBName)
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(12)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPICatalogLock{}, &ZTAPIAuditEvent{}, &ZTAPIModelIdentity{}, &Ability{}))
	require.NoError(t, MigrateZTAPIHealth(db))
	t.Run("test_alert_concurrency", func(t *testing.T) {
		for _, sameOperation := range []bool{true, false} {
			t.Run(map[bool]string{true: "same_operation", false: "different_operations"}[sameOperation], func(t *testing.T) {
				require.NoError(t, db.Where("kind = ?", "alert_test").Delete(&ZTAPIHealthOutbox{}).Error)
				require.NoError(t, db.Where("action = ?", "model.health_test_alert").Delete(&ZTAPIAuditEvent{}).Error)
				operationID := uuid.NewString()
				start := time.Now().Add(2 * time.Second).UnixNano()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				outputs := make(chan string, 8)
				errs := make(chan error, 8)
				var wg sync.WaitGroup
				for range 8 {
					op := operationID
					if !sameOperation {
						op = uuid.NewString()
					}
					cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZTAPIHealthMySQLTestAlertProcess$", "-test.count=1")
					cmd.Env = append(os.Environ(),
						"ZTAPI_HEALTH_MYSQL_TEST_ALERT_OPERATION="+op,
						"ZTAPI_HEALTH_MYSQL_TEST_ALERT_START="+strconv.FormatInt(start, 10))
					wg.Add(1)
					go func() {
						defer wg.Done()
						output, err := cmd.CombinedOutput()
						outputs <- string(output)
						errs <- err
					}()
				}
				wg.Wait()
				close(outputs)
				close(errs)
				var all strings.Builder
				for output := range outputs {
					all.WriteString(output)
				}
				for err := range errs {
					require.NoError(t, err, all.String())
				}
				require.Equal(t, 8, strings.Count(all.String(), "RESULT:job:"), all.String())
				expected := map[bool]int{true: 1, false: 8}[sameOperation]
				for _, table := range []any{&ZTAPIHealthOutbox{}, &ZTAPIAuditEvent{}} {
					var count int64
					require.NoError(t, db.Model(table).Count(&count).Error)
					require.EqualValues(t, expected, count)
				}
			})
		}
	})
	for _, table := range []string{"ztapi_health_states", "ztapi_health_requests", "ztapi_health_events", "ztapi_health_incidents", "ztapi_health_outbox", "ztapi_model_configs", "ztapi_catalog_locks"} {
		var engine string
		require.NoError(t, db.Raw("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&engine).Error)
		require.Equal(t, "InnoDB", engine)
	}
	alias := "zt-health-" + uuid.NewString()
	c := ZTAPIModelConfig{SourceModel: alias + "-upstream", PublicName: &alias, Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI, Published: true, EnabledGroups: `["default"]`, Version: 1, InputPricePerMillion: 1, OutputPricePerMillion: 2}
	require.NoError(t, db.Create(&c).Error)
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB; InvalidateZTAPIAliasCache() })
	s := NewZTAPIHealthStore(db)
	ctx := context.Background()
	assertIdentityLocked := func(t *testing.T) {
		t.Helper()
		var current ZTAPIModelConfig
		require.NoError(t, s.DB.First(&current, c.ID).Error)
		for _, enabled := range []string{"true", "false"} {
			t.Run("enabled_"+enabled, func(t *testing.T) {
				t.Setenv("ZTAPI_HEALTH_ENABLED", enabled)
				replacement := alias + "-replacement"
				next := current
				next.PublicName = &replacement
				next.Published = false
				_, err := UpdateZTAPIModelConfigAndBilling(&next, current.Version, nil)
				require.ErrorIs(t, err, ErrZTAPIHealthIdentityLocked)
				next = current
				next.SourceModel += "-replacement"
				next.Published = false
				_, err = UpdateZTAPIModelConfigAndBilling(&next, current.Version, nil)
				require.ErrorIs(t, err, ErrZTAPIHealthIdentityLocked)
				_, _, err = UpdateZTAPIModelIdentity(ZTAPIModelIdentityUpdate{
					ModelConfigID: c.ID, ExpectedVersion: current.Version, SourceModel: current.SourceModel,
					PublicName: replacement, Protocol: current.Protocol, ProviderFamily: current.ProviderFamily,
					SourceReference: "synthetic-mysql-identity", OperatorID: 1, Reason: "replacement must be rejected",
				})
				require.ErrorIs(t, err, ErrZTAPIHealthIdentityLocked)
				var actual ZTAPIModelConfig
				require.NoError(t, s.DB.First(&actual, c.ID).Error)
				require.Equal(t, current, actual, "rejected identity edits must roll back every catalog field")
			})
		}
		var identities int64
		require.NoError(t, s.DB.Model(&ZTAPIModelIdentity{}).Where("model_config_id = ?", c.ID).Count(&identities).Error)
		require.Zero(t, identities)
	}
	tickets := make([]*types.ZTAPIHealthTicket, 12)
	for i := range tickets {
		tickets[i] = healthAdmit(t, s, c, uuid.NewString())
	}
	oldGenerationTicket := healthAdmit(t, s, c, uuid.NewString())
	t.Run("identity_locked_after_admission", assertIdentityLocked)
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for _, ticket := range tickets {
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(ticket *types.ZTAPIHealthTicket) {
				defer wg.Done()
				errs <- s.RecordOutcome(ctx, ticket, types.ZTAPIHealthOutcome{Result: "failure", Reason: "synthetic_upstream_5xx", Dispatched: true, HTTPStatus: 503})
			}(ticket)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	state, err := s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.True(t, state.Open)
	require.EqualValues(t, 12, state.CompletionSequence)
	incidents, err := s.ListIncidents(ctx, c.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, incidents, 1)
	// A fresh connection observes the committed breaker with instrumentation off.
	require.NoError(t, sqlDB.Close())
	reopened, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	reopenedSQL, err := reopened.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopenedSQL.Close()) })
	s = NewZTAPIHealthStore(reopened)
	DB = reopened
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	require.ErrorIs(t, s.CheckAvailable(ctx, alias), ErrZTAPIHealthCircuitOpen)
	require.NoError(t, reopened.Model(&c).Updates(map[string]any{"input_price_per_million": 99, "enabled_groups": `["enterprise"]`, "version": 2}).Error)
	jobs, err := s.ClaimOutbox(ctx, "unpublish", 100, 60)
	require.NoError(t, err)
	for _, job := range jobs {
		require.NoError(t, s.ProcessUnpublish(ctx, job.ID, job.LeaseToken))
	}
	require.NoError(t, reopened.First(&c, c.ID).Error)
	require.False(t, c.Published)
	require.EqualValues(t, 99, c.InputPricePerMillion)
	require.Equal(t, `["enterprise"]`, c.EnabledGroups)
	require.NoError(t, s.ManualRecover(ctx, c.ID, state.Generation, 1, "synthetic-mysql-verification"))
	require.ErrorIs(t, s.ManualRecover(ctx, c.ID, state.Generation, 1, "duplicate-recovery"), ErrZTAPIHealthGenerationConflict)
	window, err := s.Window(ctx, c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 12, window.ValidSamples)
	require.EqualValues(t, 12, window.Failures)
	recovered, err := s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.False(t, recovered.Open)
	require.Equal(t, state.Generation+1, recovered.Generation)
	require.Zero(t, recovered.ConsecutiveFailures)
	require.NoError(t, reopened.First(&c, c.ID).Error)
	require.False(t, c.Published, "manual recovery never republishes")
	incidents, err = s.ListIncidents(ctx, c.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, incidents, 1)
	require.Positive(t, incidents[0].RecoveredAt)
	require.Equal(t, 1, incidents[0].RecoveryOperatorID)
	require.Equal(t, "synthetic-mysql-verification", incidents[0].RecoveryEvidence)
	var audits int64
	require.NoError(t, reopened.Model(&ZTAPIAuditEvent{}).Where("model_config_id = ? AND action = ?", c.ID, "model.health_manual_recovery").Count(&audits).Error)
	require.EqualValues(t, 1, audits)
	t.Run("identity_locked_after_recovery", assertIdentityLocked)
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	unpublished, err := s.AdmitRequest(ctx, alias, uuid.NewString(), "synthetic-unpublished", 1, false)
	require.ErrorIs(t, err, ErrZTAPIModelNotPublic)
	require.Nil(t, unpublished)

	healthRecord(t, s, oldGenerationTicket, "failure")
	var stale ZTAPIHealthEvent
	require.NoError(t, reopened.First(&stale, "execution_id = ?", oldGenerationTicket.ExecutionID).Error)
	require.True(t, stale.StaleGeneration)
	require.False(t, stale.Counted)
	state, err = s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.False(t, state.Open)
	require.Zero(t, state.ConsecutiveFailures)

	// Explicit fixture-only republish isolates completion behavior, not publication authorization.
	require.NoError(t, reopened.Model(&c).Update("published", true).Error)
	healthRecord(t, s, healthAdmit(t, s, c, uuid.NewString()), "success")
	state, err = s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.False(t, state.Open, "a recovered success must not retrip historical failures")
	require.Equal(t, recovered.Generation, state.Generation)
	require.Zero(t, state.ConsecutiveFailures)
	window, err = s.Window(ctx, c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 13, window.ValidSamples)
	require.EqualValues(t, 12, window.Failures)
	incidents, err = s.ListIncidents(ctx, c.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, incidents, 1)
	t.Logf("MYSQL_RECOVERY_SUCCESS generation=%d valid=%d failures=%d incidents=%d open=%t", state.Generation, window.ValidSamples, window.Failures, len(incidents), state.Open)

	healthRecord(t, s, healthAdmit(t, s, c, uuid.NewString()), "failure")
	state, err = s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.True(t, state.Open, "a new attributable failure must evaluate the retained rolling window")
	require.EqualValues(t, 1, state.ConsecutiveFailures)
	incidents, err = s.ListIncidents(ctx, c.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, incidents, 2)
	require.Equal(t, "rolling_24h_gt_2pct", incidents[1].Rule)
	require.EqualValues(t, 14, incidents[1].ValidSamples)
	require.EqualValues(t, 13, incidents[1].Failures)
	jobs, err = s.ClaimOutbox(ctx, "unpublish", 100, 60)
	require.NoError(t, err)
	for _, job := range jobs {
		require.NoError(t, s.ProcessUnpublish(ctx, job.ID, job.LeaseToken))
	}
	require.NoError(t, reopened.First(&c, c.ID).Error)
	require.False(t, c.Published)
	require.EqualValues(t, 99, c.InputPricePerMillion)
	require.Equal(t, `["enterprise"]`, c.EnabledGroups)
	t.Logf("MYSQL_NEW_FAILURE generation=%d sequence=%d incidents=%d rule=%s open=%t", state.Generation, state.CompletionSequence, len(incidents), incidents[1].Rule, state.Open)

	t.Run("identity_guard_current_read_after_rr_snapshot", func(t *testing.T) {
		racingAlias := "zt-health-rr-" + uuid.NewString()
		racing := ZTAPIModelConfig{SourceModel: racingAlias + "-upstream", PublicName: &racingAlias,
			Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
			Published: true, EnabledGroups: `["default"]`, Version: 1}
		require.NoError(t, reopened.Create(&racing).Error)
		admissionDB, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		admissionSQL, err := admissionDB.DB()
		require.NoError(t, err)
		admissionSQL.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, admissionSQL.Close()) })
		var defaultIsolation string
		require.NoError(t, reopened.Raw("SELECT @@global.transaction_isolation").Scan(&defaultIsolation).Error)
		require.Equal(t, "REPEATABLE-READ", defaultIsolation)
		txB := reopened.Begin()
		require.NoError(t, txB.Error)
		defer txB.Rollback()
		var isolation string
		var identityConnection, admissionConnection uint64
		require.NoError(t, txB.Raw("SELECT @@session.transaction_isolation").Scan(&isolation).Error)
		require.Equal(t, "REPEATABLE-READ", isolation)
		require.NoError(t, txB.Raw("SELECT CONNECTION_ID()").Scan(&identityConnection).Error)
		require.NoError(t, admissionDB.Raw("SELECT CONNECTION_ID()").Scan(&admissionConnection).Error)
		require.NotEqual(t, identityConnection, admissionConnection)

		// These nonlocking catalog reads establish B's snapshot before admission A commits.
		var current ZTAPIModelConfig
		require.NoError(t, txB.First(&current, racing.ID).Error)
		require.NoError(t, ValidateZTAPISourceModelNames(txB, []string{current.SourceModel}))
		var snapshotAdmissions int64
		require.NoError(t, txB.Model(&ZTAPIHealthRequest{}).Where("model_id = ?", racing.ID).Count(&snapshotAdmissions).Error)
		require.Zero(t, snapshotAdmissions)
		admissionStore := NewZTAPIHealthStore(admissionDB)
		inflight := healthAdmit(t, admissionStore, racing, uuid.NewString())
		committed, err := admissionStore.GetRequest(ctx, inflight.ExecutionID)
		require.NoError(t, err)
		require.False(t, committed.Completed)
		admittedState, err := admissionStore.GetState(ctx, racing.ID)
		require.NoError(t, err)
		require.False(t, admittedState.Open)
		require.Zero(t, admittedState.CompletionSequence)
		var committedAdmissions int64
		require.NoError(t, admissionDB.Model(&ZTAPIHealthRequest{}).Where("model_id = ?", racing.ID).Count(&committedAdmissions).Error)
		require.EqualValues(t, 1, committedAdmissions)
		require.NoError(t, txB.Model(&ZTAPIHealthRequest{}).Where("model_id = ?", racing.ID).Count(&snapshotAdmissions).Error)
		require.Zero(t, snapshotAdmissions, "B's ordinary RR reads still cannot see the committed admission")
		guardErr := guardZTAPIHealthIdentityChangeTx(txB, &current, current.SourceModel, racingAlias+"-replacement", current.Protocol, current.ProviderFamily)
		t.Logf("MYSQL_RR_GUARD isolation=%s separate_connections=%t snapshot_admissions=%d committed_admissions=%d completion_sequence=%d guard_error=%v", isolation, identityConnection != admissionConnection, snapshotAdmissions, committedAdmissions, admittedState.CompletionSequence, guardErr)
		require.ErrorIs(t, guardErr, ErrZTAPIHealthIdentityLocked, "identity guard must use a locking current read, not B's old RR snapshot")
	})
}
