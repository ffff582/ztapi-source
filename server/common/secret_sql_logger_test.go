package common

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openSecretSQLLoggerTestDB(t *testing.T, dialector gorm.Dialector, dryRun bool, output *bytes.Buffer) *gorm.DB {
	t.Helper()
	databaseLogger := NewSecretSQLLogger(gormlogger.New(log.New(output, "", 0), gormlogger.Config{LogLevel: gormlogger.Info, SlowThreshold: time.Millisecond}))
	db, err := gorm.Open(dialector, &gorm.Config{Logger: databaseLogger, DisableAutomaticPing: dryRun, DryRun: dryRun, SkipDefaultTransaction: dryRun})
	require.NoError(t, err)
	require.NoError(t, db.Use(databaseLogger))
	if !dryRun {
		pool, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = pool.Close() })
	}
	return db.Debug()
}

func TestSecretSQLLoggerHidesParametersAcrossDialects(t *testing.T) {
	for _, test := range []struct {
		name        string
		dialector   gorm.Dialector
		placeholder string
	}{
		{"sqlite", sqlite.Open(":memory:"), "?"},
		{"mysql", mysql.New(mysql.Config{DSN: "fictional:fictional@tcp(127.0.0.1:1)/fictional", SkipInitializeWithVersion: true}), "?"},
		{"postgres", postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=fictional password=fictional dbname=fictional sslmode=disable"}), "$1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			db := openSecretSQLLoggerTestDB(t, test.dialector, true, &output)
			err := db.Exec("UPDATE secret_sql_fixtures SET credential = ? WHERE id = ?", "fictional-opaque-bound-credential", 987654321).Error
			require.NoError(t, err)
			require.Contains(t, output.String(), "UPDATE secret_sql_fixtures SET credential = "+test.placeholder)
			require.NotContains(t, output.String(), "fictional-opaque-bound-credential")
			require.NotContains(t, output.String(), "987654321")
		})
	}
}

func TestSecretSQLLoggerSQLiteScanDoesNotBypassParameterHiding(t *testing.T) {
	var output bytes.Buffer
	db := openSecretSQLLoggerTestDB(t, sqlite.Open(filepath.Join(t.TempDir(), "sql-secrets.db")), false, &output)
	for _, secret := range []string{
		"fictional-opaque-bound-credential",
		`{"api_key":"fictional-json-sql-credential"}`,
		"Authorization: Bearer fictional-header-sql-credential",
		"OPENAI_API_KEY=fictional-env-sql-credential",
	} {
		output.Reset()
		var actual string
		require.NoError(t, db.Raw("SELECT ? AS secret_value", secret).Scan(&actual).Error)
		require.Equal(t, secret, actual, "must preserve executed bind parameters")
		require.Contains(t, output.String(), "SELECT ? AS secret_value")
		require.NotContains(t, output.String(), secret)
		require.NotContains(t, output.String(), "fictional-")
	}
	output.Reset()
	var result string
	err := db.Raw("SELECT ? FROM missing_secret_sql_table", "fictional-error-bound-credential").Scan(&result).Error
	require.Error(t, err)
	require.Contains(t, output.String(), "missing_secret_sql_table")
	require.Contains(t, output.String(), "no such table")
	require.NotContains(t, output.String(), "fictional-error-bound-credential")
}

func TestSecretSQLLoggerRedactsLateScanErrorsWithoutChangingReturnedError(t *testing.T) {
	for _, test := range []struct {
		name   string
		secret string
		bind   func(string) any
	}{
		{"string", "fictional-late-scan-credential", func(value string) any { return value }},
		{"pointer", "fictional-late-scan-credential", func(value string) any { return &value }},
		{"bytes", "fictional-late-scan-credential", func(value string) any { return []byte(value) }},
		{"bytes_pointer", "fictional-late-scan-credential", func(value string) any { bytes := []byte(value); return &bytes }},
		{"escaped", "fictional-late-scan-\"credential\"\\tail\nline", func(value string) any { return value }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			db := openSecretSQLLoggerTestDB(t, sqlite.Open(filepath.Join(t.TempDir(), "late-scan-errors.db")), false, &output)
			var actual int
			err := db.Raw("SELECT ? AS secret_value", test.bind(test.secret)).Scan(&actual).Error
			require.Error(t, err)
			require.Contains(t, err.Error(), "fictional-late-scan-", "caller must retain the original conversion error")
			require.Contains(t, err.Error(), "converting driver.Value")
			require.NotContains(t, output.String(), "fictional-late-scan-", "late errors must not bypass bound-value filtering")
			require.Contains(t, output.String(), "SELECT ? AS secret_value")
			require.Contains(t, output.String(), "converting driver.Value")
			require.Contains(t, output.String(), "[rows:1]")
		})
	}
}

func TestSecretSQLLoggerRedactsDriverErrorsWithoutChangingReturnedError(t *testing.T) {
	var output bytes.Buffer
	db := openSecretSQLLoggerTestDB(t, sqlite.Open(filepath.Join(t.TempDir(), "sql-errors.db")), false, &output)
	original := errors.New("duplicate entry fictional-opaque-error-credential; SQLSTATE 23000; request_id=req-sql-17")
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Before("secret_sql:template").Register("test:driver-error", func(tx *gorm.DB) { tx.AddError(original) }))
	err := db.Exec("SELECT ?", "fictional-opaque-error-credential").Error
	require.ErrorIs(t, err, original)
	require.Contains(t, err.Error(), "fictional-opaque-error-credential", "caller must retain original error semantics")
	require.Contains(t, output.String(), "SQLSTATE 23000")
	require.Contains(t, output.String(), "req-sql-17")
	require.NotContains(t, output.String(), "fictional-opaque-error-credential")
}

func TestSecretSQLLoggerRedactsPointerParametersInDriverErrors(t *testing.T) {
	var output bytes.Buffer
	db := openSecretSQLLoggerTestDB(t, sqlite.Open(filepath.Join(t.TempDir(), "sql-pointer-errors.db")), false, &output)
	credential := "fictional-pointer-error-credential"
	original := errors.New("duplicate entry " + credential + "; SQLSTATE 23000")
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Before("secret_sql:template").Register("test:pointer-driver-error", func(tx *gorm.DB) { tx.AddError(original) }))
	require.ErrorIs(t, db.Exec("SELECT ?", &credential).Error, original)
	require.NotContains(t, output.String(), credential)
	require.Contains(t, output.String(), "SQLSTATE 23000")
}

func TestSecretSQLLoggerRedactsMessagesAndPreservesLogMode(t *testing.T) {
	var output bytes.Buffer
	l := NewSecretSQLLogger(gormlogger.New(log.New(&output, "", 0), gormlogger.Config{LogLevel: gormlogger.Info}))
	ctx := context.Background()
	for _, write := range []func(context.Context, string, ...interface{}){l.Info, l.Warn, l.Error} {
		write(ctx, "Authorization: Bearer %s prompt_tokens=123 cost=0.125 request_id=req-sql-17", "fictional-sql-message-credential")
	}
	require.NotContains(t, output.String(), "fictional-sql-message-credential")
	for _, safe := range []string{"prompt_tokens=123", "cost=0.125", "req-sql-17"} {
		require.Contains(t, output.String(), safe)
	}
	output.Reset()
	l.LogMode(gormlogger.Silent).Trace(ctx, time.Now(), func() (string, int64) { return "SELECT 1", 1 }, nil)
	require.Empty(t, output.String())
	l.LogMode(gormlogger.Info).Trace(ctx, time.Now(), func() (string, int64) { return "SELECT ?", 1 }, errors.New("api_key=fictional-trace-credential; query failed"))
	require.NotContains(t, output.String(), "fictional-trace-credential")
	require.True(t, strings.Contains(output.String(), "query failed"))
}
