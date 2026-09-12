/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type adminUserResponse struct {
	ID           int    `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Role         int    `json:"role"`
	Status       int    `json:"status"`
	Group        string `json:"group" gorm:"column:user_group"`
	Quota        int    `json:"quota"`
	UsedQuota    int    `json:"used_quota"`
	RequestCount int    `json:"request_count"`
	Remark       string `json:"remark"`
	CreatedAt    int64  `json:"created_at"`
	LastLoginAt  int64  `json:"last_login_at"`
}

type adminUserStatusRequest struct {
	Status         int    `json:"status"`
	ExpectedStatus int    `json:"expected_status"`
	Reason         string `json:"reason"`
}

type adminUserRoleRequest struct {
	Role         int    `json:"role"`
	ExpectedRole int    `json:"expected_role"`
	Reason       string `json:"reason"`
}

func GetZTAPIAdminUsers(c *gin.Context) {
	writeZTAPIAdminUserPage(c, false)
}

func GetZTAPIAdminStaff(c *gin.Context) {
	writeZTAPIAdminUserPage(c, true)
}

func writeZTAPIAdminUserPage(c *gin.Context, staffOnly bool) {
	pageInfo := common.GetPageQuery(c)
	query := model.DB.Model(&model.User{}).Where("deleted_at IS NULL")
	keyword := strings.TrimSpace(c.Query("keyword"))
	if staffOnly && keyword == "" {
		query = query.Where("role IN ?", []int{common.RoleSupportUser, common.RoleFinanceUser, common.RoleAdminUser, common.RoleRootUser})
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		includeEmail := adminViewerCanSearchEmail(c.GetInt("role"))
		if id, err := strconv.Atoi(keyword); err == nil && includeEmail {
			query = query.Where("id = ? OR username LIKE ? OR email LIKE ? OR display_name LIKE ?", id, like, like, like)
		} else if err == nil {
			query = query.Where("id = ? OR username LIKE ? OR display_name LIKE ?", id, like, like)
		} else if includeEmail {
			query = query.Where("username LIKE ? OR email LIKE ? OR display_name LIKE ?", like, like, like)
		} else {
			query = query.Where("username LIKE ? OR display_name LIKE ?", like, like)
		}
	}
	if value := c.Query("status"); value != "" {
		status, err := strconv.Atoi(value)
		if err != nil {
			writeAdminUserError(c, model.ErrAdminUserInvalidStatus)
			return
		}
		query = query.Where("status = ?", status)
	}
	if value := c.Query("role"); value != "" && !staffOnly {
		role, err := strconv.Atoi(value)
		if err != nil || !common.IsValidateRole(role) {
			writeAdminUserError(c, model.ErrAdminUserInvalidRole)
			return
		}
		query = query.Where("role = ?", role)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]adminUserResponse, 0)
	if err := query.Select(adminUserProjectionColumns()).Order("id DESC").Offset(pageInfo.GetStartIdx()).Limit(pageInfo.GetPageSize()).Scan(&items).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	redactAdminUserResponses(items, c.GetInt("role"))
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func GetZTAPIAdminUser(c *gin.Context) {
	userID, ok := parseAdminUserID(c)
	if !ok {
		return
	}
	var user adminUserResponse
	err := model.DB.Model(&model.User{}).Select(adminUserProjectionColumns()).Where("id = ? AND deleted_at IS NULL", userID).Scan(&user).Error
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if user.ID == 0 {
		writeAdminUserError(c, gorm.ErrRecordNotFound)
		return
	}
	redactAdminUserResponse(&user, c.GetInt("role"))
	common.ApiSuccess(c, user)
}

func adminUserProjectionColumns() string {
	groupColumn := "`group`"
	if model.DB.Dialector.Name() == "postgres" {
		groupColumn = `"group"`
	}
	return "id, username, display_name, email, role, status, " + groupColumn +
		" AS user_group, quota, used_quota, request_count, remark, created_at, last_login_at"
}

func UpdateZTAPIAdminUserStatus(c *gin.Context) {
	userID, ok := parseAdminUserID(c)
	if !ok {
		return
	}
	var request adminUserStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAdminUserError(c, err)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" {
		writeAdminUserError(c, errors.New("status change reason is required"))
		return
	}
	updated, err := model.UpdateZTAPIUserStatus(userID, c.GetInt("role"), request.Status, request.ExpectedStatus)
	cacheSyncPending := errors.Is(err, model.ErrAdminUserCacheSyncPending)
	if err != nil && !cacheSyncPending {
		writeAdminUserError(c, err)
		return
	}
	if cacheSyncPending {
		common.SysLog(fmt.Sprintf("user %d status committed with delayed cache synchronization: %s", updated.Id, err.Error()))
	}
	invalidateAdminUserSecurityCaches(updated.Id)
	auditResult := "committed"
	if cacheSyncPending {
		auditResult = "committed_cache_sync_pending"
	}
	recordManageAuditFor(c, updated.Id, "user.status_update", map[string]interface{}{
		"target_user_id": updated.Id,
		"from_status":    request.ExpectedStatus,
		"to_status":      updated.Status,
		"reason":         request.Reason,
		"result":         auditResult,
	})
	if cacheSyncPending {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    projectAdminUser(updated, c.GetInt("role")),
			"warning": "用户状态已更新，但缓存同步暂时延迟。",
		})
		return
	}
	common.ApiSuccess(c, projectAdminUser(updated, c.GetInt("role")))
}

func UpdateZTAPIAdminStaffRole(c *gin.Context) {
	userID, ok := parseAdminUserID(c)
	if !ok {
		return
	}
	var request adminUserRoleRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeAdminUserError(c, err)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" {
		writeAdminUserError(c, errors.New("role change reason is required"))
		return
	}
	updated, err := model.UpdateZTAPIUserRole(userID, request.Role, request.ExpectedRole)
	if err != nil {
		writeAdminUserError(c, err)
		return
	}
	invalidateAdminUserSecurityCaches(updated.Id)
	recordManageAuditFor(c, updated.Id, "user.role_update", map[string]interface{}{
		"target_user_id": updated.Id,
		"from_role":      request.ExpectedRole,
		"to_role":        updated.Role,
		"reason":         request.Reason,
	})
	common.ApiSuccess(c, projectAdminUser(updated, c.GetInt("role")))
}

func projectAdminUser(user *model.User, viewerRole int) adminUserResponse {
	projected := adminUserResponse{
		ID: user.Id, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email,
		Role: user.Role, Status: user.Status, Group: user.Group, Quota: user.Quota,
		UsedQuota: user.UsedQuota, RequestCount: user.RequestCount, Remark: user.Remark,
		CreatedAt: user.CreatedAt, LastLoginAt: user.LastLoginAt,
	}
	redactAdminUserResponse(&projected, viewerRole)
	return projected
}

func projectAdminUsers(users []*model.User, viewerRole int) []adminUserResponse {
	projected := make([]adminUserResponse, 0, len(users))
	for _, user := range users {
		if user != nil {
			projected = append(projected, projectAdminUser(user, viewerRole))
		}
	}
	return projected
}

func redactAdminUserResponses(users []adminUserResponse, viewerRole int) {
	for index := range users {
		redactAdminUserResponse(&users[index], viewerRole)
	}
}

func redactAdminUserResponse(user *adminUserResponse, viewerRole int) {
	if user == nil || adminViewerCanSearchEmail(viewerRole) {
		return
	}
	user.Email = maskAdminUserEmail(user.Email)
}

func adminViewerCanSearchEmail(viewerRole int) bool {
	return viewerRole == common.RoleAdminUser || viewerRole == common.RoleRootUser
}

func maskAdminUserEmail(email string) string {
	separator := strings.LastIndexByte(email, '@')
	if separator <= 0 || separator == len(email)-1 {
		return "***"
	}
	local := []rune(email[:separator])
	if len(local) == 0 {
		return "***"
	}
	return string(local[0]) + "***" + email[separator:]
}

func parseAdminUserID(c *gin.Context) (int, bool) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil || userID <= 0 {
		writeAdminUserError(c, errors.New("invalid user id"))
		return 0, false
	}
	return userID, true
}

func invalidateAdminUserSecurityCaches(userID int) {
	if err := model.InvalidateUserCache(userID); err != nil {
		common.SysLog(fmt.Sprintf("failed to invalidate user cache for user %d: %s", userID, err.Error()))
	}
	if err := model.InvalidateUserTokensCache(userID); err != nil {
		common.SysLog(fmt.Sprintf("failed to invalidate token cache for user %d: %s", userID, err.Error()))
	}
}

func writeAdminUserError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		status = http.StatusNotFound
	case errors.Is(err, model.ErrAdminUserForbidden):
		status = http.StatusForbidden
	case errors.Is(err, model.ErrAdminUserStateConflict), errors.Is(err, model.ErrLastEnabledRoot), errors.Is(err, model.ErrAdminUserDebtOutstanding):
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"success": false, "message": err.Error()})
}
