// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/support"
	"github.com/QuantumNous/new-api/support/bridgeapi"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Worker struct {
	cfg                  Config
	db                   *gorm.DB
	store                *support.Store
	provider             support.Provider
	knowledge            func(context.Context, string) (string, error)
	client               *Client
	mu                   sync.Mutex
	startedAt            time.Time
	nextNotificationPoll time.Time
}

func NewWorker(cfg Config, db *gorm.DB, store *support.Store, provider support.Provider, knowledge func(context.Context, string) (string, error), client *Client) (*Worker, error) {
	if len(cfg.BindingKey) != 0 && len(cfg.BindingKey) != 32 {
		return nil, errors.New("invalid Telegram binding encryption configuration")
	}
	cfg.BindingKey = append([]byte(nil), cfg.BindingKey...)
	cfg.BotUsername = strings.TrimPrefix(cfg.BotUsername, "@")
	if db == nil || db.Dialector.Name() != "sqlite" || store == nil || provider == nil || knowledge == nil || client == nil || cfg.OwnerID <= 0 || cfg.BotID != client.botID || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`).MatchString(cfg.BotUsername) || cfg.ChannelID <= 0 || cfg.Model == "" || cfg.Supplier == "" {
		return nil, errors.New("invalid private Telegram worker configuration")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Environment == "" {
		cfg.Environment = "production"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if db.WithContext(ctx).AutoMigrate(&cursor{}, &group{}, &inbox{}, &outbox{}, &dialogue{}, &preference{}, &intake{}) != nil {
		return nil, errors.New("Telegram private database initialization failed")
	}
	var state cursor
	err := db.WithContext(ctx).Where("id = ?", 1).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state = cursor{ID: 1, BotID: cfg.BotID, OwnerID: cfg.OwnerID}
		err = db.WithContext(ctx).Create(&state).Error
	}
	if err != nil || state.BotID != cfg.BotID || state.OwnerID != cfg.OwnerID {
		return nil, errors.New("Telegram private database identity mismatch")
	}
	return &Worker{cfg: cfg, db: db, store: store, provider: provider, knowledge: knowledge, client: client, startedAt: cfg.Now().UTC()}, nil
}

const (
	privateReply     = "群聊不能核查任何人的账单、余额、退款或本人请求，也不会把 Telegram 身份当成平台账户。请到 " + bindingURL + " 登录后使用站内诊断；不要发送密钥或敏感截图。"
	busyReply        = "目前暂时无法处理这条消息，请稍后重新提问；本机器人不会自动重试模型调用。"
	interruptedReply = "上一次消息处理中断，未自动重试模型。请重新提问；已派发请求的用量状态保留待核，不会假定为零。"
	closedReply      = "本群智能客服尚未启用或已经关闭，请联系群主，或登录 https://ztapi.vip 获取帮助。"
	imageReply       = "这张图片暂时无法识别。请发送不超过 3 MiB 的 JPEG、PNG 或 WebP 原图，并遮盖密钥等敏感信息；本次未发起图片模型调用。"
)

func (w *Worker) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		err := w.Step(ctx)
		if ctx.Err() != nil {
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return ctx.Err()
		}
		delay := 250 * time.Millisecond
		if err != nil {
			delay = time.Second
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.RetryAfter > delay {
				delay = apiErr.RetryAfter
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (w *Worker) Step(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var interrupted []inbox
	if w.db.WithContext(ctx).Where("state IN ? AND call_id <> ?", []string{"queued", "admitted", "dispatched"}, "").Order("update_id ASC").Limit(20).Find(&interrupted).Error != nil {
		return errors.New("Telegram interrupted inbox unavailable")
	}
	for _, row := range interrupted {
		if err := w.process(ctx, row); err != nil {
			return err
		}
	}
	if w.cfg.AdmitWrite != nil && w.cfg.AdmitWrite() != nil {
		_ = w.alert(ctx, "storage", "Telegram 客服已暂停新任务准入，请检查私有数据库与磁盘余量。")
		_ = w.flush(ctx)
		return errors.New("Telegram admission temporarily unavailable")
	}
	var state cursor
	if w.db.WithContext(ctx).Where("id = ?", 1).Take(&state).Error != nil {
		return errors.New("Telegram cursor unavailable")
	}
	updates, err := w.client.GetUpdates(ctx, state.Offset)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = w.alert(ctx, "poll", "Telegram 客服轮询暂时不可用，请检查服务状态。")
		_ = w.flush(ctx)
		return err
	}
	if err = w.ingest(ctx, updates); err != nil {
		return err
	}
	var rows []inbox
	if w.db.WithContext(ctx).Where("state IN ?", []string{"queued", "admitted", "dispatched"}).Order("update_id ASC").Limit(20).Find(&rows).Error != nil {
		return errors.New("Telegram inbox unavailable")
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = w.process(ctx, row); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = w.flush(ctx); err != nil {
			return err
		}
	}
	if err = w.pollNotifications(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return w.flush(ctx)
}

func (w *Worker) ingest(ctx context.Context, updates []Update) error {
	if len(updates) > 100 {
		return errors.New("Telegram update batch too large")
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].UpdateID < updates[j].UpdateID })
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state cursor
		if err := tx.Where("id = ?", 1).Take(&state).Error; err != nil {
			return err
		}
		for _, update := range updates {
			if update.UpdateID < state.Offset {
				continue
			}
			if update.UpdateID < 0 || update.UpdateID == int64(^uint64(0)>>1) {
				return errors.New("invalid update")
			}
			row := w.capture(update)
			insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if insert.Error != nil {
				return insert.Error
			}
			if insert.RowsAffected > 0 && row.Command == "welcome" {
				if err := w.queueWelcome(ctx, tx, row); err != nil {
					return err
				}
			}
			if insert.RowsAffected > 0 && row.Command != "welcome" && (row.Directed || row.Command != "") {
				if err := tx.Create(&outbox{Key: replyKey(row.UpdateID), UpdateID: row.UpdateID, ChatID: row.ChatID, MessageID: row.MessageID, ThreadID: row.ThreadID, Text: interruptedReply, Kind: "reply", State: "held", NextAttemptAt: w.now(), CreatedAt: w.now()}).Error; err != nil {
					return err
				}
			}
			state.Offset = update.UpdateID + 1
		}
		return tx.Model(&cursor{}).Where("id = ?", 1).Update("offset", state.Offset).Error
	})
	if err != nil {
		return errors.New("Telegram updates were not durably admitted")
	}
	return nil
}

func (w *Worker) capture(update Update) inbox {
	if row, handled := w.captureWelcome(update); handled {
		return row
	}
	row := inbox{UpdateID: update.UpdateID, State: "done", CreatedAt: w.now()}
	m := update.Message
	if m == nil || m.From == nil || m.From.IsBot || m.From.ID <= 0 || int64(int(m.From.ID)) != m.From.ID {
		return row
	}
	row.ChatID, row.SenderID, row.MessageID, row.ThreadID, row.Date = m.Chat.ID, m.From.ID, m.MessageID, m.MessageThreadID, m.Date
	row.IsGroup = m.Chat.Type == "group" || m.Chat.Type == "supergroup"
	if !row.IsGroup && (m.Chat.Type != "private" || m.Chat.ID != m.From.ID) {
		return inbox{UpdateID: update.UpdateID, State: "done", CreatedAt: w.now()}
	}
	text := m.Text
	entities := m.Entities
	if text == "" {
		text = m.Caption
		entities = m.CaptionEntities
	}
	row.Command = w.command(text)
	if !row.IsGroup && row.Command == "start" {
		parts := strings.Fields(text)
		row.BindingAttempt = len(parts) > 1
		if len(parts) == 2 && strings.HasPrefix(parts[1], "bind_") && bindingToken.MatchString(strings.TrimPrefix(parts[1], "bind_")) {
			row.BindToken, _ = w.sealBinding(row, strings.TrimPrefix(parts[1], "bind_"))
			if regexp.MustCompile(`^[A-Za-z0-9_]{0,64}$`).MatchString(m.From.Username) {
				row.Username = m.From.Username
			}
			row.DisplayName = w.clean(strings.TrimSpace(m.From.FirstName + " " + m.From.LastName))
			if utf8.RuneCountInString(row.DisplayName) > 100 {
				row.DisplayName = ""
			}
		}
	}
	row.Directed = !row.IsGroup || w.mentioned(text, entities) || (m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.ID == w.cfg.BotID)
	row.Credential = w.hasCredentials(text)
	row.Sensitive = sensitive(text)
	row.Invalid = !utf8.ValidString(text) || utf8.RuneCountInString(text) > 4000
	row.Text = w.clean(text)
	if row.Command == "start" && len(strings.Fields(text)) > 1 {
		row.Text = "/start [binding token omitted]"
	}
	if row.Credential {
		row.Text = "[credential omitted]"
		row.BindToken = ""
	}
	if len(m.Photo) > 0 {
		for _, photo := range m.Photo {
			if row.FileID == "" || photo.Width*photo.Height > 0 {
				row.FileID, row.FileSize = photo.FileID, photo.FileSize
			}
		}
		row.MIMEType = "image/jpeg"
	} else if m.Document != nil {
		row.FileID, row.FileSize, row.MIMEType = m.Document.FileID, m.Document.FileSize, m.Document.MIMEType
	}
	if row.FileID != "" && (row.FileSize < 0 || row.FileSize > support.MaxImageBytes || (row.MIMEType != "image/jpeg" && row.MIMEType != "image/png" && row.MIMEType != "image/webp")) {
		row.Invalid = true
	}
	if strings.TrimSpace(row.Text) == "" && row.FileID != "" {
		row.Text = "请识别当前这张图片。"
	}
	row.State = "queued"
	return row
}

func (w *Worker) process(ctx context.Context, row inbox) error {
	if row.CallID != "" {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		result, err := w.store.FinishCall(cleanup, row.CallID, support.ProviderResult{Content: interruptedReply, Outcome: "interrupted"}, w.now())
		if err != nil {
			return errors.New("Telegram interrupted receipt unavailable")
		}
		text := interruptedReply
		if result.Reply.Role == "assistant" {
			text = w.safeAnswer(row, result.Reply.Content)
		}
		return w.complete(cleanup, row, text, "answer")
	}
	if row.Command == "support_on" || row.Command == "support_off" {
		if row.Credential {
			return w.complete(ctx, row, credentialReply, "notice")
		}
		text := "只有机器人配置的 owner 可以启用或关闭群客服。"
		if row.SenderID == w.cfg.OwnerID {
			if !row.IsGroup {
				text = "请在目标群中发送 /support_on 或 /support_off。"
			} else {
				enabled := row.Command == "support_on"
				entry := group{ChatID: row.ChatID, Enabled: enabled, UpdatedAt: w.now()}
				if w.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "chat_id"}}, DoUpdates: clause.Assignments(map[string]any{"enabled": enabled, "updated_at": w.now()})}).Create(&entry).Error != nil {
					return errors.New("Telegram group configuration unavailable")
				}
				text = "本群智能客服已关闭。"
				if enabled {
					text = "本群智能客服已启用。@ztapi_bot 或回复机器人即可提问；账户诊断请在站内登录。"
				}
			}
		}
		return w.complete(ctx, row, text, "control")
	}
	enabled, err := true, error(nil)
	if row.IsGroup {
		enabled, err = w.enabled(ctx, row.ChatID)
	}
	if err != nil {
		return err
	}
	if !enabled {
		if row.Directed {
			return w.complete(ctx, row, closedReply, "notice")
		}
		return w.ignore(ctx, row)
	}
	if !row.Directed {
		fresh := time.Unix(row.Date, 0)
		if strings.HasPrefix(strings.TrimSpace(row.Text), "/") || row.FileID != "" || fresh.Before(w.now().Add(-2*time.Minute)) || fresh.Before(w.startedAt.Add(-30*time.Second)) || fresh.After(w.now().Add(30*time.Second)) || !casualSelected(w.cfg.BotID, row.UpdateID, row.ChatID, row.MessageID) {
			return w.ignore(ctx, row)
		}
		claim := w.db.WithContext(ctx).Model(&group{}).Where("chat_id = ? AND enabled = ? AND (last_casual_at IS NULL OR last_casual_at <= ?)", row.ChatID, true, w.now().Add(-30*time.Minute)).Update("last_casual_at", w.now())
		if claim.Error != nil {
			return errors.New("Telegram casual admission unavailable")
		}
		if claim.RowsAffected != 1 {
			return w.ignore(ctx, row)
		}
	}
	if row.Credential {
		return w.complete(ctx, row, credentialReply, "notice")
	}
	if row.Invalid {
		return w.complete(ctx, row, "消息过长或附件格式不支持。"+imageReply, "notice")
	}
	if handled, e := w.preferences(ctx, row); handled || e != nil {
		return e
	}
	if handled, e := w.workflow(ctx, row); handled || e != nil {
		return e
	}
	if w.cfg.AdmitWrite != nil && w.cfg.AdmitWrite() != nil {
		return w.complete(ctx, row, busyReply, "notice")
	}
	prefs, prefErr := w.preferencePrompt(ctx, row)
	if prefErr != nil {
		return w.complete(ctx, row, busyReply, "notice")
	}
	knowledgeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	knowledge, knowledgeErr := w.knowledge(knowledgeCtx, row.Text)
	cancel()
	if knowledgeErr != nil || len(knowledge) > 60*1024 {
		_ = w.alert(ctx, "knowledge", "Telegram 客服公开知识源暂时不可用。")
		return w.complete(ctx, row, "公开业务资料暂时不可用，无法确认动态信息。请稍后再试或登录站内咨询。", "notice")
	}
	var image *support.ImageInput
	if row.FileID != "" {
		if !w.cfg.VisionEnabled {
			return w.complete(ctx, row, imageReply, "notice")
		}
		image, err = w.client.DownloadImage(ctx, row.FileID)
		if err != nil {
			return w.complete(ctx, row, imageReply, "notice")
		}
	}
	conversation, history, err := w.actorContext(ctx, row)
	if err != nil {
		return w.complete(ctx, row, busyReply, "notice")
	}
	imageHash := ""
	if image != nil {
		sum := sha256.Sum256([]byte(image.DataBase64))
		imageHash = hex.EncodeToString(sum[:])
	}
	job, beginErr := w.store.BeginCall(ctx, support.BeginInput{UserID: int(row.SenderID), ConversationID: conversation.ID, IdempotencyKey: callKey(w.cfg.BotID, row.UpdateID), Content: row.Text, HasImage: image != nil, ImageHash: imageHash, ChannelID: w.cfg.ChannelID, Supplier: w.cfg.Supplier, Model: w.cfg.Model, Environment: w.cfg.Environment, Now: w.now()})
	if beginErr != nil {
		if job.CallID != "" {
			row.CallID = job.CallID
			return w.process(ctx, row)
		}
		return w.complete(ctx, row, busyReply, "notice")
	}
	if job.Reply != nil {
		return w.complete(ctx, row, w.safeAnswer(row, job.Reply.Content), "answer")
	}
	row.CallID = job.CallID
	if w.db.WithContext(ctx).Model(&inbox{}).Where("update_id = ?", row.UpdateID).Updates(map[string]any{"state": "admitted", "call_id": job.CallID}).Error != nil {
		return errors.New("Telegram call admission persistence failed")
	}
	if ctx.Err() != nil || (w.cfg.AdmitWrite != nil && w.cfg.AdmitWrite() != nil) {
		return w.process(ctx, row)
	}
	if err = w.store.MarkDispatched(ctx, job.CallID); err != nil {
		return w.process(ctx, row)
	}
	// Once dispatched, parent cancellation only stops future admission. Receipt
	// and outbox persistence share a fresh five-second cleanup budget.
	callCtx, callCancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	policy := w.answerPolicy(row)
	messages := []support.PromptMessage{{Role: "system", Content: policy + "\n<public_knowledge>\n" + knowledge + "\n</public_knowledge>\n" + prefs + "\n" + policy}}
	messages = append(messages, history...)
	messages = append(messages, support.PromptMessage{Role: "user", Content: row.Text})
	result, providerErr := w.infer(callCtx, support.ProviderInput{Messages: messages, Image: image})
	callCancel()
	if providerErr != nil || result.Outcome != "completed" || strings.TrimSpace(result.Content) == "" {
		result.Content = busyReply
		if result.Outcome == "" {
			result.Outcome = "upstream_error"
		}
	}
	result.Content = w.safeAnswer(row, result.Content)
	cleanup, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cleanupCancel()
	finished, err := w.store.FinishCall(cleanup, job.CallID, result, w.now())
	if err != nil {
		return errors.New("Telegram usage receipt persistence failed")
	}
	if providerErr != nil || result.Outcome != "completed" {
		_ = w.alert(cleanup, "provider", "Telegram 客服模型服务暂时异常，请检查受保护的运行配置和上游状态。")
	}
	return w.complete(cleanup, row, w.safeAnswer(row, finished.Reply.Content), "answer")
}

func (w *Worker) infer(ctx context.Context, in support.ProviderInput) (result support.ProviderResult, err error) {
	defer func() {
		if recover() != nil {
			result = support.ProviderResult{Outcome: "provider_panic"}
			err = errors.New("Telegram inference failed")
		}
	}()
	return w.provider.Complete(ctx, in)
}

func (w *Worker) complete(ctx context.Context, row inbox, text, kind string) error {
	return w.completeWithGeneration(ctx, row, text, kind, "")
}

func (w *Worker) completeAccount(ctx context.Context, row inbox, text, generation string) error {
	return w.completeWithGeneration(ctx, row, text, "account", generation)
}

func (w *Worker) completeWithGeneration(ctx context.Context, row inbox, text, kind, generation string) error {
	entry := outbox{Key: replyKey(row.UpdateID), UpdateID: row.UpdateID, ChatID: row.ChatID, MessageID: row.MessageID, ThreadID: row.ThreadID, Text: w.clean(text), Kind: kind, BindingGeneration: generation, State: "pending", NextAttemptAt: w.now(), CreatedAt: w.now()}
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]any{"text": entry.Text, "kind": kind, "binding_generation": generation, "state": "pending", "next_attempt_at": w.now()})}).Create(&entry).Error; err != nil {
			return err
		}
		return tx.Model(&inbox{}).Where("update_id = ?", row.UpdateID).Updates(map[string]any{"state": "done", "file_id": "", "bind_token": ""}).Error
	})
	if err != nil {
		return errors.New("Telegram response persistence failed")
	}
	return nil
}

func (w *Worker) ignore(ctx context.Context, row inbox) error {
	if w.db.WithContext(ctx).Model(&inbox{}).Where("update_id = ?", row.UpdateID).Updates(map[string]any{"state": "done", "file_id": ""}).Error != nil {
		return errors.New("Telegram inbox completion failed")
	}
	return nil
}

func (w *Worker) enabled(ctx context.Context, chatID int64) (bool, error) {
	var row group
	err := w.db.WithContext(ctx).Where("chat_id = ?", chatID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("Telegram group state unavailable")
	}
	return row.Enabled, nil
}

func (w *Worker) alert(ctx context.Context, kind, text string) error {
	entry := outbox{Key: fmt.Sprintf("alert:%s:%d", kind, w.now().Unix()/1800), ChatID: w.cfg.OwnerID, Text: w.clean(text), Kind: "alert", State: "pending", NextAttemptAt: w.now(), CreatedAt: w.now()}
	return w.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error
}

func (w *Worker) flush(ctx context.Context) error {
	if err := w.ackNotifications(ctx); err != nil {
		return err
	}
	var rows []outbox
	if w.db.WithContext(ctx).Where("state = ? AND next_attempt_at <= ?", "pending", w.now()).Order("created_at ASC, key ASC").Limit(30).Find(&rows).Error != nil {
		return errors.New("Telegram outbox unavailable")
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if row.Kind == "welcome" {
			enabled, err := w.enabled(ctx, row.ChatID)
			if err != nil {
				return err
			}
			if !enabled {
				if err := w.db.WithContext(ctx).Model(&outbox{}).Where("key = ? AND state = ?", row.Key, "pending").Update("state", "suppressed").Error; err != nil {
					return errors.New("Telegram welcome suppression failed")
				}
				continue
			}
		}
		if (row.Kind == "alert" || row.Kind == "intake_owner") && row.ChatID != w.cfg.OwnerID {
			return errors.New("Telegram private alert target rejected")
		}
		text := row.Text
		if row.Kind == "notification" {
			result, err := w.bridgeCall(ctx, bridgeapi.Request{Action: "authorize_notification", TelegramID: row.ChatID, EventID: row.BridgeEventID})
			if err != nil {
				if e := w.retryOutbox(ctx, row, time.Second); e != nil {
					return e
				}
				continue
			}
			if !result.Allowed {
				if w.db.WithContext(ctx).Model(&outbox{}).Where("key = ? AND state = ?", row.Key, "pending").Update("state", "suppressed").Error != nil {
					return errors.New("Telegram notification suppression failed")
				}
				continue
			}
		}
		if row.Kind == "account" {
			result, err := w.bridgeCall(ctx, bridgeapi.Request{Action: "binding_status", TelegramID: row.ChatID})
			if err != nil || !result.Bound || row.BindingGeneration == "" || result.BindingGeneration != row.BindingGeneration {
				text = bindingReply
			}
		}
		if row.Kind == "answer" {
			enabled, err := true, error(nil)
			if row.ChatID < 0 {
				enabled, err = w.enabled(ctx, row.ChatID)
			}
			if err != nil {
				return err
			}
			if !enabled {
				text = closedReply
			}
		}
		id, err := w.client.send(ctx, row.ChatID, row.MessageID, row.ThreadID, w.clean(text))
		if err != nil {
			delay := time.Second * time.Duration(1<<min(row.Attempts, 8))
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.RetryAfter > delay {
				delay = apiErr.RetryAfter
			}
			if e := w.retryOutbox(ctx, row, delay); e != nil {
				return e
			}
			continue
		}
		if w.db.WithContext(ctx).Model(&outbox{}).Where("key = ? AND state = ?", row.Key, "pending").Updates(map[string]any{"state": "sent", "sent_message_id": id, "attempts": row.Attempts + 1}).Error != nil {
			return errors.New("Telegram delivery persistence failed")
		}
		if row.Kind == "notification" {
			if err := w.ackNotifications(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func conversationKey(message *Message) string {
	return fmt.Sprintf("tg-conversation:%d:%d:%d", message.Chat.ID, message.From.ID, message.MessageThreadID)
}
func callKey(botID, updateID int64) string { return fmt.Sprintf("tg-update:%d:%d", botID, updateID) }
func replyKey(updateID int64) string       { return fmt.Sprintf("reply:%d", updateID) }
func casualSelected(botID, updateID, chatID int64, messageID int) bool {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%d", botID, updateID, chatID, messageID)))
	return binary.BigEndian.Uint64(sum[:8])%40 == 0
}
func (w *Worker) now() time.Time { return w.cfg.Now().UTC() }

func (w *Worker) command(text string) string {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return ""
	}
	name := strings.ToLower(parts[0])
	split := strings.Split(name, "@")
	if len(split) > 2 || (len(split) == 2 && !strings.EqualFold(split[1], w.cfg.BotUsername)) {
		return ""
	}
	switch split[0] {
	case "/support_on", "/support_off", "/memory", "/forget", "/start", "/binding", "/recent", "/diagnose", "/handoff", "/case_reply", "/case_resolve":
		return strings.TrimPrefix(split[0], "/")
	}
	return ""
}
func (w *Worker) mentioned(text string, entities []Entity) bool {
	for _, entity := range entities {
		if entity.Type == "text_mention" && entity.User != nil && entity.User.ID == w.cfg.BotID {
			return true
		}
	}
	return regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])@` + regexp.QuoteMeta(w.cfg.BotUsername) + `($|[^A-Za-z0-9_])`).MatchString(text)
}
func sensitive(text string) bool {
	text = strings.ToLower(text)
	if privateOperation.MatchString(text) {
		return true
	}
	if !recordTarget.MatchString(text) {
		return false
	}
	if recordQuery.MatchString(text) && personalRecordOwner.MatchString(text) {
		return true
	}
	if publicGuide.MatchString(text) {
		return false
	}
	if strongRecordQuery.MatchString(text) || recordIdentifier.MatchString(text) {
		return true
	}
	return recordQuery.MatchString(text) && (recordOwner.MatchString(text) || strings.Contains(text, "request_id"))
}

var recordTarget = regexp.MustCompile(`余额|账单|扣款|扣费|消费|用量|退款|充值记录|充值订单|请求编号|request[_ -]?id|balance|billing|refund`)
var recordQuery = regexp.MustCompile(`查|核对|核查|展示|显示|调取|读取|导出|多少|明细|list|show|check|lookup`)
var recordOwner = regexp.MustCompile(`我的|我这|我现在|我当前|他人|别人|客户|用户[0-9 ]|user[_ -]?id|my |their |his |her `)
var personalRecordOwner = regexp.MustCompile(`我的|我这|我现在|我当前|他人|别人|(?:用户|客户)\s*[0-9]+|user[_ -]?id|my |their |his |her `)
var publicGuide = regexp.MustCompile(`怎么|如何|在哪|哪里|规则|说明|解释|计算|计费|入口|接口|文档|步骤|含义|what is|how |where `)
var strongRecordQuery = regexp.MustCompile(`帮我(?:查|核对|核查|调取|读取)|请(?:帮我)?(?:查询|展示|导出)(?:我|用户|客户|他人|别人)`)
var recordIdentifier = regexp.MustCompile(`request[_ -]?id\s*[:=]\s*[a-z0-9_-]+`)
var privateOperation = regexp.MustCompile(`(?:帮我|给我)(?:修改|调整|增加|扣除).{0,12}余额|(?:帮我|给我)(?:退款|退费)|(?:采购|供应商).{0,12}(?:成本|密钥|价格)|内部(?:渠道|成本|利润)|毛利`)
var falseOperation = regexp.MustCompile(`(?i)(?:我|我们|客服)(?:已|已经|刚刚|正在).{0,24}(?:退款|退费|修改.{0,8}余额|调整.{0,8}余额|切换.{0,8}渠道|(?:查询|查到).{0,8}(?:账单|余额|账户))|(?:已|已经)(?:为|帮|给)(?:您|你).{0,20}(?:退款|退费|修改|调整|切换|查询)|(?:已|已经)(?:修改|调整)(?:您的|你的)?余额|(?:i have|we have|i've).{0,40}(?:refunded|changed your balance|checked your account)`)
var telegramCredential = regexp.MustCompile(`\b[0-9]{5,20}:[A-Za-z0-9_-]{20,128}\b`)
var publicCredentialPlaceholder = regexp.MustCompile(`(?i)\bBearer\s+(?:YOUR_[A-Z0-9_]+|\$\{ZTAPI_API_KEY\})`)
var credentialAssignment = regexp.MustCompile(`(?i)(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|token|secret|password|密钥|秘钥|密码|令牌)\s*(?:[:=：]|是|为|\bis\b)\s*["']?([^\s"'，,。;；<>]{4,256})`)
var credentialPlaceholder = regexp.MustCompile(`(?i)^(?:YOUR_[A-Z0-9_]+|\$ZTAPI_API_KEY|\$\{ZTAPI_API_KEY\}|\[已隐藏凭据\])$`)

func (w *Worker) hasCredentials(text string) bool {
	text = publicCredentialPlaceholder.ReplaceAllString(text, "credential placeholder")
	for _, match := range credentialAssignment.FindAllStringSubmatch(text, -1) {
		if !credentialPlaceholder.MatchString(match[1]) {
			return true
		}
	}
	return support.Redact(text) != text || strings.Contains(text, w.client.token) || telegramCredential.MatchString(text)
}
func (w *Worker) clean(text string) string {
	text = regexp.MustCompile(`(?i)\bbind_[A-Za-z0-9_-]{16,128}`).ReplaceAllString(text, "[binding token omitted]")
	text = strings.ReplaceAll(text, w.client.token, "[已隐藏凭据]")
	text = telegramCredential.ReplaceAllString(text, "[已隐藏凭据]")
	text = support.Redact(text)
	text = credentialAssignment.ReplaceAllStringFunc(text, func(match string) string {
		parts := credentialAssignment.FindStringSubmatch(match)
		if len(parts) == 2 && credentialPlaceholder.MatchString(parts[1]) {
			return match
		}
		return "[已隐藏凭据]"
	})
	text = strings.ToValidUTF8(text, "")
	units := 0
	var out strings.Builder
	for _, r := range text {
		if r == 0 {
			continue
		}
		width := 1
		if r > 0xffff {
			width = 2
		}
		if units+width > 3900 {
			break
		}
		out.WriteRune(r)
		units += width
	}
	return out.String()
}
func (w *Worker) safeReply(text string) string {
	if falseOperation.MatchString(text) || w.hasCredentials(text) {
		return privateReply
	}
	if strings.TrimSpace(text) == "" {
		return busyReply
	}
	return w.clean(text)
}
