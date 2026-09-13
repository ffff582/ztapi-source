package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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

func TestPlaygroundTemporaryTokenUsesWalletWithoutFiniteTokenQuota(t *testing.T) {
	token := playgroundTemporaryToken(17, "default")
	require.Equal(t, 17, token.UserId)
	require.Equal(t, "playground-default", token.Name)
	require.Equal(t, "default", token.Group)
	require.True(t, token.UnlimitedQuota)
	require.Zero(t, token.Id)
}
