// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeProvider struct {
	calls  int
	inputs []support.ProviderInput
	fn     func(context.Context, support.ProviderInput) (support.ProviderResult, error)
}

func (p *fakeProvider) Complete(ctx context.Context, in support.ProviderInput) (support.ProviderResult, error) {
	p.calls++
	p.inputs = append(p.inputs, in)
	if p.fn != nil {
		return p.fn(ctx, in)
	}
	input, output := int64(10), int64(2)
	return support.ProviderResult{Content: "这是测试答复。", Outcome: "completed", InputTokens: &input, OutputTokens: &output}, nil
}

type sentMessage struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
	Reply  struct {
		MessageID int `json:"message_id"`
	} `json:"reply_parameters"`
}
type harness struct {
	t              *testing.T
	db             *gorm.DB
	store          *support.Store
	client         *Client
	worker         *Worker
	provider       *fakeProvider
	cfg            Config
	now            time.Time
	updates        []Update
	sent, attempts []sentMessage
	offsets        []int64
	sendFailures   int
	retryAfter     int
	fileData       []byte
	fileError      bool
	knowledgeError bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, store := testDB(t)
	h := &harness{t: t, db: db, store: store, now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), provider: &fakeProvider{}}
	h.cfg = Config{OwnerID: 42, BotID: 123456789, BotUsername: "ztapi_bot", ChannelID: 8, Model: "deepseek-v4.1-flash", Supplier: "xcloud", VisionEnabled: true, Now: func() time.Time { return h.now }}
	client, err := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" {
			t.Fatal("unexpected network host")
		}
		if strings.HasPrefix(r.URL.Path, "/file/") {
			return testResponse(200, string(h.fileData)), nil
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var result any
		switch method {
		case "getUpdates":
			var in struct {
				Offset int64 `json:"offset"`
			}
			if common.DecodeJson(r.Body, &in) != nil {
				t.Fatal("bad poll payload")
			}
			h.offsets = append(h.offsets, in.Offset)
			result = h.updates
			if result == nil {
				result = []Update{}
			}
			h.updates = nil
		case "sendMessage":
			var in sentMessage
			if common.DecodeJson(r.Body, &in) != nil {
				t.Fatal("bad send payload")
			}
			h.attempts = append(h.attempts, in)
			if h.sendFailures > 0 {
				h.sendFailures--
				return testResponse(429, fmt.Sprintf(`{"ok":false,"error_code":429,"parameters":{"retry_after":%d}}`, h.retryAfter)), nil
			}
			h.sent = append(h.sent, in)
			result = map[string]int{"message_id": 1000 + len(h.sent)}
		case "getFile":
			if h.fileError {
				return testResponse(500, `{"ok":false}`), nil
			}
			result = map[string]any{"file_path": "photos/current.png", "file_size": len(h.fileData)}
		default:
			t.Fatalf("unexpected or mutating method %s", method)
		}
		raw, _ := common.Marshal(map[string]any{"ok": true, "result": result})
		return testResponse(200, string(raw)), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	h.client = client
	h.restart()
	return h
}
func (h *harness) restart() {
	h.t.Helper()
	w, err := NewWorker(h.cfg, h.db, h.store, h.provider, func(context.Context, string) (string, error) {
		if h.knowledgeError {
			return "", errors.New("private knowledge error " + testToken)
		}
		return "公开平台资料，不含任何账户数据。", nil
	}, h.client)
	if err != nil {
		h.t.Fatal(err)
	}
	h.worker = w
}
func (h *harness) message(id, sender int64, text string) Update {
	return Update{UpdateID: id, Message: &Message{MessageID: int(id), Date: h.now.Unix(), Chat: Chat{ID: -1001234567, Type: "supergroup"}, From: &User{ID: sender}, Text: text}}
}
func (h *harness) step(updates ...Update) {
	h.t.Helper()
	h.updates = updates
	if err := h.worker.Step(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) enable() {
	h.t.Helper()
	h.step(h.message(1, h.cfg.OwnerID, "/support_on"))
	h.sent = nil
	h.attempts = nil
}

func TestPublicRecordExamplesAreNotPrivateQueries(t *testing.T) {
	h := newHarness(t)
	h.enable()
	for i, text := range []string{
		"帮我查一下退款规则",
		"文档中的 request_id=demo 是什么意思？",
		"帮我查一下客户退款规则",
		"帮我查我的余额并解释退款规则",
	} {
		h.step(h.message(int64(i+2), 9, "@ztapi_bot "+text))
	}
	if h.provider.calls != 3 || len(h.sent) != 4 || !strings.Contains(h.sent[3].Text, "登录") {
		t.Fatalf("public guidance incorrectly refused or personal lookup allowed: calls=%d replies=%+v", h.provider.calls, h.sent)
	}
}

func TestOwnerCommandsPersistWhitelistAndOffStopsAI(t *testing.T) {
	h := newHarness(t)
	h.step(h.message(1, 9, "/support_on"))
	if len(h.sent) != 1 || h.provider.calls != 0 {
		t.Fatal("unauthorized command did not explicitly refuse")
	}
	h.step(h.message(2, h.cfg.OwnerID, "/support_on@ztapi_bot"))
	h.restart()
	h.step(h.message(3, 9, "@ztapi_bot 你好"))
	if h.provider.calls != 1 {
		t.Fatal("owner enable did not survive restart")
	}
	h.step(h.message(4, h.cfg.OwnerID, "/support_off"))
	h.step(h.message(5, 9, "@ztapi_bot 在吗"))
	if h.provider.calls != 1 || !strings.Contains(h.sent[len(h.sent)-1].Text, "关闭") {
		t.Fatal("off still called AI or left mention unanswered")
	}
}

func TestMentionsRepliesSensitiveBusyAndProviderFailureAlwaysEnqueueReply(t *testing.T) {
	h := newHarness(t)
	h.enable()
	for i, text := range []string{"@ztapi_bot 查我的余额和账单", "@ztapi_bot Bearer very-private-key", "@ztapi_bot 我的request_id=other-user"} {
		h.step(h.message(int64(2+i), 9, text))
	}
	if h.provider.calls != 0 || len(h.sent) != 3 {
		t.Fatal("sensitive request reached inference or was silent")
	}
	for _, reply := range h.sent {
		if !strings.Contains(reply.Text, "登录") || strings.Contains(reply.Text, "very-private-key") {
			t.Fatal("sensitive fallback unsafe")
		}
	}
	u := h.message(5, 10, "你好")
	u.Message.ReplyToMessage = &Message{From: &User{ID: h.cfg.BotID, IsBot: true}}
	h.step(u)
	if h.provider.calls != 1 {
		t.Fatal("reply to bot not handled")
	}
	h.provider.fn = func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
		return support.ProviderResult{Outcome: "upstream_error"}, errors.New("private channel error " + testToken)
	}
	h.step(h.message(6, 11, "@ztapi_bot 你好"))
	var groupReplies, ownerAlerts int
	for _, reply := range h.sent {
		if reply.ChatID == h.cfg.OwnerID {
			ownerAlerts++
		} else {
			groupReplies++
			if strings.Contains(reply.Text, "channel") || strings.Contains(reply.Text, testToken) {
				t.Fatal("operational failure leaked to group")
			}
		}
	}
	if groupReplies != 5 || ownerAlerts == 0 {
		t.Fatalf("failure silent or alert not private: groups=%d alerts=%d", groupReplies, ownerAlerts)
	}
	var pending int64
	h.db.Table("telegram_support_outbox").Where("state = ?", "held").Count(&pending)
	if pending != 0 {
		t.Fatal("responses left held")
	}
}

func TestSenderAndChatHistoryAreIsolatedAndOtherBotsIgnored(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.step(h.message(2, 10, "@ztapi_bot A_PRIVATE_TEXT"))
	h.step(h.message(3, 11, "@ztapi_bot B_PRIVATE_TEXT"))
	raw, _ := common.Marshal(h.provider.inputs[1])
	if strings.Contains(string(raw), "A_PRIVATE_TEXT") {
		t.Fatal("another sender's history leaked")
	}
	bot := h.message(4, 44, "@ztapi_bot bot message")
	bot.Message.From.IsBot = true
	before := len(h.sent)
	h.step(bot)
	if h.provider.calls != 2 || len(h.sent) != before {
		t.Fatal("bot loop not blocked")
	}
}

func TestCurrentPhotoAndPhotoFailureFallback(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.fileData = testImage(t, "png")
	photo := h.message(2, 10, "")
	photo.Message.Caption = "@ztapi_bot 看这张图"
	photo.Message.Photo = []PhotoSize{{FileID: "current-photo", Width: 2, Height: 2, FileSize: int64(len(h.fileData))}}
	h.step(photo)
	if h.provider.calls != 1 || h.provider.inputs[0].Image == nil {
		t.Fatal("current photo not supplied")
	}
	h.step(h.message(3, 11, "@ztapi_bot 你好"))
	if h.provider.inputs[1].Image != nil {
		t.Fatal("old image mixed into another user's message")
	}
	h.fileError = true
	photo.UpdateID = 4
	photo.Message.MessageID = 4
	h.step(photo)
	if h.provider.calls != 2 || !strings.Contains(h.sent[len(h.sent)-1].Text, "图片") {
		t.Fatal("failed photo called model or remained silent")
	}
}

func TestDuplicateUpdatesAndDispatchedCrashNeverRepeatModel(t *testing.T) {
	h := newHarness(t)
	h.enable()
	u := h.message(2, 10, "@ztapi_bot 你好")
	h.step(u)
	h.step(u)
	h.restart()
	h.step(u)
	if h.provider.calls != 1 || len(h.sent) != 1 {
		t.Fatalf("duplicate update re-executed or re-replied: model=%d sent=%d", h.provider.calls, len(h.sent))
	}
	var state cursor
	if err := h.db.Take(&state, 1).Error; err != nil {
		t.Fatal(err)
	}
	if state.Offset != 3 {
		t.Fatalf("bad durable offset: %d", state.Offset)
	}
	u = h.message(3, 11, "@ztapi_bot interrupted")
	if err := h.worker.ingest(context.Background(), []Update{u}); err != nil {
		t.Fatal(err)
	}
	conversation, err := h.store.CreateConversation(context.Background(), 11, conversationKey(u.Message), h.now)
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.store.BeginCall(context.Background(), support.BeginInput{UserID: 11, ConversationID: conversation.ID, IdempotencyKey: callKey(h.cfg.BotID, u.UpdateID), Content: u.Message.Text, ChannelID: 8, Supplier: "xcloud", Model: h.cfg.Model, Now: h.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.MarkDispatched(context.Background(), job.CallID); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Table("telegram_support_inbox").Where("update_id = ?", u.UpdateID).Updates(map[string]any{"state": "dispatched", "call_id": job.CallID}).Error; err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.step()
	if h.provider.calls != 1 || len(h.sent) != 2 || !strings.Contains(h.sent[1].Text, "中断") {
		t.Fatal("crash caused paid retry or silent loss")
	}
	usage, err := support.ReadFinancialUsage(context.Background(), h.db, h.now.Add(-time.Hour), h.now.Add(time.Hour))
	if err != nil || len(usage) != 2 {
		t.Fatalf("missing immutable receipts: %d %v", len(usage), err)
	}
	found := false
	for _, entry := range usage {
		if entry.CallID == job.CallID {
			found = true
			if entry.InputTokens != nil || entry.QuoteCostU != nil || !entry.Dispatched {
				t.Fatal("dispatched unknown became free or estimated")
			}
		}
	}
	if !found {
		t.Fatal("interrupted financial receipt missing")
	}
}

func TestOutboxRetryAfterSurvivesRestartWithoutRepeatingInference(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.sendFailures = 1
	h.retryAfter = 17
	h.step(h.message(2, 10, "@ztapi_bot 你好"))
	if h.provider.calls != 1 || len(h.sent) != 0 {
		t.Fatal("initial send did not fail as requested")
	}
	h.restart()
	h.now = h.now.Add(16 * time.Second)
	h.step()
	if len(h.sent) != 0 {
		t.Fatal("ignored retry_after")
	}
	h.now = h.now.Add(time.Second)
	h.step()
	if len(h.sent) != 1 || h.provider.calls != 1 {
		t.Fatal("durable send retry regenerated model output")
	}
}

func TestCasualParticipationIsFreshDeterministicAndCooledDown(t *testing.T) {
	h := newHarness(t)
	h.enable()
	var selected []Update
	for id := int64(100); id < 10000 && len(selected) < 3; id++ {
		u := h.message(id, 10, "普通闲聊")
		if casualSelected(h.cfg.BotID, u.UpdateID, u.Message.Chat.ID, u.Message.MessageID) {
			selected = append(selected, u)
		}
	}
	if len(selected) != 3 {
		t.Fatal("deterministic selection did not choose samples")
	}
	old := selected[0]
	old.Message.Date = h.now.Add(-time.Hour).Unix()
	h.step(old)
	if h.provider.calls != 0 {
		t.Fatal("old message triggered casual model call")
	}
	h.step(selected[1])
	h.step(selected[2])
	if h.provider.calls != 1 {
		t.Fatal("casual cooldown ignored")
	}
	h.now = h.now.Add(30 * time.Minute)
	next := selected[2]
	next.UpdateID += 10000
	next.Message.MessageID = int(next.UpdateID)
	next.Message.Date = h.now.Unix()
	for !casualSelected(h.cfg.BotID, next.UpdateID, next.Message.Chat.ID, next.Message.MessageID) {
		next.UpdateID++
		next.Message.MessageID++
	}
	h.step(next)
	if h.provider.calls != 2 {
		t.Fatal("casual did not resume after persisted cooldown")
	}
}

func TestAdmissionGuardBlocksNewInferenceButShutdownPersistsReceipt(t *testing.T) {
	h := newHarness(t)
	h.enable()
	closed := false
	h.cfg.AdmitWrite = func() error {
		if closed {
			return errors.New("disk guard private " + testToken)
		}
		return nil
	}
	h.restart()
	h.provider.fn = func(ctx context.Context, in support.ProviderInput) (support.ProviderResult, error) {
		closed = true
		n := int64(2)
		return support.ProviderResult{Content: "完成", Outcome: "completed", InputTokens: &n, OutputTokens: &n}, nil
	}
	h.step(h.message(2, 10, "@ztapi_bot 你好"))
	if h.provider.calls != 1 {
		t.Fatal("admission did not run model")
	}
	var receipts int64
	h.db.Table("support_call_receipts").Count(&receipts)
	if receipts != 1 {
		t.Fatal("disk admission guard blocked receipt")
	}
	h.updates = []Update{h.message(3, 11, "@ztapi_bot 下一条")}
	_ = h.worker.Step(context.Background())
	if h.provider.calls != 1 {
		t.Fatal("guard admitted paid work")
	}
}

func TestRunCancellationDoesNotDiscardDispatchedUsage(t *testing.T) {
	h := newHarness(t)
	h.enable()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	h.provider.fn = func(ctx context.Context, in support.ProviderInput) (support.ProviderResult, error) {
		started <- ctx
		<-release
		n := int64(5)
		return support.ProviderResult{Content: "完成", Outcome: "completed", InputTokens: &n, OutputTokens: &n}, nil
	}
	h.updates = []Update{h.message(2, 10, "@ztapi_bot 你好")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.worker.Run(ctx) }()
	var providerCtx context.Context
	select {
	case providerCtx = <-started:
	case err := <-done:
		t.Fatalf("worker stopped before dispatch: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not dispatch")
	}
	cancel()
	select {
	case <-providerCtx.Done():
		t.Fatal("parent cancellation canceled dispatched provider")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("worker failed graceful shutdown")
	}
	var receipts int64
	h.db.Table("support_call_receipts").Count(&receipts)
	if receipts != 1 {
		t.Fatal("shutdown lost usage receipt")
	}
	var pending int64
	h.db.Table("telegram_support_outbox").Where("update_id = ? AND state = ?", 2, "pending").Count(&pending)
	if pending != 1 {
		t.Fatal("shutdown lost durable response")
	}
}

func testDB(t *testing.T) (*gorm.DB, *support.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	store, err := support.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return db, store
}

func TestNewWorkerDoesNotPollSendOrInferUntilRun(t *testing.T) {
	db, store := testDB(t)
	calls := 0
	client, err := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return testResponse(200, `{"ok":true,"result":[]}`), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	worker, err := NewWorker(Config{OwnerID: 42, BotID: 123456789, BotUsername: "ztapi_bot", ChannelID: 8, Model: "deepseek-v4.1-flash", Supplier: "xcloud", Now: func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }}, db, store, p, func(context.Context, string) (string, error) { t.Fatal("constructor called knowledge"); return "", nil }, client)
	if err != nil || worker == nil || calls != 0 || p.calls != 0 {
		t.Fatalf("disabled initialization had side effects: calls=%d model=%d err=%v", calls, p.calls, err)
	}
}

func TestMissingIdentityConfigurationFailsClosed(t *testing.T) {
	for _, field := range []string{"owner", "bot", "username"} {
		t.Run(field, func(t *testing.T) {
			db, store := testDB(t)
			client, _ := NewClient(testToken, &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) { t.Fatal("constructor network request"); return nil, nil })})
			cfg := Config{OwnerID: 42, BotID: 123456789, BotUsername: "ztapi_bot", ChannelID: 8, Model: "flash", Supplier: "xcloud"}
			switch field {
			case "owner":
				cfg.OwnerID = 0
			case "bot":
				cfg.BotID = 0
			case "username":
				cfg.BotUsername = ""
			}
			if _, err := NewWorker(cfg, db, store, &fakeProvider{}, func(context.Context, string) (string, error) { return "", nil }, client); err == nil {
				t.Fatal("missing identity silently defaulted")
			}
		})
	}
}

func TestQueuedOwnerCommandAndMentionAreNotDiscardedByAge(t *testing.T) {
	h := newHarness(t)
	on := h.message(1, h.cfg.OwnerID, "/support_on")
	on.Message.Date = h.now.Add(-24 * time.Hour).Unix()
	u := h.message(2, 10, "@ztapi_bot 你好")
	u.Message.Date = on.Message.Date
	h.step(on, u)
	if h.provider.calls != 1 || len(h.sent) != 2 {
		t.Fatalf("queued explicit request was discarded: calls=%d sent=%d", h.provider.calls, len(h.sent))
	}
}

func TestRestartCompletesDispatchedReceiptEvenWhenAdmissionGuardIsClosed(t *testing.T) {
	h := newHarness(t)
	h.enable()
	u := h.message(2, 10, "@ztapi_bot interrupted")
	if err := h.worker.ingest(context.Background(), []Update{u}); err != nil {
		t.Fatal(err)
	}
	conversation, err := h.store.CreateConversation(context.Background(), 10, conversationKey(u.Message), h.now)
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.store.BeginCall(context.Background(), support.BeginInput{UserID: 10, ConversationID: conversation.ID, IdempotencyKey: callKey(h.cfg.BotID, u.UpdateID), Content: u.Message.Text, ChannelID: 8, Supplier: "xcloud", Model: h.cfg.Model, Now: h.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.MarkDispatched(context.Background(), job.CallID); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Table("telegram_support_inbox").Where("update_id = ?", u.UpdateID).Updates(map[string]any{"state": "admitted", "call_id": job.CallID}).Error; err != nil {
		t.Fatal(err)
	}
	h.cfg.AdmitWrite = func() error { return errors.New("storage admission closed") }
	h.restart()
	_ = h.worker.Step(context.Background())
	var receipts int64
	h.db.Table("support_call_receipts").Count(&receipts)
	if receipts != 1 || h.provider.calls != 0 {
		t.Fatal("storage admission blocked orphan receipt completion or repeated model")
	}
}

func TestInboxAndOffsetCommitAtomically(t *testing.T) {
	h := newHarness(t)
	h.enable()
	u := h.message(2, 10, "@ztapi_bot atomic")
	if err := h.db.Callback().Update().Before("gorm:update").Register("reject_cursor", func(tx *gorm.DB) {
		if tx.Statement.Table == "telegram_support_cursor" {
			tx.AddError(errors.New("test cursor failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	h.updates = []Update{u}
	if err := h.worker.Step(context.Background()); err == nil {
		t.Fatal("cursor failure was ignored")
	}
	var rows int64
	h.db.Table("telegram_support_inbox").Where("update_id = ?", 2).Count(&rows)
	var state cursor
	h.db.Take(&state, 1)
	if rows != 0 || state.Offset != 2 || h.provider.calls != 0 {
		t.Fatal("offset or call escaped inbox transaction rollback")
	}
	h.db.Callback().Update().Remove("reject_cursor")
	h.step(u)
	if h.provider.calls != 1 {
		t.Fatal("rolled-back update was lost")
	}
}

func TestReceiptSurvivesFailureBeforeResponseOutboxCommit(t *testing.T) {
	h := newHarness(t)
	h.enable()
	failed := false
	if err := h.db.Callback().Create().Before("gorm:create").Register("reject_final_outbox", func(tx *gorm.DB) {
		if tx.Statement.Table != "telegram_support_outbox" || failed {
			return
		}
		if entry, ok := tx.Statement.Dest.(*outbox); ok && entry.State == "pending" && entry.Kind == "answer" {
			failed = true
			tx.AddError(errors.New("test outbox failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	h.updates = []Update{h.message(2, 10, "@ztapi_bot once")}
	if err := h.worker.Step(context.Background()); err == nil {
		t.Fatal("outbox write failure ignored")
	}
	var count int64
	h.db.Table("support_call_receipts").Count(&count)
	if count != 1 {
		t.Fatal("receipt was not durable before outbox")
	}
	h.db.Callback().Create().Remove("reject_final_outbox")
	h.restart()
	h.step()
	if h.provider.calls != 1 || len(h.sent) != 1 {
		t.Fatal("receipt recovery regenerated model or lost response")
	}
}

func TestShortWindowRateLimitIsExplicitAndHasNoDailyCap(t *testing.T) {
	h := newHarness(t)
	h.enable()
	for id := int64(2); id <= 8; id++ {
		h.step(h.message(id, 10, "@ztapi_bot 闲聊"))
	}
	if h.provider.calls != 6 || len(h.sent) != 7 || !strings.Contains(h.sent[6].Text, "暂时") {
		t.Fatalf("rate limit silent or missing: calls=%d replies=%d", h.provider.calls, len(h.sent))
	}
	h.now = h.now.Add(61 * time.Second)
	h.step(h.message(9, 10, "@ztapi_bot 继续"))
	if h.provider.calls != 7 {
		t.Fatal("short-window limit became a day cap")
	}
}

func TestConversationStorageCapDoesNotPermanentlyBlockSender(t *testing.T) {
	h := newHarness(t)
	h.enable()
	u := h.message(2, 10, "@ztapi_bot 新消息")
	legacyKey := fmt.Sprintf("tg-conversation:%d:%d:%d", u.Message.Chat.ID, u.Message.From.ID, u.Message.MessageThreadID)
	conversation, err := h.store.CreateConversation(context.Background(), 10, legacyKey, h.now)
	if err != nil {
		t.Fatal(err)
	}
	messages := make([]support.Message, 490)
	for i := range messages {
		messages[i] = support.Message{ConversationID: conversation.ID, Role: "user", Content: "旧消息", CreatedAt: h.now}
	}
	if err := h.db.Create(&messages).Error; err != nil {
		t.Fatal(err)
	}
	h.step(u)
	if h.provider.calls != 1 {
		t.Fatal("old conversation length imposed a permanent sender limit")
	}
}

func TestStableActorHistorySupportsFollowupWithoutCrossSenderChatOrTopic(t *testing.T) {
	h := newHarness(t)
	h.enable()
	first := h.message(2, 10, "@ztapi_bot actor-A-original")
	first.Message.MessageThreadID = 1
	h.step(first)
	followup := h.message(3, 10, "@ztapi_bot 请接着解释")
	followup.Message.MessageThreadID = 1
	h.step(followup)
	raw, _ := common.Marshal(h.provider.inputs[1])
	if !strings.Contains(string(raw), "actor-A-original") || !strings.Contains(string(raw), "这是测试答复") {
		t.Fatal("same actor lost its prior question/answer")
	}
	other := h.message(4, 11, "@ztapi_bot other-sender")
	other.Message.MessageThreadID = 1
	h.step(other)
	other = h.message(5, 10, "@ztapi_bot other-topic")
	other.Message.MessageThreadID = 2
	h.step(other)
	on := h.message(6, h.cfg.OwnerID, "/support_on")
	on.Message.Chat.ID = -1007654321
	h.step(on)
	other = h.message(7, 10, "@ztapi_bot other-chat")
	other.Message.Chat.ID = on.Message.Chat.ID
	other.Message.MessageThreadID = 1
	h.step(other)
	for _, input := range h.provider.inputs[2:] {
		raw, _ := common.Marshal(input)
		if strings.Contains(string(raw), "actor-A-original") {
			t.Fatal("actor context crossed sender/chat/topic boundary")
		}
	}
}

func TestHistoryIsOnlyLastNineSanitizedTextMessages(t *testing.T) {
	h := newHarness(t)
	h.enable()
	for i := 0; i < 8; i++ {
		h.now = h.now.Add(61 * time.Second)
		h.step(h.message(int64(2+i), 10, fmt.Sprintf("@ztapi_bot turn-%d", i)))
	}
	input := h.provider.inputs[7]
	if len(input.Messages) != 11 {
		t.Fatalf("want system + 9 history + current, got %d", len(input.Messages))
	}
	raw, _ := common.Marshal(input)
	if strings.Contains(string(raw), "turn-0") || !strings.Contains(string(raw), "turn-6") || strings.Count(string(raw), "turn-7") != 1 {
		t.Fatal("history window incorrect or current message duplicated")
	}
	for _, message := range input.Messages {
		if _, ok := message.Content.(string); !ok {
			t.Fatal("historical image or structured content entered history")
		}
	}
}

func TestPublicBusinessQuestionsAndNormalBillingAnswersAreAllowed(t *testing.T) {
	h := newHarness(t)
	h.enable()
	answer := "Token 按公开计费规则结算。余额不足时请充值，账单和 request_id 在站内使用日志查看；退款规则以公开说明和人工核实为准。"
	h.provider.fn = func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
		return support.ProviderResult{Content: answer, Outcome: "completed"}, nil
	}
	for i, text := range []string{"Token 怎么计费？", "怎么充值？", "在哪看账单？", "request_id 在哪找？", "余额不足怎么处理？", "请解释退款规则"} {
		h.step(h.message(int64(2+i), 10, "@ztapi_bot "+text))
	}
	if h.provider.calls != 6 || len(h.sent) != 6 {
		t.Fatalf("public business nouns blocked: calls=%d replies=%d", h.provider.calls, len(h.sent))
	}
	for _, reply := range h.sent {
		if reply.Text != answer {
			t.Fatalf("normal billing answer became refusal: %s", reply.Text)
		}
	}
}

func TestSpecificAccountQueriesAndFakeBackendClaimsAreRefused(t *testing.T) {
	h := newHarness(t)
	h.enable()
	for i, text := range []string{"查我的余额", "请查询用户123的账单", "我的余额是多少", "帮我核对 request_id=private-record", "帮我修改余额"} {
		h.step(h.message(int64(2+i), 10, "@ztapi_bot "+text))
	}
	if h.provider.calls != 0 || len(h.sent) != 5 {
		t.Fatal("specific records or mutation request reached AI or was silent")
	}
	h.provider.fn = func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
		return support.ProviderResult{Content: "我已为您退款，并修改您的余额。", Outcome: "completed"}, nil
	}
	h.step(h.message(7, 10, "@ztapi_bot 你好"))
	last := h.sent[len(h.sent)-1].Text
	if strings.Contains(last, "我已为您退款") || !strings.Contains(last, "登录") {
		t.Fatal("false backend operation claim escaped")
	}
}

func TestFollowingSameActorNeverReusesOldImage(t *testing.T) {
	h := newHarness(t)
	h.enable()
	h.fileData = testImage(t, "png")
	photo := h.message(2, 10, "")
	photo.Message.Caption = "@ztapi_bot current-image-caption"
	photo.Message.Photo = []PhotoSize{{FileID: "current-photo", Width: 2, Height: 2}}
	h.step(photo)
	h.step(h.message(3, 10, "@ztapi_bot 请继续说明"))
	if h.provider.inputs[1].Image != nil {
		t.Fatal("old image was reused for followup")
	}
	raw, _ := common.Marshal(h.provider.inputs[1])
	if !strings.Contains(string(raw), "current-image-caption") || strings.Contains(string(raw), "data:image/") {
		t.Fatal("caption context lost or historical image bytes leaked")
	}
}

func TestShutdownDoesNotHideReceiptOrOutboxPersistenceFailure(t *testing.T) {
	for _, table := range []string{"support_call_receipts", "telegram_support_outbox"} {
		t.Run(table, func(t *testing.T) {
			h := newHarness(t)
			h.enable()
			started := make(chan struct{})
			release := make(chan struct{})
			h.provider.fn = func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
				close(started)
				<-release
				n := int64(5)
				return support.ProviderResult{Content: "完成", Outcome: "completed", InputTokens: &n, OutputTokens: &n}, nil
			}
			if err := h.db.Callback().Create().Before("gorm:create").Register("shutdown_write_failure", func(tx *gorm.DB) {
				if tx.Statement.Table != table {
					return
				}
				if entry, ok := tx.Statement.Dest.(*outbox); ok && entry.State != "pending" {
					return
				}
				tx.AddError(errors.New("test persistence failure"))
			}); err != nil {
				t.Fatal(err)
			}
			h.updates = []Update{h.message(2, 10, "@ztapi_bot shutdown")}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- h.worker.Run(ctx) }()
			select {
			case <-started:
			case err := <-done:
				t.Fatalf("stopped before model: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("model not started")
			}
			cancel()
			close(release)
			select {
			case err := <-done:
				if err == nil || errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "persistence") {
					t.Fatalf("shutdown hid durable write failure: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("shutdown did not finish")
			}
		})
	}
}
