// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/support/bridgeapi"
)

type Bridge interface {
	Execute(context.Context, bridgeapi.Request) (bridgeapi.Result, error)
}

type Config struct {
	Bridge                       Bridge
	BindingKey                   []byte
	OwnerID, BotID               int64
	BotUsername                  string
	ChannelID                    int
	Model, Supplier, Environment string
	VisionEnabled                bool
	Now                          func() time.Time
	AdmitWrite                   func() error
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type Entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *User  `json:"user,omitempty"`
}
type PhotoSize struct {
	FileID        string `json:"file_id"`
	FileSize      int64  `json:"file_size"`
	Width, Height int
}
type Document struct {
	FileID   string `json:"file_id"`
	MIMEType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}
type Message struct {
	MessageID       int         `json:"message_id"`
	MessageThreadID int         `json:"message_thread_id"`
	Date            int64       `json:"date"`
	Chat            Chat        `json:"chat"`
	From            *User       `json:"from"`
	Text            string      `json:"text"`
	Caption         string      `json:"caption"`
	Entities        []Entity    `json:"entities"`
	CaptionEntities []Entity    `json:"caption_entities"`
	ReplyToMessage  *Message    `json:"reply_to_message,omitempty"`
	Photo           []PhotoSize `json:"photo,omitempty"`
	Document        *Document   `json:"document,omitempty"`
	NewChatMembers  []User      `json:"new_chat_members,omitempty"`
}
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

type APIError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return "telegram API request failed" }
