package model

import (
	"crypto/aes"
	"crypto/cipher"
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
)

const (
	ztapiUpstreamMasterKeyEnv       = "ZTAPI_UPSTREAM_MASTER_KEY"
	ztapiChannelKeyCiphertextPrefix = "ztapi-key:v1:"
	ztapiChannelKeyAAD              = "ztapi-channel-key:v1"
	ztapiUpstreamMasterKeyMinLength = 32
)

func ztapiUpstreamAEAD() (cipher.AEAD, error) {
	master := os.Getenv(ztapiUpstreamMasterKeyEnv)
	if len(master) < ztapiUpstreamMasterKeyMinLength {
		return nil, fmt.Errorf("%s must contain at least %d characters", ztapiUpstreamMasterKeyEnv, ztapiUpstreamMasterKeyMinLength)
	}
	key := sha256.Sum256([]byte(master))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize ZTAPI upstream credential cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func encryptZTAPIChannelKey(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	aead, err := ztapiUpstreamAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate ZTAPI upstream credential nonce: %w", err)
	}
	sealed := aead.Seal(nil, nonce, []byte(plaintext), []byte(ztapiChannelKeyAAD))
	payload := append(nonce, sealed...)
	return ztapiChannelKeyCiphertextPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

func decryptZTAPIChannelKey(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	if !strings.HasPrefix(ciphertext, ztapiChannelKeyCiphertextPrefix) {
		return "", errors.New("unsupported ZTAPI upstream credential format")
	}
	aead, err := ztapiUpstreamAEAD()
	if err != nil {
		return "", err
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ciphertext, ztapiChannelKeyCiphertextPrefix))
	if err != nil {
		return "", errors.New("invalid ZTAPI upstream credential encoding")
	}
	if len(payload) <= aead.NonceSize() {
		return "", errors.New("invalid ZTAPI upstream credential payload")
	}
	nonce, sealed := payload[:aead.NonceSize()], payload[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, sealed, []byte(ztapiChannelKeyAAD))
	if err != nil {
		return "", errors.New("cannot decrypt ZTAPI upstream credential")
	}
	return string(plaintext), nil
}

func ztapiStatementIncludesKey(tx *gorm.DB) bool {
	if tx == nil || tx.Statement == nil {
		return true
	}
	for _, omitted := range tx.Statement.Omits {
		name := strings.ToLower(strings.Trim(strings.TrimSpace(omitted), "`\""))
		if name == "key" || strings.HasSuffix(name, ".key") {
			return false
		}
	}
	if len(tx.Statement.Selects) == 0 {
		return true
	}
	for _, selected := range tx.Statement.Selects {
		name := strings.ToLower(strings.Trim(strings.TrimSpace(selected), "`\""))
		if name == "*" || name == "key" || strings.HasSuffix(name, ".key") {
			return true
		}
	}
	return false
}

func (channel *Channel) BeforeSave(_ *gorm.DB) error {
	if channel == nil || !channel.ZTAPIManaged {
		return nil
	}
	if channel.Status == common.ChannelStatusEnabled && channel.Key == "" && channel.ZTAPIKeyCiphertext == "" {
		return errors.New("enabled ZTAPI channel requires an upstream credential")
	}
	if channel.Key == "" {
		return nil
	}
	ciphertext, err := encryptZTAPIChannelKey(channel.Key)
	if err != nil {
		return err
	}
	channel.ZTAPIKeyCiphertext = ciphertext
	channel.Key = ""
	return nil
}

func (channel *Channel) AfterFind(tx *gorm.DB) error {
	if channel == nil || !channel.ZTAPIManaged || channel.ZTAPIKeyCiphertext == "" || !ztapiStatementIncludesKey(tx) {
		return nil
	}
	plaintext, err := decryptZTAPIChannelKey(channel.ZTAPIKeyCiphertext)
	if err != nil {
		return err
	}
	channel.Key = plaintext
	return nil
}

type ztapiChannelSecretMigrationRow struct {
	ID                 int    `gorm:"column:id"`
	Key                string `gorm:"column:key"`
	ZTAPIKeyCiphertext string `gorm:"column:ztapi_key_ciphertext"`
}

func migrateZTAPIChannelSecretsDB(db *gorm.DB) error {
	if db == nil {
		return errors.New("ZTAPI database is not initialized")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var rows []ztapiChannelSecretMigrationRow
		if err := tx.Table("channels").
			Select("id", "key", "ztapi_key_ciphertext").
			Where("ztapi_managed = ?", true).
			Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			ciphertext := row.ZTAPIKeyCiphertext
			if ciphertext != "" {
				if _, err := decryptZTAPIChannelKey(ciphertext); err != nil {
					return fmt.Errorf("validate encrypted ZTAPI channel %d credential: %w", row.ID, err)
				}
			} else if row.Key != "" {
				var err error
				ciphertext, err = encryptZTAPIChannelKey(row.Key)
				if err != nil {
					return fmt.Errorf("migrate ZTAPI channel %d credential: %w", row.ID, err)
				}
			}
			if row.Key == "" && ciphertext == row.ZTAPIKeyCiphertext {
				continue
			}
			if err := tx.Table("channels").Where("id = ?", row.ID).Updates(map[string]any{
				"key": "", "ztapi_key_ciphertext": ciphertext,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func migrateZTAPIChannelSecrets() error {
	return migrateZTAPIChannelSecretsDB(DB)
}
