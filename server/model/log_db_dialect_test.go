package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitLogDB reuses the main database when LOG_SQL_DSN is unset. chooseDB — the
// only place that assigns common.LogSqlType — is skipped on that path, so the
// value silently stayed at its SQLite default while LOG_DB pointed at MySQL.
// Callers branching on LogSqlType then emitted SQLite SQL against MySQL:
//
//	Error 1305: FUNCTION ztapi.strftime does not exist
//
// The existing overviewDayExpression unit test cannot catch this because it
// passes the dialect in explicitly; only the wiring is wrong.
func TestInitLogDBSyncsLogSqlTypeWhenReusingMainDatabase(t *testing.T) {
	t.Setenv("LOG_SQL_DSN", "")

	originalDB := DB
	originalLogDB := LOG_DB
	originalLogSqlType := common.LogSqlType
	t.Cleanup(func() {
		DB = originalDB
		LOG_DB = originalLogDB
		common.LogSqlType = originalLogSqlType
	})

	db, err := gorm.Open(sqlite.Open("file:logdialect?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db

	// Seed a deliberately wrong value: if InitLogDB does not sync, this stale
	// value survives and the assertion below fails.
	common.LogSqlType = common.DatabaseTypePostgreSQL

	if err := InitLogDB(); err != nil {
		t.Fatalf("InitLogDB: %v", err)
	}

	if LOG_DB != DB {
		t.Fatal("LOG_DB must reuse the main database when LOG_SQL_DSN is unset")
	}

	want := DB.Dialector.Name()
	if common.LogSqlType != want {
		t.Fatalf("common.LogSqlType = %q, want %q (the main database dialect)",
			common.LogSqlType, want)
	}
}

// The dialect string must be usable by callers directly — the DatabaseType
// constants and GORM's dialector names have to stay aligned.
func TestDatabaseTypeConstantsMatchGormDialectNames(t *testing.T) {
	tests := []struct {
		name      string
		dialector gorm.Dialector
		want      string
	}{
		{
			name:      "sqlite",
			dialector: sqlite.Open("file:dialectname?mode=memory&cache=shared"),
			want:      common.DatabaseTypeSQLite,
		},
		{
			name: "mysql",
			dialector: mysql.New(mysql.Config{
				DSN:                       "user:pass@tcp(localhost:3306)/ztapi",
				SkipInitializeWithVersion: true,
			}),
			want: common.DatabaseTypeMySQL,
		},
		{
			name: "postgresql",
			dialector: postgres.New(postgres.Config{
				DSN: "host=localhost user=ztapi password=ztapi dbname=ztapi port=5432 sslmode=disable",
			}),
			want: common.DatabaseTypePostgreSQL,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(test.dialector, &gorm.Config{
				DisableAutomaticPing: true,
				DryRun:               true,
				Logger:               logger.Default.LogMode(logger.Silent),
			})
			if err != nil {
				t.Fatalf("open %s dry-run database: %v", test.name, err)
			}
			if got := db.Dialector.Name(); got != test.want {
				t.Fatalf("%s dialector name = %q, want %q", test.name, got, test.want)
			}
		})
	}
}

func TestInitLogDBSyncsMySQLDialectWhenReusingMainDatabase(t *testing.T) {
	t.Setenv("LOG_SQL_DSN", "")

	originalDB := DB
	originalLogDB := LOG_DB
	originalLogSqlType := common.LogSqlType
	t.Cleanup(func() {
		DB = originalDB
		LOG_DB = originalLogDB
		common.LogSqlType = originalLogSqlType
	})

	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:pass@tcp(localhost:3306)/ztapi",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		DryRun:               true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open MySQL dry-run database: %v", err)
	}
	DB = db
	LOG_DB = nil
	common.LogSqlType = common.DatabaseTypeSQLite

	if err := InitLogDB(); err != nil {
		t.Fatalf("InitLogDB: %v", err)
	}
	if LOG_DB != DB {
		t.Fatal("LOG_DB must reuse the MySQL main database when LOG_SQL_DSN is unset")
	}
	if common.LogSqlType != common.DatabaseTypeMySQL {
		t.Fatalf("common.LogSqlType = %q, want %q", common.LogSqlType, common.DatabaseTypeMySQL)
	}
}
