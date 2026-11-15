// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrRateLimited = errors.New("support short-window rate limit")

func (Conversation) TableName() string { return "support_conversations" }
func (Message) TableName() string      { return "support_messages" }
func (Ticket) TableName() string       { return "support_tickets" }

type callRecord struct {
	ID             string    `gorm:"primaryKey;size:64"`
	UserID         int       `gorm:"not null;index"`
	ConversationID string    `gorm:"size:64;not null;index"`
	UserMessageID  uint      `gorm:"not null"`
	IdempotencyKey string    `gorm:"size:64;not null;uniqueIndex"`
	RequestHash    string    `gorm:"size:64;not null"`
	ChannelID      int       `gorm:"not null"`
	Supplier       string    `gorm:"size:64;not null"`
	Model          string    `gorm:"size:128;not null"`
	Environment    string    `gorm:"size:24;not null"`
	Dispatched     bool      `gorm:"not null"`
	StartedAt      time.Time `gorm:"not null;index"`
}

func (callRecord) TableName() string { return "support_calls" }

type activeCall struct {
	ConversationID string `gorm:"primaryKey;size:64"`
	CallID         string `gorm:"size:64;not null;uniqueIndex"`
}

func (activeCall) TableName() string { return "support_active_calls" }

type callReceipt struct {
	CallID            string `gorm:"primaryKey;size:64"`
	ReplyMessageID    uint   `gorm:"not null"`
	Outcome           string `gorm:"size:64;not null"`
	InputTokens       *int64
	OutputTokens      *int64
	CacheReadTokens   *int64
	QuoteCostU        *string
	ActualCostU       *string
	UpstreamCredit    *string
	UpstreamRequestID string    `gorm:"size:128"`
	QuoteEvidence     string    `gorm:"type:text"`
	ActualEvidence    string    `gorm:"type:text"`
	CreatedAt         time.Time `gorm:"not null;index"`
}

func (callReceipt) TableName() string            { return "support_call_receipts" }
func (*callReceipt) BeforeUpdate(*gorm.DB) error { return errors.New("support receipts are immutable") }
func (*callReceipt) BeforeDelete(*gorm.DB) error { return errors.New("support receipts are immutable") }

type ticketAction struct {
	ID        string    `gorm:"primaryKey;size:64"`
	TicketID  uint      `gorm:"not null;index"`
	ActorID   int       `gorm:"not null"`
	Hash      string    `gorm:"size:64;not null"`
	Result    string    `gorm:"type:text;not null"`
	CreatedAt time.Time `gorm:"not null"`
}

func (ticketAction) TableName() string { return "support_ticket_actions" }
func (*ticketAction) BeforeUpdate(*gorm.DB) error {
	return errors.New("support ticket audit is immutable")
}
func (*ticketAction) BeforeDelete(*gorm.DB) error {
	return errors.New("support ticket audit is immutable")
}

type Store struct {
	db *gorm.DB
	mu sync.Mutex
}
type ConversationView struct {
	Conversation Conversation `json:"conversation"`
	Messages     []Message    `json:"messages"`
	Ticket       *Ticket      `json:"ticket,omitempty"`
}
type BeginInput struct {
	Attachment                                                    *ImageInput
	UserID                                                        int
	ConversationID, IdempotencyKey, Content, ImageHash, RequestID string
	HasImage                                                      bool
	Model, Supplier, Environment                                  string
	ChannelID                                                     int
	Now                                                           time.Time
}
type CallJob struct {
	CallID  string
	Message Message
	Reply   *Message
}
type SendResult struct {
	Message Message `json:"message"`
	Reply   Message `json:"reply"`
	CallID  string  `json:"call_id"`
}
type TicketInput struct {
	ConversationID string `json:"conversation_id"`
	RequestID      string `json:"request_id,omitempty"`
	Subject        string `json:"subject"`
	Content        string `json:"content"`
	Contact        string `json:"contact,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}
type TicketReply struct {
	Ticket  Ticket  `json:"ticket"`
	Message Message `json:"message"`
}

func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("support private store required")
	}
	if err := db.AutoMigrate(&Conversation{}, &Message{}, &Ticket{}, &callRecord{}, &activeCall{}, &callReceipt{}, &ticketAction{}, &attachmentRecord{}); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}
func keyFor(user int, kind, key string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d\n%s\n%s", user, kind, key)))
	return hex.EncodeToString(h[:])
}
func validKey(key string) bool {
	return len(key) >= 8 && len(key) <= 128 && key == strings.TrimSpace(key) && !strings.ContainsAny(key, "\r\n\x00")
}
func messagePayloadHash(conversation, text, imageHash, requestID string) (string, error) {
	raw, err := common.Marshal([]any{conversation, text, imageHash, requestID})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
func replayStoredCall(tx *gorm.DB, call callRecord) (SendResult, error) {
	out := SendResult{CallID: call.ID}
	if err := tx.First(&out.Message, call.UserMessageID).Error; err != nil {
		return out, err
	}
	var receipt callReceipt
	if err := tx.Where("call_id = ?", call.ID).Take(&receipt).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return out, ErrBusy
	} else if err != nil {
		return out, err
	}
	return out, tx.First(&out.Reply, receipt.ReplyMessageID).Error
}
func newID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func (s *Store) CreateConversation(ctx context.Context, user int, key string, now time.Time) (Conversation, error) {
	now = now.UTC()
	if user <= 0 || !validKey(key) {
		return Conversation{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	identity := keyFor(user, "conversation", key)
	var c Conversation
	err := s.db.WithContext(ctx).Where("idempotency_key = ?", identity).Take(&c).Error
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return c, err
	}
	var recent int64
	if err = s.db.WithContext(ctx).Model(&Conversation{}).Where("user_id = ? AND created_at >= ?", user, now.Add(-time.Minute)).Count(&recent).Error; err != nil {
		return c, err
	}
	if recent >= 10 {
		return c, ErrRateLimited
	}
	id, err := newID()
	if err != nil {
		return c, err
	}
	c = Conversation{ID: id, UserID: user, Title: "新对话", IdempotencyKey: identity, CreatedAt: now, UpdatedAt: now}
	return c, s.db.WithContext(ctx).Create(&c).Error
}
func (s *Store) Conversations(ctx context.Context, user int) ([]Conversation, error) {
	rows := []Conversation{}
	err := s.db.WithContext(ctx).Where("user_id = ?", user).Order("updated_at DESC,id DESC").Limit(30).Find(&rows).Error
	return rows, err
}
func conversationView(tx *gorm.DB, user int, id string) (ConversationView, error) {
	view := ConversationView{Messages: []Message{}}
	if err := tx.Where("id = ? AND user_id = ?", id, user).Take(&view.Conversation).Error; err != nil {
		return view, notFound(err)
	}
	if err := tx.Where("conversation_id = ?", id).Order("id DESC").Limit(500).Find(&view.Messages).Error; err != nil {
		return view, err
	}
	for l, r := 0, len(view.Messages)-1; l < r; l, r = l+1, r-1 {
		view.Messages[l], view.Messages[r] = view.Messages[r], view.Messages[l]
	}
	var ticket Ticket
	if err := tx.Where("conversation_id = ? AND user_id = ?", id, user).Order("id DESC").Take(&ticket).Error; err == nil {
		view.Ticket = &ticket
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return view, err
	}
	return view, nil
}
func (s *Store) Conversation(ctx context.Context, user int, id string) (ConversationView, error) {
	return conversationView(s.db.WithContext(ctx), user, id)
}

func (s *Store) BeginCall(ctx context.Context, in BeginInput) (CallJob, error) {
	in.Now = in.Now.UTC()
	job := CallJob{}
	if in.UserID <= 0 || !validKey(in.IdempotencyKey) || !boundedText(in.Content, 4000) || strings.TrimSpace(in.Content) == "" {
		return job, ErrConflict
	}
	text := Redact(strings.TrimSpace(in.Content))
	if in.Attachment != nil {
		in.ImageHash = imageFingerprint(in.Attachment)
	}
	hash, err := messagePayloadHash(in.ConversationID, text, in.ImageHash, in.RequestID)
	if err != nil {
		return job, err
	}
	identity := keyFor(in.UserID, "message", in.IdempotencyKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c Conversation
		if err := tx.Where("id = ? AND user_id = ?", in.ConversationID, in.UserID).Take(&c).Error; err != nil {
			return notFound(err)
		}
		var existing callRecord
		err := tx.Where("idempotency_key = ?", identity).Take(&existing).Error
		if err == nil {
			if existing.RequestHash != hash {
				return ErrConflict
			}
			job.CallID = existing.ID
			if err = tx.First(&job.Message, existing.UserMessageID).Error; err != nil {
				return err
			}
			var receipt callReceipt
			if err = tx.Where("call_id = ?", existing.ID).Take(&receipt).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBusy
			} else if err != nil {
				return err
			}
			var reply Message
			if err = tx.First(&reply, receipt.ReplyMessageID).Error; err != nil {
				return err
			}
			job.Reply = &reply
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var humanReplay ticketAction
		if replayErr := tx.Where("id = ?", identity).Take(&humanReplay).Error; replayErr == nil {
			if humanReplay.Hash != hash {
				return ErrConflict
			}
			var previous SendResult
			if e := common.UnmarshalJsonStr(humanReplay.Result, &previous); e != nil {
				return e
			}
			job = CallJob{CallID: previous.CallID, Message: previous.Message, Reply: &previous.Reply}
			return nil
		} else if !errors.Is(replayErr, gorm.ErrRecordNotFound) {
			return replayErr
		}
		var humanCount int64
		if err = tx.Model(&Ticket{}).Where("conversation_id = ? AND status IN ?", c.ID, []string{"open", "in_progress"}).Count(&humanCount).Error; err != nil {
			return err
		}
		if humanCount > 0 {
			return ErrHumanHandoff
		}
		var active int64
		if err = tx.Model(&activeCall{}).Where("conversation_id = ?", c.ID).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return ErrBusy
		}
		var count int64
		if err = tx.Model(&callRecord{}).Where("started_at >= ? AND user_id = ?", in.Now.Add(-time.Minute), in.UserID).Count(&count).Error; err != nil {
			return err
		}
		if count >= 6 {
			return ErrRateLimited
		}
		if err = tx.Model(&callRecord{}).Where("started_at >= ?", in.Now.Add(-time.Minute)).Count(&count).Error; err != nil {
			return err
		}
		if count >= 60 {
			return ErrRateLimited
		}
		if err = tx.Model(&Message{}).Where("conversation_id = ?", c.ID).Count(&count).Error; err != nil {
			return err
		}
		if count >= 490 {
			return errors.New("support conversation length limit")
		}
		id, err := newID()
		if err != nil {
			return err
		}
		message := Message{ConversationID: c.ID, Role: "user", Content: text, HasImage: in.HasImage, CreatedAt: in.Now}
		if err = tx.Create(&message).Error; err != nil {
			return err
		}
		if err = persistAttachment(tx, in.UserID, &message, in.Attachment, in.Now); err != nil {
			return err
		}
		environment := in.Environment
		if environment == "" {
			environment = "production"
		}
		call := callRecord{ID: id, UserID: in.UserID, ConversationID: c.ID, UserMessageID: message.ID, IdempotencyKey: identity, RequestHash: hash, ChannelID: in.ChannelID, Supplier: in.Supplier, Model: in.Model, Environment: environment, StartedAt: in.Now}
		if err = tx.Create(&call).Error; err != nil {
			return err
		}
		if err = tx.Create(&activeCall{ConversationID: c.ID, CallID: id}).Error; err != nil {
			return err
		}
		updates := map[string]any{"updated_at": in.Now}
		if c.Title == "新对话" {
			updates["title"] = shortText(text, 36)
		}
		if err = tx.Model(&Conversation{}).Where("id = ? AND user_id = ?", c.ID, in.UserID).Updates(updates).Error; err != nil {
			return err
		}
		job = CallJob{CallID: id, Message: message}
		return nil
	})
	return job, err
}
func (s *Store) MarkDispatched(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var call callRecord
		if err := tx.Where("id = ?", id).Take(&call).Error; err != nil {
			return notFound(err)
		}
		var count int64
		if err := tx.Model(&Ticket{}).Where("conversation_id = ? AND status IN ?", call.ConversationID, []string{"open", "in_progress"}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrHumanHandoff
		}
		r := tx.Model(&callRecord{}).Where("id = ? AND dispatched = ?", id, false).Update("dispatched", true)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
}

func (s *Store) FinishCall(ctx context.Context, id string, result ProviderResult, now time.Time) (SendResult, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := SendResult{CallID: id}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var call callRecord
		if err := tx.Where("id = ?", id).Take(&call).Error; err != nil {
			return notFound(err)
		}
		if err := tx.First(&out.Message, call.UserMessageID).Error; err != nil {
			return err
		}
		var previous callReceipt
		if err := tx.Where("call_id = ?", id).Take(&previous).Error; err == nil {
			return tx.First(&out.Reply, previous.ReplyMessageID).Error
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if !call.Dispatched {
			zero := "0"
			result = ProviderResult{Outcome: "not_dispatched", Content: "本次未发起上游调用，不产生模型消耗。您可以继续留言或提交人工工单。", QuoteCostU: &zero, QuoteEvidence: "local_dispatch_log:not_dispatched"}
		}
		text := Redact(strings.TrimSpace(result.Content))
		role := "assistant"
		if result.Outcome != "completed" || text == "" {
			role = "system"
			if text == "" {
				text = "智能客服暂时无法完成答复，费用证据已保留，您可以提交人工工单。"
			}
		}
		for _, claim := range []string{"已为您退款", "已经退款到账", "已修改您的余额", "已为您切换渠道"} {
			if strings.Contains(text, claim) {
				text = "我没有退款、修改余额或切换渠道的权限。请提交人工工单，由管理员核查。"
				role = "system"
				break
			}
		}
		var humanCount int64
		if err := tx.Model(&Ticket{}).Where("conversation_id = ? AND status IN ?", call.ConversationID, []string{"open", "in_progress"}).Count(&humanCount).Error; err != nil {
			return err
		}
		if humanCount > 0 {
			role = "system"
			text = "当前会话已转人工，管理员会在这里回复。本次智能客服请求的费用证据已保留。"
		}
		out.Reply = Message{ConversationID: call.ConversationID, Role: role, Content: shortText(text, 12000), CreatedAt: now}
		if err := tx.Create(&out.Reply).Error; err != nil {
			return err
		}
		receipt := callReceipt{CallID: id, ReplyMessageID: out.Reply.ID, Outcome: result.Outcome, InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, CacheReadTokens: result.CacheReadTokens, QuoteCostU: result.QuoteCostU, UpstreamCredit: result.UpstreamCredit, UpstreamRequestID: result.UpstreamRequestID, QuoteEvidence: result.QuoteEvidence, CreatedAt: now}
		if err := tx.Create(&receipt).Error; err != nil {
			return err
		}
		if err := tx.Where("call_id = ?", id).Delete(&activeCall{}).Error; err != nil {
			return err
		}
		return tx.Model(&Conversation{}).Where("id = ?", call.ConversationID).Update("updated_at", now).Error
	})
	return out, err
}
func (s *Store) InterruptOrphans(ctx context.Context, now time.Time) error {
	var active []activeCall
	if err := s.db.WithContext(ctx).Find(&active).Error; err != nil {
		return err
	}
	for _, a := range active {
		if _, err := s.FinishCall(ctx, a.CallID, ProviderResult{Outcome: "interrupted", Content: "上一次客服请求中断，费用状态尚未确认。系统没有自动重试，您可以提交人工工单。"}, now); err != nil {
			return err
		}
	}
	return nil
}

// ReadFinancialUsage never migrates or changes the support store. A left join
// deliberately preserves admitted calls without a receipt for reconciliation.
func ReadFinancialUsage(ctx context.Context, db *gorm.DB, start, end time.Time) ([]FinancialUsage, error) {
	rows := []FinancialUsage{}
	err := db.WithContext(ctx).Table("support_calls c").
		Select("c.id AS call_id,c.started_at,r.created_at AS completed_at,c.channel_id,c.supplier,c.model,c.environment,c.dispatched,r.outcome,r.input_tokens,r.output_tokens,r.cache_read_tokens,r.quote_cost_u,r.actual_cost_u,r.upstream_credit,r.upstream_request_id,r.quote_evidence,r.actual_evidence").
		Joins("LEFT JOIN support_call_receipts r ON r.call_id=c.id").
		Where("COALESCE(r.created_at,c.started_at) >= ? AND COALESCE(r.created_at,c.started_at) < ?", start.UTC(), end.UTC()).
		Order("c.started_at,c.id").Limit(100001).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > 100000 {
		return nil, errors.New("support finance window exceeds safe limit")
	}
	for i := range rows {
		rows[i].Purpose = "customer_service"
		rows[i].InputTokenScope = "inclusive"
		if rows[i].Outcome == "" {
			rows[i].Outcome = "unconfirmed"
		}
	}
	return rows, nil
}

func (s *Store) CreateTicket(ctx context.Context, user int, in TicketInput, now time.Time) (Ticket, error) {
	now = now.UTC()
	out := Ticket{}
	if user <= 0 || !validKey(in.IdempotencyKey) || strings.TrimSpace(in.Subject) == "" || !boundedText(in.Subject, 160) || !boundedText(in.Content, 4000) || !boundedText(in.Contact, 240) || len(in.RequestID) > 128 {
		return out, ErrConflict
	}
	identity := keyFor(user, "ticket", in.IdempotencyKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conv Conversation
		if err := tx.Where("id = ? AND user_id = ?", in.ConversationID, user).Take(&conv).Error; err != nil {
			return notFound(err)
		}
		if err := tx.Where("idempotency_key = ?", identity).Take(&out).Error; err == nil {
			if out.ConversationID != in.ConversationID || out.Subject != Redact(in.Subject) || out.Content != Redact(in.Content) || out.Contact != Redact(in.Contact) || out.RequestID != in.RequestID {
				return ErrConflict
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var recent int64
		if err := tx.Model(&Ticket{}).Where("conversation_id = ? AND status IN ?", in.ConversationID, []string{"open", "in_progress"}).Count(&recent).Error; err != nil {
			return err
		}
		if recent > 0 {
			return ErrConflict
		}
		if err := tx.Model(&Ticket{}).Where("user_id = ? AND created_at >= ?", user, now.Add(-time.Minute)).Count(&recent).Error; err != nil {
			return err
		}
		if recent >= 5 {
			return ErrRateLimited
		}
		out = Ticket{UserID: user, ConversationID: in.ConversationID, Subject: Redact(in.Subject), Content: Redact(in.Content), Contact: Redact(in.Contact), RequestID: in.RequestID, Status: "open", Version: 1, IdempotencyKey: identity, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		message := Message{ConversationID: conv.ID, Role: "system", Content: fmt.Sprintf("人工工单 #%d 已提交，管理员会在此对话中回复。当前未执行退款、余额调整或渠道变更。", out.ID), CreatedAt: now}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		return tx.Model(&Conversation{}).Where("id = ?", conv.ID).Update("updated_at", now).Error
	})
	return out, err
}
func (s *Store) Tickets(ctx context.Context, status string, after uint, limit int) ([]Ticket, uint, error) {
	rows := []Ticket{}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := s.db.WithContext(ctx).Where("id > ?", after)
	if status != "all" {
		q = q.Where("status = ?", status)
	}
	if err := q.Order("id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	var next uint
	if len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	return rows, next, nil
}
func (s *Store) Ticket(ctx context.Context, id uint) (Ticket, error) {
	var ticket Ticket
	err := s.db.WithContext(ctx).First(&ticket, id).Error
	return ticket, notFound(err)
}

func (s *Store) ReplyTicket(ctx context.Context, actor int, id uint, text, key string, now time.Time) (TicketReply, error) {
	now = now.UTC()
	out := TicketReply{}
	if actor <= 0 || !validKey(key) || strings.TrimSpace(text) == "" || !boundedText(text, 4000) {
		return out, ErrConflict
	}
	identity := keyFor(actor, "ticket_reply", key)
	text = Redact(text)
	hash := keyFor(actor, "reply_payload", fmt.Sprintf("%d\n%s", id, text))
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous ticketAction
		if err := tx.Where("id = ?", identity).Take(&previous).Error; err == nil {
			if previous.Hash != hash || previous.TicketID != id {
				return ErrConflict
			}
			return common.UnmarshalJsonStr(previous.Result, &out)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.First(&out.Ticket, id).Error; err != nil {
			return notFound(err)
		}
		out.Message = Message{ConversationID: out.Ticket.ConversationID, Role: "human", Content: text, CreatedAt: now}
		if err := tx.Create(&out.Message).Error; err != nil {
			return err
		}
		out.Ticket.Version++
		out.Ticket.UpdatedAt = now
		if out.Ticket.Status == "open" {
			out.Ticket.Status = "in_progress"
		}
		if err := tx.Model(&Ticket{}).Where("id = ?", id).Updates(map[string]any{"version": out.Ticket.Version, "status": out.Ticket.Status, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&Conversation{}).Where("id = ?", out.Ticket.ConversationID).Update("updated_at", now).Error; err != nil {
			return err
		}
		raw, err := common.Marshal(out)
		if err != nil {
			return err
		}
		return tx.Create(&ticketAction{ID: identity, TicketID: id, ActorID: actor, Hash: hash, Result: string(raw), CreatedAt: now}).Error
	})
	return out, err
}
func (s *Store) ChangeTicketStatus(ctx context.Context, actor int, id uint, status string, version uint64, key string, now time.Time) (Ticket, error) {
	now = now.UTC()
	out := Ticket{}
	if actor <= 0 || !validKey(key) || (status != "open" && status != "in_progress" && status != "resolved") {
		return out, ErrConflict
	}
	identity := keyFor(actor, "ticket_status", key)
	hash := keyFor(actor, "status_payload", fmt.Sprintf("%d\n%s\n%d", id, status, version))
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous ticketAction
		if err := tx.Where("id = ?", identity).Take(&previous).Error; err == nil {
			if previous.Hash != hash || previous.TicketID != id {
				return ErrConflict
			}
			return common.UnmarshalJsonStr(previous.Result, &out)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.First(&out, id).Error; err != nil {
			return notFound(err)
		}
		if out.Version != version {
			return ErrConflict
		}
		out.Version++
		out.Status = status
		out.UpdatedAt = now
		r := tx.Model(&Ticket{}).Where("id = ? AND version = ?", id, version).Updates(map[string]any{"status": status, "version": out.Version, "updated_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		raw, err := common.Marshal(out)
		if err != nil {
			return err
		}
		return tx.Create(&ticketAction{ID: identity, TicketID: id, ActorID: actor, Hash: hash, Result: string(raw), CreatedAt: now}).Error
	})
	return out, err
}
