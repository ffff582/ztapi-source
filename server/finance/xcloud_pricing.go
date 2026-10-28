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
const XcloudSupplierDiscountFactor = "0.4"

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

func xcloudQuote(sourceModel, input, output, cacheRead string) XcloudPublicQuote {
	discount := decimalValue(XcloudSupplierDiscountFactor)
	inputPrice := decimalValue(input).Mul(discount)
	return XcloudPublicQuote{
		SourceModel:            sourceModel,
		InputPerMillion:        inputPrice,
		OutputPerMillion:       decimalValue(output).Mul(discount),
		CacheReadPerMillion:    decimalValue(cacheRead).Mul(discount),
		CacheWritePerMillion:   inputPrice.Mul(decimalValue("1.25")),
		CacheWrite5mPerMillion: inputPrice.Mul(decimalValue("1.25")),
		CacheWrite1hPerMillion: inputPrice.Mul(decimalValue("2")),
		SourceURL:              XcloudPricingURL,
		RetrievedAt:            time.Date(2026, 10, 28, 0, 0, 0, 0, shanghai),
		DiscountFactor:         discount,
	}
}

// XcloudPublicQuotes is the public-price evidence for the Claude source
// models currently declared by the stored Xcloud channel. The public page
// does not list claude-opus-5, so that source model intentionally has no row.
// The stored quote applies the agreed supplier discount and remains an
// estimate until an Xcloud bill is recorded.
func XcloudPublicQuotes() []XcloudPublicQuote {
	return []XcloudPublicQuote{
		xcloudQuote("claude-haiku-4-5-20251001", "1", "5", "0.1"),
		xcloudQuote("claude-opus-4-6", "5", "25", "0.5"),
		xcloudQuote("claude-opus-4-7", "5", "25", "0.5"),
		xcloudQuote("claude-opus-4-8", "5", "25", "0.5"),
		xcloudQuote("claude-sonnet-4-6", "3", "15", "0.3"),
		xcloudQuote("claude-sonnet-5", "1.5", "10.5", "0.15"),
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
