package controller_test

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestZTAPIPendingNoChargeHTTPPermissionsProofAndReplay(t *testing.T) {
	db, engine := setupBalanceLedgerControllerTest(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIPendingResolution{}))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	finance, auth := createBalanceLedgerOperator(t, db, "nocharge-finance", common.RoleFinanceUser)
	support, supportAuth := createBalanceLedgerOperator(t, db, "nocharge-support", common.RoleSupportUser)
	target := createBalanceLedgerTarget(t, db, "nocharge-customer", 1000)
	token := model.Token{UserId: target.Id, KeyHash: "nocharge-customer-hash", RemainQuota: 1000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	row, err := model.BeginZTAPIRequestSettlement(model.ZTAPIRequestSettlement{OperationID: "nocharge-http-op", RequestID: "nocharge-http-request", UserID: target.Id, TokenID: token.Id, PublicModel: "quoted-model", PriceSnapshotJSON: `{}`, ReservedQuota: 200})
	require.NoError(t, err)
	_, err = model.BeginZTAPIRequestAttempt(row.OperationID, 11, "version-1", "chat")
	require.NoError(t, err)
	_, err = model.PendZTAPIRequestSettlement(row.OperationID, "{}", `["upstream_billing_unconfirmed"]`)
	require.NoError(t, err)
	path := fmt.Sprintf("/api/admin/request-settlements/%d/confirm-no-charge", row.ID)
	proof := `{"source":"supplier","proof_id":"statement-1","verification_reference":"confirmed statement row 5","attempts":[{"attempt":1,"channel_id":11,"credential_version":"version-1","upstream_request_id":"","verification_reference":"statement row 5"}]}`
	denied := performBalanceLedgerRequest(t, engine, http.MethodPost, path, support, supportAuth, proof)
	assertBalanceLedgerDenied(t, denied)
	for _, bad := range []string{`{"trusted":true}`, `{"source":"a","proof_id":"b","verification_reference":"c","attempts":[{"amount":100}]}`} {
		response := performBalanceLedgerRequest(t, engine, http.MethodPost, path, finance, auth, bad)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	for i := 0; i < 2; i++ {
		response := performBalanceLedgerRequest(t, engine, http.MethodPost, path, finance, auth, proof)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"status":"released"`)
	}
	var customer model.User
	require.NoError(t, db.First(&customer, target.Id).Error)
	require.Equal(t, 1000, customer.Quota)
	var key model.Token
	require.NoError(t, db.First(&key, token.Id).Error)
	require.Equal(t, 1000, key.RemainQuota)
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIPendingResolution{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
