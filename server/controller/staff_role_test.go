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
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

func TestZTAPIAdminUsersReturnOnlySafeProjection(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	support, supportToken := createBalanceLedgerOperator(t, db, "support-reader", common.RoleSupportUser)
	target := createSensitiveUser(t, db, "safe-target", common.RoleCommonUser)

	list := performBalanceLedgerRequest(t, engine, http.MethodGet, "/api/admin/users?keyword=safe-target&p=1&page_size=10", support, supportToken, "")
	assertSafeUserResponse(t, list, target.Id, "safe-target")
	assertMaskedEmail(t, list, "s***@example.com", target.Email)

	detail := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/users/%d", target.Id), support, supportToken, "")
	assertSafeUserResponse(t, detail, target.Id, "safe-target")
	assertMaskedEmail(t, detail, "s***@example.com", target.Email)
}

func TestZTAPIAdminUsersExposeFullEmailOnlyToPrivilegedOperators(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	root, rootToken := createBalanceLedgerOperator(t, db, "root-email-reader", common.RoleRootUser)
	target := createSensitiveUser(t, db, "full-email-target", common.RoleCommonUser)

	detail := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/users/%d", target.Id), root, rootToken, "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"email":"safe@example.com"`) {
		t.Fatalf("root response did not include the full email: status=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestAdminUserSearchMatchesFullEmailOnlyForPrivilegedOperators(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	support, supportToken := createBalanceLedgerOperator(t, db, "support-email-search", common.RoleSupportUser)
	root, rootToken := createBalanceLedgerOperator(t, db, "root-email-search", common.RoleRootUser)
	target := createSensitiveUser(t, db, "email-search-target", common.RoleCommonUser)
	target.Email = "hidden-search-target@example.test"
	if err := db.Model(&model.User{}).Where("id = ?", target.Id).Update("email", target.Email).Error; err != nil {
		t.Fatalf("update target email: %v", err)
	}

	keyword := url.QueryEscape(target.Email)
	for _, endpoint := range []string{
		"/api/admin/users?keyword=" + keyword + "&p=1&page_size=10",
		"/api/user/search?keyword=" + keyword + "&p=1&page_size=10",
	} {
		supportResponse := performBalanceLedgerRequest(t, engine, http.MethodGet, endpoint, support, supportToken, "")
		if supportResponse.Code != http.StatusOK || strings.Contains(supportResponse.Body.String(), `"username":"`+target.Username+`"`) {
			t.Fatalf("support email search must not find target: endpoint=%s status=%d body=%s", endpoint, supportResponse.Code, supportResponse.Body.String())
		}

		rootResponse := performBalanceLedgerRequest(t, engine, http.MethodGet, endpoint, root, rootToken, "")
		if rootResponse.Code != http.StatusOK || !strings.Contains(rootResponse.Body.String(), `"username":"`+target.Username+`"`) {
			t.Fatalf("root email search did not find target: endpoint=%s status=%d body=%s", endpoint, rootResponse.Code, rootResponse.Body.String())
		}
	}
}

func TestLegacyAdminUserReadsReturnOnlySafeProjection(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	support, supportToken := createBalanceLedgerOperator(t, db, "legacy-safe-support", common.RoleSupportUser)
	target := createSensitiveUser(t, db, "legacy-safe-target", common.RoleCommonUser)

	requests := []string{
		"/api/user/?p=1&page_size=10",
		"/api/user/search?keyword=legacy-safe-target&p=1&page_size=10",
		fmt.Sprintf("/api/user/%d", target.Id),
	}
	for _, targetURL := range requests {
		response := performBalanceLedgerRequest(t, engine, http.MethodGet, targetURL, support, supportToken, "")
		assertSafeUserResponse(t, response, target.Id, target.Username)
		assertMaskedEmail(t, response, "s***@example.com", target.Email)
	}
}

func TestLegacyAdminUserCreateCannotCreateAnotherRoot(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	root, rootToken := createBalanceLedgerOperator(t, db, "legacy-create-root", common.RoleRootUser)

	response := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/user/", root, rootToken,
		`{"username":"forbidden-root","password":"password123","display_name":"Forbidden Root","role":100}`)
	assertAdminRequestDenied(t, response)

	var count int64
	if err := db.Model(&model.User{}).Where("username = ?", "forbidden-root").Count(&count).Error; err != nil {
		t.Fatalf("count forbidden roots: %v", err)
	}
	if count != 0 {
		t.Fatalf("legacy create inserted %d forbidden root users", count)
	}
}

func TestZTAPIUserStatusUsesNamedPermissionAndLimitsNonRootTargets(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	support, supportToken := createBalanceLedgerOperator(t, db, "status-support", common.RoleSupportUser)
	finance, financeToken := createBalanceLedgerOperator(t, db, "status-finance", common.RoleFinanceUser)
	staff, _ := createBalanceLedgerOperator(t, db, "status-staff", common.RoleAdminUser)
	target := createBalanceLedgerTarget(t, db, "status-target", 100)

	updated := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/status", target.Id), support, supportToken,
		`{"status":2,"expected_status":1,"reason":"customer requested suspension"}`)
	assertUserMutation(t, updated, target.Id, common.UserStatusDisabled, common.RoleCommonUser)

	var persisted model.User
	if err := db.First(&persisted, target.Id).Error; err != nil {
		t.Fatalf("reload status target: %v", err)
	}
	if persisted.Status != common.UserStatusDisabled {
		t.Fatalf("persisted status = %d, want disabled", persisted.Status)
	}

	staffDenied := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/status", staff.Id), support, supportToken,
		`{"status":2,"expected_status":1,"reason":"not allowed"}`)
	assertAdminRequestDenied(t, staffDenied)

	financeDenied := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/status", target.Id), finance, financeToken,
		`{"status":1,"expected_status":2,"reason":"finance cannot edit users"}`)
	assertAdminRequestDenied(t, financeDenied)
}

func TestZTAPIUserEnableCacheFailureReturnsCommittedWarningAndWritesAudit(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	support, supportToken := createBalanceLedgerOperator(t, db, "enable-cache-support", common.RoleSupportUser)
	target := createBalanceLedgerTarget(t, db, "enable-cache-target", 100)
	if err := db.Model(&model.User{}).Where("id = ?", target.Id).Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatalf("disable target before request: %v", err)
	}

	originalRedisClient := common.RDB
	failingRedis := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  10 * time.Millisecond,
		WriteTimeout: 10 * time.Millisecond,
		MaxRetries:   0,
	})
	common.RDB = failingRedis
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = failingRedis.Close()
		common.RDB = originalRedisClient
	})

	response := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/status", target.Id), support, supportToken,
		`{"status":1,"expected_status":2,"reason":"restore customer access"}`)
	var body struct {
		Success bool   `json:"success"`
		Warning string `json:"warning"`
		Data    struct {
			Status int `json:"status"`
		} `json:"data"`
	}
	if err := common.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode enable cache warning response: %v; body=%s", err, response.Body.String())
	}
	if response.Code != http.StatusOK || !body.Success || body.Warning == "" || body.Data.Status != common.UserStatusEnabled {
		t.Fatalf("enable cache warning response = %#v; status=%d body=%s", body, response.Code, response.Body.String())
	}

	var persisted model.User
	if err := db.First(&persisted, target.Id).Error; err != nil {
		t.Fatalf("reload enabled target: %v", err)
	}
	if persisted.Status != common.UserStatusEnabled {
		t.Fatalf("persisted status = %d, want enabled", persisted.Status)
	}
	var audit model.Log
	if err := db.Where("user_id = ? AND type = ?", target.Id, model.LogTypeManage).Order("id DESC").First(&audit).Error; err != nil {
		t.Fatalf("load enable cache warning audit: %v", err)
	}
	for _, required := range []string{"user.status_update", "restore customer access", `"result":"committed_cache_sync_pending"`} {
		if !strings.Contains(audit.Other, required) && !strings.Contains(audit.Content, required) {
			t.Fatalf("enable cache warning audit missing %q: content=%q other=%s", required, audit.Content, audit.Other)
		}
	}
}

func TestZTAPIStaffRoleAssignmentIsRootOnlyAndAudited(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	admin, adminToken := createBalanceLedgerOperator(t, db, "role-admin", common.RoleAdminUser)
	finance, financeToken := createBalanceLedgerOperator(t, db, "role-finance", common.RoleFinanceUser)
	root, rootToken := createBalanceLedgerOperator(t, db, "role-root", common.RoleRootUser)
	target := createBalanceLedgerTarget(t, db, "role-target", 100)
	search := performBalanceLedgerRequest(t, engine, http.MethodGet, "/api/admin/staff?keyword=role-target&p=1&page_size=10", root, rootToken, "")
	assertSafeUserResponse(t, search, target.Id, target.Username)

	denied := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/staff/%d/role", target.Id), admin, adminToken,
		`{"role":2,"expected_role":1,"reason":"attempted escalation"}`)
	assertAdminRequestDenied(t, denied)
	financeDenied := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/staff/%d/role", target.Id), finance, financeToken,
		`{"role":2,"expected_role":1,"reason":"finance escalation"}`)
	assertAdminRequestDenied(t, financeDenied)

	updated := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/staff/%d/role", target.Id), root, rootToken,
		`{"role":2,"expected_role":1,"reason":"assign support shift"}`)
	assertUserMutation(t, updated, target.Id, common.UserStatusEnabled, common.RoleSupportUser)

	var audit model.Log
	if err := db.Where("user_id = ? AND type = ?", target.Id, model.LogTypeManage).Order("id DESC").First(&audit).Error; err != nil {
		t.Fatalf("load role audit: %v", err)
	}
	for _, required := range []string{"user.role_update", "assign support shift", `"from_role":1`, `"to_role":2`} {
		if !strings.Contains(audit.Other, required) && !strings.Contains(audit.Content, required) {
			t.Fatalf("role audit missing %q: content=%q other=%s", required, audit.Content, audit.Other)
		}
	}
}

func TestZTAPICannotDemoteLastEnabledRoot(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	root, rootToken := createBalanceLedgerOperator(t, db, "last-root", common.RoleRootUser)

	denied := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/staff/%d/role", root.Id), root, rootToken,
		`{"role":10,"expected_role":100,"reason":"unsafe demotion"}`)
	assertAdminRequestDenied(t, denied)

	secondRoot, _ := createBalanceLedgerOperator(t, db, "second-root", common.RoleRootUser)
	allowed := performBalanceLedgerRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/admin/staff/%d/role", root.Id), secondRoot, secondRoot.GetAccessToken(),
		`{"role":10,"expected_role":100,"reason":"handover complete"}`)
	assertUserMutation(t, allowed, root.Id, common.UserStatusEnabled, common.RoleAdminUser)
}

func TestZTAPIConcurrentRootDemotionsPreserveOneEnabledRoot(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	first, firstToken := createBalanceLedgerOperator(t, db, "concurrent-root-first", common.RoleRootUser)
	second, secondToken := createBalanceLedgerOperator(t, db, "concurrent-root-second", common.RoleRootUser)

	requests := []struct {
		operator model.User
		token    string
	}{
		{operator: first, token: firstToken},
		{operator: second, token: secondToken},
	}
	responses := make([]*httptest.ResponseRecorder, len(requests))
	var wait sync.WaitGroup
	for index, request := range requests {
		wait.Add(1)
		go func(index int, request struct {
			operator model.User
			token    string
		}) {
			defer wait.Done()
			responses[index] = performBalanceLedgerRequest(t, engine, http.MethodPatch,
				fmt.Sprintf("/api/admin/staff/%d/role", request.operator.Id), request.operator, request.token,
				`{"role":10,"expected_role":100,"reason":"concurrent handover"}`)
		}(index, request)
	}
	wait.Wait()

	successes := 0
	for _, response := range responses {
		if response.Code == http.StatusOK && strings.Contains(response.Body.String(), `"success":true`) {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent demotions = %d, want 1; responses=%s | %s", successes, responses[0].Body.String(), responses[1].Body.String())
	}
	var enabledRoots int64
	if err := db.Model(&model.User{}).Where("role = ? AND status = ?", common.RoleRootUser, common.UserStatusEnabled).Count(&enabledRoots).Error; err != nil {
		t.Fatalf("count enabled roots: %v", err)
	}
	if enabledRoots != 1 {
		t.Fatalf("enabled roots = %d, want 1", enabledRoots)
	}
}

func TestLegacyUserManageCannotBypassBalanceLedger(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	admin, adminToken := createBalanceLedgerOperator(t, db, "legacy-quota-admin", common.RoleAdminUser)
	target := createBalanceLedgerTarget(t, db, "legacy-quota-target", 100)

	body := fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":500}`, target.Id)
	response := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/user/manage", admin, adminToken, body)
	assertAdminRequestDenied(t, response)

	var persisted model.User
	if err := db.First(&persisted, target.Id).Error; err != nil {
		t.Fatalf("reload legacy quota target: %v", err)
	}
	if persisted.Quota != 100 {
		t.Fatalf("legacy manage changed quota to %d", persisted.Quota)
	}
	var ledgerCount int64
	if err := db.Model(&model.BalanceLedger{}).Count(&ledgerCount).Error; err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if ledgerCount != 0 {
		t.Fatalf("legacy manage unexpectedly created %d ledger rows", ledgerCount)
	}
}

func TestLegacyUserManageCannotBypassStatusOrRoleGovernance(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	admin, adminToken := createBalanceLedgerOperator(t, db, "legacy-status-admin", common.RoleAdminUser)
	root, rootToken := createBalanceLedgerOperator(t, db, "legacy-role-root", common.RoleRootUser)
	target := createBalanceLedgerTarget(t, db, "legacy-governance-target", 100)

	disable := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/user/manage", admin, adminToken,
		fmt.Sprintf(`{"id":%d,"action":"disable"}`, target.Id))
	assertAdminRequestDenied(t, disable)
	promote := performBalanceLedgerRequest(t, engine, http.MethodPost, "/api/user/manage", root, rootToken,
		fmt.Sprintf(`{"id":%d,"action":"promote"}`, target.Id))
	assertAdminRequestDenied(t, promote)

	var persisted model.User
	if err := db.First(&persisted, target.Id).Error; err != nil {
		t.Fatalf("reload legacy governance target: %v", err)
	}
	if persisted.Status != common.UserStatusEnabled || persisted.Role != common.RoleCommonUser {
		t.Fatalf("legacy manage changed governed fields: status=%d role=%d", persisted.Status, persisted.Role)
	}
}

func createSensitiveUser(t *testing.T, db *gorm.DB, username string, role int) model.User {
	t.Helper()
	secretToken := "ACCESS_TOKEN_SECRET"
	user := model.User{
		Username:         username,
		Password:         "PASSWORD_HASH_SECRET",
		OriginalPassword: "ORIGINAL_PASSWORD_SECRET",
		DisplayName:      "Safe Target",
		Role:             role,
		Status:           common.UserStatusEnabled,
		Email:            "safe@example.com",
		GitHubId:         "GITHUB_SECRET",
		TelegramId:       "TELEGRAM_SECRET",
		Setting:          `{"provider_key":"SETTING_SECRET"}`,
		StripeCustomer:   "STRIPE_SECRET",
		AccessToken:      &secretToken,
		AffCode:          username + "-aff",
		Quota:            800,
		UsedQuota:        200,
		RequestCount:     3,
		Group:            "default",
		Remark:           "commercial customer",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create sensitive user: %v", err)
	}
	return user
}

func assertSafeUserResponse(t *testing.T, recorder *httptest.ResponseRecorder, userID int, username string) {
	t.Helper()
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, fmt.Sprintf(`"id":%d`, userID)) || !strings.Contains(body, `"username":"`+username+`"`) {
		t.Fatalf("unexpected safe user response: status=%d body=%s", recorder.Code, body)
	}
	for _, forbidden := range []string{"PASSWORD_HASH_SECRET", "ORIGINAL_PASSWORD_SECRET", "ACCESS_TOKEN_SECRET", "GITHUB_SECRET", "TELEGRAM_SECRET", "SETTING_SECRET", "STRIPE_SECRET", `"password"`, `"access_token"`, `"github_id"`, `"setting"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("safe user response contains %q: %s", forbidden, body)
		}
	}
}

func assertMaskedEmail(t *testing.T, recorder *httptest.ResponseRecorder, masked, full string) {
	t.Helper()
	body := recorder.Body.String()
	if !strings.Contains(body, `"email":"`+masked+`"`) {
		t.Fatalf("response missing masked email %q: %s", masked, body)
	}
	if strings.Contains(body, `"email":"`+full+`"`) {
		t.Fatalf("response exposed full email %q: %s", full, body)
	}
}

func assertUserMutation(t *testing.T, recorder *httptest.ResponseRecorder, userID, status, role int) {
	t.Helper()
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, `"success":true`) || !strings.Contains(body, fmt.Sprintf(`"id":%d`, userID)) || !strings.Contains(body, fmt.Sprintf(`"status":%d`, status)) || !strings.Contains(body, fmt.Sprintf(`"role":%d`, role)) {
		t.Fatalf("unexpected user mutation response: status=%d body=%s", recorder.Code, body)
	}
	for _, forbidden := range []string{`"password"`, `"access_token"`, `"setting"`, `"github_id"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("user mutation response contains %q: %s", forbidden, body)
		}
	}
}

func assertAdminRequestDenied(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code == http.StatusOK && strings.Contains(recorder.Body.String(), `"success":true`) {
		t.Fatalf("request unexpectedly succeeded: %s", recorder.Body.String())
	}
}
