package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ztapiTokenKeyCiphertextPrefix = "ztapi-token:v1:"
	ztapiTokenKeyAAD              = "ztapi-token-key:v1"
)

// ErrTokenKeyNotRecoverable marks keys created before keys were stored
// encrypted: only their hash exists, so the plaintext cannot be shown again.
var ErrTokenKeyNotRecoverable = errors.New("token key was stored as a hash only and cannot be shown again")

// ztapiTokenAEAD derives its own key from the upstream master key so customer
// API keys and upstream credentials never share an encryption key.
func ztapiTokenAEAD() (cipher.AEAD, error) {
	master := os.Getenv(ztapiUpstreamMasterKeyEnv)
	if len(master) < ztapiUpstreamMasterKeyMinLength {
		return nil, fmt.Errorf("%s must contain at least %d characters", ztapiUpstreamMasterKeyEnv, ztapiUpstreamMasterKeyMinLength)
	}
	mac := hmac.New(sha256.New, []byte(master))
	mac.Write([]byte(ztapiTokenKeyAAD))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("initialize ZTAPI token key cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// EncryptZTAPITokenKey seals a customer API key so its owner can copy it later.
func EncryptZTAPITokenKey(plaintext string) (string, error) {
	if plaintext == "" {
		return "", errors.New("token key is empty")
	}
	aead, err := ztapiTokenAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate ZTAPI token key nonce: %w", err)
	}
	sealed := aead.Seal(nil, nonce, []byte(plaintext), []byte(ztapiTokenKeyAAD))
	payload := append(nonce, sealed...)
	return ztapiTokenKeyCiphertextPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecryptZTAPITokenKey opens a key sealed by EncryptZTAPITokenKey.
func DecryptZTAPITokenKey(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", ErrTokenKeyNotRecoverable
	}
	if !strings.HasPrefix(ciphertext, ztapiTokenKeyCiphertextPrefix) {
		return "", errors.New("unsupported ZTAPI token key format")
	}
	aead, err := ztapiTokenAEAD()
	if err != nil {
		return "", err
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ciphertext, ztapiTokenKeyCiphertextPrefix))
	if err != nil {
		return "", errors.New("invalid ZTAPI token key encoding")
	}
	if len(payload) <= aead.NonceSize() {
		return "", errors.New("invalid ZTAPI token key payload")
	}
	nonce, sealed := payload[:aead.NonceSize()], payload[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, sealed, []byte(ztapiTokenKeyAAD))
	if err != nil {
		return "", errors.New("cannot decrypt ZTAPI token key")
	}
	return string(plaintext), nil
}

// RegenerateTokenKey replaces a token's key in place. Quota, limits and usage
// stay with the token; the previous key stops authenticating immediately.
func RegenerateTokenKey(id int, userId int, keyHash string, keyPrefix string, keyCiphertext string) error {
	if id == 0 || userId == 0 {
		return errors.New("id 或 userId 为空！")
	}
	var previousHash string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var token Token
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "key_hash").
			Where("id = ? AND user_id = ?", id, userId).
			First(&token).Error; err != nil {
			return err
		}
		previousHash = token.KeyHash
		return tx.Model(&Token{}).Where("id = ? AND user_id = ?", id, userId).Updates(map[string]interface{}{
			"key_hash":       keyHash,
			"key_prefix":     keyPrefix,
			"key_ciphertext": keyCiphertext,
		}).Error
	})
	if err != nil {
		return err
	}
	if common.RedisEnabled && previousHash != "" {
		if err := cacheDeleteToken(previousHash); err != nil {
			common.SysLog("failed to delete replaced token key cache: " + err.Error())
		}
	}
	return nil
}
