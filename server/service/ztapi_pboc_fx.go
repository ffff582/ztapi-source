package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

// The upstream settles USD-quoted models in CNY at the People's Bank of China
// daily central parity rate, published every business morning at 9:15.
var ztapiPBOCCentralParityURL = "https://www.chinamoney.com.cn/r/cms/www/chinamoney/data/fx/ccpr.json"

var ztapiPBOCHTTPClient = &http.Client{Timeout: 10 * time.Second}

var (
	ztapiPBOCRateMin = decimal.NewFromInt(5)
	ztapiPBOCRateMax = decimal.NewFromInt(9)
)

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
