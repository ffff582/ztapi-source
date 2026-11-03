// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
)

type Client struct {
	token string
	botID int64
	http  *http.Client
}

var tokenPattern = regexp.MustCompile(`^([0-9]{5,20}):[A-Za-z0-9_-]{20,128}$`)
var filePattern = regexp.MustCompile(`^(photos|documents)/[A-Za-z0-9_/-]+\.(jpg|jpeg|png|webp)$`)

func NewClient(token string, httpClient *http.Client) (*Client, error) {
	match := tokenPattern.FindStringSubmatch(token)
	if len(match) != 2 {
		return nil, errors.New("invalid Telegram bot configuration")
	}
	id, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("invalid Telegram bot identity")
	}
	copy := http.Client{}
	if httpClient != nil {
		copy = *httpClient
	}
	copy.Jar = nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout <= 0 || copy.Timeout > 40*time.Second {
		copy.Timeout = 40 * time.Second
	}
	return &Client{token: token, botID: id, http: &copy}, nil
}
func (c Client) String() string   { return "TelegramClient{credentials:hidden}" }
func (c Client) GoString() string { return c.String() }

func (c *Client) call(ctx context.Context, method string, payload, result any) error {
	switch method {
	case "getUpdates", "sendMessage", "getFile", "getMe", "getChat", "getWebhookInfo":
	default:
		return errors.New("Telegram method is not allowed")
	}
	timeout := 30 * time.Second
	if method == "getUpdates" {
		timeout = 40 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := common.Marshal(payload)
	if err != nil {
		return &APIError{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.token+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return &APIError{}
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer response.Body.Close()
	maxResponseBytes := int64(1 << 20)
	if method == "getUpdates" {
		maxResponseBytes = 4 << 20
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return transportError(err)
	}
	if int64(len(body)) > maxResponseBytes {
		return &APIError{Category: "invalid_response"}
	}
	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		Code       int             `json:"error_code"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if common.Unmarshal(body, &envelope) != nil {
		return &APIError{Code: response.StatusCode, Category: apiCategory(response.StatusCode)}
	}
	if response.StatusCode != http.StatusOK || !envelope.OK {
		code := envelope.Code
		if code == 0 {
			code = response.StatusCode
		}
		delay := time.Duration(0)
		if envelope.Parameters.RetryAfter > 0 {
			seconds := envelope.Parameters.RetryAfter
			if seconds > 86400 {
				seconds = 86400
			}
			delay = time.Duration(seconds) * time.Second
		}
		return &APIError{Code: code, RetryAfter: delay, Category: apiCategory(code), Rejected: !envelope.OK && code >= 400 && code < 500}
	}
	if len(envelope.Result) == 0 || common.Unmarshal(envelope.Result, result) != nil {
		return &APIError{Category: "invalid_response"}
	}
	return nil
}

func transportError(err error) *APIError {
	category := "network"
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		category = "timeout"
	}
	return &APIError{Category: category}
}

func apiCategory(code int) string {
	switch {
	case code == 401 || code == 403:
		return "auth"
	case code == 409:
		return "receiver_conflict"
	case code == 429:
		return "rate_limited"
	case code >= 500:
		return "telegram_server"
	default:
		return "invalid_response"
	}
}

func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	if offset < 0 {
		return nil, &APIError{}
	}
	var result []Update
	err := c.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 20, "limit": 20, "allowed_updates": []string{"message", "my_chat_member"}}, &result)
	if err == nil && len(result) > 20 {
		return nil, &APIError{}
	}
	return result, err
}

func (c *Client) Verify(ctx context.Context, ownerID, botID int64, username string) error {
	if ownerID <= 0 || botID != c.botID {
		return errors.New("Telegram verification failed")
	}
	var me User
	if c.call(ctx, "getMe", struct{}{}, &me) != nil || !me.IsBot || me.ID != botID || !strings.EqualFold(me.Username, strings.TrimPrefix(username, "@")) {
		return errors.New("Telegram bot identity verification failed")
	}
	var chat Chat
	if c.call(ctx, "getChat", map[string]any{"chat_id": ownerID}, &chat) != nil || chat.ID != ownerID || chat.Type != "private" {
		return errors.New("Telegram owner private chat verification failed")
	}
	var webhook struct {
		URL string `json:"url"`
	}
	if c.call(ctx, "getWebhookInfo", struct{}{}, &webhook) != nil {
		return errors.New("Telegram webhook verification failed")
	}
	if webhook.URL != "" {
		return errors.New("Telegram webhook exists; polling is disabled")
	}
	return nil
}

func (c *Client) DownloadImage(ctx context.Context, fileID string) (*support.ImageInput, error) {
	if fileID == "" || len(fileID) > 512 || strings.ContainsAny(fileID, "\r\n\x00") {
		return nil, errors.New("Telegram image unavailable")
	}
	var file struct {
		Path string `json:"file_path"`
		Size int64  `json:"file_size"`
	}
	if c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &file) != nil || file.Size < 0 || file.Size > support.MaxImageBytes || len(file.Path) > 512 || strings.Contains(file.Path, "..") || strings.Contains(file.Path, "//") || !filePattern.MatchString(file.Path) {
		return nil, errors.New("Telegram image unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/file/bot"+c.token+"/"+file.Path, nil)
	if err != nil {
		return nil, errors.New("Telegram image unavailable")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("Telegram image unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > support.MaxImageBytes {
		return nil, errors.New("Telegram image unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, support.MaxImageBytes+1))
	if err != nil || len(body) > support.MaxImageBytes {
		return nil, errors.New("Telegram image unavailable")
	}
	image := &support.ImageInput{MIMEType: http.DetectContentType(body), DataBase64: base64.StdEncoding.EncodeToString(body)}
	if support.ValidateImage(image) != nil {
		return nil, errors.New("Telegram image unavailable")
	}
	return image, nil
}

func (c *Client) send(ctx context.Context, chatID int64, messageID, threadID int, text string) (int, error) {
	payload := map[string]any{"chat_id": chatID, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}}
	if messageID > 0 {
		payload["reply_parameters"] = map[string]any{"message_id": messageID, "allow_sending_without_reply": true}
	}
	if threadID > 0 {
		payload["message_thread_id"] = threadID
	}
	var result struct {
		ID int `json:"message_id"`
	}
	if err := c.call(ctx, "sendMessage", payload, &result); err != nil {
		return 0, err
	}
	if result.ID <= 0 {
		return 0, &APIError{}
	}
	return result.ID, nil
}
