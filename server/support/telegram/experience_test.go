// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/support"
)

func (h *harness) private(id, sender int64, text string) Update {
	u := h.message(id, sender, text)
	u.Message.Chat = Chat{ID: sender, Type: "private"}
	return u
}

func promptText(t *testing.T, input support.ProviderInput) string {
	t.Helper()
	var text strings.Builder
	for _, m := range input.Messages {
		text.WriteString(fmt.Sprint(m.Content))
		text.WriteByte('\n')
	}
	return text.String()
}

func TestPrivateGeneralHelpNeedsNoBinding(t *testing.T) {
	h := newHarness(t)
	h.step(h.private(1, 10, "Codex 怎么配置？"))
	if h.provider.calls != 1 || len(h.sent) != 1 {
		t.Fatalf("private general help silently discarded: calls=%d sent=%d", h.provider.calls, len(h.sent))
	}
	prompt := promptText(t, h.provider.inputs[0])
	for _, required := range []string{"小 Z", "一次只问一个", "不得嘲笑客户", "不得替用户创建"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("missing policy %q", required)
		}
	}
}

func TestCredentialsAreRefusedWithoutEchoOrInference(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.step(h.message(2, 10, "@ztapi_bot Bearer secret-customer-value"))
	h.step(h.private(3, 10, "记住我的密钥 Bearer secret-customer-value"))
	if h.provider.calls != 0 || len(h.sent) != 2 {
		t.Fatalf("credential path silent or inferred: %+v", h.sent)
	}
	for _, sent := range h.sent {
		if !strings.Contains(sent.Text, "撤回") || !strings.Contains(sent.Text, "轮换") || strings.Contains(sent.Text, "secret-customer-value") {
			t.Fatalf("unsafe credential refusal: %q", sent.Text)
		}
	}
	var rows []inbox
	h.db.Find(&rows)
	for _, row := range rows {
		if strings.Contains(row.Text, "secret-customer-value") {
			t.Fatal("credential persisted")
		}
	}
}

func TestNaturalCredentialAssignmentsAreNeverPersistedOrPrompted(t *testing.T) {
	for _, text := range []string{"密码是 customer-pass-123", "password is customer-pass-123", "secret: customer-pass-123", "token=customer-pass-123"} {
		h := newHarness(t)
		h.step(h.private(1, 10, text))
		if h.provider.calls != 0 || len(h.sent) != 1 || !strings.Contains(h.sent[0].Text, "轮换") {
			t.Fatalf("credential assignment reached model: %s", text)
		}
		var row inbox
		h.db.Take(&row, 1)
		if strings.Contains(row.Text, "customer-pass-123") {
			t.Fatal("natural credential persisted")
		}
	}
}

func TestCredentialsCannotRideOwnerControlCommands(t *testing.T) {
	h := newHarness(t)
	h.step(h.message(1, h.cfg.OwnerID, "/support_on Bearer leaked-owner-credential"))
	enabled, err := h.worker.enabled(context.Background(), -1001234567)
	if err != nil {
		t.Fatal(err)
	}
	if enabled || len(h.sent) != 1 || !strings.Contains(h.sent[0].Text, "撤回") {
		t.Fatal("credential-bearing command was executed instead of refused")
	}
}

func TestSeriousPolicyAndOutputSafeguards(t *testing.T) {
	for _, reply := range []string{"哈哈，这报错真搞笑。", "已经退款了。", "已修复，请重试。", "已转人工，客服已经接管。", "I have issued your refund.", "退款成功。", "问题已经修好了。", "Refund processed successfully.", "The issue is fixed."} {
		t.Run(reply, func(t *testing.T) {
			h := newHarness(t)
			h.enable()
			h.provider.fn = func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
				return support.ProviderResult{Content: reply, Outcome: "completed"}, nil
			}
			h.step(h.message(2, 10, "@ztapi_bot 接口报错 500，怎么排查？"))
			if h.provider.calls != 1 || len(h.sent) != 1 {
				t.Fatal("serious help missing")
			}
			if h.sent[0].Text == reply {
				t.Fatalf("unsafe serious output escaped: %s", reply)
			}
			if !strings.Contains(promptText(t, h.provider.inputs[0]), "严肃模式") {
				t.Fatal("serious mode is not deterministic")
			}
		})
	}
}

func TestTroubleshootingAsksOneQuestionAndDoesNotCreateKeys(t *testing.T) {
	for _, answer := range []string{"你用什么客户端？什么系统？", "我已经为你创建了新的 API 密钥。", "已为您生成访问令牌。", "I have created an API key for you."} {
		h := newHarness(t)
		h.enable()
		h.provider.fn = func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
			return support.ProviderResult{Content: answer, Outcome: "completed"}, nil
		}
		h.step(h.message(2, 10, "@ztapi_bot 配置报错，怎么设置？"))
		if h.sent[0].Text == answer {
			t.Fatalf("unsafe setup advice: %s", answer)
		}
	}
}

func TestManualExistingKeyGuidanceDoesNotClaimAnAction(t *testing.T) {
	h := newHarness(t)
	row := inbox{Text: "没有 API Key 怎么配置 Codex？"}
	for _, answer := range []string{
		"请登录官网，在控制台自行创建 API Key，再在本机配置，不要把密钥发给机器人。",
		"我可以指导你在网站自行创建 API Key，但不会替你操作。",
	} {
		if got := h.worker.safeAnswer(row, answer); got != answer {
			t.Fatalf("manual website guidance was rejected: %q", got)
		}
	}
}

func TestModelCannotInventPersonalAccountEvidenceOrMockCustomers(t *testing.T) {
	for _, answer := range []string{"你的余额是 100 U。", "该请求实际扣费 0 U。", "Your balance is $100.", "你太蠢了，连这个都不懂。"} {
		h := newHarness(t)
		h.provider.fn = func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
			return support.ProviderResult{Content: answer, Outcome: "completed"}, nil
		}
		h.step(h.private(1, 10, "解释公开计费规则"))
		if len(h.sent) != 1 || h.sent[0].Text == answer {
			t.Fatalf("invented evidence or mockery escaped: %s", answer)
		}
	}
}

func TestPreferenceReadFailureDoesNotAdmitOrDispatchModel(t *testing.T) {
	h := newHarness(t)
	if err := h.db.Migrator().DropTable(&preference{}); err != nil {
		t.Fatal(err)
	}
	h.step(h.private(1, 10, "怎么配置？"))
	usages, err := support.ReadFinancialUsage(context.Background(), h.db, h.now.Add(-time.Minute), h.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 0 || h.provider.calls != 0 {
		t.Fatal("preparation failure was marked dispatched")
	}
}

func TestOptInPreferencesForgetAndActorTopicIsolation(t *testing.T) {
	h := newHarness(t)
	h.step(h.private(1, 10, "记住我的客户端是 Codex，系统是 Windows，模型是 deepseek-v4.1-flash"))
	if h.provider.calls != 0 || len(h.sent) != 1 {
		t.Fatal("explicit preference was not handled deterministically")
	}
	h.step(h.private(2, 10, "ACTOR_PRIVATE_MARKER，如何配置？"))
	first := promptText(t, h.provider.inputs[0])
	if !strings.Contains(first, "<preferences>") || !strings.Contains(first, "Codex") {
		t.Fatal("opt-in preferences absent")
	}
	h.step(h.private(3, 11, "怎么配置？"))
	if strings.Contains(promptText(t, h.provider.inputs[1]), "<preferences>") || strings.Contains(promptText(t, h.provider.inputs[1]), "ACTOR_PRIVATE_MARKER") {
		t.Fatal("preferences crossed actors")
	}
	h.step(h.private(4, 10, "/forget"))
	h.restart()
	h.step(h.private(5, 10, "继续"))
	after := promptText(t, h.provider.inputs[2])
	if strings.Contains(after, "<preferences>") || strings.Contains(after, "ACTOR_PRIVATE_MARKER") {
		t.Fatal("forget left old prompt context")
	}
	var receipts int64
	h.db.Table("support_calls").Count(&receipts)
	if receipts != 3 {
		t.Fatalf("forget lost receipts: %d", receipts)
	}
}

func TestPrivateHistoryNeverReachesGroupsOrOtherTopics(t *testing.T) {
	h := newHarness(t)
	h.step(h.private(1, 10, "PRIVATE_ONLY_MARKER"))
	h.step(h.message(2, h.cfg.OwnerID, "/support_on"))
	h.step(h.message(3, 10, "@ztapi_bot 你好"))
	u := h.message(4, 10, "@ztapi_bot TOPIC_ONE_MARKER")
	u.Message.MessageThreadID = 1
	h.step(u)
	u = h.message(5, 10, "@ztapi_bot 你好")
	u.Message.MessageThreadID = 2
	h.step(u)
	if len(h.provider.inputs) != 4 {
		t.Fatal("private or topic handling missing")
	}
	for _, input := range h.provider.inputs[1:] {
		if strings.Contains(promptText(t, input), "PRIVATE_ONLY_MARKER") {
			t.Fatal("private context leaked into group")
		}
	}
	if strings.Contains(promptText(t, h.provider.inputs[3]), "TOPIC_ONE_MARKER") {
		t.Fatal("context crossed topics")
	}
}

func TestMentionedNaturalPreferenceOptInStaysInActorChatTopic(t *testing.T) {
	h := newHarness(t)
	h.enable()
	u := h.message(2, 10, "@ztapi_bot 请记住我用 Codex，系统 Windows")
	u.Message.MessageThreadID = 1
	h.step(u)
	if h.provider.calls != 0 {
		t.Fatal("mentioned explicit preference invoked model")
	}
	u = h.message(3, 10, "@ztapi_bot 继续配置")
	u.Message.MessageThreadID = 1
	h.step(u)
	if !strings.Contains(promptText(t, h.provider.inputs[0]), "<preferences>") {
		t.Fatal("same topic preference missing")
	}
	u = h.message(4, 10, "@ztapi_bot 继续配置")
	u.Message.MessageThreadID = 2
	h.step(u)
	h.step(h.private(5, 10, "继续配置"))
	for _, in := range h.provider.inputs[1:] {
		if strings.Contains(promptText(t, in), "<preferences>") {
			t.Fatal("preference crossed topic or chat")
		}
	}
}

func TestRepeatedForgetCannotExhaustConversationAdmissionOrBlockOtherActors(t *testing.T) {
	h := newHarness(t)
	h.step(h.private(1, 10, "FORGET_STRESS_OLD_TEXT"))
	for id := int64(2); id <= 14; id++ {
		h.updates = []Update{h.private(id, 10, "/forget")}
		if err := h.worker.Step(context.Background()); err != nil {
			t.Fatalf("forget exhausted admission and blocked receiver: %v", err)
		}
	}
	h.step(h.private(15, 11, "继续配置"))
	h.step(h.private(16, 10, "继续配置"))
	if h.provider.calls != 3 || strings.Contains(promptText(t, h.provider.inputs[2]), "FORGET_STRESS_OLD_TEXT") {
		t.Fatal("forget stalled actors or revived old context")
	}
}
