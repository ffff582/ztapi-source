package service

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestPasswordHashUsesReviewedArgon2idParametersAndUniqueSalts(t *testing.T) {
	const password = "correct horse battery staple"

	first, err := HashZTAPIPassword(password)
	if err != nil {
		t.Fatalf("hash first password: %v", err)
	}
	second, err := HashZTAPIPassword(password)
	if err != nil {
		t.Fatalf("hash second password: %v", err)
	}
	if first == second {
		t.Fatal("equal passwords produced identical encoded hashes")
	}

	version, memory, iterations, parallelism, salt, key := parseArgon2idForTest(t, first)
	if version != 19 {
		t.Fatalf("argon2 version = %d, want 19", version)
	}
	if memory != 65536 || iterations != 3 || parallelism != 2 {
		t.Fatalf(
			"argon2 parameters = m=%d,t=%d,p=%d, want m=65536,t=3,p=2",
			memory,
			iterations,
			parallelism,
		)
	}
	if len(salt) != 16 {
		t.Fatalf("salt length = %d, want 16", len(salt))
	}
	if len(key) != 32 {
		t.Fatalf("key length = %d, want 32", len(key))
	}

	_, _, _, _, secondSalt, _ := parseArgon2idForTest(t, second)
	if string(salt) == string(secondSalt) {
		t.Fatal("equal passwords reused the same salt")
	}
}

func TestPasswordVerificationAcceptsValidAndRejectsWrongOrMalformedHashes(t *testing.T) {
	encoded, err := HashZTAPIPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if !VerifyZTAPIPassword("correct horse battery staple", encoded) {
		t.Fatal("valid password was rejected")
	}
	if VerifyZTAPIPassword("wrong password", encoded) {
		t.Fatal("wrong password was accepted")
	}

	malformed := []string{
		"",
		"not-an-argon2-hash",
		"$argon2id$v=19$m=65536,t=3,p=2$bad$bad",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$a2V5",
		"$argon2id$v=18$m=65536,t=3,p=2$c2FsdA$a2V5",
		"$argon2id$v=19$m=1,t=1,p=1$c2FsdA$a2V5",
	}
	for _, candidate := range malformed {
		t.Run(candidate, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("verification panicked for malformed hash: %v", recovered)
				}
			}()
			if VerifyZTAPIPassword("correct horse battery staple", candidate) {
				t.Fatalf("malformed hash %q was accepted", candidate)
			}
		})
	}
}

func TestPasswordBoundaryValidationUsesCharactersForMinimumAndBytesForMaximum(t *testing.T) {
	tests := []struct {
		name     string
		password string
		valid    bool
	}{
		{name: "nine characters", password: "123456789", valid: false},
		{name: "ten characters", password: "1234567890", valid: true},
		{name: "ten CJK characters", password: "甲乙丙丁戊己庚辛壬癸", valid: true},
		{name: "256 bytes", password: strings.Repeat("a", 256), valid: true},
		{name: "257 bytes", password: strings.Repeat("a", 257), valid: false},
		{name: "invalid UTF-8", password: string([]byte{0xff, 0xfe}) + "1234567890", valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateZTAPIPassword(test.password)
			if test.valid && err != nil {
				t.Fatalf("valid password rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid password accepted")
			}
		})
	}
}

func TestPasswordUsernameBoundaryNormalizesAndRestrictsCharacters(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		normalized string
		valid      bool
	}{
		{name: "trim ASCII", input: "  alice-_01  ", normalized: "alice-_01", valid: true},
		{name: "CJK letters", input: "用户_12", normalized: "用户_12", valid: true},
		{name: "minimum bytes", input: "abc", normalized: "abc", valid: true},
		{name: "below minimum", input: "ab", valid: false},
		{name: "maximum bytes", input: strings.Repeat("a", 32), normalized: strings.Repeat("a", 32), valid: true},
		{name: "above maximum", input: strings.Repeat("a", 33), valid: false},
		{name: "embedded space", input: "ali ce", valid: false},
		{name: "control character", input: "ali\nce", valid: false},
		{name: "punctuation", input: "alice!", valid: false},
		{name: "invalid UTF-8", input: string([]byte{0xff, 'a', 'b'}), valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeZTAPIUsername(test.input)
			if test.valid {
				if err != nil {
					t.Fatalf("valid username rejected: %v", err)
				}
				if got != test.normalized {
					t.Fatalf("normalized username = %q, want %q", got, test.normalized)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid username normalized to %q", got)
			}
		})
	}
}

func parseArgon2idForTest(t *testing.T, encoded string) (
	version int,
	memory uint32,
	iterations uint32,
	parallelism uint8,
	salt []byte,
	key []byte,
) {
	t.Helper()

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		t.Fatalf("unexpected encoded hash format: %q", encoded)
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		t.Fatalf("parse version: %v", err)
	}
	if _, err := fmt.Sscanf(
		parts[3],
		"m=%d,t=%d,p=%d",
		&memory,
		&iterations,
		&parallelism,
	); err != nil {
		t.Fatalf("parse parameters: %v", err)
	}
	var err error
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	return
}
