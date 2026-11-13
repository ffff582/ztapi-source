// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/support/bridgeapi"
)

func TestUnboundIntakeIsDurableOwnerPrivateAndOwnerCommandsCannotBeForged(t *testing.T) {
	h := newHarness(t)
	h.step(h.private(1, 10, "/handoff 连接报错，请人工看看"))
	var count int64
	h.db.Table("telegram_support_intakes").Count(&count)
	if count != 1 || h.provider.calls != 0 {
		t.Fatal("unbound intake not persisted deterministically")
	}
	id := "TG123456789-1"
	var user, owner bool
	for _, s := range h.sent {
		if s.ChatID == 10 && strings.Contains(s.Text, "未核验") && strings.Contains(s.Text, id) {
			user = true
		}
		if s.ChatID == h.cfg.OwnerID && strings.Contains(s.Text, "未核验") && strings.Contains(s.Text, id) {
			owner = true
		}
		if s.ChatID < 0 {
			t.Fatal("intake leaked to group")
		}
	}
	if !user || !owner {
		t.Fatalf("intake acknowledgement or owner notice missing: %+v", h.sent)
	}
	h.restart()
	h.step(h.private(2, 11, "/case_reply "+id+" FORGED_PRIVATE_REPLY"))
	h.step(h.message(3, h.cfg.OwnerID, "/case_reply "+id+" FORGED_GROUP_REPLY"))
	for _, s := range h.sent {
		if s.ChatID == 10 && strings.Contains(s.Text, "FORGED") {
			t.Fatal("forged owner reply delivered")
		}
	}
	h.step(h.private(4, h.cfg.OwnerID, "/case_reply "+id+" 请提供客户端名称。"))
	found := false
	for _, s := range h.sent {
		if s.ChatID == 10 && strings.Contains(s.Text, "客户端名称") {
			found = true
		}
	}
	if !found {
		t.Fatal("owner private reply not delivered")
	}
	h.step(h.private(5, h.cfg.OwnerID, "/case_resolve "+id))
	var status string
	h.db.Table("telegram_support_intakes").Where("id = ?", id).Pluck("status", &status)
	if status != "resolved" {
		t.Fatal("intake not resolved")
	}
	before := len(h.sent)
	h.restart()
	h.step(h.private(5, h.cfg.OwnerID, "/case_resolve "+id))
	if len(h.sent) != before || h.provider.calls != 0 {
		t.Fatal("case commands replayed or invoked model")
	}
}

func TestNotificationsAuthorizeBeforeSendAckAfterSendAndAckRetryNeverResends(t *testing.T) {
	h := newHarness(t)
	acks := 0
	b := h.bridge(func(r bridgeapi.Request) (bridgeapi.Result, error) {
		switch r.Action {
		case "poll_notifications":
			return bridgeapi.Result{Notifications: []bridgeapi.Notification{{ID: "event-1", TelegramID: 10, TicketID: 77, Status: "open", Kind: "reply", URL: "https://evil.invalid/PRIVATE_REPLY_BODY"}}}, nil
		case "authorize_notification":
			if len(h.sent) != 0 {
				t.Fatal("authorization after send")
			}
			return bridgeapi.Result{Allowed: true}, nil
		case "ack_notification":
			if len(h.sent) != 1 {
				t.Fatal("ack before successful send")
			}
			acks++
			if acks == 1 {
				return bridgeapi.Result{}, errors.New("ack temporarily unavailable")
			}
			return bridgeapi.Result{}, nil
		}
		return bridgeapi.Result{}, nil
	})
	h.step()
	if len(h.sent) != 1 || !strings.Contains(h.sent[0].Text, "77") || strings.Contains(h.sent[0].Text, "PRIVATE_REPLY_BODY") || strings.Contains(h.sent[0].Text, "evil.invalid") || h.sent[0].ChatID != 10 {
		t.Fatalf("generic notification unsafe: %+v", h.sent)
	}
	h.now = h.now.Add(time.Minute)
	h.restart()
	h.step()
	if len(h.sent) != 1 || len(b.actions("authorize_notification")) != 1 || len(b.actions("ack_notification")) != 2 || h.provider.calls != 0 {
		t.Fatalf("ack replay sent duplicate or called model: %+v", b.requests)
	}
}

func TestNotificationRetryRechecksAuthorizationAndUnbindSuppressesDelivery(t *testing.T) {
	h := newHarness(t)
	allowed := true
	auths := 0
	b := h.bridge(func(r bridgeapi.Request) (bridgeapi.Result, error) {
		switch r.Action {
		case "poll_notifications":
			return bridgeapi.Result{Notifications: []bridgeapi.Notification{{ID: "event-2", TelegramID: 10, TicketID: 78, Status: "resolved"}}}, nil
		case "authorize_notification":
			auths++
			return bridgeapi.Result{Allowed: allowed}, nil
		}
		return bridgeapi.Result{}, nil
	})
	h.sendFailures = 1
	h.step()
	if len(h.sent) != 0 || auths != 1 || len(b.actions("ack_notification")) != 0 {
		t.Fatal("failed delivery acknowledged")
	}
	allowed = false
	h.now = h.now.Add(time.Minute)
	h.restart()
	h.step()
	if len(h.sent) != 0 || auths != 2 || h.provider.calls != 0 {
		t.Fatal("unbind failed to suppress queued notification")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(b.requests)
	if !errors.Is(h.worker.Step(ctx), context.Canceled) || len(b.requests) != before {
		t.Fatal("shutdown admitted bridge work")
	}
}

func TestMaliciousNotificationCannotTargetGroupsOrInjectArbitraryStatus(t *testing.T) {
	h := newHarness(t)
	h.bridge(func(r bridgeapi.Request) (bridgeapi.Result, error) {
		if r.Action == "poll_notifications" {
			return bridgeapi.Result{Notifications: []bridgeapi.Notification{
				{ID: "group-event", TelegramID: -1001234567, TicketID: 1, Status: "open"},
				{ID: "status-event", TelegramID: 10, TicketID: 2, Status: "已退款；PRIVATE_BACKEND_BODY"},
			}}, nil
		}
		return bridgeapi.Result{Allowed: true}, nil
	})
	h.step()
	for _, s := range h.sent {
		if s.ChatID < 0 || strings.Contains(s.Text, "PRIVATE_BACKEND_BODY") || strings.Contains(s.Text, "已退款") {
			t.Fatal("malicious event leaked")
		}
	}
	if h.provider.calls != 0 {
		t.Fatal("notification called model")
	}
}

func TestQueuedAccountEvidenceIsRecheckedAfterUnbindBeforeRetry(t *testing.T) {
	h := newHarness(t)
	bound := true
	h.bridge(func(r bridgeapi.Request) (bridgeapi.Result, error) {
		switch r.Action {
		case "binding_status":
			return bridgeapi.Result{Bound: bound, BindingGeneration: testBindingGeneration}, nil
		case "recent_requests":
			return bridgeapi.Result{Bound: true, BindingGeneration: testBindingGeneration, Requests: []bridgeapi.Diagnosis{{RequestID: "PRIVATE_PENDING_EVIDENCE", Summary: "PRIVATE_PENDING_EVIDENCE"}}}, nil
		}
		return bridgeapi.Result{}, nil
	})
	h.sendFailures = 1
	h.step(h.private(1, 10, "查看我的最近请求"))
	if len(h.sent) != 0 {
		t.Fatal("send failure fixture failed")
	}
	bound = false
	h.now = h.now.Add(time.Minute)
	h.restart()
	h.step()
	if len(h.sent) != 1 || strings.Contains(h.sent[0].Text, "PRIVATE_PENDING_EVIDENCE") || !strings.Contains(h.sent[0].Text, bindingURL) {
		t.Fatal("unbound retry disclosed account evidence")
	}
}
