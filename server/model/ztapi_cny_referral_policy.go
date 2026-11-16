package model

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

const ZTAPICNYReferralRuleVersion = "cny-credited-principal-5pct-v1"

func cnyReferralOrderAdmission(ctx context.Context, order CNYTopUpOrder) *ZTAPICNYReferralPolicy {
	if os.Getenv("ZTAPI_CNY_REFERRAL_POLICY_FILE") == "" {
		return nil
	}
	// A slow or unavailable referral gate must not block ordinary checkout.
	ctx, cancel := context.WithTimeout(ctx, 350*time.Millisecond)
	defer cancel()
	policy, err := ProductionZTAPICNYReferralPolicy(ctx, time.Unix(order.CreatedAt, 0))
	if err != nil || ctx.Err() != nil || order.MerchantNo != policy.MerchantNo || order.AppID != policy.AppID || order.CNYPerUnit != policy.CNYPerUnit {
		return nil
	}
	return &policy
}

type ZTAPICNYReferralPolicy struct {
	Version          int                         `json:"version"`
	EnabledAt        int64                       `json:"enabled_at"`
	VerifiedAt       int64                       `json:"verified_at"`
	ExpiresAt        int64                       `json:"expires_at"`
	MerchantNo       string                      `json:"merchant_no"`
	AppID            string                      `json:"app_id"`
	CNYPerUnit       string                      `json:"cny_per_unit"`
	QuotaPerUnit     int64                       `json:"quota_per_unit"`
	FeeBPS           map[string]int              `json:"fee_bps"`
	Evidence         string                      `json:"evidence"`
	FeeEvidence      ZTAPICNYReferralFeeEvidence `json:"fee_evidence"`
	MarketObservedAt int64                       `json:"market_observed_at"`
	ObservedMarketFX string                      `json:"observed_market_cny_per_usdt"`
}

// FeeBPS bounds all merchant and payout costs at the supported order sizes.
// Sample payment receipts alone do not establish that bound.
type ZTAPICNYReferralFeeEvidence struct {
	MerchantNo          string `json:"merchant_no"`
	AppID               string `json:"app_id"`
	EffectiveAt         int64  `json:"effective_at"`
	ExpiresAt           int64  `json:"expires_at"`
	ScheduleReference   string `json:"schedule_reference"`
	SettlementReference string `json:"settlement_reference"`
}

func EvaluateZTAPICNYReferralMargin(gate ZTAPIReferralPriceGateInput, policy ZTAPICNYReferralPolicy, fx decimal.Decimal) (decimal.Decimal, error) {
	report, err := EvaluateZTAPIReferralPriceGate(gate)
	rate, rateErr := decimal.NewFromString(policy.CNYPerUnit)
	alipay, hasAlipay := policy.FeeBPS["alipay"]
	wxpay, hasWechat := policy.FeeBPS["wxpay"]
	if err != nil || !report.Allowed || rateErr != nil || !rate.IsPositive() || !fx.IsPositive() || !hasAlipay || !hasWechat || len(policy.FeeBPS) != 2 || alipay <= 0 || wxpay <= 0 || alipay >= 8500 || wxpay >= 8500 {
		return decimal.Zero, ErrZTAPIReferralNotReady
	}
	fee := decimal.NewFromInt(int64(max(alipay, wxpay))).Shift(-4)
	retained := decimal.NewFromInt(1).Sub(fee)
	fivePercent := decimal.RequireFromString("0.05")
	minimum := decimal.NewFromInt(1)
	for _, item := range gate.Items {
		revenue := decimal.RequireFromString(item.CustomerRevenue).Mul(rate)
		supplier, err := ztapiCNYReferralSupplierCost(item)
		if err != nil {
			return decimal.Zero, ErrZTAPIReferralNotReady
		}
		commission := fivePercent.Mul(decimal.RequireFromString(item.CustomerRevenue)).Mul(decimal.Max(rate, fx))
		cost := supplier.Add(decimal.RequireFromString(item.PlatformFee)).Mul(fx).Add(commission)
		if cost.GreaterThan(revenue.Mul(retained.Sub(decimal.RequireFromString("0.10")))) {
			return retained.Sub(cost.Div(revenue)), ErrZTAPIReferralNotReady
		}
		minimum = decimal.Min(minimum, retained.Sub(cost.Div(revenue)))
	}
	return minimum, nil
}

func ztapiCNYReferralSupplierCost(item ZTAPIReferralPriceGateItem) (decimal.Decimal, error) {
	if item.SupplierCostUpperBound == "" {
		quote, err := types.ParseZTAPIImageSaleReferralEvidence(item.Evidence)
		if err != nil {
			return decimal.Zero, err
		}
		return decimal.NewFromString(quote.Item.QuotedSupplierCostUSD)
	}
	parse := decimal.RequireFromString
	return parse(item.SupplierCostUpperBound).Mul(parse(item.UsageUpperBound)).Mul(parse(item.GroupMultiplier)).Mul(parse(item.CashMultiplier)), nil
}

func ztapiCNYReferralPrincipal(order CNYTopUpOrder, policy ZTAPICNYReferralPolicy) (int64, error) {
	rate, err := decimal.NewFromString(policy.CNYPerUnit)
	actualRate, orderErr := decimal.NewFromString(order.CNYPerUnit)
	if err != nil || orderErr != nil || !rate.Equal(actualRate) || policy.QuotaPerUnit <= 0 {
		return 0, ErrZTAPIReferralInvalid
	}
	quotaPerUnit := decimal.NewFromInt(policy.QuotaPerUnit)
	quota, err := CNYTopUpQuota(order.PayFen, rate, quotaPerUnit)
	if err != nil || quota != order.Quota {
		return 0, ErrZTAPIReferralInvalid
	}
	principal, _ := decimal.NewFromInt(quota).Shift(6).QuoRem(quotaPerUnit, 0)
	if !principal.BigInt().IsInt64() || principal.IntPart() < 20 {
		return 0, ErrZTAPIReferralInvalid
	}
	return principal.IntPart(), nil
}

// A separate reviewed file keeps the CNY gate independent of USDT admission.
func ProductionZTAPICNYReferralPolicy(ctx context.Context, at time.Time) (ZTAPICNYReferralPolicy, error) {
	var policy ZTAPICNYReferralPolicy
	path := os.Getenv("ZTAPI_CNY_REFERRAL_POLICY_FILE")
	if !filepath.IsAbs(path) || len(path) > 2048 {
		return policy, ErrZTAPIReferralNotReady
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return policy, ErrZTAPIReferralNotReady
	}
	f, err := os.Open(path)
	if err != nil {
		return policy, ErrZTAPIReferralNotReady
	}
	defer f.Close()
	opened, err := f.Stat()
	now := at.Unix()
	if err != nil || !os.SameFile(info, opened) || common.DecodeJson(f, &policy) != nil || policy.Version != 2 || policy.EnabledAt < policy.VerifiedAt || policy.VerifiedAt <= 0 || policy.EnabledAt > now || policy.ExpiresAt <= now || policy.ExpiresAt-policy.VerifiedAt > 31*86400 || !ValidCNYPaymentID(policy.MerchantNo) || !ValidCNYPaymentID(policy.AppID) || strings.TrimSpace(policy.Evidence) == "" || !decimal.NewFromInt(policy.QuotaPerUnit).Equal(decimal.NewFromFloat(common.QuotaPerUnit)) {
		return ZTAPICNYReferralPolicy{}, ErrZTAPIReferralNotReady
	}
	if policy.MerchantNo != os.Getenv("ZTAPI_CNY_TOPUP_MERCHANT") || policy.AppID != os.Getenv("ZTAPI_CNY_TOPUP_APP") || policy.CNYPerUnit != os.Getenv("ZTAPI_CNY_TOPUP_CNY_PER_U") || os.Getenv("ZTAPI_CNY_TOPUP_PUBLIC") != "true" || os.Getenv("ZTAPI_CNY_TOPUP_ENABLED") != "true" {
		return ZTAPICNYReferralPolicy{}, ErrZTAPIReferralNotReady
	}
	methods := strings.Split(os.Getenv("ZTAPI_CNY_TOPUP_METHODS"), ",")
	if len(methods) != 2 || methods[0] == methods[1] || (methods[0] != "alipay" && methods[0] != "wxpay") || (methods[1] != "alipay" && methods[1] != "wxpay") {
		return ZTAPICNYReferralPolicy{}, ErrZTAPIReferralNotReady
	}
	input, blockers, err := ProductionZTAPIReferralReadiness(ctx, DB, at)
	if err != nil || len(blockers) > 0 || !input.FinanceReady || !input.WorkerReady || !input.PlatformFeeKnown {
		return ZTAPICNYReferralPolicy{}, ErrZTAPIReferralNotReady
	}
	fx, err := CurrentZTAPIFXPolicy(DB.WithContext(ctx))
	if err != nil {
		return ZTAPICNYReferralPolicy{}, ErrZTAPIReferralNotReady
	}
	rate, err := cnyReferralReviewedFX(policy, fx, now)
	if err != nil {
		return ZTAPICNYReferralPolicy{}, ErrZTAPIReferralNotReady
	}
	if _, err = EvaluateZTAPICNYReferralMargin(input.PriceGate, policy, rate); err != nil {
		return ZTAPICNYReferralPolicy{}, err
	}
	return policy, nil
}

func cnyReferralReviewedFX(policy ZTAPICNYReferralPolicy, fx *ZTAPIFXPolicy, now int64) (decimal.Decimal, error) {
	proof := policy.FeeEvidence
	if proof.MerchantNo != policy.MerchantNo || proof.AppID != policy.AppID || proof.EffectiveAt <= 0 || proof.EffectiveAt > policy.VerifiedAt ||
		proof.ExpiresAt < policy.ExpiresAt || proof.ExpiresAt <= now || strings.TrimSpace(proof.ScheduleReference) == "" || strings.TrimSpace(proof.SettlementReference) == "" {
		return decimal.Zero, ErrZTAPIReferralNotReady
	}
	// A PBOC refresh can copy the old USDT rate into a newly written FX row.
	// Require the timestamp of an independent USDT market observation as well.
	if fx == nil || fx.CreatedAt < now-86400 || fx.CreatedAt > now || policy.MarketObservedAt < now-86400 || policy.MarketObservedAt > now {
		return decimal.Zero, ErrZTAPIReferralNotReady
	}
	market, errMarket := decimal.NewFromString(fx.MarketCNYPerUSDT)
	upstream, errUpstream := decimal.NewFromString(fx.UpstreamCNYPerUSD)
	observed, errObserved := decimal.NewFromString(policy.ObservedMarketFX)
	if errMarket != nil || errUpstream != nil || errObserved != nil || !ztapiFXRateInRange(market) || !ztapiFXRateInRange(upstream) || !ztapiFXRateInRange(observed) {
		return decimal.Zero, ErrZTAPIReferralNotReady
	}
	return decimal.Max(market, upstream, observed), nil
}
