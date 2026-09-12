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
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/router"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

func TestAdminFinanceOrdersUseSafeProjectionFiltersAndCASMutations(t *testing.T) {
	db, engine := setupAdminOperationsTest(t)
	finance, financeToken := createAdminOperationsUser(t, db, "finance-operator", common.RoleFinanceUser, 0)
	support, supportToken := createAdminOperationsUser(t, db, "support-operator", common.RoleSupportUser, 0)
	customer, _ := createAdminOperationsUser(t, db, "customer-keyword", common.RoleCommonUser, 100)
	pending := model.TopUp{UserId: customer.Id, Amount: 2, Money: 2.5, TradeNo: "trade-safe-001", PaymentMethod: "alipay", PaymentProvider: model.PaymentProviderEpay, CreateTime: time.Now().Unix(), Status: common.TopUpStatusPending}
	rejected := model.TopUp{UserId: customer.Id, Amount: 3, Money: 3.5, TradeNo: "=trade-safe-002", PaymentMethod: "wechat", PaymentProvider: model.PaymentProviderEpay, CreateTime: time.Now().Unix(), Status: common.TopUpStatusPending}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending topup: %v", err)
	}
	if err := db.Create(&rejected).Error; err != nil {
		t.Fatalf("create rejectable topup: %v", err)
	}

	list := performAdminOperationsRequest(t, engine, http.MethodGet, "/api/admin/topups?p=1&page_size=10&keyword=customer-keyword&status=pending", finance, financeToken, "", "finance-list-request")
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var page struct {
		Success bool `json:"success"`
		Data    struct {
			Total int `json:"total"`
			Items []struct {
				ID              int     `json:"id"`
				UserID          int     `json:"user_id"`
				Username        string  `json:"username"`
				Amount          int64   `json:"amount"`
				Money           float64 `json:"money"`
				TradeNo         string  `json:"trade_no"`
				PaymentMethod   string  `json:"payment_method"`
				PaymentProvider string  `json:"payment_provider"`
				CreateTime      int64   `json:"create_time"`
				CompleteTime    int64   `json:"complete_time"`
				Status          string  `json:"status"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := common.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode topups: %v body=%s", err, list.Body.String())
	}
	if !page.Success || page.Data.Total != 2 || len(page.Data.Items) != 2 || page.Data.Items[0].Username != customer.Username {
		t.Fatalf("unexpected topup page: %#v", page)
	}
	for _, forbidden := range []string{"password", "access_token", "other", "stripe_customer"} {
		if strings.Contains(strings.ToLower(list.Body.String()), forbidden) {
			t.Fatalf("topup response exposed %q: %s", forbidden, list.Body.String())
		}
	}

	denied := performAdminOperationsRequest(t, engine, http.MethodGet, "/api/admin/topups", support, supportToken, "", "finance-denied")
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), `"success":false`) {
		t.Fatalf("support topup read status=%d body=%s", denied.Code, denied.Body.String())
	}

	missingReason := performAdminOperationsRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/topups/%d/complete", pending.Id), finance, financeToken, `{"expected_status":"pending"}`, "finance-missing-reason")
	if missingReason.Code != http.StatusBadRequest {
		t.Fatalf("missing reason status=%d body=%s", missingReason.Code, missingReason.Body.String())
	}

	completeBody := `{"expected_status":"pending","reason":"=payment verified by finance"}`
	completed := performAdminOperationsRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/topups/%d/complete", pending.Id), finance, financeToken, completeBody, "finance-complete-request")
	if completed.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completed.Code, completed.Body.String())
	}
	assertAdminOperationsUserQuota(t, db, customer.Id, 100+2*int(common.QuotaPerUnit))
	var completedTopUp model.TopUp
	if err := db.First(&completedTopUp, pending.Id).Error; err != nil || completedTopUp.Status != common.TopUpStatusSuccess || completedTopUp.CompleteTime == 0 {
		t.Fatalf("completed topup=%#v err=%v", completedTopUp, err)
	}
	var completionLedger model.BalanceLedger
	if err := db.Where("idempotency_key = ?", fmt.Sprintf("topup:%d", pending.Id)).First(&completionLedger).Error; err != nil {
		t.Fatalf("load topup completion ledger: %v", err)
	}
	if completionLedger.UserID != customer.Id || completionLedger.OperatorID != finance.Id || completionLedger.Delta != 2*int64(common.QuotaPerUnit) || completionLedger.BalanceBefore != 100 || completionLedger.BalanceAfter != 100+2*int64(common.QuotaPerUnit) || completionLedger.Reason != "=payment verified by finance" || completionLedger.RequestID != "finance-complete-request" || completionLedger.SourceType != "topup_completion" {
		t.Fatalf("unexpected completion ledger: %#v", completionLedger)
	}

	stale := performAdminOperationsRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/topups/%d/complete", pending.Id), finance, financeToken, completeBody, "finance-stale-request")
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"status":"success"`) || !strings.Contains(stale.Body.String(), fmt.Sprintf(`"id":%d`, pending.Id)) {
		t.Fatalf("stale completion status=%d body=%s", stale.Code, stale.Body.String())
	}
	assertBodyOmits(t, stale.Body.String(), "password", "access_token", "other", "stripe_customer")
	assertAdminOperationsUserQuota(t, db, customer.Id, 100+2*int(common.QuotaPerUnit))
	var completionLedgerCount int64
	if err := db.Model(&model.BalanceLedger{}).Where("idempotency_key = ?", fmt.Sprintf("topup:%d", pending.Id)).Count(&completionLedgerCount).Error; err != nil || completionLedgerCount != 1 {
		t.Fatalf("completion ledger count=%d err=%v", completionLedgerCount, err)
	}

	rejectBody := `{"expected_status":"pending","reason":"payment evidence did not match"}`
	rejectResponse := performAdminOperationsRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/topups/%d/reject", rejected.Id), finance, financeToken, rejectBody, "finance-reject-request")
	if rejectResponse.Code != http.StatusOK {
		t.Fatalf("reject status=%d body=%s", rejectResponse.Code, rejectResponse.Body.String())
	}
	var rejectedTopUp model.TopUp
	if err := db.First(&rejectedTopUp, rejected.Id).Error; err != nil || rejectedTopUp.Status != "rejected" || rejectedTopUp.CompleteTime != 0 {
		t.Fatalf("rejected topup=%#v err=%v", rejectedTopUp, err)
	}
	assertAdminOperationsUserQuota(t, db, customer.Id, 100+2*int(common.QuotaPerUnit))
	var rejectionLedgerCount int64
	if err := db.Model(&model.BalanceLedger{}).Where("idempotency_key = ?", fmt.Sprintf("topup:%d", rejected.Id)).Count(&rejectionLedgerCount).Error; err != nil || rejectionLedgerCount != 0 {
		t.Fatalf("rejection ledger count=%d err=%v", rejectionLedgerCount, err)
	}
	rejectedFilter := performAdminOperationsRequest(t, engine, http.MethodGet, "/api/admin/topups?p=1&page_size=10&status=rejected", finance, financeToken, "", "finance-rejected-filter")
	if rejectedFilter.Code != http.StatusOK || !strings.Contains(rejectedFilter.Body.String(), `"total":1`) || !strings.Contains(rejectedFilter.Body.String(), `"status":"rejected"`) {
		t.Fatalf("rejected filter status=%d body=%s", rejectedFilter.Code, rejectedFilter.Body.String())
	}
	fullOrderFilter := fmt.Sprintf("/api/admin/topups?p=1&page_size=10&user_id=%d&username=%s&payment_provider=%s&from=%d&to=%d&status=rejected", customer.Id, customer.Username, model.PaymentProviderEpay, rejected.CreateTime-1, rejected.CreateTime+2)
	filteredOrders := performAdminOperationsRequest(t, engine, http.MethodGet, fullOrderFilter, finance, financeToken, "", "finance-full-filter")
	if filteredOrders.Code != http.StatusOK || !strings.Contains(filteredOrders.Body.String(), `"total":1`) || !strings.Contains(filteredOrders.Body.String(), rejected.TradeNo) {
		t.Fatalf("full order filter status=%d body=%s", filteredOrders.Code, filteredOrders.Body.String())
	}

	ordersCSV := performAdminOperationsRequest(t, engine, http.MethodGet, strings.Replace(fullOrderFilter, "/api/admin/topups?", "/api/admin/topups/export?", 1), finance, financeToken, "", "finance-orders-export")
	assertCSV(t, ordersCSV, []string{"id", "user_id", "username", "amount", "money", "trade_no", "payment_method", "payment_provider", "create_time", "complete_time", "status"}, []string{"'" + rejected.TradeNo, "rejected"})
	assertAuditAction(t, db, "finance-orders-export", "export.orders")

	ledgerFilter := fmt.Sprintf("/api/admin/balance-ledger?p=1&page_size=10&user_id=%d&operator_id=%d&source_type=topup_completion&request_id=finance-complete-request&from=%d&to=%d", customer.Id, finance.Id, completionLedger.CreatedAt.Add(-time.Second).Unix(), completionLedger.CreatedAt.Add(time.Second).Unix()+1)
	ledgerPage := performAdminOperationsRequest(t, engine, http.MethodGet, ledgerFilter, finance, financeToken, "", "finance-ledger-filter")
	if ledgerPage.Code != http.StatusOK || !strings.Contains(ledgerPage.Body.String(), `"total":1`) || !strings.Contains(ledgerPage.Body.String(), `"source_type":"topup_completion"`) || strings.Contains(ledgerPage.Body.String(), "idempotency_key") {
		t.Fatalf("ledger filter status=%d body=%s", ledgerPage.Code, ledgerPage.Body.String())
	}
	ledgerCSV := performAdminOperationsRequest(t, engine, http.MethodGet, strings.Replace(ledgerFilter, "/api/admin/balance-ledger?", "/api/admin/balance-ledger/export?", 1), finance, financeToken, "", "finance-ledger-export")
	assertCSV(t, ledgerCSV, []string{"id", "user_id", "operator_id", "delta", "balance_before", "balance_after", "reason", "request_id", "source_type", "created_at"}, []string{"topup_completion", "finance-complete-request", "'=payment verified by finance"})
	assertAuditAction(t, db, "finance-ledger-export", "export.ledger")

	for action, requestID := range map[string]string{"topup.complete": "finance-complete-request", "topup.reject": "finance-reject-request"} {
		var audit model.Log
		if err := db.Where("type = ? AND request_id = ?", model.LogTypeManage, requestID).First(&audit).Error; err != nil {
			t.Fatalf("load %s audit: %v", action, err)
		}
		for _, required := range []string{action, `"reason"`, `"from_status":"pending"`} {
			if !strings.Contains(audit.Other, required) {
				t.Fatalf("%s audit missing %q: %s", action, required, audit.Other)
			}
		}
	}
}

func TestAdminTopUpCompletionIsConcurrentSafeAndCreatesOneLedger(t *testing.T) {
	db, engine := setupAdminOperationsTest(t)
	finance, token := createAdminOperationsUser(t, db, "finance-concurrent", common.RoleFinanceUser, 0)
	customer, _ := createAdminOperationsUser(t, db, "customer-concurrent", common.RoleCommonUser, 50)
	topUp := model.TopUp{UserId: customer.Id, Amount: 4, Money: 4, TradeNo: "trade-concurrent", PaymentProvider: model.PaymentProviderEpay, CreateTime: time.Now().Unix(), Status: common.TopUpStatusPending}
	if err := db.Create(&topUp).Error; err != nil {
		t.Fatalf("create concurrent topup: %v", err)
	}

	var wait sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			responses <- performAdminOperationsRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/topups/%d/complete", topUp.Id), finance, token, `{"expected_status":"pending","reason":"concurrent verification"}`, fmt.Sprintf("finance-concurrent-%d", index))
		}(index)
	}
	wait.Wait()
	close(responses)
	statuses := map[int]int{}
	for response := range responses {
		statuses[response.Code]++
	}
	if statuses[http.StatusOK] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("concurrent statuses=%v", statuses)
	}
	assertAdminOperationsUserQuota(t, db, customer.Id, 50+4*int(common.QuotaPerUnit))
	var ledgerCount int64
	if err := db.Model(&model.BalanceLedger{}).Where("idempotency_key = ?", fmt.Sprintf("topup:%d", topUp.Id)).Count(&ledgerCount).Error; err != nil || ledgerCount != 1 {
		t.Fatalf("concurrent ledger count=%d err=%v", ledgerCount, err)
	}
}

func TestAdminTopUpCacheFailureReportsCommittedStateAndRetryCannotDuplicate(t *testing.T) {
	db, engine := setupAdminOperationsTest(t)
	finance, token := createAdminOperationsUser(t, db, "finance-cache", common.RoleFinanceUser, 0)
	customer, _ := createAdminOperationsUser(t, db, "customer-cache", common.RoleCommonUser, 75)
	topUp := model.TopUp{UserId: customer.Id, Amount: 1, Money: 1, TradeNo: "trade-cache", PaymentProvider: model.PaymentProviderEpay, CreateTime: time.Now().Unix(), Status: common.TopUpStatusPending}
	if err := db.Create(&topUp).Error; err != nil {
		t.Fatalf("create cache topup: %v", err)
	}

	originalRDB := common.RDB
	failingRedis := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 10 * time.Millisecond, ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: 0})
	common.RDB = failingRedis
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = failingRedis.Close()
		common.RDB = originalRDB
	})

	response := performAdminOperationsRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/topups/%d/complete", topUp.Id), finance, token, `{"expected_status":"pending","reason":"cache failure verification"}`, "finance-cache-commit")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"success":true`) || !strings.Contains(response.Body.String(), `"status":"success"`) || !strings.Contains(response.Body.String(), `"warning":"committed_cache_sync_pending"`) {
		t.Fatalf("cache failure response status=%d body=%s", response.Code, response.Body.String())
	}
	assertBodyOmits(t, response.Body.String(), "127.0.0.1", "connection refused", "dial tcp")
	assertAdminOperationsUserQuota(t, db, customer.Id, 75+int(common.QuotaPerUnit))
	var ledger model.BalanceLedger
	if err := db.Where("idempotency_key = ?", fmt.Sprintf("topup:%d", topUp.Id)).First(&ledger).Error; err != nil {
		t.Fatalf("load committed cache ledger: %v", err)
	}

	common.RedisEnabled = false
	retry := performAdminOperationsRequest(t, engine, http.MethodPost, fmt.Sprintf("/api/admin/topups/%d/complete", topUp.Id), finance, token, `{"expected_status":"pending","reason":"cache failure verification"}`, "finance-cache-retry")
	if retry.Code != http.StatusConflict || !strings.Contains(retry.Body.String(), `"status":"success"`) {
		t.Fatalf("cache retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	assertAdminOperationsUserQuota(t, db, customer.Id, 75+int(common.QuotaPerUnit))
	var ledgerCount int64
	if err := db.Model(&model.BalanceLedger{}).Where("idempotency_key = ?", fmt.Sprintf("topup:%d", topUp.Id)).Count(&ledgerCount).Error; err != nil || ledgerCount != 1 {
		t.Fatalf("cache retry ledger count=%d err=%v", ledgerCount, err)
	}
	var audit model.Log
	if err := db.Where("type = ? AND request_id = ?", model.LogTypeManage, "finance-cache-commit").First(&audit).Error; err != nil || !strings.Contains(audit.Other, `"result":"committed_cache_sync_pending"`) {
		t.Fatalf("cache pending audit=%#v err=%v", audit, err)
	}
}

func TestAdminRequestAndAuditLogsShareSafeFiltersAndCSVProjection(t *testing.T) {
	db, engine := setupAdminOperationsTest(t)
	admin, token := createAdminOperationsUser(t, db, "log-admin", common.RoleAdminUser, 0)
	now := time.Now().Unix()
	logs := []model.Log{
		{UserId: 71, Username: "=formula-user", CreatedAt: now - 60, Type: model.LogTypeConsume, Content: "PASSWORD_CONTENT", ModelName: "=formula-model", Quota: 500000, PromptTokens: 11, CompletionTokens: 7, UseTime: 321, Ip: "198.51.100.9", RequestId: "=formula-request", UpstreamRequestId: "upstream-secret", TokenName: "access-token-secret", Other: `{"password":"PASSWORD_OTHER","upstream_response":"SECRET_BODY"}`},
		{UserId: 72, Username: "error-user", CreatedAt: now - 120, Type: model.LogTypeError, Content: "SECRET_ERROR_BODY", ModelName: "gpt-error", PromptTokens: 3, CompletionTokens: 0, UseTime: 99, RequestId: "request-error", Other: `{"credential":"SECRET_CREDENTIAL"}`},
		{UserId: 71, Username: "old-user", CreatedAt: now - 25*60*60, Type: model.LogTypeConsume, Content: "OLD_CONTENT", ModelName: "old-model", RequestId: "old-request"},
		{UserId: admin.Id, Username: admin.Username, CreatedAt: now - 30, Type: model.LogTypeManage, Content: "RAW_AUDIT_CONTENT_PASSWORD", RequestId: "audit-request", Other: fmt.Sprintf(`{"op":{"action":"user.status_update","params":{"target_user_id":71,"from_status":1,"to_status":2,"reason":"=formula-reason","password":"PARAM_SECRET"}},"admin_info":{"admin_id":%d,"admin_username":"%s","admin_role":%d,"access_token":"ADMIN_SECRET"},"audit_info":{"upstream_response":"AUDIT_SECRET_BODY"}}`, admin.Id, admin.Username, admin.Role)},
		{UserId: admin.Id, Username: admin.Username, CreatedAt: now - 29, Type: model.LogTypeManage, Content: "SECOND_RAW_AUDIT", RequestId: "audit-request", Other: fmt.Sprintf(`{"op":{"action":"user.status_update","params":{"target_user_id":72,"from_status":1,"to_status":2,"reason":"other operator"}},"admin_info":{"admin_id":%d,"admin_username":"other-operator","admin_role":%d}}`, admin.Id*10, admin.Role)},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	recent := performAdminOperationsRequest(t, engine, http.MethodGet, "/api/admin/request-logs?p=1&page_size=10", admin, token, "", "request-log-list")
	assertRequestLogPage(t, recent, 2, 2)
	assertBodyOmits(t, recent.Body.String(), "PASSWORD_CONTENT", "PASSWORD_OTHER", "SECRET_BODY", "SECRET_ERROR_BODY", "198.51.100.9", "upstream-secret", "access-token-secret", `"content"`, `"other"`, `"ip"`, `"upstream_request_id"`, `"token_name"`, `"status_code"`, `"channel"`, `"error_summary"`)
	legacy := performAdminOperationsRequest(t, engine, http.MethodGet, "/api/log/?p=1&page_size=10&type=0", admin, token, "", "legacy-request-log-list")
	assertRequestLogPage(t, legacy, 2, 2)
	assertBodyOmits(t, legacy.Body.String(), "PASSWORD_CONTENT", "PASSWORD_OTHER", "SECRET_BODY", "SECRET_ERROR_BODY", "198.51.100.9", "upstream-secret", "access-token-secret", `"content"`, `"other"`, `"ip"`, `"upstream_request_id"`, `"token_name"`)

	filtered := performAdminOperationsRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/request-logs?p=1&page_size=10&from=%d&to=%d&status=success&type=%d&user_id=71&model=%s&request_id=%s", now-3600, now+1, model.LogTypeConsume, "=formula-model", "=formula-request"), admin, token, "", "request-log-filter")
	assertRequestLogPage(t, filtered, 1, 1)
	for _, required := range []string{`"request_id":"=formula-request"`, `"latency":321`, `"prompt_tokens":11`, `"completion_tokens":7`, `"total_tokens":18`, `"quota":500000`, `"billed_amount":1`} {
		if !strings.Contains(filtered.Body.String(), required) {
			t.Fatalf("filtered request logs missing %q: %s", required, filtered.Body.String())
		}
	}

	requestCSV := performAdminOperationsRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/request-logs/export?from=%d&to=%d&status=success&user_id=71", now-3600, now+1), admin, token, "", "request-log-export")
	assertCSV(t, requestCSV, []string{"id", "created_at", "status", "type", "user_id", "username", "model", "request_id", "latency", "prompt_tokens", "completion_tokens", "total_tokens", "quota", "billed_amount"}, []string{"'=formula-user", "'=formula-model", "'=formula-request"})
	assertBodyOmits(t, requestCSV.Body.String(), "PASSWORD_CONTENT", "PASSWORD_OTHER", "SECRET_BODY", "198.51.100.9", "upstream-secret", "access-token-secret")
	assertAuditAction(t, db, "request-log-export", "export.request_logs")

	audits := performAdminOperationsRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/audit-logs?p=1&page_size=10&from=%d&to=%d&action=user.status_update&operator_id=%d&request_id=audit-request", now-3600, now+1, admin.Id), admin, token, "", "audit-log-list")
	if audits.Code != http.StatusOK || !strings.Contains(audits.Body.String(), `"total":1`) || !strings.Contains(audits.Body.String(), `"action":"user.status_update"`) || !strings.Contains(audits.Body.String(), `"request_id":"audit-request"`) || !strings.Contains(audits.Body.String(), `"admin_username":"log-admin"`) || !strings.Contains(audits.Body.String(), `"reason":"=formula-reason"`) {
		t.Fatalf("unexpected audit response status=%d body=%s", audits.Code, audits.Body.String())
	}
	assertBodyOmits(t, audits.Body.String(), "RAW_AUDIT_CONTENT_PASSWORD", "PARAM_SECRET", "ADMIN_SECRET", "AUDIT_SECRET_BODY", `"content"`, `"other"`, `"auth_method"`, `"audit_info"`)

	auditCSV := performAdminOperationsRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/audit-logs/export?from=%d&to=%d&action=user.status_update", now-3600, now+1), admin, token, "", "audit-log-export")
	assertCSV(t, auditCSV, []string{"id", "created_at", "request_id", "action", "params", "admin_id", "admin_username", "admin_role"}, []string{"=formula-reason"})
	assertBodyOmits(t, auditCSV.Body.String(), "RAW_AUDIT_CONTENT_PASSWORD", "PARAM_SECRET", "ADMIN_SECRET", "AUDIT_SECRET_BODY")
	assertAuditAction(t, db, "audit-log-export", "export.audit_logs")
}

func TestLegacyAdminCompleteTopUpUsesGovernedLedgerContract(t *testing.T) {
	db, engine := setupAdminOperationsTest(t)
	finance, token := createAdminOperationsUser(t, db, "finance-legacy", common.RoleFinanceUser, 0)
	customer, _ := createAdminOperationsUser(t, db, "customer-legacy", common.RoleCommonUser, 25)
	topUp := model.TopUp{UserId: customer.Id, Amount: 2, Money: 2, TradeNo: "trade-legacy", PaymentProvider: model.PaymentProviderEpay, CreateTime: time.Now().Unix(), Status: common.TopUpStatusPending}
	if err := db.Create(&topUp).Error; err != nil {
		t.Fatalf("create legacy topup: %v", err)
	}

	ungoverned := performAdminOperationsRequest(t, engine, http.MethodPost, "/api/user/topup/complete", finance, token, `{"trade_no":"trade-legacy"}`, "legacy-ungoverned")
	if ungoverned.Code != http.StatusBadRequest {
		t.Fatalf("legacy ungoverned status=%d body=%s", ungoverned.Code, ungoverned.Body.String())
	}
	governed := performAdminOperationsRequest(t, engine, http.MethodPost, "/api/user/topup/complete", finance, token, `{"trade_no":"trade-legacy","expected_status":"pending","reason":"legacy route verification"}`, "legacy-governed")
	if governed.Code != http.StatusOK || !strings.Contains(governed.Body.String(), `"status":"success"`) {
		t.Fatalf("legacy governed status=%d body=%s", governed.Code, governed.Body.String())
	}
	assertAdminOperationsUserQuota(t, db, customer.Id, 25+2*int(common.QuotaPerUnit))
	var ledger model.BalanceLedger
	if err := db.Where("idempotency_key = ?", fmt.Sprintf("topup:%d", topUp.Id)).First(&ledger).Error; err != nil || ledger.SourceType != "topup_completion" || ledger.RequestID != "legacy-governed" {
		t.Fatalf("legacy ledger=%#v err=%v", ledger, err)
	}
}

func assertRequestLogPage(t *testing.T, recorder *httptest.ResponseRecorder, total, items int) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("request logs status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Total int              `json:"total"`
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil || !response.Success || response.Data.Total != total || len(response.Data.Items) != items {
		t.Fatalf("unexpected request log page: err=%v response=%#v body=%s", err, response, recorder.Body.String())
	}
}

func assertCSV(t *testing.T, recorder *httptest.ResponseRecorder, header []string, requiredCells []string) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("csv status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("decode csv: %v body=%s", err, recorder.Body.String())
	}
	if len(rows) < 2 || strings.Join(rows[0], "|") != strings.Join(header, "|") {
		t.Fatalf("csv header=%v rows=%v", rows[0], rows)
	}
	flatRows := make([]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		flatRows = append(flatRows, strings.Join(row, "|"))
	}
	flat := strings.Join(flatRows, "\n")
	for _, required := range requiredCells {
		if !strings.Contains(flat, required) {
			t.Fatalf("csv missing sanitized cell %q: %v", required, rows)
		}
	}
}

func setupAdminOperationsTest(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	originalDB, originalLogDB := model.DB, model.LOG_DB
	originalSQLite, originalMySQL, originalPostgreSQL := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
	originalRedis, originalMemoryCache := common.RedisEnabled, common.MemoryCacheEnabled
	originalGlobalRateLimit, originalCriticalRateLimit := common.GlobalApiRateLimitEnable, common.CriticalRateLimitEnable
	originalQuotaPerUnit := common.QuotaPerUnit
	common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = true, false, false
	common.RedisEnabled, common.MemoryCacheEnabled = false, false
	common.GlobalApiRateLimitEnable, common.CriticalRateLimitEnable = false, false
	common.QuotaPerUnit = 500000
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access sqlite: %v", err)
	}
	model.DB, model.LOG_DB = db, db
	if err := db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Log{}, &model.BalanceLedger{}, &model.Option{}, &model.ZTAPIModelConfig{}); err != nil {
		t.Fatalf("migrate admin operations tables: %v", err)
	}
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("admin-operations-test"))))
	engine.Use(middleware.RequestId())
	router.SetApiRouter(engine)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		model.DB, model.LOG_DB = originalDB, originalLogDB
		common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = originalSQLite, originalMySQL, originalPostgreSQL
		common.RedisEnabled, common.MemoryCacheEnabled = originalRedis, originalMemoryCache
		common.GlobalApiRateLimitEnable, common.CriticalRateLimitEnable = originalGlobalRateLimit, originalCriticalRateLimit
		common.QuotaPerUnit = originalQuotaPerUnit
	})
	return db, engine
}

func createAdminOperationsUser(t *testing.T, db *gorm.DB, username string, role, quota int) (model.User, string) {
	t.Helper()
	token := username + "-access-token"
	user := model.User{Username: username, Password: "PASSWORD_HASH_SECRET", Role: role, Status: common.UserStatusEnabled, Quota: quota, AffCode: username + "-aff"}
	user.SetAccessToken(token)
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user, token
}

func performAdminOperationsRequest(t *testing.T, engine *gin.Engine, method, target string, operator model.User, token, body, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-API-User", fmt.Sprintf("%d", operator.Id))
	request.Header.Set("X-Request-ID", requestID)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func assertAdminOperationsUserQuota(t *testing.T, db *gorm.DB, userID, expected int) {
	t.Helper()
	var user model.User
	if err := db.First(&user, userID).Error; err != nil || user.Quota != expected {
		t.Fatalf("user quota=%d want=%d err=%v", user.Quota, expected, err)
	}
}

func assertBodyOmits(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(body, value) {
			t.Fatalf("response exposed %q: %s", value, body)
		}
	}
}

func assertAuditAction(t *testing.T, db *gorm.DB, requestID, action string) {
	t.Helper()
	var audit model.Log
	if err := db.Where("type = ? AND request_id = ?", model.LogTypeManage, requestID).First(&audit).Error; err != nil {
		t.Fatalf("load %s audit: %v", action, err)
	}
	if !strings.Contains(audit.Other, `"action":"`+action+`"`) {
		t.Fatalf("audit action %s missing: %s", action, audit.Other)
	}
}

var _ = setting.StripeMinTopUp
var _ = operation_setting.MinTopUp
