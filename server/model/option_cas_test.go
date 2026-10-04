/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestOptionCASPredicatesQuoteReservedColumns(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:pass@tcp(localhost:3306)/ztapi",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		DryRun:                 true,
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatalf("open MySQL dry-run database: %v", err)
	}

	readSQL := db.Where(optionKeyEquals("RegisterEnabled")).Take(&Option{}).Statement.SQL.String()
	if !strings.Contains(readSQL, "`key` = ?") {
		t.Fatalf("option read SQL = %q, want quoted reserved column `key`", readSQL)
	}

	updateStatement := db.Model(&Option{}).
		Where(optionKeyValueEquals("RegisterEnabled", "true")).
		Update("value", "false")
	if updateStatement.Error != nil {
		t.Fatalf("build option CAS update SQL: %v", updateStatement.Error)
	}
	updateSQL := updateStatement.Statement.SQL.String()
	if !strings.Contains(updateSQL, "`key` = ?") || !strings.Contains(updateSQL, "`value` = ?") {
		t.Fatalf("option CAS update SQL = %q, want quoted key/value predicates", updateSQL)
	}
}

func TestUpdateOptionIfMatchesReportsCommittedRuntimeSyncPending(t *testing.T) {
	originalDB := DB
	originalMap := common.OptionMap
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db
	common.OptionMap = map[string]string{"GroupRatio": `{}`}
	t.Cleanup(func() {
		DB = originalDB
		common.OptionMap = originalMap
	})
	if err := db.AutoMigrate(&Option{}); err != nil {
		t.Fatalf("migrate options: %v", err)
	}

	err = UpdateOptionIfMatches("GroupRatio", `{}`, `not-json`, `{}`)
	if err == nil || !strings.Contains(err.Error(), "committed") || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("runtime sync error=%v, want committed-pending semantics", err)
	}
	var persisted Option
	if loadErr := db.Where("key = ?", "GroupRatio").First(&persisted).Error; loadErr != nil || persisted.Value != "not-json" {
		t.Fatalf("persisted option=%#v load_err=%v", persisted, loadErr)
	}
}
