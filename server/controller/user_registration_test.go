package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

func TestRegisterDoesNotCreateUndisclosedDefaultToken(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate user table: %v", err)
	}

	previousRegisterEnabled := common.RegisterEnabled
	previousPasswordRegisterEnabled := common.PasswordRegisterEnabled
	previousEmailVerificationEnabled := common.EmailVerificationEnabled
	previousQuotaForNewUser := common.QuotaForNewUser
	previousGenerateDefaultToken := constant.GenerateDefaultToken
	common.RegisterEnabled = true
	common.PasswordRegisterEnabled = true
	common.EmailVerificationEnabled = false
	common.QuotaForNewUser = 0
	constant.GenerateDefaultToken = true
	t.Cleanup(func() {
		common.RegisterEnabled = previousRegisterEnabled
		common.PasswordRegisterEnabled = previousPasswordRegisterEnabled
		common.EmailVerificationEnabled = previousEmailVerificationEnabled
		common.QuotaForNewUser = previousQuotaForNewUser
		constant.GenerateDefaultToken = previousGenerateDefaultToken
	})

	ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/user/register", map[string]any{
		"username": "new-ztapi-user",
		"password": "correct-horse",
	}, 0)

	Register(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("registration failed: %s", response.Message)
	}
	var userCount int64
	if err := db.Model(&model.User{}).Where("username = ?", "new-ztapi-user").Count(&userCount).Error; err != nil {
		t.Fatalf("count registered users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("registered user count = %d, want 1", userCount)
	}
	var tokenCount int64
	if err := db.Model(&model.Token{}).Count(&tokenCount).Error; err != nil {
		t.Fatalf("count tokens after registration: %v", err)
	}
	if tokenCount != 0 {
		t.Fatalf("registration silently created %d undisclosed token(s), want 0", tokenCount)
	}
}
