package service

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	ztAPIPasswordMemory      uint32 = 65536
	ztAPIPasswordIterations  uint32 = 3
	ztAPIPasswordParallelism uint8  = 2
	ztAPIPasswordSaltLength         = 16
	ztAPIPasswordKeyLength   uint32 = 32
	ztAPIDummyPasswordHash          = "$argon2id$v=19$m=65536,t=3,p=2$WlRBUElBdXRoRHVtbXkhIQ$dAeJWRaxa1dbN68pDvBckxNon8ol6MUXbqDCApoCpLs"
)

var (
	errInvalidZTAPIUsername = errors.New("invalid ZTAPI username")
	errInvalidZTAPIPassword = errors.New("invalid ZTAPI password")
)

func HashZTAPIPassword(password string) (string, error) {
	if err := ValidateZTAPIPassword(password); err != nil {
		return "", err
	}

	salt := make([]byte, ztAPIPasswordSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey(
		[]byte(password),
		salt,
		ztAPIPasswordIterations,
		ztAPIPasswordMemory,
		ztAPIPasswordParallelism,
		ztAPIPasswordKeyLength,
	)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		ztAPIPasswordMemory,
		ztAPIPasswordIterations,
		ztAPIPasswordParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func VerifyZTAPIPassword(password string, encodedHash string) bool {
	salt, expectedKey, ok := parseZTAPIArgon2idHash(encodedHash)
	if !ok {
		return false
	}
	actualKey := argon2.IDKey(
		[]byte(password),
		salt,
		ztAPIPasswordIterations,
		ztAPIPasswordMemory,
		ztAPIPasswordParallelism,
		ztAPIPasswordKeyLength,
	)
	return subtle.ConstantTimeCompare(actualKey, expectedKey) == 1
}

func ZTAPIPasswordHashOrDummy(encodedHash string) (string, bool) {
	if _, _, ok := parseZTAPIArgon2idHash(encodedHash); ok {
		return encodedHash, true
	}
	return ztAPIDummyPasswordHash, false
}

func ValidateZTAPIPassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 10 || len(password) > 256 {
		return errInvalidZTAPIPassword
	}
	return nil
}

func NormalizeZTAPIUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if !utf8.ValidString(username) || len(username) < 3 || len(username) > 32 {
		return "", errInvalidZTAPIUsername
	}
	for _, character := range username {
		if character == '_' || character == '-' || unicode.IsLetter(character) || unicode.IsNumber(character) {
			continue
		}
		return "", errInvalidZTAPIUsername
	}
	return username, nil
}

func parseZTAPIArgon2idHash(encodedHash string) ([]byte, []byte, bool) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, false
	}

	version, err := parsePrefixedUint(parts[2], "v=", 8)
	if err != nil || version != argon2.Version {
		return nil, nil, false
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return nil, nil, false
	}
	memory, err := parsePrefixedUint(parameters[0], "m=", 32)
	if err != nil || uint32(memory) != ztAPIPasswordMemory {
		return nil, nil, false
	}
	iterations, err := parsePrefixedUint(parameters[1], "t=", 32)
	if err != nil || uint32(iterations) != ztAPIPasswordIterations {
		return nil, nil, false
	}
	parallelism, err := parsePrefixedUint(parameters[2], "p=", 8)
	if err != nil || uint8(parallelism) != ztAPIPasswordParallelism {
		return nil, nil, false
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != ztAPIPasswordSaltLength {
		return nil, nil, false
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) != int(ztAPIPasswordKeyLength) {
		return nil, nil, false
	}
	return salt, key, true
}

func parsePrefixedUint(value string, prefix string, bitSize int) (uint64, error) {
	if !strings.HasPrefix(value, prefix) {
		return 0, errors.New("missing prefix")
	}
	return strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, bitSize)
}
