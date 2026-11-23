// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
	"github.com/QuantumNous/new-api/support/bridgeapi"
)

type generationSource struct{}

func (generationSource) Identity(_ context.Context, id int) (support.Identity, error) {
	return support.Identity{ID: id, Status: common.UserStatusEnabled}, nil
}
func (generationSource) RecentRequests(ctx context.Context, id int) ([]support.Diagnosis, error) {
	d, err := (generationSource{}).Diagnose(ctx, id, "owned-request")
	return []support.Diagnosis{d}, err
}
func (generationSource) Diagnose(_ context.Context, id int, request string) (support.Diagnosis, error) {
	return support.Diagnosis{RequestID: request, Summary: fmt.Sprintf("PRIVATE_ACCOUNT_%d_EVIDENCE", id)}, nil
}
func (generationSource) Knowledge(context.Context, int, string, string) (string, error) {
	return "public fixture knowledge", nil
}

func newGenerationBridge(t *testing.T, h *harness) *support.TelegramBridge {
	t.Helper()
	_, store := testDB(t)
	b, err := support.NewTelegramBridge(store, generationSource{}, support.TelegramBridgeConfig{
		Secret: []byte(strings.Repeat("g", 32)), BotUsername: h.cfg.BotUsername, Now: func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.Bridge = b
	h.restart()
	return b
}

func confirmGenerationBinding(t *testing.T, b *support.TelegramBridge, user int, tg int64, key string) string {
	t.Helper()
	ctx := context.Background()
	v, err := b.UpdateBinding(ctx, user, support.BindingInput{Action: "create", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(v.BindingURL)
	if err != nil || v.Pending == nil {
		t.Fatalf("binding challenge unavailable: %v", err)
	}
	generation := v.Pending.ID
	_, err = b.Execute(ctx, bridgeapi.Request{Action: "claim_binding", TelegramID: tg, Token: strings.TrimPrefix(u.Query().Get("start"), "bind_")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.UpdateBinding(ctx, user, support.BindingInput{Action: "confirm", IdempotencyKey: key + "-confirm", ChallengeID: generation}); err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestAccountReplyRetryRequiresOriginalBindingGeneration(t *testing.T) {
	for _, command := range []string{"/diagnose owned-request", "/recent", "/binding", "/handoff connection failure"} {
		for _, rebind := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/rebind=%t", command, rebind), func(t *testing.T) {
				h := newHarness(t)
				b := newGenerationBridge(t, h)
				generation := confirmGenerationBinding(t, b, 501, 10, "account-a-binding")
				h.sendFailures = 1
				h.step(h.private(1, 10, command))
				if len(h.sent) != 0 || len(h.attempts) != 1 {
					t.Fatal("account reply was not queued after send failure")
				}
				original := h.attempts[0].Text
				if strings.Contains(command, "diagnose") || command == "/recent" {
					if !strings.Contains(original, "PRIVATE_ACCOUNT_501_EVIDENCE") {
						t.Fatal("account A evidence was not exercised")
					}
				}
				if rebind {
					if _, err := b.UpdateBinding(context.Background(), 501, support.BindingInput{Action: "unbind", IdempotencyKey: "account-a-unbind"}); err != nil {
						t.Fatal(err)
					}
					confirmGenerationBinding(t, b, 502, 10, "account-b-binding")
				}
				h.now = h.now.Add(time.Minute)
				h.restart()
				h.step()
				if len(h.sent) != 1 {
					t.Fatalf("unexpected retry delivery count: %d", len(h.sent))
				}
				if rebind && (h.sent[0].Text != bindingReply || h.sent[0].Text == original) {
					t.Fatalf("revoked account A reply escaped under account B: %q", h.sent[0].Text)
				}
				if !rebind && h.sent[0].Text != original {
					t.Fatal("current binding generation could not receive its own reply")
				}
				var queued struct{ BindingGeneration string }
				if err := h.db.Table("telegram_support_outbox").Where("key = ?", replyKey(1)).Take(&queued).Error; err != nil {
					t.Fatal(err)
				}
				if queued.BindingGeneration != generation {
					t.Fatal("the original account reply generation was not persisted")
				}
				if h.provider.calls != 0 {
					t.Fatal("deterministic account reply or retry invoked inference")
				}
			})
		}
	}
}

func TestLegacyAccountReplyWithoutGenerationIsNotDisclosed(t *testing.T) {
	h := newHarness(t)
	b := newGenerationBridge(t, h)
	confirmGenerationBinding(t, b, 502, 10, "current-account-binding")
	entry := outbox{Key: "legacy-account-reply", ChatID: 10, Text: "PRIVATE_LEGACY_ACCOUNT_EVIDENCE", Kind: "account", State: "pending", NextAttemptAt: h.now, CreatedAt: h.now}
	if err := h.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	h.step()
	if len(h.sent) != 1 || h.sent[0].Text != bindingReply || h.provider.calls != 0 {
		t.Fatalf("generationless account reply was disclosed: %+v", h.sent)
	}
}

func TestBindingGenerationNeverEntersGroupPromptsOrReplies(t *testing.T) {
	h := newHarness(t)
	b := newGenerationBridge(t, h)
	generation := confirmGenerationBinding(t, b, 501, 10, "private-account-binding")
	h.step(h.private(1, 10, "/diagnose owned-request"))
	h.step(h.message(2, h.cfg.OwnerID, "/support_on"))
	h.step(h.message(3, 10, "@ztapi_bot configuration help"))
	if h.provider.calls != 1 {
		t.Fatal("public group help was not exercised")
	}
	for _, input := range h.provider.inputs {
		text := promptText(t, input)
		if strings.Contains(text, generation) || strings.Contains(text, "PRIVATE_ACCOUNT_501_EVIDENCE") {
			t.Fatal("private authorization or account evidence entered a group prompt")
		}
	}
	for _, sent := range h.sent {
		if strings.Contains(sent.Text, generation) || sent.ChatID < 0 && strings.Contains(sent.Text, "PRIVATE_ACCOUNT_501_EVIDENCE") {
			t.Fatal("private authorization or account evidence entered a visible reply")
		}
	}
}
