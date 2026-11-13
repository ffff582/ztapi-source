package controller_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func TestAdminOverviewAggregatesBoundedRangeAndRedactsByPermission(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	if err := db.AutoMigrate(&model.Channel{}, &model.TopUp{}); err != nil {
		t.Fatalf("migrate overview tables: %v", err)
	}

	admin, adminToken := createBalanceLedgerOperator(t, db, "overview-admin", common.RoleAdminUser)
	support, supportToken := createBalanceLedgerOperator(t, db, "overview-support", common.RoleSupportUser)
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC).Unix()
	to := from + int64(24*time.Hour/time.Second)

	if err := db.Create(&[]model.User{
		{Username: "overview-user-a", Password: "unused", Status: common.UserStatusEnabled, Quota: 700, AffCode: "overview-aff-a"},
		{Username: "overview-user-b", Password: "unused", Status: common.UserStatusEnabled, Quota: 300, AffCode: "overview-aff-b"},
	}).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if err := db.Create(&[]model.Channel{
		{Name: "enabled", Key: "key-enabled", Status: common.ChannelStatusEnabled},
		{Name: "disabled", Key: "key-disabled", Status: common.ChannelStatusManuallyDisabled},
	}).Error; err != nil {
		t.Fatalf("seed channels: %v", err)
	}
	if err := db.Create(&[]model.TopUp{
		{UserId: 1, Amount: 100, TradeNo: "overview-pending", Status: common.TopUpStatusPending, CreateTime: from + 10},
		{UserId: 1, Amount: 100, TradeNo: "overview-complete", Status: common.TopUpStatusSuccess, CreateTime: from + 10},
	}).Error; err != nil {
		t.Fatalf("seed topups: %v", err)
	}
	logs := []model.Log{
		{Type: model.LogTypeConsume, CreatedAt: from, Quota: 120, ModelName: "gpt-test", ChannelId: 1},
		{Type: model.LogTypeConsume, CreatedAt: to, Quota: 80, ModelName: "gpt-test", ChannelId: 1},
		{Type: model.LogTypeError, CreatedAt: from + 30, Content: "upstream body api_key=SECRET", ModelName: "gpt-test", ChannelId: 2},
		{Type: model.LogTypeError, CreatedAt: from - 1, Content: "outside range", ModelName: "outside", ChannelId: 2},
		{Type: model.LogTypeManage, CreatedAt: from + 40, Content: "changed channel", Other: `{"op":{"action":"channel.update"}}`},
		{Type: model.LogTypeManage, CreatedAt: from + 41, Content: "raw-content-secret", Other: `{"op":{"action":"<script>SECRET_OTHER</script>"}}`},
	}
	for index := 0; index < 11; index++ {
		logs = append(logs, model.Log{Type: model.LogTypeError, CreatedAt: from + 50 + int64(index), Content: "unsafe upstream detail", ModelName: "gpt-test", ChannelId: 2})
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	adminResponse := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, to), admin, adminToken, "")
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("overview status = %d body=%s", adminResponse.Code, adminResponse.Body.String())
	}
	var adminBody struct {
		Success bool `json:"success"`
		Data    struct {
			Requests struct {
				Total   int64 `json:"total"`
				Success int64 `json:"success"`
				Failure int64 `json:"failure"`
			} `json:"requests"`
			BilledQuota      int64 `json:"billed_quota"`
			UserBalanceTotal int64 `json:"user_balance_total"`
			PendingTopUps    int64 `json:"pending_top_ups"`
			Series           []struct {
				Start       int64 `json:"start"`
				Requests    int64 `json:"requests"`
				BilledQuota int64 `json:"billed_quota"`
			} `json:"series"`
			ChannelStatuses []struct {
				Status int   `json:"status"`
				Count  int64 `json:"count"`
			} `json:"channel_statuses"`
			RecentFailures []struct {
				Summary string `json:"summary"`
			} `json:"recent_failures"`
			RecentAuditOperations []struct {
				Action string `json:"action"`
			} `json:"recent_audit_operations"`
		} `json:"data"`
	}
	if err := common.Unmarshal(adminResponse.Body.Bytes(), &adminBody); err != nil {
		t.Fatalf("decode admin overview: %v body=%s", err, adminResponse.Body.String())
	}
	if !adminBody.Success || adminBody.Data.Requests.Total != 13 || adminBody.Data.Requests.Success != 1 || adminBody.Data.Requests.Failure != 12 {
		t.Fatalf("request totals = %#v, want total=13 success=1 failure=12", adminBody.Data.Requests)
	}
	if adminBody.Data.BilledQuota != 120 || adminBody.Data.UserBalanceTotal != 1_000 || adminBody.Data.PendingTopUps != 1 {
		t.Fatalf("financial totals = quota:%d balance:%d pending:%d", adminBody.Data.BilledQuota, adminBody.Data.UserBalanceTotal, adminBody.Data.PendingTopUps)
	}
	if len(adminBody.Data.Series) != 1 || adminBody.Data.Series[0].Requests != 13 || adminBody.Data.Series[0].BilledQuota != 120 {
		t.Fatalf("daily series = %#v", adminBody.Data.Series)
	}
	if len(adminBody.Data.ChannelStatuses) != 2 || len(adminBody.Data.RecentFailures) != 10 || len(adminBody.Data.RecentAuditOperations) != 1 {
		t.Fatalf("bounded collections = statuses:%d failures:%d audits:%d", len(adminBody.Data.ChannelStatuses), len(adminBody.Data.RecentFailures), len(adminBody.Data.RecentAuditOperations))
	}
	if adminBody.Data.RecentFailures[0].Summary != "请求失败" || strings.Contains(adminResponse.Body.String(), "SECRET") || strings.Contains(adminResponse.Body.String(), "unsafe upstream detail") {
		t.Fatalf("failure response exposed unsafe content: %s", adminResponse.Body.String())
	}
	if adminBody.Data.RecentAuditOperations[0].Action != "channel.update" {
		t.Fatalf("audit action = %q, want channel.update", adminBody.Data.RecentAuditOperations[0].Action)
	}
	if strings.Contains(adminResponse.Body.String(), "SECRET_OTHER") || strings.Contains(adminResponse.Body.String(), "raw-content-secret") || strings.Contains(adminResponse.Body.String(), "<script>") {
		t.Fatalf("audit response exposed unsafe fields: %s", adminResponse.Body.String())
	}

	supportResponse := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, to), support, supportToken, "")
	if strings.Contains(supportResponse.Body.String(), "billed_quota") || strings.Contains(supportResponse.Body.String(), "user_balance_total") || strings.Contains(supportResponse.Body.String(), "pending_top_ups") || strings.Contains(supportResponse.Body.String(), "recent_audit_operations") {
		t.Fatalf("support response includes redacted fields: %s", supportResponse.Body.String())
	}
	if !strings.Contains(supportResponse.Body.String(), `"channel_statuses"`) || !strings.Contains(supportResponse.Body.String(), `"recent_failures"`) {
		t.Fatalf("support response omits permitted operations data: %s", supportResponse.Body.String())
	}
	finance, financeToken := createBalanceLedgerOperator(t, db, "overview-finance", common.RoleFinanceUser)
	financeResponse := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, to), finance, financeToken, "")
	if !strings.Contains(financeResponse.Body.String(), `"billed_quota"`) || strings.Contains(financeResponse.Body.String(), `"channel_statuses"`) || strings.Contains(financeResponse.Body.String(), `"recent_failures"`) || strings.Contains(financeResponse.Body.String(), `"recent_audit_operations"`) {
		t.Fatalf("finance response violates redaction contract: %s", financeResponse.Body.String())
	}
}

func TestAdminOverviewQueryFailureUsesFixedSafeMessage(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	if err := db.AutoMigrate(&model.Channel{}, &model.TopUp{}); err != nil {
		t.Fatalf("migrate overview tables: %v", err)
	}
	admin, token := createBalanceLedgerOperator(t, db, "overview-query-error", common.RoleAdminUser)
	if err := db.Migrator().DropTable(&model.Log{}); err != nil {
		t.Fatalf("drop logs table: %v", err)
	}
	from := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC).Unix()
	response := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, from+60), admin, token, "")
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "Unable to load operations overview.") || strings.Contains(response.Body.String(), "no such table") {
		t.Fatalf("unsafe query failure response: %d %s", response.Code, response.Body.String())
	}
}

func TestAdminOverviewReturnsNumericZeroesForEmptyDataAndRejectsInvalidRanges(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	if err := db.AutoMigrate(&model.Channel{}, &model.TopUp{}); err != nil {
		t.Fatalf("migrate overview tables: %v", err)
	}
	admin, token := createBalanceLedgerOperator(t, db, "overview-empty", common.RoleAdminUser)
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC).Unix()
	to := from + int64(24*time.Hour/time.Second)

	empty := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, to), admin, token, "")
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"total":0`) || !strings.Contains(empty.Body.String(), `"billed_quota":0`) || !strings.Contains(empty.Body.String(), `"user_balance_total":0`) || !strings.Contains(empty.Body.String(), `"pending_top_ups":0`) || !strings.Contains(empty.Body.String(), `"series":[]`) || !strings.Contains(empty.Body.String(), `"recent_failures":[]`) || !strings.Contains(empty.Body.String(), `"recent_audit_operations":[]`) {
		t.Fatalf("empty overview is not explicit zero data: %s", empty.Body.String())
	}

	for _, target := range []string{
		"/api/admin/overview?from=not-a-time&to=1",
		fmt.Sprintf("/api/admin/overview?from=%d&to=%d", to, from),
		fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, from+int64(32*24*time.Hour/time.Second)),
	} {
		response := performBalanceLedgerRequest(t, engine, http.MethodGet, target, admin, token, "")
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "access-token") || strings.Contains(response.Body.String(), "database") {
			t.Fatalf("invalid range response = %d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestAdminOverviewUsesHalfOpenWindowsAndRejectsUnauthorizedRoles(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	if err := db.AutoMigrate(&model.Channel{}, &model.TopUp{}); err != nil {
		t.Fatalf("migrate overview tables: %v", err)
	}
	admin, token := createBalanceLedgerOperator(t, db, "overview-window-admin", common.RoleAdminUser)
	commonUser, commonToken := createBalanceLedgerOperator(t, db, "overview-common", common.RoleCommonUser)
	unknownUser, unknownToken := createBalanceLedgerOperator(t, db, "overview-unknown", 999)
	from := time.Date(2026, time.August, 2, 0, 0, 0, 0, time.UTC).Unix()
	boundary := from + 60
	if err := db.Create(&model.Log{Type: model.LogTypeConsume, CreatedAt: boundary, Quota: 55}).Error; err != nil {
		t.Fatalf("seed boundary log: %v", err)
	}
	left := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, boundary), admin, token, "")
	if !strings.Contains(left.Body.String(), `"total":0`) {
		t.Fatalf("left half-open window includes boundary: %s", left.Body.String())
	}
	right := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", boundary, from+120), admin, token, "")
	if !strings.Contains(right.Body.String(), `"total":1`) {
		t.Fatalf("right half-open window omits boundary: %s", right.Body.String())
	}
	zero := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", boundary, boundary), admin, token, "")
	if zero.Code != http.StatusBadRequest {
		t.Fatalf("zero range status = %d body=%s", zero.Code, zero.Body.String())
	}
	denied := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, from+120), commonUser, commonToken, "")
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), `"success":false`) {
		t.Fatalf("common role was not denied: %d %s", denied.Code, denied.Body.String())
	}
	unknownDenied := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/admin/overview?from=%d&to=%d", from, from+120), unknownUser, unknownToken, "")
	if unknownDenied.Code != http.StatusOK || !strings.Contains(unknownDenied.Body.String(), `"success":false`) {
		t.Fatalf("unknown role was not denied: %d %s", unknownDenied.Code, unknownDenied.Body.String())
	}
}
