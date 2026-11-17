// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package spending

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
)

type HandlerConfig struct {
	PublicOrigin      string
	Bindings          Bindings
	TelegramAvailable bool
	Now               func() time.Time
}
type handler struct {
	source Source
	store  *Store
	cfg    HandlerConfig
	slots  chan struct{}
}

func ValidOrigin(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery
}
func NewHandler(source Source, store *Store, cfg HandlerConfig) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &handler{source: source, store: store, cfg: cfg, slots: make(chan struct{}, 2)}
}
func respond(w http.ResponseWriter, status int, data any) {
	raw, err := common.Marshal(data)
	if err != nil {
		http.Error(w, "spending_unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func failure(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"success": false, "message": code})
}
func success(w http.ResponseWriter, data any) {
	respond(w, 200, map[string]any{"success": true, "data": data})
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/spending/") {
		failure(w, 404, "spending_not_found")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		failure(w, 429, "spending_busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || parts[0] != "Bearer" {
		failure(w, 401, "authentication_required")
		return
	}
	claims, err := service.ParseZTAPIAccessToken(parts[1], h.cfg.Now())
	if err != nil {
		failure(w, 401, "invalid_access_token")
		return
	}
	owner, err := strconv.Atoi(claims.Subject)
	if err != nil || owner <= 0 || r.Header.Get("New-Api-User") != claims.Subject {
		failure(w, 401, "identity_mismatch")
		return
	}
	user, err := h.source.Identity(ctx, owner)
	if err != nil {
		failure(w, 503, "spending_identity_unavailable")
		return
	}
	if user.ID != owner || user.Status != 1 || user.DeletedAt != nil {
		failure(w, 403, "spending_account_disabled")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if !ValidOrigin(h.cfg.PublicOrigin) || r.Header.Get("Origin") != h.cfg.PublicOrigin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			failure(w, 403, "spending_origin_denied")
			return
		}
	}
	switch {
	case r.URL.Path == "/api/spending/config" && r.Method == "GET":
		p, e := h.store.Preferences(ctx, owner)
		if e != nil {
			failure(w, 503, "spending_store_unavailable")
			return
		}
		bound := false
		available := h.cfg.TelegramAvailable && h.cfg.Bindings != nil
		if h.cfg.Bindings != nil {
			b, e := h.cfg.Bindings.Lookup(ctx, owner)
			bound = e == nil && b.TelegramID > 0 && b.Generation != ""
			if e != nil && !errors.Is(e, ErrNotFound) {
				available = false
			}
		}
		success(w, map[string]any{"preferences": p, "telegram_bound": bound, "telegram_available": available, "timezone": "Asia/Shanghai"})
	case r.URL.Path == "/api/spending/config" && r.Method == "PUT":
		var p Preferences
		raw, ok := decode(w, r, &p)
		if !ok {
			return
		}
		var fields map[string]any
		if common.Unmarshal(raw, &fields) != nil || len(fields) != 7 {
			failure(w, 400, "invalid_spending_payload")
			return
		}
		for _, key := range []string{"enabled", "daily_spend_u", "hourly_spend_u", "low_balance_u", "input_tokens", "telegram", "version"} {
			if value, present := fields[key]; !present || value == nil {
				failure(w, 400, "invalid_spending_payload")
				return
			}
		}
		if p.Enabled && p.Telegram {
			if !h.cfg.TelegramAvailable || h.cfg.Bindings == nil {
				failure(w, 409, "spending_telegram_unavailable")
				return
			}
			binding, e := h.cfg.Bindings.Lookup(ctx, owner)
			if e != nil || binding.TelegramID <= 0 || binding.Generation == "" {
				failure(w, 409, "spending_telegram_binding_required")
				return
			}
		}
		p, e := h.store.SavePreferences(ctx, owner, p, h.cfg.Now())
		if e != nil {
			switch {
			case errors.Is(e, ErrConflict):
				failure(w, 409, "spending_version_conflict")
			case errors.Is(e, ErrInvalid):
				failure(w, 400, "invalid_spending_preferences")
			default:
				failure(w, 503, "spending_store_unavailable")
			}
			return
		}
		success(w, p)
	case r.URL.Path == "/api/spending/overview" && r.Method == "GET":
		now := h.cfg.Now()
		snapshot, e := h.source.Load(ctx, owner, now)
		if e != nil {
			snapshot.Overview = emptyOverview(now)
		}
		success(w, snapshot.Overview)
	case r.URL.Path == "/api/spending/alerts" && r.Method == "GET":
		limit := 20
		before := int64(0)
		if value, ok := r.URL.Query()["limit"]; ok {
			if len(value) != 1 {
				failure(w, 400, "invalid_spending_cursor")
				return
			}
			n, e := strconv.Atoi(value[0])
			if e != nil || n < 1 || n > 100 {
				failure(w, 400, "invalid_spending_cursor")
				return
			}
			limit = n
		}
		if value, ok := r.URL.Query()["before"]; ok {
			if len(value) != 1 {
				failure(w, 400, "invalid_spending_cursor")
				return
			}
			n, e := strconv.ParseInt(value[0], 10, 64)
			if e != nil || n <= 0 {
				failure(w, 400, "invalid_spending_cursor")
				return
			}
			before = n
		}
		rows, count, e := h.store.Alerts(ctx, owner, limit, before)
		if e != nil {
			failure(w, 503, "spending_store_unavailable")
			return
		}
		success(w, map[string]any{"items": rows, "unread_count": count})
	case strings.HasPrefix(r.URL.Path, "/api/spending/alerts/") && strings.HasSuffix(r.URL.Path, "/read") && r.Method == "POST":
		id, e := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/spending/alerts/"), "/read"), 10, 64)
		if e != nil || id <= 0 {
			failure(w, 404, "spending_not_found")
			return
		}
		var in struct{}
		if _, ok := decode(w, r, &in); !ok {
			return
		}
		e = h.store.ReadAlert(ctx, owner, id, h.cfg.Now())
		if e != nil {
			if errors.Is(e, ErrNotFound) {
				failure(w, 404, "spending_not_found")
			} else {
				failure(w, 503, "spending_store_unavailable")
			}
			return
		}
		success(w, map[string]bool{"read": true})
	default:
		failure(w, 404, "spending_not_found")
	}
}
func decode(w http.ResponseWriter, r *http.Request, dest any) ([]byte, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 415, "json_required")
		return nil, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		failure(w, 413, "spending_payload_too_large")
		return nil, false
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), dest) != nil {
		failure(w, 400, "invalid_spending_payload")
		return nil, false
	}
	return raw, true
}
