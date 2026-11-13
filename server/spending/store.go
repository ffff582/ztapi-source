// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package spending

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil || db.Dialector.Name() != "sqlite" {
		return nil, ErrInvalid
	}
	raw, err := db.DB()
	if err != nil {
		return nil, err
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	var foreign int64
	if err = db.Table("sqlite_master").Where("type = ? AND name NOT LIKE ? AND name NOT LIKE ?", "table", "spending_%", "sqlite_%").Count(&foreign).Error; err != nil || foreign != 0 {
		return nil, ErrInvalid
	}
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=1000", "PRAGMA foreign_keys=ON"} {
		if err = db.Exec(q).Error; err != nil {
			return nil, err
		}
	}
	if err = db.AutoMigrate(&Preferences{}, &Alert{}, &ruleState{}); err != nil {
		return nil, err
	}
	// A process may have died after Telegram accepted a message. Never replay it.
	if err = db.Model(&Alert{}).Where("delivery_status = ?", "sending").Update("delivery_status", "unknown").Error; err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Preferences(ctx context.Context, owner int) (Preferences, error) {
	if owner <= 0 {
		return Preferences{}, ErrInvalid
	}
	var p Preferences
	err := s.db.WithContext(ctx).Where("user_id = ?", owner).Take(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Preferences{UserID: owner}, nil
	}
	return p, err
}
func (s *Store) SavePreferences(ctx context.Context, owner int, p Preferences, now time.Time) (Preferences, error) {
	if owner <= 0 || validatePreferences(p) != nil {
		return Preferences{}, ErrInvalid
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old Preferences
		err := tx.Where("user_id = ?", owner).Take(&old).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if p.Version != 0 {
				return ErrConflict
			}
			p.UserID = owner
			p.Version = 1
			if p.Enabled {
				p.EnabledAt = now.UTC()
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&p)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
			return nil
		}
		if err != nil {
			return err
		}
		if old.Version != p.Version {
			return ErrConflict
		}
		p.UserID = owner
		p.Version++
		p.EnabledAt = old.EnabledAt
		if p.Enabled && !old.Enabled {
			p.EnabledAt = now.UTC()
		}
		result := tx.Model(&Preferences{}).Where("user_id = ? AND version = ?", owner, old.Version).Select("enabled", "daily_spend_u", "hourly_spend_u", "low_balance_u", "input_tokens", "telegram", "version", "enabled_at").Updates(&p)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		return tx.Model(&Alert{}).Where("user_id = ? AND delivery_status = ?", owner, "pending").Update("delivery_status", "cancelled").Error
	})
	return p, err
}
func (s *Store) Alerts(ctx context.Context, owner, limit int, before int64) ([]Alert, int64, error) {
	if owner <= 0 || limit < 1 || limit > 100 || before < 0 {
		return nil, 0, ErrInvalid
	}
	rows := []Alert{}
	var count int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Alert{}).Where("user_id = ? AND read_at IS NULL", owner).Count(&count).Error; err != nil {
			return err
		}
		q := tx.Where("user_id = ?", owner)
		if before > 0 {
			q = q.Where("id < ?", before)
		}
		return q.Order("id DESC").Limit(limit).Find(&rows).Error
	})
	return rows, count, err
}
func (s *Store) ReadAlert(ctx context.Context, owner int, id int64, now time.Time) error {
	if owner <= 0 || id <= 0 {
		return ErrNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Alert
		if err := tx.Select("id").Where("id = ? AND user_id = ?", id, owner).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		return tx.Model(&Alert{}).Where("id = ? AND user_id = ? AND read_at IS NULL", id, owner).Update("read_at", now.UTC()).Error
	})
}
