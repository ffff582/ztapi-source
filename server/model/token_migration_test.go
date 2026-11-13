package model

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type legacyTokenMigrationRow struct {
	Id             int    `gorm:"primaryKey"`
	UserId         int    `gorm:"index"`
	Key            string `gorm:"column:key;type:char(48);uniqueIndex"`
	Status         int    `gorm:"default:1"`
	Name           string `gorm:"index"`
	CreatedTime    int64  `gorm:"bigint"`
	AccessedTime   int64  `gorm:"bigint"`
	ExpiredTime    int64  `gorm:"bigint;default:-1"`
	RemainQuota    int    `gorm:"default:0"`
	UnlimitedQuota bool
	ModelLimits    string         `gorm:"type:text"`
	Group          string         `gorm:"column:group;default:''"`
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

func (legacyTokenMigrationRow) TableName() string {
	return "tokens"
}

func tokenMigrationColumns(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()

	columnTypes, err := db.Migrator().ColumnTypes("tokens")
	if err != nil {
		t.Fatalf("inspect token columns: %v", err)
	}
	columns := make(map[string]bool, len(columnTypes))
	for _, columnType := range columnTypes {
		columns[strings.ToLower(columnType.Name())] = true
	}
	return columns
}

func runLegacyTokenKeyMigrationTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("ZTAPI_DROP_LEGACY_TOKEN_KEY", "")

	if err := db.AutoMigrate(&legacyTokenMigrationRow{}); err != nil {
		t.Fatalf("create legacy token schema: %v", err)
	}
	legacyRows := []legacyTokenMigrationRow{
		{
			UserId:         7,
			Key:            "legacy-plaintext-token-one",
			Status:         common.TokenStatusEnabled,
			Name:           "legacy-one",
			CreatedTime:    1,
			AccessedTime:   1,
			ExpiredTime:    -1,
			RemainQuota:    100,
			UnlimitedQuota: true,
			Group:          "default",
		},
		{
			UserId:         8,
			Key:            "legacy-plaintext-token-two",
			Status:         common.TokenStatusEnabled,
			Name:           "legacy-two",
			CreatedTime:    1,
			AccessedTime:   1,
			ExpiredTime:    -1,
			RemainQuota:    100,
			UnlimitedQuota: true,
			Group:          "default",
		},
	}
	if err := db.Create(&legacyRows).Error; err != nil {
		t.Fatalf("seed legacy tokens: %v", err)
	}

	if err := migrateTokenKeyStorage(); err != nil {
		t.Fatalf("migrate legacy token keys: %v", err)
	}

	columns := tokenMigrationColumns(t, db)
	if !columns["key"] {
		t.Fatal("legacy key column was dropped without explicit operator approval")
	}
	if !columns["key_hash"] || !columns["key_prefix"] {
		t.Fatalf("hash-only columns missing after migration: %#v", columns)
	}

	var migrated []Token
	if err := db.Order("id").Find(&migrated).Error; err != nil {
		t.Fatalf("load migrated tokens: %v", err)
	}
	if len(migrated) != len(legacyRows) {
		t.Fatalf("migrated row count = %d, want %d", len(migrated), len(legacyRows))
	}
	for i, token := range migrated {
		expectedHash := common.HashZTAPIKey(fmt.Sprintf("ztapi-invalid-legacy-token:%d", token.Id))
		if token.Status != common.TokenStatusDisabled {
			t.Fatalf("row %d status = %d, want disabled", token.Id, token.Status)
		}
		if token.KeyHash != expectedHash {
			t.Fatalf("row %d key_hash = %q, want %q", token.Id, token.KeyHash, expectedHash)
		}
		if token.KeyPrefix != "" {
			t.Fatalf("row %d key_prefix = %q, want empty", token.Id, token.KeyPrefix)
		}
		if token.KeyHash == common.HashZTAPIKey(legacyRows[i].Key) {
			t.Fatalf("row %d placeholder hash was derived from legacy plaintext", token.Id)
		}
	}
	var retainedLegacyRows []legacyTokenMigrationRow
	if err := db.Order("id").Find(&retainedLegacyRows).Error; err != nil {
		t.Fatalf("load retained legacy rows: %v", err)
	}
	for i, row := range retainedLegacyRows {
		if row.Key == legacyRows[i].Key {
			t.Fatalf("row %d still contains the legacy plaintext token", row.Id)
		}
		if row.Key != fmt.Sprintf("ztapi-invalid-legacy-token:%d", row.Id) {
			t.Fatalf("row %d legacy key placeholder = %q", row.Id, row.Key)
		}
	}
	if migrated[0].KeyHash == migrated[1].KeyHash {
		t.Fatal("legacy placeholder hashes are not unique")
	}

	rerunUpdates := 0
	callbackName := "test:legacy-token-rerun:" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" {
			rerunUpdates++
		}
	}); err != nil {
		t.Fatalf("register rerun update detector: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(callbackName)
	})
	if err := migrateTokenKeyStorage(); err != nil {
		t.Fatalf("idempotent rerun failed: %v", err)
	}
	if rerunUpdates != 0 {
		t.Fatalf("idempotent rerun issued %d token updates, want 0", rerunUpdates)
	}
	var rerun []Token
	if err := db.Order("id").Find(&rerun).Error; err != nil {
		t.Fatalf("load tokens after rerun: %v", err)
	}
	for i := range migrated {
		if rerun[i].KeyHash != migrated[i].KeyHash || rerun[i].Status != migrated[i].Status {
			t.Fatalf("rerun changed migrated row %d: before=%#v after=%#v", migrated[i].Id, migrated[i], rerun[i])
		}
	}

	if err := db.AutoMigrate(&Token{}); err != nil {
		t.Fatalf("auto-migrate hash-only token schema: %v", err)
	}
}

func TestMigrateTokenKeyStorageDropsLegacyColumnOnlyWhenExplicitlyEnabled(t *testing.T) {
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	initCol()
	t.Setenv("ZTAPI_DROP_LEGACY_TOKEN_KEY", "true")

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	originalDB := DB
	DB = db
	t.Cleanup(func() {
		DB = originalDB
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&legacyTokenMigrationRow{}); err != nil {
		t.Fatalf("create legacy token schema: %v", err)
	}
	if err := db.Create(&legacyTokenMigrationRow{
		UserId: 1,
		Key:    "legacy-plaintext-token",
		Status: common.TokenStatusEnabled,
		Name:   "legacy",
	}).Error; err != nil {
		t.Fatalf("seed legacy token: %v", err)
	}

	if err := migrateTokenKeyStorage(); err != nil {
		t.Fatalf("migrate legacy token keys: %v", err)
	}
	if tokenMigrationColumns(t, db)["key"] {
		t.Fatal("legacy key column remains after explicit drop approval")
	}
}

func TestMigrateTokenKeyStorageDisablesAndSanitizesLegacyRowsWithoutDroppingColumnSQLite(t *testing.T) {
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	initCol()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	runLegacyTokenKeyMigrationTest(t, db)
}

func TestLegacyTokenIDsQueryQuotesReservedKeyForEachDialect(t *testing.T) {
	tests := []struct {
		name      string
		dialector gorm.Dialector
		want      string
	}{
		{
			name:      "sqlite",
			dialector: sqlite.Open("file:token-migration-query?mode=memory&cache=shared"),
			want:      "`key` IS NOT NULL AND `key` <> ? AND `key` NOT LIKE ?",
		},
		{
			name: "mysql",
			dialector: mysql.New(mysql.Config{
				DSN:                       "user:pass@tcp(localhost:3306)/ztapi",
				SkipInitializeWithVersion: true,
			}),
			want: "`key` IS NOT NULL AND `key` <> ? AND `key` NOT LIKE ?",
		},
		{
			name: "postgresql",
			dialector: postgres.New(postgres.Config{
				DSN: "host=localhost user=ztapi password=ztapi dbname=ztapi port=5432 sslmode=disable",
			}),
			want: `"key" IS NOT NULL AND "key" <> $1 AND "key" NOT LIKE $2`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(test.dialector, &gorm.Config{
				DisableAutomaticPing: true,
				DryRun:               true,
			})
			if err != nil {
				t.Fatalf("open dry-run database: %v", err)
			}

			var ids []int
			statement := legacyTokenIDsQuery(db).Pluck("id", &ids).Statement
			query := statement.SQL.String()
			if !strings.Contains(query, test.want) {
				t.Fatalf("legacy token query = %q, want dialect-safe predicate %q", query, test.want)
			}
		})
	}
}

func TestMigrateTokenKeyStorageMySQL(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set TEST_MYSQL_DSN to run MySQL migration compatibility test")
	}

	common.UsingSQLite = false
	common.UsingMySQL = true
	common.UsingPostgreSQL = false
	initCol()
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	DB = db
	if db.Migrator().HasTable("tokens") {
		t.Fatal("refusing to use TEST_MYSQL_DSN because tokens table already exists")
	}
	t.Cleanup(func() {
		_ = db.Migrator().DropTable("tokens")
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	runLegacyTokenKeyMigrationTest(t, db)
}

func TestMigrateTokenKeyStoragePostgreSQL(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run PostgreSQL migration compatibility test")
	}

	common.UsingSQLite = false
	common.UsingMySQL = false
	common.UsingPostgreSQL = true
	initCol()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	DB = db
	if db.Migrator().HasTable("tokens") {
		t.Fatal("refusing to use TEST_POSTGRES_DSN because tokens table already exists")
	}
	t.Cleanup(func() {
		_ = db.Migrator().DropTable("tokens")
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	runLegacyTokenKeyMigrationTest(t, db)
}
