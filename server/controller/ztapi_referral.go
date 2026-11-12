package controller

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ztapiReferralSummaryProjection(s model.ZTAPIReferralSummary) gin.H {
	f := model.FormatZTAPIReferralMicros
	return gin.H{"invited_count": s.InvitedCount, "total_commission": f(s.TotalCommissionMicros), "reversed_commission": f(s.ReversedCommissionMicros), "frozen_commission": f(s.FrozenCommissionMicros), "available_commission": f(s.AvailableCommissionMicros), "withdrawal_reserved": f(s.WithdrawalReservedMicros), "paid_commission": f(s.PaidCommissionMicros), "manual_loss": f(s.ManualLossMicros)}
}

func ztapiReferralReadiness(c *gin.Context) (model.ZTAPIReferralReadinessInput, model.ZTAPIReferralReadinessReport, error) {
	input, extra, err := model.ProductionZTAPIReferralReadiness(c.Request.Context(), model.DB, time.Now().UTC())
	if err != nil {
		if c.Request.Context().Err() != nil {
			return input, model.ZTAPIReferralReadinessReport{}, c.Request.Context().Err()
		}
		// Catalog admission can fail closed without blocking existing cash obligations.
		return input, model.ZTAPIReferralReadinessReport{BlockedReasons: []string{"catalog_evidence_unavailable"}}, nil
	}
	worker := service.GetZTAPIReferralWorkerStatus()
	input.WorkerReady = input.WorkerReady && worker.Ready(time.Now().UTC())
	report, err := model.CheckZTAPIReferralReadiness(c.Request.Context(), input)
	if err != nil {
		return input, report, err
	}
	report.BlockedReasons = append(report.BlockedReasons, extra...)
	report.Ready = report.Ready && len(extra) == 0
	return input, report, nil
}

func ztapiReferralCampaignProjection(campaign *model.ZTAPIReferralCampaign, report model.ZTAPIReferralReadinessReport) gin.H {
	reasons := []string{}
	if !report.Ready {
		reasons = append(reasons, "采购型号、用量上界、平台费用或财务能力尚未全部核验，暂未开放新邀请")
	}
	if !campaign.Enabled && report.Ready {
		reasons = append(reasons, "活动尚未开启")
	}
	return gin.H{"enabled": campaign.Enabled, "accepting_invites": campaign.Enabled && report.Ready, "cny_accepting_orders": campaign.Enabled && report.Ready && service.ZTAPICNYReferralActive(time.Now().UTC()), "version": campaign.Version, "started_at": campaign.StartedAt, "rate": "0.05", "freeze_days": 7, "term_months": 12, "minimum_withdrawal": "20.000000", "network": "TRC20", "blocked_reasons": reasons, "rule_version": campaign.RuleVersion}
}

func GetZTAPIReferralPublicStatus(c *gin.Context) {
	campaign, err := model.GetZTAPIReferralCampaign(c.Request.Context())
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	report := model.ZTAPIReferralReadinessReport{}
	if campaign.Enabled {
		_, report, err = ztapiReferralReadiness(c)
		if err != nil {
			ztapiReferralHTTPError(c, err)
			return
		}
	}
	c.Header("Cache-Control", "no-store")
	projection := ztapiReferralCampaignProjection(campaign, report)
	if !campaign.Enabled {
		projection["blocked_reasons"] = []string{"推广活动尚未开放，本次注册不会建立现金返佣关系"}
	}
	common.ApiSuccess(c, projection)
}

func GetZTAPIReferralSelf(c *gin.Context) {
	user, ok := ztapiReferralUser(c)
	if !ok {
		return
	}
	if user.AffCode == "" {
		code, err := model.EnsureZTAPIReferralInviteCode(c.Request.Context(), user.Id)
		if err != nil {
			ztapiReferralHTTPError(c, err)
			return
		}
		user.AffCode = code
	}
	_, report, err := ztapiReferralReadiness(c)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	campaign, err := model.GetZTAPIReferralCampaign(c.Request.Context())
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	now := time.Now().UTC()
	summary, err := model.GetZTAPIReferralSummary(c.Request.Context(), user.Id, now)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	commissions, total, err := model.ListZTAPIReferralCommissions(c.Request.Context(), user.Id, 1, 20)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	withdrawals, withdrawalTotal, err := model.ListZTAPIReferralWithdrawals(c.Request.Context(), user.Id, 1, 20, "")
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	twoFA, err := model.GetTwoFAByUserId(user.Id)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	_, hashValid := service.ZTAPIPasswordHashOrDummy(user.Password)
	summaryData := ztapiReferralSummaryProjection(summary)
	summaryData["requires_totp"] = twoFA != nil && twoFA.IsEnabled
	summaryData["withdrawal_reauth_available"] = hashValid
	// The origin is fixed, not derived from Host or a forwarded header.
	inviteURL := ""
	if user.AffCode != "" {
		inviteURL = "https://ztapi.vip/register?invite=" + user.AffCode
	}
	common.ApiSuccess(c, gin.H{"campaign": ztapiReferralCampaignProjection(campaign, report), "invite_code": user.AffCode, "invite_url": inviteURL, "summary": summaryData, "commissions": ztapiReferralCommissionProjections(commissions, now), "commission_total": total, "withdrawals": ztapiReferralWithdrawalProjections(withdrawals), "withdrawal_total": withdrawalTotal, "server_time": now.Unix(), "requires_totp": twoFA != nil && twoFA.IsEnabled, "withdrawal_reauth_available": hashValid})
}

func ztapiReferralCommissionProjections(rows []model.ZTAPIReferralCommission, at time.Time) []gin.H {
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		status := "available"
		if row.AvailableAt > at.Unix() || row.CNYRefundBlocked {
			status = "frozen"
		}
		if row.ReversedMicros == row.CommissionMicros {
			status = "reversed"
		}
		method := "usdt"
		if row.CNYOrderID != nil {
			method = row.CNYMethod
		}
		items = append(items, gin.H{"id": row.ID, "invitee_label": fmt.Sprintf("好友 #%d", row.BindingID), "principal": model.FormatZTAPIReferralMicros(row.PrincipalMicros), "commission": model.FormatZTAPIReferralMicros(row.CommissionMicros), "reversed": model.FormatZTAPIReferralMicros(row.ReversedMicros), "earned_at": row.EarnedAt, "available_at": row.AvailableAt, "status": status, "payment_method": method})
	}
	return items
}

func ztapiReferralWithdrawalProjections(rows []model.ZTAPIReferralWithdrawal) []gin.H {
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, ztapiReferralWithdrawalProjection(row))
	}
	return items
}

func GetZTAPIReferralCommissions(c *gin.Context) {
	user, ok := ztapiReferralUser(c)
	if !ok {
		return
	}
	page, size, ok := ztapiReferralPage(c)
	if !ok {
		return
	}
	rows, total, err := model.ListZTAPIReferralCommissions(c.Request.Context(), user.Id, page, size)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": ztapiReferralCommissionProjections(rows, time.Now().UTC()), "total": total})
}

func GetZTAPIReferralWithdrawals(c *gin.Context) {
	user, ok := ztapiReferralUser(c)
	if !ok {
		return
	}
	page, size, ok := ztapiReferralPage(c)
	if !ok {
		return
	}
	rows, total, err := model.ListZTAPIReferralWithdrawals(c.Request.Context(), user.Id, page, size, "")
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": ztapiReferralWithdrawalProjections(rows), "total": total})
}

func RequestZTAPIReferralWithdrawal(c *gin.Context) {
	user, ok := ztapiReferralUser(c)
	if !ok {
		return
	}
	body, ok := ztapiSupplierRefundReadBody(c, "idempotency_key|amount|address|password|totp_code")
	var request struct {
		Key      string `json:"idempotency_key"`
		Amount   string `json:"amount"`
		Address  string `json:"address"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	if !ok || common.Unmarshal(body, &request) != nil {
		ztapiReferralHTTPError(c, model.ErrZTAPIReferralInvalid)
		return
	}
	hash, valid := service.ZTAPIPasswordHashOrDummy(user.Password)
	if !valid || !ztAPIVerifyPassword(request.Password, hash) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "code": "referral_reauthentication_required", "message": "请使用当前账号密码完成验证；第三方登录账号请先设置密码"})
		return
	}
	twoFA, err := model.GetTwoFAByUserId(user.Id)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	if twoFA != nil && twoFA.IsEnabled {
		valid, err := twoFA.ValidateTOTPAndUpdateUsage(request.TOTPCode)
		if err != nil || !valid {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "code": "referral_totp_required", "message": "请提供有效的二次验证码"})
			return
		}
	}
	amount, err := parseZTAPIReferralAmount(request.Amount)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	row, err := model.RequestZTAPIReferralWithdrawal(c.Request.Context(), model.ZTAPIReferralWithdrawalRequest{UserID: user.Id, AmountMicros: amount, Address: request.Address, IdempotencyKey: request.Key, Now: time.Now().UTC()})
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, ztapiReferralWithdrawalProjection(*row))
}

func userZTAPIReferralWithdrawalAction(c *gin.Context, confirm bool) {
	user, ok := ztapiReferralUser(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		ztapiReferralHTTPError(c, model.ErrZTAPIReferralInvalid)
		return
	}
	body, ok := ztapiSupplierRefundReadBody(c, "version")
	var request struct {
		Version int64 `json:"version"`
	}
	if !ok || common.Unmarshal(body, &request) != nil || request.Version < 1 {
		ztapiReferralHTTPError(c, model.ErrZTAPIReferralInvalid)
		return
	}
	input := model.ZTAPIReferralWithdrawalAction{WithdrawalID: id, ExpectedVersion: request.Version, UserID: user.Id, Now: time.Now().UTC()}
	var row *model.ZTAPIReferralWithdrawal
	if confirm {
		row, err = model.ConfirmZTAPIReferralWithdrawal(c.Request.Context(), input)
	} else {
		row, err = model.CancelZTAPIReferralWithdrawal(c.Request.Context(), input)
	}
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, ztapiReferralWithdrawalProjection(*row))
}

func ConfirmZTAPIReferralWithdrawal(c *gin.Context) { userZTAPIReferralWithdrawalAction(c, true) }
func CancelZTAPIReferralWithdrawal(c *gin.Context)  { userZTAPIReferralWithdrawalAction(c, false) }

func GetAdminZTAPIReferrals(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionReferralRead) {
		return
	}
	page, size, ok := ztapiReferralPage(c)
	if !ok {
		return
	}
	_, report, err := ztapiReferralReadiness(c)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	campaign, err := model.GetZTAPIReferralCampaign(c.Request.Context())
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	summary, err := model.GetZTAPIReferralSummary(c.Request.Context(), 0, time.Now().UTC())
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	rows, total, err := model.ListZTAPIReferralWithdrawals(c.Request.Context(), 0, page, size, c.Query("status"))
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	commissions, commissionTotal, err := model.ListZTAPIReferralCommissions(c.Request.Context(), 0, page, size)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"campaign": ztapiReferralCampaignProjection(campaign, report), "summary": ztapiReferralSummaryProjection(summary), "withdrawals": gin.H{"items": ztapiReferralWithdrawalProjections(rows), "total": total}, "commissions": gin.H{"items": ztapiReferralCommissionProjections(commissions, time.Now().UTC()), "total": commissionTotal}})
}

func GetAdminZTAPIReferralReadiness(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionReferralRead) {
		return
	}
	_, report, err := ztapiReferralReadiness(c)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	fingerprint, keys, err := model.ZTAPIReferralCatalogIdentity(c.Request.Context(), model.DB)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"readiness": report, "catalog_sha256": fingerprint, "required_dimension_keys": keys, "worker": service.GetZTAPIReferralWorkerStatus()})
}

func UpdateZTAPIReferralCampaign(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionReferralWrite) {
		return
	}
	operator, ok := ztapiReferralUser(c)
	if !ok {
		return
	}
	if operator.Role != common.RoleRootUser {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "code": "permission_denied", "message": "仅平台负责人可调整活动状态"})
		return
	}
	body, ok := ztapiSupplierRefundReadBody(c, "version|enabled")
	var request struct {
		Version int64 `json:"version"`
		Enabled *bool `json:"enabled"`
	}
	if !ok || common.Unmarshal(body, &request) != nil || request.Enabled == nil || request.Version < 0 {
		ztapiReferralHTTPError(c, model.ErrZTAPIReferralInvalid)
		return
	}
	input, report, err := ztapiReferralReadiness(c)
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	if *request.Enabled && !report.Ready {
		c.JSON(http.StatusConflict, gin.H{"success": false, "code": "referral_not_ready", "message": "成本和财务启用检查未通过", "data": report})
		return
	}
	row, err := model.SetZTAPIReferralCampaign(c.Request.Context(), model.ZTAPIReferralCampaignUpdate{ExpectedVersion: request.Version, OperatorID: c.GetInt("id"), Enabled: *request.Enabled, Now: time.Now().UTC(), Readiness: input})
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, ztapiReferralCampaignProjection(row, report))
}

func GetZTAPIReferralDowngradeStatus(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionReferralRead) {
		return
	}
	campaign, err := model.GetZTAPIReferralCampaign(c.Request.Context())
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	var bindings, entries, unknown int64
	for _, query := range []struct {
		target any
		count  *int64
		filter string
	}{{&model.ZTAPIReferralBinding{}, &bindings, ""}, {&model.ZTAPIReferralLedgerEntry{}, &entries, ""}, {&model.ZTAPIReferralWithdrawal{}, &unknown, "status IN ?"}} {
		db := model.DB.WithContext(c.Request.Context()).Model(query.target)
		if query.filter != "" {
			db = db.Where(query.filter, []string{"paying", "submitted_unknown", "manual_review"})
		}
		if err := db.Count(query.count).Error; err != nil {
			ztapiReferralHTTPError(c, err)
			return
		}
	}
	blocked := campaign.StartedAt > 0 || bindings > 0 || entries > 0 || unknown > 0
	common.ApiSuccess(c, gin.H{"downgrade_allowed": !blocked, "requires_referral_compatible_backend": blocked, "bindings": bindings, "ledger_entries": entries, "unresolved_payments": unknown, "reason": "已有返利承诺和未结付款必须由兼容版本继续履行；不能删除账本或直接切回无返利功能的后端"})
}

func AdminZTAPIReferralWithdrawalAction(c *gin.Context) {
	if !ztapiSupplierRefundPermission(c, common.PermissionReferralWrite) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		ztapiReferralHTTPError(c, model.ErrZTAPIReferralInvalid)
		return
	}
	body, ok := ztapiSupplierRefundReadBody(c, "version|fee|note|tx_id")
	var request struct {
		Version int64  `json:"version"`
		Fee     string `json:"fee"`
		Note    string `json:"note"`
		TxID    string `json:"tx_id"`
	}
	if !ok || common.Unmarshal(body, &request) != nil || request.Version < 1 {
		ztapiReferralHTTPError(c, model.ErrZTAPIReferralInvalid)
		return
	}
	input := model.ZTAPIReferralWithdrawalAction{WithdrawalID: id, ExpectedVersion: request.Version, OperatorID: c.GetInt("id"), Note: request.Note, TxID: strings.ToLower(request.TxID), Now: time.Now().UTC()}
	var row *model.ZTAPIReferralWithdrawal
	switch c.Param("action") {
	case "quote":
		input.FeeMicros, err = parseZTAPIReferralAmount(request.Fee)
		if err == nil {
			row, err = model.QuoteZTAPIReferralWithdrawal(c.Request.Context(), input)
		}
	case "reject":
		row, err = model.RejectZTAPIReferralWithdrawal(c.Request.Context(), input)
	case "claim":
		row, err = model.ClaimZTAPIReferralPaymentIntent(c.Request.Context(), input)
	case "recheck-payment":
		row, err = model.RecheckZTAPIReferralPaymentIntent(c.Request.Context(), input)
	case "submitted":
		row, err = model.SubmitZTAPIReferralManualPayment(c.Request.Context(), input)
	case "confirm-paid":
		var current model.ZTAPIReferralWithdrawal
		err = model.DB.WithContext(c.Request.Context()).First(&current, id).Error
		if err != nil {
			break
		}
		if current.Version != input.ExpectedVersion || current.HolderID != input.OperatorID || (current.Status != "paying" && current.Status != "submitted_unknown") {
			err = model.ErrZTAPIReferralConflict
			break
		}
		ctx := c.Request.Context()
		var evidence *service.ZTAPIReferralVerifiedReceipt
		evidence, err = service.VerifyZTAPIReferralTronPayment(ctx, os.Getenv("TRONGRID_API_KEY"), input.TxID, current.Address, current.NetAmountMicros, current.CreatedAt)
		if errors.Is(err, service.ErrZTAPIReferralPaymentUnverified) {
			c.JSON(http.StatusConflict, gin.H{"success": false, "code": "referral_payment_unverified", "message": "尚未取得匹配的已确认链上转账，提现仍保持占用，请登记待确认后重试"})
			return
		}
		if err != nil {
			break
		}
		row, err = model.ConfirmZTAPIReferralManualPayment(ctx, model.ZTAPIReferralPaymentConfirmation{WithdrawalID: id, ExpectedVersion: input.ExpectedVersion, OperatorID: input.OperatorID, Now: input.Now, Evidence: model.ZTAPIReferralPaymentEvidence{TxID: evidence.TxID, LogIndex: evidence.LogIndex, Network: evidence.Network, ContractAddress: evidence.ContractAddress, From: evidence.From, To: evidence.To, AmountMicros: evidence.AmountMicros, BlockTimestampMS: evidence.BlockTimestampMS, BlockNumber: evidence.BlockNumber, ReceiptSHA256: evidence.ReceiptSHA256}})
	default:
		err = model.ErrZTAPIReferralInvalid
	}
	if err != nil {
		ztapiReferralHTTPError(c, err)
		return
	}
	common.ApiSuccess(c, ztapiReferralWithdrawalProjection(*row))
}
