// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const continuationWindow = 2 * time.Minute
const contextBatchWindow = 8 * time.Second

var contextMentions = regexp.MustCompile(`@[A-Za-z][A-Za-z0-9_]{4,31}`)

func (w *Worker) freshContext(row inbox) bool {
	date := time.Unix(row.Date, 0)
	return !date.Before(w.now().Add(-continuationWindow)) && !date.Before(w.startedAt.Add(-30*time.Second)) && !date.After(w.now().Add(30*time.Second))
}

func (w *Worker) activateFollowup(tx *gorm.DB, row *inbox) error {
	if row.Directed || !row.IsGroup || row.OtherTarget || row.Command != "" || strings.HasPrefix(strings.TrimSpace(row.Text), "/") || !w.freshContext(*row) {
		return nil
	}
	var enabled group
	err := tx.Where("chat_id = ? AND enabled = ?", row.ChatID, true).Take(&enabled).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var prior inbox
	err = tx.Where("chat_id = ? AND sender_id = ? AND thread_id = ? AND update_id < ? AND (directed = ? OR command = ?)", row.ChatID, row.SenderID, row.ThreadID, row.UpdateID, true, "forget").Order("update_id DESC").Take(&prior).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ordinaryContext(prior) || time.Unix(prior.Date, 0).Before(w.now().Add(-continuationWindow)) {
		return nil
	}
	changed := tx.Model(&inbox{}).Where("update_id = ? AND state = ? AND directed = ?", row.UpdateID, "queued", false).Updates(map[string]any{"directed": true, "context_followup": true})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected == 0 {
		return nil
	}
	row.Directed, row.ContextFollowup = true, true
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&outbox{Key: replyKey(row.UpdateID), UpdateID: row.UpdateID, ChatID: row.ChatID, MessageID: row.MessageID, ThreadID: row.ThreadID, Text: interruptedReply, Kind: "reply", State: "held", CreatedAt: w.now(), NextAttemptAt: w.now()}).Error
}

func ordinaryContext(row inbox) bool {
	text := strings.TrimSpace(contextMentions.ReplaceAllString(row.Text, ""))
	return row.Command == "" && !row.OtherTarget && !row.Credential && !row.Invalid && !row.Sensitive && !strings.HasPrefix(text, "/") && !rememberIntent.MatchString(text) && !handoffIntent.MatchString(text) && !accountIntent.MatchString(text) && text != "忘记我的偏好"
}

func (w *Worker) prepareContextBatches(ctx context.Context) error {
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []inbox
		if err := tx.Where("state = ? AND call_id = ? AND context_prepared = ?", "queued", "", false).Order("update_id ASC").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		fresh := make(map[int64]bool, len(rows))
		for i := range rows {
			// Freeze preparation before any BeginCall. Restart recovery must never
			// change content behind an existing idempotent billing admission.
			if err := tx.Model(&inbox{}).Where("update_id = ?", rows[i].UpdateID).Update("context_prepared", true).Error; err != nil {
				return err
			}
			if rows[i].CreatedAt.Before(w.startedAt) {
				continue
			}
			fresh[rows[i].UpdateID] = true
			if err := w.activateFollowup(tx, &rows[i]); err != nil {
				return err
			}
		}
		for i := range rows {
			parent := &rows[i]
			if !fresh[parent.UpdateID] || parent.State != "queued" || !parent.Directed || !ordinaryContext(*parent) || !w.freshContext(*parent) {
				continue
			}
			joined := 1
			for j := i + 1; j < len(rows) && joined < 6; j++ {
				child := &rows[j]
				if child.ChatID != parent.ChatID || child.SenderID != parent.SenderID || child.ThreadID != parent.ThreadID {
					continue
				}
				if !fresh[child.UpdateID] || child.State != "queued" || !child.ContextFollowup || !ordinaryContext(*child) || child.Date < parent.Date || child.Date-parent.Date > int64(contextBatchWindow/time.Second) {
					break
				}
				if parent.FileID != "" && child.FileID != "" {
					break
				}
				combined := parent.Text + "\n\n" + child.Text
				if w.clean(combined) != combined || w.hasCredentials(combined) {
					break
				}
				parent.Text, parent.Sensitive = combined, sensitive(combined)
				if child.FileID != "" {
					parent.FileID, parent.FileSize, parent.MIMEType = child.FileID, child.FileSize, child.MIMEType
				}
				if err := tx.Model(&inbox{}).Where("update_id = ?", parent.UpdateID).Updates(map[string]any{"text": parent.Text, "sensitive": parent.Sensitive, "file_id": parent.FileID, "file_size": parent.FileSize, "mime_type": parent.MIMEType}).Error; err != nil {
					return err
				}
				if err := tx.Model(&inbox{}).Where("update_id = ? AND state = ?", child.UpdateID, "queued").Updates(map[string]any{"state": "coalesced", "batch_update_id": parent.UpdateID, "file_id": ""}).Error; err != nil {
					return err
				}
				if err := tx.Model(&outbox{}).Where("update_id = ? AND state = ?", child.UpdateID, "held").Update("state", "suppressed").Error; err != nil {
					return err
				}
				child.State = "coalesced"
				joined++
				if parent.Sensitive {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return errors.New("Telegram context preparation unavailable")
	}
	return nil
}
