// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/QuantumNous/new-api/support/bridgeapi"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ticketNotification struct {
	ID             string    `gorm:"primaryKey;size:64"`
	UserID         int       `gorm:"not null;index"`
	TelegramID     int64     `gorm:"not null;index"`
	Generation     string    `gorm:"size:64;not null;uniqueIndex:notification_event,priority:1"`
	TicketID       uint      `gorm:"not null;uniqueIndex:notification_event,priority:2"`
	TicketVersion  uint64    `gorm:"not null;uniqueIndex:notification_event,priority:3"`
	ConversationID string    `gorm:"size:64;not null"`
	Status         string    `gorm:"size:24;not null"`
	State          string    `gorm:"size:24;not null;index"`
	CreatedAt      time.Time `gorm:"not null;index"`
}

func (ticketNotification) TableName() string { return "support_telegram_notifications" }

func (b *TelegramBridge) notifications(ctx context.Context) ([]bridgeapi.Notification, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var candidates []struct {
		UserID         int
		TelegramID     int64
		Generation     string
		TicketID       uint
		Version        uint64
		ConversationID string
		Status         string
	}
	err := b.store.db.WithContext(ctx).Table("support_tickets AS t").Select("t.user_id, b.telegram_id, b.generation, t.id AS ticket_id, t.version, t.conversation_id, t.status").Joins("JOIN support_telegram_bindings AS b ON b.user_id = t.user_id").
		Where("t.version > 1 AND t.updated_at >= b.bound_at").
		Where("NOT EXISTS (SELECT 1 FROM support_telegram_notifications n WHERE n.generation = b.generation AND n.ticket_id = t.id AND n.ticket_version = t.version)").
		Order("t.updated_at, t.id").Limit(20).Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		state := "pending"
		if err = b.activeUser(ctx, c.UserID); err != nil {
			if errors.Is(err, ErrNotFound) {
				state = "revoked"
			} else {
				return nil, err
			}
		}
		event := ticketNotification{ID: keyFor(c.UserID, "tg-notify", fmt.Sprintf("%s:%d:%d", c.Generation, c.TicketID, c.Version)), UserID: c.UserID, TelegramID: c.TelegramID, Generation: c.Generation, TicketID: c.TicketID, TicketVersion: c.Version, ConversationID: c.ConversationID, Status: c.Status, State: state, CreatedAt: b.now()}
		if err = b.store.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&event).Error; err != nil {
			return nil, err
		}
	}
	var pending []ticketNotification
	err = b.store.db.WithContext(ctx).Table("support_telegram_notifications AS n").Select("n.*").Joins("JOIN support_telegram_bindings AS b ON b.user_id = n.user_id AND b.telegram_id = n.telegram_id AND b.generation = n.generation").Where("n.state = ?", "pending").Order("n.created_at,n.id").Limit(20).Find(&pending).Error
	if err != nil {
		return nil, err
	}
	out := []bridgeapi.Notification{}
	for _, n := range pending {
		if err = b.activeUser(ctx, n.UserID); err != nil {
			if errors.Is(err, ErrNotFound) {
				if err = b.store.db.WithContext(ctx).Model(&ticketNotification{}).Where("id = ? AND state = ?", n.ID, "pending").Update("state", "revoked").Error; err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}
		out = append(out, bridgeapi.Notification{ID: n.ID, TelegramID: n.TelegramID, TicketID: n.TicketID, Status: n.Status, Kind: "ticket_update", URL: "https://ztapi.vip/console/support?conversation=" + url.QueryEscape(n.ConversationID)})
	}
	return out, nil
}

func (b *TelegramBridge) notificationAccess(ctx context.Context, tg int64, id string, ack bool) (bool, error) {
	link, err := b.bindingForTelegram(ctx, tg)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var event ticketNotification
	err = b.store.db.WithContext(ctx).Where("id = ? AND telegram_id = ? AND user_id = ? AND generation = ?", id, tg, link.UserID, link.Generation).Take(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if event.State != "pending" && event.State != "sent" {
		return false, nil
	}
	if ack && event.State == "pending" {
		err = b.store.db.WithContext(ctx).Model(&ticketNotification{}).Where("id = ? AND state = ?", id, "pending").Update("state", "sent").Error
	}
	return true, err
}
