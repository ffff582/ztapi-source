package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var ztapiTelegramTokenPattern = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{10,128}$`)
var ztapiTelegramChatPattern = regexp.MustCompile(`^-?[1-9][0-9]{0,19}$`)
var ztapiHealthRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,255}$`)

func validZTAPIHealthAlertRecipient(c ZTAPIHealthWorkerConfig) bool {
	if c.TelegramConfigured || c.TelegramBotToken != "" || c.TelegramChatID != "" {
		return ztapiTelegramTokenPattern.MatchString(c.TelegramBotToken) && ztapiTelegramChatPattern.MatchString(c.TelegramChatID)
	}
	return validZTAPIHealthWebhook(c.AlertWebhookURL)
}

func ztapiHealthSafeRequestID(id string) string {
	if !ztapiHealthRequestIDPattern.MatchString(id) || strings.HasPrefix(id, "sk-") {
		return "unknown"
	}
	return id
}

func ztapiHealthTelegramText(item ZTAPIHealthWorkItem, now time.Time) string {
	d := ztapiHealthSafeAlertMetadata(item, now)
	trigger := "unknown"
	if d.OpenedAt != nil && *d.OpenedAt > 0 {
		trigger = time.Unix(*d.OpenedAt, 0).UTC().Format(time.RFC3339)
	}
	rule := "unknown"
	switch d.Rule {
	case "consecutive_2":
		rule = "consecutive_2: 2 consecutive failures"
	case "rolling_24h_gt_2pct":
		rule = "rolling_24h_gt_2pct: at least 3 failures in 24h AND failure rate >2%"
	}
	title := "ZTAPI MODEL CIRCUIT OPEN"
	if item.Test {
		title = "ZTAPI TEST ALERT - NO MODEL WAS CHANGED"
		rule = "TEST ONLY; live rules: consecutive_2 OR (24h >=3 failures AND >2%)"
	}
	status := "unknown"
	if d.HTTPStatus != nil && *d.HTTPStatus >= 100 && *d.HTTPStatus <= 599 {
		status = fmt.Sprintf("%d", *d.HTTPStatus)
	}
	latency := "unknown"
	if d.LatencyMilliseconds != nil {
		latency = fmt.Sprintf("%d", *d.LatencyMilliseconds)
	}
	resultValid := "unknown"
	if d.ResultValid != nil {
		resultValid = fmt.Sprintf("%t", *d.ResultValid)
	}
	return fmt.Sprintf("%s\nmodel: %s\nmodality: %s\noperation: %s\ncondition: %s\nerror_code: %s\nhttp_status: %s\nfinish_reason: %s\nupstream_request_id: %s\nupstream_task_id: %s\nlatency_ms: %s\nresult_valid: %s\ntrigger_time: %s\nincident_id: %d\noutbox_id: %d\nRecovery requires manual confirmation.", title, d.Model, d.Modality, d.Operation, rule, d.ErrorCode, status, strings.Join(d.FinishReasons, ","), d.UpstreamRequestID, d.UpstreamTaskID, latency, resultValid, trigger, item.IncidentID, item.ID)
}

// Only this fixed Telegram origin receives the bot credential. Never log the
// request URL or raw HTTP errors, which include the credential in the path.
func sendZTAPIHealthTelegram(ctx context.Context, c ZTAPIHealthWorkerConfig, item ZTAPIHealthWorkItem) (bool, string, string) {
	return sendZTAPITelegramText(ctx, c, ztapiHealthTelegramText(item, c.Now()))
}

func sendZTAPITelegramText(ctx context.Context, c ZTAPIHealthWorkerConfig, text string) (bool, string, string) {
	if !ztapiTelegramTokenPattern.MatchString(c.TelegramBotToken) || !ztapiTelegramChatPattern.MatchString(c.TelegramChatID) {
		return false, "alert_recipient_missing_or_invalid", ""
	}
	body, err := common.Marshal(map[string]any{"chat_id": c.TelegramChatID, "text": text})
	if err != nil {
		return false, "alert_payload_error", ""
	}
	ctx, cancel := context.WithTimeout(ctx, c.AlertTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.TelegramBotToken+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return false, "alert_request_invalid", ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ztapiHealthHTTPClient(c.AlertTimeout, c.AlertTransport).Do(req)
	if err != nil {
		return false, "alert_transport_error", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "alert_http_status", ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return false, "alert_response_invalid", ""
	}
	// Persist only an allowlisted API receipt, not chat identity, message text,
	// Telegram's error description, or arbitrary upstream response fields.
	var receipt struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
			Date      int64 `json:"date"`
		} `json:"result"`
	}
	if common.Unmarshal(raw, &receipt) != nil || !receipt.OK || receipt.Result.MessageID <= 0 || receipt.Result.Date < 0 {
		return false, "alert_telegram_rejected", ""
	}
	safe, err := common.Marshal(receipt)
	if err != nil {
		return false, "alert_response_invalid", ""
	}
	return true, "telegram_accepted", string(safe)
}
