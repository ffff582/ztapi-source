package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

type ImageSaleReferralQuoteItem struct {
	Key                   string           `json:"key"`
	CustomerRevenueUSD    string           `json:"customer_revenue_usd"`
	QuotedSupplierCostUSD string           `json:"quoted_supplier_cost_usd"`
	AssumedTokensPerImage map[string]int64 `json:"assumed_tokens_per_image"`
	QuoteMarginPass       bool             `json:"quote_margin_pass"`
}

// Approved quote evidence may support referral admission, but is not proof of
// actual profit or a supplier usage/cost hard cap.
type ImageSaleReferralQuote struct {
	Scope                string                       `json:"scope"`
	QuoteOnly            bool                         `json:"quote_only"`
	SupplierHardCapKnown bool                         `json:"supplier_hard_cap_known"`
	RevenueUnit          string                       `json:"revenue_unit"`
	QuotedCostUnit       string                       `json:"quoted_cost_unit"`
	SupplierPriceUnit    string                       `json:"supplier_price_unit"`
	GiftRate             string                       `json:"gift_rate"`
	ReferralRate         string                       `json:"referral_rate"`
	TargetMargin         string                       `json:"target_margin"`
	FingerprintSHA256    string                       `json:"fingerprint_sha256"`
	AllQuoteMarginsPass  bool                         `json:"all_quote_margins_pass"`
	Items                []ImageSaleReferralQuoteItem `json:"items"`
}

type ztapiImageSaleReferralAssumption struct {
	key, price string
	tokens     map[string]int64
}

func ztapiImageSaleReferralAssumptions(retail ZTAPIImageSaleContract) []ztapiImageSaleReferralAssumption {
	return []ztapiImageSaleReferralAssumption{
		{"image_sale/1K", retail.BasePrices["1K"], map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 8192}},
		{"image_sale/2K", retail.BasePrices["2K"], map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 8192}},
		{"image_sale/4K", retail.BasePrices["4K"], map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 16384}},
		{"image_sale/extra_prompt_block", retail.PromptBlockPrice, map[string]int64{"text_input": 2000}},
		{"image_sale/extra_reference_unit", retail.ReferenceUnitPrice, map[string]int64{"image_input": 4096}},
	}
}

func QuoteImageSaleReferralCosts(retailJSON, sourceCostJSON, operation string) (ImageSaleReferralQuote, error) {
	invalid := ImageSaleReferralQuote{}
	retail, err := ParseZTAPIImageSaleContract(retailJSON)
	if err != nil || (operation != "generation" && operation != "edit") {
		return invalid, errors.New("image sale referral quote requires an approved retail contract and operation")
	}
	canonicalRetail, err := CanonicalizeZTAPIImageSaleContract(retailJSON)
	if err != nil {
		return invalid, err
	}
	canonicalCost, err := CanonicalizeZTAPIMediaPriceContract(sourceCostJSON)
	if err != nil {
		return invalid, err
	}
	quote := ImageSaleReferralQuote{
		Scope: "quote_assumptions_only", QuoteOnly: true, RevenueUnit: ZTAPIImageSaleBillingUnit,
		QuotedCostUnit:    ZTAPIImageSaleBillingUnit,
		SupplierPriceUnit: ZTAPIMediaBillingUnitUSDPerMillionTokens,
		GiftRate:          "0.05", ReferralRate: "0.05", TargetMargin: "0.10", AllQuoteMarginsPass: true,
	}
	for _, assumption := range ztapiImageSaleReferralAssumptions(retail) {
		cost, err := CalculateZTAPIImageSaleTokenCost(canonicalCost, operation, assumption.tokens)
		if err != nil {
			return invalid, err
		}
		price, err := decimal.NewFromString(assumption.price)
		if err != nil || !price.IsPositive() {
			return invalid, errors.New("image sale retail price missing")
		}
		item := ImageSaleReferralQuoteItem{
			Key: assumption.key, CustomerRevenueUSD: price.String(), QuotedSupplierCostUSD: cost.String(),
			AssumedTokensPerImage: map[string]int64{},
			// No division or rounded margin at the exact 10% threshold.
			QuoteMarginPass: !cost.Mul(decimal.RequireFromString("1.05")).GreaterThan(price.Mul(decimal.RequireFromString("0.85"))),
		}
		for dimension, quantity := range assumption.tokens {
			item.AssumedTokensPerImage[dimension] = quantity
		}
		quote.Items = append(quote.Items, item)
		quote.AllQuoteMarginsPass = quote.AllQuoteMarginsPass && item.QuoteMarginPass
	}
	payload, err := common.Marshal(struct {
		Version    int                    `json:"version"`
		ImageSale  string                 `json:"image_sale"`
		SourceCost string                 `json:"source_cost"`
		Operation  string                 `json:"image_operation"`
		Quote      ImageSaleReferralQuote `json:"quote"`
	}{1, canonicalRetail, canonicalCost, operation, quote})
	if err != nil {
		return invalid, err
	}
	digest := sha256.Sum256(payload)
	quote.FingerprintSHA256 = hex.EncodeToString(digest[:])
	return quote, nil
}

func CalculateZTAPIImageSaleTokenCost(contractJSON, operation string, quantities map[string]int64) (decimal.Decimal, error) {
	contract, err := ParseZTAPIMediaPriceContract(contractJSON)
	if err != nil || contract.Modality != "image" || len(quantities) == 0 || (operation != "generation" && operation != "edit") {
		return decimal.Zero, errors.New("image supplier cost matrix or operation missing or invalid")
	}
	keys := make([]string, 0, len(quantities))
	for dimension := range quantities {
		keys = append(keys, dimension)
	}
	sort.Strings(keys)
	cost := decimal.Zero
	for _, dimension := range keys {
		if quantities[dimension] < 0 {
			return decimal.Zero, errors.New("invalid supplier Token quantity")
		}
		conditions := map[string]string{"token_bucket": dimension}
		if _, ok := contract.Rules[0].Conditions["image_operation"]; ok {
			conditions["image_operation"] = operation
		}
		rule, err := SelectZTAPIMediaPriceRuleFromContract(contract, ZTAPIMediaPriceSelector{Modality: "image", Conditions: conditions})
		if err != nil {
			return decimal.Zero, err
		}
		rate, err := decimal.NewFromString(rule.CostUSD[dimension])
		if err != nil || !rate.IsPositive() {
			return decimal.Zero, errors.New("supplier Token price missing")
		}
		cost = cost.Add(rate.Mul(decimal.NewFromInt(quantities[dimension])).Shift(-6))
	}
	return cost, nil
}

// Stored in the existing attestation item's Evidence string; quoted admission
// does not populate the legacy supplier/usage upper-bound fields.
type ZTAPIImageSaleReferralEvidence struct {
	Version                int                        `json:"version"`
	Scope                  string                     `json:"scope"`
	QuoteOnly              bool                       `json:"quote_only"`
	HardCapKnown           bool                       `json:"hard_cap_known"`
	RevenueUnit            string                     `json:"revenue_unit"`
	QuotedCostUnit         string                     `json:"quoted_cost_unit"`
	Operation              string                     `json:"image_operation"`
	QuoteFingerprintSHA256 string                     `json:"quote_fingerprint_sha256"`
	Item                   ImageSaleReferralQuoteItem `json:"item"`
}

func BuildZTAPIImageSaleReferralEvidence(quote ImageSaleReferralQuote, operation, itemKey string) (ZTAPIImageSaleReferralEvidence, error) {
	for _, item := range quote.Items {
		if item.Key == itemKey {
			evidence := ZTAPIImageSaleReferralEvidence{Version: 1, Scope: quote.Scope, QuoteOnly: quote.QuoteOnly, HardCapKnown: quote.SupplierHardCapKnown,
				RevenueUnit: quote.RevenueUnit, QuotedCostUnit: quote.QuotedCostUnit, Operation: operation, QuoteFingerprintSHA256: quote.FingerprintSHA256, Item: item}
			raw, err := common.Marshal(evidence)
			if err != nil {
				return ZTAPIImageSaleReferralEvidence{}, err
			}
			return ParseZTAPIImageSaleReferralEvidence(string(raw))
		}
	}
	return ZTAPIImageSaleReferralEvidence{}, errors.New("image sale quoted tier missing")
}

func ParseZTAPIImageSaleReferralEvidence(raw string) (ZTAPIImageSaleReferralEvidence, error) {
	invalid := errors.New("image sale quoted evidence invalid")
	var evidence ZTAPIImageSaleReferralEvidence
	if len(raw) > 16384 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil || common.DecodeJsonStrict(strings.NewReader(raw), &evidence) != nil {
		return ZTAPIImageSaleReferralEvidence{}, invalid
	}
	var fields map[string]json.RawMessage
	if common.UnmarshalJsonStr(raw, &fields) != nil || len(fields) != 9 {
		return ZTAPIImageSaleReferralEvidence{}, invalid
	}
	for _, key := range []string{"version", "scope", "quote_only", "hard_cap_known", "revenue_unit", "quoted_cost_unit", "image_operation", "quote_fingerprint_sha256", "item"} {
		if _, ok := fields[key]; !ok {
			return ZTAPIImageSaleReferralEvidence{}, invalid
		}
	}
	var itemFields map[string]json.RawMessage
	if common.Unmarshal(fields["item"], &itemFields) != nil || len(itemFields) != 5 {
		return ZTAPIImageSaleReferralEvidence{}, invalid
	}
	for _, key := range []string{"key", "customer_revenue_usd", "quoted_supplier_cost_usd", "assumed_tokens_per_image", "quote_margin_pass"} {
		if _, ok := itemFields[key]; !ok {
			return ZTAPIImageSaleReferralEvidence{}, invalid
		}
	}
	if common.GetJsonType(fields["hard_cap_known"]) != "boolean" || common.GetJsonType(fields["quote_only"]) != "boolean" || common.GetJsonType(itemFields["quote_margin_pass"]) != "boolean" {
		return ZTAPIImageSaleReferralEvidence{}, invalid
	}
	hash, err := hex.DecodeString(evidence.QuoteFingerprintSHA256)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != evidence.QuoteFingerprintSHA256 || evidence.Version != 1 || evidence.Scope != "quote_assumptions_only" || !evidence.QuoteOnly || evidence.HardCapKnown ||
		evidence.RevenueUnit != ZTAPIImageSaleBillingUnit || evidence.QuotedCostUnit != ZTAPIImageSaleBillingUnit || (evidence.Operation != "generation" && evidence.Operation != "edit") {
		return ZTAPIImageSaleReferralEvidence{}, invalid
	}
	retail, _ := ParseZTAPIImageSaleContract(ZTAPIImageSaleContract20261109JSON())
	for _, expected := range ztapiImageSaleReferralAssumptions(retail) {
		if evidence.Item.Key != expected.key {
			continue
		}
		cost, err := ztapiImageSaleEvidenceMoney(evidence.Item.QuotedSupplierCostUSD)
		price := decimal.RequireFromString(expected.price)
		if err != nil || evidence.Item.CustomerRevenueUSD != price.String() || !reflect.DeepEqual(evidence.Item.AssumedTokensPerImage, expected.tokens) ||
			evidence.Item.QuoteMarginPass != !cost.Mul(decimal.RequireFromString("1.05")).GreaterThan(price.Mul(decimal.RequireFromString("0.85"))) {
			return ZTAPIImageSaleReferralEvidence{}, invalid
		}
		return evidence, nil
	}
	return ZTAPIImageSaleReferralEvidence{}, invalid
}

func ztapiImageSaleEvidenceMoney(raw string) (decimal.Decimal, error) {
	if len(raw) == 0 || len(raw) > 40 || strings.Count(raw, ".") > 1 {
		return decimal.Zero, errors.New("invalid quote amount")
	}
	for _, ch := range raw {
		if (ch < '0' || ch > '9') && ch != '.' {
			return decimal.Zero, errors.New("invalid quote amount")
		}
	}
	return decimal.NewFromString(raw)
}

func ZTAPIImageSaleReferralDimension(operation, itemKey string) (string, error) {
	if operation != "generation" && operation != "edit" {
		return "", errors.New("invalid image sale operation")
	}
	switch itemKey {
	case "image_sale/1K", "image_sale/2K", "image_sale/4K", "image_sale/extra_prompt_block", "image_sale/extra_reference_unit":
		return "spec:image_sale_" + operation + "_" + strings.ToLower(strings.TrimPrefix(itemKey, "image_sale/")) + "/images", nil
	}
	return "", errors.New("invalid image sale quoted tier")
}
