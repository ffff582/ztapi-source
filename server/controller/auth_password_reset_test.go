package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

func TestZTAPIPasswordResetChangesArgon2PasswordConsumesTokenAndRevokesSessions(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	enableZTAPIPasswordResetForTest(t)
	engine := newZTAPIAuthControllerEngine()
	encoded, err := service.HashZTAPIPassword("old-password-123")
	if err != nil {
		t.Fatal(err)
	}
	user, err := model.CreateZTAPIUserWithEncodedPasswordAndEmail(
		"reset-user",
		encoded,
		"reset@example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	login := loginZTAPasswordResetUser(t, engine, "reset-user", "old-password-123")

	var emailBody string
	originalSendEmail := ztAPISendEmail
	ztAPISendEmail = func(_ string, receiver string, content string) error {
		if receiver != "reset@example.com" {
			t.Fatalf("reset email receiver = %q", receiver)
		}
		emailBody = content
		return nil
	}
	originalServerAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://ztapi.vip"
	t.Cleanup(func() {
		ztAPISendEmail = originalSendEmail
		system_setting.ServerAddress = originalServerAddress
	})

	requested := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/password-reset/request",
		`{"email":" Reset@Example.com "}`,
		nil,
	)
	if requested.Code != http.StatusOK || !strings.Contains(requested.Body.String(), `"success":true`) {
		t.Fatalf("password reset request failed: status=%d body=%s", requested.Code, requested.Body.String())
	}
	match := regexp.MustCompile(`token=([a-f0-9]+)`).FindStringSubmatch(emailBody)
	if len(match) != 2 || !strings.Contains(emailBody, "/reset-password?") {
		t.Fatalf("reset email is missing the ZTAPI reset link: %s", emailBody)
	}
	if !strings.Contains(emailBody, "email="+url.QueryEscape("reset@example.com")) {
		t.Fatalf("reset email link is missing the normalized email: %s", emailBody)
	}
	token := match[1]

	confirmed := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/password-reset/confirm",
		fmt.Sprintf(`{"email":"reset@example.com","token":%q,"new_password":"new-password-456"}`, token),
		nil,
	)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("password reset confirmation status = %d; body=%s", confirmed.Code, confirmed.Body.String())
	}

	var updated model.User
	if err := db.First(&updated, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !service.VerifyZTAPIPassword("new-password-456", updated.Password) {
		t.Fatal("password reset did not store a ZTAPI Argon2id password")
	}
	if service.VerifyZTAPIPassword("old-password-123", updated.Password) {
		t.Fatal("old password remained valid after reset")
	}

	refresh := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/refresh",
		"",
		login,
	)
	if refresh.Code != http.StatusUnauthorized {
		t.Fatalf("pre-reset session refresh status = %d, want 401; body=%s", refresh.Code, refresh.Body.String())
	}
	replayed := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/password-reset/confirm",
		fmt.Sprintf(`{"email":"reset@example.com","token":%q,"new_password":"third-password-789"}`, token),
		nil,
	)
	if replayed.Code != http.StatusBadRequest {
		t.Fatalf("replayed reset token status = %d, want 400; body=%s", replayed.Code, replayed.Body.String())
	}
}

func TestZTAPIPasswordResetRequestDoesNotRevealWhetherEmailExists(t *testing.T) {
	setupZTAPIAuthControllerTest(t)
	enableZTAPIPasswordResetForTest(t)
	engine := newZTAPIAuthControllerEngine()
	encoded, err := service.HashZTAPIPassword("old-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.CreateZTAPIUserWithEncodedPasswordAndEmail(
		"existing-user",
		encoded,
		"existing@example.com",
	); err != nil {
		t.Fatal(err)
	}

	originalSendEmail := ztAPISendEmail
	ztAPISendEmail = func(_ string, _ string, _ string) error { return nil }
	t.Cleanup(func() { ztAPISendEmail = originalSendEmail })

	existing := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/password-reset/request", `{"email":"existing@example.com"}`, nil)
	missing := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/password-reset/request", `{"email":"missing@example.com"}`, nil)
	if existing.Code != http.StatusOK || missing.Code != http.StatusOK || existing.Body.String() != missing.Body.String() {
		t.Fatalf("password reset responses reveal account existence: existing=%d %s missing=%d %s", existing.Code, existing.Body.String(), missing.Code, missing.Body.String())
	}
}

func TestZTAPIPasswordResetRequestIsUnavailableUntilEmailIsConfigured(t *testing.T) {
	setupZTAPIAuthControllerTest(t)
	engine := newZTAPIAuthControllerEngine()
	originalEmailVerification := common.EmailVerificationEnabled
	originalSMTPServer := common.SMTPServer
	originalSMTPFrom := common.SMTPFrom
	common.EmailVerificationEnabled = false
	common.SMTPServer = ""
	common.SMTPFrom = ""
	t.Cleanup(func() {
		common.EmailVerificationEnabled = originalEmailVerification
		common.SMTPServer = originalSMTPServer
		common.SMTPFrom = originalSMTPFrom
	})

	response := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/password-reset/request",
		`{"email":"user@example.com"}`,
		nil,
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("password reset request status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
}

func enableZTAPIPasswordResetForTest(t *testing.T) {
	t.Helper()
	originalEmailVerification := common.EmailVerificationEnabled
	originalSMTPServer := common.SMTPServer
	originalSMTPFrom := common.SMTPFrom
	common.EmailVerificationEnabled = true
	common.SMTPServer = "smtp.example.com"
	common.SMTPFrom = "noreply@example.com"
	t.Cleanup(func() {
		common.EmailVerificationEnabled = originalEmailVerification
		common.SMTPServer = originalSMTPServer
		common.SMTPFrom = originalSMTPFrom
	})
}

func loginZTAPasswordResetUser(t *testing.T, engine *gin.Engine, username string, password string) *http.Cookie {
	t.Helper()
	request := performZTAPIAuthRequest(t, engine, http.MethodPost, "/auth/login", fmt.Sprintf(`{"username":%q,"password":%q}`, username, password), nil)
	if request.Code != http.StatusOK {
		t.Fatalf("login before password reset failed: %s", request.Body.String())
	}
	return requireRefreshCookie(t, request)
}
