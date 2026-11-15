package middleware

import "testing"

func TestAuthReauthFailureContract(t *testing.T) {
	response := authReauthFailure("authentication failed")

	if response["success"] != false {
		t.Fatalf("expected success=false, got %#v", response["success"])
	}
	if response["code"] != authReauthCode {
		t.Fatalf("expected code %q, got %#v", authReauthCode, response["code"])
	}
	if response["message"] != "authentication failed" {
		t.Fatalf("expected message to be preserved, got %#v", response["message"])
	}
}
