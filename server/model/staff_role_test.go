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
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestZTAPIUserDisableRollsBackWhenDisabledCacheCannotBeWritten(t *testing.T) {
	originalDB := DB
	originalSQLite := common.UsingSQLite
	originalRedisEnabled := common.RedisEnabled
	originalCacheSync := ztapiUserStatusCacheSync
	common.UsingSQLite = true
	common.RedisEnabled = true
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlite connection: %v", err)
	}
	DB = db
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatalf("migrate user: %v", err)
	}
	user := User{Username: "cache-guard-user", Password: "not-used", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "cache-guard-aff"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	wantErr := errors.New("redis write unavailable")
	ztapiUserStatusCacheSync = func(cached User) error {
		if cached.Id != user.Id || cached.Status != common.UserStatusDisabled {
			t.Fatalf("unexpected cache sync user: id=%d status=%d", cached.Id, cached.Status)
		}
		return wantErr
	}
	t.Cleanup(func() {
		ztapiUserStatusCacheSync = originalCacheSync
		DB = originalDB
		common.UsingSQLite = originalSQLite
		common.RedisEnabled = originalRedisEnabled
		_ = sqlDB.Close()
	})

	if _, err := UpdateZTAPIUserStatus(user.Id, common.RoleSupportUser, common.UserStatusDisabled, common.UserStatusEnabled); !errors.Is(err, wantErr) {
		t.Fatalf("disable error = %v, want %v", err, wantErr)
	}
	var persisted User
	if err := db.First(&persisted, user.Id).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if persisted.Status != common.UserStatusEnabled {
		t.Fatalf("status = %d, want enabled rollback", persisted.Status)
	}
}
