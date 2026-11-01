// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import "time"

type cursor struct {
	ID             int `gorm:"primaryKey;autoIncrement:false"`
	BotID, OwnerID int64
	Offset         int64
}

func (cursor) TableName() string { return "telegram_support_cursor" }

type group struct {
	ChatID       int64 `gorm:"primaryKey;autoIncrement:false"`
	Enabled      bool  `gorm:"not null"`
	LastCasualAt *time.Time
	UpdatedAt    time.Time
}

func (group) TableName() string { return "telegram_support_groups" }

type inbox struct {
	UpdateID                              int64 `gorm:"primaryKey;autoIncrement:false"`
	ChatID, SenderID                      int64
	MessageID, ThreadID                   int
	Date                                  int64
	Text                                  string `gorm:"type:text"`
	Command                               string
	Directed, Sensitive, Invalid, IsGroup bool
	FileID, MIMEType                      string
	FileSize                              int64
	State                                 string `gorm:"size:24;index"`
	CallID                                string `gorm:"size:64"`
	CreatedAt                             time.Time
}

func (inbox) TableName() string { return "telegram_support_inbox" }

type outbox struct {
	Key                     string `gorm:"primaryKey;size:128"`
	UpdateID                int64  `gorm:"index"`
	ChatID                  int64
	MessageID, ThreadID     int
	Text                    string `gorm:"type:text"`
	Kind                    string `gorm:"size:24"`
	State                   string `gorm:"size:16;index"`
	Attempts, SentMessageID int
	NextAttemptAt           time.Time `gorm:"index"`
	CreatedAt               time.Time
}

func (outbox) TableName() string { return "telegram_support_outbox" }

type dialogue struct {
	Key                        string `gorm:"primaryKey;size:128"`
	ChatID, SenderID           int64
	ThreadID                   int
	ConversationID, PreviousID string `gorm:"size:64"`
	Generation                 int64
}

func (dialogue) TableName() string { return "telegram_support_dialogues" }
