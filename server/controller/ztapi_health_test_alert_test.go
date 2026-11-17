package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestZTAPIHealthTestAlertAPI(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	require.NoError(t, model.MigrateZTAPIHealth(db))
	admin, token := createChannelOperator(t, db, "test-alert-admin", common.RoleAdminUser)
	reader, readerToken := createChannelOperator(t, db, "test-alert-reader", common.RoleSupportUser)
	ordinary, ordinaryToken := createChannelOperator(t, db, "test-alert-user", common.RoleCommonUser)
	const path = "/api/models/ztapi/health/test-alert"
	const operation = "c7c15c6b-0b70-461f-97c0-01369ad299ad"
	body := `{"operation_id":"` + operation + `","operator_id":999}`
	for _, actor := range []struct {
		user  model.User
		token string
	}{{reader, readerToken}, {ordinary, ordinaryToken}} {
		r := performChannelRequest(t, engine, http.MethodPost, path, actor.user, actor.token, body, "")
		assertChannelRequestDenied(t, r)
	}
	for _, body := range []string{`{}`, `null`, `{"operation_id":"invalid"}`, `{"operation_id":12}`, `{"operation_id":"00000000-0000-0000-0000-000000000000"}`, `{"operation_id":"` + strings.Repeat("a", 2048) + `"}`, body + ` {}`, body + strings.Repeat(" ", 2048)} {
		r := performChannelRequest(t, engine, http.MethodPost, path, admin, token, body, "")
		require.Equal(t, http.StatusBadRequest, r.Code, r.Body.String())
	}
	r := performChannelRequest(t, engine, http.MethodPost, path, admin, token, body, "")
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	require.Contains(t, r.Body.String(), `"status":"pending"`)
	require.Contains(t, r.Body.String(), `"operation_id":"`+operation+`"`)
	var job model.ZTAPIHealthOutbox
	require.NoError(t, db.First(&job, "dedup_key = ?", "alert-test:"+operation).Error)
	var audits []model.ZTAPIAuditEvent
	require.NoError(t, db.Find(&audits).Error)
	require.Len(t, audits, 1)
	require.Equal(t, admin.Id, audits[0].OperatorID)
	r = performChannelRequest(t, engine, http.MethodPost, path, admin, token, body, "")
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	require.Contains(t, r.Body.String(), fmt.Sprintf(`"id":%d`, job.ID))
	for _, operation := range []string{
		"2ba1ddca-56c6-4095-99a3-fae4f4b46a71",
		"2ba1ddca-56c6-4095-99a3-fae4f4b46a72",
		"2ba1ddca-56c6-4095-99a3-fae4f4b46a73",
		"2ba1ddca-56c6-4095-99a3-fae4f4b46a74",
		"2ba1ddca-56c6-4095-99a3-fae4f4b46a75",
	} {
		r = performChannelRequest(t, engine, http.MethodPost, path, admin, token, `{"operation_id":"`+operation+`"}`, "")
		require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	}
	r = performChannelRequest(t, engine, http.MethodPost, path, admin, token, `{"operation_id":"2ba1ddca-56c6-4095-99a3-fae4f4b46a76"}`, "")
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	r = performChannelRequest(t, engine, http.MethodGet, path+"/"+operation, ordinary, ordinaryToken, "", "")
	assertChannelRequestDenied(t, r)
	r = performChannelRequest(t, engine, http.MethodGet, path+"/invalid", reader, readerToken, "", "")
	require.Equal(t, http.StatusBadRequest, r.Code, r.Body.String())
	r = performChannelRequest(t, engine, http.MethodGet, path+"/2ba1ddca-56c6-4095-99a3-fae4f4b46a76", reader, readerToken, "", "")
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	require.NoError(t, db.Model(&job).Updates(map[string]any{"status": "leased", "attempts": 2, "lease_token": "PRIVATE-LEASE", "lease_until": 123, "last_error": "alert_transport_error"}).Error)
	r = performChannelRequest(t, engine, http.MethodGet, path+"/"+operation, reader, readerToken, "", "")
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	require.Contains(t, r.Body.String(), `"attempts":2`)
	require.Contains(t, r.Body.String(), `"last_error":"alert_transport_error"`)
	for _, private := range []string{"PRIVATE-LEASE", "lease_token", "lease_until", "DedupKey", "LeaseToken"} {
		require.NotContains(t, r.Body.String(), private)
	}
	require.NoError(t, db.Model(&job).Update("lease_until", time.Now().Unix()+60).Error)
	const receipt = `{"ok":true,"result":{"message_id":123,"date":2000000000}}`
	require.NoError(t, model.NewZTAPIHealthStore(db).FinishOutboxWithReceipt(context.Background(), job.ID, "PRIVATE-LEASE", "", 0, receipt))
	r = performChannelRequest(t, engine, http.MethodGet, path+"/"+operation, reader, readerToken, "", "")
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	var response struct {
		Data struct {
			Status  string `json:"status"`
			Receipt string `json:"delivery_receipt"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(r.Body.Bytes(), &response))
	require.Equal(t, "done", response.Data.Status)
	require.Equal(t, receipt, response.Data.Receipt)
	for _, table := range []any{&model.ZTAPIHealthState{}, &model.ZTAPIHealthEvent{}, &model.ZTAPIHealthIncident{}, &model.ZTAPIHealthRequest{}} {
		var count int64
		require.NoError(t, db.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIHealthOutbox{}).Count(&count).Error)
	require.EqualValues(t, 7, count)
	require.NoError(t, db.Model(&model.ZTAPIAuditEvent{}).Count(&count).Error)
	require.EqualValues(t, 7, count)
	require.NoError(t, db.Migrator().DropTable(&model.ZTAPIHealthOutbox{}))
	r = performChannelRequest(t, engine, http.MethodGet, path+"/"+operation, reader, readerToken, "", "")
	require.Equal(t, http.StatusServiceUnavailable, r.Code, r.Body.String())
	require.NotContains(t, r.Body.String(), "ztapi_health_outbox")
}
