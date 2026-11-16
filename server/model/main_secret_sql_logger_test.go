package model

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	gormlogger "gorm.io/gorm/logger"
)

func TestChooseDBHidesBoundSQLParametersEvenInDebug(t *testing.T) {
	var output bytes.Buffer
	previousLogger := gormlogger.Default
	gormlogger.Default = gormlogger.New(log.New(&output, "", 0), gormlogger.Config{LogLevel: gormlogger.Info})
	previousPath := common.SQLitePath
	previousSQLite, previousMySQL, previousPostgreSQL := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
	previousLogType, previousDebug := common.LogSqlType, common.DebugEnabled
	common.SQLitePath = filepath.Join(t.TempDir(), "secret-sql.db")
	common.DebugEnabled = true
	t.Cleanup(func() {
		gormlogger.Default = previousLogger
		common.SQLitePath = previousPath
		common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = previousSQLite, previousMySQL, previousPostgreSQL
		common.LogSqlType, common.DebugEnabled = previousLogType, previousDebug
		initCol()
	})
	for _, isLog := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "log"}[isLog], func(t *testing.T) {
			t.Setenv("ZTAPI_TEST_SECRET_SQL_DSN", "local")
			db, err := chooseDB("ZTAPI_TEST_SECRET_SQL_DSN", isLog)
			if err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			output.Reset()
			var got string
			if err := db.Debug().Raw("SELECT ? AS secret_value", "fictional-bound-opaque-credential").Scan(&got).Error; err != nil {
				t.Fatal(err)
			}
			if got != "fictional-bound-opaque-credential" {
				t.Fatal("logger altered query execution")
			}
			if strings.Contains(output.String(), "fictional-bound-opaque-credential") {
				t.Error("SQL debug log interpolated a bound parameter")
			}
			if !strings.Contains(output.String(), "SELECT ? AS secret_value") {
				t.Error("SQL log lost safe query template")
			}
		})
	}
}
