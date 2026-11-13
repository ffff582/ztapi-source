// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type TelegramBridgeConfig struct {
	Secret      []byte
	BotUsername string
	Now         func() time.Time
	AdmitWrite  func() error
}

type TelegramBridge struct {
	store  *Store
	source Source
	cfg    TelegramBridgeConfig
	mu     sync.Mutex
}

type bindingChallenge struct {
	ID          string `gorm:"primaryKey;size:64"`
	UserID      int    `gorm:"not null;index"`
	TokenHash   string `gorm:"size:64;not null;uniqueIndex"`
	State       string `gorm:"size:24;not null;index"`
	TelegramID  int64
	Username    string    `gorm:"size:64"`
	DisplayName string    `gorm:"size:128"`
	CreatedAt   time.Time `gorm:"not null;index"`
	ExpiresAt   time.Time `gorm:"not null"`
}

func (bindingChallenge) TableName() string { return "support_telegram_binding_challenges" }

type telegramBinding struct {
	UserID      int       `gorm:"primaryKey;autoIncrement:false"`
	TelegramID  int64     `gorm:"not null;uniqueIndex"`
	Username    string    `gorm:"size:64"`
	DisplayName string    `gorm:"size:128"`
	Generation  string    `gorm:"size:64;not null;uniqueIndex"`
	BoundAt     time.Time `gorm:"not null"`
}

func (telegramBinding) TableName() string { return "support_telegram_bindings" }

type bindingAction struct {
	ID          string    `gorm:"primaryKey;size:64"`
	UserID      int       `gorm:"not null;index"`
	Action      string    `gorm:"size:24;not null"`
	PayloadHash string    `gorm:"size:64;not null"`
	ChallengeID string    `gorm:"size:64"`
	CreatedAt   time.Time `gorm:"not null"`
}

func (bindingAction) TableName() string            { return "support_telegram_binding_actions" }
func (*bindingAction) BeforeUpdate(*gorm.DB) error { return errors.New("binding audit is immutable") }
func (*bindingAction) BeforeDelete(*gorm.DB) error { return errors.New("binding audit is immutable") }

type bridgeNonce struct {
	ID        string    `gorm:"primaryKey;size:32"`
	CreatedAt time.Time `gorm:"not null;index"`
}

func (bridgeNonce) TableName() string { return "support_telegram_bridge_nonces" }

type BindingInput struct {
	Action         string `json:"action"`
	IdempotencyKey string `json:"idempotency_key"`
	ChallengeID    string `json:"challenge_id,omitempty"`
}
type PendingBindingView struct {
	ID          string    `json:"id"`
	Claimed     bool      `json:"claimed"`
	TelegramID  int64     `json:"telegram_id,omitempty"`
	Username    string    `json:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type BindingView struct {
	Enabled     bool                `json:"enabled"`
	Bound       bool                `json:"bound"`
	TelegramID  int64               `json:"telegram_id,omitempty"`
	Username    string              `json:"username,omitempty"`
	DisplayName string              `json:"display_name,omitempty"`
	BoundAt     *time.Time          `json:"bound_at,omitempty"`
	Pending     *PendingBindingView `json:"pending,omitempty"`
	BindingURL  string              `json:"binding_url,omitempty"`
}

func NewTelegramBridge(store *Store, source Source, cfg TelegramBridgeConfig) (*TelegramBridge, error) {
	if store == nil || source == nil || len(cfg.Secret) != 32 || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`).MatchString(cfg.BotUsername) {
		return nil, errors.New("invalid Telegram support bridge configuration")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Secret = append([]byte(nil), cfg.Secret...)
	if err := store.db.AutoMigrate(&bindingChallenge{}, &telegramBinding{}, &bindingAction{}, &bridgeNonce{}, &ticketNotification{}); err != nil {
		return nil, err
	}
	return &TelegramBridge{store: store, source: source, cfg: cfg}, nil
}
func (b *TelegramBridge) now() time.Time { return b.cfg.Now().UTC() }
func (b *TelegramBridge) activeUser(ctx context.Context, user int) error {
	identity, err := b.source.Identity(ctx, user)
	if err != nil {
		return err
	}
	if identity.ID != user || identity.Status != common.UserStatusEnabled || identity.DeletedAt != nil {
		return ErrNotFound
	}
	return nil
}
func (b *TelegramBridge) bindingForTelegram(ctx context.Context, tg int64) (telegramBinding, error) {
	var link telegramBinding
	if tg <= 0 {
		return link, ErrNotFound
	}
	err := b.store.db.WithContext(ctx).Where("telegram_id = ?", tg).Take(&link).Error
	if err != nil {
		return link, notFound(err)
	}
	return link, b.activeUser(ctx, link.UserID)
}
func (b *TelegramBridge) Binding(ctx context.Context, user int) (BindingView, error) {
	if err := b.activeUser(ctx, user); err != nil {
		return BindingView{}, err
	}
	return b.bindingView(ctx, user)
}
func (b *TelegramBridge) bindingView(ctx context.Context, user int) (BindingView, error) {
	view := BindingView{Enabled: true}
	var link telegramBinding
	err := b.store.db.WithContext(ctx).Where("user_id = ?", user).Take(&link).Error
	if err == nil {
		view.Bound = true
		view.TelegramID = link.TelegramID
		view.Username = link.Username
		view.DisplayName = link.DisplayName
		view.BoundAt = &link.BoundAt
		return view, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return view, err
	}
	var challenge bindingChallenge
	err = b.store.db.WithContext(ctx).Where("user_id = ? AND state IN ?", user, []string{"pending", "claimed"}).Order("created_at DESC, id DESC").Take(&challenge).Error
	if err == nil {
		view.Pending = &PendingBindingView{ID: challenge.ID, Claimed: challenge.State == "claimed", TelegramID: challenge.TelegramID, Username: challenge.Username, DisplayName: challenge.DisplayName, ExpiresAt: challenge.ExpiresAt}
		return view, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return view, err
}
func (b *TelegramBridge) challengeToken(c bindingChallenge) string {
	mac := hmac.New(sha256.New, b.cfg.Secret)
	_, _ = mac.Write([]byte(fmt.Sprintf("ztapi-bind:v1:%s:%d", c.ID, c.UserID)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (b *TelegramBridge) UpdateBinding(ctx context.Context, user int, in BindingInput) (BindingView, error) {
	if user <= 0 || !validKey(in.IdempotencyKey) || len(in.ChallengeID) > 64 {
		return BindingView{}, ErrConflict
	}
	if in.Action != "create" && in.Action != "confirm" && in.Action != "cancel" && in.Action != "unbind" {
		return BindingView{}, ErrConflict
	}
	if err := b.activeUser(ctx, user); err != nil {
		return BindingView{}, err
	}
	if b.cfg.AdmitWrite != nil {
		if err := b.cfg.AdmitWrite(); err != nil {
			return BindingView{}, err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	auditID := keyFor(user, "telegram_binding_action", in.IdempotencyKey)
	hash := keyFor(user, in.Action, in.ChallengeID)
	var created bindingChallenge
	freshCreate := false
	err := b.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old bindingAction
		if e := tx.Where("id = ?", auditID).Take(&old).Error; e == nil {
			if old.PayloadHash != hash || old.Action != in.Action {
				return ErrConflict
			}
			if in.Action == "create" {
				return tx.Where("id = ? AND user_id = ?", old.ChallengeID, user).Take(&created).Error
			}
			return nil
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		audit := bindingAction{ID: auditID, UserID: user, Action: in.Action, PayloadHash: hash, CreatedAt: now}
		switch in.Action {
		case "create":
			var count int64
			if e := tx.Model(&telegramBinding{}).Where("user_id = ?", user).Count(&count).Error; e != nil {
				return e
			}
			if count > 0 {
				return ErrConflict
			}
			if e := tx.Model(&bindingChallenge{}).Where("user_id = ? AND created_at >= ?", user, now.Add(-10*time.Minute)).Count(&count).Error; e != nil {
				return e
			}
			if count >= 5 {
				return ErrRateLimited
			}
			if e := tx.Model(&bindingChallenge{}).Where("user_id = ? AND state IN ?", user, []string{"pending", "claimed"}).Update("state", "cancelled").Error; e != nil {
				return e
			}
			id, e := newID()
			if e != nil {
				return e
			}
			created = bindingChallenge{ID: id, UserID: user, State: "pending", CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
			created.TokenHash = tokenDigest(b.challengeToken(created))
			audit.ChallengeID = id
			if e = tx.Create(&created).Error; e != nil {
				return e
			}
			freshCreate = true
		case "confirm":
			var c bindingChallenge
			if e := tx.Where("id = ? AND user_id = ?", in.ChallengeID, user).Take(&c).Error; e != nil {
				return notFound(e)
			}
			if c.State != "claimed" || !c.ExpiresAt.After(now) || c.TelegramID <= 0 {
				return ErrConflict
			}
			var count int64
			if e := tx.Model(&telegramBinding{}).Where("user_id = ? OR telegram_id = ?", user, c.TelegramID).Count(&count).Error; e != nil {
				return e
			}
			if count > 0 {
				return ErrConflict
			}
			link := telegramBinding{UserID: user, TelegramID: c.TelegramID, Username: c.Username, DisplayName: c.DisplayName, Generation: c.ID, BoundAt: now}
			if e := tx.Create(&link).Error; e != nil {
				return e
			}
			if e := tx.Model(&c).Update("state", "confirmed").Error; e != nil {
				return e
			}
			audit.ChallengeID = c.ID
		case "cancel":
			q := tx.Model(&bindingChallenge{}).Where("user_id = ? AND state IN ?", user, []string{"pending", "claimed"})
			if in.ChallengeID != "" {
				q = q.Where("id = ?", in.ChallengeID)
			}
			if e := q.Update("state", "cancelled").Error; e != nil {
				return e
			}
		case "unbind":
			if e := tx.Where("user_id = ?", user).Delete(&telegramBinding{}).Error; e != nil {
				return e
			}
			if e := tx.Model(&bindingChallenge{}).Where("user_id = ? AND state IN ?", user, []string{"pending", "claimed"}).Update("state", "cancelled").Error; e != nil {
				return e
			}
			if e := tx.Model(&ticketNotification{}).Where("user_id = ? AND state = ?", user, "pending").Update("state", "revoked").Error; e != nil {
				return e
			}
		}
		return tx.Create(&audit).Error
	})
	if err != nil {
		return BindingView{}, err
	}
	view, err := b.bindingView(ctx, user)
	if err == nil && freshCreate && created.State == "pending" && created.ExpiresAt.After(now) && view.Pending != nil && view.Pending.ID == created.ID {
		view.BindingURL = "https://t.me/" + b.cfg.BotUsername + "?start=" + url.QueryEscape("bind_"+b.challengeToken(created))
	}
	return view, err
}

func (b *TelegramBridge) claimBinding(ctx context.Context, tg int64, token, username, displayName string) (time.Time, error) {
	if tg <= 0 || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(token) || !regexp.MustCompile(`^[A-Za-z0-9_]{0,64}$`).MatchString(username) || !boundedText(displayName, 128) {
		return time.Time{}, ErrConflict
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var expires time.Time
	err := b.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c bindingChallenge
		if e := tx.Where("token_hash = ?", tokenDigest(token)).Take(&c).Error; e != nil {
			return notFound(e)
		}
		if !c.ExpiresAt.After(b.now()) || (c.State != "pending" && c.State != "claimed") {
			return ErrConflict
		}
		if c.State == "claimed" && c.TelegramID != tg {
			return ErrConflict
		}
		var count int64
		if e := tx.Model(&telegramBinding{}).Where("telegram_id = ?", tg).Count(&count).Error; e != nil {
			return e
		}
		if count > 0 {
			return ErrConflict
		}
		if err := b.activeUser(ctx, c.UserID); err != nil {
			return err
		}
		expires = c.ExpiresAt
		return tx.Model(&c).Updates(map[string]any{"state": "claimed", "telegram_id": tg, "username": username, "display_name": Redact(displayName)}).Error
	})
	return expires, err
}
