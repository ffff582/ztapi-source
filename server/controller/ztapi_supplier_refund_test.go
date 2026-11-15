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
	"gorm.io/gorm"
)

func setupSupplierRefundController(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	db, engine := setupBalanceLedgerControllerTest(t)
	if err := model.MigrateZTAPISupplierRefund(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.Token{}); err != nil {
		t.Fatal(err)
	}
	group := engine.Group("/test/supplier-refunds")
	group.GET("", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetZTAPISupplierRefunds)
	group.POST("", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.SubmitZTAPISupplierRefund)
	group.POST("/:id/approve", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.ApproveZTAPISupplierRefund)
	return db, engine
}

func TestZTAPISupplierRefundHTTPPermissionsAndUntrustedSubmit(t *testing.T) {
	db, engine := setupSupplierRefundController(t)
	finance, key := createBalanceLedgerOperator(t, db, "supplier-finance", common.RoleFinanceUser)
	support, supportKey := createBalanceLedgerOperator(t, db, "supplier-support", common.RoleSupportUser)
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/test/supplier-refunds", ""},
		{http.MethodPost, "/test/supplier-refunds", `{"source":"a","proof_id":"1","mode":"full","evidence_reference":"row:1"}`},
		{http.MethodPost, "/test/supplier-refunds/1/approve", `{"verification_reference":"review:1"}`},
	} {
		res := performBalanceLedgerRequest(t, engine, req.method, req.path, support, supportKey, req.body)
		assertBalanceLedgerDenied(t, res)
	}
	res := performBalanceLedgerRequest(t, engine, http.MethodPost, "/test/supplier-refunds", finance, key, `{"source":"supplier","proof_id":"untrusted","request_id":"not-found","user_id":777,"mode":"full","evidence_reference":"statement:1"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("submit=%d %s", res.Code, res.Body.String())
	}
	var proof model.ZTAPISupplierRefund
	if err := db.First(&proof).Error; err != nil {
		t.Fatal(err)
	}
	if proof.Status != "pending" {
		t.Fatalf("submission trusted: %+v", proof)
	}
	var count int64
	if err := db.Model(&model.ZTAPISupplierRefundApproval{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("submission manufactured approval")
	}
	res = performBalanceLedgerRequest(t, engine, http.MethodGet, "/test/supplier-refunds", finance, key, "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "untrusted") {
		t.Fatalf("finance list=%d %s", res.Code, res.Body.String())
	}
	res = performBalanceLedgerRequest(t, engine, http.MethodPost, fmt.Sprintf("/test/supplier-refunds/%d/approve", proof.ID), finance, key, `{"verification_reference":"review:1"}`)
	if res.Code != http.StatusAccepted {
		t.Fatalf("missing lineage approval=%d %s", res.Code, res.Body.String())
	}
	var approval model.ZTAPISupplierRefundApproval
	if err := db.First(&approval).Error; err != nil {
		t.Fatal(err)
	}
	if approval.OperatorID != finance.Id {
		t.Fatalf("approval identity=%d want=%d", approval.OperatorID, finance.Id)
	}
}

func TestZTAPISupplierRefundHTTPRejectsForgedFieldsAndMalformedBodies(t *testing.T) {
	db, engine := setupSupplierRefundController(t)
	admin, key := createBalanceLedgerOperator(t, db, "supplier-admin", common.RoleAdminUser)
	for _, body := range []string{
		`{"source":"a","proof_id":"1","mode":"full","trusted":true}`,
		`{"source":"a","proof_id":"1","mode":"full","approved":true}`,
		`{"source":"a","proof_id":"1","mode":"full","refunded_quota":100}`,
		`{"source":"a","proof_id":"1","mode":"partial","units":[{"dimension":"input","units":"1","unit_quota":"999"}]}`,
		`{"source":`, `null`, `{}`, `{} {}`,
		`{"source":"` + strings.Repeat("x", 70000) + `"}`,
	} {
		res := performBalanceLedgerRequest(t, engine, http.MethodPost, "/test/supplier-refunds", admin, key, body)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("bad submit=%d %s", res.Code, res.Body.String())
		}
	}
	for _, path := range []string{"/test/supplier-refunds/0/approve", "/test/supplier-refunds/-1/approve", "/test/supplier-refunds/nope/approve"} {
		res := performBalanceLedgerRequest(t, engine, http.MethodPost, path, admin, key, `{"verification_reference":"review:1"}`)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("bad id=%d %s", res.Code, res.Body.String())
		}
	}
	res := performBalanceLedgerRequest(t, engine, http.MethodPost, "/test/supplier-refunds/1/approve", admin, key, `{"verification_reference":"review:1","operator_id":999}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("forged operator=%d %s", res.Code, res.Body.String())
	}
	for _, query := range []string{"?limit=-1", "?limit=1001", "?after_id=-1", "?status=bogus"} {
		res := performBalanceLedgerRequest(t, engine, http.MethodGet, "/test/supplier-refunds"+query, admin, key, "")
		if res.Code != http.StatusBadRequest {
			t.Fatalf("bad query=%d %s", res.Code, res.Body.String())
		}
	}
}

func TestZTAPISupplierRefundHTTPDisabledFinanceDenied(t *testing.T) {
	db, engine := setupSupplierRefundController(t)
	finance, key := createBalanceLedgerOperator(t, db, "supplier-disabled", common.RoleFinanceUser)
	if err := db.Model(&model.User{}).Where("id = ?", finance.Id).Update("status", common.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	res := performBalanceLedgerRequest(t, engine, http.MethodGet, "/test/supplier-refunds", finance, key, "")
	assertBalanceLedgerDenied(t, res)
}
