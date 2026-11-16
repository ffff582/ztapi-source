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

type adminTopUpResponse struct {
	ID              int     `json:"id" gorm:"column:id"`
	UserID          int     `json:"user_id" gorm:"column:user_id"`
	Username        string  `json:"username" gorm:"column:username"`
	Amount          int64   `json:"amount" gorm:"column:amount"`
	Money           float64 `json:"money" gorm:"column:money"`
	TradeNo         string  `json:"trade_no" gorm:"column:trade_no"`
	PaymentMethod   string  `json:"payment_method" gorm:"column:payment_method"`
	PaymentProvider string  `json:"payment_provider" gorm:"column:payment_provider"`
	CreateTime      int64   `json:"create_time" gorm:"column:create_time"`
	CompleteTime    int64   `json:"complete_time" gorm:"column:complete_time"`
	Status          string  `json:"status" gorm:"column:status"`
}

type adminTopUpMutationRequest struct {
	ExpectedStatus string `json:"expected_status"`
	Reason         string `json:"reason"`
}

type adminTopUpFilter struct {
	Keyword         string
	Status          string
	UserID          int
	Username        string
	PaymentProvider string
	From            int64
	To              int64
}

type adminBalanceLedgerFilter struct {
	UserID     int
	OperatorID int
	SourceType string
	RequestID  string
	From       int64
	To         int64
}

type adminBalanceLedgerResponse struct {
	ID            int       `json:"id"`
	UserID        int       `json:"user_id"`
	OperatorID    int       `json:"operator_id"`
	Delta         int64     `json:"delta"`
	BalanceBefore int64     `json:"balance_before"`
	BalanceAfter  int64     `json:"balance_after"`
	Reason        string    `json:"reason"`
	RequestID     string    `json:"request_id"`
	SourceType    string    `json:"source_type"`
	CreatedAt     time.Time `json:"created_at"`
}

var adminFinanceSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func GetAdminTopUps(c *gin.Context) {
	filter, err := parseAdminTopUpFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	pageInfo := boundedAdminPage(c)
	items, total, err := loadAdminTopUps(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		writeAdminFinanceError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func ExportAdminTopUps(c *gin.Context) {
	filter, err := parseAdminTopUpFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	items, _, err := loadAdminTopUps(filter, 0, adminLogCSVLimit)
	if err != nil {
		writeAdminFinanceError(c, err)
		return
	}
	recordAdminExportAudit(c, "export.orders", topUpFilterAuditParams(filter), len(items))
	header := []string{"id", "user_id", "username", "amount", "money", "trade_no", "payment_method", "payment_provider", "create_time", "complete_time", "status"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{
			strconv.Itoa(item.ID), strconv.Itoa(item.UserID), item.Username,
			strconv.FormatInt(item.Amount, 10), strconv.FormatFloat(item.Money, 'f', -1, 64),
			item.TradeNo, item.PaymentMethod, item.PaymentProvider,
			strconv.FormatInt(item.CreateTime, 10), strconv.FormatInt(item.CompleteTime, 10), item.Status,
		})
	}
	writeSafeCSV(c, "ztapi-topups.csv", header, rows)
}

func parseAdminTopUpFilter(c *gin.Context) (adminTopUpFilter, error) {
	filter := adminTopUpFilter{
		Keyword: strings.TrimSpace(c.Query("keyword")), Username: strings.TrimSpace(c.Query("username")),
		PaymentProvider: strings.TrimSpace(c.Query("payment_provider")), Status: strings.TrimSpace(c.Query("status")),
	}
	var err error
	if value := strings.TrimSpace(c.Query("user_id")); value != "" {
		filter.UserID, err = strconv.Atoi(value)
		if err != nil || filter.UserID <= 0 {
			return filter, errors.New("invalid user_id")
		}
	}
	if filter.Status != "" && !isTopUpStatus(filter.Status) {
		return filter, errors.New("invalid topup status")
	}
	if filter.PaymentProvider != "" && !adminFinanceSourcePattern.MatchString(filter.PaymentProvider) {
		return filter, errors.New("invalid payment_provider")
	}
	filter.From, filter.To, err = parseOptionalAdminUnixRange(c)
	return filter, err
}

func adminTopUpQuery(filter adminTopUpFilter) *gorm.DB {
	query := model.DB.Table("top_ups").Joins("LEFT JOIN users ON users.id = top_ups.user_id")
	if filter.Keyword != "" {
		pattern := "%" + escapeAdminLike(filter.Keyword) + "%"
		query = query.Where("top_ups.trade_no LIKE ? ESCAPE '!' OR users.username LIKE ? ESCAPE '!'", pattern, pattern)
	}
	if filter.Status != "" {
		query = query.Where("top_ups.status = ?", filter.Status)
	}
	if filter.UserID > 0 {
		query = query.Where("top_ups.user_id = ?", filter.UserID)
	}
	if filter.Username != "" {
		query = query.Where("users.username = ?", filter.Username)
	}
	if filter.PaymentProvider != "" {
		query = query.Where("top_ups.payment_provider = ?", filter.PaymentProvider)
	}
	if filter.From > 0 {
		query = query.Where("top_ups.create_time >= ?", filter.From)
	}
	if filter.To > 0 {
		query = query.Where("top_ups.create_time < ?", filter.To)
	}
	return query
}

func loadAdminTopUps(filter adminTopUpFilter, offset, limit int) ([]adminTopUpResponse, int64, error) {
	query := adminTopUpQuery(filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := make([]adminTopUpResponse, 0)
	columns := "top_ups.id, top_ups.user_id, COALESCE(users.username, '') AS username, top_ups.amount, top_ups.money, top_ups.trade_no, top_ups.payment_method, top_ups.payment_provider, top_ups.create_time, top_ups.complete_time, top_ups.status"
	if err := query.Select(columns).Order("top_ups.id DESC").Offset(offset).Limit(limit).Scan(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func GetAdminBalanceLedger(c *gin.Context) {
	filter, err := parseAdminBalanceLedgerFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	pageInfo := boundedAdminPage(c)
	items, total, err := loadAdminBalanceLedger(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "unable to load balance ledger"})
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func ExportAdminBalanceLedger(c *gin.Context) {
	filter, err := parseAdminBalanceLedgerFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	items, _, err := loadAdminBalanceLedger(filter, 0, adminLogCSVLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "unable to load balance ledger"})
		return
	}
	recordAdminExportAudit(c, "export.ledger", balanceLedgerFilterAuditParams(filter), len(items))
	header := []string{"id", "user_id", "operator_id", "delta", "balance_before", "balance_after", "reason", "request_id", "source_type", "created_at"}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{
			strconv.Itoa(item.ID), strconv.Itoa(item.UserID), strconv.Itoa(item.OperatorID), strconv.FormatInt(item.Delta, 10),
			strconv.FormatInt(item.BalanceBefore, 10), strconv.FormatInt(item.BalanceAfter, 10), item.Reason,
			item.RequestID, item.SourceType, item.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	writeSafeCSV(c, "ztapi-balance-ledger.csv", header, rows)
}

func parseAdminBalanceLedgerFilter(c *gin.Context) (adminBalanceLedgerFilter, error) {
	filter := adminBalanceLedgerFilter{SourceType: strings.TrimSpace(c.Query("source_type")), RequestID: strings.TrimSpace(c.Query("request_id"))}
	var err error
	if value := strings.TrimSpace(c.Query("user_id")); value != "" {
		filter.UserID, err = strconv.Atoi(value)
		if err != nil || filter.UserID <= 0 {
			return filter, errors.New("invalid user_id")
		}
	}
	if value := strings.TrimSpace(c.Query("operator_id")); value != "" {
		filter.OperatorID, err = strconv.Atoi(value)
		if err != nil || filter.OperatorID <= 0 {
			return filter, errors.New("invalid operator_id")
		}
	}
	if filter.SourceType != "" && !adminFinanceSourcePattern.MatchString(filter.SourceType) {
		return filter, errors.New("invalid source_type")
	}
	filter.From, filter.To, err = parseOptionalAdminUnixRange(c)
	return filter, err
}

func adminBalanceLedgerQuery(filter adminBalanceLedgerFilter) *gorm.DB {
	query := model.DB.Model(&model.BalanceLedger{})
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.OperatorID > 0 {
		query = query.Where("operator_id = ?", filter.OperatorID)
	}
	if filter.SourceType != "" {
		query = query.Where("source_type = ?", filter.SourceType)
	}
	if filter.RequestID != "" {
		query = query.Where("request_id = ?", filter.RequestID)
	}
	if filter.From > 0 {
		query = query.Where("created_at >= ?", time.Unix(filter.From, 0).UTC())
	}
	if filter.To > 0 {
		query = query.Where("created_at < ?", time.Unix(filter.To, 0).UTC())
	}
	return query
}

func loadAdminBalanceLedger(filter adminBalanceLedgerFilter, offset, limit int) ([]adminBalanceLedgerResponse, int64, error) {
	query := adminBalanceLedgerQuery(filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := make([]adminBalanceLedgerResponse, 0)
	columns := "id, user_id, operator_id, delta, balance_before, balance_after, reason, request_id, source_type, created_at"
	if err := query.Select(columns).Order("created_at DESC, id DESC").Offset(offset).Limit(limit).Scan(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func parseOptionalAdminUnixRange(c *gin.Context) (int64, int64, error) {
	var from, to int64
	var err error
	if value := strings.TrimSpace(c.Query("from")); value != "" {
		from, err = strconv.ParseInt(value, 10, 64)
		if err != nil || from < 0 {
			return 0, 0, errors.New("invalid time range")
		}
	}
	if value := strings.TrimSpace(c.Query("to")); value != "" {
		to, err = strconv.ParseInt(value, 10, 64)
		if err != nil || to < 0 {
			return 0, 0, errors.New("invalid time range")
		}
	}
	if from > 0 && to > 0 && to <= from {
		return 0, 0, errors.New("invalid time range")
	}
	return from, to, nil
}

func topUpFilterAuditParams(filter adminTopUpFilter) map[string]interface{} {
	params := map[string]interface{}{"from": filter.From, "to": filter.To}
	if filter.Keyword != "" {
		params["keyword"] = filter.Keyword
	}
	if filter.Status != "" {
		params["status"] = filter.Status
	}
	if filter.UserID > 0 {
		params["user_id"] = filter.UserID
	}
	if filter.Username != "" {
		params["username"] = filter.Username
	}
	if filter.PaymentProvider != "" {
		params["payment_provider"] = filter.PaymentProvider
	}
	return params
}

func balanceLedgerFilterAuditParams(filter adminBalanceLedgerFilter) map[string]interface{} {
	params := map[string]interface{}{"from": filter.From, "to": filter.To}
	if filter.UserID > 0 {
		params["user_id"] = filter.UserID
	}
	if filter.OperatorID > 0 {
		params["operator_id"] = filter.OperatorID
	}
	if filter.SourceType != "" {
		params["source_type"] = filter.SourceType
	}
	if filter.RequestID != "" {
		params["request_id"] = filter.RequestID
	}
	return params
}

func CompleteAdminTopUp(c *gin.Context) {
	processAdminTopUp(c, common.TopUpStatusSuccess, "topup.complete")
}

func RejectAdminTopUp(c *gin.Context) {
	processAdminTopUp(c, model.TopUpStatusRejected, "topup.reject")
}

func processAdminTopUp(c *gin.Context, targetStatus, action string) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid topup id"})
		return
	}
	var request adminTopUpMutationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid topup mutation"})
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" || len(request.Reason) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "reason is required and must not exceed 500 bytes"})
		return
	}
	if request.ExpectedStatus != common.TopUpStatusPending {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "expected_status must be pending"})
		return
	}

	applyAdminTopUpMutation(c, id, request, targetStatus, action)
}

func applyAdminTopUpMutation(c *gin.Context, id int, request adminTopUpMutationRequest, targetStatus, action string) {
	result, err := model.ProcessAdminTopUp(model.AdminTopUpMutation{
		ID: id, OperatorID: c.GetInt("id"), ExpectedStatus: request.ExpectedStatus,
		TargetStatus: targetStatus, Reason: request.Reason, RequestID: c.GetString(common.RequestIdKey),
	})
	cacheSyncPending := errors.Is(err, model.ErrTopUpCacheSyncPending)
	if err != nil && !cacheSyncPending {
		writeAdminFinanceError(c, err, result)
		return
	}
	topUp := result.TopUp
	params := map[string]interface{}{
		"topup_id":       topUp.Id,
		"trade_no":       topUp.TradeNo,
		"target_user_id": topUp.UserId,
		"from_status":    request.ExpectedStatus,
		"to_status":      targetStatus,
		"reason":         request.Reason,
		"result":         "committed",
	}
	if targetStatus == common.TopUpStatusSuccess {
		params["quota_added"] = result.QuotaAdded
		if result.Ledger != nil {
			params["ledger_id"] = result.Ledger.ID
		}
	}
	if cacheSyncPending {
		params["result"] = "committed_cache_sync_pending"
	}
	recordManageAuditFor(c, topUp.UserId, action, params)
	if cacheSyncPending {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": projectAdminTopUp(topUp, ""), "warning": "committed_cache_sync_pending"})
		return
	}
	common.ApiSuccess(c, projectAdminTopUp(topUp, ""))
}

func projectAdminTopUp(topUp *model.TopUp, username string) adminTopUpResponse {
	return adminTopUpResponse{
		ID: topUp.Id, UserID: topUp.UserId, Username: username, Amount: topUp.Amount,
		Money: topUp.Money, TradeNo: topUp.TradeNo, PaymentMethod: topUp.PaymentMethod,
		PaymentProvider: topUp.PaymentProvider, CreateTime: topUp.CreateTime,
		CompleteTime: topUp.CompleteTime, Status: topUp.Status,
	}
}

func boundedAdminPage(c *gin.Context) *common.PageInfo {
	pageInfo := common.GetPageQuery(c)
	if pageInfo.Page < 1 {
		pageInfo.Page = 1
	}
	if pageInfo.PageSize < 1 {
		pageInfo.PageSize = common.ItemsPerPage
	}
	if pageInfo.PageSize > 100 {
		pageInfo.PageSize = 100
	}
	return pageInfo
}

func escapeAdminLike(value string) string {
	replacer := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return replacer.Replace(value)
}

func isTopUpStatus(status string) bool {
	switch status {
	case common.TopUpStatusPending, common.TopUpStatusSuccess, common.TopUpStatusFailed, common.TopUpStatusExpired:
		return true
	case model.TopUpStatusRejected:
		return true
	default:
		return false
	}
}

func writeAdminFinanceError(c *gin.Context, err error, result ...*model.AdminTopUpMutationResult) {
	status := http.StatusInternalServerError
	message := "unable to process topup"
	switch {
	case errors.Is(err, model.ErrTopUpNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		status, message = http.StatusNotFound, "topup not found"
	case errors.Is(err, model.ErrTopUpStateConflict):
		status, message = http.StatusConflict, "topup status changed"
	case errors.Is(err, model.ErrTopUpStatusInvalid):
		status, message = http.StatusBadRequest, "invalid topup status"
	}
	payload := gin.H{"success": false, "message": message}
	if status == http.StatusConflict && len(result) > 0 && result[0] != nil && result[0].TopUp != nil {
		payload["data"] = projectAdminTopUp(result[0].TopUp, "")
	}
	c.JSON(status, payload)
}
