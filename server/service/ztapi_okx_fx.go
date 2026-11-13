package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// OKX C2C "buy" ads are merchants buying USDT, so their price is what the
// platform receives in CNY when it sells USDT through Alipay.
var ztapiOKXAlipayBidURL = "https://www.okx.com/v3/c2c/tradingOrders/books?quoteCurrency=CNY&baseCurrency=USDT&side=buy&paymentMethod=aliPay&userType=all&receivingAds=false&showTrade=false&showFollow=false&showAlreadyTraded=false&isAbleFilter=false"

var ztapiOKXHTTPClient = &http.Client{Timeout: 10 * time.Second}

var (
	ztapiOKXMinAvailableUSDT = decimal.NewFromInt(1000)
	ztapiOKXRateMin          = decimal.NewFromInt(5)
	ztapiOKXRateMax          = decimal.NewFromInt(9)
)

const ztapiOKXSampleSize = 5

type ZTAPIOKXQuote struct {
	Price         string `json:"price"`
	AvailableUSDT string `json:"available_usdt"`
	MinOrderCNY   string `json:"min_order_cny"`
	MaxOrderCNY   string `json:"max_order_cny"`
	Merchant      string `json:"merchant"`
}

type ZTAPIOKXAlipayBid struct {
	MedianCNYPerUSDT string          `json:"median_cny_per_usdt"`
	Quotes           []ZTAPIOKXQuote `json:"quotes"`
	EligibleAds      int             `json:"eligible_ads"`
	MinAvailableUSDT string          `json:"min_available_usdt"`
	FetchedAt        int64           `json:"fetched_at"`
}

type ztapiOKXBookResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Buy []struct {
			Price                  string `json:"price"`
			AvailableAmount        string `json:"availableAmount"`
			QuoteMinAmountPerOrder string `json:"quoteMinAmountPerOrder"`
			QuoteMaxAmountPerOrder string `json:"quoteMaxAmountPerOrder"`
			NickName               string `json:"nickName"`
		} `json:"buy"`
	} `json:"data"`
}

func maskZTAPIOKXMerchant(name string) string {
	if name == "" {
		return ""
	}
	first, _ := utf8.DecodeRuneInString(name)
	return string(first) + "***"
}

// FetchZTAPIOKXAlipayUSDTBid reads the live Alipay merchant bids and returns
// the median of the five best bids that can each absorb at least 1,000 USDT.
func FetchZTAPIOKXAlipayUSDTBid(ctx context.Context) (*ZTAPIOKXAlipayBid, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ztapiOKXAlipayBidURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Accept", "application/json")
	response, err := ztapiOKXHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OKX returned HTTP %d", response.StatusCode)
	}
	var book ztapiOKXBookResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 4<<20)).Decode(&book); err != nil {
		return nil, fmt.Errorf("OKX response is not readable: %w", err)
	}
	if book.Code != 0 {
		return nil, fmt.Errorf("OKX rejected the request: %s", book.Msg)
	}
	type bid struct {
		price decimal.Decimal
		quote ZTAPIOKXQuote
	}
	eligible := make([]bid, 0, len(book.Data.Buy))
	for _, ad := range book.Data.Buy {
		price, priceErr := decimal.NewFromString(ad.Price)
		available, availableErr := decimal.NewFromString(ad.AvailableAmount)
		if priceErr != nil || availableErr != nil || available.LessThan(ztapiOKXMinAvailableUSDT) ||
			price.LessThan(ztapiOKXRateMin) || price.GreaterThan(ztapiOKXRateMax) {
			continue
		}
		eligible = append(eligible, bid{price: price, quote: ZTAPIOKXQuote{
			Price: price.String(), AvailableUSDT: available.StringFixed(2),
			MinOrderCNY: ad.QuoteMinAmountPerOrder, MaxOrderCNY: ad.QuoteMaxAmountPerOrder,
			Merchant: maskZTAPIOKXMerchant(ad.NickName),
		}})
	}
	if len(eligible) < 3 {
		return nil, errors.New("OKX has fewer than three Alipay merchants able to take 1,000 USDT")
	}
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].price.GreaterThan(eligible[j].price) })
	sample := eligible
	if len(sample) > ztapiOKXSampleSize {
		sample = sample[:ztapiOKXSampleSize]
	}
	prices := make([]decimal.Decimal, len(sample))
	quotes := make([]ZTAPIOKXQuote, len(sample))
	for i, item := range sample {
		prices[i] = item.price
		quotes[i] = item.quote
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i].LessThan(prices[j]) })
	median := prices[len(prices)/2]
	if len(prices)%2 == 0 {
		median = prices[len(prices)/2-1].Add(prices[len(prices)/2]).Div(decimal.NewFromInt(2))
	}
	return &ZTAPIOKXAlipayBid{
		MedianCNYPerUSDT: median.StringFixed(4), Quotes: quotes, EligibleAds: len(eligible),
		MinAvailableUSDT: ztapiOKXMinAvailableUSDT.String(), FetchedAt: time.Now().Unix(),
	}, nil
}
