package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testAlertOperation = "c7c15c6b-0b70-461f-97c0-01369ad299ad"
const testAlertReceipt = `{"ok":true,"result":{"message_id":123,"date":2000000000}}`

func TestZTAPIHealthTestAlertEnqueue(t *testing.T) {
	s, cfg, now := healthFixture(t)
	ctx := context.Background()
	job, err := s.EnqueueTestAlert(ctx, strings.ToUpper(testAlertOperation), 42)
	require.NoError(t, err)
	require.Equal(t, "alert-test:"+testAlertOperation, job.DedupKey)
	require.Equal(t, "alert_test", job.Kind)
	require.Equal(t, "pending", job.Status)
	require.Zero(t, job.ModelID)
	require.Zero(t, job.IncidentID)
	require.Zero(t, job.Generation)
	require.Zero(t, job.EventID)
	require.Equal(t, *now, job.CreatedAt)
	require.Equal(t, *now, job.NextAttemptAt)
	require.Empty(t, job.DeliveryReceipt)
	otherStore := NewZTAPIHealthStore(s.DB)
	otherStore.Now = s.Now
	duplicate, err := otherStore.EnqueueTestAlert(ctx, testAlertOperation, 77)
	require.NoError(t, err)
	require.Equal(t, job, duplicate)
	found, err := otherStore.GetTestAlert(ctx, strings.ToUpper(testAlertOperation))
	require.NoError(t, err)
	require.Equal(t, job, found)
	var audits []ZTAPIAuditEvent
	require.NoError(t, s.DB.Find(&audits).Error)
	require.Len(t, audits, 1)
	require.Equal(t, 42, audits[0].OperatorID)
	require.Equal(t, "model.health_test_alert", audits[0].Action)
	require.Zero(t, audits[0].ModelConfigID)
	var payload map[string]any
	require.NoError(t, common.UnmarshalJsonStr(audits[0].Payload, &payload))
	require.Equal(t, testAlertOperation, payload["operation_id"])
	require.EqualValues(t, job.ID, payload["outbox_id"])
	for _, table := range []any{&ZTAPIHealthState{}, &ZTAPIHealthRequest{}, &ZTAPIHealthEvent{}, &ZTAPIHealthIncident{}} {
		var count int64
		require.NoError(t, s.DB.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
	var unchanged ZTAPIModelConfig
	require.NoError(t, s.DB.First(&unchanged, cfg.ID).Error)
	require.Equal(t, cfg, unchanged)
	for range 12 {
		_, err = otherStore.EnqueueTestAlert(ctx, uuid.NewString(), 42)
		require.NoError(t, err)
	}
	_, err = otherStore.EnqueueTestAlert(ctx, testAlertOperation, 77)
	require.NoError(t, err)
}

func TestZTAPIHealthTestAlertInvalidInput(t *testing.T) {
	s, _, _ := healthFixture(t)
	ctx := context.Background()
	for _, operation := range []string{"", "invalid", uuid.Nil.String(), strings.ReplaceAll(testAlertOperation, "-", ""), "urn:uuid:" + testAlertOperation, " " + testAlertOperation} {
		_, err := s.EnqueueTestAlert(ctx, operation, 42)
		require.ErrorIs(t, err, ErrZTAPIHealthTestAlertInvalid)
		_, err = s.GetTestAlert(ctx, operation)
		require.ErrorIs(t, err, ErrZTAPIHealthTestAlertInvalid)
	}
	for _, operator := range []int{0, -1} {
		_, err := s.EnqueueTestAlert(ctx, testAlertOperation, operator)
		require.ErrorIs(t, err, ErrZTAPIHealthTestAlertInvalid)
	}
	_, err := s.GetTestAlert(ctx, testAlertOperation)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var count int64
	require.NoError(t, s.DB.Model(&ZTAPIHealthOutbox{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestZTAPIHealthTestAlertAuditRollback(t *testing.T) {
	s, _, _ := healthFixture(t)
	ctx := context.Background()
	require.NoError(t, s.DB.Migrator().DropTable(&ZTAPIAuditEvent{}))
	_, err := s.EnqueueTestAlert(ctx, testAlertOperation, 42)
	require.Error(t, err)
	var count int64
	require.NoError(t, s.DB.Model(&ZTAPIHealthOutbox{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, s.DB.AutoMigrate(&ZTAPIAuditEvent{}))
	_, err = s.EnqueueTestAlert(ctx, testAlertOperation, 42)
	require.NoError(t, err)
}

func TestZTAPIHealthTestAlertConcurrent(t *testing.T) {
	for _, sameOperation := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-operations", true: "same-operation"}[sameOperation], func(t *testing.T) {
			s, _, _ := healthFixture(t)
			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make(chan error, 16)
			ids := make(chan int64, 16)
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					store := NewZTAPIHealthStore(s.DB.Session(&gorm.Session{NewDB: true}))
					store.Now = s.Now
					op := testAlertOperation
					if !sameOperation {
						op = uuid.NewString()
					}
					<-start
					job, err := store.EnqueueTestAlert(context.Background(), op, 42)
					errs <- err
					if err == nil {
						ids <- job.ID
					}
				}()
			}
			close(start)
			wg.Wait()
			close(errs)
			close(ids)
			successes := 0
			for err := range errs {
				require.NoError(t, err)
				successes++
			}
			require.Equal(t, 16, successes)
			uniqueIDs := map[int64]struct{}{}
			for id := range ids {
				uniqueIDs[id] = struct{}{}
			}
			require.Len(t, uniqueIDs, map[bool]int{false: 16, true: 1}[sameOperation])
			for _, table := range []any{&ZTAPIHealthOutbox{}, &ZTAPIAuditEvent{}} {
				var count int64
				require.NoError(t, s.DB.Model(table).Count(&count).Error)
				require.EqualValues(t, map[bool]int64{false: 16, true: 1}[sameOperation], count)
			}
		})
	}
}

func TestZTAPIHealthTestAlertReceiptLeaseCAS(t *testing.T) {
	s, _, now := healthFixture(t)
	ctx := context.Background()
	_, err := s.EnqueueTestAlert(ctx, testAlertOperation, 42)
	require.NoError(t, err)
	jobs, err := s.ClaimOutbox(ctx, "alert_test", 1, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	old := jobs[0]
	require.ErrorIs(t, s.FinishOutboxWithReceipt(ctx, old.ID, "forged", "", 0, testAlertReceipt), ErrZTAPIHealthLeaseConflict)
	*now += 30
	require.ErrorIs(t, s.FinishOutboxWithReceipt(ctx, old.ID, old.LeaseToken, "", 0, testAlertReceipt), ErrZTAPIHealthLeaseConflict)
	jobs, err = s.ClaimOutbox(ctx, "alert_test", 1, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	job := jobs[0]
	require.NotEqual(t, old.LeaseToken, job.LeaseToken)
	require.ErrorIs(t, s.FinishOutboxWithReceipt(ctx, old.ID, old.LeaseToken, "", 0, testAlertReceipt), ErrZTAPIHealthLeaseConflict)
	require.NoError(t, s.FinishOutboxWithReceipt(ctx, job.ID, job.LeaseToken, "alert_transport_error", *now+60, ""))
	pending, err := s.GetTestAlert(ctx, testAlertOperation)
	require.NoError(t, err)
	require.Equal(t, "pending", pending.Status)
	require.Empty(t, pending.DeliveryReceipt)
	require.Zero(t, pending.DeliveredAt)
	require.Equal(t, "alert_transport_error", pending.LastError)
	*now += 60
	jobs, err = s.ClaimOutbox(ctx, "alert_test", 1, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	job = jobs[0]
	require.Equal(t, 3, job.Attempts)
	require.NoError(t, s.FinishOutboxWithReceipt(ctx, job.ID, job.LeaseToken, "", 0, testAlertReceipt))
	done, err := s.GetTestAlert(ctx, testAlertOperation)
	require.NoError(t, err)
	require.Equal(t, "done", done.Status)
	require.Equal(t, *now, done.DeliveredAt)
	require.Equal(t, testAlertReceipt, done.DeliveryReceipt)
	require.Empty(t, done.LastError)
	require.ErrorIs(t, s.FinishOutboxWithReceipt(ctx, job.ID, job.LeaseToken, "", 0, `{"ok":true,"result":{"message_id":999,"date":2000000000}}`), ErrZTAPIHealthLeaseConflict)
	duplicate, err := s.EnqueueTestAlert(ctx, testAlertOperation, 42)
	require.NoError(t, err)
	require.Equal(t, done, duplicate)
}

func TestZTAPIHealthOutboxReceiptValidation(t *testing.T) {
	s, _, now := healthFixture(t)
	ctx := context.Background()
	_, err := s.EnqueueTestAlert(ctx, testAlertOperation, 42)
	require.NoError(t, err)
	jobs, err := s.ClaimOutbox(ctx, "alert_test", 1, 30)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	job := jobs[0]
	for _, receipt := range []string{
		" ", "null", "[]", "{}", strings.Repeat(" ", 513),
		`{"ok":false,"result":{"message_id":1,"date":2}}`,
		`{"ok":true,"result":{"message_id":1,"date":2},"token":"SECRET"}`,
		`{"ok":true,"result":{"message_id":1,"date":2,"chat":{"id":123}}}`,
		`{"ok":true,"result":{"message_id":1}}`,
		`{"ok":true,"result":{"message_id":null,"date":2}}`,
		`{"ok":true,"result":{"message_id":1,"date":null}}`,
		`{"ok":true,"result":{"message_id":1.5,"date":2}}`,
		`{"ok":true,"result":{"message_id":"1","date":2}}`,
		`{"ok":true,"result":{"message_id":9223372036854775808,"date":2}}`,
		`{"ok":true,"result":{"message_id":0,"date":2}}`,
		`{"ok":true,"result":{"message_id":-1,"date":2}}`,
		`{"ok":true,"result":{"message_id":1,"date":-1}}`,
		`{"ok":true,"result":{"message_id":1,"date":2}} {}`,
	} {
		require.ErrorIs(t, s.FinishOutboxWithReceipt(ctx, job.ID, job.LeaseToken, "", 0, receipt), ErrZTAPIHealthInvalidReceipt, receipt)
	}
	require.ErrorIs(t, s.FinishOutboxWithReceipt(ctx, job.ID, job.LeaseToken, "alert_transport_error", *now+60, testAlertReceipt), ErrZTAPIHealthInvalidReceipt)
	unchanged, err := s.GetTestAlert(ctx, testAlertOperation)
	require.NoError(t, err)
	require.Equal(t, job, *unchanged)
	require.NoError(t, s.FinishOutboxWithReceipt(ctx, job.ID, job.LeaseToken, "", 0, " {\"result\":{\"date\":2000000000,\"message_id\":123},\"ok\":true} "))
	done, err := s.GetTestAlert(ctx, testAlertOperation)
	require.NoError(t, err)
	require.Equal(t, testAlertReceipt, done.DeliveryReceipt)
	zeroDate, err := ztapiHealthDeliveryReceipt(`{"ok":true,"result":{"message_id":9223372036854775807,"date":0}}`)
	require.NoError(t, err)
	require.Equal(t, `{"ok":true,"result":{"message_id":9223372036854775807,"date":0}}`, zeroDate)
}

func TestZTAPIHealthOutboxReceiptUnpublishAndLegacy(t *testing.T) {
	s, _, now := healthFixture(t)
	ctx := context.Background()
	for _, kind := range []string{"unpublish", "alert", "coverage"} {
		job := ZTAPIHealthOutbox{Kind: kind, DedupKey: kind, Status: "leased", LeaseToken: "token", LeaseUntil: *now + 30}
		require.NoError(t, s.DB.Create(&job).Error)
		err := s.FinishOutboxWithReceipt(ctx, job.ID, job.LeaseToken, "", 0, testAlertReceipt)
		if kind == "unpublish" {
			require.ErrorIs(t, err, ErrZTAPIHealthLeaseConflict)
			require.ErrorIs(t, s.FinishOutbox(ctx, job.ID, job.LeaseToken, "", 0), ErrZTAPIHealthLeaseConflict)
		} else {
			require.NoError(t, err)
		}
	}
	job := ZTAPIHealthOutbox{Kind: "alert", DedupKey: "legacy-webhook", Status: "leased", LeaseToken: "token", LeaseUntil: *now + 30}
	require.NoError(t, s.DB.Create(&job).Error)
	require.NoError(t, s.FinishOutbox(ctx, job.ID, job.LeaseToken, "", 0))
	require.NoError(t, s.DB.First(&job, job.ID).Error)
	require.Equal(t, "done", job.Status)
	require.Empty(t, job.DeliveryReceipt)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := s.EnqueueTestAlert(cancelled, testAlertOperation, 42)
	require.True(t, errors.Is(err, context.Canceled))
}

func TestZTAPIHealthOutboxReceiptRetryRechecksLease(t *testing.T) {
	s, _, now := healthFixture(t)
	ctx := context.Background()
	job := ZTAPIHealthOutbox{Kind: "alert", DedupKey: "expiry-during-retry", Status: "leased", LeaseToken: "token", LeaseUntil: *now + 30}
	require.NoError(t, s.DB.Create(&job).Error)
	var once sync.Once
	const callback = "test:expire-lease-during-retry"
	require.NoError(t, s.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		once.Do(func() {
			*now += 31
			tx.AddError(errors.New("database is locked"))
		})
	}))
	t.Cleanup(func() { s.DB.Callback().Update().Remove(callback) })
	require.ErrorIs(t, s.FinishOutboxWithReceipt(ctx, job.ID, job.LeaseToken, "", 0, testAlertReceipt), ErrZTAPIHealthLeaseConflict)
	var unchanged ZTAPIHealthOutbox
	require.NoError(t, s.DB.First(&unchanged, job.ID).Error)
	require.Equal(t, job, unchanged)
}

func TestZTAPIHealthOutboxReceiptMigration(t *testing.T) {
	s, _, _ := healthFixture(t)
	job := ZTAPIHealthOutbox{Kind: "alert", DedupKey: "pre-receipt", Status: "done", Attempts: 3, DeliveredAt: 123}
	require.NoError(t, s.DB.Create(&job).Error)
	require.NoError(t, s.DB.Migrator().DropColumn(&ZTAPIHealthOutbox{}, "DeliveryReceipt"))
	require.NoError(t, MigrateZTAPIHealth(s.DB))
	var after ZTAPIHealthOutbox
	require.NoError(t, s.DB.First(&after, job.ID).Error)
	require.Equal(t, job, after)
	require.NoError(t, MigrateZTAPIHealth(s.DB))
}

func TestZTAPIHealthTestAlertProcess(t *testing.T) {
	path := os.Getenv("ZTAPI_TEST_ALERT_CHILD_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	start, err := strconv.ParseInt(os.Getenv("ZTAPI_TEST_ALERT_CHILD_START"), 10, 64)
	require.NoError(t, err)
	time.Sleep(time.Until(time.Unix(0, start)))
	store := NewZTAPIHealthStore(db)
	store.Now = func() time.Time { return time.Unix(2_000_000_000, 0) }
	job, err := store.EnqueueTestAlert(context.Background(), os.Getenv("ZTAPI_TEST_ALERT_CHILD_OPERATION"), 42)
	require.NoError(t, err)
	fmt.Printf("RESULT:job:%d\n", job.ID)
}

func TestZTAPIHealthTestAlertAcrossProcesses(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			s, _, _ := healthFixture(t)
			var files []struct {
				Name string
				File string
			}
			require.NoError(t, s.DB.Raw("PRAGMA database_list").Scan(&files).Error)
			var path string
			for _, file := range files {
				if file.Name == "main" {
					path = file.File
				}
			}
			require.NotEmpty(t, path)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now().Add(2 * time.Second).UnixNano()
			var wg sync.WaitGroup
			outputs := make(chan string, 8)
			errs := make(chan error, 8)
			for i := 0; i < 8; i++ {
				op := testAlertOperation
				if !same {
					op = uuid.NewString()
				}
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZTAPIHealthTestAlertProcess$", "-test.count=1")
				cmd.Env = append(os.Environ(), "ZTAPI_TEST_ALERT_CHILD_DB="+path, "ZTAPI_TEST_ALERT_CHILD_OPERATION="+op, "ZTAPI_TEST_ALERT_CHILD_START="+strconv.FormatInt(start, 10))
				wg.Add(1)
				go func() { defer wg.Done(); output, err := cmd.CombinedOutput(); outputs <- string(output); errs <- err }()
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
			require.Equal(t, 8, strings.Count(all.String(), "RESULT:job:"))
			for _, table := range []any{&ZTAPIHealthOutbox{}, &ZTAPIAuditEvent{}} {
				var count int64
				require.NoError(t, s.DB.Model(table).Count(&count).Error)
				require.EqualValues(t, map[bool]int64{false: 8, true: 1}[same], count)
			}
		})
	}
}
