package service

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// BillingSession — 统一计费会话
// ---------------------------------------------------------------------------

// BillingSession 封装单次请求的预扣费/结算/退款生命周期。
// 实现 relaycommon.BillingSettler 接口。
type BillingSession struct {
	relayInfo        *relaycommon.RelayInfo
	funding          FundingSource
	operationID      string
	reserveSequence  uint64
	preConsumedQuota int  // 实际预扣额度（信任用户可能为 0）
	tokenConsumed    int  // 令牌额度实际扣减量
	extraReserved    int  // 发送前补充预扣的额度（订阅退款时需要单独回滚）
	fundingSettled   bool // funding.Settle 已成功，资金来源已提交
	settled          bool // Settle 全部完成（资金 + 令牌）
	tokenRefunded    bool
	fundingRefunded  bool
	extraRefunded    bool
	refunded         bool // Refund 全部完成
	mu               sync.Mutex
}

// Settle 根据实际消耗额度进行结算。
// 资金来源和令牌额度分两步提交：若资金来源已提交但令牌调整失败，
// 会标记 fundingSettled 防止 Refund 对已提交的资金来源执行退款。
func (s *BillingSession) Settle(actualQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled {
		return nil
	}
	s.ensureRequestID()
	fundingDelta := actualQuota - s.preConsumedQuota
	tokenDelta := actualQuota - s.tokenConsumed
	// 1) 调整资金来源（仅在尚未提交时执行，防止重复调用）
	if !s.fundingSettled {
		if err := s.funding.Settle(fundingDelta); err != nil {
			return err
		}
		s.fundingSettled = true
		if s.funding.Source() == BillingSourceSubscription {
			s.relayInfo.SubscriptionPostDelta += int64(fundingDelta)
		}
	}
	// 2) 调整令牌额度
	var tokenErr error
	if !s.relayInfo.TokenUnlimited && tokenDelta != 0 {
		if tokenDelta > 0 {
			tokenErr = model.DecreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKeyHash, tokenDelta)
			if errors.Is(tokenErr, model.ErrInsufficientTokenQuota) {
				tokenErr = model.ExhaustTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKeyHash)
				if tokenErr == nil {
					common.SysLog(fmt.Sprintf("finite token exhausted during settlement (userId=%d, tokenId=%d, delta=%d)",
						s.relayInfo.UserId, s.relayInfo.TokenId, tokenDelta))
				}
			}
		} else {
			tokenErr = model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKeyHash, -tokenDelta)
		}
		if tokenErr != nil {
			// 资金来源已提交，令牌调整失败只能记录日志；标记 settled 防止 Refund 误退资金
			common.SysLog(fmt.Sprintf("error adjusting token quota after funding settled (userId=%d, tokenId=%d, delta=%d): %s",
				s.relayInfo.UserId, s.relayInfo.TokenId, tokenDelta, tokenErr.Error()))
		}
	}
	s.settled = true
	return tokenErr
}

// Refund synchronously returns all reserved funding and token quota.
// Each completed step is tracked so a failed later step can be retried safely.
func (s *BillingSession) Refund(c *gin.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.settled || s.refunded || !s.needsRefundLocked() {
		return nil
	}
	s.ensureRequestID()

	logger.LogInfo(c, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（token_quota=%s, funding=%s）",
		s.relayInfo.UserId,
		logger.FormatQuota(s.tokenConsumed),
		s.funding.Source(),
	))

	if !s.tokenRefunded {
		if s.tokenConsumed > 0 && !s.relayInfo.TokenUnlimited {
			if err := queueAndProcessBillingRefund(model.BillingRefundPending{
				UserID:         s.relayInfo.UserId,
				TokenID:        s.relayInfo.TokenId,
				TokenKeyHash:   s.relayInfo.TokenKeyHash,
				Amount:         int64(s.tokenConsumed),
				Kind:           model.BillingRefundKindToken,
				IdempotencyKey: s.billingRefundIdempotencyKey("token"),
				RequestID:      s.relayInfo.RequestId,
			}); err != nil {
				common.SysLog("error refunding token quota: " + err.Error())
				return err
			}
		}
		s.tokenRefunded = true
	}

	if !s.fundingRefunded {
		if err := s.funding.Refund(); err != nil {
			common.SysLog("error refunding billing source: " + err.Error())
			return err
		}
		s.fundingRefunded = true
	}

	if !s.extraRefunded {
		if s.extraReserved > 0 &&
			s.funding.Source() == BillingSourceSubscription &&
			s.relayInfo.SubscriptionId > 0 {
			if err := model.PostConsumeUserSubscriptionDelta(
				s.relayInfo.SubscriptionId,
				-int64(s.extraReserved),
			); err != nil {
				common.SysLog("error refunding subscription extra reserved quota: " + err.Error())
				return err
			}
		}
		s.extraRefunded = true
	}

	s.refunded = true
	return nil
}

// NeedsRefund 返回是否存在需要退还的预扣状态。
func (s *BillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsRefundLocked()
}

func (s *BillingSession) needsRefundLocked() bool {
	if s.settled || s.refunded || s.fundingSettled {
		// fundingSettled 时资金来源已提交结算，不能再退预扣费
		return false
	}
	if s.preConsumedQuota > 0 || s.tokenConsumed > 0 {
		return true
	}
	return false
}

// GetPreConsumedQuota 返回实际预扣的额度。
func (s *BillingSession) GetPreConsumedQuota() int {
	return s.preConsumedQuota
}

func (s *BillingSession) Reserve(targetQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.settled || s.refunded || targetQuota <= 0 {
		return nil
	}
	s.ensureRequestID()

	fundingDelta := 0
	if targetQuota > s.preConsumedQuota {
		fundingDelta = targetQuota - s.preConsumedQuota
	}
	tokenDelta := 0
	if !s.relayInfo.TokenUnlimited && targetQuota > s.tokenConsumed {
		tokenDelta = targetQuota - s.tokenConsumed
	}
	if fundingDelta == 0 && tokenDelta == 0 {
		return nil
	}

	if fundingDelta > 0 {
		if err := s.reserveFunding(fundingDelta); err != nil {
			return err
		}
		s.reserveSequence++
	}
	if tokenDelta > 0 {
		if err := s.reserveToken(tokenDelta); err != nil {
			if fundingDelta > 0 {
				s.rollbackFundingReserve(fundingDelta)
			}
			return err
		}
	}

	s.preConsumedQuota += fundingDelta
	s.tokenConsumed += tokenDelta
	s.extraReserved += fundingDelta
	s.syncRelayInfo()
	return nil
}

// ---------------------------------------------------------------------------
// PreConsume — 统一预扣费入口
// ---------------------------------------------------------------------------

// preConsume 执行预扣费：令牌预扣 -> 资金来源预扣。
// 任一步骤失败时原子回滚已完成的步骤。
func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError {
	s.ensureRequestID()
	effectiveQuota := quota

	if effectiveQuota > 0 {
		logger.LogInfo(c, fmt.Sprintf("用户 %d 需要预扣费 %s (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(effectiveQuota), s.funding.Source()))
	}

	// ---- 1) 预扣令牌额度 ----
	if quota > 0 && !s.relayInfo.TokenUnlimited {
		if err := PreConsumeTokenQuota(s.relayInfo, quota); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		s.tokenConsumed = quota
	}

	// ---- 2) 预扣资金来源 ----
	if err := s.funding.PreConsume(effectiveQuota); err != nil {
		// 预扣费失败，回滚令牌额度
		if s.tokenConsumed > 0 {
			if rollbackErr := queueAndProcessBillingRefund(model.BillingRefundPending{
				UserID:         s.relayInfo.UserId,
				TokenID:        s.relayInfo.TokenId,
				TokenKeyHash:   s.relayInfo.TokenKeyHash,
				Amount:         int64(s.tokenConsumed),
				Kind:           model.BillingRefundKindToken,
				IdempotencyKey: s.billingRefundIdempotencyKey("token-preconsume"),
				RequestID:      s.relayInfo.RequestId,
			}); rollbackErr != nil {
				common.SysLog(fmt.Sprintf("error rolling back token quota (userId=%d, tokenId=%d, amount=%d, fundingErr=%s): %s",
					s.relayInfo.UserId, s.relayInfo.TokenId, s.tokenConsumed, err.Error(), rollbackErr.Error()))
			}
			s.tokenConsumed = 0
		}
		// TODO: model 层应定义哨兵错误（如 ErrNoActiveSubscription），用 errors.Is 替代字符串匹配
		errMsg := err.Error()
		if strings.Contains(errMsg, "no active subscription") || strings.Contains(errMsg, "subscription quota insufficient") {
			return types.NewErrorWithStatusCode(fmt.Errorf("订阅额度不足或未配置订阅: %s", errMsg), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		if errors.Is(err, model.ErrInsufficientUserQuota) {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}

	s.preConsumedQuota = effectiveQuota

	// ---- 同步 RelayInfo 兼容字段 ----
	s.syncRelayInfo()

	return nil
}

func (s *BillingSession) ensureRequestID() string {
	if s.relayInfo == nil {
		return ""
	}
	requestID := strings.TrimSpace(s.relayInfo.RequestId)
	switch funding := s.funding.(type) {
	case *WalletFunding:
		if requestID == "" {
			requestID = strings.TrimSpace(funding.requestId)
		}
	case *SubscriptionFunding:
		if requestID == "" {
			requestID = strings.TrimSpace(funding.requestId)
		}
	}
	if requestID == "" {
		requestID = common.GetUUID()
	}
	s.relayInfo.RequestId = requestID
	switch funding := s.funding.(type) {
	case *WalletFunding:
		funding.requestId = requestID
	case *SubscriptionFunding:
		funding.requestId = requestID
	}
	return requestID
}

func (s *BillingSession) reserveFunding(delta int) error {
	switch funding := s.funding.(type) {
	case *WalletFunding:
		err := applyWalletBalanceMutation(
			model.ReserveUserBalanceDebit,
			funding.userId,
			int64(delta),
			s.billingRefundIdempotencyKey(fmt.Sprintf("wallet-reserve-debit-%d", s.reserveSequence)),
			funding.requestId,
		)
		if err != nil {
			if errors.Is(err, model.ErrInsufficientUserQuota) {
				return types.NewErrorWithStatusCode(err, types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
			}
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		funding.consumed += delta
		return nil
	case *SubscriptionFunding:
		if err := model.PostConsumeUserSubscriptionDelta(funding.subscriptionId, int64(delta)); err != nil {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("订阅额度不足或未配置订阅: %s", err.Error()),
				types.ErrorCodeInsufficientUserQuota,
				http.StatusForbidden,
				types.ErrOptionWithSkipRetry(),
				types.ErrOptionWithNoRecordErrorLog(),
			)
		}
		return nil
	default:
		return types.NewError(fmt.Errorf("unsupported funding source: %s", s.funding.Source()), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
}

func (s *BillingSession) rollbackFundingReserve(delta int) {
	switch funding := s.funding.(type) {
	case *WalletFunding:
		if err := queueAndProcessBillingRefund(model.BillingRefundPending{
			UserID:         funding.userId,
			Amount:         int64(delta),
			Kind:           model.BillingRefundKindWallet,
			IdempotencyKey: s.billingRefundIdempotencyKey(fmt.Sprintf("wallet-reserve-%d", s.reserveSequence)),
			RequestID:      funding.requestId,
		}); err != nil {
			common.SysLog("error rolling back wallet funding reserve: " + err.Error())
		} else {
			funding.consumed -= delta
		}
	case *SubscriptionFunding:
		if err := model.PostConsumeUserSubscriptionDelta(funding.subscriptionId, -int64(delta)); err != nil {
			common.SysLog("error rolling back subscription funding reserve: " + err.Error())
		}
	}
}

func (s *BillingSession) billingRefundIdempotencyKey(kind string) string {
	if strings.TrimSpace(s.operationID) == "" {
		s.operationID = common.GetUUID()
	}
	return billingRefundIdempotencyKey(s.operationID, kind)
}

func billingRefundIdempotencyKey(operationID string, kind string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(operationID)))
	return fmt.Sprintf("billing-refund:%x:%s", digest[:16], kind)
}

func queueAndProcessBillingRefund(candidate model.BillingRefundPending) error {
	pending, err := model.EnsureBillingRefundPending(candidate)
	if err != nil {
		return err
	}
	return model.ProcessBillingRefundPending(pending.ID)
}

func (s *BillingSession) reserveToken(delta int) error {
	if delta <= 0 || s.relayInfo.TokenUnlimited {
		return nil
	}
	if err := PreConsumeTokenQuota(s.relayInfo, delta); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return nil
}

// syncRelayInfo 将 BillingSession 的状态同步到 RelayInfo 的兼容字段上。
func (s *BillingSession) syncRelayInfo() {
	info := s.relayInfo
	info.FinalPreConsumedQuota = s.preConsumedQuota
	info.BillingSource = s.funding.Source()

	if sub, ok := s.funding.(*SubscriptionFunding); ok {
		info.SubscriptionId = sub.subscriptionId
		info.SubscriptionPreConsumed = sub.preConsumed + int64(s.extraReserved)
		info.SubscriptionPostDelta = 0
		info.SubscriptionAmountTotal = sub.AmountTotal
		info.SubscriptionAmountUsedAfterPreConsume = sub.AmountUsedAfter + int64(s.extraReserved)
		info.SubscriptionPlanId = sub.PlanId
		info.SubscriptionPlanTitle = sub.PlanTitle
	} else {
		info.SubscriptionId = 0
		info.SubscriptionPreConsumed = 0
	}
}

// ---------------------------------------------------------------------------
// NewBillingSession 工厂 — 根据计费偏好创建会话并处理回退
// ---------------------------------------------------------------------------

// NewBillingSession 根据用户计费偏好创建 BillingSession，处理 subscription_first / wallet_first 的回退。
func NewBillingSession(c *gin.Context, relayInfo *relaycommon.RelayInfo, preConsumedQuota int) (*BillingSession, *types.NewAPIError) {
	if relayInfo == nil {
		return nil, types.NewError(fmt.Errorf("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	pref := common.NormalizeBillingPreference(relayInfo.UserSetting.BillingPreference)

	// 钱包路径需要先检查用户额度
	tryWallet := func() (*BillingSession, *types.NewAPIError) {
		userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if userQuota <= 0 {
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)),
				types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		if userQuota-preConsumedQuota < 0 {
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("预扣费额度失败, 用户剩余额度: %s, 需要预扣费额度: %s", logger.FormatQuota(userQuota), logger.FormatQuota(preConsumedQuota)),
				types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		relayInfo.UserQuota = userQuota

		operationID := common.GetUUID()
		session := &BillingSession{
			operationID: operationID,
			relayInfo:   relayInfo,
			funding: &WalletFunding{
				operationID: operationID,
				requestId:   relayInfo.RequestId,
				userId:      relayInfo.UserId,
			},
		}
		if apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	trySubscription := func() (*BillingSession, *types.NewAPIError) {
		subConsume := int64(preConsumedQuota)
		if subConsume <= 0 {
			subConsume = 1
		}
		session := &BillingSession{
			operationID: common.GetUUID(),
			relayInfo:   relayInfo,
			funding: &SubscriptionFunding{
				requestId: relayInfo.RequestId,
				userId:    relayInfo.UserId,
				modelName: relayInfo.OriginModelName,
				amount:    subConsume,
			},
		}
		// 必须传 subConsume 而非 preConsumedQuota，保证 SubscriptionFunding.amount、
		// preConsume 参数和 FinalPreConsumedQuota 三者一致，避免订阅多扣费。
		if apiErr := session.preConsume(c, int(subConsume)); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	switch pref {
	case "subscription_only":
		return trySubscription()
	case "wallet_only":
		return tryWallet()
	case "wallet_first":
		session, err := tryWallet()
		if err != nil {
			if err.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				return trySubscription()
			}
			return nil, err
		}
		return session, nil
	case "subscription_first":
		fallthrough
	default:
		hasSub, subCheckErr := model.HasActiveUserSubscription(relayInfo.UserId)
		if subCheckErr != nil {
			return nil, types.NewError(subCheckErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if !hasSub {
			return tryWallet()
		}
		session, apiErr := trySubscription()
		if apiErr != nil {
			if apiErr.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				return tryWallet()
			}
			return nil, apiErr
		}
		return session, nil
	}
}
