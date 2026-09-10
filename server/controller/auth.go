package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	ztAPIRefreshCookieName   = "ztapi_refresh"
	ztAPIRefreshCookiePath   = "/api/auth"
	ztAPIRefreshCookieMaxAge = 30 * 24 * 60 * 60
)

type ztAPIAuthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type ZTAPIUserDTO struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     int    `json:"role"`
	Group    string `json:"group"`
}

type ztAPIAuthData struct {
	AccessToken string       `json:"access_token"`
	ExpiresIn   int          `json:"expires_in"`
	User        ZTAPIUserDTO `json:"user"`
}

var ztAPIVerifyPassword = service.VerifyZTAPIPassword

func ZTAPIRegister(c *gin.Context) {
	if !common.RegisterEnabled || !common.PasswordRegisterEnabled {
		writeZTAPIAuthError(c, http.StatusForbidden, "registration disabled")
		return
	}
	request, username, ok := decodeZTAPIRegistration(c)
	if !ok {
		return
	}

	exists, err := model.CheckUserExistOrDeleted(username, "")
	if err != nil {
		writeZTAPIAuthError(c, http.StatusInternalServerError, "internal server error")
		return
	}
	if exists {
		writeZTAPIAuthError(c, http.StatusConflict, "username unavailable")
		return
	}

	encodedPassword, err := service.HashZTAPIPassword(request.Password)
	if err != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid registration")
		return
	}
	user, err := model.CreateZTAPIUserWithEncodedPassword(username, encodedPassword)
	if err != nil {
		exists, lookupErr := model.CheckUserExistOrDeleted(username, "")
		if lookupErr == nil && exists {
			writeZTAPIAuthError(c, http.StatusConflict, "username unavailable")
			return
		}
		writeZTAPIAuthError(c, http.StatusInternalServerError, "internal server error")
		return
	}
	issueZTAPIAuthSession(c, user)
}

func ZTAPILogin(c *gin.Context) {
	var request ztAPIAuthRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		writeZTAPIAuthError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	username, err := service.NormalizeZTAPIUsername(request.Username)
	if err != nil || service.ValidateZTAPIPassword(request.Password) != nil {
		writeZTAPIAuthError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}

	user, lookupErr := model.GetZTAPIUserForLogin(username)
	userExists := lookupErr == nil && user != nil
	verificationHash, storedHashValid := service.ZTAPIPasswordHashOrDummy("")
	if userExists {
		verificationHash, storedHashValid = service.ZTAPIPasswordHashOrDummy(user.Password)
	}
	passwordMatches := ztAPIVerifyPassword(request.Password, verificationHash)

	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		writeZTAPIAuthError(c, http.StatusInternalServerError, "internal server error")
		return
	}
	if !userExists || !storedHashValid ||
		user.Status != common.UserStatusEnabled || !passwordMatches {
		writeZTAPIAuthError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if common.IsZTAPIAdminHost(c.Request.Host) &&
		!common.HasAdminPermission(user.Role, common.PermissionOverviewRead) {
		writeZTAPIAuthError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	issueZTAPIAuthSession(c, user)
}

func ZTAPIRefresh(c *gin.Context) {
	cookie, err := c.Cookie(ztAPIRefreshCookieName)
	if err != nil || cookie == "" {
		writeZTAPIAuthError(c, http.StatusUnauthorized, "invalid session")
		return
	}
	tokens, err := service.RotateZTAPISession(
		cookie,
		c.ClientIP(),
		c.Request.UserAgent(),
		time.Now().UTC(),
	)
	if err != nil {
		writeZTAPIAuthError(c, http.StatusUnauthorized, "invalid session")
		return
	}
	user, err := model.GetUserById(tokens.UserID, false)
	if err != nil || user.Status != common.UserStatusEnabled {
		_ = service.RevokeZTAPISessionFamily(tokens.RefreshToken, time.Now().UTC())
		writeZTAPIAuthError(c, http.StatusUnauthorized, "invalid session")
		return
	}
	if common.IsZTAPIAdminHost(c.Request.Host) &&
		!common.HasAdminPermission(user.Role, common.PermissionOverviewRead) {
		_ = service.RevokeZTAPISessionFamily(tokens.RefreshToken, time.Now().UTC())
		clearZTAPIRefreshCookie(c)
		writeZTAPIAuthError(c, http.StatusUnauthorized, "invalid session")
		return
	}
	setZTAPIRefreshCookie(c, tokens.RefreshToken, ztAPIRefreshCookieMaxAge)
	writeZTAPIAuthSuccess(c, tokens, user)
}

func ZTAPILogout(c *gin.Context) {
	clearZTAPIRefreshCookie(c)
	cookie, err := c.Cookie(ztAPIRefreshCookieName)
	if err == nil && cookie != "" {
		if err := service.RevokeZTAPISessionFamily(cookie, time.Now().UTC()); err != nil {
			writeZTAPIAuthError(c, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func ZTAPISession(c *gin.Context) {
	userID := c.GetInt("id")
	username := c.GetString("username")
	role := c.GetInt("role")
	group := c.GetString("group")
	if userID <= 0 || username == "" {
		writeZTAPIAuthError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": ZTAPIUserDTO{
			ID:       userID,
			Username: username,
			Role:     role,
			Group:    group,
		},
	})
}

func decodeZTAPIRegistration(c *gin.Context) (ztAPIAuthRequest, string, bool) {
	var request ztAPIAuthRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid registration")
		return ztAPIAuthRequest{}, "", false
	}
	username, err := service.NormalizeZTAPIUsername(request.Username)
	if err != nil || service.ValidateZTAPIPassword(request.Password) != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid registration")
		return ztAPIAuthRequest{}, "", false
	}
	return request, username, true
}

func issueZTAPIAuthSession(c *gin.Context, user *model.User) {
	tokens, err := service.CreateZTAPISession(
		user.Id,
		c.ClientIP(),
		c.Request.UserAgent(),
		time.Now().UTC(),
	)
	if err != nil {
		writeZTAPIAuthError(c, http.StatusInternalServerError, "internal server error")
		return
	}
	setZTAPIRefreshCookie(c, tokens.RefreshToken, ztAPIRefreshCookieMaxAge)
	writeZTAPIAuthSuccess(c, tokens, user)
}

func writeZTAPIAuthSuccess(c *gin.Context, tokens service.ZTAPISessionTokens, user *model.User) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": ztAPIAuthData{
			AccessToken: tokens.AccessToken,
			ExpiresIn:   tokens.ExpiresIn,
			User: ZTAPIUserDTO{
				ID:       user.Id,
				Username: user.Username,
				Role:     user.Role,
				Group:    user.Group,
			},
		},
	})
}

func writeZTAPIAuthError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"success": false,
		"message": message,
	})
}

func setZTAPIRefreshCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     ztAPIRefreshCookieName,
		Value:    value,
		Path:     ztAPIRefreshCookiePath,
		MaxAge:   maxAge,
		Expires:  time.Now().UTC().Add(time.Duration(maxAge) * time.Second),
		HttpOnly: true,
		Secure:   gin.Mode() == gin.ReleaseMode,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearZTAPIRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     ztAPIRefreshCookieName,
		Value:    "",
		Path:     ztAPIRefreshCookiePath,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
		HttpOnly: true,
		Secure:   gin.Mode() == gin.ReleaseMode,
		SameSite: http.SameSiteStrictMode,
	})
}
