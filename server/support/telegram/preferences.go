// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var rememberIntent = regexp.MustCompile(`(?i)^(?:请)?(?:记住|记下|帮我记住)|^remember\b`)
var modelPreference = regexp.MustCompile(`(?i)(?:模型|model)\s*(?:是|为|[:=])?\s*([a-z][a-z0-9_.-]{0,79})(?:\s|$|[，,。])`)

func actorKey(row inbox) string {
	return conversationKey(&Message{Chat: Chat{ID: row.ChatID}, From: &User{ID: row.SenderID}, MessageThreadID: row.ThreadID})
}

func (w *Worker) preferences(ctx context.Context, row inbox) (bool, error) {
	text := strings.TrimSpace(regexp.MustCompile(`(?i)^@`+regexp.QuoteMeta(w.cfg.BotUsername)+`\s+`).ReplaceAllString(strings.TrimSpace(row.Text), ""))
	if row.Command == "forget" || text == "忘记我的偏好" {
		var current dialogue
		err := w.db.WithContext(ctx).Where("key = ?", actorKey(row)).Take(&current).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return true, errors.New("Telegram forget context unavailable")
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			current = dialogue{Key: actorKey(row), ChatID: row.ChatID, SenderID: row.SenderID, ThreadID: row.ThreadID}
		}
		current.ConversationID, current.PreviousID = "", ""
		current.Generation++
		err = w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("key = ?", actorKey(row)).Delete(&preference{}).Error; err != nil {
				return err
			}
			// A tombstone excludes history immediately without consuming Store's
			// new-conversation allowance. The next question creates a fresh segment.
			return tx.Save(&current).Error
		})
		if err != nil {
			return true, errors.New("Telegram forget persistence failed")
		}
		return true, w.complete(ctx, row, "已清除当前对话的偏好；此前文本不再用于后续模型上下文。财务凭证保留。", "control")
	}
	if row.Command != "memory" && !rememberIntent.MatchString(text) {
		return false, nil
	}
	p := preference{Key: actorKey(row)}
	oldErr := w.db.WithContext(ctx).Where("key = ?", p.Key).Take(&p).Error
	if oldErr != nil && !errors.Is(oldErr, gorm.ErrRecordNotFound) {
		return true, errors.New("Telegram preferences unavailable")
	}
	lower := strings.ToLower(row.Text)
	for _, name := range []string{"Claude Code", "Codex", "Cursor", "Cherry Studio", "OpenAI SDK"} {
		if strings.Contains(lower, strings.ToLower(name)) {
			p.Client = name
			break
		}
	}
	for _, name := range []string{"Windows", "macOS", "Linux", "Android", "iOS"} {
		if regexp.MustCompile(`(?i)\b` + name + `\b`).MatchString(row.Text) {
			p.OS = name
			break
		}
	}
	if model := modelPreference.FindStringSubmatch(row.Text); len(model) == 2 {
		p.Model = model[1]
	}
	if p.Client == "" && p.OS == "" && p.Model == "" {
		return true, w.complete(ctx, row, "只可自愿保存客户端、系统、模型三项偏好。你使用哪个客户端？", "control")
	}
	if w.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&p).Error != nil {
		return true, errors.New("Telegram preference persistence failed")
	}
	return true, w.complete(ctx, row, "已保存当前对话的客户端、系统或模型偏好，未保存其他内容。", "control")
}

func (w *Worker) preferencePrompt(ctx context.Context, row inbox) (string, error) {
	var p preference
	err := w.db.WithContext(ctx).Where("key = ?", actorKey(row)).Take(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("Telegram preferences unavailable")
	}
	return fmt.Sprintf("\n<preferences>client=%s; os=%s; model=%s</preferences>", p.Client, p.OS, p.Model), nil
}
