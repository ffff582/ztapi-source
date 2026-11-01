// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func startTestFixture(t *testing.T) *fixture {
	t.Helper()
	f, err := newFixture(fixtureOptions{StateRoot: t.TempDir(), PublicOrigin: "http://127.0.0.1:4187", AdminOrigin: "http://127.0.0.1:4188"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	return f
}

func call(t *testing.T, f *fixture, method, path, token string, id int, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = common.Marshal(body)
		require.NoError(t, err)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:18087"+path, bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:44000"
	r.Header.Set("Origin", "http://127.0.0.1:4187")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("New-Api-User", fmt.Sprint(id))
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	return w
}

func payload(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
	require.True(t, result.Success, w.Body.String())
	return result.Data
}

func login(t *testing.T, f *fixture, username string) (string, int) {
	t.Helper()
	w := call(t, f, "POST", "/api/auth/login", "", 0, map[string]string{"username": username, "password": fixturePassword})
	require.Equal(t, 200, w.Code, w.Body.String())
	data := payload(t, w)
	return data["access_token"].(string), int(data["user"].(map[string]any)["id"].(float64))
}

func TestFixtureRejectsNonLoopbackConfiguration(t *testing.T) {
	for _, address := range []string{"0.0.0.0:18087", ":18087", "example.com:18087", "[::]:18087"} {
		require.Error(t, validateListen(address))
	}
	require.NoError(t, validateListen("127.0.0.1:0"))
	f, err := newFixture(fixtureOptions{StateRoot: t.TempDir(), PublicOrigin: "https://production.example", AdminOrigin: "http://127.0.0.1:4188"})
	require.Error(t, err)
	require.Nil(t, f)
}

func TestFixtureAuthenticatesRealLocalJWTAndRefreshCookie(t *testing.T) {
	f := startTestFixture(t)
	token, id := login(t, f, "fixture-alice-1440")
	claims, err := service.ParseZTAPIAccessToken(token, f.now)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprint(id), claims.Subject)
	require.Equal(t, 200, call(t, f, "GET", "/api/support/config", token, id, nil).Code)
	require.Equal(t, 401, call(t, f, "GET", "/api/support/config", token+"forged", id, nil).Code)
	require.Equal(t, 401, call(t, f, "GET", "/api/support/config", token, id+1, nil).Code)
	old, err := service.IssueZTAPIAccessToken(id, f.now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 401, call(t, f, "GET", "/api/support/config", old, id, nil).Code)
	logged := call(t, f, "POST", "/api/auth/login", "", 0, map[string]string{"username": "fixture-alice-1440", "password": fixturePassword})
	r := httptest.NewRequest("POST", "http://127.0.0.1:18087/api/auth/refresh", nil)
	r.RemoteAddr = "127.0.0.1:45000"
	r.Header.Set("Origin", "http://127.0.0.1:4187")
	for _, cookie := range logged.Result().Cookies() {
		require.True(t, cookie.HttpOnly)
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code)
	_, err = service.ParseZTAPIAccessToken(payload(t, w)["access_token"].(string), f.now)
	require.NoError(t, err)
}

func TestFixtureUsesPrivatePersistentAttachmentsAndOwnerScopedEvidence(t *testing.T) {
	f := startTestFixture(t)
	token, id := login(t, f, "fixture-alice-1440")
	other, otherID := login(t, f, "fixture-bob")
	known := call(t, f, "GET", fmt.Sprintf("/api/support/diagnostics/fixture-request-%d", id), token, id, nil)
	require.Equal(t, 200, known.Code)
	foreign := call(t, f, "GET", fmt.Sprintf("/api/support/diagnostics/fixture-request-%d", id), other, otherID, nil)
	missing := call(t, f, "GET", "/api/support/diagnostics/not-found", other, otherID, nil)
	require.Equal(t, 404, foreign.Code)
	require.Equal(t, missing.Body.String(), foreign.Body.String())
	conv := payload(t, call(t, f, "POST", "/api/support/conversations", token, id, map[string]string{"idempotency_key": "fixture-conversation-test"}))
	require.Equal(t, "新对话", conv["title"])
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	sent := call(t, f, "POST", "/api/support/conversations/"+conv["id"].(string)+"/messages", token, id, map[string]any{"content": "", "image": map[string]string{"mime_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(pngBytes.Bytes())}, "idempotency_key": "fixture-image-test"})
	require.Equal(t, 200, sent.Code, sent.Body.String())
	message := payload(t, sent)["message"].(map[string]any)
	attachment := int(message["attachment_id"].(float64))
	path := fmt.Sprintf("/api/support/attachments/%d", attachment)
	require.Equal(t, 200, call(t, f, "GET", path, token, id, nil).Code)
	require.Equal(t, 404, call(t, f, "GET", path, other, otherID, nil).Code)
	var count int64
	require.NoError(t, f.db.Table("support_messages").Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.EqualValues(t, 1, f.provider.calls.Load())
	require.Error(t, f.readDB.Exec("CREATE TABLE forbidden_fixture_write (id INTEGER)").Error)
	rows, err := f.readUsages(context.Background(), f.now.Add(-time.Hour), f.now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestFixtureHumanHandoffStopsProviderAndFinanceReadsReceipts(t *testing.T) {
	f := startTestFixture(t)
	user, id := login(t, f, "fixture-alice-1440")
	staff, staffID := login(t, f, "fixture-support")
	financeToken, financeID := login(t, f, "fixture-finance")
	conv := payload(t, call(t, f, "POST", "/api/support/conversations", user, id, map[string]string{"idempotency_key": "fixture-handoff-conv"}))["id"].(string)
	path := "/api/support/conversations/" + conv + "/messages"
	sendInput := map[string]string{"content": "fixture actual support handler", "idempotency_key": "fixture-ai-message"}
	require.Equal(t, 200, call(t, f, "POST", path, user, id, sendInput).Code)
	require.Equal(t, 200, call(t, f, "POST", path, user, id, sendInput).Code)
	require.EqualValues(t, 1, f.provider.calls.Load())
	ticket := payload(t, call(t, f, "POST", "/api/support/tickets", user, id, map[string]string{"conversation_id": conv, "subject": "synthetic handoff", "content": "synthetic evidence", "idempotency_key": "fixture-ticket-test"}))
	ack := call(t, f, "POST", path, user, id, map[string]string{"content": "human follow-up", "idempotency_key": "fixture-human-message"})
	require.Equal(t, 200, ack.Code)
	require.Equal(t, "", payload(t, ack)["call_id"])
	require.Equal(t, "system", payload(t, ack)["reply"].(map[string]any)["role"])
	staffPath := fmt.Sprintf("/api/admin/support/tickets/%d", int(ticket["id"].(float64)))
	require.Equal(t, 200, call(t, f, "GET", staffPath, staff, staffID, nil).Code)
	require.Equal(t, 403, call(t, f, "GET", staffPath, financeToken, financeID, nil).Code)
	reply := call(t, f, "POST", staffPath+"/replies", staff, staffID, map[string]string{"content": "human staff reply", "idempotency_key": "fixture-reply-test"})
	require.Equal(t, 201, reply.Code)
	require.Equal(t, "human", payload(t, reply)["message"].(map[string]any)["role"])
	require.EqualValues(t, 1, f.provider.calls.Load())
	report := call(t, f, "GET", "/api/admin/finance-reporting/report?month="+f.now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01"), financeToken, financeID, nil)
	require.Equal(t, 200, report.Code, report.Body.String())
	data := payload(t, report)
	require.Equal(t, "available", data["support_source_status"])
	require.Len(t, data["customer_service_costs"], 1)
	require.Equal(t, "0.000123", data["customer_service_estimate_total"])
	require.NotContains(t, report.Body.String(), "human staff reply")
	require.NotContains(t, report.Body.String(), "fixture actual support handler")
	require.Equal(t, "0.000000", data["customer_service_confirmed_total"])
}

func TestFixtureRejectsRemoteHostAndNeverOffersRelayRoutes(t *testing.T) {
	f := startTestFixture(t)
	r := httptest.NewRequest("GET", "http://production.example/healthz", nil)
	r.RemoteAddr = "127.0.0.1:42000"
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	require.Equal(t, 403, w.Code)
	for _, path := range []string{"/v1/chat/completions", "/api/channel/", "/api/user/1/", "/api/admin/finance-reporting/facts"} {
		status := call(t, f, "POST", path, "", 0, map[string]string{"synthetic": "only"}).Code
		require.True(t, status == 404 || status == 405, path)
	}
	metrics := call(t, f, "GET", "/__fixture/status", "", 0, nil)
	require.Equal(t, 200, metrics.Code)
	require.True(t, strings.Contains(metrics.Body.String(), `"paid_calls":0`))
}
