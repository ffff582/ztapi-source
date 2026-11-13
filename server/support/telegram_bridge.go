// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support/bridgeapi"
	"gorm.io/gorm"
)

func (b *TelegramBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != "POST" || r.URL.Path != bridgeapi.Path || r.URL.RawQuery != "" {
		fail(w, 404, "bridge_route_not_found")
		return
	}
	if r.Header.Get("Origin") != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 401, "bridge_authentication_required")
		return
	}
	stamp, nonce, signature := r.Header.Get(bridgeapi.HeaderTimestamp), r.Header.Get(bridgeapi.HeaderNonce), r.Header.Get(bridgeapi.HeaderSignature)
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || strconv.FormatInt(seconds, 10) != stamp || seconds < b.now().Unix()-90 || seconds > b.now().Unix()+90 || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(nonce) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(signature) {
		fail(w, 401, "bridge_authentication_required")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if err != nil {
		fail(w, 413, "bridge_payload_too_large")
		return
	}
	expected := bridgeapi.Signature(b.cfg.Secret, stamp, nonce, r.Method, r.URL.Path, raw)
	got, _ := hex.DecodeString(signature)
	want, _ := hex.DecodeString(expected)
	if !hmac.Equal(got, want) {
		fail(w, 401, "bridge_authentication_required")
		return
	}
	var in bridgeapi.Request
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), &in) != nil || !validBridgeRequest(in) {
		fail(w, 400, "invalid_bridge_payload")
		return
	}
	if b.cfg.AdmitWrite != nil {
		if b.cfg.AdmitWrite() != nil {
			fail(w, 503, "bridge_storage_unavailable")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	b.mu.Lock()
	err = b.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if e := tx.Model(&bridgeNonce{}).Where("id = ?", nonce).Count(&count).Error; e != nil {
			return e
		}
		if count != 0 {
			return ErrConflict
		}
		if e := tx.Where("created_at < ?", b.now().Add(-5*time.Minute)).Delete(&bridgeNonce{}).Error; e != nil {
			return e
		}
		return tx.Create(&bridgeNonce{ID: nonce, CreatedAt: b.now()}).Error
	})
	b.mu.Unlock()
	if errors.Is(err, ErrConflict) {
		fail(w, 401, "bridge_replay_denied")
		return
	}
	if err != nil {
		fail(w, 503, "bridge_storage_unavailable")
		return
	}
	out, err := b.Execute(ctx, in)
	if err != nil {
		storeFailure(w, err)
		return
	}
	send(w, 200, out)
}

func validBridgeRequest(in bridgeapi.Request) bool {
	if in.TelegramID < 0 || len(in.Token) > 64 || len(in.RequestID) > 128 || len(in.EventID) > 64 || !boundedText(in.Content, 4000) || !boundedText(in.DisplayName, 128) || len(in.Username) > 64 {
		return false
	}
	switch in.Action {
	case "claim_binding":
		return in.TelegramID > 0 && in.Token != "" && in.RequestID == "" && in.Content == "" && in.EventID == "" && in.IdempotencyKey == ""
	case "binding_status", "recent_requests":
		return in.TelegramID > 0 && in.Token == "" && in.Username == "" && in.DisplayName == "" && in.RequestID == "" && in.Content == "" && in.EventID == "" && in.IdempotencyKey == ""
	case "diagnose":
		return in.TelegramID > 0 && in.Token == "" && in.Username == "" && in.DisplayName == "" && in.RequestID != "" && in.Content == "" && in.EventID == "" && in.IdempotencyKey == ""
	case "create_ticket":
		return in.TelegramID > 0 && in.Token == "" && in.Username == "" && in.DisplayName == "" && strings.TrimSpace(in.Content) != "" && validKey(in.IdempotencyKey) && in.EventID == ""
	case "poll_notifications":
		return in.TelegramID == 0 && in.Token == "" && in.Username == "" && in.DisplayName == "" && in.RequestID == "" && in.Content == "" && in.EventID == "" && in.IdempotencyKey == ""
	case "authorize_notification", "ack_notification":
		return in.TelegramID > 0 && in.EventID != "" && in.Token == "" && in.Username == "" && in.DisplayName == "" && in.RequestID == "" && in.Content == "" && in.IdempotencyKey == ""
	default:
		return false
	}
}

func (b *TelegramBridge) Execute(ctx context.Context, in bridgeapi.Request) (bridgeapi.Result, error) {
	out := bridgeapi.Result{}
	if !validBridgeRequest(in) {
		return out, ErrConflict
	}
	switch in.Action {
	case "claim_binding":
		expires, err := b.claimBinding(ctx, in.TelegramID, in.Token, in.Username, in.DisplayName)
		out.Pending = err == nil
		out.ExpiresAt = &expires
		return out, err
	case "poll_notifications":
		rows, err := b.notifications(ctx)
		out.Notifications = rows
		return out, err
	case "authorize_notification", "ack_notification":
		ok, err := b.notificationAccess(ctx, in.TelegramID, in.EventID, in.Action == "ack_notification")
		out.Allowed = ok
		return out, err
	}
	link, err := b.bindingForTelegram(ctx, in.TelegramID)
	if in.Action == "binding_status" && errors.Is(err, ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Bound = true
	out.BindingGeneration = link.Generation
	switch in.Action {
	case "binding_status":
		return out, nil
	case "recent_requests":
		rows, err := b.source.RecentRequests(ctx, link.UserID)
		if err != nil {
			return out, err
		}
		if len(rows) > 5 {
			rows = rows[:5]
		}
		for _, row := range rows {
			out.Requests = append(out.Requests, bridgeapi.Diagnosis(row))
		}
		return out, nil
	case "diagnose":
		d, err := b.source.Diagnose(ctx, link.UserID, in.RequestID)
		if err != nil {
			return out, err
		}
		value := bridgeapi.Diagnosis(d)
		out.Diagnosis = &value
		return out, nil
	case "create_ticket":
		if in.RequestID != "" {
			if _, err = b.source.Diagnose(ctx, link.UserID, in.RequestID); err != nil {
				return out, err
			}
		}
		conv, err := b.store.CreateConversation(ctx, link.UserID, "tg-case:"+in.IdempotencyKey, b.now())
		if err != nil {
			return out, err
		}
		ticket, err := b.store.CreateTicket(ctx, link.UserID, TicketInput{ConversationID: conv.ID, RequestID: in.RequestID, Subject: "Telegram 客服工单", Content: Redact(in.Content), Contact: "Telegram（已核验绑定）", IdempotencyKey: "tg-case:" + in.IdempotencyKey}, b.now())
		if err != nil {
			return out, err
		}
		out.Ticket = &bridgeapi.Ticket{ID: ticket.ID, Status: ticket.Status, ConversationID: ticket.ConversationID}
		return out, nil
	}
	return out, ErrConflict
}
