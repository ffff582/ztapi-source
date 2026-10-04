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
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
	require.NoError(t, db.Migrator().DropTable(&ZTAPIHealthProbeEvidence{}, &ZTAPIHealthVerificationGate{}, &ZTAPIHealthRouteState{}, &ZTAPIHealthVerificationCase{}))
	seedLegacyVerificationCases(t, db)
	require.NoError(t, MigrateZTAPIHealth(db))
	require.False(t, db.Migrator().HasColumn(&ZTAPIHealthVerificationCase{}, "window"))
	require.False(t, db.Migrator().HasIndex(&ZTAPIHealthVerificationCase{}, "idx_ztapi_verify_window"))
	firstCancelledAt := requireLegacyVerificationCasesCancelled(t, db)
	legacyClaim, err := NewZTAPIHealthVerificationStore(db).Claim(context.Background(), time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, legacyClaim, "legacy MySQL queued and claimed rows must never be paid work")
	require.NoError(t, MigrateZTAPIHealth(db))
	require.Equal(t, firstCancelledAt, requireLegacyVerificationCasesCancelled(t, db), "repeated MySQL migration must preserve cancellation audit timestamps")
	orphanIDs, validCase := seedPreMigratedVerificationCases(t, db, "mysql-pre-migrated", 890000)
	require.NoError(t, MigrateZTAPIHealth(db))
	requirePreMigratedVerificationCasesReconciled(t, db, orphanIDs, validCase)
	foundRowsConfig := *cfg
	foundRowsConfig.ClientFoundRows = true
	verificationDB, err := gorm.Open(mysql.Open(foundRowsConfig.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	verificationSQL, err := verificationDB.DB()
	require.NoError(t, err)
	verificationSQL.SetMaxOpenConns(12)
	t.Cleanup(func() { require.NoError(t, verificationSQL.Close()) })
	require.NoError(t, verificationDB.Exec("DELETE FROM ztapi_health_probe_evidence").Error)
	require.NoError(t, verificationDB.Exec("DELETE FROM ztapi_health_verification_cases").Error)
	require.NoError(t, verificationDB.Exec("DELETE FROM ztapi_health_route_states").Error)
	require.NoError(t, verificationDB.Exec("DELETE FROM ztapi_health_verification_gates").Error)
	require.NoError(t, verificationDB.AutoMigrate(&ZTAPIProbeBudget{}))
	_, err = InitializeZTAPIProbeAllocation(context.Background(), verificationDB, "mysql-health-suite-"+uuid.NewString(), 30_000_000_000)
	require.NoError(t, err)
	t.Run("verification_store_client_found_rows", func(t *testing.T) {
		ctx := context.Background()
		now := time.Now().UTC().Add(time.Hour)
		fingerprint, err := FingerprintZTAPICredential("mysql-verification-" + uuid.NewString())
		require.NoError(t, err)
		route := ZTAPIHealthRouteIdentity{ModelID: 900001, ChannelID: 900002, Protocol: "responses", Stream: true, CredentialVersion: fingerprint, Generation: 1}
		store := NewZTAPIHealthVerificationStore(verificationDB)
		firstSuspicion := ZTAPIHealthSuspicion{Route: route, SourceEventID: now.UnixNano()}
		firstEvent := verificationEventForSuspicion(firstSuspicion)
		require.NoError(t, verificationDB.Create(&firstEvent).Error)
		firstCase, created, err := store.EnqueueSuspicion(ctx, firstSuspicion, now)
		require.NoError(t, err)
		require.True(t, created)
		first, err := store.Claim(ctx, now, time.Minute)
		require.NoError(t, err)
		require.Equal(t, firstCase.ID, first.ID)
		firstDispatched := beginVerificationDispatch(t, store, first, now.Add(500*time.Millisecond))
		probeID := firstDispatched.ProbeRequestID
		state, err := store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: firstDispatched.ID, LeaseToken: firstDispatched.LeaseToken, Generation: firstDispatched.Generation, ProbeRequestID: probeID, Result: "failure"}, now.Add(time.Second))
		require.NoError(t, err)
		require.False(t, state.Open)
		require.EqualValues(t, 1, state.IndependentFailures)

		secondSuspicion := ZTAPIHealthSuspicion{Route: route, SourceEventID: now.UnixNano() + 1}
		secondEvent := verificationEventForSuspicion(secondSuspicion)
		require.NoError(t, verificationDB.Create(&secondEvent).Error)
		secondCase, created, err := store.EnqueueSuspicion(ctx, secondSuspicion, now.Add(5*time.Minute+time.Second))
		require.NoError(t, err)
		require.True(t, created)
		second, err := store.Claim(ctx, now.Add(5*time.Minute+time.Second), time.Minute)
		require.NoError(t, err)
		require.Equal(t, secondCase.ID, second.ID)
		secondDispatched := beginVerificationDispatch(t, store, second, now.Add(5*time.Minute+1500*time.Millisecond))
		_, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: secondDispatched.ID, LeaseToken: secondDispatched.LeaseToken, Generation: secondDispatched.Generation, ProbeRequestID: probeID, Result: "failure"}, now.Add(5*time.Minute+2*time.Second))
		require.ErrorIs(t, err, ErrZTAPIVerificationDuplicateProbe)
		require.NoError(t, applyZTAPIHealthRouteIdentity(verificationDB, route).Take(&state).Error)
		require.False(t, state.Open)
		require.EqualValues(t, 1, state.IndependentFailures, "CLIENT_FOUND_ROWS must not turn duplicate evidence into a second failure")

		state, err = store.CompleteProbe(ctx, ZTAPIHealthProbeCompletion{CaseID: secondDispatched.ID, LeaseToken: secondDispatched.LeaseToken, Generation: secondDispatched.Generation, ProbeRequestID: secondDispatched.ProbeRequestID, Result: "failure"}, now.Add(5*time.Minute+3*time.Second))
		require.NoError(t, err)
		require.True(t, state.Open)
		require.EqualValues(t, 2, state.IndependentFailures)
	})
	t.Run("verification_claim_is_atomic", func(t *testing.T) {
		ctx := context.Background()
		now := time.Now().UTC().Add(2 * time.Hour)
		fingerprint, err := FingerprintZTAPICredential("mysql-claim-" + uuid.NewString())
		require.NoError(t, err)
		route := ZTAPIHealthRouteIdentity{ModelID: 900003, ChannelID: 900004, Protocol: "chat", Stream: false, CredentialVersion: fingerprint, Generation: 1}
		store := NewZTAPIHealthVerificationStore(verificationDB)
		suspicion := ZTAPIHealthSuspicion{Route: route, SourceEventID: now.UnixNano()}
		event := verificationEventForSuspicion(suspicion)
		require.NoError(t, verificationDB.Create(&event).Error)
		_, created, err := store.EnqueueSuspicion(ctx, suspicion, now)
		require.NoError(t, err)
		require.True(t, created)
		start := make(chan struct{})
		results := make(chan *ZTAPIHealthVerificationCase, 8)
		errs := make(chan error, 8)
		for range 8 {
			go func() {
				<-start
				claimed, claimErr := NewZTAPIHealthVerificationStore(verificationDB.Session(&gorm.Session{NewDB: true})).Claim(ctx, now, time.Minute)
				results <- claimed
				errs <- claimErr
			}()
		}
		close(start)
		claims := 0
		for range 8 {
			require.NoError(t, <-errs)
			if <-results != nil {
				claims++
			}
		}
		require.Equal(t, 1, claims)
	})
	t.Run("real_success_and_probe_completion_share_lock_order", func(t *testing.T) {
		ctx := context.Background()
		for iteration := 0; iteration < 50; iteration++ {
			suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
			publicName := "zt-mysql-health-race-" + suffix
			config := ZTAPIModelConfig{
				SourceModel: "gpt-mysql-health-race-" + suffix, PublicName: &publicName,
				Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
				EnabledGroups: `["default"]`, Published: true, Version: 1,
			}
			require.NoError(t, verificationDB.Session(&gorm.Session{SkipHooks: true}).Create(&config).Error)
			healthStore := NewZTAPIHealthStore(verificationDB.Session(&gorm.Session{NewDB: true}))
			realTicket, err := healthStore.AdmitRequestWithEntryProtocol(ctx, publicName, "real-"+suffix, "request-"+suffix, 1, false, "chat")
			require.NoError(t, err)
			fingerprint, err := FingerprintZTAPICredential("mysql-health-race-key-" + suffix)
			require.NoError(t, err)
			route := ZTAPIHealthRouteIdentity{
				ModelID: config.ID, ChannelID: 910000 + iteration, EntryProtocol: "chat", Protocol: "responses",
				CredentialVersion: fingerprint, Generation: realTicket.Generation,
			}
			require.NoError(t, healthStore.AdmitAttempt(ctx, realTicket, route.ChannelID, route.Protocol, route.CredentialVersion.String()))
			routeState := routeStateFromIdentity(route)
			routeState.IndependentFailures = 1
			routeState.LastResult = "failure"
			require.NoError(t, verificationDB.Create(&routeState).Error)

			now := time.Now().UTC().Add(time.Duration(iteration) * time.Second)
			verificationStore := NewZTAPIHealthVerificationStore(verificationDB.Session(&gorm.Session{NewDB: true}))
			verificationCase, created, err := enqueueVerificationSuspicion(t, verificationStore, ctx, ZTAPIHealthSuspicion{
				Route: route, SourceEventID: now.UnixNano(),
			}, now)
			require.NoError(t, err)
			require.True(t, created)
			claimed, err := verificationStore.Claim(ctx, now, time.Minute)
			require.NoError(t, err)
			require.Equal(t, verificationCase.ID, claimed.ID)
			dispatched := beginVerificationDispatch(t, verificationStore, claimed, now.Add(time.Millisecond))

			var healthRetries, verificationRetries atomic.Int64
			healthStore.retryObserver = func(error) { healthRetries.Add(1) }
			verificationStore.retryObserver = func(error) { verificationRetries.Add(1) }
			runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			errs := make(chan error, 2)
			modelLocked := make(chan struct{})
			catalogLocked := make(chan struct{})
			stateLockAttempted := make(chan struct{})
			releaseModelLock := make(chan struct{})
			healthStore.afterStateLock = func() {
				close(modelLocked)
				<-releaseModelLock
			}
			verificationStore.afterAggregationLock = func() { close(catalogLocked) }
			verificationStore.beforeStateLock = func() { close(stateLockAttempted) }
			go func() {
				errs <- healthStore.RecordOutcome(runCtx, realTicket, types.ZTAPIHealthOutcome{
					Result: "success", Reason: "valid_output", ChannelID: route.ChannelID,
					CredentialVersion: route.CredentialVersion.String(), UpstreamProtocol: route.Protocol,
					Dispatched: true, TransportComplete: true, HasText: true,
				})
			}()
			select {
			case <-modelLocked:
			case runErr := <-errs:
				require.NoError(t, runErr)
				t.Fatal("real completion returned before acquiring the model lock")
			case <-runCtx.Done():
				t.Fatal("real completion did not acquire the model lock")
			}
			go func() {
				_, completeErr := verificationStore.CompleteProbe(runCtx, ZTAPIHealthProbeCompletion{
					CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
					ProbeRequestID: dispatched.ProbeRequestID, Result: "failure",
				}, now.Add(2*time.Second))
				errs <- completeErr
			}()
			select {
			case <-catalogLocked:
			case runErr := <-errs:
				require.NoError(t, runErr)
				t.Fatal("probe completion returned before acquiring the catalog lock")
			case <-runCtx.Done():
				t.Fatal("probe completion did not acquire the catalog lock")
			}
			select {
			case <-stateLockAttempted:
			case runErr := <-errs:
				require.NoError(t, runErr)
				t.Fatal("probe completion returned before attempting the model lock")
			case <-runCtx.Done():
				t.Fatal("probe completion did not attempt the model lock")
			}
			var lockWaits int64
			require.Eventually(t, func() bool {
				lockWaits = 0
				err := verificationDB.Raw("SELECT COUNT(*) FROM performance_schema.data_lock_waits").Scan(&lockWaits).Error
				return err == nil && lockWaits > 0
			}, 5*time.Second, 10*time.Millisecond, "probe completion must be blocked on the model row before the original transaction is released")
			close(releaseModelLock)
			for result := 0; result < 2; result++ {
				select {
				case runErr := <-errs:
					require.NoError(t, runErr)
				case <-runCtx.Done():
					t.Fatal("real request and probe completion deadlocked")
				}
			}
			cancel()
			require.Zero(t, healthRetries.Load(), "real completion must not rely on a deadlock retry")
			require.Zero(t, verificationRetries.Load(), "probe completion must not rely on a deadlock retry")
			var persistedCase ZTAPIHealthVerificationCase
			require.NoError(t, verificationDB.First(&persistedCase, "id = ?", dispatched.ID).Error)
			require.Equal(t, "completed", persistedCase.State)
			require.Empty(t, persistedCase.LeaseToken)
		}
	})
	t.Run("concurrent_route_open_creates_one_model_incident", func(t *testing.T) {
		for _, table := range []string{
			"ztapi_health_probe_evidence", "ztapi_health_verification_cases", "ztapi_health_route_states", "ztapi_health_verification_gates",
		} {
			require.NoError(t, verificationDB.Exec("DELETE FROM "+table).Error)
		}
		require.NoError(t, verificationDB.AutoMigrate(&Channel{}, &ZTAPIModelPublicationSnapshot{}, &ZTAPICatalogLock{}, &ZTAPIProbeBudget{}))
		ctx := context.Background()
		now := time.Now().UTC().Add(3 * time.Hour)
		suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
		publicName := "zt-mysql-aggregate-" + suffix
		sourceModel := "gpt-mysql-aggregate-" + suffix
		config := ZTAPIModelConfig{
			SourceModel: sourceModel, PublicName: &publicName, Family: ZTAPIModelFamilyOpenAI,
			Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
			EnabledGroups: `["default"]`, Published: false, Version: 1,
		}
		require.NoError(t, verificationDB.Session(&gorm.Session{SkipHooks: true}).Create(&config).Error)
		channels := []Channel{
			{Name: "mysql-enterprise-" + suffix, Type: constant.ChannelTypeOpenAI, Key: "enterprise-" + suffix, Models: sourceModel, Status: common.ChannelStatusEnabled, ZTAPIManaged: true, ZTAPIFamily: ZTAPIModelFamilyOpenAI},
			{Name: "mysql-pool-" + suffix, Type: constant.ChannelTypeOpenAI, Key: "pool-" + suffix, Models: sourceModel, Status: common.ChannelStatusEnabled, ZTAPIManaged: true, ZTAPIFamily: ZTAPIModelFamilyOpenAI},
		}
		for index := range channels {
			require.NoError(t, verificationDB.Session(&gorm.Session{SkipHooks: true}).Create(&channels[index]).Error)
			require.NoError(t, verificationDB.Create(&Ability{Group: "default", Model: sourceModel, ChannelId: channels[index].Id, Enabled: true}).Error)
		}
		snapshot := ZTAPIModelPublicationSnapshot{
			ModelConfigID: config.ID, ModelVersion: config.Version, SourceModel: sourceModel, PublicName: publicName,
			Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
			AllowedChannelIDs: fmt.Sprintf("[%d,%d]", channels[0].Id, channels[1].Id), EnabledGroups: `["default"]`,
		}
		require.NoError(t, verificationDB.Session(&gorm.Session{SkipHooks: true}).Create(&snapshot).Error)
		require.NoError(t, verificationDB.Model(&config).Updates(map[string]any{"published": true, "publication_snapshot_id": snapshot.ID}).Error)
		require.NoError(t, verificationDB.Create(&ZTAPIHealthState{ModelID: config.ID, Generation: 1}).Error)

		routes := make([]ZTAPIHealthRouteIdentity, 2)
		for index := range channels {
			fingerprint, err := FingerprintZTAPICredential("authorization\x00Bearer " + channels[index].Key)
			require.NoError(t, err)
			routes[index] = ZTAPIHealthRouteIdentity{
				ModelID: config.ID, ChannelID: channels[index].Id, EntryProtocol: "chat", Protocol: "responses",
				CredentialVersion: fingerprint, Generation: 1,
			}
			routeState := routeStateFromIdentity(routes[index])
			routeState.IndependentFailures = 1
			routeState.LastResult = "failure"
			require.NoError(t, verificationDB.Create(&routeState).Error)
		}

		store := NewZTAPIHealthVerificationStore(verificationDB)
		completions := make([]ZTAPIHealthProbeCompletion, 2)
		for index := range routes {
			eventID := now.UnixNano() + int64(index) + 1
			verificationCase, created, err := enqueueVerificationSuspicion(t, store, ctx, ZTAPIHealthSuspicion{Route: routes[index], SourceEventID: eventID}, now.Add(time.Duration(index)*time.Second))
			require.NoError(t, err)
			require.True(t, created)
			claimed, err := store.Claim(ctx, now.Add(time.Duration(index)*time.Second), time.Minute)
			require.NoError(t, err)
			require.Equal(t, verificationCase.ID, claimed.ID)
			dispatched := beginVerificationDispatch(t, store, claimed, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
			completions[index] = ZTAPIHealthProbeCompletion{
				CaseID: dispatched.ID, LeaseToken: dispatched.LeaseToken, Generation: dispatched.Generation,
				ProbeRequestID: dispatched.ProbeRequestID, Result: "failure",
			}
		}

		start := make(chan struct{})
		errs := make(chan error, len(completions))
		for _, completion := range completions {
			completion := completion
			go func() {
				<-start
				_, err := NewZTAPIHealthVerificationStore(verificationDB.Session(&gorm.Session{NewDB: true})).CompleteProbe(ctx, completion, now.Add(5*time.Second))
				errs <- err
			}()
		}
		close(start)
		for range completions {
			require.NoError(t, <-errs)
		}
		allUnavailable, eligible, err := evaluateZTAPIModelAvailabilityTx(ctx, verificationDB, routes[0])
		require.NoError(t, err)
		require.EqualValues(t, 2, eligible)
		require.True(t, allUnavailable)
		var incidents, unpublishJobs, alertJobs int64
		require.NoError(t, verificationDB.Model(&ZTAPIHealthIncident{}).Where("model_id = ? AND rule = ?", config.ID, "verified_all_routes").Count(&incidents).Error)
		require.NoError(t, verificationDB.Model(&ZTAPIHealthOutbox{}).Where("model_id = ? AND kind = ?", config.ID, "unpublish").Count(&unpublishJobs).Error)
		require.NoError(t, verificationDB.Model(&ZTAPIHealthOutbox{}).Where("model_id = ? AND kind = ?", config.ID, "alert").Count(&alertJobs).Error)
		require.EqualValues(t, 1, incidents)
		require.EqualValues(t, 1, unpublishJobs)
		require.EqualValues(t, 1, alertJobs)
	})
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
				var outboxCount, auditCount int64
				require.NoError(t, db.Model(&ZTAPIHealthOutbox{}).Where("kind = ?", "alert_test").Count(&outboxCount).Error)
				require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Where("action = ?", "model.health_test_alert").Count(&auditCount).Error)
				require.EqualValues(t, expected, outboxCount)
				require.EqualValues(t, expected, auditCount)
			})
		}
	})
	for _, table := range []string{"ztapi_health_states", "ztapi_health_requests", "ztapi_health_events", "ztapi_health_incidents", "ztapi_health_outbox", "ztapi_health_verification_cases", "ztapi_health_verification_gates", "ztapi_health_route_states", "ztapi_health_probe_evidence", "ztapi_model_configs", "ztapi_catalog_locks"} {
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
		healthMarkDiagnostic(t, s, tickets[i])
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
	require.True(t, c.Published, "legacy model-wide incidents must not unpublish a model")
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
	require.NoError(t, reopened.Model(&c).Update("published", false).Error)
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
	require.True(t, c.Published, "legacy rolling-window incidents must not unpublish a model")
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
