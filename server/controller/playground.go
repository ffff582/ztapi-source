package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func playgroundAccessAllowed(c *gin.Context) bool {
	return !c.GetBool("use_access_token") ||
		common.GetContextKeyBool(c, constant.ContextKeyZTAPIJWTAuthenticated)
}

func selectPlaygroundBillingToken(tokens []*model.Token, now int64) *model.Token {
	usable := func(token *model.Token) bool {
		return token != nil && token.Id > 0 && token.UserId > 0 &&
			token.Status == common.TokenStatusEnabled && !token.DeletedAt.Valid &&
			(token.ExpiredTime == -1 || token.ExpiredTime >= now) &&
			(token.UnlimitedQuota || token.RemainQuota > 0)
	}
	for _, token := range tokens {
		if usable(token) && token.UnlimitedQuota {
			return token
		}
	}
	for _, token := range tokens {
		if usable(token) {
			return token
		}
	}
	return nil
}

func Playground(c *gin.Context) {
	var newAPIError *types.NewAPIError

	defer func() {
		if newAPIError != nil {
			c.JSON(newAPIError.StatusCode, gin.H{
				"error": newAPIError.ToOpenAIError(),
			})
		}
	}()

	if !playgroundAccessAllowed(c) {
		newAPIError = types.NewError(errors.New("暂不支持使用 access token"), types.ErrorCodeAccessDenied, types.ErrOptionWithSkipRetry())
		return
	}

	userId := c.GetInt("id")

	// Write user context to ensure acceptUnsetRatio is available
	userCache, err := model.GetUserCache(userId)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		return
	}
	userCache.WriteContext(c)

	tokens, err := model.GetAllUserTokens(userId, 0, 100)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		return
	}
	billingToken := selectPlaygroundBillingToken(tokens, time.Now().Unix())
	if billingToken == nil {
		newAPIError = types.NewErrorWithStatusCode(errors.New("请先创建一个可用的 API 密钥"), types.ErrorCodeAccessDenied, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		return
	}
	if err = middleware.SetupContextForToken(c, billingToken); err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		return
	}

	Relay(c, types.RelayFormatOpenAI)
}
