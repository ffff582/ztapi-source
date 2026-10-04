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
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupAdminPasswordResetTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := DB
	originalSQLite := common.UsingSQLite
	common.UsingSQLite = true
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&User{}, &AuthSession{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	DB = db
	t.Cleanup(func() {
		DB = originalDB
		common.UsingSQLite = originalSQLite
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func createPasswordResetUser(t *testing.T, db *gorm.DB, username string, role int) User {
	t.Helper()
	user := User{
		Username: username, Password: "argon2-before", Role: role,
		Status: common.UserStatusEnabled, AffCode: username + "-aff",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	session := AuthSession{
		UserID: user.Id, FamilyID: username + "-family", TokenHash: username + "-token-hash",
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
		CreationIP: "198.51.100.7", UserAgentHash: username + "-agent-hash",
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session for %s: %v", username, err)
	}
	return user
}

func TestAdminPasswordResetStoresTheNewPasswordAndEndsEverySession(t *testing.T) {
	db := setupAdminPasswordResetTestDB(t)
	user := createPasswordResetUser(t, db, "customer", common.RoleCommonUser)

	updated, err := ResetZTAPIUserPasswordByAdmin(user.Id, 9, common.RoleAdminUser, "argon2-after", time.Now().UTC())
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if updated.Id != user.Id {
		t.Fatalf("reset returned user %d, want %d", updated.Id, user.Id)
	}

	var stored User
	if err := db.First(&stored, user.Id).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored.Password != "argon2-after" {
		t.Fatalf("stored password = %q, want the new encoded password", stored.Password)
	}
	var live int64
	if err := db.Model(&AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", user.Id).Count(&live).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if live != 0 {
		t.Fatalf("%d sessions survived the password reset", live)
	}
}

func TestAdminPasswordResetRefusesStaffAccountsAndSelfService(t *testing.T) {
	db := setupAdminPasswordResetTestDB(t)
	finance := createPasswordResetUser(t, db, "finance", common.RoleFinanceUser)
	admin := createPasswordResetUser(t, db, "admin", common.RoleAdminUser)

	if _, err := ResetZTAPIUserPasswordByAdmin(finance.Id, admin.Id, common.RoleAdminUser, "argon2-after", time.Now().UTC()); !errors.Is(err, ErrAdminUserForbidden) {
		t.Fatalf("admin resetting a staff password = %v, want ErrAdminUserForbidden", err)
	}
	if _, err := ResetZTAPIUserPasswordByAdmin(admin.Id, admin.Id, common.RoleRootUser, "argon2-after", time.Now().UTC()); !errors.Is(err, ErrAdminUserSelfPasswordReset) {
		t.Fatalf("self reset = %v, want ErrAdminUserSelfPasswordReset", err)
	}

	var stored User
	if err := db.First(&stored, finance.Id).Error; err != nil {
		t.Fatalf("reload finance user: %v", err)
	}
	if stored.Password != "argon2-before" {
		t.Fatal("a refused reset still changed the password")
	}
	var live int64
	if err := db.Model(&AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", finance.Id).Count(&live).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if live != 1 {
		t.Fatalf("refused reset left %d live sessions, want 1", live)
	}

	if _, err := ResetZTAPIUserPasswordByAdmin(finance.Id, 9_999, common.RoleRootUser, "argon2-after", time.Now().UTC()); err != nil {
		t.Fatalf("root resetting a staff password: %v", err)
	}
}
