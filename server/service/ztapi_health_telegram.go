package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var ztapiTelegramTokenPattern = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{10,128}$`)
var ztapiTelegramChatPattern = regexp.MustCompile(`^-?[1-9][0-9]{0,19}$`)
var ztapiHealthRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,255}$`)

func validZTAPIHealthAlertRecipient(c ZTAPIHealthWorkerConfig) bool {
	if c.TelegramConfigured || c.TelegramBotToken != "" || c.TelegramChatID != "" {
		return ztapiTelegramTokenPattern.MatchString(c.TelegramBotToken) && ztapiTelegramChatPattern.MatchString(c.TelegramChatID)
	}
	return validZTAPIHealthWebhook(c.AlertWebhookURL)
}

func ztapiHealthSafeRequestID(id string) string {
	if !ztapiHealthRequestIDPattern.MatchString(id) || strings.HasPrefix(id, "sk-") {
		return "unknown"
	}
	return id
}

var ztapiBeijingTime = time.FixedZone("北京时间", 8*60*60)

func ztapiAlertValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "unknown" || value == "not_applicable" {
		return "未提供"
	}
	return value
}

func ztapiHealthModalityLabel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "text":
		return "文本"
	case "image":
		return "图片"
	case "video":
		return "视频"
	case "embedding":
		return "向量"
	default:
		return "未提供"
	}
}

func ztapiHealthOperationLabel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "video_fetch":
		return "查询视频结果"
	case "video_create":
		return "创建视频"
	case "image_generation":
		return "生成图片"
	case "chat", "chat_completions":
		return "文本对话"
	default:
		return "未提供"
	}
}

func ztapiHealthRouteLabel(d ZTAPIHealthAlertMetadata) string {
	if d.ChannelID <= 0 {
		return "未提供"
	}
	mode := "非流式"
	if d.Stream {
		mode = "流式"
	}
	return fmt.Sprintf("通道 #%d，客户入口 %s，最终上游 %s，%s", d.ChannelID, ztapiAlertValue(d.EntryProtocol), ztapiAlertValue(d.UpstreamProtocol), mode)
}

func ztapiHealthAction(d ZTAPIHealthAlertMetadata) string {
	finish := strings.ToLower(strings.Join(d.FinishReasons, ","))
	reason := strings.ToLower(strings.TrimSpace(d.ErrorCode))
	if strings.Contains(finish, "content_filter") || strings.Contains(reason, "content_filter") {
		return "请询问上游为什么触发内容过滤，并把上游请求编号一并发给对方"
	}
	if strings.Contains(reason, "auth") || strings.Contains(reason, "api_key") || (d.HTTPStatus != nil && *d.HTTPStatus == http.StatusUnauthorized) {
		return "请让 Codex 检查号池和企业 API Key；如果密钥配置正常，再携带上游请求编号询问上游"
	}
	if strings.Contains(reason, "model_permission") || strings.Contains(reason, "model_not_granted") {
		return "请携带上游请求编号询问上游是否已给当前 API Key 开通该模型"
	}
	if d.HTTPStatus != nil && *d.HTTPStatus >= 500 {
		return "请携带上游请求编号询问上游是否发生服务故障；同时让 Codex 检查备用线路是否已接管"
	}
	if d.HTTPStatus != nil && *d.HTTPStatus >= 400 {
		return "请先让 Codex 检查请求参数；若参数符合上游文档，再携带上游请求编号询问上游"
	}
	return "请让 Codex 检查模型线路、响应内容和备用通道，复测通过后再人工恢复上架"
}

func ztapiHealthTelegramText(item ZTAPIHealthWorkItem, now time.Time) string {
	d := ztapiHealthSafeAlertMetadata(item, now)
	trigger := "未提供"
	if d.OpenedAt != nil && *d.OpenedAt > 0 {
		trigger = time.Unix(*d.OpenedAt, 0).In(ztapiBeijingTime).Format("2006-01-02 15:04:05")
	}
	rule := "未提供"
	switch d.Rule {
	case "consecutive_2":
		rule = "连续 2 次失败"
	case "rolling_24h_gt_2pct":
		rule = "24 小时内至少失败 3 次，且失败率超过 2%"
	case "verified_route_2":
		rule = "独立诊断探针连续 2 次失败"
	case "verified_all_routes":
		rule = "所有已授权线路均经独立探针确认不可用"
	case "manual_verified_recovery":
		rule = "管理员依据复验结果手动恢复"
	}
	if item.Test {
		return fmt.Sprintf("【ZTAPI 测试通知】\n结果：告警通道测试成功，本次没有修改或下架任何模型。\n时间（北京时间）：%s\n建议处理：无需处理。", trigger)
	}
	status := "未提供"
	if d.HTTPStatus != nil && *d.HTTPStatus >= 100 && *d.HTTPStatus <= 599 {
		status = fmt.Sprintf("%d", *d.HTTPStatus)
	}
	latency := "未提供"
	if d.LatencyMilliseconds != nil {
		latency = fmt.Sprintf("%d 毫秒", *d.LatencyMilliseconds)
	}
	resultValid := "未提供"
	if d.ResultValid != nil {
		if *d.ResultValid {
			resultValid = "是"
		} else {
			resultValid = "否"
		}
	}
	if item.Kind == "recovery_alert" {
		return fmt.Sprintf("【ZTAPI 复核恢复】\n模型：%s\n结果：故障状态已由管理员复核关闭。\n当前状态：模型仍需管理员重新上架，系统不会自动恢复销售。\n复核时间（北京时间）：%s\n\n建议处理：确认模型价格和授权线路无变化后，在管理后台重新上架。\n事故编号：%d\n通知编号：%d", ztapiAlertValue(d.Model), trigger, item.IncidentID, item.ID)
	}
	title := "【ZTAPI 模型下架】"
	impact := "所有已授权线路均经独立探针确认不可用，模型已自动下架，客户暂时无法调用。"
	recovery := "问题解决后，请让 Codex 复测并由管理员确认恢复，再重新上架。"
	if item.Kind == "route_alert" {
		title = "【ZTAPI 线路降级】"
		impact = "已停止使用这条故障线路，模型仍可正常调用，系统会改走其他已授权线路。"
		recovery = "请处理故障线路；无需重新上架模型。线路恢复须经复验确认。"
	}
	return fmt.Sprintf("%s\n模型：%s\n影响：%s\n故障线路：%s\n触发条件：%s\n类型：%s\n操作：%s\n错误：%s\nHTTP 状态：%s\n结束原因：%s\n耗时：%s\n结果有效：%s\n发生时间（北京时间）：%s\n\n建议处理：%s\n恢复方式：%s\n\n诊断探针编号：%s\n诊断上游请求编号：%s\n上游任务编号：%s\n触发用户请求编号：%s（仅用于定位最初线索，不是下架证据）\n触发请求对应上游编号：%s（仅用于定位最初线索）\n事故编号：%d\n通知编号：%d", title, ztapiAlertValue(d.Model), impact, ztapiHealthRouteLabel(d), rule, ztapiHealthModalityLabel(d.Modality), ztapiHealthOperationLabel(d.Operation), ztapiAlertValue(d.ErrorCode), status, ztapiAlertValue(strings.Join(d.FinishReasons, ",")), latency, resultValid, trigger, ztapiHealthAction(d), recovery, ztapiAlertValue(d.ProbeRequestID), ztapiAlertValue(d.UpstreamRequestID), ztapiAlertValue(d.UpstreamTaskID), ztapiAlertValue(d.TriggerRequestID), ztapiAlertValue(d.TriggerUpstreamRequestID), item.IncidentID, item.ID)
}

// Only this fixed Telegram origin receives the bot credential. Never log the
// request URL or raw HTTP errors, which include the credential in the path.
func sendZTAPIHealthTelegram(ctx context.Context, c ZTAPIHealthWorkerConfig, item ZTAPIHealthWorkItem) (bool, string, string) {
	return sendZTAPITelegramText(ctx, c, ztapiHealthTelegramText(item, c.Now()))
}

func sendZTAPITelegramText(ctx context.Context, c ZTAPIHealthWorkerConfig, text string) (bool, string, string) {
	if !ztapiTelegramTokenPattern.MatchString(c.TelegramBotToken) || !ztapiTelegramChatPattern.MatchString(c.TelegramChatID) {
		return false, "alert_recipient_missing_or_invalid", ""
	}
	body, err := common.Marshal(map[string]any{"chat_id": c.TelegramChatID, "text": text})
	if err != nil {
		return false, "alert_payload_error", ""
	}
	ctx, cancel := context.WithTimeout(ctx, c.AlertTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.TelegramBotToken+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return false, "alert_request_invalid", ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ztapiHealthHTTPClient(c.AlertTimeout, c.AlertTransport).Do(req)
	if err != nil {
		return false, "alert_transport_error", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "alert_http_status", ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return false, "alert_response_invalid", ""
	}
	// Persist only an allowlisted API receipt, not chat identity, message text,
	// Telegram's error description, or arbitrary upstream response fields.
	var receipt struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
			Date      int64 `json:"date"`
		} `json:"result"`
	}
	if common.Unmarshal(raw, &receipt) != nil || !receipt.OK || receipt.Result.MessageID <= 0 || receipt.Result.Date < 0 {
		return false, "alert_telegram_rejected", ""
	}
	safe, err := common.Marshal(receipt)
	if err != nil {
		return false, "alert_response_invalid", ""
	}
	return true, "telegram_accepted", string(safe)
}
