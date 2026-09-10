package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

const (
	ztAPIAccessTokenLifetime  = 15 * time.Minute
	ztAPIRefreshTokenLifetime = 30 * 24 * time.Hour
	ztAPIAccessTokenExpiresIn = 900
	ztAPISessionIssuer        = "ztapi"
)

var (
	ErrZTAPIRefreshInvalid = errors.New("refresh token invalid")
	ErrZTAPIRefreshReplay  = errors.New("refresh token replay detected")
	ErrZTAPIRefreshExpired = errors.New("refresh token expired")
	ErrZTAPIRefreshRevoked = errors.New("refresh token revoked")
	errZTAPISigningKey     = errors.New("ZTAPI_SESSION_SIGNING_KEY must contain at least 32 bytes")

	ztAPISigningKeyMu sync.RWMutex
	ztAPISigningKey   []byte
)

type ZTAPIAccessClaims struct {
	jwt.RegisteredClaims
}

type ZTAPISessionTokens struct {
	UserID       int
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

func ConfigureZTAPISessionSigningKey(raw string) error {
	if len([]byte(raw)) < 32 {
		return errZTAPISigningKey
	}
	key := append([]byte(nil), []byte(raw)...)
	ztAPISigningKeyMu.Lock()
	ztAPISigningKey = key
	ztAPISigningKeyMu.Unlock()
	return nil
}

func ConfigureZTAPISessionSigningKeyFromEnv() error {
	return ConfigureZTAPISessionSigningKey(os.Getenv("ZTAPI_SESSION_SIGNING_KEY"))
}

func IssueZTAPIAccessToken(userID int, now time.Time) (string, error) {
	if userID <= 0 {
		return "", errors.New("invalid access-token subject")
	}
	key, err := currentZTAPISigningKey()
	if err != nil {
		return "", err
	}
	jti, err := randomBase64URL(16)
	if err != nil {
		return "", fmt.Errorf("generate access-token ID: %w", err)
	}
	now = now.UTC()
	claims := ZTAPIAccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ztAPISessionIssuer,
			Subject:   strconv.Itoa(userID),
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ztAPIAccessTokenLifetime)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
}

func ParseZTAPIAccessToken(raw string, now time.Time) (*ZTAPIAccessClaims, error) {
	key, err := currentZTAPISigningKey()
	if err != nil {
		return nil, err
	}
	claims := &ZTAPIAccessClaims{}
	token, err := jwt.ParseWithClaims(
		raw,
		claims,
		func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, errors.New("invalid access-token signing algorithm")
			}
			return key, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(ztAPISessionIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(func() time.Time { return now.UTC() }),
	)
	if err != nil || !token.Valid {
		return nil, errors.New("invalid access token")
	}
	userID, err := strconv.Atoi(claims.Subject)
	if err != nil || userID <= 0 || claims.ID == "" || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return nil, errors.New("invalid access-token claims")
	}
	return claims, nil
}

func CreateZTAPISession(userID int, clientIP string, userAgent string, now time.Time) (ZTAPISessionTokens, error) {
	if userID <= 0 {
		return ZTAPISessionTokens{}, errors.New("invalid session user")
	}
	familyID, err := randomBase64URL(16)
	if err != nil {
		return ZTAPISessionTokens{}, fmt.Errorf("generate session family: %w", err)
	}
	return createZTAPISessionInFamily(model.DB, userID, familyID, clientIP, userAgent, now)
}

func RotateZTAPISession(refreshToken string, clientIP string, userAgent string, now time.Time) (ZTAPISessionTokens, error) {
	if refreshToken == "" {
		return ZTAPISessionTokens{}, ErrZTAPIRefreshInvalid
	}
	tokenHash := sha256Hex(refreshToken)
	const maxSQLiteAttempts = 50
	for attempt := 0; ; attempt++ {
		tokens, err := rotateZTAPISessionOnce(tokenHash, clientIP, userAgent, now.UTC())
		if err == nil || model.DB.Dialector.Name() != "sqlite" || !isSQLiteBusyError(err) || attempt >= maxSQLiteAttempts-1 {
			return tokens, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func RevokeZTAPISessionFamily(refreshToken string, now time.Time) error {
	if refreshToken == "" {
		return nil
	}
	tokenHash := sha256Hex(refreshToken)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		tx = silentZTAPISessionDB(tx)
		var session model.AuthSession
		err := tx.Where("token_hash = ?", tokenHash).First(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load session for revocation: %w", err)
		}
		return revokeZTAPISessionFamily(tx, session.FamilyID, now.UTC())
	})
}

func rotateZTAPISessionOnce(tokenHash string, clientIP string, userAgent string, now time.Time) (ZTAPISessionTokens, error) {
	var result ZTAPISessionTokens
	var outcomeErr error
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		tx = silentZTAPISessionDB(tx)
		var session model.AuthSession
		if tx.Dialector.Name() == "sqlite" {
			update := tx.Model(&model.AuthSession{}).
				Where(
					"token_hash = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?",
					tokenHash,
					now,
				).
				Update("used_at", now)
			if update.Error != nil {
				return fmt.Errorf("claim refresh session: %w", update.Error)
			}
			if err := tx.Where("token_hash = ?", tokenHash).First(&session).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					outcomeErr = ErrZTAPIRefreshInvalid
					return nil
				}
				return fmt.Errorf("load refresh session: %w", err)
			}
			if update.RowsAffected == 0 {
				return rejectUnclaimedZTAPIRefresh(tx, &session, now, &outcomeErr)
			}
		} else {
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("token_hash = ?", tokenHash).
				First(&session).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				outcomeErr = ErrZTAPIRefreshInvalid
				return nil
			}
			if err != nil {
				return fmt.Errorf("lock refresh session: %w", err)
			}
			if session.RevokedAt != nil || !session.ExpiresAt.After(now) || session.UsedAt != nil {
				return rejectUnclaimedZTAPIRefresh(tx, &session, now, &outcomeErr)
			}
			update := tx.Model(&model.AuthSession{}).
				Where("id = ? AND used_at IS NULL AND revoked_at IS NULL", session.ID).
				Update("used_at", now)
			if update.Error != nil {
				return fmt.Errorf("claim refresh session: %w", update.Error)
			}
			if update.RowsAffected != 1 {
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, session.ID).Error; err != nil {
					return fmt.Errorf("reload refresh session: %w", err)
				}
				return rejectUnclaimedZTAPIRefresh(tx, &session, now, &outcomeErr)
			}
		}

		var err error
		result, err = createZTAPISessionInFamily(
			tx,
			session.UserID,
			session.FamilyID,
			clientIP,
			userAgent,
			now,
		)
		return err
	})
	if err != nil {
		return ZTAPISessionTokens{}, err
	}
	if outcomeErr != nil {
		return ZTAPISessionTokens{}, outcomeErr
	}
	return result, nil
}

func rejectUnclaimedZTAPIRefresh(
	tx *gorm.DB,
	session *model.AuthSession,
	now time.Time,
	outcomeErr *error,
) error {
	switch {
	case session.UsedAt != nil:
		if err := revokeZTAPISessionFamily(tx, session.FamilyID, now); err != nil {
			return err
		}
		*outcomeErr = ErrZTAPIRefreshReplay
	case session.RevokedAt != nil:
		*outcomeErr = ErrZTAPIRefreshRevoked
	case !session.ExpiresAt.After(now):
		*outcomeErr = ErrZTAPIRefreshExpired
	default:
		*outcomeErr = ErrZTAPIRefreshInvalid
	}
	return nil
}

func createZTAPISessionInFamily(
	tx *gorm.DB,
	userID int,
	familyID string,
	clientIP string,
	userAgent string,
	now time.Time,
) (ZTAPISessionTokens, error) {
	refreshToken, err := randomBase64URL(32)
	if err != nil {
		return ZTAPISessionTokens{}, fmt.Errorf("generate refresh token: %w", err)
	}
	accessToken, err := IssueZTAPIAccessToken(userID, now)
	if err != nil {
		return ZTAPISessionTokens{}, err
	}
	session := &model.AuthSession{
		UserID:        userID,
		FamilyID:      familyID,
		TokenHash:     sha256Hex(refreshToken),
		ExpiresAt:     now.Add(ztAPIRefreshTokenLifetime),
		CreatedAt:     now,
		CreationIP:    clientIP,
		UserAgentHash: sha256Hex(userAgent),
	}
	if err := silentZTAPISessionDB(tx).Create(session).Error; err != nil {
		return ZTAPISessionTokens{}, fmt.Errorf("store refresh session: %w", err)
	}
	return ZTAPISessionTokens{
		UserID:       userID,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    ztAPIAccessTokenExpiresIn,
	}, nil
}

func revokeZTAPISessionFamily(tx *gorm.DB, familyID string, now time.Time) error {
	if err := tx.Model(&model.AuthSession{}).
		Where("family_id = ? AND revoked_at IS NULL", familyID).
		Update("revoked_at", now).Error; err != nil {
		return fmt.Errorf("revoke refresh-session family: %w", err)
	}
	return nil
}

func currentZTAPISigningKey() ([]byte, error) {
	ztAPISigningKeyMu.RLock()
	defer ztAPISigningKeyMu.RUnlock()
	if len(ztAPISigningKey) < 32 {
		return nil, errZTAPISigningKey
	}
	return append([]byte(nil), ztAPISigningKey...), nil
}

func randomBase64URL(byteLength int) (string, error) {
	value := make([]byte, byteLength)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func isSQLiteBusyError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "database is busy")
}

func silentZTAPISessionDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{
		Logger: db.Logger.LogMode(gormlogger.Silent),
	})
}
