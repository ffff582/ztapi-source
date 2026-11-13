package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/gin-gonic/gin"
)

var ztAPIAdminHostBootstrapPaths = map[string]struct{}{
	"/api/auth/login":   {},
	"/api/auth/refresh": {},
	"/api/auth/logout":  {},
}

// ZTAPIAdminHostGate prevents ordinary customer identities from using the
// administration hostname as an alternate route to user-facing APIs.
func ZTAPIAdminHostGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !common.IsZTAPIAdminHost(c.Request.Host) {
			c.Next()
			return
		}
		if _, allowed := ztAPIAdminHostBootstrapPaths[c.Request.URL.Path]; allowed {
			c.Next()
			return
		}

		identity, ok := loadAuthenticatedIdentity(c)
		if !ok {
			return
		}
		if !common.HasAdminPermission(identity.role, common.PermissionOverviewRead) {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": common.TranslateMessage(c, i18n.MsgAuthInsufficientPrivilege),
			})
			c.Abort()
			return
		}
		setAuthenticatedIdentity(c, identity)
		c.Next()
	}
}
