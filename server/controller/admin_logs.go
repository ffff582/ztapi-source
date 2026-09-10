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
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	adminLogDefaultRangeSeconds int64 = 24 * 60 * 60
	adminLogMaxRangeSeconds     int64 = 31 * 24 * 60 * 60
	adminLogCSVLimit                  = 5000
)

var adminAuditActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

type adminLogFilter struct {
	From      int64
	To        int64
	Type      int
	UserID    int
	Username  string
	Model     string
	RequestID string
	Status    string
	Action    string
	Operator  int
}

type adminRequestLogRow struct {
	ID               int    `gorm:"column:id"`
	CreatedAt        int64  `gorm:"column:created_at"`
	Type             int    `gorm:"column:type"`
	UserID           int    `gorm:"column:user_id"`
	Username         string `gorm:"column:username"`
	ModelName        string `gorm:"column:model_name"`
	RequestID        string `gorm:"column:request_id"`
	UseTime          int    `gorm:"column:use_time"`
	PromptTokens     int    `gorm:"column:prompt_tokens"`
	CompletionTokens int    `gorm:"column:completion_tokens"`
	Quota            int    `gorm:"column:quota"`
}

type adminRequestLogResponse struct {
	ID               int     `json:"id"`
	CreatedAt        int64   `json:"created_at"`
	Status           string  `json:"status"`
	Type             int     `json:"type"`
	UserID           int     `json:"user_id"`
	Username         string  `json:"username"`
	Model            string  `json:"model"`
	RequestID        string  `json:"request_id"`
	Latency          int     `json:"latency"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Quota            int     `json:"quota"`
	BilledAmount     float64 `json:"billed_amount"`
}

type adminAuditLogRow struct {
	ID        int    `gorm:"column:id"`
	CreatedAt int64  `gorm:"column:created_at"`
	RequestID string `gorm:"column:request_id"`
	Other     string `gorm:"column:other"`
}

type adminAuditOperator struct {
	AdminID       int    `json:"admin_id"`
	AdminUsername string `json:"admin_username"`
	AdminRole     int    `json:"admin_role"`
}

type adminAuditLogResponse struct {
	ID        int                    `json:"id"`
	CreatedAt int64                  `json:"created_at"`
	RequestID string                 `json:"request_id"`
	Action    string                 `json:"action"`
	Params    map[string]interface{} `json:"params"`
	Operator  adminAuditOperator     `json:"operator"`
}

func GetAdminRequestLogs(c *gin.Context) {
	filter, err := parseAdminRequestLogFilter(c)
	if err != nil {
		writeAdminLogInputError(c, err)
		return
	}
	pageInfo := boundedAdminPage(c)
	items, total, err := loadAdminRequestLogs(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		writeAdminLogQueryError(c)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func ExportAdminRequestLogs(c *gin.Context) {
	filter, err := parseAdminRequestLogFilter(c)
	if err != nil {
		writeAdminLogInputError(c, err)
		return
	}
	items, _, err := loadAdminRequestLogs(filter, 0, adminLogCSVLimit)
	if err != nil {
		writeAdminLogQueryError(c)
		return
	}
	recordAdminExportAudit(c, "export.request_logs", adminLogFilterAuditParams(filter), len(items))
	header := []string{"id", "created_at", "status", "type", "user_id", "username", "model", "request_id", "latency", "prompt_tokens", "completion_tokens", "total_tokens", "quota", "billed_amount"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{
			strconv.Itoa(item.ID), strconv.FormatInt(item.CreatedAt, 10), item.Status,
			strconv.Itoa(item.Type), strconv.Itoa(item.UserID), item.Username, item.Model,
			item.RequestID, strconv.Itoa(item.Latency), strconv.Itoa(item.PromptTokens),
			strconv.Itoa(item.CompletionTokens), strconv.Itoa(item.TotalTokens),
			strconv.Itoa(item.Quota), strconv.FormatFloat(item.BilledAmount, 'f', -1, 64),
		})
	}
	writeSafeCSV(c, "ztapi-request-logs.csv", header, rows)
}

func GetAdminAuditLogs(c *gin.Context) {
	filter, err := parseAdminAuditLogFilter(c)
	if err != nil {
		writeAdminLogInputError(c, err)
		return
	}
	pageInfo := boundedAdminPage(c)
	items, total, err := loadAdminAuditLogs(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		writeAdminLogQueryError(c)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func ExportAdminAuditLogs(c *gin.Context) {
	filter, err := parseAdminAuditLogFilter(c)
	if err != nil {
		writeAdminLogInputError(c, err)
		return
	}
	items, _, err := loadAdminAuditLogs(filter, 0, adminLogCSVLimit)
	if err != nil {
		writeAdminLogQueryError(c)
		return
	}
	recordAdminExportAudit(c, "export.audit_logs", adminLogFilterAuditParams(filter), len(items))
	header := []string{"id", "created_at", "request_id", "action", "params", "admin_id", "admin_username", "admin_role"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		params, _ := json.Marshal(item.Params)
		rows = append(rows, []string{
			strconv.Itoa(item.ID), strconv.FormatInt(item.CreatedAt, 10), item.RequestID,
			item.Action, string(params), strconv.Itoa(item.Operator.AdminID),
			item.Operator.AdminUsername, strconv.Itoa(item.Operator.AdminRole),
		})
	}
	writeSafeCSV(c, "ztapi-audit-logs.csv", header, rows)
}

func parseAdminRequestLogFilter(c *gin.Context) (adminLogFilter, error) {
	filter, err := parseAdminLogRange(c)
	if err != nil {
		return filter, err
	}
	filter.Status = strings.TrimSpace(c.Query("status"))
	if filter.Status != "" && filter.Status != "success" && filter.Status != "error" {
		return filter, errors.New("invalid log status")
	}
	if value := strings.TrimSpace(c.Query("type")); value != "" {
		filter.Type, err = strconv.Atoi(value)
		if err != nil || (filter.Type != 0 && filter.Type != model.LogTypeConsume && filter.Type != model.LogTypeError) {
			return filter, errors.New("invalid request log type")
		}
	}
	if filter.Status == "success" && filter.Type != 0 && filter.Type != model.LogTypeConsume || filter.Status == "error" && filter.Type != 0 && filter.Type != model.LogTypeError {
		return filter, errors.New("log status and type conflict")
	}
	if value := strings.TrimSpace(c.Query("user_id")); value != "" {
		filter.UserID, err = strconv.Atoi(value)
		if err != nil || filter.UserID <= 0 {
			return filter, errors.New("invalid user_id")
		}
	}
	if value := strings.TrimSpace(c.Query("user")); value != "" {
		if id, parseErr := strconv.Atoi(value); parseErr == nil && id > 0 {
			filter.UserID = id
		} else {
			filter.Username = value
		}
	}
	if value := strings.TrimSpace(c.Query("username")); value != "" {
		filter.Username = value
	}
	filter.Model = strings.TrimSpace(c.Query("model"))
	if filter.Model == "" {
		filter.Model = strings.TrimSpace(c.Query("model_name"))
	}
	filter.RequestID = strings.TrimSpace(c.Query("request_id"))
	return filter, nil
}

func parseAdminAuditLogFilter(c *gin.Context) (adminLogFilter, error) {
	filter, err := parseAdminLogRange(c)
	if err != nil {
		return filter, err
	}
	filter.Action = strings.TrimSpace(c.Query("action"))
	if filter.Action != "" && !adminAuditActionPattern.MatchString(filter.Action) {
		return filter, errors.New("invalid audit action")
	}
	if value := strings.TrimSpace(c.Query("operator_id")); value != "" {
		filter.Operator, err = strconv.Atoi(value)
		if err != nil || filter.Operator <= 0 {
			return filter, errors.New("invalid operator_id")
		}
	}
	filter.RequestID = strings.TrimSpace(c.Query("request_id"))
	return filter, nil
}

func parseAdminLogRange(c *gin.Context) (adminLogFilter, error) {
	now := time.Now().UTC().Unix()
	filter := adminLogFilter{From: now - adminLogDefaultRangeSeconds, To: now}
	fromValue, toValue := strings.TrimSpace(c.Query("from")), strings.TrimSpace(c.Query("to"))
	if fromValue == "" {
		fromValue = strings.TrimSpace(c.Query("start_timestamp"))
	}
	if toValue == "" {
		toValue = strings.TrimSpace(c.Query("end_timestamp"))
	}
	var err error
	if fromValue != "" {
		filter.From, err = strconv.ParseInt(fromValue, 10, 64)
		if err != nil {
			return filter, errors.New("invalid log time range")
		}
	}
	if toValue != "" {
		filter.To, err = strconv.ParseInt(toValue, 10, 64)
		if err != nil {
			return filter, errors.New("invalid log time range")
		}
	}
	if filter.From < 0 || filter.To <= filter.From || filter.To-filter.From > adminLogMaxRangeSeconds {
		return filter, errors.New("invalid log time range")
	}
	return filter, nil
}

func requestLogQuery(filter adminLogFilter) *gorm.DB {
	query := model.LOG_DB.Model(&model.Log{}).Where("type IN ? AND created_at >= ? AND created_at < ?", []int{model.LogTypeConsume, model.LogTypeError}, filter.From, filter.To)
	if filter.Status == "success" {
		query = query.Where("type = ?", model.LogTypeConsume)
	} else if filter.Status == "error" {
		query = query.Where("type = ?", model.LogTypeError)
	}
	if filter.Type != 0 {
		query = query.Where("type = ?", filter.Type)
	}
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Username != "" {
		query = query.Where("username = ?", filter.Username)
	}
	if filter.Model != "" {
		query = query.Where("model_name = ?", filter.Model)
	}
	if filter.RequestID != "" {
		query = query.Where("request_id = ?", filter.RequestID)
	}
	return query
}

func loadAdminRequestLogs(filter adminLogFilter, offset, limit int) ([]adminRequestLogResponse, int64, error) {
	query := requestLogQuery(filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]adminRequestLogRow, 0)
	columns := "id, created_at, type, user_id, username, model_name, request_id, use_time, prompt_tokens, completion_tokens, quota"
	if err := query.Select(columns).Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]adminRequestLogResponse, 0, len(rows))
	for _, row := range rows {
		billed := 0.0
		if common.QuotaPerUnit > 0 {
			billed = float64(row.Quota) / common.QuotaPerUnit
		}
		items = append(items, adminRequestLogResponse{
			ID: row.ID, CreatedAt: row.CreatedAt, Status: adminRequestLogStatus(row.Type),
			Type: row.Type, UserID: row.UserID, Username: row.Username, Model: row.ModelName,
			RequestID: row.RequestID, Latency: row.UseTime, PromptTokens: row.PromptTokens,
			CompletionTokens: row.CompletionTokens, TotalTokens: row.PromptTokens + row.CompletionTokens,
			Quota: row.Quota, BilledAmount: billed,
		})
	}
	return items, total, nil
}

func adminRequestLogStatus(logType int) string {
	if logType == model.LogTypeConsume {
		return "success"
	}
	return "error"
}

func auditLogQuery(filter adminLogFilter) *gorm.DB {
	query := model.LOG_DB.Model(&model.Log{}).Where("type = ? AND created_at >= ? AND created_at < ?", model.LogTypeManage, filter.From, filter.To)
	if filter.Action != "" {
		query = query.Where("other LIKE ?", `%"action":"`+filter.Action+`"%`)
	}
	if filter.Operator > 0 {
		prefix := `%"admin_id":` + strconv.Itoa(filter.Operator)
		query = query.Where("(other LIKE ? OR other LIKE ?)", prefix+`,%`, prefix+`}%`)
	}
	if filter.RequestID != "" {
		query = query.Where("request_id = ?", filter.RequestID)
	}
	return query
}

func loadAdminAuditLogs(filter adminLogFilter, offset, limit int) ([]adminAuditLogResponse, int64, error) {
	query := auditLogQuery(filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]adminAuditLogRow, 0)
	if err := query.Select("id, created_at, request_id, other").Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]adminAuditLogResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, projectAdminAuditLog(row))
	}
	return items, total, nil
}

func projectAdminAuditLog(row adminAuditLogRow) adminAuditLogResponse {
	result := adminAuditLogResponse{ID: row.ID, CreatedAt: row.CreatedAt, RequestID: row.RequestID, Action: "unknown", Params: map[string]interface{}{}}
	var other map[string]interface{}
	if err := json.Unmarshal([]byte(row.Other), &other); err != nil {
		return result
	}
	op, _ := other["op"].(map[string]interface{})
	action, _ := op["action"].(string)
	if adminAuditActionPattern.MatchString(action) {
		result.Action = action
		params, _ := op["params"].(map[string]interface{})
		result.Params = allowAdminAuditParams(action, params)
	}
	adminInfo, _ := other["admin_info"].(map[string]interface{})
	result.Operator = adminAuditOperator{
		AdminID:       jsonNumberToInt(adminInfo["admin_id"]),
		AdminUsername: boundedAuditString(adminInfo["admin_username"], 128),
		AdminRole:     jsonNumberToInt(adminInfo["admin_role"]),
	}
	return result
}

var adminAuditAllowedParams = map[string][]string{
	"user.status_update":              {"target_user_id", "from_status", "to_status", "reason", "result"},
	"user.role_update":                {"target_user_id", "from_role", "to_role", "reason"},
	"balance.adjustment":              {"target_user_id", "delta", "reason", "ledger_id", "result"},
	"topup.complete":                  {"topup_id", "trade_no", "target_user_id", "from_status", "to_status", "reason", "quota_added", "result"},
	"topup.reject":                    {"topup_id", "trade_no", "target_user_id", "from_status", "to_status", "reason", "result"},
	"settings.update":                 {"key", "from_value", "to_value", "reason", "result"},
	"channel.create":                  {"name", "type", "count"},
	"channel.update":                  {"name", "id"},
	"channel.delete":                  {"name", "id"},
	"channel.delete_batch":            {"count"},
	"channel.key_view":                {"name", "id"},
	"redemption.create":               {"count", "name", "quota"},
	"option.update":                   {"key"},
	"user.create":                     {"username", "role"},
	"user.update":                     {"username", "id"},
	"user.delete":                     {"username", "id"},
	"user.binding_clear":              {"bindingType", "username"},
	"user.quota_add":                  {"quota"},
	"user.quota_subtract":             {"quota"},
	"user.quota_override":             {"from", "to"},
	"ztapi.model_below_cost_override": {"public_name", "id", "version"},
	"export.orders":                   {"keyword", "status", "user_id", "username", "payment_provider", "from", "to", "row_count"},
	"export.ledger":                   {"user_id", "operator_id", "source_type", "request_id", "from", "to", "row_count"},
	"export.request_logs":             {"status", "type", "user_id", "username", "model", "request_id", "from", "to", "row_count"},
	"export.audit_logs":               {"action", "operator_id", "request_id", "from", "to", "row_count"},
}

func allowAdminAuditParams(action string, params map[string]interface{}) map[string]interface{} {
	allowed := adminAuditAllowedParams[action]
	result := make(map[string]interface{}, len(allowed))
	for _, key := range allowed {
		value, ok := params[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if len(typed) > 500 {
				typed = typed[:500]
			}
			result[key] = typed
		case float64, bool, nil:
			result[key] = typed
		}
	}
	return result
}

func jsonNumberToInt(value interface{}) int {
	number, _ := value.(float64)
	return int(number)
}

func boundedAuditString(value interface{}, limit int) string {
	text, _ := value.(string)
	if len(text) > limit {
		return text[:limit]
	}
	return text
}

func writeSafeCSV(c *gin.Context, filename string, header []string, rows [][]string) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Status(http.StatusOK)
	writer := csv.NewWriter(c.Writer)
	_ = writer.Write(header)
	for _, row := range rows {
		for i := range row {
			row[i] = neutralizeCSVFormula(row[i])
		}
		_ = writer.Write(row)
	}
	writer.Flush()
}

func recordAdminExportAudit(c *gin.Context, action string, params map[string]interface{}, rowCount int) {
	params["row_count"] = rowCount
	recordManageAudit(c, action, params)
}

func adminLogFilterAuditParams(filter adminLogFilter) map[string]interface{} {
	params := map[string]interface{}{"from": filter.From, "to": filter.To}
	if filter.Status != "" {
		params["status"] = filter.Status
	}
	if filter.Type != 0 {
		params["type"] = filter.Type
	}
	if filter.UserID > 0 {
		params["user_id"] = filter.UserID
	}
	if filter.Username != "" {
		params["username"] = filter.Username
	}
	if filter.Model != "" {
		params["model"] = filter.Model
	}
	if filter.RequestID != "" {
		params["request_id"] = filter.RequestID
	}
	if filter.Action != "" {
		params["action"] = filter.Action
	}
	if filter.Operator > 0 {
		params["operator_id"] = filter.Operator
	}
	return params
}

func neutralizeCSVFormula(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func writeAdminLogInputError(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
}

func writeAdminLogQueryError(c *gin.Context) {
	c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "unable to load admin logs"})
}
