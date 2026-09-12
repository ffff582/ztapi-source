package common

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const (
	ztAPIKeyPrefix  = "sk-zt-"
	ztAPIKeyRawSize = 32
)

func HashZTAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func IsValidZTAPIKey(plaintext string) bool {
	if !strings.HasPrefix(plaintext, ztAPIKeyPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(plaintext, ztAPIKeyPrefix)
	if len(encoded) != base64.RawURLEncoding.EncodedLen(ztAPIKeyRawSize) {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	return err == nil && len(raw) == ztAPIKeyRawSize
}

func GenerateZTAPIKey() (plaintext string, hash string, prefix string, err error) {
	raw := make([]byte, ztAPIKeyRawSize)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}

	plaintext = ztAPIKeyPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return plaintext, HashZTAPIKey(plaintext), plaintext[:12], nil
}
