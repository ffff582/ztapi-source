package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const testSessionSigningKey = "0123456789abcdef0123456789abcdef"

func TestSessionSigningKeyValidationRejectsMissingAndShortValues(t *testing.T) {
	if err := ConfigureZTAPISessionSigningKey(""); err == nil {
		t.Fatal("missing signing key was accepted")
	}
	if err := ConfigureZTAPISessionSigningKey(strings.Repeat("a", 31)); err == nil {
		t.Fatal("31-byte signing key was accepted")
	}
	if err := ConfigureZTAPISessionSigningKey(testSessionSigningKey); err != nil {
		t.Fatalf("32-byte signing key was rejected: %v", err)
	}

	original, existed := os.LookupEnv("ZTAPI_SESSION_SIGNING_KEY")
	legacyEnvName := strings.Join(
		[]string{"GAN", "API", "SESSION", "SIGNING", "KEY"},
		"_",
	)
	legacy, legacyExisted := os.LookupEnv(legacyEnvName)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("ZTAPI_SESSION_SIGNING_KEY", original)
		} else {
			_ = os.Unsetenv("ZTAPI_SESSION_SIGNING_KEY")
		}
		if legacyExisted {
			_ = os.Setenv(legacyEnvName, legacy)
		} else {
			_ = os.Unsetenv(legacyEnvName)
		}
	})
	_ = os.Unsetenv("ZTAPI_SESSION_SIGNING_KEY")
	_ = os.Setenv(legacyEnvName, testSessionSigningKey)
	if err := ConfigureZTAPISessionSigningKeyFromEnv(); err == nil {
		t.Fatal("startup configuration accepted the legacy signing-key variable")
	}
	_ = os.Setenv("ZTAPI_SESSION_SIGNING_KEY", strings.Repeat("b", 31))
	if err := ConfigureZTAPISessionSigningKeyFromEnv(); err == nil {
		t.Fatal("startup configuration accepted a short signing key")
	}
	_ = os.Setenv("ZTAPI_SESSION_SIGNING_KEY", testSessionSigningKey)
	if err := ConfigureZTAPISessionSigningKeyFromEnv(); err != nil {
		t.Fatalf("startup configuration rejected a valid signing key: %v", err)
	}
}

func TestSessionAccessTokenUsesHS256RequiredClaimsAndFifteenMinuteExpiry(t *testing.T) {
	configureSessionTestKey(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)

	first, err := IssueZTAPIAccessToken(42, now)
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	second, err := IssueZTAPIAccessToken(42, now)
	if err != nil {
		t.Fatalf("issue second access token: %v", err)
	}
	if first == second {
		t.Fatal("two access tokens reused the same JWT ID")
	}

	claims, err := ParseZTAPIAccessToken(first, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	if claims.Issuer != "ztapi" {
		t.Fatalf("issuer = %q, want ztapi", claims.Issuer)
	}
	if claims.Subject != "42" {
		t.Fatalf("subject = %q, want 42", claims.Subject)
	}
	if claims.ID == "" {
		t.Fatal("JWT ID is empty")
	}
	if claims.IssuedAt == nil || !claims.IssuedAt.Time.Equal(now) {
		t.Fatalf("issued at = %v, want %v", claims.IssuedAt, now)
	}
	if claims.ExpiresAt == nil || claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time) != 15*time.Minute {
		t.Fatalf("access lifetime = %v, want 15m", claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time))
	}
	if _, err := ParseZTAPIAccessToken(first, now.Add(15*time.Minute)); err == nil {
		t.Fatal("expired access token was accepted")
	}
}

func TestSessionAccessTokenRejectsWrongAlgorithmSignatureAndIssuer(t *testing.T) {
	configureSessionTestKey(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	claims := ZTAPIAccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "ztapi",
			Subject:   "42",
			ID:        "test-jti",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
		},
	}

	noneToken, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none token: %v", err)
	}
	if _, err := ParseZTAPIAccessToken(noneToken, now); err == nil {
		t.Fatal("none algorithm was accepted")
	}

	hs384Token, err := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString([]byte(testSessionSigningKey))
	if err != nil {
		t.Fatalf("sign HS384 token: %v", err)
	}
	if _, err := ParseZTAPIAccessToken(hs384Token, now); err == nil {
		t.Fatal("HS384 algorithm was accepted")
	}

	wrongSignature, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatalf("sign wrong-key token: %v", err)
	}
	if _, err := ParseZTAPIAccessToken(wrongSignature, now); err == nil {
		t.Fatal("wrong signing key was accepted")
	}

	claims.Issuer = "not-ztapi"
	wrongIssuer, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSessionSigningKey))
	if err != nil {
		t.Fatalf("sign wrong-issuer token: %v", err)
	}
	if _, err := ParseZTAPIAccessToken(wrongIssuer, now); err == nil {
		t.Fatal("wrong issuer was accepted")
	}
}

func TestSessionCreationStoresOnlyRefreshHashForThirtyDays(t *testing.T) {
	db, user := setupSessionTestDB(t)
	configureSessionTestKey(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)

	tokens, err := CreateZTAPISession(user.Id, "192.0.2.10", "ZTAPI Test Agent", now)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatal("session did not return both tokens")
	}
	if tokens.ExpiresIn != 900 {
		t.Fatalf("expires_in = %d, want 900", tokens.ExpiresIn)
	}

	var stored model.AuthSession
	if err := db.First(&stored).Error; err != nil {
		t.Fatalf("load stored session: %v", err)
	}
	wantTokenHash := sha256HexForTest(tokens.RefreshToken)
	if stored.TokenHash != wantTokenHash {
		t.Fatalf("stored token hash = %q, want SHA-256 digest", stored.TokenHash)
	}
	if stored.TokenHash == tokens.RefreshToken {
		t.Fatal("plaintext refresh token was stored")
	}
	if stored.ExpiresAt.Sub(stored.CreatedAt) != 30*24*time.Hour {
		t.Fatalf("refresh lifetime = %v, want 720h", stored.ExpiresAt.Sub(stored.CreatedAt))
	}
	if stored.CreationIP != "192.0.2.10" {
		t.Fatalf("creation IP = %q", stored.CreationIP)
	}
	if stored.UserAgentHash != sha256HexForTest("ZTAPI Test Agent") {
		t.Fatalf("user-agent hash = %q, want SHA-256 digest", stored.UserAgentHash)
	}

	columns, err := db.Migrator().ColumnTypes(&model.AuthSession{})
	if err != nil {
		t.Fatalf("inspect auth session columns: %v", err)
	}
	for _, column := range columns {
		switch column.Name() {
		case "refresh_token", "token", "plaintext_token":
			t.Fatalf("plaintext-capable session column exists: %s", column.Name())
		}
	}
}

func TestSessionRotationInvalidatesOldTokenAndReplayRevokesFamily(t *testing.T) {
	db, user := setupSessionTestDB(t)
	configureSessionTestKey(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)

	initial, err := CreateZTAPISession(user.Id, "192.0.2.10", "initial-agent", now)
	if err != nil {
		t.Fatalf("create initial session: %v", err)
	}
	replacement, err := RotateZTAPISession(initial.RefreshToken, "192.0.2.11", "replacement-agent", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("rotate session: %v", err)
	}
	if replacement.RefreshToken == initial.RefreshToken {
		t.Fatal("rotation returned the presented refresh token")
	}

	var sessions []model.AuthSession
	if err := db.Order("id").Find(&sessions).Error; err != nil {
		t.Fatalf("load session family: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2", len(sessions))
	}
	if sessions[0].UsedAt == nil {
		t.Fatal("presented refresh row was not marked used")
	}
	if sessions[0].FamilyID != sessions[1].FamilyID {
		t.Fatalf("replacement family = %q, want %q", sessions[1].FamilyID, sessions[0].FamilyID)
	}

	if _, err := RotateZTAPISession(initial.RefreshToken, "192.0.2.12", "replay-agent", now.Add(2*time.Minute)); !errors.Is(err, ErrZTAPIRefreshReplay) {
		t.Fatalf("old-token replay error = %v, want ErrZTAPIRefreshReplay", err)
	}
	assertSessionFamilyRevoked(t, db, sessions[0].FamilyID)
	if _, err := RotateZTAPISession(replacement.RefreshToken, "192.0.2.13", "after-replay", now.Add(3*time.Minute)); !errors.Is(err, ErrZTAPIRefreshRevoked) {
		t.Fatalf("replacement after replay error = %v, want ErrZTAPIRefreshRevoked", err)
	}
}

func TestSessionExpiredUsedRefreshReplayRevokesStillValidReplacement(t *testing.T) {
	db, user := setupSessionTestDB(t)
	configureSessionTestKey(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)

	initial, err := CreateZTAPISession(user.Id, "192.0.2.10", "initial-agent", now)
	if err != nil {
		t.Fatalf("create initial session: %v", err)
	}
	rotationTime := now.Add(30*24*time.Hour - time.Minute)
	replacement, err := RotateZTAPISession(initial.RefreshToken, "192.0.2.11", "replacement-agent", rotationTime)
	if err != nil {
		t.Fatalf("rotate session near initial expiry: %v", err)
	}

	replayTime := now.Add(30*24*time.Hour + time.Minute)
	if _, err := RotateZTAPISession(initial.RefreshToken, "192.0.2.12", "expired-replay", replayTime); !errors.Is(err, ErrZTAPIRefreshReplay) {
		t.Fatalf("expired used-token replay error = %v, want ErrZTAPIRefreshReplay", err)
	}

	var initialRow model.AuthSession
	if err := db.Where("token_hash = ?", sha256HexForTest(initial.RefreshToken)).First(&initialRow).Error; err != nil {
		t.Fatalf("load initial row: %v", err)
	}
	assertSessionFamilyRevoked(t, db, initialRow.FamilyID)
	if _, err := RotateZTAPISession(replacement.RefreshToken, "192.0.2.13", "after-expired-replay", replayTime); !errors.Is(err, ErrZTAPIRefreshRevoked) {
		t.Fatalf("replacement after expired replay error = %v, want ErrZTAPIRefreshRevoked", err)
	}

	if _, err := RotateZTAPISession(initial.RefreshToken, "192.0.2.14", "repeat-expired-replay", replayTime.Add(time.Minute)); !errors.Is(err, ErrZTAPIRefreshReplay) {
		t.Fatalf("repeated replay after family revocation error = %v, want ErrZTAPIRefreshReplay", err)
	}
	assertSessionFamilyRevoked(t, db, initialRow.FamilyID)
}

func TestSessionConcurrentRefreshAllowsOneRotationAndRevokesFamilyOnReplay(t *testing.T) {
	db, user := setupSessionTestDB(t)
	configureSessionTestKey(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	initial, err := CreateZTAPISession(user.Id, "192.0.2.10", "initial-agent", now)
	if err != nil {
		t.Fatalf("create initial session: %v", err)
	}

	const attempts = 2
	start := make(chan struct{})
	results := make(chan struct {
		tokens ZTAPISessionTokens
		err    error
	}, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			tokens, rotateErr := RotateZTAPISession(
				initial.RefreshToken,
				"192.0.2."+strconv.Itoa(20+index),
				"concurrent-agent",
				now.Add(time.Minute),
			)
			results <- struct {
				tokens ZTAPISessionTokens
				err    error
			}{tokens: tokens, err: rotateErr}
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)

	successCount := 0
	var successfulRefresh string
	for result := range results {
		if result.err == nil {
			successCount++
			successfulRefresh = result.tokens.RefreshToken
			continue
		}
		if !errors.Is(result.err, ErrZTAPIRefreshReplay) {
			t.Fatalf("concurrent refresh error = %v, want replay rejection", result.err)
		}
	}
	if successCount != 1 {
		t.Fatalf("successful concurrent rotations = %d, want 1", successCount)
	}

	var initialRow model.AuthSession
	if err := db.Where("token_hash = ?", sha256HexForTest(initial.RefreshToken)).First(&initialRow).Error; err != nil {
		t.Fatalf("load initial row: %v", err)
	}
	assertSessionFamilyRevoked(t, db, initialRow.FamilyID)
	if _, err := RotateZTAPISession(successfulRefresh, "192.0.2.30", "post-concurrency", now.Add(2*time.Minute)); !errors.Is(err, ErrZTAPIRefreshRevoked) {
		t.Fatalf("successful replacement remained usable: %v", err)
	}
}

func TestSessionLogoutRevokesFamilyAndIsIdempotent(t *testing.T) {
	db, user := setupSessionTestDB(t)
	configureSessionTestKey(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	initial, err := CreateZTAPISession(user.Id, "192.0.2.10", "initial-agent", now)
	if err != nil {
		t.Fatalf("create initial session: %v", err)
	}
	replacement, err := RotateZTAPISession(initial.RefreshToken, "192.0.2.11", "replacement-agent", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("rotate initial session: %v", err)
	}

	if err := RevokeZTAPISessionFamily(replacement.RefreshToken, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("revoke session family: %v", err)
	}
	if err := RevokeZTAPISessionFamily(replacement.RefreshToken, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("repeat session revocation: %v", err)
	}
	if err := RevokeZTAPISessionFamily("unknown-token", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("unknown-token logout: %v", err)
	}

	var row model.AuthSession
	if err := db.Where("token_hash = ?", sha256HexForTest(replacement.RefreshToken)).First(&row).Error; err != nil {
		t.Fatalf("load replacement row: %v", err)
	}
	assertSessionFamilyRevoked(t, db, row.FamilyID)
}

func TestSessionRejectsExpiredAndRevokedRefreshTokens(t *testing.T) {
	db, user := setupSessionTestDB(t)
	configureSessionTestKey(t)
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	expired, err := CreateZTAPISession(user.Id, "192.0.2.10", "expired-agent", now.Add(-31*24*time.Hour))
	if err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	if _, err := RotateZTAPISession(expired.RefreshToken, "192.0.2.11", "rotate-expired", now); !errors.Is(err, ErrZTAPIRefreshExpired) {
		t.Fatalf("expired refresh error = %v, want ErrZTAPIRefreshExpired", err)
	}

	revoked, err := CreateZTAPISession(user.Id, "192.0.2.12", "revoked-agent", now)
	if err != nil {
		t.Fatalf("create revoked session: %v", err)
	}
	if err := RevokeZTAPISessionFamily(revoked.RefreshToken, now.Add(time.Minute)); err != nil {
		t.Fatalf("revoke refresh family: %v", err)
	}
	if _, err := RotateZTAPISession(revoked.RefreshToken, "192.0.2.13", "rotate-revoked", now.Add(2*time.Minute)); !errors.Is(err, ErrZTAPIRefreshRevoked) {
		t.Fatalf("revoked refresh error = %v, want ErrZTAPIRefreshRevoked", err)
	}

	var expiredRow model.AuthSession
	if err := db.Where("token_hash = ?", sha256HexForTest(expired.RefreshToken)).First(&expiredRow).Error; err != nil {
		t.Fatalf("load expired row: %v", err)
	}
	if expiredRow.UsedAt != nil {
		t.Fatal("expired refresh token was marked used")
	}
}

func TestSessionOperationsDoNotLogRefreshTokensOrHashes(t *testing.T) {
	var output bytes.Buffer
	databaseLogger := gormlogger.New(
		log.New(&output, "", 0),
		gormlogger.Config{LogLevel: gormlogger.Info},
	)
	_, user := setupSessionTestDBWithLogger(t, databaseLogger)
	configureSessionTestKey(t)
	output.Reset()

	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	initial, err := CreateZTAPISession(user.Id, "192.0.2.10", "log-test", now)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	replacement, err := RotateZTAPISession(initial.RefreshToken, "192.0.2.11", "log-test", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("rotate session: %v", err)
	}
	if err := RevokeZTAPISessionFamily(replacement.RefreshToken, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	logged := output.String()
	for _, secret := range []string{
		initial.RefreshToken,
		sha256HexForTest(initial.RefreshToken),
		replacement.RefreshToken,
		sha256HexForTest(replacement.RefreshToken),
	} {
		if strings.Contains(logged, secret) {
			t.Fatalf("database log contains refresh credential %q: %s", secret, logged)
		}
	}
}

func setupSessionTestDB(t *testing.T) (*gorm.DB, *model.User) {
	return setupSessionTestDBWithLogger(t, nil)
}

func setupSessionTestDBWithLogger(t *testing.T, databaseLogger gormlogger.Interface) (*gorm.DB, *model.User) {
	t.Helper()

	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalSQLite := common.UsingSQLite
	originalMySQL := common.UsingMySQL
	originalPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false

	dsn := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)
	config := &gorm.Config{}
	if databaseLogger != nil {
		config.Logger = databaseLogger
	}
	db, err := gorm.Open(sqlite.Open(dsn), config)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access sqlite connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(10)
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}); err != nil {
		t.Fatalf("migrate session tables: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.UsingSQLite = originalSQLite
		common.UsingMySQL = originalMySQL
		common.UsingPostgreSQL = originalPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})

	user := &model.User{
		Username: "session-user-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Password: "not-used",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return db, user
}

func configureSessionTestKey(t *testing.T) {
	t.Helper()
	if err := ConfigureZTAPISessionSigningKey(testSessionSigningKey); err != nil {
		t.Fatalf("configure signing key: %v", err)
	}
}

func sha256HexForTest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func assertSessionFamilyRevoked(t *testing.T, db *gorm.DB, familyID string) {
	t.Helper()
	var rows []model.AuthSession
	if err := db.Where("family_id = ?", familyID).Find(&rows).Error; err != nil {
		t.Fatalf("load family %q: %v", familyID, err)
	}
	if len(rows) == 0 {
		t.Fatalf("session family %q is empty", familyID)
	}
	for _, row := range rows {
		if row.RevokedAt == nil {
			t.Fatalf("session %d in family %q is not revoked", row.ID, familyID)
		}
	}
}
