package model

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const tokenSecretTestMasterKey = "token-secret-test-master-key-0123456789"

func TestTokenKeyCiphertextNeverLeavesTheServer(t *testing.T) {
	field, ok := reflect.TypeOf(Token{}).FieldByName("KeyCiphertext")
	if !ok {
		t.Fatal("Token is missing KeyCiphertext")
	}
	if got := field.Tag.Get("json"); got != "-" {
		t.Fatalf("KeyCiphertext json tag = %q, want %q", got, "-")
	}

	token := Token{KeyHash: "hash", KeyCiphertext: "ztapi-token:v1:sealed"}
	token.Clean()
	if token.KeyCiphertext != "" {
		t.Fatal("Clean must drop the ciphertext before a token is cached")
	}
}

func TestZTAPITokenKeyRoundTrip(t *testing.T) {
	t.Setenv(ztapiUpstreamMasterKeyEnv, tokenSecretTestMasterKey)
	plaintext, _, _, err := common.GenerateZTAPIKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	ciphertext, err := EncryptZTAPITokenKey(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(ciphertext, ztapiTokenKeyCiphertextPrefix) {
		t.Fatalf("ciphertext %q lacks the token key prefix", ciphertext)
	}
	if strings.Contains(ciphertext, plaintext) {
		t.Fatal("ciphertext contains the plaintext key")
	}
	again, err := EncryptZTAPITokenKey(plaintext)
	if err != nil {
		t.Fatalf("encrypt again: %v", err)
	}
	if again == ciphertext {
		t.Fatal("encrypting the same key twice must use a fresh nonce")
	}

	opened, err := DecryptZTAPITokenKey(ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if opened != plaintext {
		t.Fatalf("decrypted %q, want %q", opened, plaintext)
	}
}

func TestZTAPITokenKeyNeedsTheSameMasterKey(t *testing.T) {
	t.Setenv(ztapiUpstreamMasterKeyEnv, tokenSecretTestMasterKey)
	ciphertext, err := EncryptZTAPITokenKey("sk-zt-example")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	t.Setenv(ztapiUpstreamMasterKeyEnv, "another-master-key-0123456789-abcdef")
	if _, err := DecryptZTAPITokenKey(ciphertext); err == nil {
		t.Fatal("a different master key must not open the token key")
	}

	t.Setenv(ztapiUpstreamMasterKeyEnv, "")
	if _, err := EncryptZTAPITokenKey("sk-zt-example"); err == nil {
		t.Fatal("encryption must refuse to run without a master key")
	}
}

func TestZTAPITokenKeyIsSeparatedFromUpstreamCredentials(t *testing.T) {
	t.Setenv(ztapiUpstreamMasterKeyEnv, tokenSecretTestMasterKey)
	channelCiphertext, err := encryptZTAPIChannelKey("sk-upstream-secret")
	if err != nil {
		t.Fatalf("encrypt channel key: %v", err)
	}
	relabelled := ztapiTokenKeyCiphertextPrefix + strings.TrimPrefix(channelCiphertext, ztapiChannelKeyCiphertextPrefix)
	if _, err := DecryptZTAPITokenKey(relabelled); err == nil {
		t.Fatal("an upstream credential ciphertext must not open as a customer key")
	}
	if _, err := DecryptZTAPITokenKey(channelCiphertext); err == nil {
		t.Fatal("an upstream credential ciphertext must be rejected by format")
	}
}

func TestZTAPITokenKeyHashOnlyTokensAreNotRecoverable(t *testing.T) {
	t.Setenv(ztapiUpstreamMasterKeyEnv, tokenSecretTestMasterKey)
	if _, err := DecryptZTAPITokenKey(""); !errors.Is(err, ErrTokenKeyNotRecoverable) {
		t.Fatalf("decrypt of a hash-only token = %v, want ErrTokenKeyNotRecoverable", err)
	}
}

func TestRegenerateTokenKeyReplacesOnlyTheOwnersKey(t *testing.T) {
	db := openTokenKeyTestDB(t)
	if err := db.AutoMigrate(&Token{}); err != nil {
		t.Fatalf("migrate tokens: %v", err)
	}
	owned := Token{UserId: 7, Name: "owned", KeyHash: strings.Repeat("a", 64), KeyPrefix: "sk-zt-aaaaaa", RemainQuota: 500, UsedQuota: 20}
	other := Token{UserId: 8, Name: "other", KeyHash: strings.Repeat("b", 64), KeyPrefix: "sk-zt-bbbbbb"}
	for _, token := range []*Token{&owned, &other} {
		if err := token.Insert(); err != nil {
			t.Fatalf("insert %s: %v", token.Name, err)
		}
	}

	newHash := strings.Repeat("c", 64)
	if err := RegenerateTokenKey(owned.Id, 7, newHash, "sk-zt-cccccc", "ztapi-token:v1:sealed"); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if _, err := GetTokenByHash(owned.KeyHash, true); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("old key lookup = %v, want record not found", err)
	}
	replaced, err := GetTokenByHash(newHash, true)
	if err != nil {
		t.Fatalf("new key lookup: %v", err)
	}
	if replaced.Id != owned.Id || replaced.KeyPrefix != "sk-zt-cccccc" || replaced.KeyCiphertext != "ztapi-token:v1:sealed" {
		t.Fatalf("replaced token = %+v", replaced)
	}
	if replaced.RemainQuota != 500 || replaced.UsedQuota != 20 || replaced.Name != "owned" {
		t.Fatalf("regenerate must keep quota, usage and name: %+v", replaced)
	}

	err = RegenerateTokenKey(other.Id, 7, strings.Repeat("d", 64), "sk-zt-dddddd", "")
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("regenerating another user's token = %v, want record not found", err)
	}
	untouched, err := GetTokenByHash(other.KeyHash, true)
	if err != nil || untouched.KeyPrefix != "sk-zt-bbbbbb" {
		t.Fatalf("another user's token changed: %+v, %v", untouched, err)
	}
}
