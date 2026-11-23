package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/support"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestFinanceSupportStoreCannotWriteMigrateOrCreateMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "support # & store.sqlite")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE sentinel (value INTEGER)").Error)
	require.NoError(t, db.Exec("INSERT INTO sentinel VALUES (7)").Error)
	raw, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	read, err := openFinanceSupportStore(path)
	require.NoError(t, err)
	readRaw, err := read.DB()
	require.NoError(t, err)
	var value int
	require.NoError(t, read.Raw("SELECT value FROM sentinel").Scan(&value).Error)
	require.Equal(t, 7, value)
	require.Error(t, read.Exec("INSERT INTO sentinel VALUES (8)").Error)
	require.Error(t, read.Exec("CREATE TABLE forbidden (id INTEGER)").Error)
	require.NoError(t, readRaw.Close())
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	_, err = openFinanceSupportStore(missing)
	require.Error(t, err)
	_, err = os.Stat(missing)
	require.True(t, os.IsNotExist(err))
}

func TestFinanceSupportReaderIsOptionalAndOutagesAreRetryable(t *testing.T) {
	require.Nil(t, financeSupportReader(""))
	path := filepath.Join(t.TempDir(), "support.sqlite")
	reader := financeSupportReader(path)
	require.NotNil(t, reader)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	_, err := reader(context.Background(), start, start.AddDate(0, 1, 0))
	require.Error(t, err)
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
	// A readable but incompatible database must not be migrated into a support
	// store; return an unavailable source so the HTTP layer can remain healthy.
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE sentinel (value INTEGER)").Error)
	_, err = reader(context.Background(), start, start.AddDate(0, 1, 0))
	require.Error(t, err)
	require.False(t, db.Migrator().HasTable("support_calls"))
	raw, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, raw.Close())
}

func TestFinanceSiteAndTelegramReadersAreCombinedOnceAndFailClosed(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	paths := []string{filepath.Join(t.TempDir(), "site.db"), filepath.Join(t.TempDir(), "telegram.db")}
	for i, path := range paths {
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		store, err := support.NewStore(db)
		require.NoError(t, err)
		conv, err := store.CreateConversation(ctx, 8, "combined-conversation", start)
		require.NoError(t, err)
		environment := "production"
		if i == 1 {
			environment = "telegram-production"
		}
		job, err := store.BeginCall(ctx, support.BeginInput{UserID: 8, ConversationID: conv.ID, IdempotencyKey: "combined-message", Content: "private", Model: "flash", ChannelID: 8, Supplier: "xcloud", Environment: environment, Now: start.Add(time.Minute)})
		require.NoError(t, err)
		require.NoError(t, store.MarkDispatched(ctx, job.CallID))
		in, out, cache, quote := int64(20), int64(2), int64(0), "0.000001"
		_, err = store.FinishCall(ctx, job.CallID, support.ProviderResult{Content: "reply", Outcome: "completed", InputTokens: &in, OutputTokens: &out, CacheReadTokens: &cache, QuoteCostU: &quote, QuoteEvidence: "fixture-quote"}, start.Add(2*time.Minute))
		require.NoError(t, err)
		raw, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, raw.Close())
	}
	reader := financeSupportReaders(paths[0], paths[1], paths[0])
	rows, err := reader(ctx, start, start.AddDate(0, 1, 0))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NotEqual(t, rows[0].CallID, rows[1].CallID)
	require.Equal(t, "telegram-production", rows[1].Environment)
	require.NoError(t, os.Rename(paths[1], paths[1]+".offline"))
	_, err = reader(ctx, start, start.AddDate(0, 1, 0))
	require.Error(t, err, "missing Telegram cost source must not silently remove costs")
	require.Nil(t, financeSupportReaders("", ""))
}

func TestFinanceSupportReaderRejectsUnboundedOrCrossMonthWindows(t *testing.T) {
	reader := financeSupportReader(filepath.Join(t.TempDir(), "missing.sqlite"))
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	for _, end := range []time.Time{start, start.Add(-time.Second), start.AddDate(0, 1, 0).Add(time.Second)} {
		_, err := reader(context.Background(), start, end)
		require.ErrorContains(t, err, "support finance range")
	}
}

func TestFinanceSupportReaderRecoversAndReadsRealMonthlyReceiptsWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "support.sqlite")
	reader := financeSupportReader(path)
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	ctx := context.Background()
	_, err := reader(ctx, start, start.AddDate(0, 1, 0))
	require.Error(t, err)
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	store, err := support.NewStore(db)
	require.NoError(t, err)
	conv, err := store.CreateConversation(ctx, 8, "finance-reader-conversation", start.Add(-time.Minute))
	require.NoError(t, err)
	in := support.BeginInput{UserID: 8, ConversationID: conv.ID, IdempotencyKey: "finance-call-completed", Content: "private message never enters finance", Model: "flash", ChannelID: 4, Supplier: "xcloud", Environment: "production", Now: start.Add(-time.Second).UTC()}
	job, err := store.BeginCall(ctx, in)
	require.NoError(t, err)
	require.NoError(t, store.MarkDispatched(ctx, job.CallID))
	input, output, cache, quote := int64(100), int64(0), int64(20), "0.000000200001"
	_, err = store.FinishCall(ctx, job.CallID, support.ProviderResult{Content: "reply", Outcome: "completed", InputTokens: &input, OutputTokens: &output, CacheReadTokens: &cache, QuoteCostU: &quote, QuoteEvidence: "frozen-quote"}, start.Add(time.Second).UTC())
	require.NoError(t, err)
	in.IdempotencyKey, in.Now = "finance-call-pending", start.Add(time.Minute).UTC()
	pending, err := store.BeginCall(ctx, in)
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	rows, err := reader(ctx, start, start.AddDate(0, 1, 0))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, job.CallID, rows[0].CallID)
	require.True(t, rows[0].StartedAt.Equal(start.Add(-time.Second)))
	require.Equal(t, quote, *rows[0].QuoteCostU)
	require.Equal(t, "inclusive", rows[0].InputTokenScope)
	require.Equal(t, pending.CallID, rows[1].CallID)
	require.Nil(t, rows[1].CompletedAt)
	require.Nil(t, rows[1].QuoteCostU)
	require.Nil(t, rows[1].InputTokens)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
