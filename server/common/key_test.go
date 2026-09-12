package common

import (
	"strings"
	"testing"
)

func TestHashZTAPIKeyReturnsLowercaseSHA256Hex(t *testing.T) {
	const plaintext = "sk-zt-test-key"
	const expected = "7a55cdef91118304adf6e67ad9ae521bc3a726497eac49acb65026a90be066e2"

	if got := HashZTAPIKey(plaintext); got != expected {
		t.Fatalf("HashZTAPIKey(%q) = %q, want %q", plaintext, got, expected)
	}
}

func TestGenerateZTAPIKeyContract(t *testing.T) {
	plaintext, hash, prefix, err := GenerateZTAPIKey()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(plaintext, "sk-zt-") {
		t.Fatalf("plaintext %q does not have sk-zt- prefix", plaintext)
	}
	if len(plaintext) != len("sk-zt-")+43 {
		t.Fatalf("plaintext length = %d, want %d", len(plaintext), len("sk-zt-")+43)
	}
	if strings.Contains(plaintext, "=") {
		t.Fatalf("plaintext %q contains base64 padding", plaintext)
	}
	if hash != HashZTAPIKey(plaintext) {
		t.Fatalf("hash %q does not match generated plaintext", hash)
	}
	if prefix != plaintext[:12] {
		t.Fatalf("prefix = %q, want %q", prefix, plaintext[:12])
	}
}

func TestIsValidZTAPIKeyRejectsPreLaunchPrefix(t *testing.T) {
	plaintext, _, _, err := GenerateZTAPIKey()
	if err != nil {
		t.Fatal(err)
	}

	legacy := "sk-" + "gan-" + strings.TrimPrefix(plaintext, "sk-zt-")
	if IsValidZTAPIKey(legacy) {
		t.Fatalf("pre-launch credential %q was accepted", legacy)
	}
}
