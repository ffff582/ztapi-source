package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ZTAPIFXModePlatformV1 marks a price source whose costs are stored in CNY and
// converted to the platform billing unit (USDT) at the platform rate frozen on
// the source itself, without the legacy 3% buffer.
const ZTAPIFXModePlatformV1 = "platform_v1"

const (
	ZTAPIFXMarketSourceOKXAlipay = "okx_alipay"
	ZTAPIFXMarketSourceManual    = "manual"
)

// The upstream bills USD quotes in CNY at the People's Bank of China daily
// central parity rate, so the platform tracks the same published rate.
const (
	ZTAPIFXUpstreamSourcePBOCMid = "pboc_mid"
	ZTAPIFXUpstreamSourceManual  = "manual"
)

var ErrZTAPIUpstreamUSDRateUnset = errors.New("upstream USD settlement rate is not set")

var (
	ztapiFXRateMin     = decimal.RequireFromString("5")
	ztapiFXRateMax     = decimal.RequireFromString("9")
	ztapiFXStopLossMax = decimal.RequireFromString("0.5")
)

// ZTAPIFXPolicy is append-only; the newest row is the policy in force.
type ZTAPIFXPolicy struct {
	ID                 int64  `json:"id" gorm:"primaryKey"`
	MarketCNYPerUSDT   string `json:"market_cny_per_usdt" gorm:"type:decimal(24,10);not null"`
	StopLossCNY        string `json:"stop_loss_cny" gorm:"type:decimal(24,10);not null"`
	PlatformCNYPerUSDT string `json:"platform_cny_per_usdt" gorm:"type:decimal(24,10);not null"`
	UpstreamCNYPerUSD  string `json:"upstream_cny_per_usd" gorm:"type:decimal(24,10);not null;default:0"`
	UpstreamSource     string `json:"upstream_source" gorm:"size:16;not null;default:''"`
	UpstreamRateDate   string `json:"upstream_rate_date" gorm:"size:16;not null;default:''"`
	MarketSource       string `json:"market_source" gorm:"size:16;not null"`
	Reason             string `json:"reason" gorm:"size:255;not null"`
	OperatorID         int    `json:"operator_id" gorm:"not null"`
	CreatedAt          int64  `json:"created_at" gorm:"bigint;not null;index"`
}

func (ZTAPIFXPolicy) TableName() string { return "ztapi_fx_policies" }

// migrateZTAPIFXPricingColumns adds the FX snapshot columns to price sources
// created before platform pricing, so a release rolls forward on its own.
func migrateZTAPIFXPricingColumns(db *gorm.DB) error {
	return migrateZTAPIFXPricingColumnsWithMigrator(db.Migrator())
}

func migrateZTAPIFXPricingColumnsWithMigrator(migrator ztapiMediaPricingColumnMigrator) error {
	if !migrator.HasTable(&ZTAPIModelPriceSource{}) {
		return nil
	}
	for _, field := range []string{"FXMode", "PlatformCNYPerUnit", "UpstreamCNYPerUSD"} {
		if migrator.HasColumn(&ZTAPIModelPriceSource{}, field) {
			continue
		}
		if err := migrator.AddColumn(&ZTAPIModelPriceSource{}, field); err != nil {
			// A parallel instance may have added the column first.
			if migrator.HasColumn(&ZTAPIModelPriceSource{}, field) {
				continue
			}
			return err
		}
	}
	return nil
}

type ZTAPIFXPolicyInput struct {
	MarketCNYPerUSDT  string
	StopLossCNY       string
	UpstreamCNYPerUSD string
	UpstreamSource    string
	UpstreamRateDate  string
	MarketSource      string
	Reason            string
	OperatorID        int
}

// ZTAPIABFXParams is the FX evidence a price source is built with. The zero
// value is the legacy mode: CNY quotes use the frozen 7.2 rate and 3% buffer,
// USD quotes are billed as-is.
type ZTAPIABFXParams struct {
	Mode            string
	PlatformRate    decimal.Decimal
	UpstreamUSDRate decimal.Decimal
}

func (p ZTAPIABFXParams) Equal(other ZTAPIABFXParams) bool {
	return p.Mode == other.Mode && p.PlatformRate.Equal(other.PlatformRate) &&
		p.UpstreamUSDRate.Equal(other.UpstreamUSDRate)
}

// ztapiDecimalOrZero keeps optional decimal columns insertable and comparable.
func ztapiDecimalOrZero(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "0"
	}
	return trimmed
}

func ztapiFXRateInRange(rate decimal.Decimal) bool {
	return rate.GreaterThanOrEqual(ztapiFXRateMin) && rate.LessThanOrEqual(ztapiFXRateMax)
}

func buildZTAPIFXPolicy(input ZTAPIFXPolicyInput) (ZTAPIFXPolicy, error) {
	market, err := decimal.NewFromString(strings.TrimSpace(input.MarketCNYPerUSDT))
	if err != nil || !ztapiFXRateInRange(market) {
		return ZTAPIFXPolicy{}, errors.New("USDT market rate must be between 5 and 9 CNY")
	}
	stopLoss, err := decimal.NewFromString(strings.TrimSpace(input.StopLossCNY))
	if err != nil || stopLoss.IsNegative() || stopLoss.GreaterThan(ztapiFXStopLossMax) {
		return ZTAPIFXPolicy{}, errors.New("stop-loss must be between 0 and 0.5 CNY")
	}
	platform := market.Sub(stopLoss)
	if !ztapiFXRateInRange(platform) {
		return ZTAPIFXPolicy{}, errors.New("platform rate must stay between 5 and 9 CNY")
	}
	upstreamRaw := strings.TrimSpace(input.UpstreamCNYPerUSD)
	if upstreamRaw == "" {
		upstreamRaw = "0"
	}
	upstream, err := decimal.NewFromString(upstreamRaw)
	if err != nil || (!upstream.IsZero() && !ztapiFXRateInRange(upstream)) {
		return ZTAPIFXPolicy{}, errors.New("upstream USD rate must be empty or between 5 and 9 CNY")
	}
	if input.MarketSource != ZTAPIFXMarketSourceOKXAlipay && input.MarketSource != ZTAPIFXMarketSourceManual {
		return ZTAPIFXPolicy{}, errors.New("unsupported USDT market rate source")
	}
	upstreamSource := strings.TrimSpace(input.UpstreamSource)
	if upstreamSource == "" {
		upstreamSource = ZTAPIFXUpstreamSourcePBOCMid
	}
	if upstreamSource != ZTAPIFXUpstreamSourcePBOCMid && upstreamSource != ZTAPIFXUpstreamSourceManual {
		return ZTAPIFXPolicy{}, errors.New("unsupported upstream USD rate source")
	}
	upstreamDate := strings.TrimSpace(input.UpstreamRateDate)
	if len(upstreamDate) > 16 {
		return ZTAPIFXPolicy{}, errors.New("upstream rate date is invalid")
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || len(reason) > 255 {
		return ZTAPIFXPolicy{}, errors.New("a reason of at most 255 bytes is required")
	}
	if input.OperatorID < 0 {
		return ZTAPIFXPolicy{}, errors.New("operator is invalid")
	}
	return ZTAPIFXPolicy{
		MarketCNYPerUSDT:   market.StringFixed(10),
		StopLossCNY:        stopLoss.StringFixed(10),
		PlatformCNYPerUSDT: platform.StringFixed(10),
		UpstreamCNYPerUSD:  upstream.StringFixed(10),
		UpstreamSource:     upstreamSource,
		UpstreamRateDate:   upstreamDate,
		MarketSource:       input.MarketSource,
		Reason:             reason,
		OperatorID:         input.OperatorID,
	}, nil
}

func CreateZTAPIFXPolicy(input ZTAPIFXPolicyInput) (ZTAPIFXPolicy, error) {
	if DB == nil {
		return ZTAPIFXPolicy{}, errors.New("ZTAPI database is not initialized")
	}
	if input.OperatorID <= 0 {
		return ZTAPIFXPolicy{}, errors.New("FX policy changes require an operator")
	}
	policy, err := buildZTAPIFXPolicy(input)
	if err != nil {
		return ZTAPIFXPolicy{}, err
	}
	policy.CreatedAt = common.GetTimestamp()
	if err := DB.Create(&policy).Error; err != nil {
		return ZTAPIFXPolicy{}, err
	}
	return policy, nil
}

func CurrentZTAPIFXPolicy(db *gorm.DB) (*ZTAPIFXPolicy, error) {
	if db == nil || !db.Migrator().HasTable(&ZTAPIFXPolicy{}) {
		return nil, nil
	}
	var policies []ZTAPIFXPolicy
	if err := db.Order("id DESC").Limit(1).Find(&policies).Error; err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, nil
	}
	return &policies[0], nil
}

func ListZTAPIFXPolicies(limit int) ([]ZTAPIFXPolicy, error) {
	if DB == nil {
		return nil, errors.New("ZTAPI database is not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var policies []ZTAPIFXPolicy
	err := DB.Order("id DESC").Limit(limit).Find(&policies).Error
	return policies, err
}

func (policy *ZTAPIFXPolicy) ABFXParams() (ZTAPIABFXParams, error) {
	platform, err := decimal.NewFromString(strings.TrimSpace(policy.PlatformCNYPerUSDT))
	if err != nil || !ztapiFXRateInRange(platform) {
		return ZTAPIABFXParams{}, errors.New("FX policy platform rate is invalid")
	}
	upstream, err := decimal.NewFromString(strings.TrimSpace(policy.UpstreamCNYPerUSD))
	if err != nil || upstream.IsNegative() {
		return ZTAPIABFXParams{}, errors.New("FX policy upstream USD rate is invalid")
	}
	return ZTAPIABFXParams{Mode: ZTAPIFXModePlatformV1, PlatformRate: platform, UpstreamUSDRate: upstream}, nil
}

// currentZTAPIABFXParams returns the FX in force. Without any policy the
// catalog keeps the legacy conversion, so older databases price as before.
func currentZTAPIABFXParams(db *gorm.DB) (ZTAPIABFXParams, error) {
	policy, err := CurrentZTAPIFXPolicy(db)
	if err != nil || policy == nil {
		return ZTAPIABFXParams{}, err
	}
	return policy.ABFXParams()
}

func ztapiABFXParamsFromSource(source *ZTAPIModelPriceSource) (ZTAPIABFXParams, error) {
	if source.FXMode == "" {
		return ZTAPIABFXParams{}, nil
	}
	if source.FXMode != ZTAPIFXModePlatformV1 {
		return ZTAPIABFXParams{}, fmt.Errorf("unsupported FX mode %q", source.FXMode)
	}
	platform, err := decimal.NewFromString(strings.TrimSpace(source.PlatformCNYPerUnit))
	if err != nil || !platform.IsPositive() {
		return ZTAPIABFXParams{}, errors.New("price source platform rate is invalid")
	}
	upstreamRaw := strings.TrimSpace(source.UpstreamCNYPerUSD)
	if upstreamRaw == "" {
		upstreamRaw = "0"
	}
	upstream, err := decimal.NewFromString(upstreamRaw)
	if err != nil || upstream.IsNegative() {
		return ZTAPIABFXParams{}, errors.New("price source upstream USD rate is invalid")
	}
	return ZTAPIABFXParams{Mode: ZTAPIFXModePlatformV1, PlatformRate: platform, UpstreamUSDRate: upstream}, nil
}

// ensureZTAPIFXPolicySeeded writes the policy agreed on 2026-09-16: the OKX
// C2C Alipay merchants' USDT bid (6.66) minus a 0.03 CNY stop-loss. The
// upstream USD settlement rate stays unset until the supplier confirms it.
func ensureZTAPIFXPolicySeeded(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&ZTAPIFXPolicy{}) {
		return nil
	}
	var count int64
	if err := db.Model(&ZTAPIFXPolicy{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	policy, err := buildZTAPIFXPolicy(ZTAPIFXPolicyInput{
		MarketCNYPerUSDT: "6.66", StopLossCNY: "0.03", UpstreamCNYPerUSD: "0",
		MarketSource: ZTAPIFXMarketSourceOKXAlipay, UpstreamSource: ZTAPIFXUpstreamSourcePBOCMid,
		Reason: "initial platform rate: OKX Alipay USDT bid 6.66 minus 0.03 stop-loss",
	})
	if err != nil {
		return err
	}
	policy.CreatedAt = common.GetTimestamp()
	return db.Create(&policy).Error
}
