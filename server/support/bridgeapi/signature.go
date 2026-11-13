// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package bridgeapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func Signature(secret []byte, timestamp, nonce, method, path string, raw []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestamp + "\n" + nonce + "\n" + method + "\n" + path + "\n"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}
