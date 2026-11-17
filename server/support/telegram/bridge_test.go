// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/support/bridgeapi"
)

const testBridgeEndpoint = "http://ztapi-support-20261102:3012/internal/telegram-support"
const testBindingGeneration = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestBridgeClientAllowsOnlyOwnedBlueGreenEndpoints(t *testing.T) {
	for _, host := range []string{"ztapi-support-20261102", "ztapi-support-experience-20261102", "ztapi-support-experience-20261102-candidate"} {
		_, err := NewBridgeClient("http://"+host+":3012"+bridgeapi.Path, []byte(strings.Repeat("x", 32)), nil)
		if err != nil {
			t.Fatalf("owned rollout endpoint rejected: %s: %v", host, err)
		}
	}
	if _, err := NewBridgeClient("http://ztapi-support-evil.example:3012"+bridgeapi.Path, []byte(strings.Repeat("x", 32)), nil); err == nil {
		t.Fatal("arbitrary support-looking host accepted")
	}
}

type fakeBridge struct {
	requests []bridgeapi.Request
	fn       func(bridgeapi.Request) (bridgeapi.Result, error)
}

func (b *fakeBridge) Execute(ctx context.Context, req bridgeapi.Request) (bridgeapi.Result, error) {
	if err := ctx.Err(); err != nil {
		return bridgeapi.Result{}, err
	}
	b.requests = append(b.requests, req)
	if b.fn != nil {
		return b.fn(req)
	}
	return bridgeapi.Result{}, nil
}
func (b *fakeBridge) actions(action string) []bridgeapi.Request {
	var requests []bridgeapi.Request
	for _, r := range b.requests {
		if r.Action == action {
			requests = append(requests, r)
		}
	}
	return requests
}
func (h *harness) bridge(fn func(bridgeapi.Request) (bridgeapi.Result, error)) *fakeBridge {
	b := &fakeBridge{fn: fn}
	h.cfg.Bridge = b
	h.cfg.BindingKey = []byte(strings.Repeat("k", 32))
	h.restart()
	return b
}

func TestBindingChallengeIsNeverPersistedOrPromptedInPlaintext(t *testing.T) {
	h := newHarness(t)
	b := h.bridge(func(r bridgeapi.Request) (bridgeapi.Result, error) { return bridgeapi.Result{Pending: true}, nil })
	token := strings.Repeat("b", 43)
	u := h.private(1, 10, "/start bind_"+token)
	if err := h.worker.ingest(context.Background(), []Update{u}); err != nil {
		t.Fatal(err)
	}
	var row inbox
	if err := h.db.Take(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", row), token) {
		t.Fatal("plaintext binding token remained in durable inbox")
	}
	h.restart()
	h.step()
	if len(b.actions("claim_binding")) != 1 || b.actions("claim_binding")[0].Token != token || h.provider.calls != 0 {
		t.Fatal("restart lost pending secure challenge")
	}
	if err := h.db.Take(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.BindToken != "" {
		t.Fatal("completed challenge ciphertext retained")
	}
}

func TestBindingCipherRejectsMissingKeyTamperingAndCrossActorReplay(t *testing.T) {
	h := newHarness(t)
	b := h.bridge(func(bridgeapi.Request) (bridgeapi.Result, error) { return bridgeapi.Result{Pending: true}, nil })
	token := strings.Repeat("c", 43)
	row := h.worker.capture(h.private(1, 10, "/start bind_"+token))
	for _, mutate := range []func(*inbox){
		func(r *inbox) { r.UpdateID++ }, func(r *inbox) { r.SenderID++ }, func(r *inbox) { r.ChatID++ },
		func(r *inbox) { r.BindToken = "broken-ciphertext" },
	} {
		copy := row
		mutate(&copy)
		if _, err := h.worker.openBinding(copy); err == nil {
			t.Fatal("tampered or foreign binding decrypted")
		}
	}
	h.cfg.BindingKey = nil
	h.restart()
	h.step(h.private(1, 10, "/start bind_"+token))
	if len(b.actions("claim_binding")) != 0 || h.provider.calls != 0 || len(h.sent) != 1 || !strings.Contains(h.sent[0].Text, "安全绑定暂不可用") {
		t.Fatal("binding worked without encryption key")
	}
}

func TestBridgeClientPinsEndpointAndSignsFreshRawBody(t *testing.T) {
	secret := []byte(strings.Repeat("x", 32))
	nonces := map[string]bool{}
	calls := 0
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "POST" || req.URL.String() != testBridgeEndpoint || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Fatal("bridge target or auth escaped")
		}
		raw, _ := io.ReadAll(req.Body)
		ts, nonce := req.Header.Get(bridgeapi.HeaderTimestamp), req.Header.Get(bridgeapi.HeaderNonce)
		unix, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || time.Since(time.Unix(unix, 0)).Abs() > 5*time.Second || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(nonce) || nonces[nonce] {
			t.Fatal("bad timestamp or replayed nonce")
		}
		nonces[nonce] = true
		mac := hmac.New(sha256.New, secret)
		fmt.Fprintf(mac, "%s\n%s\nPOST\n%s\n", ts, nonce, bridgeapi.Path)
		mac.Write(raw)
		if req.Header.Get(bridgeapi.HeaderSignature) != hex.EncodeToString(mac.Sum(nil)) {
			t.Fatal("signature does not cover exact body")
		}
		return testResponse(200, `{"success":true,"data":{"bound":true,"binding_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
	})}
	b, err := NewBridgeClient(testBridgeEndpoint, secret, client)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := b.Execute(context.Background(), bridgeapi.Request{Action: "binding_status", TelegramID: 10})
		if err != nil || !result.Bound || result.BindingGeneration != testBindingGeneration {
			t.Fatalf("bridge failed: %v", err)
		}
	}
	if calls != 2 {
		t.Fatal("bridge retried unexpectedly")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", b, b), string(secret)) {
		t.Fatal("bridge secret rendered")
	}
}

func TestBridgeClientRejectsMaliciousEndpointsRedirectsAndOversize(t *testing.T) {
	secret := []byte(strings.Repeat("x", 32))
	for _, endpoint := range []string{"https://ztapi.vip" + bridgeapi.Path, "http://127.0.0.1:3012" + bridgeapi.Path, testBridgeEndpoint + "?x=1", testBridgeEndpoint + "#x", strings.Replace(testBridgeEndpoint, "http://", "http://user:pass@", 1), testBridgeEndpoint + "/", strings.Replace(testBridgeEndpoint, "/internal/", "/%69nternal/", 1)} {
		if _, err := NewBridgeClient(endpoint, secret, nil); err == nil {
			t.Fatalf("accepted endpoint %s", endpoint)
		}
	}
	if _, err := NewBridgeClient(testBridgeEndpoint, secret[:31], nil); err == nil {
		t.Fatal("short secret accepted")
	}
	for _, body := range []string{`{"success":false,"message":"PRIVATE_BACKEND_ERROR"}`, `{"success":true,"success":false,"data":{}}`, strings.Repeat("x", 70000)} {
		client, _ := NewBridgeClient(testBridgeEndpoint, secret, &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) { return testResponse(200, body), nil })})
		if _, err := client.Execute(context.Background(), bridgeapi.Request{Action: "binding_status", TelegramID: 10}); err == nil || strings.Contains(err.Error(), "PRIVATE_BACKEND_ERROR") {
			t.Fatal("unsafe error or response accepted")
		}
	}
	calls := 0
	client, _ := NewBridgeClient(testBridgeEndpoint, secret, &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		calls++
		res := testResponse(307, `{"success":true,"data":{}}`)
		res.Header.Set("Location", "https://evil.invalid/")
		return res, nil
	})})
	if _, err := client.Execute(context.Background(), bridgeapi.Request{Action: "binding_status", TelegramID: 10}); err == nil || calls != 1 {
		t.Fatal("redirect followed or accepted")
	}
	if _, err := client.Execute(context.Background(), bridgeapi.Request{Action: "delete_user", TelegramID: 10}); err == nil || calls != 1 {
		t.Fatal("unknown action sent")
	}
	if _, err := client.Execute(context.Background(), bridgeapi.Request{Action: "create_ticket", TelegramID: 10, Content: strings.Repeat("x", 17000)}); err == nil || calls != 1 {
		t.Fatal("large body sent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Execute(ctx, bridgeapi.Request{Action: "binding_status", TelegramID: 10}); err == nil || calls != 1 {
		t.Fatal("canceled call sent")
	}
}

func TestPrivateStartOnlyClaimsAndRequiresWebsiteConfirmation(t *testing.T) {
	h := newHarness(t)
	b := h.bridge(func(r bridgeapi.Request) (bridgeapi.Result, error) { return bridgeapi.Result{Pending: true}, nil })
	token := strings.Repeat("a", 43)
	u := h.private(1, 10, "/start bind_"+token)
	u.Message.From.Username = "not_an_identity"
	h.step(u)
	h.restart()
	h.step(u)
	claims := b.actions("claim_binding")
	if len(claims) != 1 || claims[0].Token != token || claims[0].TelegramID != 10 || claims[0].Username != "not_an_identity" || len(b.actions("binding_status")) != 0 || h.provider.calls != 0 {
		t.Fatalf("claim escaped scope: %+v", b.requests)
	}
	if len(h.sent) != 1 || !strings.Contains(h.sent[0].Text, "返回网站") || !strings.Contains(h.sent[0].Text, "确认") || strings.Contains(h.sent[0].Text, "绑定成功") || strings.Contains(h.sent[0].Text, token) {
		t.Fatalf("claim confused with binding: %+v", h.sent)
	}
	h.step(h.message(2, 10, "/start bind_"+token+" @ztapi_bot"))
	if len(b.actions("claim_binding")) != 1 {
		t.Fatal("group could claim")
	}
}

func TestPrivateAccountReadsAreBoundActorOnlyAndNeverGroupPrompts(t *testing.T) {
	h := newHarness(t)
	b := h.bridge(func(r bridgeapi.Request) (bridgeapi.Result, error) {
		switch r.Action {
		case "binding_status":
			if r.TelegramID == 10 {
				return bridgeapi.Result{Bound: true, BindingGeneration: testBindingGeneration}, nil
			}
			return bridgeapi.Result{}, nil
		case "recent_requests":
			return bridgeapi.Result{Bound: true, BindingGeneration: testBindingGeneration, Requests: []bridgeapi.Diagnosis{{RequestID: "OWNED_REQUEST_ONLY", Outcome: "failed", Summary: "OWNED_EVIDENCE_ONLY"}}}, nil
		case "diagnose":
			return bridgeapi.Result{Bound: true, BindingGeneration: testBindingGeneration, Diagnosis: &bridgeapi.Diagnosis{RequestID: r.RequestID, Summary: "OWNED_EVIDENCE_ONLY", ChargedU: ""}}, nil
		}
		return bridgeapi.Result{}, nil
	})
	h.step(h.private(1, 10, "查看我的最近请求"))
	if len(b.actions("recent_requests")) != 1 || b.actions("recent_requests")[0].TelegramID != 10 || !strings.Contains(h.sent[0].Text, "OWNED_REQUEST_ONLY") || !strings.Contains(h.sent[0].Text, "未知") {
		t.Fatalf("owned evidence not used safely: %+v", h.sent)
	}
	h.step(h.private(2, 10, "/diagnose request-owned"))
	if len(b.actions("diagnose")) != 1 || b.actions("diagnose")[0].RequestID != "request-owned" {
		t.Fatal("diagnosis unavailable")
	}
	h.step(h.private(3, 11, "查看我的最近请求"))
	if !strings.Contains(h.sent[len(h.sent)-1].Text, bindingURL) || len(b.actions("recent_requests")) != 1 {
		t.Fatal("unbound actor read evidence")
	}
	h.step(h.message(4, h.cfg.OwnerID, "/support_on"))
	h.step(h.message(5, 10, "@ztapi_bot 查看我的最近请求"))
	h.step(h.message(6, 10, "@ztapi_bot 配置怎么设置？"))
	if len(b.actions("recent_requests")) != 1 {
		t.Fatal("group read account data")
	}
	for _, in := range h.provider.inputs {
		if strings.Contains(promptText(t, in), "OWNED_EVIDENCE_ONLY") {
			t.Fatal("private evidence entered unrelated prompt")
		}
	}
}

func TestBoundHandoffIsDurableSiteTicketWithFixedUpdateKey(t *testing.T) {
	h := newHarness(t)
	b := h.bridge(func(r bridgeapi.Request) (bridgeapi.Result, error) {
		if r.Action == "binding_status" {
			return bridgeapi.Result{Bound: true, BindingGeneration: testBindingGeneration}, nil
		}
		if r.Action == "create_ticket" {
			return bridgeapi.Result{Bound: true, BindingGeneration: testBindingGeneration, Ticket: &bridgeapi.Ticket{ID: 77, Status: "open"}}, nil
		}
		return bridgeapi.Result{}, nil
	})
	u := h.private(1, 10, "转人工：请求持续失败")
	h.step(u)
	h.restart()
	h.step(u)
	requests := b.actions("create_ticket")
	if len(requests) != 1 || requests[0].IdempotencyKey != "tg-action:123456789:1" || requests[0].TelegramID != 10 || h.provider.calls != 0 || !strings.Contains(h.sent[0].Text, "77") {
		t.Fatalf("handoff not durable or replayed: %+v %+v", requests, h.sent)
	}
}

func TestBridgeFailureDoesNotInventAccountFacts(t *testing.T) {
	h := newHarness(t)
	h.bridge(func(bridgeapi.Request) (bridgeapi.Result, error) {
		return bridgeapi.Result{}, errors.New("SECRET_BACKEND_FAILURE")
	})
	h.step(h.private(1, 10, "查看我的账单"))
	if h.provider.calls != 0 || len(h.sent) != 1 || strings.Contains(h.sent[0].Text, "SECRET_BACKEND_FAILURE") || !strings.Contains(h.sent[0].Text, bindingURL) {
		t.Fatalf("bridge outage fabricated data: %+v", h.sent)
	}
}
