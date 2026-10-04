package controller

import (
	"errors"
	"net/http"
	"strings"
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

func selectPlaygroundAccessToken(tokens []*model.Token, now int64) *model.Token {
	for _, token := range tokens {
		if token != nil && token.Id > 0 && token.UserId > 0 &&
			token.Status == common.TokenStatusEnabled && !token.DeletedAt.Valid &&
			(token.ExpiredTime == -1 || token.ExpiredTime >= now) {
			return token
		}
	}
	return nil
}

// RewritePlaygroundMediaPath keeps media workbench requests session-authenticated
// while allowing the existing relay/task code to use its canonical public paths.
func RewritePlaygroundMediaPath() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		switch {
		case strings.HasPrefix(path, "/pg/images/"):
			c.Request.URL.Path = strings.Replace(path, "/pg/images/", "/v1/images/", 1)
		case strings.HasPrefix(path, "/pg/video/generations"):
			c.Request.URL.Path = strings.Replace(path, "/pg/video/generations", "/v1/video/generations", 1)
		}
		c.Next()
	}
}

func writePlaygroundError(c *gin.Context, newAPIError *types.NewAPIError) {
	if newAPIError == nil {
		return
	}
	c.JSON(newAPIError.StatusCode, gin.H{"error": newAPIError.ToOpenAIError()})
}

func preparePlayground(c *gin.Context, requireBalance bool) *types.NewAPIError {
	if !playgroundAccessAllowed(c) {
		return types.NewError(errors.New("暂不支持使用 access token"), types.ErrorCodeAccessDenied, types.ErrOptionWithSkipRetry())
	}

	userID := c.GetInt("id")
	userCache, err := model.GetUserCache(userID)
	if err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	userCache.WriteContext(c)

	tokens, err := model.GetAllUserTokens(userID, 0, 100)
	if err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	now := time.Now().Unix()
	var billingToken *model.Token
	if requireBalance {
		billingToken = selectPlaygroundBillingToken(tokens, now)
	} else {
		billingToken = selectPlaygroundAccessToken(tokens, now)
	}
	if billingToken == nil {
		return types.NewErrorWithStatusCode(errors.New("请先创建一个可用的 API 密钥"), types.ErrorCodeAccessDenied, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if err = middleware.SetupContextForToken(c, billingToken); err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	return nil
}

func Playground(c *gin.Context) {
	if newAPIError := preparePlayground(c, true); newAPIError != nil {
		writePlaygroundError(c, newAPIError)
		return
	}
	Relay(c, types.RelayFormatOpenAI)
}

func PlaygroundImage(c *gin.Context) {
	if newAPIError := preparePlayground(c, true); newAPIError != nil {
		writePlaygroundError(c, newAPIError)
		return
	}
	Relay(c, types.RelayFormatOpenAIImage)
}

func PlaygroundVideo(c *gin.Context) {
	if newAPIError := preparePlayground(c, true); newAPIError != nil {
		writePlaygroundError(c, newAPIError)
		return
	}
	RelayTask(c)
}

func PlaygroundVideoFetch(c *gin.Context) {
	if newAPIError := preparePlayground(c, false); newAPIError != nil {
		writePlaygroundError(c, newAPIError)
		return
	}
	RelayTaskFetch(c)
}
