// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package spending

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type BindingReader struct{ DB *gorm.DB }

func OpenBindingStore(path string) (*BindingReader, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalid
	}
	if info, e := os.Stat(path); e != nil || !info.Mode().IsRegular() {
		return nil, ErrUnavailable
	}
	uri := (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()
	db, e := gorm.Open(sqlite.Open("file:"+uri+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		return nil, ErrUnavailable
	}
	raw, e := db.DB()
	if e != nil {
		return nil, ErrUnavailable
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	return &BindingReader{DB: db}, nil
}
func (b *BindingReader) Close() error {
	if b == nil || b.DB == nil {
		return nil
	}
	raw, e := b.DB.DB()
	if e != nil {
		return e
	}
	return raw.Close()
}
func (b *BindingReader) Lookup(ctx context.Context, owner int) (Binding, error) {
	if b == nil || b.DB == nil || owner <= 0 {
		return Binding{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var value Binding
	err := b.DB.WithContext(ctx).Table("support_telegram_bindings").Select("telegram_id,generation").Where("user_id = ?", owner).Take(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Binding{}, ErrNotFound
	}
	if err != nil || value.TelegramID <= 0 || value.Generation == "" || len(value.Generation) > 64 {
		return Binding{}, ErrUnavailable
	}
	return value, nil
}

type TelegramSender struct {
	token  string
	client *http.Client
}

var botTokenPattern = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{20,128}$`)

func NewTelegramSender(token string, client *http.Client) (*TelegramSender, error) {
	if !botTokenPattern.MatchString(token) {
		return nil, ErrInvalid
	}
	copy := http.Client{}
	if client != nil {
		copy = *client
	}
	copy.Timeout = 10 * time.Second
	copy.Jar = nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &TelegramSender{token: token, client: &copy}, nil
}
func (s TelegramSender) String() string   { return "SpendingTelegramSender{credentials:hidden}" }
func (s TelegramSender) GoString() string { return s.String() }
func ReadTelegramSender(path string) (*TelegramSender, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	raw, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(raw) > 4096 {
		return nil, ErrInvalid
	}
	var cfg struct {
		BotToken string `json:"bot_token"`
	}
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), &cfg) != nil {
		return nil, ErrInvalid
	}
	return NewTelegramSender(cfg.BotToken, nil)
}
func (s *TelegramSender) Send(ctx context.Context, chat int64) SendResult {
	if chat <= 0 {
		return SendResult{Status: "failed"}
	}
	raw, err := common.Marshal(map[string]any{"chat_id": chat, "text": "ZTAPI 消费预警 / Spending alert\n请查看站内通知 / Review your notifications: https://ztapi.vip/console/spending", "link_preview_options": map[string]bool{"is_disabled": true}})
	if err != nil {
		return SendResult{Status: "failed"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+s.token+"/sendMessage", bytes.NewReader(raw))
	if err != nil {
		return SendResult{Status: "failed"}
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return SendResult{Status: "unknown"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(body) > 16384 {
		return SendResult{Status: "unknown"}
	}
	var envelope struct {
		OK     bool `json:"ok"`
		Code   int  `json:"error_code"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(body)) != nil || common.Unmarshal(body, &envelope) != nil {
		return SendResult{Status: "unknown"}
	}
	if response.StatusCode == 200 && envelope.OK && envelope.Result.MessageID > 0 {
		return SendResult{Status: "sent"}
	}
	if !envelope.OK && response.StatusCode == 429 && envelope.Code == 429 && envelope.Parameters.RetryAfter > 0 && envelope.Parameters.RetryAfter <= 3600 {
		return SendResult{Status: "rate_limited", RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if !envelope.OK && envelope.Code == response.StatusCode && (response.StatusCode == 403 || response.StatusCode == 400) {
		return SendResult{Status: "blocked"}
	}
	return SendResult{Status: "unknown"}
}
