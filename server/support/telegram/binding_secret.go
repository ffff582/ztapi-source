// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

func (w *Worker) bindingCipher() (cipher.AEAD, error) {
	if len(w.cfg.BindingKey) != 32 {
		return nil, errors.New("Telegram secure binding unavailable")
	}
	block, err := aes.NewCipher(w.cfg.BindingKey)
	if err != nil {
		return nil, errors.New("Telegram secure binding unavailable")
	}
	return cipher.NewGCM(block)
}
func (w *Worker) bindingAAD(row inbox) []byte {
	return []byte(fmt.Sprintf("tg-binding:%d:%d:%d:%d", w.cfg.BotID, row.UpdateID, row.SenderID, row.ChatID))
}
func (w *Worker) sealBinding(row inbox, token string) (string, error) {
	aead, err := w.bindingCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", errors.New("Telegram secure binding unavailable")
	}
	sealed := aead.Seal(nonce, nonce, []byte(token), w.bindingAAD(row))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}
func (w *Worker) openBinding(row inbox) (string, error) {
	aead, err := w.bindingCipher()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(row.BindToken)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() || len(raw) > 256 {
		return "", errors.New("Telegram secure binding unavailable")
	}
	token, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], w.bindingAAD(row))
	if err != nil || !bindingToken.Match(token) {
		return "", errors.New("Telegram secure binding unavailable")
	}
	return string(token), nil
}
