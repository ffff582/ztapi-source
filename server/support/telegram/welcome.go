// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const welcomeReply = "欢迎新朋友加入 ZTAPI 交流群！接入配置、模型选择、充值或账单问题，直接 @ztapi_bot 问我就好。涉及个人账号或扣费，请到 https://ztapi.vip 登录后咨询；群里不要发送密码、API Key 或敏感截图。"

func (w *Worker) captureWelcome(update Update) (inbox, bool) {
	m := update.Message
	if m == nil || len(m.NewChatMembers) == 0 {
		return inbox{}, false
	}
	row := inbox{UpdateID: update.UpdateID, State: "done", CreatedAt: w.now()}
	if len(m.NewChatMembers) > 100 || m.Chat.ID >= 0 || (m.Chat.Type != "group" && m.Chat.Type != "supergroup") || m.MessageID <= 0 || m.Date <= 0 {
		return row, true
	}
	var memberID int64
	for _, member := range m.NewChatMembers {
		if member.ID > 0 && !member.IsBot && member.ID != w.cfg.BotID {
			memberID = member.ID
			break
		}
	}
	if memberID == 0 {
		return row, true
	}
	fresh := time.Unix(m.Date, 0)
	if fresh.Before(w.now().Add(-2*time.Minute)) || fresh.Before(w.startedAt.Add(-30*time.Second)) || fresh.After(w.now().Add(30*time.Second)) {
		return row, true
	}
	row.ChatID, row.SenderID, row.MessageID, row.ThreadID, row.Date = m.Chat.ID, memberID, m.MessageID, m.MessageThreadID, m.Date
	row.IsGroup, row.Command = true, "welcome"
	return row, true
}

func (w *Worker) queueWelcome(ctx context.Context, tx *gorm.DB, row inbox) error {
	var active group
	err := tx.WithContext(ctx).Where("chat_id = ? AND enabled = ?", row.ChatID, true).Take(&active).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	entry := outbox{Key: replyKey(row.UpdateID), UpdateID: row.UpdateID, ChatID: row.ChatID, MessageID: row.MessageID, ThreadID: row.ThreadID, Text: welcomeReply, Kind: "welcome", State: "pending", NextAttemptAt: w.now(), CreatedAt: w.now()}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error
}
