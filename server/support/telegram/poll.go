// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const pollFailureWindow = 60 * time.Second
const pollStaleWindow = 90 * time.Second

type pollState struct {
	ID                int `gorm:"primaryKey;autoIncrement:false"`
	Sequence          int64
	Failures          int
	FirstFailureAt    *time.Time
	LastAttemptAt     *time.Time
	LastSuccessAt     *time.Time
	LastErrorCategory string `gorm:"size:32"`
	LastErrorCode     int
	Announced         bool
}

func (pollState) TableName() string { return "telegram_support_poll_state" }

// PollEvent deliberately cannot carry raw errors, URLs, credentials or messages.
type PollEvent struct {
	Event, Category string
	Code, Failures  int
	Incident        int64
}

type PollHealth struct {
	OK                  bool       `json:"ok"`
	State               string     `json:"state"`
	Phase               string     `json:"phase"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	ErrorCategory       string     `json:"error_category,omitempty"`
	ErrorCode           int        `json:"error_code,omitempty"`
}

func (w *Worker) initPoll(ctx context.Context) error {
	if w.db.WithContext(ctx).AutoMigrate(&pollState{}) != nil {
		return errors.New("Telegram poll schema unavailable")
	}
	var state pollState
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&pollState{ID: 1}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", 1).Take(&state).Error; err != nil {
			return err
		}
		// A process crash after dispatch is ambiguous; do not repeat a status send.
		return tx.Model(&outbox{}).Where("kind IN ? AND state = ?", []string{"poll_alert", "poll_recovery"}, "dispatching").Update("state", "delivery_unknown").Error
	})
	if err != nil {
		return errors.New("Telegram poll status unavailable")
	}
	w.poll = state
	w.pollTouch("starting")
	return nil
}

func (w *Worker) pollTouch(phase string) {
	w.pollMu.Lock()
	w.pollPhase, w.pollProgress = phase, w.now()
	w.pollMu.Unlock()
}

func (w *Worker) PollHealth() PollHealth {
	w.pollMu.RLock()
	s, phase, progress := w.poll, w.pollPhase, w.pollProgress
	w.pollMu.RUnlock()
	now := w.now()
	h := PollHealth{State: "starting", Phase: phase, ConsecutiveFailures: s.Failures, LastSuccessAt: s.LastSuccessAt, LastAttemptAt: s.LastAttemptAt, ErrorCategory: s.LastErrorCategory, ErrorCode: s.LastErrorCode}
	if phase == "blocked" {
		h.State = "blocked"
		return h
	}
	if s.Announced || (s.FirstFailureAt != nil && now.Sub(*s.FirstFailureAt) >= pollFailureWindow) {
		h.State = "degraded"
		return h
	}
	if now.Sub(progress) > pollStaleWindow {
		h.State = "stalled"
		return h
	}
	if s.LastSuccessAt == nil || s.LastSuccessAt.Before(w.startedAt) {
		return h
	}
	// A bounded inference batch is work, not a failed poll. Each item advances progress.
	if phase != "processing" && now.Sub(*s.LastSuccessAt) > pollStaleWindow {
		h.State = "stale"
		return h
	}
	h.OK, h.State = true, "ok"
	if s.Failures > 0 {
		h.State = "retrying"
	}
	return h
}

func (w *Worker) recordPoll(ctx context.Context, pollErr error) error {
	w.pollMu.RLock()
	s := w.poll
	w.pollMu.RUnlock()
	now := w.now()
	firstSuccess := s.LastSuccessAt == nil
	s.LastAttemptAt = &now
	event := PollEvent{Event: "poll_success"}
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if pollErr != nil {
			category, code := "network", 0
			var apiErr *APIError
			if errors.As(pollErr, &apiErr) {
				category, code = apiErr.Category, apiErr.Code
			}
			if category == "" {
				category = apiCategory(code)
			}
			if s.FirstFailureAt == nil {
				s.FirstFailureAt = &now
				s.Sequence++
				if err := tx.Model(&outbox{}).Where("kind = ? AND state = ?", "poll_recovery", "pending").Update("state", "suppressed").Error; err != nil {
					return err
				}
			}
			s.Failures = min(s.Failures+1, 1000000000)
			s.LastErrorCategory, s.LastErrorCode = category, code
			event = PollEvent{Event: "poll_failure", Category: category, Code: code, Failures: s.Failures, Incident: s.Sequence}
			critical := code == 401 || code == 403 || code == 409
			if !s.Announced && (critical || (s.Failures >= 3 && now.Sub(*s.FirstFailureAt) >= pollFailureWindow)) {
				s.Announced = true
				text := fmt.Sprintf("Telegram 客服接收消息持续异常（故障 P%06d）。\n类别：%s；状态码：%d；连续失败：%d 次。\n正在自动重试；模型 API、客户余额与渠道未改动。恢复后会另行通知。", s.Sequence, category, code, s.Failures)
				if err := w.queuePollStatus(tx, s.Sequence, "poll_alert", text); err != nil {
					return err
				}
			}
		} else {
			if s.Announced {
				key := pollStatusKey(s.Sequence, "poll_alert")
				var alert outbox
				if err := tx.Where("key = ?", key).Take(&alert).Error; err != nil {
					return err
				}
				if err := tx.Model(&outbox{}).Where("key = ? AND state = ?", key, "pending").Update("state", "suppressed").Error; err != nil {
					return err
				}
				text := fmt.Sprintf("Telegram 客服接收消息已恢复（故障 P%06d）。\n异常持续约 %d 秒，已恢复拉取消息。", s.Sequence, int(now.Sub(*s.FirstFailureAt).Seconds()))
				if alert.State != "sent" {
					text += "\n此前故障提醒未确认送达；本条是恢复通知，不是新增故障。"
				}
				if err := w.queuePollStatus(tx, s.Sequence, "poll_recovery", text); err != nil {
					return err
				}
			}
			if s.Failures > 0 {
				event.Event = "poll_recovered"
			}
			s.LastSuccessAt, s.FirstFailureAt = &now, nil
			s.Failures, s.LastErrorCode, s.LastErrorCategory, s.Announced = 0, 0, "", false
		}
		return tx.Save(&s).Error
	})
	if err != nil {
		return errors.New("Telegram poll evidence persistence failed")
	}
	w.pollMu.Lock()
	w.poll, w.pollProgress = s, now
	w.pollMu.Unlock()
	if w.cfg.PollLog != nil && (event.Event != "poll_success" || firstSuccess) {
		w.cfg.PollLog(event)
	}
	return nil
}

func pollStatusKey(sequence int64, kind string) string { return fmt.Sprintf("%s:%d", kind, sequence) }
func pollStatusKind(kind string) bool                  { return kind == "poll_alert" || kind == "poll_recovery" }

func (w *Worker) queuePollStatus(tx *gorm.DB, sequence int64, kind, text string) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&outbox{Key: pollStatusKey(sequence, kind), Kind: kind, ChatID: w.cfg.OwnerID, Text: w.clean(text), State: "pending", CreatedAt: w.now(), NextAttemptAt: w.now()}).Error
}

func (w *Worker) sendPollStatus(ctx context.Context, row outbox) error {
	claimed := w.db.WithContext(ctx).Model(&outbox{}).Where("key = ? AND state = ?", row.Key, "pending").Updates(map[string]any{"state": "dispatching", "attempts": row.Attempts + 1})
	if claimed.Error != nil {
		return errors.New("Telegram status dispatch persistence failed")
	}
	if claimed.RowsAffected == 0 {
		return nil
	}
	id, sendErr := w.client.send(ctx, row.ChatID, 0, 0, w.clean(row.Text))
	state := "sent"
	changes := map[string]any{"state": state, "sent_message_id": id}
	if sendErr != nil {
		state = "delivery_unknown"
		var apiErr *APIError
		if errors.As(sendErr, &apiErr) && apiErr.Rejected {
			state = "pending"
			delay := time.Second * time.Duration(1<<min(row.Attempts, 8))
			if apiErr.RetryAfter > delay {
				delay = apiErr.RetryAfter
			}
			changes["next_attempt_at"] = w.now().Add(delay)
		}
		changes["state"] = state
	}
	if w.db.WithContext(ctx).Model(&outbox{}).Where("key = ? AND state = ?", row.Key, "dispatching").Updates(changes).Error != nil {
		return errors.New("Telegram status delivery persistence failed")
	}
	if state == "delivery_unknown" && w.cfg.PollLog != nil {
		event := PollEvent{Event: "status_delivery_unknown"}
		var apiErr *APIError
		if errors.As(sendErr, &apiErr) {
			event.Category, event.Code = apiErr.Category, apiErr.Code
		}
		w.cfg.PollLog(event)
	}
	return nil
}

func (w *Worker) pollRetryDelay(err error) time.Duration {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return time.Second
	}
	w.pollMu.RLock()
	failures := w.poll.Failures
	w.pollMu.RUnlock()
	base := min(time.Second*time.Duration(1<<min(max(failures-1, 0), 5)), 25*time.Second)
	delay := base + time.Duration(rand.Int64N(int64(base/5)+1))
	return max(delay, apiErr.RetryAfter)
}
