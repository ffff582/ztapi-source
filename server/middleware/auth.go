package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func validUserInfo(username string, role int) bool {
	// check username is empty
	if strings.TrimSpace(username) == "" {
		return false
	}
	if !common.IsValidateRole(role) {
		return false
	}
	return true
}

const authReauthCode = "AUTH_REAUTH_REQUIRED"

func authReauthFailure(message string) gin.H {
	return gin.H{
		"success": false,
		"code":    authReauthCode,
		"message": message,
	}
}

// 防止不同newapi版本冲突，导致数据不通用

// 管理/root 写操作审计兜底：内聚在鉴权链路里，保证任何经过 AdminAuth/RootAuth
// 的写接口都会自动留痕（无需在路由上单独挂审计中间件，避免漏挂）。
// handler 内手动埋点者会设置 ContextKeyAuditLogged，finishAdminAudit 据此跳过。
var auditWriter *auditResponseWriter

type authenticatedIdentity struct {
	username       string
	role           int
	id             int
	status         int
	group          interface{}
	useAccessToken bool
	ztapiJWT       bool
}

func loadAuthenticatedIdentity(c *gin.Context) (*authenticatedIdentity, bool) {
	session := sessions.Default(c)
	identity := &authenticatedIdentity{group: session.Get("group")}
	usernameValue := session.Get("username")
	if usernameValue == nil {
		accessToken := c.Request.Header.Get("Authorization")
		if accessToken == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgAuthNotLoggedIn),
			})
			c.Abort()
			return nil, false
		}
		var user *model.User
		var authErr error
		ztapiJWTParsed := false
		parts := strings.Fields(accessToken)
		if len(parts) == 2 && parts[0] == "Bearer" {
			if claims, jwtErr := service.ParseZTAPIAccessToken(parts[1], time.Now().UTC()); jwtErr == nil {
				ztapiJWTParsed = true
				identity.ztapiJWT = true
				if userID, parseErr := strconv.Atoi(claims.Subject); parseErr == nil {
					user, authErr = model.GetUserById(userID, false)
					if errors.Is(authErr, gorm.ErrRecordNotFound) {
						user = nil
						authErr = nil
					} else if authErr != nil {
						authErr = fmt.Errorf("%w: %v", model.ErrDatabase, authErr)
					}
				}
			}
		}
		if !ztapiJWTParsed {
			user, authErr = model.ValidateAccessToken(accessToken)
		}
		if authErr != nil {
			if errors.Is(authErr, model.ErrDatabase) {
				common.SysLog("ValidateAccessToken database error: " + authErr.Error())
				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
				})
			} else {
				c.JSON(http.StatusOK, authReauthFailure(
					common.TranslateMessage(c, i18n.MsgAuthAccessTokenInvalid),
				))
			}
			c.Abort()
			return nil, false
		}
		if user == nil || user.Username == "" || !validUserInfo(user.Username, user.Role) {
			c.JSON(http.StatusOK, authReauthFailure(
				common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid),
			))
			c.Abort()
			return nil, false
		}
		identity.username = user.Username
		identity.role = user.Role
		identity.id = user.Id
		identity.status = user.Status
		identity.useAccessToken = true
	} else {
		var ok bool
		identity.username, ok = usernameValue.(string)
		if !ok {
			c.JSON(http.StatusOK, authReauthFailure(common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)))
			c.Abort()
			return nil, false
		}
		identity.role, ok = session.Get("role").(int)
		if !ok {
			c.JSON(http.StatusOK, authReauthFailure(common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)))
			c.Abort()
			return nil, false
		}
		identity.id, ok = session.Get("id").(int)
		if !ok {
			c.JSON(http.StatusOK, authReauthFailure(common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)))
			c.Abort()
			return nil, false
		}
		identity.status, ok = session.Get("status").(int)
		if !ok {
			c.JSON(http.StatusOK, authReauthFailure(common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)))
			c.Abort()
			return nil, false
		}
		currentUser, currentErr := model.GetUserById(identity.id, false)
		if currentErr != nil || currentUser == nil {
			c.JSON(http.StatusOK, authReauthFailure(common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)))
			c.Abort()
			return nil, false
		}
		identity.username = currentUser.Username
		identity.role = currentUser.Role
		identity.status = currentUser.Status
		identity.group = currentUser.Group
	}

	apiUserIDString := c.Request.Header.Get("New-Api-User")
	if apiUserIDString == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthUserIdNotProvided),
		})
		c.Abort()
		return nil, false
	}
	apiUserID, err := strconv.Atoi(apiUserIDString)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthUserIdFormatError),
		})
		c.Abort()
		return nil, false
	}
	if identity.id != apiUserID {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthUserIdMismatch),
		})
		c.Abort()
		return nil, false
	}
	if identity.status == common.UserStatusDisabled {
		c.JSON(http.StatusOK, authReauthFailure(common.TranslateMessage(c, i18n.MsgAuthUserBanned)))
		c.Abort()
		return nil, false
	}
	if !validUserInfo(identity.username, identity.role) {
		c.JSON(http.StatusOK, authReauthFailure(common.TranslateMessage(c, i18n.MsgAuthUserInfoInvalid)))
		c.Abort()
		return nil, false
	}
	return identity, true
}

func setAuthenticatedIdentity(c *gin.Context, identity *authenticatedIdentity) {
	c.Header("Auth-Version", "864b7076dbcd0a3c01b5520316720ebf")
	c.Set("username", identity.username)
	c.Set("role", identity.role)
	c.Set("id", identity.id)
	c.Set("group", identity.group)
	c.Set("user_group", identity.group)
	c.Set("use_access_token", identity.useAccessToken)
	common.SetContextKey(c, constant.ContextKeyZTAPIJWTAuthenticated, identity.ztapiJWT)
}

func legacyRoleAllowed(role int, minRole int) bool {
	switch minRole {
	case common.RoleCommonUser:
		return role == common.RoleCommonUser || role == common.RoleSupportUser || role == common.RoleFinanceUser || role == common.RoleAdminUser || role == common.RoleRootUser
	case common.RoleAdminUser:
		return role == common.RoleAdminUser || role == common.RoleRootUser
	case common.RoleRootUser:
		return role == common.RoleRootUser
	default:
		return false
	}
}

func authHelper(c *gin.Context, minRole int) {
	identity, ok := loadAuthenticatedIdentity(c)
	if !ok {
		return
	}
	if !legacyRoleAllowed(identity.role, minRole) {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
		})
		c.Abort()
		return
	}
	setAuthenticatedIdentity(c, identity)
	var auditWriter *auditResponseWriter
	if minRole == common.RoleAdminUser || minRole == common.RoleRootUser {
		auditWriter = beginAdminAudit(c)
	}
	c.Next()
	finishAdminAudit(c, auditWriter)
}

func AdminPermissionAuth(permission common.AdminPermission) func(c *gin.Context) {
	return func(c *gin.Context) {
		identity, ok := loadAuthenticatedIdentity(c)
		if !ok {
			return
		}
		if !common.HasAdminPermission(identity.role, permission) {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
			})
			c.Abort()
			return
		}
		setAuthenticatedIdentity(c, identity)
		auditWriter := beginAdminAudit(c)
		c.Next()
		finishAdminAudit(c, auditWriter)
	}
}

func TryUserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		id := session.Get("id")
		if id != nil {
			c.Set("id", id)
		}
		c.Next()
	}
}

func UserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleCommonUser)
	}
}

func AdminAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleAdminUser)
	}
}

func RootAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, common.RoleRootUser)
	}
}

func WssAuth(c *gin.Context) {

}

// TokenOrUserAuth allows either session-based user auth or API token auth.
// Used for endpoints that need to be accessible from both the dashboard and API clients.
func TokenOrUserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		// Try session auth first (dashboard users)
		session := sessions.Default(c)
		if id := session.Get("id"); id != nil {
			if status, ok := session.Get("status").(int); ok && status == common.UserStatusEnabled {
				c.Set("id", id)
				c.Next()
				return
			}
		}
		// Fall back to token auth (API clients)
		TokenAuth()(c)
	}
}

// TokenAuthReadOnly 宽松版本的令牌认证中间件，用于只读查询接口。
// 只验证令牌 key 是否存在，不检查令牌状态、过期时间和额度。
// 即使令牌已过期、已耗尽或已禁用，也允许访问。
// 仍然检查用户是否被封禁。
func TokenAuthReadOnly() func(c *gin.Context) {
	return func(c *gin.Context) {
		key, ok := extractZTAPICredential(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgTokenNotProvided),
			})
			c.Abort()
			return
		}
		keyHash := common.HashZTAPIKey(key)

		token, err := model.GetTokenByHash(keyHash, false)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgTokenInvalid),
				})
			} else {
				common.SysLog("TokenAuthReadOnly GetTokenByHash database error: " + err.Error())
				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
				})
			}
			c.Abort()
			return
		}

		userCache, err := model.GetUserCache(token.UserId)
		if err != nil {
			common.SysLog(fmt.Sprintf("TokenAuthReadOnly GetUserCache error for user %d: %v", token.UserId, err))
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
			})
			c.Abort()
			return
		}
		if userCache.Status != common.UserStatusEnabled {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgAuthUserBanned),
			})
			c.Abort()
			return
		}

		c.Set("id", token.UserId)
		c.Set("token_id", token.Id)
		common.SetContextKey(c, constant.ContextKeyTokenKeyHash, token.KeyHash)
		c.Next()
	}
}

type AuthenticatedProtocol string

const (
	AuthenticatedProtocolOpenAI    AuthenticatedProtocol = "openai"
	AuthenticatedProtocolAnthropic AuthenticatedProtocol = "anthropic"
	AuthenticatedProtocolGemini    AuthenticatedProtocol = "gemini"
)

type ztAPICredentialCandidate struct {
	value    string
	protocol AuthenticatedProtocol
}

func GetAuthenticatedProtocol(c *gin.Context) AuthenticatedProtocol {
	value, ok := common.GetContextKey(c, constant.ContextKeyAuthenticatedProtocol)
	if !ok {
		return ""
	}
	protocol, _ := value.(AuthenticatedProtocol)
	return protocol
}

func extractZTAPICredential(c *gin.Context) (string, bool) {
	const keyProtocolPrefix = "openai-insecure-api-key."
	candidates := make([]ztAPICredentialCandidate, 0, 5)
	path := c.Request.URL.Path

	if isWebSocketRequest(c.Request) {
		for _, protocol := range strings.Split(c.GetHeader("Sec-WebSocket-Protocol"), ",") {
			protocol = strings.TrimSpace(protocol)
			if strings.HasPrefix(protocol, keyProtocolPrefix) {
				candidates = append(candidates, ztAPICredentialCandidate{
					value:    strings.TrimPrefix(protocol, keyProtocolPrefix),
					protocol: AuthenticatedProtocolOpenAI,
				})
			}
		}
	}

	if isAnthropicCredentialPath(c.Request) {
		candidates = appendCredentialCandidates(
			candidates,
			nonEmptyHeaderValues(c.Request.Header, "x-api-key"),
			AuthenticatedProtocolAnthropic,
		)
	}

	if isGeminiCredentialPath(path) {
		candidates = appendCredentialCandidates(
			candidates,
			nonEmptyHeaderValues(c.Request.Header, "x-goog-api-key"),
			AuthenticatedProtocolGemini,
		)
		for _, value := range c.Request.URL.Query()["key"] {
			if value != "" {
				candidates = append(candidates, ztAPICredentialCandidate{
					value:    value,
					protocol: AuthenticatedProtocolGemini,
				})
			}
		}
	}

	for _, authorization := range nonEmptyHeaderValues(c.Request.Header, "Authorization") {
		if len(authorization) >= len("Bearer ") &&
			strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
			candidates = append(candidates, ztAPICredentialCandidate{
				value:    authorization[len("Bearer "):],
				protocol: AuthenticatedProtocolOpenAI,
			})
		}
	}

	scrubZTAPICredentialSources(c.Request)

	var credential string
	var protocol AuthenticatedProtocol
	for _, candidate := range candidates {
		if credential == "" {
			credential = candidate.value
			protocol = candidate.protocol
			continue
		}
		if candidate.value != credential {
			return "", false
		}
	}
	if !common.IsValidZTAPIKey(credential) {
		return "", false
	}
	common.SetContextKey(c, constant.ContextKeyAuthenticatedProtocol, protocol)
	return credential, true
}

func appendCredentialCandidates(
	candidates []ztAPICredentialCandidate,
	values []string,
	protocol AuthenticatedProtocol,
) []ztAPICredentialCandidate {
	for _, value := range values {
		candidates = append(candidates, ztAPICredentialCandidate{
			value:    value,
			protocol: protocol,
		})
	}
	return candidates
}

func nonEmptyHeaderValues(header http.Header, name string) []string {
	values := header.Values(name)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func isGeminiCredentialPath(path string) bool {
	return path == "/v1/models" ||
		strings.HasPrefix(path, "/v1/models/") ||
		strings.HasPrefix(path, "/v1beta/models") ||
		strings.HasPrefix(path, "/v1beta/openai/models") ||
		strings.HasPrefix(path, "/v1beta/models/")
}

func isAnthropicCredentialPath(request *http.Request) bool {
	if request == nil || request.URL == nil {
		return false
	}
	path := request.URL.Path
	if path == "/v1/messages" {
		return true
	}
	isModelRoute := path == "/v1/models" || strings.HasPrefix(path, "/v1/models/")
	return isModelRoute && request.Header.Get("anthropic-version") != ""
}

func isWebSocketRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(request.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, token := range strings.Split(request.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

func scrubZTAPICredentialSources(request *http.Request) {
	if request == nil {
		return
	}
	request.Header.Del("Authorization")
	request.Header.Del("x-api-key")
	request.Header.Del("x-goog-api-key")

	const keyProtocolPrefix = "openai-insecure-api-key."
	protocols := request.Header.Values("Sec-WebSocket-Protocol")
	request.Header.Del("Sec-WebSocket-Protocol")
	for _, headerValue := range protocols {
		safeProtocols := make([]string, 0)
		for _, protocol := range strings.Split(headerValue, ",") {
			protocol = strings.TrimSpace(protocol)
			if protocol == "" || strings.HasPrefix(protocol, keyProtocolPrefix) {
				continue
			}
			safeProtocols = append(safeProtocols, protocol)
		}
		if len(safeProtocols) > 0 {
			request.Header.Add("Sec-WebSocket-Protocol", strings.Join(safeProtocols, ", "))
		}
	}

	if request.URL != nil && isGeminiCredentialPath(request.URL.Path) {
		query := request.URL.Query()
		query.Del("key")
		request.URL.RawQuery = query.Encode()
		request.RequestURI = request.URL.RequestURI()
	}
}

func TokenAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		strictCredential, ok := extractZTAPICredential(c)
		if !ok {
			abortWithOpenAiMessage(c, http.StatusUnauthorized,
				common.TranslateMessage(c, i18n.MsgTokenInvalid))
			return
		}

		token, err := model.ValidateUserToken(strictCredential)
		if token != nil {
			id := c.GetInt("id")
			if id == 0 {
				c.Set("id", token.UserId)
			}
		}
		if err != nil {
			if errors.Is(err, model.ErrDatabase) {
				common.SysLog("TokenAuth ValidateUserToken database error: " + err.Error())
				abortWithOpenAiMessage(c, http.StatusInternalServerError,
					common.TranslateMessage(c, i18n.MsgDatabaseError))
			} else {
				abortWithOpenAiMessage(c, http.StatusUnauthorized,
					common.TranslateMessage(c, i18n.MsgTokenInvalid))
			}
			return
		}

		allowIps := token.GetIpLimits()
		if len(allowIps) > 0 {
			clientIp := c.ClientIP()
			logger.LogDebug(c, "Token has IP restrictions, checking client IP %s", clientIp)
			ip := net.ParseIP(clientIp)
			if ip == nil {
				abortWithOpenAiMessage(c, http.StatusForbidden, "无法解析客户端 IP 地址")
				return
			}
			if common.IsIpInCIDRList(ip, allowIps) == false {
				abortWithOpenAiMessage(c, http.StatusForbidden, "您的 IP 不在令牌允许访问的列表中", types.ErrorCodeAccessDenied)
				return
			}
			logger.LogDebug(c, "Client IP %s passed the token IP restrictions check", clientIp)
		}

		userCache, err := model.GetUserCache(token.UserId)
		if err != nil {
			common.SysLog(fmt.Sprintf("TokenAuth GetUserCache error for user %d: %v", token.UserId, err))
			abortWithOpenAiMessage(c, http.StatusInternalServerError,
				common.TranslateMessage(c, i18n.MsgDatabaseError))
			return
		}
		userEnabled := userCache.Status == common.UserStatusEnabled
		if !userEnabled {
			abortWithOpenAiMessage(c, http.StatusForbidden, common.TranslateMessage(c, i18n.MsgAuthUserBanned))
			return
		}

		userCache.WriteContext(c)

		userGroup := userCache.Group
		tokenGroup := token.Group
		if tokenGroup != "" {
			// check common.UserUsableGroups[userGroup]
			if _, ok := service.GetUserUsableGroups(userGroup)[tokenGroup]; !ok {
				abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf("无权访问 %s 分组", tokenGroup))
				return
			}
			// check group in common.GroupRatio
			if !ratio_setting.ContainsGroupRatio(tokenGroup) {
				if tokenGroup != "auto" {
					abortWithOpenAiMessage(c, http.StatusForbidden, fmt.Sprintf("分组 %s 已被弃用", tokenGroup))
					return
				}
			}
			userGroup = tokenGroup
		}
		common.SetContextKey(c, constant.ContextKeyUsingGroup, userGroup)

		err = SetupContextForToken(c, token)
		if err != nil {
			return
		}
		c.Next()
	}
}

func SetupContextForToken(c *gin.Context, token *model.Token, parts ...string) error {
	if token == nil {
		return fmt.Errorf("token is nil")
	}
	c.Set("id", token.UserId)
	c.Set("token_id", token.Id)
	common.SetContextKey(c, constant.ContextKeyTokenKeyHash, token.KeyHash)
	c.Set("token_name", token.Name)
	c.Set("token_unlimited_quota", token.UnlimitedQuota)
	if !token.UnlimitedQuota {
		c.Set("token_quota", token.RemainQuota)
	}
	if token.ModelLimitsEnabled {
		c.Set("token_model_limit_enabled", true)
		c.Set("token_model_limit", token.GetModelLimitsMap())
	} else {
		c.Set("token_model_limit_enabled", false)
	}
	common.SetContextKey(c, constant.ContextKeyTokenGroup, token.Group)
	common.SetContextKey(c, constant.ContextKeyTokenCrossGroupRetry, token.CrossGroupRetry)
	if len(parts) > 1 {
		if model.IsAdmin(token.UserId) {
			c.Set("specific_channel_id", parts[1])
		} else {
			c.Header("specific_channel_version", "701e3ae1dc3f7975556d354e0675168d004891c8")
			abortWithOpenAiMessage(c, http.StatusForbidden, "普通用户不支持指定渠道")
			return fmt.Errorf("普通用户不支持指定渠道")
		}
	}
	return nil
}
