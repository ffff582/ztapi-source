// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package bridgeapi

import "time"

const Path = "/internal/telegram-support"
const HeaderTimestamp = "X-ZTAPI-Telegram-Timestamp"
const HeaderNonce = "X-ZTAPI-Telegram-Nonce"
const HeaderSignature = "X-ZTAPI-Telegram-Signature"

type Request struct {
	Action         string `json:"action"`
	TelegramID     int64  `json:"telegram_id,omitempty"`
	Token          string `json:"token,omitempty"`
	Username       string `json:"username,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
	Content        string `json:"content,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	EventID        string `json:"event_id,omitempty"`
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

type Ticket struct {
	ID             uint   `json:"id"`
	Status         string `json:"status"`
	ConversationID string `json:"conversation_id"`
}

type Notification struct {
	ID         string `json:"id"`
	TelegramID int64  `json:"telegram_id"`
	TicketID   uint   `json:"ticket_id"`
	Status     string `json:"status"`
	Kind       string `json:"kind"`
	URL        string `json:"url"`
}

type Result struct {
	Bound bool `json:"bound"`
	// BindingGeneration is opaque authorization metadata, never prompt or reply content.
	BindingGeneration string         `json:"binding_generation,omitempty"`
	Pending           bool           `json:"pending,omitempty"`
	ExpiresAt         *time.Time     `json:"expires_at,omitempty"`
	Requests          []Diagnosis    `json:"requests,omitempty"`
	Diagnosis         *Diagnosis     `json:"diagnosis,omitempty"`
	Ticket            *Ticket        `json:"ticket,omitempty"`
	Notifications     []Notification `json:"notifications,omitempty"`
	Allowed           bool           `json:"allowed,omitempty"`
}

type Response struct {
	Success bool   `json:"success"`
	Data    Result `json:"data"`
	Message string `json:"message,omitempty"`
}
