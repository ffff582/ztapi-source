// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package spending

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/shopspring/decimal"
)

var (
	Beijing          = time.FixedZone("Asia/Shanghai", 8*3600)
	ErrInvalid       = errors.New("invalid spending input")
	ErrConflict      = errors.New("spending version conflict")
	ErrNotFound      = errors.New("spending record not found")
	ErrUnavailable   = errors.New("spending evidence unavailable")
	thresholdPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,6})?$`)
)

type Identity struct {
	ID        int
	Status    int
	DeletedAt *time.Time
	Quota     int64
}
type Preferences struct {
	UserID       int       `json:"-" gorm:"primaryKey;autoIncrement:false"`
	Enabled      bool      `json:"enabled"`
	DailySpendU  string    `json:"daily_spend_u"`
	HourlySpendU string    `json:"hourly_spend_u"`
	LowBalanceU  string    `json:"low_balance_u"`
	InputTokens  int64     `json:"input_tokens"`
	Telegram     bool      `json:"telegram"`
	Version      int64     `json:"version"`
	EnabledAt    time.Time `json:"-"`
}

func (Preferences) TableName() string { return "spending_preferences" }
func validatePreferences(p Preferences) error {
	if p.Version < 0 || p.InputTokens < 0 || p.InputTokens > 1000000000 {
		return ErrInvalid
	}
	positive := p.InputTokens > 0
	for _, s := range []string{p.DailySpendU, p.HourlySpendU, p.LowBalanceU} {
		if s == "" {
			continue
		}
		if !thresholdPattern.MatchString(s) {
			return ErrInvalid
		}
		v, err := decimal.NewFromString(s)
		if err != nil || v.GreaterThan(decimal.NewFromInt(1000000)) {
			return ErrInvalid
		}
		positive = positive || v.IsPositive()
	}
	if p.Enabled && !positive {
		return ErrInvalid
	}
	return nil
}

type Overview struct {
	AsOf                   time.Time `json:"as_of"`
	Timezone               string    `json:"timezone"`
	DayStart               time.Time `json:"day_start"`
	Complete               bool      `json:"complete"`
	ConfirmedDailyU        *string   `json:"confirmed_daily_u"`
	ConfirmedHourlyU       *string   `json:"confirmed_hourly_u"`
	ConfirmedRefundsDailyU *string   `json:"confirmed_refunds_daily_u"`
	AvailableBalanceU      *string   `json:"available_balance_u"`
	PendingRequests        int       `json:"pending_requests"`
	IncompleteRequests     int       `json:"incomplete_requests"`
}

func emptyOverview(now time.Time) Overview {
	local := now.In(Beijing)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, Beijing)
	return Overview{AsOf: now.UTC(), Timezone: "Asia/Shanghai", DayStart: day}
}

type Input struct {
	RequestID, Model string
	Tokens           int64
	At               time.Time
}
type Snapshot struct {
	Overview Overview
	Inputs   []Input
	Binding  *Binding
}
type Source interface {
	Identity(context.Context, int) (Identity, error)
	Load(context.Context, int, time.Time) (Snapshot, error)
}
type Alert struct {
	ID                int64      `json:"id" gorm:"primaryKey"`
	UserID            int        `json:"-" gorm:"not null;index:spending_alert_owner,priority:1"`
	Kind              string     `json:"kind"`
	CreatedAt         time.Time  `json:"created_at" gorm:"index:spending_alert_owner,priority:2"`
	MeasuredValue     string     `json:"measured_value"`
	Threshold         string     `json:"threshold"`
	Unit              string     `json:"unit"`
	RequestID         string     `json:"request_id,omitempty"`
	Model             string     `json:"model,omitempty"`
	ReadAt            *time.Time `json:"read_at,omitempty"`
	DeliveryStatus    string     `json:"delivery_status" gorm:"index"`
	DedupKey          string     `json:"-" gorm:"uniqueIndex;not null"`
	PreferenceVersion int64      `json:"-"`
	BindingGeneration string     `json:"-"`
	NextAttempt       int64      `json:"-"`
	Attempts          int        `json:"-"`
}

func (Alert) TableName() string { return "spending_alerts" }

type Binding struct {
	TelegramID int64
	Generation string
}
type Bindings interface {
	Lookup(context.Context, int) (Binding, error)
}
type Sender interface {
	Send(context.Context, int64) SendResult
}
type SendResult struct {
	Status     string
	RetryAfter time.Duration
}
