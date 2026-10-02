package model

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openTokenKeyTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	initCol()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

func TestTokenPersistenceContractHasHashAndPrefixButNoPlaintextKey(t *testing.T) {
	tokenType := reflect.TypeOf(Token{})

	if _, ok := tokenType.FieldByName("Key"); ok {
		t.Fatal("Token still has a persistent plaintext Key field")
	}

	keyHash, ok := tokenType.FieldByName("KeyHash")
	if !ok {
		t.Fatal("Token is missing KeyHash")
	}
	if got := keyHash.Tag.Get("json"); got != "-" {
		t.Fatalf("KeyHash json tag = %q, want %q", got, "-")
	}
	if got := keyHash.Tag.Get("gorm"); got != "column:key_hash;type:char(64);uniqueIndex" {
		t.Fatalf("KeyHash gorm tag = %q", got)
	}

	keyPrefix, ok := tokenType.FieldByName("KeyPrefix")
	if !ok {
		t.Fatal("Token is missing KeyPrefix")
	}
	if got := keyPrefix.Tag.Get("json"); got != "key_prefix" {
		t.Fatalf("KeyPrefix json tag = %q, want %q", got, "key_prefix")
	}
	if got := keyPrefix.Tag.Get("gorm"); got != "column:key_prefix;type:varchar(20);index" {
		t.Fatalf("KeyPrefix gorm tag = %q", got)
	}
}

func TestValidateUserTokenHashesFullPlaintextForLookup(t *testing.T) {
	db := openTokenKeyTestDB(t)
	if err := db.AutoMigrate(&Token{}); err != nil {
		t.Fatalf("migrate token schema: %v", err)
	}
	if !db.Migrator().HasColumn("tokens", "key_hash") {
		if err := db.Exec("ALTER TABLE `tokens` ADD COLUMN `key_hash` char(64)").Error; err != nil {
			t.Fatalf("add test key_hash column: %v", err)
		}
	}
	if !db.Migrator().HasColumn("tokens", "key_prefix") {
		if err := db.Exec("ALTER TABLE `tokens` ADD COLUMN `key_prefix` varchar(20)").Error; err != nil {
			t.Fatalf("add test key_prefix column: %v", err)
		}
	}

	const plaintext = "sk-zt-abcdefghijklmnopqrstuvwxyz0123456789ABCDE"
	row := map[string]any{
		"user_id":           7,
		"name":              "hash-lookup",
		"key_hash":          common.HashZTAPIKey(plaintext),
		"key_prefix":        plaintext[:12],
		"status":            common.TokenStatusEnabled,
		"created_time":      1,
		"accessed_time":     1,
		"expired_time":      -1,
		"remain_quota":      0,
		"unlimited_quota":   true,
		"model_limits":      "",
		"used_quota":        0,
		"group":             "default",
		"cross_group_retry": false,
	}
	if err := db.Table("tokens").Create(row).Error; err != nil {
		t.Fatalf("seed hashed token: %v", err)
	}

	token, err := ValidateUserToken(plaintext)
	if err != nil {
		t.Fatalf("ValidateUserToken returned error: %v", err)
	}
	if token.Id == 0 || token.UserId != 7 {
		t.Fatalf("unexpected token loaded: %#v", token)
	}

	serialized, err := common.Marshal(token)
	if err != nil {
		t.Fatalf("marshal token: %v", err)
	}
	if strings.Contains(string(serialized), plaintext) {
		t.Fatalf("serialized token leaked plaintext: %s", serialized)
	}
	if strings.Contains(string(serialized), common.HashZTAPIKey(plaintext)) {
		t.Fatalf("serialized token leaked key hash: %s", serialized)
	}
	if !strings.Contains(string(serialized), plaintext[:12]) {
		t.Fatalf("serialized token omitted display prefix: %s", serialized)
	}
}

func TestPrepareTokenCacheEntryOmitsPlaintextAndStoredHash(t *testing.T) {
	const plaintext = "sk-zt-cache-contract-plaintext"
	keyHash := common.HashZTAPIKey(plaintext)
	token := Token{
		Id:        9,
		UserId:    4,
		KeyHash:   keyHash,
		KeyPrefix: plaintext[:12],
		Name:      "cache-contract",
	}

	cacheKey, cachedToken := prepareTokenCacheEntry(token)
	if strings.Contains(cacheKey, plaintext) {
		t.Fatalf("cache key leaked plaintext: %q", cacheKey)
	}
	if strings.Contains(cacheKey, keyHash) {
		t.Fatalf("cache key leaked stored hash: %q", cacheKey)
	}
	if cachedToken.KeyHash != "" {
		t.Fatalf("cached token retained stored hash: %q", cachedToken.KeyHash)
	}

	payload, err := common.Marshal(cachedToken)
	if err != nil {
		t.Fatalf("marshal cached token: %v", err)
	}
	if strings.Contains(string(payload), plaintext) || strings.Contains(string(payload), keyHash) {
		t.Fatalf("cache payload leaked key material: %s", payload)
	}
}
