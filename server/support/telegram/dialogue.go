// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/support"
	"gorm.io/gorm"
)

func (w *Worker) actorContext(ctx context.Context, row inbox) (support.Conversation, []support.PromptMessage, error) {
	key := conversationKey(&Message{Chat: Chat{ID: row.ChatID}, From: &User{ID: row.SenderID}, MessageThreadID: row.ThreadID})
	var binding dialogue
	query := func() *gorm.DB {
		return w.db.WithContext(ctx).Where("key = ? AND chat_id = ? AND sender_id = ? AND thread_id = ?", key, row.ChatID, row.SenderID, row.ThreadID)
	}
	err := query().Take(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		conversation, e := w.store.CreateConversation(ctx, int(row.SenderID), key, w.now())
		if e != nil {
			return support.Conversation{}, nil, e
		}
		binding = dialogue{Key: key, ChatID: row.ChatID, SenderID: row.SenderID, ThreadID: row.ThreadID, ConversationID: conversation.ID}
		if err = w.db.WithContext(ctx).Create(&binding).Error; err != nil {
			return support.Conversation{}, nil, err
		}
	} else if err != nil {
		return support.Conversation{}, nil, err
	}
	view, err := w.store.Conversation(ctx, int(row.SenderID), binding.ConversationID)
	if err != nil {
		return support.Conversation{}, nil, err
	}
	messages := view.Messages
	if len(messages) < 9 && binding.PreviousID != "" {
		previous, e := w.store.Conversation(ctx, int(row.SenderID), binding.PreviousID)
		if e != nil {
			return support.Conversation{}, nil, e
		}
		messages = append(previous.Messages, messages...)
	}
	conversation := view.Conversation
	if len(view.Messages) >= 480 {
		// Keep the actor key stable while rotating physical Store segments before
		// its storage-length cap. Old calls/receipts/messages remain untouched.
		next, e := w.store.CreateConversation(ctx, int(row.SenderID), fmt.Sprintf("%s:segment:%d", key, binding.Generation+1), w.now())
		if e != nil {
			return support.Conversation{}, nil, e
		}
		update := query().Model(&dialogue{}).Where("generation = ?", binding.Generation).Updates(map[string]any{"conversation_id": next.ID, "previous_id": conversation.ID, "generation": binding.Generation + 1})
		if update.Error != nil || update.RowsAffected != 1 {
			return support.Conversation{}, nil, errors.New("Telegram actor segment unavailable")
		}
		conversation = next
	}
	history := make([]support.PromptMessage, 0, 9)
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		history = append(history, support.PromptMessage{Role: message.Role, Content: w.clean(message.Content)})
	}
	if len(history) > 9 {
		history = history[len(history)-9:]
	}
	return conversation, history, nil
}
