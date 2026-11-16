// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrImageStorageFull = errors.New("support private image storage is full")

type attachmentRecord struct {
	ID             uint      `gorm:"primaryKey"`
	UserID         int       `gorm:"not null;index"`
	ConversationID string    `gorm:"size:64;not null;index"`
	MessageID      uint      `gorm:"not null;uniqueIndex"`
	MIMEType       string    `gorm:"size:32;not null"`
	Data           []byte    `gorm:"not null"`
	Size           int64     `gorm:"not null"`
	CreatedAt      time.Time `gorm:"not null"`
}

func (attachmentRecord) TableName() string { return "support_attachments" }
func imageFingerprint(image *ImageInput) string {
	if image == nil {
		return ""
	}
	digest := sha256.Sum256([]byte(image.MIMEType + "\n" + image.DataBase64))
	return hex.EncodeToString(digest[:])
}

func persistAttachment(tx *gorm.DB, user int, message *Message, input *ImageInput, now time.Time) error {
	if input == nil {
		return nil
	}
	if ValidateImage(input) != nil {
		return errors.New("invalid private screenshot")
	}
	raw, err := base64.StdEncoding.DecodeString(input.DataBase64)
	if err != nil {
		return err
	}
	var userSize, totalSize int64
	if err = tx.Model(&attachmentRecord{}).Where("user_id = ?", user).Select("COALESCE(SUM(size),0)").Scan(&userSize).Error; err != nil {
		return err
	}
	if err = tx.Model(&attachmentRecord{}).Select("COALESCE(SUM(size),0)").Scan(&totalSize).Error; err != nil {
		return err
	}
	if userSize+int64(len(raw)) > 50<<20 || totalSize+int64(len(raw)) > 512<<20 {
		return ErrImageStorageFull
	}
	a := attachmentRecord{UserID: user, ConversationID: message.ConversationID, MessageID: message.ID, MIMEType: input.MIMEType, Data: raw, Size: int64(len(raw)), CreatedAt: now.UTC()}
	if err = tx.Create(&a).Error; err != nil {
		return err
	}
	message.AttachmentID = &a.ID
	message.HasImage = true
	return tx.Model(&Message{}).Where("id = ?", message.ID).Updates(map[string]any{"attachment_id": a.ID, "has_image": true}).Error
}
func (s *Store) Attachment(ctx context.Context, user int, id uint) (ImageInput, error) {
	var row attachmentRecord
	err := s.db.WithContext(ctx).Select("mime_type,data").Where("id = ? AND user_id = ?", id, user).Take(&row).Error
	return ImageInput{MIMEType: row.MIMEType, DataBase64: base64.StdEncoding.EncodeToString(row.Data)}, notFound(err)
}
func (s *Store) StaffAttachment(ctx context.Context, id uint) (ImageInput, error) {
	var row attachmentRecord
	err := s.db.WithContext(ctx).Select("mime_type,data").Where("id = ?", id).Take(&row).Error
	return ImageInput{MIMEType: row.MIMEType, DataBase64: base64.StdEncoding.EncodeToString(row.Data)}, notFound(err)
}

func (s *Store) AppendHumanMessage(ctx context.Context, user int, conversationID, text string, image *ImageInput, key string, now time.Time, requestIDs ...string) (SendResult, error) {
	out := SendResult{}
	now = now.UTC()
	if user <= 0 || !validKey(key) || !boundedText(text, 4000) || strings.TrimSpace(text) == "" || image != nil && ValidateImage(image) != nil {
		return out, ErrConflict
	}
	text = Redact(strings.TrimSpace(text))
	requestID := ""
	if len(requestIDs) > 0 {
		requestID = requestIDs[0]
	}
	identity := keyFor(user, "message", key)
	hash, hashErr := messagePayloadHash(conversationID, text, imageFingerprint(image), requestID)
	if hashErr != nil {
		return out, hashErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conv Conversation
		if err := tx.Where("id = ? AND user_id = ?", conversationID, user).Take(&conv).Error; err != nil {
			return notFound(err)
		}
		var previousCall callRecord
		if e := tx.Where("idempotency_key = ?", identity).Take(&previousCall).Error; e == nil {
			if previousCall.RequestHash != hash {
				return ErrConflict
			}
			var replayErr error
			out, replayErr = replayStoredCall(tx, previousCall)
			return replayErr
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var previous ticketAction
		if err := tx.Where("id = ?", identity).Take(&previous).Error; err == nil {
			if previous.Hash != hash {
				return ErrConflict
			}
			return common.UnmarshalJsonStr(previous.Result, &out)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var ticket Ticket
		if err := tx.Where("conversation_id = ? AND user_id = ? AND status IN ?", conversationID, user, []string{"open", "in_progress"}).Order("id DESC").Take(&ticket).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return err
		}
		var count int64
		if err := tx.Model(&Message{}).Where("conversation_id = ? AND role = ? AND created_at >= ?", conversationID, "user", now.Add(-time.Minute)).Count(&count).Error; err != nil {
			return err
		}
		if count >= 10 {
			return ErrRateLimited
		}
		out.Message = Message{ConversationID: conversationID, Role: "user", Content: text, CreatedAt: now}
		if err := tx.Create(&out.Message).Error; err != nil {
			return err
		}
		if err := persistAttachment(tx, user, &out.Message, image, now); err != nil {
			return err
		}
		out.Reply = Message{ConversationID: conversationID, Role: "system", Content: fmt.Sprintf("补充信息已交给人工工单 #%d，不会再次发起智能客服调用。", ticket.ID), CreatedAt: now}
		if err := tx.Create(&out.Reply).Error; err != nil {
			return err
		}
		if err := tx.Model(&Conversation{}).Where("id = ?", conversationID).Update("updated_at", now).Error; err != nil {
			return err
		}
		raw, err := common.Marshal(out)
		if err != nil {
			return err
		}
		return tx.Create(&ticketAction{ID: identity, ActorID: user, TicketID: ticket.ID, Hash: hash, Result: string(raw), CreatedAt: now}).Error
	})
	return out, err
}
