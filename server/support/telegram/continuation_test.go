// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func nonCasualMessage(h *harness, id, sender int64, text string) Update {
	for casualSelected(h.cfg.BotID, id, -1001234567, int(id)) {
		id++
	}
	return h.message(id, sender, text)
}

func TestCustomerSupplementWithoutMentionKeepsConversation(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.step(h.message(2, 9, "@ztapi_bot 首先说明我使用 Linux Codex"))
	h.step(nonCasualMessage(h, 3, 9, "接着补充：报错是找不到环境变量"))
	if h.provider.calls != 2 {
		t.Fatal("customer supplement without another mention was ignored")
	}
	var history string
	for _, m := range h.provider.inputs[1].Messages {
		history += fmt.Sprint(m.Content) + "\n"
	}
	if !strings.Contains(history, "首先说明我使用 Linux Codex") || !strings.Contains(history, "接着补充") {
		t.Fatal("supplement lost the customer's previous context")
	}
}

func TestQueuedCustomerSupplementsAreOneModelRequest(t *testing.T) {
	h := newHarness(t)
	h.enable()
	a := nonCasualMessage(h, 3, 9, "系统是 Linux")
	b := nonCasualMessage(h, a.UpdateID+1, 9, "报错是 Missing environment variable")
	h.step(h.message(2, 9, "@ztapi_bot 帮我看看 Codex 接入"), a, b)
	if h.provider.calls != 1 {
		t.Fatalf("expected one joined request, got %d", h.provider.calls)
	}
	content := fmt.Sprint(h.provider.inputs[0].Messages[len(h.provider.inputs[0].Messages)-1].Content)
	if !strings.Contains(content, "系统是 Linux") || !strings.Contains(content, "Missing environment variable") {
		t.Fatal("queued supplemental messages were not included in the model input")
	}
	for _, table := range []string{"support_calls", "support_call_receipts"} {
		var count int64
		if err := h.db.Table(table).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("batch did not produce exactly one call and receipt: %s=%d", table, count)
		}
	}
}

func TestContinuationDoesNotCrossCustomersTopicsPeopleOrExpiry(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.step(h.message(2, 9, "@ztapi_bot 我的客户端是 Codex"))
	other := nonCasualMessage(h, 10, 10, "我也在说话")
	topic := nonCasualMessage(h, 20, 9, "这是另一个话题")
	topic.Message.MessageThreadID = 33
	reply := nonCasualMessage(h, 30, 9, "这是回复另一个人")
	reply.Message.ReplyToMessage = &Message{From: &User{ID: 88}}
	mention := nonCasualMessage(h, 40, 9, "@someone_else 这不是问机器人")
	h.step(other, topic, reply, mention)
	if h.provider.calls != 1 {
		t.Fatal("continuation captured another actor or human conversation")
	}
	h.now = h.now.Add(3 * time.Minute)
	h.step(nonCasualMessage(h, 50, 9, "普通群聊不应一直唤醒客服"))
	if h.provider.calls != 1 {
		t.Fatal("expired continuation kept triggering inference")
	}
	h.step(h.message(60, 9, "@ztapi_bot 继续刚才的 Codex 问题"))
	if h.provider.calls != 2 {
		t.Fatal("explicit mention stopped working after window expiry")
	}
}

func TestJoinedSupplementRestartAndReplayDoNotRepeatInference(t *testing.T) {
	h := newHarness(t)
	h.enable()
	a := nonCasualMessage(h, 3, 9, "补充平台是 Linux")
	b := nonCasualMessage(h, a.UpdateID+1, 9, "补充客户端是 Codex")
	updates := []Update{h.message(2, 9, "@ztapi_bot 帮我处理接入"), a, b}
	h.step(updates...)
	h.now = h.now.Add(time.Second)
	h.restart()
	h.step(updates...)
	if h.provider.calls != 1 {
		t.Fatal("duplicate updates or restart repeated joined inference")
	}
	var rows []inbox
	if err := h.db.Where("batch_update_id = ?", 2).Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatal("durable batch membership was lost")
	}
	var held int64
	h.db.Model(&outbox{}).Where("state = ?", "held").Count(&held)
	if held != 0 {
		t.Fatal("joined children retained fallback replies")
	}
	h.step(nonCasualMessage(h, 20, 9, "我继续补充，不重复艾特"))
	if h.provider.calls != 2 {
		t.Fatal("active continuation did not survive restart")
	}
	if !strings.Contains(fmt.Sprint(h.provider.inputs[1].Messages), "补充平台是 Linux") {
		t.Fatal("joined context missing from next turn")
	}
}

func TestContinuationImageAndAdjacentTextReachOneRequest(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.fileData = testImage(t, "png")
	image := nonCasualMessage(h, 3, 9, "")
	image.Message.Photo = []PhotoSize{{FileID: "current-image", FileSize: int64(len(h.fileData)), Width: 2, Height: 2}}
	detail := nonCasualMessage(h, image.UpdateID+1, 9, "图片里显示找不到环境变量")
	h.step(h.message(2, 9, "@ztapi_bot 请结合截图看一下"), image, detail)
	if h.provider.calls != 1 || h.provider.inputs[0].Image == nil {
		t.Fatal("image and adjacent context did not share one inference")
	}
	if !strings.Contains(fmt.Sprint(h.provider.inputs[0].Messages), "图片里显示找不到环境变量") {
		t.Fatal("image inference lost text supplement")
	}
}

func TestContinuationDoesNotMergeCredentialsAccountQueriesOrCommands(t *testing.T) {
	h := newHarness(t)
	h.enable()
	secret := nonCasualMessage(h, 3, 9, "api_key=sk-zt-FAKE_CREDENTIAL_DO_NOT_FORWARD")
	h.step(h.message(2, 9, "@ztapi_bot 先问一般接入问题"), secret)
	if h.provider.calls != 1 || strings.Contains(fmt.Sprint(h.provider.inputs), "FAKE_CREDENTIAL_DO_NOT_FORWARD") {
		t.Fatal("credential supplement reached model context")
	}
	h.step(h.message(10, 9, "@ztapi_bot 继续一般接入问题"))
	h.step(nonCasualMessage(h, 11, 9, "帮我查我的余额"))
	if h.provider.calls != 2 {
		t.Fatal("personal account continuation bypassed deterministic authorization")
	}
	forget := h.message(20, 9, "/forget")
	forget.Message.ReplyToMessage = &Message{From: &User{ID: h.cfg.BotID, IsBot: true}}
	h.step(forget)
	h.step(nonCasualMessage(h, 21, 9, "忘记后不应自动接续"))
	if h.provider.calls != 2 {
		t.Fatal("forget failed to close automatic continuation")
	}
}

func TestOversizedBatchRemainsSeparateWithoutDroppingText(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.step(h.message(2, 9, "@ztapi_bot "+strings.Repeat("甲", 2000)), nonCasualMessage(h, 3, 9, strings.Repeat("乙", 2000)))
	if h.provider.calls != 2 {
		t.Fatal("oversized batch was dropped or silently truncated")
	}
	if !strings.Contains(fmt.Sprint(h.provider.inputs[0].Messages), strings.Repeat("甲", 2000)) || !strings.Contains(fmt.Sprint(h.provider.inputs[1].Messages), strings.Repeat("乙", 2000)) {
		t.Fatal("message text was truncated during batching")
	}
}

func TestPreparedAdmissionContentDoesNotChangeOnLaterSupplements(t *testing.T) {
	h := newHarness(t)
	h.enable()
	first := h.message(2, 9, "@ztapi_bot 原始问题")
	if err := h.worker.ingest(context.Background(), []Update{first}); err != nil {
		t.Fatal(err)
	}
	if err := h.worker.prepareContextBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.worker.ingest(context.Background(), []Update{nonCasualMessage(h, 3, 9, "晚到的补充")}); err != nil {
		t.Fatal(err)
	}
	if err := h.worker.prepareContextBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	var saved inbox
	if err := h.db.Where("update_id = ?", 2).Take(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Text != "@ztapi_bot 原始问题" {
		t.Fatal("prepared idempotent admission content was changed")
	}
}

func TestContextBatchPersistenceFailureRollsBackWithoutInferenceOrLostText(t *testing.T) {
	h := newHarness(t)
	h.enable()
	updates := []Update{h.message(2, 9, "@ztapi_bot 原始问题"), nonCasualMessage(h, 3, 9, "补充内容")}
	if err := h.worker.ingest(context.Background(), updates); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Callback().Update().Before("gorm:update").Register("test_context_rollback", func(tx *gorm.DB) {
		if values, ok := tx.Statement.Dest.(map[string]any); ok && values["state"] == "coalesced" {
			tx.AddError(errors.New("synthetic context write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.worker.prepareContextBatches(context.Background()); err == nil {
		t.Fatal("injected persistence failure was ignored")
	}
	h.db.Callback().Update().Remove("test_context_rollback")
	var parent inbox
	if err := h.db.Where("update_id = ?", 2).Take(&parent).Error; err != nil {
		t.Fatal(err)
	}
	if parent.Text != "@ztapi_bot 原始问题" || parent.ContextPrepared || h.provider.calls != 0 {
		t.Fatal("failed transaction mutated admission content or dispatched inference")
	}
	h.step()
	if h.provider.calls != 1 || !strings.Contains(fmt.Sprint(h.provider.inputs[0].Messages), "补充内容") {
		t.Fatal("recovery lost supplemental text")
	}
}
