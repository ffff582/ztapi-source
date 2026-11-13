// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
)

func TestWelcomeCommitsFixedOutboxWithoutQueuedModelWork(t *testing.T) {
	h := newHarness(t)
	h.enable()
	u := joinFixture(t, h, 2, `[{"id":10}]`, 0)
	if err := h.worker.ingest(context.Background(), []Update{u}); err != nil {
		t.Fatal(err)
	}
	var row inbox
	if h.db.Where("update_id = ?", 2).Take(&row).Error != nil || row.State != "done" || row.CallID != "" {
		t.Fatal("join event could be mistaken for queued AI work after rollback")
	}
	var pending outbox
	if h.db.Where("key = ?", replyKey(2)).Take(&pending).Error != nil || pending.Kind != "welcome" || pending.State != "pending" {
		t.Fatal("fixed welcome and cursor did not commit together")
	}
}

func joinFixture(t *testing.T, h *harness, id int64, members string, age time.Duration) Update {
	t.Helper()
	raw := fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"chat":{"id":-1001234567,"type":"supergroup"},"from":{"id":9},"new_chat_members":%s}}`, id, id, h.now.Add(-age).Unix(), members)
	var update Update
	if err := common.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	return update
}

func TestWelcomeNewMembersWithoutModelAndDeduplicateAcrossRestart(t *testing.T) {
	h := newHarness(t)
	h.enable()
	u := joinFixture(t, h, 2, `[{"id":10,"first_name":"New member"},{"id":11,"first_name":"Another"},{"id":12,"is_bot":true}]`, 0)
	h.step(u)
	if len(h.sent) != 1 || h.sent[0].ChatID != -1001234567 || !strings.Contains(h.sent[0].Text, "欢迎") || !strings.Contains(h.sent[0].Text, "@ztapi_bot") || h.provider.calls != 0 {
		t.Fatalf("welcome missing, wrong target or invoked model: sent=%+v calls=%d", h.sent, h.provider.calls)
	}
	h.restart()
	h.step(u)
	if len(h.sent) != 1 || h.provider.calls != 0 {
		t.Fatal("duplicate update replayed welcome")
	}
}

func TestWelcomeIgnoresBotsPrivateDisabledAndOldEvents(t *testing.T) {
	for _, tc := range []string{"bots", "self", "private", "disabled", "unknown-group", "old"} {
		t.Run(tc, func(t *testing.T) {
			h := newHarness(t)
			if tc != "disabled" {
				h.enable()
			}
			u := joinFixture(t, h, 2, `[{"id":10}]`, 0)
			switch tc {
			case "bots":
				u = joinFixture(t, h, 2, `[{"id":10,"is_bot":true}]`, 0)
			case "self":
				u = joinFixture(t, h, 2, fmt.Sprintf(`[{"id":%d}]`, h.cfg.BotID), 0)
			case "private":
				u.Message.Chat = Chat{ID: 9, Type: "private"}
			case "unknown-group":
				u.Message.Chat.ID = -1009999
			case "old":
				u = joinFixture(t, h, 2, `[{"id":10}]`, 3*time.Minute)
			}
			h.step(u)
			if len(h.sent) != 0 || h.provider.calls != 0 {
				t.Fatalf("unexpected welcome or model for %s", tc)
			}
		})
	}
}

func TestWelcomeRetryHonorsGroupDisableWithoutSending(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.sendFailures, h.retryAfter = 1, 17
	h.step(joinFixture(t, h, 2, `[{"id":10}]`, 0))
	if len(h.sent) != 0 || len(h.attempts) != 1 {
		t.Fatal("welcome was not queued for retry")
	}
	h.step(h.message(3, h.cfg.OwnerID, "/support_off"))
	h.sent = nil
	h.now = h.now.Add(18 * time.Second)
	h.step()
	if len(h.sent) != 0 || h.provider.calls != 0 {
		t.Fatal("welcome escaped group opt-out")
	}
	var count int64
	if h.db.Model(&outbox{}).Where("kind = ? AND state = ?", "welcome", "suppressed").Count(&count).Error != nil || count != 1 {
		t.Fatal("disabled welcome was not suppressed")
	}
}
