// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("support record not found")
var ErrConflict = errors.New("support record conflict")
var ErrBusy = errors.New("support request already running")
var ErrHumanHandoff = errors.New("support conversation is assigned to a human")

type Identity struct {
	ID, Role, Status int
	DeletedAt        *time.Time
}

type Diagnosis struct {
	RequestID     string    `json:"request_id"`
	Model         string    `json:"model"`
	CreatedAt     time.Time `json:"created_at"`
	Outcome       string    `json:"outcome"`
	Code          string    `json:"code"`
	Summary       string    `json:"summary"`
	HTTPStatus    int       `json:"http_status"`
	Stream        bool      `json:"stream"`
	Dispatched    bool      `json:"dispatched"`
	UsageReceived bool      `json:"usage_received"`
	ChargedU      string    `json:"charged_u"`
	ReservedU     string    `json:"reserved_u"`
	NeedsHuman    bool      `json:"needs_human"`
}

type Source interface {
	Identity(context.Context, int) (Identity, error)
	RecentRequests(context.Context, int) ([]Diagnosis, error)
	Diagnose(context.Context, int, string) (Diagnosis, error)
	Knowledge(context.Context, int, string, string) (string, error)
}

type Conversation struct {
	ID             string    `json:"id" gorm:"primaryKey;size:64"`
	UserID         int       `json:"-" gorm:"not null;index"`
	Title          string    `json:"title" gorm:"size:120;not null"`
	IdempotencyKey string    `json:"-" gorm:"size:128;not null;uniqueIndex"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Message struct {
	ID             uint      `json:"id" gorm:"primaryKey"`
	ConversationID string    `json:"-" gorm:"size:64;not null;index"`
	Role           string    `json:"role" gorm:"size:16;not null"`
	Content        string    `json:"content" gorm:"type:text;not null"`
	HasImage       bool      `json:"has_image"`
	AttachmentID   *uint     `json:"attachment_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Ticket struct {
	ID             uint      `json:"id" gorm:"primaryKey"`
	UserID         int       `json:"-" gorm:"not null;index"`
	ConversationID string    `json:"conversation_id" gorm:"size:64;not null;index"`
	Subject        string    `json:"subject" gorm:"size:160;not null"`
	Content        string    `json:"content" gorm:"type:text;not null"`
	Contact        string    `json:"contact" gorm:"size:240"`
	RequestID      string    `json:"request_id" gorm:"size:128"`
	Status         string    `json:"status" gorm:"size:24;not null;index"`
	Version        uint64    `json:"version" gorm:"not null"`
	IdempotencyKey string    `json:"-" gorm:"size:128;not null;uniqueIndex"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// FinancialUsage contains no message, image, customer identity or credential.
// Nil usage/cost is unknown, never an implied zero. Quotes are cash estimates.
type FinancialUsage struct {
	CallID            string     `json:"call_id"`
	StartedAt         time.Time  `json:"started_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	ChannelID         int        `json:"channel_id"`
	Supplier          string     `json:"supplier"`
	Model             string     `json:"model"`
	Purpose           string     `json:"purpose"`
	Environment       string     `json:"environment"`
	Outcome           string     `json:"outcome"`
	Dispatched        bool       `json:"dispatched"`
	InputTokens       *int64     `json:"input_tokens"`
	OutputTokens      *int64     `json:"output_tokens"`
	CacheReadTokens   *int64     `json:"cache_read_tokens"`
	InputTokenScope   string     `json:"input_token_scope"`
	QuoteCostU        *string    `json:"quote_cost_u"`
	ActualCostU       *string    `json:"actual_cost_u"`
	UpstreamCredit    *string    `json:"upstream_credit"`
	UpstreamRequestID string     `json:"upstream_request_id"`
	QuoteEvidence     string     `json:"quote_evidence"`
	ActualEvidence    string     `json:"actual_evidence"`
}
