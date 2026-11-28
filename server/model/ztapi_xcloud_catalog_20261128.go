package model

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const ZTAPIXcloudQuoteSHA25620261128 = "fbb6168ea3b19c25769c5bc9ebfbaa2c1c19b152dddefc228fe8e5b865528d67"
const ZTAPIXcloudPricingVersion20261128 = "a42d372ccf0b5dd13ecf71203521f9d2"

//go:embed ztapi_xcloud_catalog_20261128.json
var ztapiXcloudManifest20261128 []byte

type ZTAPIXcloudQuote20261128 struct {
	SourceModel      string
	Group            string
	Tier             string
	MinInputTokens   int64
	MaxInputTokens   int64
	GroupMultiplier  decimal.Decimal
	RechargeDiscount decimal.Decimal
	BaseRates        map[string]decimal.Decimal
	CashRates        map[string]decimal.Decimal
	Endpoints        []string
}

type ztapiXcloudManifest20261128Type struct {
	Success        bool                   `json:"success"`
	PricingVersion string                 `json:"pricing_version"`
	GroupRatio     map[string]json.Number `json:"group_ratio"`
	Data           []struct {
		ModelName       string      `json:"model_name"`
		QuotaType       int         `json:"quota_type"`
		BillingMode     string      `json:"billing_mode"`
		BillingExpr     string      `json:"billing_expr"`
		ModelRatio      json.Number `json:"model_ratio"`
		CompletionRatio json.Number `json:"completion_ratio"`
		CacheRatio      json.Number `json:"cache_ratio"`
		CreateCache     json.Number `json:"create_cache_ratio"`
		Groups          []string    `json:"enable_groups"`
		Endpoints       []string    `json:"supported_endpoint_types"`
	} `json:"data"`
}

type ztapiXcloudTier20261128 struct {
	name     string
	min, max int64
	rates    map[string]decimal.Decimal
}

var ztapiXcloudTierPattern20261128 = regexp.MustCompile(`tier\("([a-z_]+)",\s*([^()]+)\)`)
var ztapiXcloudBoundaryPattern20261128 = regexp.MustCompile(`^len <= (\d+) \? TIER : TIER$`)
var ztapiXcloudTermPattern20261128 = regexp.MustCompile(`^(p|c|cr|cc|cc1h)\s*\*\s*(\d+(?:\.\d+)?)$`)

func ztapiXcloudTiers20261128(expression string) ([]ztapiXcloudTier20261128, error) {
	if len(expression) > 8192 {
		return nil, errors.New("Xcloud expression is too long")
	}
	rows := ztapiXcloudTierPattern20261128.FindAllStringSubmatch(expression, -1)
	shape := strings.TrimSpace(ztapiXcloudTierPattern20261128.ReplaceAllString(expression, "TIER"))
	var boundary int64
	if len(rows) == 1 && shape == "TIER" {
	} else if parts := ztapiXcloudBoundaryPattern20261128.FindStringSubmatch(shape); len(rows) == 2 && len(parts) == 2 {
		var err error
		boundary, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || boundary <= 0 || boundary == int64(^uint64(0)>>1) {
			return nil, errors.New("invalid Xcloud context boundary")
		}
	} else {
		return nil, errors.New("unsupported Xcloud tier expression")
	}
	dimensions := map[string]string{"p": ZTAPIBillingDimensionInputTokens, "c": ZTAPIBillingDimensionOutputTokens,
		"cr": ZTAPIBillingDimensionCacheRead, "cc": ZTAPIBillingDimensionCacheWrite5m, "cc1h": ZTAPIBillingDimensionCacheWrite1h}
	seen := map[string]bool{}
	result := make([]ztapiXcloudTier20261128, 0, len(rows))
	for i, row := range rows {
		if seen[row[1]] {
			return nil, errors.New("duplicate Xcloud tier")
		}
		seen[row[1]] = true
		tier := ztapiXcloudTier20261128{name: row[1], rates: map[string]decimal.Decimal{}}
		for _, term := range strings.Split(row[2], "+") {
			parts := ztapiXcloudTermPattern20261128.FindStringSubmatch(strings.TrimSpace(term))
			if len(parts) != 3 {
				return nil, errors.New("unsupported Xcloud billing term")
			}
			dimension := dimensions[parts[1]]
			if _, exists := tier.rates[dimension]; exists {
				return nil, errors.New("duplicate Xcloud billing dimension")
			}
			price, err := decimal.NewFromString(parts[2])
			if err != nil || price.IsNegative() {
				return nil, errors.New("invalid Xcloud rate")
			}
			tier.rates[dimension] = price
		}
		if !tier.rates[ZTAPIBillingDimensionInputTokens].IsPositive() || !tier.rates[ZTAPIBillingDimensionOutputTokens].IsPositive() {
			return nil, errors.New("Xcloud input and output rates are required")
		}
		if boundary != 0 {
			if i == 0 {
				tier.max = boundary
			} else {
				tier.min = boundary + 1
			}
		}
		result = append(result, tier)
	}
	return result, nil
}

func parseZTAPIXcloudQuoteManifest20261128(raw []byte) ([]ZTAPIXcloudQuote20261128, error) {
	if len(raw) > 2<<20 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return nil, errors.New("invalid Xcloud manifest JSON")
	}
	var manifest ztapiXcloudManifest20261128Type
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	if !manifest.Success || manifest.PricingVersion == "" || len(manifest.Data) == 0 || len(manifest.GroupRatio) == 0 {
		return nil, errors.New("incomplete Xcloud manifest")
	}
	groups := map[string]decimal.Decimal{}
	for group, number := range manifest.GroupRatio {
		value, err := decimal.NewFromString(number.String())
		if group == "" || err != nil || !value.IsPositive() {
			return nil, errors.New("invalid Xcloud group multiplier")
		}
		groups[group] = value
	}
	seen := map[string]bool{}
	discount := decimal.RequireFromString("0.5")
	var quotes []ZTAPIXcloudQuote20261128
	for _, row := range manifest.Data {
		if strings.TrimSpace(row.ModelName) != row.ModelName || row.ModelName == "" || seen[row.ModelName] || row.QuotaType != 0 || len(row.Groups) == 0 || len(row.Endpoints) == 0 {
			return nil, errors.New("invalid or duplicate Xcloud model")
		}
		seen[row.ModelName] = true
		endpoints := slices.Clone(row.Endpoints)
		sort.Strings(endpoints)
		for i, endpoint := range endpoints {
			if endpoint == "" || strings.TrimSpace(endpoint) != endpoint || (i > 0 && endpoint == endpoints[i-1]) {
				return nil, errors.New("invalid or duplicate Xcloud endpoint")
			}
		}
		var tiers []ztapiXcloudTier20261128
		var err error
		if row.BillingExpr != "" {
			tiers, err = ztapiXcloudTiers20261128(row.BillingExpr)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", row.ModelName, err)
			}
			if strings.HasPrefix(row.ModelName, "gpt-") {
				// GPT receipts use the generic cache-creation counter. cc1h is
				// separately quoted, but cc alone does not assert a five-minute TTL.
				for i := range tiers {
					if rate, exists := tiers[i].rates[ZTAPIBillingDimensionCacheWrite5m]; exists {
						tiers[i].rates[ZTAPIBillingDimensionCacheWrite] = rate
						delete(tiers[i].rates, ZTAPIBillingDimensionCacheWrite5m)
					}
				}
			}
		} else {
			if row.BillingMode != "" && row.BillingMode != "ratio" {
				return nil, errors.New("Xcloud billing mode requires an expression")
			}
			input, inputErr := decimal.NewFromString(row.ModelRatio.String())
			completion, outputErr := decimal.NewFromString(row.CompletionRatio.String())
			if inputErr != nil || outputErr != nil || !input.IsPositive() || !completion.IsPositive() {
				return nil, errors.New("incomplete Xcloud ratio quote")
			}
			input = input.Mul(decimal.NewFromInt(2))
			rates := map[string]decimal.Decimal{ZTAPIBillingDimensionInputTokens: input, ZTAPIBillingDimensionOutputTokens: input.Mul(completion)}
			for dimension, rawRatio := range map[string]json.Number{ZTAPIBillingDimensionCacheRead: row.CacheRatio, ZTAPIBillingDimensionCacheWrite: row.CreateCache} {
				if rawRatio == "" {
					continue
				}
				value, err := decimal.NewFromString(rawRatio.String())
				if err != nil || value.IsNegative() {
					return nil, errors.New("invalid Xcloud cache ratio")
				}
				if value.IsPositive() {
					rates[dimension] = input.Mul(value)
				}
			}
			tiers = []ztapiXcloudTier20261128{{name: "ratio", rates: rates}}
		}
		seenGroups := map[string]bool{}
		for _, group := range row.Groups {
			if group == "" || seenGroups[group] {
				return nil, errors.New("invalid or duplicate Xcloud model group")
			}
			seenGroups[group] = true
			multiplier, published := groups[group]
			if !published {
				continue
			}
			for _, tier := range tiers {
				cash := make(map[string]decimal.Decimal, len(tier.rates))
				for dimension, rate := range tier.rates {
					cash[dimension] = rate.Mul(multiplier).Mul(discount)
				}
				quotes = append(quotes, ZTAPIXcloudQuote20261128{SourceModel: row.ModelName, Group: group, Tier: tier.name,
					MinInputTokens: tier.min, MaxInputTokens: tier.max, GroupMultiplier: multiplier, RechargeDiscount: discount,
					BaseRates: maps.Clone(tier.rates), CashRates: cash, Endpoints: slices.Clone(endpoints)})
			}
		}
	}
	sort.Slice(quotes, func(i, j int) bool {
		if quotes[i].SourceModel != quotes[j].SourceModel {
			return quotes[i].SourceModel < quotes[j].SourceModel
		}
		if quotes[i].Group != quotes[j].Group {
			return quotes[i].Group < quotes[j].Group
		}
		return quotes[i].MinInputTokens < quotes[j].MinInputTokens
	})
	return quotes, nil
}

var ztapiLoadXcloudCatalog20261128 = sync.OnceValues(func() ([]ZTAPIXcloudQuote20261128, error) {
	hash := sha256.Sum256(ztapiXcloudManifest20261128)
	if hex.EncodeToString(hash[:]) != strings.ToLower(ZTAPIXcloudQuoteSHA25620261128) {
		return nil, errors.New("Xcloud dated manifest checksum mismatch")
	}
	var version struct {
		PricingVersion string `json:"pricing_version"`
	}
	if json.Unmarshal(ztapiXcloudManifest20261128, &version) != nil || version.PricingVersion != ZTAPIXcloudPricingVersion20261128 {
		return nil, errors.New("Xcloud dated pricing version mismatch")
	}
	return parseZTAPIXcloudQuoteManifest20261128(ztapiXcloudManifest20261128)
})

func ZTAPIXcloudCatalogQuotes20261128() ([]ZTAPIXcloudQuote20261128, error) {
	quotes, err := ztapiLoadXcloudCatalog20261128()
	if err != nil {
		return nil, err
	}
	result := make([]ZTAPIXcloudQuote20261128, len(quotes))
	for i, quote := range quotes {
		quote.BaseRates, quote.CashRates, quote.Endpoints = maps.Clone(quote.BaseRates), maps.Clone(quote.CashRates), slices.Clone(quote.Endpoints)
		result[i] = quote
	}
	return result, nil
}

func ValidateZTAPIXcloudLiveQuote20261128(raw []byte) error {
	quotes, err := parseZTAPIXcloudQuoteManifest20261128(raw)
	if err != nil {
		return err
	}
	var version struct {
		PricingVersion string `json:"pricing_version"`
	}
	if json.Unmarshal(raw, &version) != nil || version.PricingVersion != ZTAPIXcloudPricingVersion20261128 {
		return errors.New("Xcloud live pricing version changed; refresh the dated review")
	}
	expected, err := ZTAPIXcloudCatalogQuotes20261128()
	if err != nil || !reflect.DeepEqual(quotes, expected) {
		return errors.New("Xcloud live pricing differs from the frozen review")
	}
	return nil
}

func ZTAPIXcloudQuoteFor20261128(source, group string, promptTokens int64) (ZTAPIXcloudQuote20261128, error) {
	if promptTokens < 0 {
		return ZTAPIXcloudQuote20261128{}, errors.New("negative Xcloud prompt length")
	}
	quotes, err := ZTAPIXcloudCatalogQuotes20261128()
	if err != nil {
		return ZTAPIXcloudQuote20261128{}, err
	}
	for _, quote := range quotes {
		if quote.SourceModel == source && quote.Group == group && promptTokens >= quote.MinInputTokens && (quote.MaxInputTokens == 0 || promptTokens <= quote.MaxInputTokens) {
			return quote, nil
		}
	}
	return ZTAPIXcloudQuote20261128{}, errors.New("exact Xcloud model, group or context quote is unavailable")
}

// Keep the CNY 7:1 / FX 6.8 stress floor exact before rounding upwards.
func ZTAPIXcloudRetailFloor20261128(cost decimal.Decimal) (decimal.Decimal, error) {
	if cost.IsNegative() {
		return decimal.Zero, errors.New("negative Xcloud procurement cost")
	}
	value := new(big.Rat).Mul(cost.Rat(), big.NewRat(140000, 11))
	units, remainder := new(big.Int), new(big.Int)
	units.QuoRem(value.Num(), value.Denom(), remainder)
	if remainder.Sign() > 0 {
		units.Add(units, big.NewInt(1))
	}
	return decimal.NewFromBigInt(units, -4), nil
}
