package controller

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	ztAPIRefreshCookieName   = "ztapi_refresh"
	ztAPIRefreshCookiePath   = "/api/auth"
	ztAPIRefreshCookieMaxAge = 30 * 24 * 60 * 60
)

type ztAPIAuthRequest struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	Email            string `json:"email"`
	VerificationCode string `json:"verification_code"`
	CaptchaID        string `json:"captcha_id"`
	CaptchaCode      string `json:"captcha_code"`
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

type ztAPIPasswordResetRequest struct {
	Email string `json:"email"`
}

type ztAPIPasswordResetConfirmRequest struct {
	Email       string `json:"email"`
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

var ztAPIVerifyPassword = service.VerifyZTAPIPassword
var ztAPISendEmail = common.SendEmail
var ztAPIConsumeCaptcha = service.ConsumeZTAPICaptcha

func ZTAPIRegister(c *gin.Context) {
	if !common.RegisterEnabled || !common.PasswordRegisterEnabled {
		writeZTAPIAuthError(c, http.StatusForbidden, "registration disabled")
		return
	}
	request, username, ok := decodeZTAPIRegistration(c)
	if !ok {
		return
	}

	pendingEmail := strings.ToLower(strings.TrimSpace(request.Email))
	// An optional registration address remains pending until the signed-in
	// user proves ownership. It therefore cannot claim password recovery.
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
	user, err := model.CreateZTAPIUserWithEncodedPasswordAndPendingEmail(username, encodedPassword, pendingEmail)
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

func ZTAPIRequestPasswordReset(c *gin.Context) {
	if !ztAPIPasswordResetEnabled() {
		writeZTAPIAuthError(c, http.StatusServiceUnavailable, "password reset unavailable")
		return
	}
	var request ztAPIPasswordResetRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil ||
		common.Validate.Var(strings.TrimSpace(request.Email), "required,email") != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid password reset request")
		return
	}
	email := strings.ToLower(strings.TrimSpace(request.Email))
	if _, err := model.GetZTAPIUserByEmail(email); err == nil {
		token := common.GenerateVerificationCode(0)
		common.RegisterVerificationCodeWithKey(email, token, common.PasswordResetPurpose)
		link := fmt.Sprintf(
			"%s/reset-password?email=%s&token=%s",
			strings.TrimRight(system_setting.ServerAddress, "/"),
			url.QueryEscape(email),
			url.QueryEscape(token),
		)
		subject := fmt.Sprintf("%s 密码重置", common.SystemName)
		content := fmt.Sprintf(
			"<p>您好，您正在重置 %s 账号密码。</p><p><a href='%s'>点击这里设置新密码</a></p><p>链接 %d 分钟内有效且只能使用一次。如果不是本人操作，请忽略。</p>",
			html.EscapeString(common.SystemName),
			html.EscapeString(link),
			common.VerificationValidMinutes,
		)
		if err := ztAPISendEmail(subject, email, content); err != nil {
			common.DeleteKey(email, common.PasswordResetPurpose)
			logger.LogError(c.Request.Context(), fmt.Sprintf("failed to send ZTAPI password reset email to %s: %s", common.MaskEmail(email), err.Error()))
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// Registration no longer reads the email verification switch, so password
// reset must not depend on it either: configured mail is all it needs.
func ztAPIPasswordResetEnabled() bool {
	return strings.TrimSpace(common.SMTPServer) != "" &&
		strings.TrimSpace(common.SMTPFrom) != ""
}

func ZTAPIConfirmPasswordReset(c *gin.Context) {
	var request ztAPIPasswordResetConfirmRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil ||
		common.Validate.Var(strings.TrimSpace(request.Email), "required,email") != nil ||
		strings.TrimSpace(request.Token) == "" ||
		service.ValidateZTAPIPassword(request.NewPassword) != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid password reset")
		return
	}
	email := strings.ToLower(strings.TrimSpace(request.Email))
	token := strings.TrimSpace(request.Token)
	if !common.ConsumeVerificationCodeWithKey(email, token, common.PasswordResetPurpose) {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid password reset")
		return
	}
	encodedPassword, err := service.HashZTAPIPassword(request.NewPassword)
	if err != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid password reset")
		return
	}
	if err := model.ResetZTAPIUserPasswordByEmail(email, encodedPassword, time.Now().UTC()); err != nil {
		common.RegisterVerificationCodeWithKey(email, token, common.PasswordResetPurpose)
		writeZTAPIAuthError(c, http.StatusInternalServerError, "password reset failed")
		return
	}
	clearZTAPIRefreshCookie(c)
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
	if email := strings.TrimSpace(request.Email); email != "" && common.Validate.Var(email, "email") != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid registration")
		return ztAPIAuthRequest{}, "", false
	}
	// The challenge is graded last so a failed one cannot be used to probe
	// which usernames or passwords the server would have accepted.
	if !ztAPIConsumeCaptcha(request.CaptchaID, request.CaptchaCode, time.Now().UTC()) {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid captcha")
		return ztAPIAuthRequest{}, "", false
	}
	return request, username, true
}

type ztAPIEmailRequest struct {
	Email            string `json:"email"`
	VerificationCode string `json:"verification_code"`
}

func normalizeZTAPIEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	return email, email != "" && common.Validate.Var(email, "email") == nil
}

func ZTAPIRequestEmailVerification(c *gin.Context) {
	if !ztAPIPasswordResetEnabled() {
		writeZTAPIAuthError(c, http.StatusServiceUnavailable, "email unavailable")
		return
	}
	var request ztAPIEmailRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid email")
		return
	}
	email, valid := normalizeZTAPIEmail(request.Email)
	if !valid {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid email")
		return
	}
	taken, err := model.ZTAPIEmailBelongsToAnotherUser(c.GetInt("id"), email)
	if err != nil {
		writeZTAPIAuthError(c, http.StatusInternalServerError, "email verification failed")
		return
	}
	if taken {
		writeZTAPIAuthError(c, http.StatusConflict, "email unavailable")
		return
	}
	code := common.GenerateVerificationCode(6)
	subject := fmt.Sprintf("%s 邮箱验证", common.SystemName)
	content := fmt.Sprintf(
		"<p>您好，您正在绑定 %s 账号邮箱。</p><p>验证码：<strong>%s</strong></p><p>验证码 %d 分钟内有效且只能使用一次。</p>",
		html.EscapeString(common.SystemName), html.EscapeString(code), common.VerificationValidMinutes,
	)
	common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose)
	if err := ztAPISendEmail(subject, email, content); err != nil {
		common.DeleteKey(email, common.EmailVerificationPurpose)
		logger.LogError(c.Request.Context(), fmt.Sprintf("failed to send ZTAPI email verification to %s: %s", common.MaskEmail(email), err.Error()))
		writeZTAPIAuthError(c, http.StatusServiceUnavailable, "email verification failed")
		return
	}
	if err := model.SetZTAPIPendingEmail(c.GetInt("id"), email); err != nil {
		common.DeleteKey(email, common.EmailVerificationPurpose)
		writeZTAPIAuthError(c, http.StatusInternalServerError, "email verification failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func ZTAPIVerifyAndBindEmail(c *gin.Context) {
	var request ztAPIEmailRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid email verification")
		return
	}
	email, valid := normalizeZTAPIEmail(request.Email)
	code := strings.TrimSpace(request.VerificationCode)
	if !valid || code == "" || !common.ConsumeVerificationCodeWithKey(email, code, common.EmailVerificationPurpose) {
		writeZTAPIAuthError(c, http.StatusBadRequest, "invalid email verification")
		return
	}
	if err := model.BindZTAPIUserEmail(c.GetInt("id"), email); err != nil {
		if errors.Is(err, model.ErrZTAPIEmailAlreadyBound) {
			writeZTAPIAuthError(c, http.StatusConflict, "email unavailable")
			return
		}
		writeZTAPIAuthError(c, http.StatusInternalServerError, "email verification failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// ZTAPIIssueCaptcha hands out one human-verification challenge for the
// registration form.
func ZTAPIIssueCaptcha(c *gin.Context) {
	captcha, err := service.IssueZTAPICaptcha(time.Now().UTC())
	if err != nil {
		writeZTAPIAuthError(c, http.StatusServiceUnavailable, "captcha unavailable")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": captcha})
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
