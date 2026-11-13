package model

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestAuthSessionMigrationsIncludeNormalAndFastPaths(t *testing.T) {
	for _, test := range []struct {
		name    string
		migrate func() error
	}{
		{name: "normal", migrate: migrateDB},
		{name: "fast", migrate: migrateDBFast},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalDB := DB
			originalSQLite := common.UsingSQLite
			originalMySQL := common.UsingMySQL
			originalPostgreSQL := common.UsingPostgreSQL
			common.UsingSQLite = true
			common.UsingMySQL = false
			common.UsingPostgreSQL = false
			initCol()

			path := filepath.Join(t.TempDir(), "auth-session-migration.db")
			db, err := gorm.Open(sqlite.Open(path+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			DB = db
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatalf("access sqlite connection: %v", err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() {
				_ = sqlDB.Close()
				DB = originalDB
				common.UsingSQLite = originalSQLite
				common.UsingMySQL = originalMySQL
				common.UsingPostgreSQL = originalPostgreSQL
				initCol()
			})

			if err := test.migrate(); err != nil {
				t.Fatalf("%s migration: %v", test.name, err)
			}
			if !db.Migrator().HasTable(&AuthSession{}) {
				t.Fatalf("%s migration omitted auth_sessions", test.name)
			}
		})
	}
}

func TestAuthUserCreationDoesNotLogEncodedPassword(t *testing.T) {
	var output bytes.Buffer
	databaseLogger := gormlogger.New(
		log.New(&output, "", 0),
		gormlogger.Config{LogLevel: gormlogger.Info},
	)
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "auth-user-log.db")),
		&gorm.Config{Logger: databaseLogger},
	)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access sqlite connection: %v", err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatalf("migrate user table: %v", err)
	}

	originalDB := DB
	DB = db
	defer func() { DB = originalDB }()
	output.Reset()

	const encodedPassword = "$argon2id$v=19$m=65536,t=3,p=2$c2Vuc2l0aXZlLXNhbHQ$c2Vuc2l0aXZlLWtleQ"
	if _, err := CreateZTAPIUserWithEncodedPassword("safe-user", encodedPassword); err != nil {
		t.Fatalf("create ZTAPI user: %v", err)
	}
	if strings.Contains(output.String(), encodedPassword) {
		t.Fatalf("database log contains encoded password: %s", output.String())
	}
}
