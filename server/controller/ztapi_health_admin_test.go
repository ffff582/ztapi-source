package controller_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestZTAPIHealthAdminReadAndRecovery(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	require.NoError(t, model.MigrateZTAPIHealth(db))
	cfg := seedZTAPIModelConfig(t, db, "health-test-source")
	root, token := createChannelOperator(t, db, "health-root", common.RoleRootUser)
	ordinaryUser, ordinary := createChannelOperator(t, db, "health-ordinary", common.RoleCommonUser)
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		if key == token {
			req.Header.Set("New-Api-User", fmt.Sprint(root.Id))
		} else if key == ordinary {
			req.Header.Set("New-Api-User", fmt.Sprint(ordinaryUser.Id))
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	path := fmt.Sprintf("/api/models/ztapi/%d/health", cfg.ID)
	denied := call("GET", path, ordinary, "")
	require.Contains(t, denied.Body.String(), `"success":false`)
	require.NotContains(t, denied.Body.String(), `"state":`)
	r := call("GET", path, token, "")
	require.Equal(t, 200, r.Code, r.Body.String())
	require.Contains(t, r.Body.String(), `"observed":false`)
	require.Equal(t, 400, call("GET", path+"?after_sequence=-1", token, "").Code)
	require.NoError(t, db.Create(&model.ZTAPIHealthState{ModelID: cfg.ID, Generation: 1, Open: true, IncidentID: 1}).Error)
	require.NoError(t, db.Create(&model.ZTAPIHealthIncident{ID: 1, ModelID: cfg.ID, Generation: 1}).Error)
	require.NoError(t, db.Create(&model.ZTAPIHealthVerificationCase{
		ID: "admin-visible-case", ModelID: cfg.ID, ChannelID: 7, EntryProtocol: "chat", Protocol: "responses",
		CredentialVersion: strings.Repeat("a", 64), Generation: 1, SourceEventID: 21, State: "completed",
		LeaseToken: "PRIVATE-CASE-LEASE", ProbeRequestID: "ztapi-health-verify-admin", Result: "failure", CreatedAt: 100, CompletedAt: 101,
	}).Error)
	require.NoError(t, db.Create(&model.ZTAPIHealthRouteState{
		ModelID: cfg.ID, ChannelID: 7, EntryProtocol: "chat", Protocol: "responses", CredentialVersion: strings.Repeat("a", 64),
		Generation: 1, IndependentFailures: 2, Open: true, LastProbeRequestID: "ztapi-health-verify-admin", LastResult: "failure", OpenedAt: 101, UpdatedAt: 101,
	}).Error)
	require.NoError(t, db.Create(&model.ZTAPIHealthEvent{
		ExecutionID: "admin-secret-event", RequestID: "customer-visible-request", ModelID: cfg.ID, Generation: 1,
		CredentialVersion: strings.Repeat("b", 64), CompletionSequence: 1, Source: "real", Result: "failure",
		Reason: "upstream_http_error", Outcome: `{"Attempts":[{"credential_version":"PRIVATE-EVENT-FINGERPRINT","authorization":"Bearer sk-private"}]}`,
	}).Error)
	require.NoError(t, db.Create(&model.ZTAPIHealthOutbox{DedupKey: "test-private", Kind: "alert", ModelID: cfg.ID, Status: "leased", LeaseToken: "PRIVATE-LEASE-NEVER-RETURN"}).Error)
	r = call("GET", path, token, "")
	require.Equal(t, 200, r.Code, r.Body.String())
	require.NotContains(t, r.Body.String(), "PRIVATE-LEASE")
	require.NotContains(t, r.Body.String(), strings.Repeat("a", 64))
	require.NotContains(t, r.Body.String(), strings.Repeat("b", 64))
	require.NotContains(t, r.Body.String(), "PRIVATE-EVENT-FINGERPRINT")
	require.NotContains(t, r.Body.String(), "sk-private")
	require.Contains(t, r.Body.String(), `"request_id":"customer-visible-request"`)
	require.Contains(t, r.Body.String(), `"verification_cases"`)
	require.Contains(t, r.Body.String(), `"route_states"`)
	require.Contains(t, r.Body.String(), `"probe_request_id":"ztapi-health-verify-admin"`)
	require.Contains(t, r.Body.String(), `"channel_id":7`)
	require.Equal(t, 400, call("POST", path+"/recover", token, `{"generation":1,"evidence":""}`).Code)
	require.Equal(t, 409, call("POST", path+"/recover", token, `{"generation":9,"evidence":"test evidence"}`).Code)
	r = call("POST", path+"/recover", token, `{"generation":1,"evidence":"test evidence"}`)
	require.Equal(t, 200, r.Code, r.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &body))
	require.Equal(t, true, body["success"])
	var updated model.ZTAPIModelConfig
	require.NoError(t, db.First(&updated, cfg.ID).Error)
	require.False(t, updated.Published)
	var state model.ZTAPIHealthState
	require.NoError(t, db.First(&state, "model_id = ?", cfg.ID).Error)
	require.False(t, state.Open)
	require.EqualValues(t, 2, state.Generation)
	require.Equal(t, 409, call("POST", path+"/recover", token, `{"generation":1,"evidence":"test evidence"}`).Code)
}

func TestZTAPIHealthAdminWorkerStatus(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	require.NoError(t, db.AutoMigrate(model.ZTAPIProbeMigrationTypes()...))
	root, token := createChannelOperator(t, db, "health-status-root", common.RoleRootUser)
	require.NoError(t, db.Create(&model.ZTAPIHealthWorkerStatus{Component: "alert", Code: "alert_recipient_missing_or_invalid", UpdatedAt: 123}).Error)
	response := performChannelRequest(t, engine, http.MethodGet, "/api/models/ztapi/health/status", root, token, "", "")
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"probe_budget":null`)
	require.Contains(t, response.Body.String(), `"alert_recipient_missing_or_invalid"`)
	require.NotContains(t, response.Body.String(), "ProbeKey")
}
