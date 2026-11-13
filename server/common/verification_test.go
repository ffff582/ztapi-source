package common

import "testing"

func TestConsumeVerificationCodeWithKeyAllowsExactlyOneUse(t *testing.T) {
	const key = "password-reset@example.com"
	const code = "one-time-token"
	RegisterVerificationCodeWithKey(key, code, PasswordResetPurpose)
	t.Cleanup(func() { DeleteKey(key, PasswordResetPurpose) })

	if !ConsumeVerificationCodeWithKey(key, code, PasswordResetPurpose) {
		t.Fatal("first verification token use was rejected")
	}
	if ConsumeVerificationCodeWithKey(key, code, PasswordResetPurpose) {
		t.Fatal("verification token was accepted more than once")
	}
}
