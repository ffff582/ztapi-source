package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func newZTAPIEmailEngine(userID int) *gin.Engine {
	engine := gin.New()
	group := engine.Group("/user/self")
	group.Use(func(c *gin.Context) {
		c.Set("id", userID)
		c.Next()
	})
	group.POST("/email/verification", ZTAPIRequestEmailVerification)
	group.PUT("/email", ZTAPIVerifyAndBindEmail)
	return engine
}

func performZTAPIEmailRequest(engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func TestZTAPIEmailCanBeAddedAfterRegistrationOnlyAfterVerification(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	encoded, err := service.HashZTAPIPassword("at-least-ten")
	if err != nil {
		t.Fatal(err)
	}
	user, err := model.CreateZTAPIUserWithEncodedPasswordAndPendingEmail("email-user", encoded, "")
	if err != nil {
		t.Fatal(err)
	}

	originalSMTPServer, originalSMTPFrom := common.SMTPServer, common.SMTPFrom
	common.SMTPServer, common.SMTPFrom = "smtp.example.com", "noreply@example.com"
	var emailBody string
	originalSend := ztAPISendEmail
	ztAPISendEmail = func(_ string, receiver string, content string) error {
		if receiver != "owner@example.com" {
			t.Fatalf("verification receiver = %q", receiver)
		}
		emailBody = content
		return nil
	}
	t.Cleanup(func() {
		common.SMTPServer, common.SMTPFrom = originalSMTPServer, originalSMTPFrom
		ztAPISendEmail = originalSend
	})

	engine := newZTAPIEmailEngine(user.Id)
	requested := performZTAPIEmailRequest(engine, http.MethodPost, "/user/self/email/verification", `{"email":" Owner@Example.com "}`)
	if requested.Code != http.StatusOK {
		t.Fatalf("request verification status = %d; body=%s", requested.Code, requested.Body.String())
	}
	match := regexp.MustCompile(`\b([a-f0-9]{6})\b`).FindStringSubmatch(emailBody)
	if len(match) != 2 {
		t.Fatalf("verification email has no six-character code: %s", emailBody)
	}

	var pending model.User
	if err := db.First(&pending, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Email != "" || pending.GetSetting().PendingEmail != "owner@example.com" {
		t.Fatalf("email state before verification = verified:%q pending:%q", pending.Email, pending.GetSetting().PendingEmail)
	}

	verified := performZTAPIEmailRequest(engine, http.MethodPut, "/user/self/email", `{"email":"owner@example.com","verification_code":"`+match[1]+`"}`)
	if verified.Code != http.StatusOK {
		t.Fatalf("verify email status = %d; body=%s", verified.Code, verified.Body.String())
	}
	var bound model.User
	if err := db.First(&bound, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if bound.Email != "owner@example.com" || bound.GetSetting().PendingEmail != "" {
		t.Fatalf("email state after verification = verified:%q pending:%q", bound.Email, bound.GetSetting().PendingEmail)
	}

	replayed := performZTAPIEmailRequest(engine, http.MethodPut, "/user/self/email", `{"email":"owner@example.com","verification_code":"`+match[1]+`"}`)
	if replayed.Code != http.StatusBadRequest {
		t.Fatalf("replayed verification status = %d, want 400; body=%s", replayed.Code, replayed.Body.String())
	}
}

func TestZTAPIEmailBindingRefusesAnEmailOwnedByAnotherAccount(t *testing.T) {
	setupZTAPIAuthControllerTest(t)
	encoded, err := service.HashZTAPIPassword("at-least-ten")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := model.CreateZTAPIUserWithEncodedPasswordAndEmail("email-owner", encoded, "taken@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_ = owner
	claimant, err := model.CreateZTAPIUserWithEncodedPasswordAndPendingEmail("email-claimant", encoded, "taken@example.com")
	if err != nil {
		t.Fatal(err)
	}
	common.RegisterVerificationCodeWithKey("taken@example.com", "123456", common.EmailVerificationPurpose)

	response := performZTAPIEmailRequest(newZTAPIEmailEngine(claimant.Id), http.MethodPut, "/user/self/email", `{"email":"taken@example.com","verification_code":"123456"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate verified email status = %d, want 409; body=%s", response.Code, response.Body.String())
	}
}
