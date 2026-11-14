// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

const XcloudPricingURL = "https://www.xcloudtoken.com/pricing"
const XcloudSupplierDiscountFactor = "0.5"
const yunxinPoolURL = "https://ai.yunxinapi.com/hub"

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
	Group                  string
	Tier                   string
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

func xcloudQuote(sourceModel, group, tier, input, output, cacheRead, cacheWrite, cacheWrite5m, cacheWrite1h string) XcloudPublicQuote {
	discount := decimalValue(XcloudSupplierDiscountFactor)
	return XcloudPublicQuote{
		SourceModel:            sourceModel,
		Group:                  group,
		Tier:                   tier,
		InputPerMillion:        decimalValue(input).Mul(discount),
		OutputPerMillion:       decimalValue(output).Mul(discount),
		CacheReadPerMillion:    decimalValue(cacheRead).Mul(discount),
		CacheWritePerMillion:   decimalValue(cacheWrite).Mul(discount),
		CacheWrite5mPerMillion: decimalValue(cacheWrite5m).Mul(discount),
		CacheWrite1hPerMillion: decimalValue(cacheWrite1h).Mul(discount),
		SourceURL:              XcloudPricingURL,
		RetrievedAt:            time.Date(2026, 10, 30, 0, 0, 0, 0, shanghai),
		DiscountFactor:         discount,
	}
}

// Prices are the group-adjusted credit prices observed on 2026-10-30, converted
// once to cash at the owner's stated 100-for-200 recharge rate. They are not
// supplier bills and must not be applied to an unproven historical group/tier.
func XcloudPublicQuotes() []XcloudPublicQuote {
	return []XcloudPublicQuote{
		xcloudQuote("claude-fable-5", "claude-max", "base", "10.05", "50.25", "0.75", "0", "12.45", "20.25"),
		xcloudQuote("claude-fable-5-1", "claude-max", "base", "10.5", "52.5", "0.75", "0", "9.375", "15"),
		xcloudQuote("claude-haiku-4-5-20251001", "claude-max", "固定", "1.5", "7.5", "0.15", "1.875", "0", "0"),
		xcloudQuote("claude-opus-4-6", "claude-max", "固定", "7.5", "37.5", "0.75", "9.375", "0", "0"),
		xcloudQuote("claude-opus-4-7", "claude-max", "固定", "7.5", "37.5", "0.75", "9.375", "0", "0"),
		xcloudQuote("claude-opus-4-8", "claude-max", "固定", "7.5", "37.5", "0.75", "9.375", "0", "0"),
		xcloudQuote("claude-opus-5-5", "new-vip", "base", "7.6", "38", "0.38", "0", "9.5", "15.2"),
		xcloudQuote("claude-sonnet-4-6", "claude-max", "base", "3", "15", "0.3", "0", "3.375", "10.05"),
		xcloudQuote("claude-sonnet-5", "claude-max", "base", "1.5", "10.5", "0.225", "0", "2.85", "4.05"),
		xcloudQuote("claude-sonnet-5-5", "new-vip", "long_context", "3.8", "3.8", "0.19", "0", "4.75", "7.6"),
		xcloudQuote("deepseek-v4.1-flash", "国产分组", "base", "0.084", "0.3", "0.0018", "0", "0", "0"),
		xcloudQuote("glm-5.3", "国产分组", "固定", "4.8", "16.8", "1.2", "0", "0", "0"),
		xcloudQuote("gpt-5.5", "gpt-plus", "固定", "5", "30", "0", "0", "0", "0"),
		xcloudQuote("gpt-5.6-sol", "gpt-plus", "固定", "5", "30", "0", "6.25", "0", "0"),
		xcloudQuote("gpt-5.6-terra", "gpt-plus", "固定", "2.5", "15", "0", "3.125", "0", "0"),
		xcloudQuote("gpt-6-astra", "new-vip", "standard", "19", "95", "1.9", "23.75", "0", "0"),
		xcloudQuote("gpt-6-astra", "new-vip", "long_context", "38", "142.5", "3.8", "47.5", "0", "0"),
		xcloudQuote("gpt-6-luna", "new-vip", "base", "0.19", "0.95", "0.019", "0.2375", "0", "0"),
		xcloudQuote("gpt-6-sol", "new-vip", "base", "3.8", "19", "0.38", "4.75", "0", "0"),
		xcloudQuote("kimi-k3", "国产分组", "base", "8.04", "30", "0.6", "0", "0", "0.9"),
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
	return XcloudUsageEstimate{SourceModel: sourceModel, CostUSDT: "0"}, fmt.Errorf("%w: historical group and tier unavailable", ErrXcloudQuoteMissing)
}

// Resolve only policies that pin both the supplier fee and the allowed routes.
// Current channel labels and the latest quote cannot prove historical cost.
type frozenXcloudSource struct {
	SourceModel, PublicName, PricePolicy, TokenPriceRulesJSON string
	PublicationID                                             int
	PriceSourceID                                             int64
	PriceSourceVersion                                        uint64
	AllowedChannelIDs                                         []int
}

func readFrozenXcloudSource(s Settlement) (frozenXcloudSource, bool) {
	var snapshot frozenXcloudSource
	valid := len(s.PriceSnapshotJSON) <= 64<<10 && common.RejectDuplicateJsonObjectMembers(strings.NewReader(s.PriceSnapshotJSON)) == nil &&
		common.UnmarshalJsonStr(s.PriceSnapshotJSON, &snapshot) == nil
	return snapshot, valid
}

func estimateFrozenXcloudUsage(s Settlement, channelID int, baseURL string, dimensions []UsageDimension, sources map[int64]model.ZTAPIModelPriceSource) (XcloudUsageEstimate, error) {
	snapshot, valid := readFrozenXcloudSource(s)
	if !valid {
		return XcloudUsageEstimate{}, ErrXcloudQuoteMissing
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if source, ok := sources[snapshot.PriceSourceID]; ok && snapshot.PricePolicy == string(model.ZTAPIPricePolicyXcloud20261114) {
		if source.ID <= 0 || source.Version == 0 || source.Version != snapshot.PriceSourceVersion ||
			source.CreatedAt <= 0 || s.CreatedAt.IsZero() || source.CreatedAt > s.CreatedAt.Unix() ||
			source.SourceModel != snapshot.SourceModel || source.ModelConfigID != snapshot.PublicationID ||
			snapshot.PublicName != s.PublicModel || source.TokenPriceRulesJSON != snapshot.TokenPriceRulesJSON {
			// These historical snapshots predate the stricter projection. Keep
			// their existing exact-ID path, not a model-wide fallback.
			if snapshot.PriceSourceID != 1885 && snapshot.PriceSourceID != 1886 {
				return XcloudUsageEstimate{}, ErrXcloudQuoteMissing
			}
		} else {
			return estimateCohortQuote(snapshot, source, channelID, baseURL, dimensions)
		}
	}
	if baseURL != "https://www.xcloudtoken.com" {
		return XcloudUsageEstimate{}, ErrXcloudQuoteMissing
	}
	switch snapshot.PricePolicy {
	case "xcloud_verified_20261114":
		switch snapshot.SourceModel {
		case "claude-sonnet-5":
			if channelID == 4 && snapshot.PriceSourceID == 1885 && snapshot.PriceSourceVersion == 37 && slices.Equal(snapshot.AllowedChannelIDs, []int{2, 4}) {
				return estimateXcloudQuote(xcloudQuote(snapshot.SourceModel, "claude-max", "base", "2", "10", "0.2", "0", "2.5", "4"), dimensions)
			}
		case "deepseek-v4.1-flash":
			if channelID == 8 && snapshot.PriceSourceID == 1886 && snapshot.PriceSourceVersion == 2 && slices.Equal(snapshot.AllowedChannelIDs, []int{8}) {
				return estimateXcloudQuote(xcloudQuote(snapshot.SourceModel, "国产分组", "base", "0.084", "0.3", "0.0018", "0", "0", "0"), dimensions)
			}
		}
	case "xcloud_glm53_20261030":
		if snapshot.SourceModel == "glm-5.3" && channelID == 8 && snapshot.PriceSourceID == 1844 &&
			snapshot.PriceSourceVersion == 5 && slices.Equal(snapshot.AllowedChannelIDs, []int{8}) {
			return estimateXcloudQuote(xcloudQuote(snapshot.SourceModel, "国产分组", "固定", "4.8", "16.8", "1.2", "0", "0", "0"), dimensions)
		}
	}
	return XcloudUsageEstimate{}, ErrXcloudQuoteMissing
}

func estimateCohortQuote(snapshot frozenXcloudSource, source model.ZTAPIModelPriceSource, channelID int, baseURL string, dimensions []UsageDimension) (XcloudUsageEstimate, error) {
	usage := make(map[string]int64, len(dimensions))
	for _, d := range dimensions {
		if _, exists := usage[d.Dimension]; exists {
			return XcloudUsageEstimate{}, ErrXcloudQuoteMissing
		}
		usage[d.Dimension] = d.Quantity
	}
	cost, err := model.ZTAPIXcloudQuotedCost20261114(&source, snapshot.PublicName, snapshot.AllowedChannelIDs, channelID, baseURL, usage)
	if err != nil {
		return XcloudUsageEstimate{}, err
	}
	result := XcloudUsageEstimate{SourceModel: snapshot.SourceModel, CostUSDT: cost.String(), IsEstimate: true}
	if baseURL == yunxinPoolURL {
		result.SourceURL = yunxinPoolURL
	} else {
		result.SourceURL, result.DiscountFactor = XcloudPricingURL, XcloudSupplierDiscountFactor
	}
	return result, nil
}

func EstimateXcloudUsageForGroup(sourceModel, group, tier string, dimensions []UsageDimension) (XcloudUsageEstimate, error) {
	for _, quote := range XcloudPublicQuotes() {
		if quote.SourceModel == strings.TrimSpace(sourceModel) && quote.Group == group && quote.Tier == tier {
			return estimateXcloudQuote(quote, dimensions)
		}
	}
	return XcloudUsageEstimate{SourceModel: sourceModel, CostUSDT: "0"}, fmt.Errorf("%w: %s", ErrXcloudQuoteMissing, strings.TrimSpace(sourceModel))
}

func estimateXcloudQuote(quote XcloudPublicQuote, dimensions []UsageDimension) (XcloudUsageEstimate, error) {
	zero := XcloudUsageEstimate{SourceModel: quote.SourceModel, CostUSDT: "0"}
	if len(dimensions) == 0 {
		return zero, errors.New("Xcloud usage dimensions are missing")
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
		if _, generic := seen["cache_write"]; generic {
			if _, split5m := seen["cache_write_5m"]; split5m {
				return zero, errors.New("overlapping cache write dimensions")
			}
			if _, split1h := seen["cache_write_1h"]; split1h {
				return zero, errors.New("overlapping cache write dimensions")
			}
		}
		price, supported := xcloudDimensionPrice(quote, name)
		if !supported {
			return zero, fmt.Errorf("%w: %s/%s", ErrXcloudQuoteMissing, quote.SourceModel, name)
		}
		cost = cost.Add(decimal.NewFromInt(dimension.Quantity).Div(decimal.NewFromInt(1_000_000)).Mul(price))
	}
	zero.CostUSDT = cost.String()
	zero.SourceURL = quote.SourceURL
	zero.IsEstimate = true
	zero.DiscountFactor = quote.DiscountFactor.String()
	return zero, nil
}
