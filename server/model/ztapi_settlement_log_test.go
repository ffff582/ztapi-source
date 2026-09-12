package model

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupZTAPISettlementLogTest(t *testing.T) (*gorm.DB, *gorm.DB, ZTAPIRequestSettlement, Log) {
	t.Helper()
	open := func(name string) *gorm.DB {
		db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), name)+"?_pragma=busy_timeout(10000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		pool, err := db.DB()
		require.NoError(t, err)
		pool.SetMaxOpenConns(8)
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	mainDB, logDB := open("main.db"), open("logs.db")
	previousMain, previousLog := DB, LOG_DB
	DB, LOG_DB = mainDB, logDB
	t.Cleanup(func() { DB, LOG_DB = previousMain, previousLog })
	require.NoError(t, mainDB.AutoMigrate(&Channel{}, &ZTAPIRequestSettlement{}, &ZTAPIRequestAttempt{}))
	require.NoError(t, logDB.AutoMigrate(&Log{}))
	require.NoError(t, MigrateZTAPISettlementLogOutbox(mainDB))
	require.NoError(t, MigrateLogDBZTAPISettlementLogs(logDB))
	require.NoError(t, mainDB.Create(&Channel{Id: 11, Name: "settlement-log-test", UsedQuota: 40}).Error)
	row := ZTAPIRequestSettlement{OperationID: "log-operation-1", RequestID: "log-request-1", UserID: 7, TokenID: 9, PublicModel: "zt-test", Status: ZTAPISettlementSettled, ChargedQuota: 12, FinalAttempt: 1}
	require.NoError(t, mainDB.Create(&row).Error)
	require.NoError(t, mainDB.Create(&ZTAPIRequestAttempt{SettlementID: row.ID, Attempt: 1, ChannelID: 11, CredentialVersion: "test-version", Protocol: "chat", UpstreamRequestID: "upstream-request-1"}).Error)
	log := Log{UserId: 7, TokenId: 9, CreatedAt: 1700000000, Type: LogTypeConsume,
		Content: "managed usage settlement", Username: "customer", TokenName: "test-token", ModelName: "zt-test",
		Quota: 12, PromptTokens: 10, CompletionTokens: 2, UseTime: 3, IsStream: true,
		ChannelId: 11, Group: "default", RequestId: "log-request-1", UpstreamRequestId: "upstream-request-1",
		Other: `{"billing_source":"wallet","model_ratio":1}`}
	return mainDB, logDB, row, log
}

func enqueueZTAPISettlementLogTest(db *gorm.DB, row *ZTAPIRequestSettlement, log Log) error {
	return db.Transaction(func(tx *gorm.DB) error { return EnqueueZTAPISettlementLogTx(tx, row, log) })
}

func TestZTAPISettlementLogEnqueueCommitsWithCanonicalTransaction(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	require.NoError(t, db.Model(&row).Update("status", ZTAPISettlementReserved).Error)
	rollback := errors.New("rollback canonical finalization")
	err := db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Model(&row).Update("status", ZTAPISettlementSettled).Error)
		row.Status = ZTAPISettlementSettled
		if err := EnqueueZTAPISettlementLogTx(tx, &row, log); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var stored ZTAPIRequestSettlement
	require.NoError(t, db.First(&stored, row.ID).Error)
	require.Equal(t, ZTAPISettlementReserved, stored.Status)
	var count int64
	require.NoError(t, db.Model(&ZTAPISettlementLogOutbox{}).Count(&count).Error)
	require.Zero(t, count)
	var channel Channel
	require.NoError(t, db.First(&channel, 11).Error)
	require.Equal(t, int64(40), channel.UsedQuota)
	require.NoError(t, logDB.Model(&Log{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestZTAPISettlementLogEnqueueReplayFreezesPayloadAndCountsChannelOnce(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	var channel Channel
	require.NoError(t, db.First(&channel, 11).Error)
	require.Equal(t, int64(52), channel.UsedQuota)
	var count int64
	require.NoError(t, db.Model(&ZTAPISettlementLogOutbox{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	log.Username = "changed metadata"
	require.ErrorIs(t, enqueueZTAPISettlementLogTest(db, &row, log), ErrZTAPISettlementLogConflict)
	n, err := ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var delivered Log
	require.NoError(t, logDB.First(&delivered).Error)
	require.Equal(t, "managed usage settlement", delivered.Content)
	require.Equal(t, "upstream-request-1", delivered.UpstreamRequestId)
	require.Equal(t, "customer", delivered.Username)
	require.Equal(t, 10, delivered.PromptTokens)
	require.Equal(t, 2, delivered.CompletionTokens)
	require.Equal(t, 12, delivered.Quota)
	require.Equal(t, log.Other, delivered.Other)
	require.Equal(t, log.CreatedAt, delivered.CreatedAt)
	require.True(t, delivered.IsStream)
	n, err = ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestZTAPISettlementLogLogAndReceiptRollbackTogether(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	require.NoError(t, logDB.Exec(`CREATE TRIGGER reject_consumption_log BEFORE INSERT ON logs BEGIN SELECT RAISE(ABORT, 'injected log failure'); END`).Error)
	n, err := ProcessPendingZTAPISettlementLogs(10)
	require.Error(t, err)
	require.Zero(t, n)
	var count int64
	require.NoError(t, logDB.Model(&ZTAPISettlementLogReceipt{}).Count(&count).Error)
	require.Zero(t, count, "receipt must not commit without log")
	require.NoError(t, logDB.Model(&Log{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, logDB.Exec("DROP TRIGGER reject_consumption_log").Error)
	n, err = ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestZTAPISettlementLogCommitBeforeAckReplaysWithoutDuplicate(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_log_ack BEFORE UPDATE ON ztapi_settlement_log_outboxes WHEN NEW.delivered_at > 0 BEGIN SELECT RAISE(ABORT, 'injected ack failure'); END`).Error)
	n, err := ProcessPendingZTAPISettlementLogs(10)
	require.Error(t, err)
	require.Zero(t, n)
	var count int64
	require.NoError(t, logDB.Model(&Log{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.NoError(t, logDB.Model(&ZTAPISettlementLogReceipt{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	var pending ZTAPISettlementLogOutbox
	require.NoError(t, db.First(&pending, "operation_id = ?", row.OperationID).Error)
	require.Zero(t, pending.DeliveredAt)
	require.NoError(t, db.Exec("DROP TRIGGER reject_log_ack").Error)
	// Discard ORM sessions: retry has only the two committed databases.
	DB, LOG_DB = db.Session(&gorm.Session{NewDB: true}), logDB.Session(&gorm.Session{NewDB: true})
	n, err = ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, logDB.Model(&Log{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.NoError(t, db.First(&pending, "operation_id = ?", row.OperationID).Error)
	require.Positive(t, pending.DeliveredAt)
	require.Positive(t, pending.LogID)
}

func TestZTAPISettlementLogConcurrentWorkersCannotDuplicate(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ProcessPendingZTAPISettlementLogs(10)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var count int64
	require.NoError(t, logDB.Model(&Log{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.NoError(t, logDB.Model(&ZTAPISettlementLogReceipt{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestZTAPISettlementLogRejectsWrongOwnerAndUnsettledRows(t *testing.T) {
	for _, change := range []string{"user", "token", "request", "model", "quota", "status", "type", "id"} {
		t.Run(change, func(t *testing.T) {
			db, _, row, log := setupZTAPISettlementLogTest(t)
			switch change {
			case "user":
				log.UserId++
			case "token":
				log.TokenId++
			case "request":
				log.RequestId = "other-request"
			case "model":
				log.ModelName = "other-model"
			case "quota":
				log.Quota++
			case "status":
				row.Status = ZTAPISettlementPending
			case "type":
				log.Type = LogTypeTopup
			case "id":
				log.Id = 123
			}
			require.Error(t, enqueueZTAPISettlementLogTest(db, &row, log))
			var count int64
			require.NoError(t, db.Model(&ZTAPISettlementLogOutbox{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestZTAPISettlementLogMissingChannelRollsBackOutbox(t *testing.T) {
	db, _, row, log := setupZTAPISettlementLogTest(t)
	log.ChannelId = 99
	require.Error(t, enqueueZTAPISettlementLogTest(db, &row, log))
	var count int64
	require.NoError(t, db.Model(&ZTAPISettlementLogOutbox{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestZTAPISettlementLogUpstreamIdentityComesFromFinalStoredAttempt(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	log.UpstreamRequestId = "untrusted-request"
	row.FinalAttempt = 2
	require.NoError(t, db.Create(&Channel{Id: 12, Name: "final-channel"}).Error)
	require.NoError(t, db.Create(&ZTAPIRequestAttempt{SettlementID: row.ID, Attempt: 2, ChannelID: 12, CredentialVersion: "test-version", Protocol: "chat", UpstreamRequestID: "trusted-final-request"}).Error)
	log.ChannelId = 12
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	_, err := ProcessPendingZTAPISettlementLogs(1)
	require.NoError(t, err)
	var stored Log
	require.NoError(t, logDB.First(&stored).Error)
	require.Equal(t, "trusted-final-request", stored.UpstreamRequestId)
	require.Equal(t, 12, stored.ChannelId)
}

func TestZTAPISettlementLogMissingAttemptDoesNotTrustCallerUpstreamID(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	row.FinalAttempt = 2
	log.UpstreamRequestId = "untrusted-request"
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	_, err := ProcessPendingZTAPISettlementLogs(1)
	require.NoError(t, err)
	var stored Log
	require.NoError(t, logDB.First(&stored).Error)
	require.Empty(t, stored.UpstreamRequestId)
}

func TestZTAPISettlementLogExcludesPromptOutputAndArbitraryMetadata(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	log.Content = "private customer prompt and generated output"
	log.ChannelName = "untrusted channel label"
	log.Other = `{"billing_source":"wallet","billing_status":"settled","publication_version":2,"price_source_version":3,"usage_semantic":"openai","prompt":"private prompt","output":"private output","headers":{"authorization":"secret"},"billing_dimensions":[{"dimension":"input_tokens","quantity":10,"unit":"token","quote_state":"quoted","unit_price_usd":"0.000001","output":"private nested output"}]}`
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	var outbox ZTAPISettlementLogOutbox
	require.NoError(t, db.First(&outbox).Error)
	require.NotContains(t, outbox.PayloadJSON, "private")
	require.NotContains(t, outbox.PayloadJSON, "secret")
	require.NotContains(t, outbox.PayloadJSON, "untrusted channel label")
	_, err := ProcessPendingZTAPISettlementLogs(1)
	require.NoError(t, err)
	var stored Log
	require.NoError(t, logDB.First(&stored).Error)
	require.JSONEq(t, `{"billing_source":"wallet","billing_status":"settled","publication_version":2,"price_source_version":3,"usage_semantic":"openai","billing_dimensions":[{"dimension":"input_tokens","quantity":10,"unit":"token","quote_state":"quoted","unit_price_usd":"0.000001"}]}`, stored.Other)
}

func TestZTAPISettlementLogMigrationHooksAreSeparateAndRepeatable(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	require.True(t, db.Migrator().HasTable(&ZTAPISettlementLogOutbox{}))
	require.False(t, db.Migrator().HasTable(&ZTAPISettlementLogReceipt{}))
	require.True(t, logDB.Migrator().HasTable(&ZTAPISettlementLogReceipt{}))
	require.False(t, logDB.Migrator().HasTable(&ZTAPISettlementLogOutbox{}))
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	require.NoError(t, MigrateZTAPISettlementLogOutbox(db))
	require.NoError(t, MigrateLogDBZTAPISettlementLogs(logDB))
	n, err := ProcessPendingZTAPISettlementLogs(1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, MigrateZTAPISettlementLogOutbox(db))
	require.NoError(t, MigrateLogDBZTAPISettlementLogs(logDB))
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	n, err = ProcessPendingZTAPISettlementLogs(1)
	require.NoError(t, err)
	require.Zero(t, n)
	var channel Channel
	require.NoError(t, db.First(&channel, 11).Error)
	require.Equal(t, int64(52), channel.UsedQuota)
}

func TestZTAPISettlementLogReceiptFailureRollsBackInsertedLog(t *testing.T) {
	db, logDB, row, log := setupZTAPISettlementLogTest(t)
	require.NoError(t, enqueueZTAPISettlementLogTest(db, &row, log))
	require.NoError(t, logDB.Exec(`CREATE TRIGGER reject_log_receipt BEFORE UPDATE ON ztapi_settlement_log_receipts BEGIN SELECT RAISE(ABORT, 'receipt update failure'); END`).Error)
	_, err := ProcessPendingZTAPISettlementLogs(1)
	require.Error(t, err)
	var count int64
	require.NoError(t, logDB.Model(&Log{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, logDB.Model(&ZTAPISettlementLogReceipt{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, logDB.Exec("DROP TRIGGER reject_log_receipt").Error)
	n, err := ProcessPendingZTAPISettlementLogs(1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
