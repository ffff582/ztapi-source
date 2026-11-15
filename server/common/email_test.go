package common

import "testing"

func TestGetSMTPAuthAllowsLocalRelayWithoutCredentials(t *testing.T) {
	originalAccount := SMTPAccount
	originalToken := SMTPToken
	originalForceLogin := SMTPForceAuthLogin
	SMTPAccount = ""
	SMTPToken = ""
	SMTPForceAuthLogin = false
	t.Cleanup(func() {
		SMTPAccount = originalAccount
		SMTPToken = originalToken
		SMTPForceAuthLogin = originalForceLogin
	})

	if auth := getSMTPAuth(); auth != nil {
		t.Fatalf("local unauthenticated relay returned an SMTP auth mechanism: %#v", auth)
	}
}
