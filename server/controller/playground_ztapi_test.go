package controller

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPlaygroundAccessAllowsOnlyZTAPIJWTAccessTokens(t *testing.T) {
	tests := []struct {
		name           string
		useAccessToken bool
		ztapiJWT       bool
		want           bool
	}{
		{name: "legacy session", want: true},
		{name: "legacy access token", useAccessToken: true, want: false},
		{name: "ztapi jwt", useAccessToken: true, ztapiJWT: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(nil)
			ctx.Set("use_access_token", tt.useAccessToken)
			common.SetContextKey(ctx, constant.ContextKeyZTAPIJWTAuthenticated, tt.ztapiJWT)
			require.Equal(t, tt.want, playgroundAccessAllowed(ctx))
		})
	}
}

func TestPlaygroundBillingTokenPrefersUsableUnlimitedToken(t *testing.T) {
	now := time.Now().Unix()
	tokens := []*model.Token{
		{Id: 9, UserId: 17, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
		{Id: 8, UserId: 17, Status: common.TokenStatusEnabled, ExpiredTime: now - 1, UnlimitedQuota: true},
		{Id: 7, UserId: 17, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true},
	}

	token := selectPlaygroundBillingToken(tokens, now)
	require.NotNil(t, token)
	require.Equal(t, 7, token.Id)
}

func TestPlaygroundBillingTokenFallsBackToUsableFiniteToken(t *testing.T) {
	now := time.Now().Unix()
	tokens := []*model.Token{
		{Id: 9, UserId: 17, Status: common.TokenStatusDisabled, ExpiredTime: -1, UnlimitedQuota: true},
		{Id: 8, UserId: 17, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 0},
		{Id: 7, UserId: 17, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100},
	}

	token := selectPlaygroundBillingToken(tokens, now)
	require.NotNil(t, token)
	require.Equal(t, 7, token.Id)
}

func TestPlaygroundBillingTokenRejectsUnavailableTokens(t *testing.T) {
	now := time.Now().Unix()
	tokens := []*model.Token{
		nil,
		{Id: 0, UserId: 17, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true},
		{Id: 8, UserId: 17, Status: common.TokenStatusEnabled, ExpiredTime: now - 1, UnlimitedQuota: true},
		{Id: 7, UserId: 17, Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 0},
	}

	require.Nil(t, selectPlaygroundBillingToken(tokens, now))
}
