package controller

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

var adminOverviewAuditActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

const (
	defaultOverviewRangeSeconds int64 = 7 * 24 * 60 * 60
	maxOverviewRangeSeconds     int64 = 31 * 24 * 60 * 60
	overviewRecentLimit               = 10
)

type adminOverviewRange struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}
type adminOverviewRequests struct {
	Total   int64 `json:"total"`
	Success int64 `json:"success"`
	Failure int64 `json:"failure"`
}
type adminOverviewChannelStatus struct {
	Status int   `json:"status"`
	Count  int64 `json:"count"`
}
type adminOverviewFailure struct {
	ID         int    `json:"id"`
	OccurredAt int64  `json:"occurred_at"`
	ChannelID  int    `json:"channel_id"`
	ModelName  string `json:"model_name"`
	Summary    string `json:"summary"`
}
type adminOverviewAuditOperation struct {
	ID         int    `json:"id"`
	OccurredAt int64  `json:"occurred_at"`
	Action     string `json:"action"`
}
type adminOverviewSeriesPoint struct {
	Start       int64 `json:"start"`
	Requests    int64 `json:"requests"`
	BilledQuota int64 `json:"billed_quota"`
}

type adminOverviewResponse struct {
	Range                 adminOverviewRange             `json:"range"`
	Requests              adminOverviewRequests          `json:"requests"`
	BilledQuota           *int64                         `json:"billed_quota,omitempty"`
	UserBalanceTotal      *int64                         `json:"user_balance_total,omitempty"`
	PendingTopUps         *int64                         `json:"pending_top_ups,omitempty"`
	Series                *[]adminOverviewSeriesPoint    `json:"series,omitempty"`
	ChannelStatuses       *[]adminOverviewChannelStatus  `json:"channel_statuses,omitempty"`
	RecentFailures        *[]adminOverviewFailure        `json:"recent_failures,omitempty"`
	RecentAuditOperations *[]adminOverviewAuditOperation `json:"recent_audit_operations,omitempty"`
}

func parseAdminOverviewRange(c *gin.Context) (adminOverviewRange, bool) {
	fromValue, toValue := c.Query("from"), c.Query("to")
	if fromValue == "" && toValue == "" {
		to := time.Now().UTC().Unix()
		return adminOverviewRange{From: to - defaultOverviewRangeSeconds, To: to}, true
	}
	if fromValue == "" || toValue == "" {
		return adminOverviewRange{}, false
	}
	from, fromErr := strconv.ParseInt(fromValue, 10, 64)
	to, toErr := strconv.ParseInt(toValue, 10, 64)
	if fromErr != nil || toErr != nil || from < 0 || to <= from || to-from > maxOverviewRangeSeconds {
		return adminOverviewRange{}, false
	}
	return adminOverviewRange{From: from, To: to}, true
}

func overviewQueryError(c *gin.Context) {
	c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Unable to load operations overview."})
}

func overviewDayExpression(logDialect string) (string, error) {
	switch logDialect {
	case common.DatabaseTypeSQLite:
		return "strftime('%Y-%m-%d', created_at, 'unixepoch')", nil
	case common.DatabaseTypeMySQL:
		return "DATE_FORMAT(FROM_UNIXTIME(created_at), '%Y-%m-%d')", nil
	case common.DatabaseTypePostgreSQL:
		return "TO_CHAR(TO_TIMESTAMP(created_at), 'YYYY-MM-DD')", nil
	default:
		return "", fmt.Errorf("unsupported log database dialect")
	}
}

func loadAdminOverviewSeries(rangeValue adminOverviewRange) ([]adminOverviewSeriesPoint, error) {
	expression, err := overviewDayExpression(common.LogSqlType)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("SELECT %s AS bucket, COUNT(*) AS requests, COALESCE(SUM(CASE WHEN type = ? THEN quota ELSE 0 END), 0) AS billed_quota FROM logs WHERE type IN ? AND created_at >= ? AND created_at < ? GROUP BY %s ORDER BY bucket ASC", expression, expression)
	var rows []struct {
		Bucket      string `gorm:"column:bucket"`
		Requests    int64  `gorm:"column:requests"`
		BilledQuota int64  `gorm:"column:billed_quota"`
	}
	if err := model.LOG_DB.Raw(query, model.LogTypeConsume, []int{model.LogTypeConsume, model.LogTypeError}, rangeValue.From, rangeValue.To).Scan(&rows).Error; err != nil {
		return nil, err
	}
	series := make([]adminOverviewSeriesPoint, 0, len(rows))
	for _, row := range rows {
		start, err := time.Parse("2006-01-02", row.Bucket)
		if err != nil {
			return nil, err
		}
		series = append(series, adminOverviewSeriesPoint{Start: start.UTC().Unix(), Requests: row.Requests, BilledQuota: row.BilledQuota})
	}
	return series, nil
}

// GetAdminOverview returns bounded operations aggregates without exposing log content or metadata.
func GetAdminOverview(c *gin.Context) {
	rangeValue, valid := parseAdminOverviewRange(c)
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid overview time range."})
		return
	}
	response := adminOverviewResponse{Range: rangeValue}
	var aggregate struct {
		Total       int64 `gorm:"column:total"`
		Success     int64 `gorm:"column:success"`
		Failure     int64 `gorm:"column:failure"`
		BilledQuota int64 `gorm:"column:billed_quota"`
	}
	if err := model.LOG_DB.Model(&model.Log{}).
		Select("COUNT(*) AS total, COALESCE(SUM(CASE WHEN type = ? THEN 1 ELSE 0 END), 0) AS success, COALESCE(SUM(CASE WHEN type = ? THEN 1 ELSE 0 END), 0) AS failure, COALESCE(SUM(CASE WHEN type = ? THEN quota ELSE 0 END), 0) AS billed_quota", model.LogTypeConsume, model.LogTypeError, model.LogTypeConsume).
		Where("type IN ? AND created_at >= ? AND created_at < ?", []int{model.LogTypeConsume, model.LogTypeError}, rangeValue.From, rangeValue.To).
		Scan(&aggregate).Error; err != nil {
		overviewQueryError(c)
		return
	}
	response.Requests = adminOverviewRequests{Total: aggregate.Total, Success: aggregate.Success, Failure: aggregate.Failure}

	role := c.GetInt("role")
	if common.HasAdminPermission(role, common.PermissionFinanceRead) {
		billedQuota := aggregate.BilledQuota
		var userBalanceTotal, pendingTopUps int64
		if err := model.DB.Model(&model.User{}).Select("COALESCE(SUM(quota), 0)").Scan(&userBalanceTotal).Error; err != nil {
			overviewQueryError(c)
			return
		}
		if err := model.DB.Model(&model.TopUp{}).Where("status = ?", common.TopUpStatusPending).Count(&pendingTopUps).Error; err != nil {
			overviewQueryError(c)
			return
		}
		response.BilledQuota, response.UserBalanceTotal, response.PendingTopUps = &billedQuota, &userBalanceTotal, &pendingTopUps
		series, err := loadAdminOverviewSeries(rangeValue)
		if err != nil {
			overviewQueryError(c)
			return
		}
		response.Series = &series
	}
	if common.HasAdminPermission(role, common.PermissionChannelRead) {
		channelStatuses := []adminOverviewChannelStatus{}
		if err := model.DB.Model(&model.Channel{}).Select("status, COUNT(*) AS count").Group("status").Order("status ASC").Scan(&channelStatuses).Error; err != nil {
			overviewQueryError(c)
			return
		}
		response.ChannelStatuses = &channelStatuses
	}
	if common.HasAdminPermission(role, common.PermissionLogRead) {
		var failures []struct {
			ID        int    `gorm:"column:id"`
			CreatedAt int64  `gorm:"column:created_at"`
			ChannelID int    `gorm:"column:channel_id"`
			ModelName string `gorm:"column:model_name"`
		}
		if err := model.LOG_DB.Model(&model.Log{}).Select("id, created_at, channel_id, model_name").Where("type = ? AND created_at >= ? AND created_at < ?", model.LogTypeError, rangeValue.From, rangeValue.To).Order("created_at DESC, id DESC").Limit(overviewRecentLimit).Find(&failures).Error; err != nil {
			overviewQueryError(c)
			return
		}
		recentFailures := make([]adminOverviewFailure, 0, len(failures))
		for _, failure := range failures {
			recentFailures = append(recentFailures, adminOverviewFailure{ID: failure.ID, OccurredAt: failure.CreatedAt, ChannelID: failure.ChannelID, ModelName: failure.ModelName, Summary: "请求失败"})
		}
		response.RecentFailures = &recentFailures
	}
	if common.HasAdminPermission(role, common.PermissionAuditRead) {
		var audits []struct {
			ID        int    `gorm:"column:id"`
			CreatedAt int64  `gorm:"column:created_at"`
			Other     string `gorm:"column:other"`
		}
		if err := model.LOG_DB.Model(&model.Log{}).Select("id, created_at, other").Where("type = ? AND created_at >= ? AND created_at < ?", model.LogTypeManage, rangeValue.From, rangeValue.To).Order("created_at DESC, id DESC").Limit(overviewRecentLimit).Find(&audits).Error; err != nil {
			overviewQueryError(c)
			return
		}
		recentAuditOperations := make([]adminOverviewAuditOperation, 0, len(audits))
		for _, audit := range audits {
			other, _ := common.StrToMap(audit.Other)
			op, _ := other["op"].(map[string]interface{})
			action, _ := op["action"].(string)
			if adminOverviewAuditActionPattern.MatchString(action) {
				recentAuditOperations = append(recentAuditOperations, adminOverviewAuditOperation{ID: audit.ID, OccurredAt: audit.CreatedAt, Action: action})
			}
		}
		response.RecentAuditOperations = &recentAuditOperations
	}
	common.ApiSuccess(c, response)
}
