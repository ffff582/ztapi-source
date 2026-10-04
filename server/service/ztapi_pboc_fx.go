package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/shopspring/decimal"
)

// The upstream settles USD-quoted models in CNY at the People's Bank of China
// daily central parity rate, published every business morning at 9:15.
var ztapiPBOCCentralParityURL = "https://www.chinamoney.com.cn/r/cms/www/chinamoney/data/fx/ccpr.json"

var ztapiPBOCHTTPClient = &http.Client{Timeout: 10 * time.Second}

var (
	ztapiPBOCRateMin      = decimal.NewFromInt(5)
	ztapiPBOCRateMax      = decimal.NewFromInt(9)
	ztapiFXRefreshOnce    sync.Once
	ztapiFXRefreshRunning atomic.Bool
)

var ztapiFXBeijing = time.FixedZone("Asia/Shanghai", 8*60*60)

type ZTAPIFXRefreshResult struct {
	Updated       bool
	SkippedManual bool
	Rate          string
	RateDate      string
}

type ZTAPIPBOCMidRate struct {
	CNYPerUSD string `json:"cny_per_usd"`
	RateDate  string `json:"rate_date"`
	Source    string `json:"source"`
	FetchedAt int64  `json:"fetched_at"`
}

type ztapiPBOCResponse struct {
	Data struct {
		LastDate string `json:"lastDate"`
	} `json:"data"`
	Records []struct {
		VrtEName string `json:"vrtEName"`
		Price    string `json:"price"`
	} `json:"records"`
}

// FetchZTAPIPBOCUSDCNYMidRate reads today's published USD/CNY central parity.
func FetchZTAPIPBOCUSDCNYMidRate(ctx context.Context) (*ZTAPIPBOCMidRate, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ztapiPBOCCentralParityURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Accept", "application/json")
	response, err := ztapiPBOCHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("central parity source returned HTTP %d", response.StatusCode)
	}
	var parity ztapiPBOCResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 2<<20)).Decode(&parity); err != nil {
		return nil, fmt.Errorf("central parity response is not readable: %w", err)
	}
	for _, record := range parity.Records {
		if !strings.EqualFold(strings.TrimSpace(record.VrtEName), "USD/CNY") {
			continue
		}
		rate, rateErr := decimal.NewFromString(strings.TrimSpace(record.Price))
		if rateErr != nil || rate.LessThan(ztapiPBOCRateMin) || rate.GreaterThan(ztapiPBOCRateMax) {
			return nil, errors.New("published USD/CNY central parity is out of the supported range")
		}
		date := strings.TrimSpace(parity.Data.LastDate)
		if index := strings.IndexByte(date, ' '); index > 0 {
			date = date[:index]
		}
		return &ZTAPIPBOCMidRate{
			CNYPerUSD: rate.StringFixed(4), RateDate: date,
			Source: model.ZTAPIFXUpstreamSourcePBOCMid, FetchedAt: time.Now().Unix(),
		}, nil
	}
	return nil, errors.New("the central parity table has no USD/CNY row")
}

// EnsureZTAPIUpstreamFXRate fills in the upstream settlement rate on first
// boot, so a release does not wait for an operator to copy the published rate
// by hand. A policy that already carries a rate, or one an operator set
// manually, is left alone.
func EnsureZTAPIUpstreamFXRate() {
	if model.DB == nil {
		return
	}
	policy, err := model.CurrentZTAPIFXPolicy(model.DB)
	if err != nil || policy == nil {
		return
	}
	if policy.UpstreamSource == model.ZTAPIFXUpstreamSourceManual {
		return
	}
	if rate, rateErr := decimal.NewFromString(strings.TrimSpace(policy.UpstreamCNYPerUSD)); rateErr == nil && rate.IsPositive() {
		return
	}
	operatorID, err := model.ZTAPIRootOperatorID()
	if err != nil || operatorID <= 0 {
		common.SysLog("ZTAPI upstream FX rate postponed: no root operator yet")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mid, err := FetchZTAPIPBOCUSDCNYMidRate(ctx)
	if err != nil {
		common.SysError("ZTAPI upstream FX rate was not fetched: " + err.Error())
		return
	}
	if _, err := model.CreateZTAPIFXPolicy(model.ZTAPIFXPolicyInput{
		MarketCNYPerUSDT: policy.MarketCNYPerUSDT, StopLossCNY: policy.StopLossCNY,
		UpstreamCNYPerUSD: mid.CNYPerUSD, UpstreamSource: model.ZTAPIFXUpstreamSourcePBOCMid,
		UpstreamRateDate: mid.RateDate, MarketSource: policy.MarketSource,
		Reason: "upstream settles at the PBOC central parity published on " + mid.RateDate, OperatorID: operatorID,
	}); err != nil {
		common.SysError("ZTAPI upstream FX rate was not stored: " + err.Error())
		return
	}
	common.SysLog("ZTAPI upstream FX rate set to the PBOC central parity " + mid.CNYPerUSD + " of " + mid.RateDate)
}

func runZTAPIFXRefreshOnce(
	ctx context.Context,
	reader func(context.Context) (*ZTAPIPBOCMidRate, error),
	reprice func(int) error,
) (ZTAPIFXRefreshResult, error) {
	result := ZTAPIFXRefreshResult{}
	if model.DB == nil {
		return result, errors.New("ZTAPI database is not initialized")
	}
	policy, err := model.CurrentZTAPIFXPolicy(model.DB)
	if err != nil {
		return result, err
	}
	if policy == nil {
		return result, errors.New("ZTAPI FX policy is not configured")
	}
	if policy.UpstreamSource == model.ZTAPIFXUpstreamSourceManual {
		result.SkippedManual = true
		return result, nil
	}
	mid, err := reader(ctx)
	if err != nil {
		return result, err
	}
	if mid == nil {
		return result, errors.New("PBOC rate response is empty")
	}
	result.Rate, result.RateDate = strings.TrimSpace(mid.CNYPerUSD), strings.TrimSpace(mid.RateDate)
	if policy.UpstreamRateDate != "" && result.RateDate < policy.UpstreamRateDate {
		return result, fmt.Errorf("published rate date %s is older than current %s", result.RateDate, policy.UpstreamRateDate)
	}
	currentRate, currentErr := decimal.NewFromString(strings.TrimSpace(policy.UpstreamCNYPerUSD))
	nextRate, nextErr := decimal.NewFromString(result.Rate)
	if nextErr != nil || !ztapiFXRateInSupportedRange(nextRate) {
		return result, errors.New("published USD/CNY central parity is out of the supported range")
	}
	changed := currentErr != nil || !currentRate.Equal(nextRate) || policy.UpstreamRateDate != result.RateDate
	operatorID, err := model.ZTAPIRootOperatorID()
	if err != nil || operatorID <= 0 {
		return result, errors.New("root operator is unavailable")
	}
	if changed {
		if _, err := model.CreateZTAPIFXPolicy(model.ZTAPIFXPolicyInput{
			MarketCNYPerUSDT: policy.MarketCNYPerUSDT, StopLossCNY: policy.StopLossCNY,
			UpstreamCNYPerUSD: result.Rate, UpstreamSource: model.ZTAPIFXUpstreamSourcePBOCMid,
			UpstreamRateDate: result.RateDate, MarketSource: policy.MarketSource,
			Reason:     "scheduled PBOC central parity refresh for " + result.RateDate,
			OperatorID: operatorID,
		}); err != nil {
			return result, err
		}
		result.Updated = true
	}
	// Repricing is idempotent. Running it for an unchanged rate also repairs a
	// previous attempt where the policy was stored but catalog publication failed.
	if err := reprice(operatorID); err != nil {
		return result, err
	}
	return result, nil
}

func ztapiFXRateInSupportedRange(rate decimal.Decimal) bool {
	return !rate.LessThan(ztapiPBOCRateMin) && !rate.GreaterThan(ztapiPBOCRateMax)
}

func nextZTAPIFXRefresh(now time.Time, lastErr error) time.Time {
	if lastErr != nil {
		return now.Add(time.Hour)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, now.Location())
	if !now.Before(next) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func runProductionZTAPIFXRefresh(ctx context.Context) error {
	if !ztapiFXRefreshRunning.CompareAndSwap(false, true) {
		return nil
	}
	defer ztapiFXRefreshRunning.Store(false)
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := runZTAPIFXRefreshOnce(
		refreshCtx,
		FetchZTAPIPBOCUSDCNYMidRate,
		func(operatorID int) error {
			repriced, repriceErr := model.ApplyZTAPIFXRepricing(operatorID)
			if repriceErr == nil {
				common.SysLog(fmt.Sprintf(
					"ZTAPI scheduled FX repricing: republished=%d unchanged=%d skipped=%d",
					repriced.Republished, repriced.Unchanged, len(repriced.Skipped),
				))
			}
			return repriceErr
		},
	)
	if err != nil {
		logger.LogWarn(ctx, "ZTAPI scheduled PBOC FX refresh failed: "+err.Error())
		return err
	}
	if result.SkippedManual {
		common.SysLog("ZTAPI scheduled PBOC FX refresh skipped: manual upstream rate is in force")
	} else if result.Updated {
		common.SysLog("ZTAPI scheduled PBOC FX rate updated to " + result.Rate + " for " + result.RateDate)
	}
	return nil
}

// StartZTAPIFXRefreshTask refreshes the supplier settlement rate at 10:00
// Beijing time. A failed run retries after one hour; a successful run waits
// until the next day's publication window.
func StartZTAPIFXRefreshTask(ctx context.Context) {
	ztapiFXRefreshOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			lastErr := runProductionZTAPIFXRefresh(ctx)
			for {
				now := time.Now().In(ztapiFXBeijing)
				delay := time.Until(nextZTAPIFXRefresh(now, lastErr))
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
					lastErr = runProductionZTAPIFXRefresh(ctx)
				}
			}
		})
	})
}
