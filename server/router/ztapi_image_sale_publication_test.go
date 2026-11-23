package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageSalePublicationRouteRequiresRoot(t *testing.T) {
	db := setupZTAPIAuthRouterTestDB(t)
	engine := newZTAPIRealRouter(t, false)
	var current model.User
	engine.GET("/__test/image-sale-session", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("id", current.Id)
		session.Set("username", current.Username)
		session.Set("role", current.Role)
		session.Set("status", current.Status)
		session.Set("group", current.Group)
		require.NoError(t, session.Save())
		c.Status(http.StatusNoContent)
	})
	for _, role := range []int{common.RoleCommonUser, common.RoleFinanceUser, common.RoleAdminUser, common.RoleRootUser} {
		current = model.User{Username: "image-sale-" + strconv.Itoa(role), AffCode: "image-sale-" + strconv.Itoa(role), Role: role, Status: common.UserStatusEnabled, Group: "default"}
		require.NoError(t, db.Create(&current).Error)
		seed := httptest.NewRecorder()
		engine.ServeHTTP(seed, httptest.NewRequest(http.MethodGet, "/__test/image-sale-session", nil))
		cookies := seed.Result().Cookies()
		require.NotEmpty(t, cookies)
		request := httptest.NewRequest(http.MethodPost, "/api/models/ztapi/image-sale/publish", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("New-API-User", strconv.Itoa(current.Id))
		request.AddCookie(cookies[0])
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		if role == common.RoleRootUser {
			require.Equal(t, http.StatusBadRequest, recorder.Code, "root reaches strict request validation")
		} else {
			// Legacy RootAuth returns HTTP 200 with success:false on role denial.
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"success":false`, "non-root must not reach the publisher")
		}
	}
}
