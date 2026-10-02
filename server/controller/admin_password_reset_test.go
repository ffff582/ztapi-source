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

package controller_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"gorm.io/gorm"
)

const adminPasswordResetNewPassword = "fresh-password-1"

func seedPasswordResetTarget(t *testing.T, db *gorm.DB, username string, role int) model.User {
	t.Helper()
	if err := db.AutoMigrate(&model.AuthSession{}); err != nil {
		t.Fatalf("migrate auth sessions: %v", err)
	}
	encoded, err := service.HashZTAPIPassword("original-password")
	if err != nil {
		t.Fatalf("hash original password: %v", err)
	}
	user := model.User{
		Username: username, Password: encoded, Role: role,
		Status: common.UserStatusEnabled, AffCode: username + "-aff",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	session := model.AuthSession{
		UserID: user.Id, FamilyID: username + "-family", TokenHash: username + "-token-hash",
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(),
		CreationIP: "198.51.100.9", UserAgentHash: username + "-agent-hash",
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	return user
}

func TestAdminPasswordResetStoresAnArgon2PasswordAndRevokesSessions(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	admin, adminToken := createBalanceLedgerOperator(t, db, "password-reset-admin", common.RoleAdminUser)
	target := seedPasswordResetTarget(t, db, "password-reset-target", common.RoleCommonUser)

	body := fmt.Sprintf(`{"password":%q,"reason":"客户收不到验证码，客服代改"}`, adminPasswordResetNewPassword)
	recorder := performBalanceLedgerRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/admin/users/%d/password", target.Id), admin, adminToken, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("password reset status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), adminPasswordResetNewPassword) {
		t.Fatalf("response echoed the new password: %s", recorder.Body.String())
	}

	var stored model.User
	if err := db.First(&stored, target.Id).Error; err != nil {
		t.Fatalf("reload target: %v", err)
	}
	if !service.VerifyZTAPIPassword(adminPasswordResetNewPassword, stored.Password) {
		t.Fatal("the stored password does not verify against the new password")
	}
	var live int64
	if err := db.Model(&model.AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", target.Id).Count(&live).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if live != 0 {
		t.Fatalf("%d sessions survived the reset", live)
	}

	var logs []model.Log
	if err := db.Where("user_id = ?", target.Id).Find(&logs).Error; err != nil {
		t.Fatalf("read audit logs: %v", err)
	}
	audited := false
	for _, entry := range logs {
		if strings.Contains(entry.Other, "user.password_reset") {
			audited = true
			if strings.Contains(entry.Other, adminPasswordResetNewPassword) || strings.Contains(entry.Content, adminPasswordResetNewPassword) {
				t.Fatalf("audit log stored the password: %s %s", entry.Content, entry.Other)
			}
		}
	}
	if !audited {
		t.Fatal("password reset was not written to the audit log")
	}
}

func TestAdminPasswordResetRejectsWeakPasswordsMissingReasonsAndUnprivilegedStaff(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	admin, adminToken := createBalanceLedgerOperator(t, db, "password-reset-guard-admin", common.RoleAdminUser)
	support, supportToken := createBalanceLedgerOperator(t, db, "password-reset-support", common.RoleSupportUser)
	finance, financeToken := createBalanceLedgerOperator(t, db, "password-reset-finance", common.RoleFinanceUser)
	target := seedPasswordResetTarget(t, db, "password-reset-guard-target", common.RoleCommonUser)
	endpoint := fmt.Sprintf("/api/admin/users/%d/password", target.Id)

	for _, body := range []string{
		`{"password":"short","reason":"客服代改"}`,
		fmt.Sprintf(`{"password":%q,"reason":"   "}`, adminPasswordResetNewPassword),
		fmt.Sprintf(`{"password":%q}`, adminPasswordResetNewPassword),
	} {
		recorder := performBalanceLedgerRequest(t, engine, http.MethodPost, endpoint, admin, adminToken, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid request status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
		}
	}

	valid := fmt.Sprintf(`{"password":%q,"reason":"客服代改"}`, adminPasswordResetNewPassword)
	for _, operator := range []struct {
		user  model.User
		token string
	}{{support, supportToken}, {finance, financeToken}} {
		recorder := performBalanceLedgerRequest(t, engine, http.MethodPost, endpoint, operator.user, operator.token, valid)
		assertBalanceLedgerDenied(t, recorder)
	}

	var stored model.User
	if err := db.First(&stored, target.Id).Error; err != nil {
		t.Fatalf("reload target: %v", err)
	}
	if !service.VerifyZTAPIPassword("original-password", stored.Password) {
		t.Fatal("a refused reset still changed the password")
	}
}
