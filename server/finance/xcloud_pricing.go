// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const XcloudPricingURL = "https://www.xcloudtoken.com/pricing"
const XcloudSupplierDiscountFactor = "0.5"

var ErrXcloudQuoteMissing = errors.New("xcloud public quote is missing")

// UsageDimension is the exclusive, immutable charge dimension persisted by
// the settlement layer. Cache dimensions are not reconstructed from a total
// token count, which prevents cache read/write double counting.
type UsageDimension struct {
	Dimension string
	Quantity  int64
}

type XcloudPublicQuote struct {
	SourceModel            string
	InputPerMillion        decimal.Decimal
	OutputPerMillion       decimal.Decimal
	CacheReadPerMillion    decimal.Decimal
	CacheWritePerMillion   decimal.Decimal
	CacheWrite5mPerMillion decimal.Decimal
	CacheWrite1hPerMillion decimal.Decimal
	SourceURL              string
	RetrievedAt            time.Time
	DiscountFactor         decimal.Decimal
}

type XcloudUsageEstimate struct {
	SourceModel    string `json:"source_model"`
	CostUSDT       string `json:"cost_usdt"`
	SourceURL      string `json:"source_url"`
	IsEstimate     bool   `json:"is_estimate"`
	DiscountFactor string `json:"discount_factor"`
}

func decimalValue(value string) decimal.Decimal {
	return decimal.RequireFromString(value)
}

func xcloudQuote(sourceModel, input, output, cacheRead, cacheWrite, cacheWrite5m, cacheWrite1h string) XcloudPublicQuote {
	discount := decimalValue(XcloudSupplierDiscountFactor)
	return XcloudPublicQuote{
		SourceModel:            sourceModel,
		InputPerMillion:        decimalValue(input).Mul(discount),
		OutputPerMillion:       decimalValue(output).Mul(discount),
		CacheReadPerMillion:    decimalValue(cacheRead).Mul(discount),
		CacheWritePerMillion:   decimalValue(cacheWrite).Mul(discount),
		CacheWrite5mPerMillion: decimalValue(cacheWrite5m).Mul(discount),
		CacheWrite1hPerMillion: decimalValue(cacheWrite1h).Mul(discount),
		SourceURL:              XcloudPricingURL,
		RetrievedAt:            time.Date(2026, 10, 29, 0, 0, 0, 0, shanghai),
		DiscountFactor:         discount,
	}
}

// XcloudPublicQuotes is the public-price evidence captured from the live
// Xcloud pricing page on 2026-10-29. Prices are official-page prices reduced
// by the agreed 50% supplier factor and remain estimates until an Xcloud bill
// is recorded. A zero cache price means that dimension is not quoted by the
// public page and must not be fabricated or double-counted.
func XcloudPublicQuotes() []XcloudPublicQuote {
	return []XcloudPublicQuote{
		xcloudQuote("claude-fable-5", "10.05", "50.25", "0.5", "8.3", "8.3", "13.5"),
		xcloudQuote("claude-fable-5-1", "10.5", "52.5", "0.5", "6.25", "6.25", "10"),
		xcloudQuote("claude-haiku-4-5-20251001", "1", "5", "0.1", "1.25", "1.25", "2"),
		xcloudQuote("claude-opus-4-6", "5", "25", "0.5", "6.25", "6.25", "10"),
		xcloudQuote("claude-opus-4-7", "5", "25", "0.5", "6.25", "6.25", "10"),
		xcloudQuote("claude-opus-4-8", "5", "25", "0.5", "6.25", "6.25", "10"),
		xcloudQuote("claude-opus-5-5", "7.6", "38", "0.2", "5", "5", "8"),
		xcloudQuote("claude-sonnet-4-6", "3", "15", "0.2", "2.25", "2.25", "6.7"),
		xcloudQuote("claude-sonnet-5", "1.5", "10.5", "0.15", "1.9", "1.9", "2.7"),
		xcloudQuote("claude-sonnet-5-5", "3.8", "3.8", "0.1", "2.5", "2.5", "4"),
		xcloudQuote("deepseek-v4.1-flash", "0.084", "0.3", "0.003", "0", "0", "0"),
		xcloudQuote("glm-5.2", "4.8", "16.8", "2", "0", "0", "2"),
		xcloudQuote("glm-5.3", "4.8", "16.8", "1.2", "0", "0", "0"),
		xcloudQuote("glm-5.3-flash", "0.48", "1.68", "0.138", "0", "0", "0"),
		xcloudQuote("gpt-5.5", "5", "30", "1", "12.5", "12.5", "0"),
		xcloudQuote("gpt-5.6-sol", "5", "30", "0", "0", "0", "0"),
		xcloudQuote("gpt-5.6-terra", "2.5", "15", "0", "0", "0", "0"),
		xcloudQuote("gpt-6-astra", "19", "95", "1", "12.5", "12.5", "0"),
		xcloudQuote("gpt-6-luna", "0.19", "0.95", "0.01", "0.125", "0.125", "0"),
		xcloudQuote("gpt-6-sol", "3.8", "19", "0.2", "2.5", "2.5", "0"),
		xcloudQuote("kimi-k3", "8.04", "30", "1", "0", "0", "1.5"),
		xcloudQuote("qwen3.8-flash", "0.09", "0.3", "0.2", "0.1", "0.1", "0.1"),
		xcloudQuote("qwen3.8-max", "1.02", "3.06", "0.22", "1.8", "1.8", "2.2"),
	}
}

func xcloudQuoteByModel(sourceModel string) (XcloudPublicQuote, bool) {
	for _, quote := range XcloudPublicQuotes() {
		if quote.SourceModel == strings.TrimSpace(sourceModel) {
			return quote, true
		}
	}
	return XcloudPublicQuote{}, false
}

func xcloudDimensionPrice(quote XcloudPublicQuote, dimension string) (decimal.Decimal, bool) {
	prices := map[string]decimal.Decimal{
		"input_tokens":   quote.InputPerMillion,
		"output_tokens":  quote.OutputPerMillion,
		"cache_read":     quote.CacheReadPerMillion,
		"cache_write":    quote.CacheWritePerMillion,
		"cache_write_5m": quote.CacheWrite5mPerMillion,
		"cache_write_1h": quote.CacheWrite1hPerMillion,
	}
	price, ok := prices[dimension]
	return price, ok && price.IsPositive()
}

func EstimateXcloudUsage(sourceModel string, dimensions []UsageDimension) (XcloudUsageEstimate, error) {
	zero := XcloudUsageEstimate{SourceModel: sourceModel, CostUSDT: "0"}
	quote, ok := xcloudQuoteByModel(sourceModel)
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrXcloudQuoteMissing, strings.TrimSpace(sourceModel))
	}
	seen := make(map[string]struct{}, len(dimensions))
	cost := decimal.Zero
	for _, dimension := range dimensions {
		name := strings.TrimSpace(dimension.Dimension)
		if name == "" || dimension.Quantity < 0 {
			return zero, fmt.Errorf("invalid Xcloud usage dimension %q", name)
		}
		if _, exists := seen[name]; exists {
			return zero, fmt.Errorf("duplicate Xcloud usage dimension %q", name)
		}
		seen[name] = struct{}{}
		price, supported := xcloudDimensionPrice(quote, name)
		if !supported {
			return zero, fmt.Errorf("%w: %s/%s", ErrXcloudQuoteMissing, sourceModel, name)
		}
		cost = cost.Add(decimal.NewFromInt(dimension.Quantity).Div(decimal.NewFromInt(1_000_000)).Mul(price))
	}
	zero.CostUSDT = cost.StringFixed(6)
	zero.SourceURL = quote.SourceURL
	zero.IsEstimate = true
	zero.DiscountFactor = quote.DiscountFactor.String()
	return zero, nil
}
