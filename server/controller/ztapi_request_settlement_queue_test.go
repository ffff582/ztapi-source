package controller_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSettlementQueueController(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	db, engine := setupBalanceLedgerControllerTest(t)
	if err := db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPIHealthRequest{}, &model.ZTAPIHealthEvent{}, &model.ZTAPISupplierRefundCharge{}); err != nil {
		t.Fatal(err)
	}
	engine.GET("/test/admin/settlements", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetAdminZTAPIRequestSettlements)
	engine.GET("/test/admin/settlements/:id", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetAdminZTAPIRequestSettlement)
	engine.GET("/test/self/settlements", middleware.UserAuth(), controller.GetSelfZTAPIRequestSettlements)
	return db, engine
}

func TestZTAPISettlementQueueDetailSeparatesOriginalAndAdditionalCharges(t *testing.T) {
	db, engine := setupSettlementQueueController(t)
	finance, key := createBalanceLedgerOperator(t, db, "charge-total-finance", common.RoleFinanceUser)
	row := model.ZTAPIRequestSettlement{RequestID: "charge-total-request", OperationID: "charge-total-op", UserID: 777, PublicModel: "quoted-model", Status: model.ZTAPISettlementSettled, FinalAttempt: 2, ChargedQuota: 100, RefundedQuota: 130, PriceSnapshotJSON: "{}", UsageJSON: "{}", ChargeDimensionsJSON: "[]"}
	require.NoError(t, db.Create(&row).Error)
	for _, charge := range []model.ZTAPISupplierRefundCharge{
		{RequestID: row.RequestID, SettlementID: row.ID, Attempt: 2, UserID: row.UserID, ChargedQuota: 100, RefundedQuota: 100},
		{RequestID: row.RequestID, SettlementID: row.ID, Attempt: 1, UserID: row.UserID, ChargedQuota: 40, RefundedQuota: 30, BillingProofID: 1},
	} {
		require.NoError(t, db.Create(&charge).Error)
	}
	response := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/test/admin/settlements/%d", row.ID), finance, key, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body struct {
		Data struct {
			Original   int64            `json:"charged_quota"`
			Additional int64            `json:"additional_charged_quota"`
			Total      int64            `json:"total_charged_quota"`
			Refunded   int64            `json:"refunded_quota"`
			Net        int64            `json:"net_charged_quota"`
			Charges    []map[string]any `json:"charge_anchors"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	require.EqualValues(t, 100, body.Data.Original)
	require.EqualValues(t, 40, body.Data.Additional)
	require.EqualValues(t, 140, body.Data.Total)
	require.EqualValues(t, 130, body.Data.Refunded)
	require.EqualValues(t, 10, body.Data.Net)
	require.Len(t, body.Data.Charges, 2)
}

func TestZTAPISettlementQueueSelfIsOwnerScopedAndRedacted(t *testing.T) {
	db, engine := setupSettlementQueueController(t)
	user, key := createBalanceLedgerOperator(t, db, "pending-customer", common.RoleCommonUser)
	other, _ := createBalanceLedgerOperator(t, db, "other-customer", common.RoleCommonUser)
	for i, owner := range []int{user.Id, other.Id} {
		row := model.ZTAPIRequestSettlement{RequestID: fmt.Sprintf("pending-%d", i), OperationID: fmt.Sprintf("op-%d", i), UserID: owner, TokenID: 100 + i, PublicModel: "visible-model", Status: "pending", ReservedQuota: 50, PriceSnapshotJSON: `{"secret-cost":"provider-cost"}`, UsageJSON: `{"supplier_proof":"proof-secret"}`, MissingDimensionsJSON: `["upstream-secret"]`, ChargeDimensionsJSON: "[]"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.ZTAPIRequestSettlement{RequestID: "already-settled", OperationID: "settled-op", UserID: user.Id, PublicModel: "visible-model", Status: "settled", PriceSnapshotJSON: "{}", UsageJSON: "{}", MissingDimensionsJSON: "[]", ChargeDimensionsJSON: "[]"}).Error; err != nil {
		t.Fatal(err)
	}
	res := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/test/self/settlements?user_id=%d", other.Id), user, key, "")
	if res.Code != http.StatusOK {
		t.Fatalf("self=%d %s", res.Code, res.Body.String())
	}
	text := res.Body.String()
	if !strings.Contains(text, "pending-0") || !strings.Contains(text, "visible-model") {
		t.Fatalf("own pending missing: %s", text)
	}
	for _, forbidden := range []string{"pending-1", "already-settled", "secret-cost", "provider-cost", "proof-secret", "upstream-secret", "price_snapshot", "usage", "token_id", "operation_id"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("self leaked %q: %s", forbidden, text)
		}
	}
	var rows int64
	if err := db.Model(&model.ZTAPIRequestSettlement{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Fatal("read mutated settlements")
	}
}

func TestZTAPISettlementQueueAdminDetailPreservesPriceUsageAttempts(t *testing.T) {
	db, engine := setupSettlementQueueController(t)
	finance, key := createBalanceLedgerOperator(t, db, "queue-finance", common.RoleFinanceUser)
	row := model.ZTAPIRequestSettlement{RequestID: "detail-request", OperationID: "detail-op", UserID: 777, PublicModel: "test-model", Status: "pending", ReservedQuota: 80, PriceSnapshotJSON: `{"input_unit_quota":"2.5"}`, UsageJSON: `{"input_tokens":7}`, MissingDimensionsJSON: `["output"]`, ChargeDimensionsJSON: "[]", Dispatched: true}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ZTAPIRequestAttempt{SettlementID: row.ID, Attempt: 1, ChannelID: 11, CredentialVersion: "frozen-credential-v1", Protocol: "chat", UpstreamRequestID: "durable-wire-before-health"}).Error; err != nil {
		t.Fatal(err)
	}
	health := model.ZTAPIHealthRequest{ExecutionID: row.RequestID, RequestID: "client-correlation", UserID: row.UserID, Admissions: `[{"Index":1,"ChannelID":11},{"Index":2,"ChannelID":22}]`}
	if err := db.Create(&health).Error; err != nil {
		t.Fatal(err)
	}
	decoy := model.ZTAPIHealthRequest{ExecutionID: "other-execution", RequestID: row.RequestID, UserID: row.UserID, Admissions: "[]"}
	if err := db.Create(&decoy).Error; err != nil {
		t.Fatal(err)
	}
	event := model.ZTAPIHealthEvent{ExecutionID: health.ExecutionID, RequestID: health.RequestID, Outcome: `{"Attempts":[{"Index":1,"ChannelID":11,"UpstreamRequestID":"failed-wire"},{"Index":2,"ChannelID":22,"UpstreamRequestID":"final-wire"}]}`}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	res := performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/test/admin/settlements/%d", row.ID), finance, key, "")
	if res.Code != http.StatusOK {
		t.Fatalf("detail=%d %s", res.Code, res.Body.String())
	}
	for _, wanted := range []string{"input_unit_quota", "2.5", "input_tokens", "failed-wire", "final-wire", "detail-request", "durable-wire-before-health"} {
		if !strings.Contains(res.Body.String(), wanted) {
			t.Fatalf("detail lacks %q: %s", wanted, res.Body.String())
		}
	}
	if strings.Contains(res.Body.String(), "other-execution") {
		t.Fatal("client correlation attached another request's evidence")
	}
	res = performBalanceLedgerRequest(t, engine, http.MethodGet, "/test/admin/settlements", finance, key, "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "detail-request") {
		t.Fatalf("admin queue=%d %s", res.Code, res.Body.String())
	}
	support, supportKey := createBalanceLedgerOperator(t, db, "queue-support", common.RoleSupportUser)
	res = performBalanceLedgerRequest(t, engine, http.MethodGet, fmt.Sprintf("/test/admin/settlements/%d", row.ID), support, supportKey, "")
	assertBalanceLedgerDenied(t, res)
	for _, path := range []string{"/test/admin/settlements/0", "/test/admin/settlements/-1", "/test/admin/settlements?limit=1001", "/test/self/settlements?after_id=-1"} {
		res := performBalanceLedgerRequest(t, engine, http.MethodGet, path, finance, key, "")
		if res.Code != http.StatusBadRequest {
			t.Fatalf("bad query=%d %s", res.Code, res.Body.String())
		}
	}
}
