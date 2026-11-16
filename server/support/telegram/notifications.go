// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/support/bridgeapi"
	"gorm.io/gorm/clause"
)

var notificationID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (w *Worker) pollNotifications(ctx context.Context) error {
	if w.cfg.Bridge == nil || w.now().Before(w.nextNotificationPoll) {
		return nil
	}
	w.nextNotificationPoll = w.now().Add(15 * time.Second)
	result, err := w.bridgeCall(ctx, bridgeapi.Request{Action: "poll_notifications"})
	if err != nil {
		return errors.New("Telegram ticket updates temporarily unavailable")
	}
	if len(result.Notifications) > 50 {
		return errors.New("Telegram ticket update batch rejected")
	}
	for _, n := range result.Notifications {
		if n.TelegramID <= 0 || n.TicketID == 0 || !notificationID.MatchString(n.ID) {
			continue
		}
		status := "有更新"
		switch n.Status {
		case "open":
			status = "待处理"
		case "in_progress":
			status = "处理中"
		case "resolved":
			status = "已结束"
		}
		// Never copy a reply body, arbitrary status, or URL from an event.
		entry := outbox{Key: "notification:" + n.ID, ChatID: n.TelegramID, BridgeEventID: n.ID, Text: fmt.Sprintf("工单 #%d 状态：%s。请登录 %s 查看。", n.TicketID, status, bindingURL), Kind: "notification", State: "pending", CreatedAt: w.now(), NextAttemptAt: w.now()}
		if w.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error != nil {
			return errors.New("Telegram ticket update persistence failed")
		}
	}
	return nil
}

func (w *Worker) retryOutbox(ctx context.Context, row outbox, delay time.Duration) error {
	if delay < time.Second*time.Duration(1<<min(row.Attempts, 8)) {
		delay = time.Second * time.Duration(1<<min(row.Attempts, 8))
	}
	if w.db.WithContext(ctx).Model(&outbox{}).Where("key = ?", row.Key).Updates(map[string]any{"attempts": row.Attempts + 1, "next_attempt_at": w.now().Add(delay)}).Error != nil {
		return errors.New("Telegram retry persistence failed")
	}
	return nil
}

func (w *Worker) ackNotifications(ctx context.Context) error {
	var rows []outbox
	if w.db.WithContext(ctx).Where("kind = ? AND state = ? AND acknowledged = ? AND next_attempt_at <= ?", "notification", "sent", false, w.now()).Order("created_at ASC, key ASC").Limit(30).Find(&rows).Error != nil {
		return errors.New("Telegram notification acknowledgements unavailable")
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err := w.bridgeCall(ctx, bridgeapi.Request{Action: "ack_notification", TelegramID: row.ChatID, EventID: row.BridgeEventID})
		if err != nil {
			if e := w.retryOutbox(ctx, row, time.Second); e != nil {
				return e
			}
			continue
		}
		if w.db.WithContext(ctx).Model(&outbox{}).Where("key = ? AND state = ?", row.Key, "sent").Update("acknowledged", true).Error != nil {
			return errors.New("Telegram notification acknowledgement persistence failed")
		}
	}
	return nil
}
