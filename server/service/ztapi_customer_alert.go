package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func ztapiCustomerSafeLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 || strings.Contains(value, "sk-") {
		return ""
	}
	for _, ch := range value {
		if unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch) {
			return ""
		}
	}
	return value
}

func ztapiCustomerAlertMetadata(ctx context.Context, store *model.ZTAPIHealthStore, job model.ZTAPIHealthOutbox) ZTAPIHealthAlertMetadata {
	d := ZTAPIHealthAlertMetadata{MetadataStatus: "available", Occurrences: job.Occurrences, OpenedAt: &job.CreatedAt}
	var event model.ZTAPIHealthEvent
	if err := store.DB.WithContext(ctx).First(&event, job.EventID).Error; err != nil || event.ModelID != job.ModelID || event.Generation != job.Generation || event.Source != "real" {
		d.MetadataStatus = "unknown"
		return d
	}
	d.Model, d.ErrorCode, d.HTTPStatus = event.PublicModel, event.Reason, &event.HTTPStatus
	d.TriggerRequestID, d.UpstreamRequestID = event.RequestID, event.UpstreamRequestID
	d.UpstreamTaskID, d.ChannelID = event.UpstreamTaskID, event.ChannelID
	d.Modality, d.Operation, d.Stream = event.Modality, event.Operation, event.Stream
	d.EntryProtocol, d.UpstreamProtocol = event.EntryProtocol, event.UpstreamProtocol
	d.LatencyMilliseconds, d.ResultValid = &event.LatencyMilliseconds, &event.ResultValid
	var outcome struct{ FinishReasons []string }
	if len(event.Outcome) <= 64*1024 && common.UnmarshalJsonStr(event.Outcome, &outcome) == nil {
		d.FinishReasons = outcome.FinishReasons
	}
	var request model.ZTAPIHealthRequest
	if store.DB.WithContext(ctx).Select("user_id").First(&request, "execution_id = ?", event.ExecutionID).Error == nil {
		d.UserID = request.UserID
		var user model.User
		if store.DB.WithContext(ctx).Select("id", "username").First(&user, request.UserID).Error == nil {
			d.Username = user.Username
		}
	}
	var channel model.Channel
	if store.DB.WithContext(ctx).Select("id", "name").First(&channel, event.ChannelID).Error == nil {
		d.ChannelName = channel.Name
	}
	return d
}

func ztapiCustomerErrorLabel(d ZTAPIHealthAlertMetadata) string {
	switch d.ErrorCode {
	case "upstream_transport_error":
		return "上游连接失败或超时，未正常返回结果"
	case "empty_output":
		return "上游返回空结果，没有有效内容"
	case "upstream_auth", "upstream_model_permission":
		return "上游密钥或模型授权异常"
	case "upstream_quota":
		return "上游额度不足（不是客户余额不足）"
	case "upstream_rate_limit":
		return "上游限流，暂时拒绝请求"
	case "malformed_upstream_response", "missing_native_terminal", "unrecognized_terminal":
		return "上游响应格式异常或输出中断"
	case "invalid_media_result", "provider_task_failed":
		return "上游媒体任务失败或结果无效"
	default:
		return "上游服务异常，未正常完成请求"
	}
}

func ztapiCustomerAlertText(item ZTAPIHealthWorkItem, now time.Time) string {
	d := ztapiHealthSafeAlertMetadata(item, now)
	user := ztapiAlertValue(d.Username)
	if d.Username == "" && d.UserID > 0 {
		user = fmt.Sprintf("用户 #%d（用户名暂时无法读取）", d.UserID)
	}
	channel := ztapiAlertValue(d.ChannelName)
	if d.ChannelName == "Yunxin pool" {
		channel = "号池"
	} else if strings.HasPrefix(d.ChannelName, "Yunxin enterprise") {
		channel = "企业"
	}
	status := "未提供"
	if d.HTTPStatus != nil && *d.HTTPStatus >= 100 && *d.HTTPStatus <= 599 {
		status = fmt.Sprintf("%d", *d.HTTPStatus)
	}
	when := "未提供"
	if d.OpenedAt != nil && *d.OpenedAt > 0 {
		when = time.Unix(*d.OpenedAt, 0).In(ztapiBeijingTime).Format("2006-01-02 15:04:05")
	}
	return fmt.Sprintf("【客户调用异常】\n用户：%s\n模型：%s\n通道：%s（编号 %d）\n原因：%s\n错误代码：%s\nHTTP 状态：%s\n结束原因：%s\n发生时间（北京时间）：%s\n同类异常：已记录 %d 次（同一用户、模型及通道，10 分钟内不重复刷屏）\n\n影响：本次请求未正常完成；这条通知不代表模型已下架，不会自行发起收费测试。\n处理建议：请让 Codex 检查通道配置及响应记录；如属上游故障，请携带下方请求编号询问上游。不会自动切换到未授权的企业通道。\n\n站内请求编号：%s\n上游请求编号：%s\n上游任务编号：%s\n通知编号：%d", user, ztapiAlertValue(d.Model), channel, d.ChannelID, ztapiCustomerErrorLabel(d), ztapiAlertValue(d.ErrorCode), status, ztapiAlertValue(strings.Join(d.FinishReasons, ",")), when, d.Occurrences, ztapiAlertValue(d.TriggerRequestID), ztapiAlertValue(d.UpstreamRequestID), ztapiAlertValue(d.UpstreamTaskID), item.ID)
}
