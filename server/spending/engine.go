// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package spending

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ruleState struct {
	Key    string `gorm:"primaryKey"`
	UserID int    `gorm:"index"`
	Kind   string
	LastAt int64
	Armed  bool
}

func (ruleState) TableName() string { return "spending_rule_states" }
func (s *Store) Evaluate(ctx context.Context, owner int, snap Snapshot, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p Preferences
		err := tx.Where("user_id = ?", owner).Take(&p).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !p.Enabled {
			return nil
		}
		notice := func(kind, measured, threshold, unit, request, model string) error {
			state := ruleState{Key: fmt.Sprintf("%d:%s", owner, kind), UserID: owner, Kind: kind, Armed: true}
			if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; e != nil {
				return e
			}
			if e := tx.Where("key = ?", state.Key).Take(&state).Error; e != nil {
				return e
			}
			dedup := fmt.Sprintf("%d:%s:", owner, kind)
			switch kind {
			case "daily_spend":
				dedup += now.In(Beijing).Format("2006-01-02")
			case "hourly_spend":
				if state.LastAt != 0 && now.Unix()-state.LastAt < 3600 {
					return nil
				}
				dedup += strconv.FormatInt(now.Unix(), 10)
			case "low_balance":
				if !state.Armed || state.LastAt != 0 && now.Unix()-state.LastAt < 86400 {
					return nil
				}
				dedup += strconv.FormatInt(now.Unix(), 10)
			case "large_input":
				dedup += request
			}
			row := Alert{UserID: owner, Kind: kind, CreatedAt: now.UTC(), MeasuredValue: measured, Threshold: threshold, Unit: unit, RequestID: request, Model: model, DedupKey: dedup, PreferenceVersion: p.Version, DeliveryStatus: "not_requested"}
			if p.Telegram && snap.Binding != nil && snap.Binding.TelegramID > 0 && snap.Binding.Generation != "" {
				row.DeliveryStatus = "pending"
				row.BindingGeneration = snap.Binding.Generation
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			return tx.Model(&ruleState{}).Where("key = ?", state.Key).Updates(map[string]any{"last_at": now.Unix(), "armed": kind != "low_balance"}).Error
		}
		check := func(kind string, value *string, threshold string, low bool) error {
			if value == nil || threshold == "" {
				return nil
			}
			v, e := decimal.NewFromString(*value)
			if e != nil {
				return ErrUnavailable
			}
			q, e := decimal.NewFromString(threshold)
			if e != nil || !q.IsPositive() {
				return nil
			}
			hit := v.GreaterThanOrEqual(q)
			if low {
				hit = v.LessThanOrEqual(q)
				if !hit {
					return tx.Model(&ruleState{}).Where("user_id = ? AND kind = ?", owner, kind).Update("armed", true).Error
				}
			}
			if hit {
				return notice(kind, v.String(), threshold, "U", "", "")
			}
			return nil
		}
		if snap.Overview.Complete {
			if err = check("daily_spend", snap.Overview.ConfirmedDailyU, p.DailySpendU, false); err != nil {
				return err
			}
			if err = check("hourly_spend", snap.Overview.ConfirmedHourlyU, p.HourlySpendU, false); err != nil {
				return err
			}
		}
		if err = check("low_balance", snap.Overview.AvailableBalanceU, p.LowBalanceU, true); err != nil {
			return err
		}
		if p.InputTokens > 0 && snap.Overview.Complete {
			for _, in := range snap.Inputs {
				if in.Tokens < p.InputTokens || in.RequestID == "" || len(in.RequestID) > 128 || len(in.Model) > 200 || in.At.Before(p.EnabledAt) || in.At.After(now) {
					continue
				}
				if err = notice("large_input", strconv.FormatInt(in.Tokens, 10), strconv.FormatInt(p.InputTokens, 10), "tokens", in.RequestID, in.Model); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

type Engine struct {
	Store    *Store
	Source   Source
	Bindings Bindings
	Sender   Sender
	cursor   int
}

func (e *Engine) RunOnce(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var prefs []Preferences
	if err := e.Store.db.WithContext(ctx).Where("enabled = ? AND user_id > ?", true, e.cursor).Order("user_id").Limit(20).Find(&prefs).Error; err != nil {
		return err
	}
	process := func(p Preferences) error {
		u, err := e.Source.Identity(ctx, p.UserID)
		if err != nil || u.ID != p.UserID || u.Status != 1 || u.DeletedAt != nil {
			return nil
		}
		snap, err := e.Source.Load(ctx, p.UserID, now)
		if err != nil {
			return nil
		}
		if p.Telegram && e.Bindings != nil {
			if b, err := e.Bindings.Lookup(ctx, p.UserID); err == nil {
				snap.Binding = &b
			}
		}
		if err = e.Store.Evaluate(ctx, p.UserID, snap, now); err != nil {
			return err
		}
		return nil
	}
	for _, p := range prefs {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := process(p)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		e.cursor = p.UserID
	}
	if len(prefs) < 20 {
		e.cursor = 0
	}
	if e.Sender != nil && e.Bindings != nil {
		return e.Deliver(ctx, now)
	}
	return nil
}
func (e *Engine) Deliver(ctx context.Context, now time.Time) error {
	var rows []Alert
	if err := e.Store.db.WithContext(ctx).Where("delivery_status = ? AND next_attempt <= ?", "pending", now.Unix()).Order("id").Limit(20).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		b, ok, readErr := e.deliveryAllowed(ctx, row)
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if !ok {
			if err := e.Store.db.WithContext(ctx).Model(&Alert{}).Where("id = ? AND delivery_status = ?", row.ID, "pending").Update("delivery_status", "cancelled").Error; err != nil {
				return err
			}
			continue
		}
		claim := e.Store.db.WithContext(ctx).Model(&Alert{}).Where("id = ? AND delivery_status = ?", row.ID, "pending").Updates(map[string]any{"delivery_status": "sending", "attempts": gorm.Expr("attempts + 1")})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			continue
		}
		// Recheck after claiming: preference saves/disable and binding changes may
		// have happened while waiting for the private SQLite writer.
		fresh, allowed, readErr := e.deliveryAllowed(ctx, row)
		status := "cancelled"
		delay := int64(0)
		if readErr != nil {
			status = "pending"
		}
		if readErr == nil && allowed && fresh == b {
			result := e.Sender.Send(ctx, b.TelegramID)
			status = result.Status
			switch status {
			case "sent", "blocked", "failed", "unknown":
			case "rate_limited":
				if row.Attempts < 2 && result.RetryAfter > 0 && result.RetryAfter <= time.Hour {
					status = "pending"
					delay = now.Add(result.RetryAfter).Unix()
				} else {
					status = "failed"
				}
			default:
				status = "unknown"
			}
		}
		updates := map[string]any{"delivery_status": status, "next_attempt": delay}
		if readErr != nil {
			updates["attempts"] = gorm.Expr("attempts - 1")
		}
		writeCtx := ctx
		cancelWrite := func() {}
		if readErr != nil {
			// No send was attempted. Restore pending even if the read exhausted
			// its caller's deadline; this is not retrying an ambiguous send.
			writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
		}
		err := e.Store.db.WithContext(writeCtx).Model(&Alert{}).Where("id = ? AND delivery_status = ?", row.ID, "sending").Updates(updates).Error
		cancelWrite()
		if err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) deliveryAllowed(ctx context.Context, row Alert) (Binding, bool, error) {
	if e.Bindings == nil || e.Source == nil || e.Sender == nil {
		return Binding{}, false, ErrUnavailable
	}
	p, err := e.Store.Preferences(ctx, row.UserID)
	if err != nil {
		return Binding{}, false, err
	}
	if !p.Enabled || !p.Telegram || p.Version != row.PreferenceVersion {
		return Binding{}, false, nil
	}
	u, err := e.Source.Identity(ctx, row.UserID)
	if errors.Is(err, ErrNotFound) {
		return Binding{}, false, nil
	}
	if err != nil {
		return Binding{}, false, err
	}
	if u.ID != row.UserID || u.Status != 1 || u.DeletedAt != nil {
		return Binding{}, false, nil
	}
	b, err := e.Bindings.Lookup(ctx, row.UserID)
	if errors.Is(err, ErrNotFound) {
		return Binding{}, false, nil
	}
	if err != nil {
		return Binding{}, false, err
	}
	return b, b.TelegramID > 0 && b.Generation != "" && b.Generation == row.BindingGeneration, nil
}
