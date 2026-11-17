// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/support/bridgeapi"
)

// The fixture simulates the bot's claim, not website confirmation. Identity is
// fixed synthetic data derived from an authenticated fixture user, never input.
func (f *fixture) claimTelegram(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		sendJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}
	originOK := false
	for _, origin := range f.origins {
		originOK = originOK || origin == r.Header.Get("Origin")
	}
	if !originOK {
		sendJSON(w, http.StatusForbidden, nil)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		sendJSON(w, http.StatusUnauthorized, nil)
		return
	}
	claims, err := service.ParseZTAPIAccessToken(strings.TrimPrefix(auth, "Bearer "), f.now)
	if err != nil || claims.Subject != r.Header.Get("New-Api-User") {
		sendJSON(w, http.StatusUnauthorized, nil)
		return
	}
	id, err := strconv.Atoi(claims.Subject)
	if err != nil || id <= 0 {
		sendJSON(w, http.StatusUnauthorized, nil)
		return
	}
	if _, err = f.source.Identity(r.Context(), id); err != nil {
		sendJSON(w, http.StatusForbidden, nil)
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || common.DecodeJsonStrict(http.MaxBytesReader(w, r.Body, 4096), &input) != nil || input.Token == "" {
		sendJSON(w, http.StatusBadRequest, nil)
		return
	}
	raw, err := common.Marshal(bridgeapi.Request{Action: "claim_binding", TelegramID: int64(900000000 + id), Token: input.Token, Username: fmt.Sprintf("fixture_user_%d", id), DisplayName: "本地合成用户"})
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, nil)
		return
	}
	nonce, err := randomString()
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, nil)
		return
	}
	nonce = nonce[:32]
	stamp := strconv.FormatInt(f.now.Unix(), 10)
	claim := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+bridgeapi.Path, bytes.NewReader(raw)).WithContext(r.Context())
	claim.Header.Set("Content-Type", "application/json")
	claim.Header.Set(bridgeapi.HeaderTimestamp, stamp)
	claim.Header.Set(bridgeapi.HeaderNonce, nonce)
	claim.Header.Set(bridgeapi.HeaderSignature, bridgeapi.Signature(f.bridgeSecret[:], stamp, nonce, http.MethodPost, bridgeapi.Path, raw))
	response := httptest.NewRecorder()
	f.support.ServeHTTP(response, claim)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(response.Code)
	_, _ = w.Write(response.Body.Bytes())
}
