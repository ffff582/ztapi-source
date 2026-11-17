// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/support/bridgeapi"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var handoffIntent = regexp.MustCompile(`(?i)转人工|找人工|人工客服|提交工单|创建工单|human support|talk to.{0,8}human|handoff`)
var accountIntent = regexp.MustCompile(`(?i)我的.{0,12}(?:请求|账单|余额|用量|扣费|扣款)|最近.{0,8}请求|查.{0,8}(?:账户|账单|余额)|request[_ -]?id\s*[:=]|my (?:requests|balance|billing)|recent requests`)
var diagnoseID = regexp.MustCompile(`(?i)request[_ -]?id\s*[:=]\s*([A-Za-z0-9_-]{1,128})`)
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var bindingToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func actionKey(botID, updateID int64) string { return fmt.Sprintf("tg-action:%d:%d", botID, updateID) }

func (w *Worker) bridgeCall(ctx context.Context, req bridgeapi.Request) (bridgeapi.Result, error) {
	if w.cfg.Bridge == nil {
		return bridgeapi.Result{}, errors.New("Telegram bridge disabled")
	}
	if err := ctx.Err(); err != nil {
		return bridgeapi.Result{}, err
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return w.cfg.Bridge.Execute(call, req)
}

func (w *Worker) workflow(ctx context.Context, row inbox) (bool, error) {
	ownerCommand := row.Command == "case_reply" || row.Command == "case_resolve"
	wantsAccount := accountIntent.MatchString(row.Text) && (!publicGuide.MatchString(row.Text) || personalRecordOwner.MatchString(row.Text)) || row.Sensitive || row.Command == "binding" || row.Command == "recent" || row.Command == "diagnose"
	wantsHandoff := row.Command == "handoff" || handoffIntent.MatchString(row.Text)
	if row.IsGroup {
		if ownerCommand || wantsAccount || wantsHandoff || row.Command == "start" {
			return true, w.complete(ctx, row, privateReply, "notice")
		}
		return false, nil
	}
	if ownerCommand {
		return true, w.ownerCase(ctx, row)
	}
	if row.Command == "start" {
		if !row.BindingAttempt {
			return true, w.complete(ctx, row, "我是小 Z。一般配置问题可以直接提问；账户绑定请登录 "+bindingURL+"。", "notice")
		}
		token, openErr := w.openBinding(row)
		if openErr != nil {
			return true, w.complete(ctx, row, "安全绑定暂不可用或链接无效，请返回网站重新发起并确认："+bindingURL, "notice")
		}
		result, err := w.bridgeCall(ctx, bridgeapi.Request{Action: "claim_binding", TelegramID: row.SenderID, Token: token, Username: row.Username, DisplayName: row.DisplayName})
		if err != nil || !result.Pending {
			return true, w.complete(ctx, row, "绑定申请未确认，链接可能失效或暂时不可用。请返回网站重新发起并确认："+bindingURL, "notice")
		}
		return true, w.complete(ctx, row, "已领取绑定申请，但尚未完成绑定。请返回网站 "+bindingURL+" 核对 Telegram 数字 ID 并明确确认；用户名不代表平台账户身份。", "notice")
	}
	if !wantsAccount && !wantsHandoff {
		return false, nil
	}
	status, err := w.bridgeCall(ctx, bridgeapi.Request{Action: "binding_status", TelegramID: row.SenderID})
	if wantsHandoff {
		if err != nil || !status.Bound {
			return true, w.unverifiedIntake(ctx, row)
		}
		result, e := w.bridgeCall(ctx, bridgeapi.Request{Action: "create_ticket", TelegramID: row.SenderID, Content: w.clean(row.Text), IdempotencyKey: actionKey(w.cfg.BotID, row.UpdateID)})
		if e != nil || !result.Bound || result.Ticket == nil || result.Ticket.ID == 0 {
			return true, w.complete(ctx, row, "尚无法确认站内工单已创建。请到 "+bindingURL+" 核实或提交；不会假定人工已接管。", "notice")
		}
		return true, w.completeAccount(ctx, row, fmt.Sprintf("站内工单 #%d 已记录。状态请在 %s 查看；这不表示人工已接管或问题已修复。", result.Ticket.ID, bindingURL), result.BindingGeneration)
	}
	if err != nil || !status.Bound {
		return true, w.complete(ctx, row, bindingReply, "notice")
	}
	if row.Command == "binding" {
		return true, w.completeAccount(ctx, row, "网站已确认当前 Telegram 数字身份的绑定。解绑或重新绑定请在 "+bindingURL+" 操作。", status.BindingGeneration)
	}
	requestID := ""
	if match := diagnoseID.FindStringSubmatch(row.Text); len(match) == 2 {
		requestID = match[1]
	}
	if row.Command == "diagnose" {
		parts := strings.Fields(row.Text)
		if len(parts) != 2 || !validRequestID.MatchString(parts[1]) {
			return true, w.complete(ctx, row, "请提供一个本人 request_id；不要发送密钥。", "notice")
		}
		requestID = parts[1]
	}
	action := "recent_requests"
	if requestID != "" {
		action = "diagnose"
	}
	result, e := w.bridgeCall(ctx, bridgeapi.Request{Action: action, TelegramID: row.SenderID, RequestID: requestID})
	if e != nil || !result.Bound {
		return true, w.complete(ctx, row, "当前无法核实本人记录，请到 "+bindingURL+" 查看。不存在或不可访问的记录不会进一步披露。", "notice")
	}
	var diagnoses []bridgeapi.Diagnosis
	if action == "diagnose" {
		if result.Diagnosis != nil && result.Diagnosis.RequestID == requestID {
			diagnoses = []bridgeapi.Diagnosis{*result.Diagnosis}
		}
	} else {
		diagnoses = result.Requests
		if len(diagnoses) > 5 {
			diagnoses = diagnoses[:5]
		}
	}
	if len(diagnoses) == 0 {
		return true, w.complete(ctx, row, "当前没有可展示的本人请求记录；费用与用量不能据此视为零。请到 "+bindingURL+" 核实。", "notice")
	}
	var answer strings.Builder
	answer.WriteString("以下为网站当前授权返回的本人请求记录，不代表已退款或已修复：\n")
	for _, d := range diagnoses {
		charged := d.ChargedU
		if charged == "" {
			charged = "未知"
		}
		usage := "未收到，用量未知"
		if d.UsageReceived {
			usage = "已收到；具体数值以网站记录为准"
		}
		fmt.Fprintf(&answer, "request_id=%s\n模型：%s\n结果：%s\n诊断：%s\n扣费 U：%s；用量：%s\n", w.clean(d.RequestID), w.clean(d.Model), w.clean(d.Outcome), w.clean(d.Summary), w.clean(charged), usage)
	}
	answer.WriteString(bindingURL)
	return true, w.completeAccount(ctx, row, answer.String(), result.BindingGeneration)
}

func (w *Worker) unverifiedIntake(ctx context.Context, row inbox) error {
	id := fmt.Sprintf("TG%d-%d", w.cfg.BotID, row.UpdateID)
	entry := intake{ID: id, SenderID: row.SenderID, ChatID: row.ChatID, ThreadID: row.ThreadID, Content: w.clean(row.Text), Status: "open", CreatedAt: w.now(), UpdatedAt: w.now()}
	owner := outbox{Key: "case-owner:" + id, ChatID: w.cfg.OwnerID, Text: w.clean(fmt.Sprintf("未核验身份的 Telegram 线索 %s\n%s\n此线索不是平台账户证据；只能在私聊中处理。", id, entry.Content)), Kind: "intake_owner", State: "pending", CreatedAt: w.now(), NextAttemptAt: w.now()}
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&owner).Error
	})
	if err != nil {
		return errors.New("Telegram unverified intake persistence failed")
	}
	return w.complete(ctx, row, "已在机器人本地记录未核验身份的线索 "+id+"，待通知负责人；尚未核验平台账户，也不表示站内工单已创建或人工已接管。账户绑定请到 "+bindingURL, "notice")
}

func (w *Worker) ownerCase(ctx context.Context, row inbox) error {
	if row.IsGroup || row.SenderID != w.cfg.OwnerID || row.ChatID != w.cfg.OwnerID {
		return w.complete(ctx, row, "仅配置的负责人可以在与机器人的私聊中处理线索。", "notice")
	}
	parts := strings.Fields(row.Text)
	if len(parts) < 2 || !regexp.MustCompile(`^TG[0-9]+-[0-9]+$`).MatchString(parts[1]) {
		return w.complete(ctx, row, "请提供有效的线索编号。", "notice")
	}
	var entry intake
	if w.db.WithContext(ctx).Where("id = ?", parts[1]).Take(&entry).Error != nil || entry.ChatID != entry.SenderID || entry.SenderID <= 0 {
		return w.complete(ctx, row, "线索不存在或不可处理。", "notice")
	}
	if entry.Status != "open" {
		return w.complete(ctx, row, "该线索已经结束。", "notice")
	}
	if row.Command == "case_reply" && len(parts) < 3 {
		return w.complete(ctx, row, "请提供回复内容。", "notice")
	}
	if row.Command == "case_resolve" && len(parts) != 2 {
		return w.complete(ctx, row, "结束线索只接受一个编号。", "notice")
	}
	text := "未核验身份的线索 " + entry.ID + " 已结束。这不表示退款、修复或平台账户核验已经完成。"
	if row.Command == "case_reply" {
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(row.Text, parts[0])), parts[1]))
		if unsupportedClaim.MatchString(body) || falseOperation.MatchString(body) || w.hasCredentials(body) {
			return w.complete(ctx, row, "未发送：回复不能包含凭据或无法核实的退款、修复、后台处理声明。", "notice")
		}
		text = "负责人对未核验身份线索 " + entry.ID + " 的回复：\n" + body
	}
	reply := outbox{Key: fmt.Sprintf("case-reply:%d", row.UpdateID), ChatID: entry.ChatID, ThreadID: entry.ThreadID, Text: w.clean(text), Kind: "intake_reply", State: "pending", CreatedAt: w.now(), NextAttemptAt: w.now()}
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if row.Command == "case_resolve" {
			if err := tx.Model(&intake{}).Where("id = ? AND status = ?", entry.ID, "open").Updates(map[string]any{"status": "resolved", "updated_at": w.now()}).Error; err != nil {
				return err
			}
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reply).Error
	})
	if err != nil {
		return errors.New("Telegram owner reply persistence failed")
	}
	return w.complete(ctx, row, "线索处理已记录，私聊通知已进入待发送队列。", "control")
}
