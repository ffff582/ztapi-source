package controller

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type ztAPIUserLog struct {
	Timestamp        int64   `json:"timestamp"`
	RequestID        string  `json:"request_id"`
	Model            string  `json:"model"`
	Status           string  `json:"status"`
	Latency          int     `json:"latency"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	BilledAmount     float64 `json:"billed_amount"`
	ErrorCode        string  `json:"error_code,omitempty"`
}

var publicUserLogErrorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

func GetZTAPIUserLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	userID := c.GetInt("id")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestID := c.Query("request_id")
	upstreamRequestID := c.Query("upstream_request_id")

	logs, total, err := model.GetUserLogs(
		userID,
		logType,
		startTimestamp,
		endTimestamp,
		modelName,
		tokenName,
		pageInfo.GetStartIdx(),
		pageInfo.GetPageSize(),
		group,
		requestID,
		upstreamRequestID,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	items := make([]ztAPIUserLog, 0, len(logs))
	for _, entry := range logs {
		billedAmount := 0.0
		if common.QuotaPerUnit > 0 {
			billedAmount = float64(entry.Quota) / common.QuotaPerUnit
		}
		items = append(items, ztAPIUserLog{
			Timestamp:        entry.CreatedAt,
			RequestID:        entry.RequestId,
			Model:            entry.ModelName,
			Status:           publicUserLogStatus(entry.Type),
			Latency:          entry.UseTime,
			PromptTokens:     entry.PromptTokens,
			CompletionTokens: entry.CompletionTokens,
			TotalTokens:      entry.PromptTokens + entry.CompletionTokens,
			BilledAmount:     billedAmount,
			ErrorCode:        publicUserLogErrorCode(entry.Other),
		})
	}

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func publicUserLogErrorCode(other string) string {
	var values map[string]any
	if err := json.Unmarshal([]byte(other), &values); err != nil {
		return ""
	}
	code, ok := values["error_code"].(string)
	if !ok {
		return ""
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 || !publicUserLogErrorCodePattern.MatchString(code) {
		return ""
	}
	return code
}

func publicUserLogStatus(logType int) string {
	switch logType {
	case model.LogTypeConsume:
		return "success"
	case model.LogTypeError:
		return "error"
	default:
		return "info"
	}
}
