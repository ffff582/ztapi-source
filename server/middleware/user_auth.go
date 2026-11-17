package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ZTAPIUserAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || parts[0] != "Bearer" {
			rejectZTAPIUserAuth(c)
			return
		}
		claims, err := service.ParseZTAPIAccessToken(parts[1], time.Now().UTC())
		if err != nil {
			rejectZTAPIUserAuth(c)
			return
		}
		userID, err := strconv.Atoi(claims.Subject)
		if err != nil {
			rejectZTAPIUserAuth(c)
			return
		}
		user, err := model.GetUserById(userID, false)
		if err != nil ||
			user.Status != common.UserStatusEnabled ||
			!common.IsValidateRole(user.Role) ||
			strings.TrimSpace(user.Username) == "" {
			rejectZTAPIUserAuth(c)
			return
		}

		c.Set("id", user.Id)
		c.Set("username", user.Username)
		c.Set("role", user.Role)
		c.Set("status", user.Status)
		c.Set("group", user.Group)
		c.Set("user_group", user.Group)
		c.Set("use_access_token", true)
		c.Next()
	}
}

func rejectZTAPIUserAuth(c *gin.Context) {
	if !c.IsAborted() {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "unauthorized",
		})
		c.Abort()
	}
}
