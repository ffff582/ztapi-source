package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const (
	ZTAPIMediaBillingUnitUSDPerMillionTokens = "usd_per_million_tokens"
	ztapiMediaModalityImage                  = "image"
	ztapiMediaModalityVideo                  = "video"
	ztapiMediaDimensionInputTokens           = "input_tokens"
	ztapiMediaDimensionOutputTokens          = "output_tokens"
)

var ztapiSupportedMediaSaleCostShares = []decimal.Decimal{
	decimal.RequireFromString("0.60"),
	decimal.RequireFromString("0.80"),
	decimal.RequireFromString("0.4125"), // 33% pool cost sold at 80% of official price.
}

type ZTAPIMediaPriceContract struct {
	Version  uint64                `json:"version"`
	Modality string                `json:"modality"`
	Rules    []ZTAPIMediaPriceRule `json:"rules"`
}

type ZTAPIMediaPriceRule struct {
	ID          string            `json:"id"`
	Conditions  map[string]string `json:"conditions"`
	BillingUnit string            `json:"billing_unit"`
	CostUSD     map[string]string `json:"cost_usd"`
	SaleUSD     map[string]string `json:"sale_usd"`
	SourceCells map[string]string `json:"source_cells"`
}

type ZTAPIMediaPriceSelector struct {
	Modality   string            `json:"modality"`
	Conditions map[string]string `json:"conditions"`
}

var (
	ztapiMediaIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)
	ztapiMediaDecimalPattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
	ztapiMediaSourceCellPattern = regexp.MustCompile(`^[A-Z]+[1-9][0-9]*$`)
)

var ztapiMediaConditionKeys = map[string]bool{
	"token_bucket": true, "prompt_tokens_tier": true,
	"resolution": true, "contains_video_input": true,
}

var ztapiMediaDimensions = map[string]bool{
	"text_input": true, "text_cached_input": true,
	"image_input": true, "image_cached_input": true, "image_output": true,
	ztapiMediaDimensionInputTokens: true, ztapiMediaDimensionOutputTokens: true,
}

func ValidateZTAPIMediaPriceContract(raw string) error {
	_, err := ParseZTAPIMediaPriceContract(raw)
	return err
}

func SelectZTAPIMediaPriceRule(raw string, request ZTAPIMediaPriceSelector) (ZTAPIMediaPriceRule, error) {
	contract, err := ParseZTAPIMediaPriceContract(raw)
	if err != nil {
		return ZTAPIMediaPriceRule{}, err
	}
	if request.Modality != contract.Modality || len(request.Conditions) != len(contract.Rules[0].Conditions) {
		return ZTAPIMediaPriceRule{}, errors.New("media price selector does not match the contract")
	}
	for key := range request.Conditions {
		if _, ok := contract.Rules[0].Conditions[key]; !ok {
			return ZTAPIMediaPriceRule{}, fmt.Errorf("unsupported media price selector condition %q", key)
		}
	}
	for _, rule := range contract.Rules {
		if equalZTAPIStringMap(rule.Conditions, request.Conditions) {
			return cloneZTAPIMediaPriceRule(rule), nil
		}
	}
	return ZTAPIMediaPriceRule{}, errors.New("no media price rule matches the selector")
}

func CanonicalizeZTAPIMediaPriceContract(raw string) (string, error) {
	contract, err := ParseZTAPIMediaPriceContract(raw)
	if err != nil {
		return "", err
	}
	sort.Slice(contract.Rules, func(i, j int) bool { return contract.Rules[i].ID < contract.Rules[j].ID })
	encoded, err := common.Marshal(contract)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func ParseZTAPIMediaPriceContract(raw string) (ZTAPIMediaPriceContract, error) {
	var contract ZTAPIMediaPriceContract
	if strings.TrimSpace(raw) == "" {
		return contract, errors.New("media price contract is required")
	}
	if err := rejectDuplicateZTAPIJSONObjectMembers(raw); err != nil {
		return contract, err
	}
	if err := validateZTAPIMediaJSONFields([]byte(raw)); err != nil {
		return contract, err
	}
	if err := common.UnmarshalJsonStr(raw, &contract); err != nil {
		return contract, errors.New("media price contract must be valid JSON")
	}
	if contract.Version != 1 || (contract.Modality != ztapiMediaModalityImage && contract.Modality != ztapiMediaModalityVideo) || len(contract.Rules) == 0 {
		return contract, errors.New("unsupported media price contract version or modality")
	}

	ids := make(map[string]bool, len(contract.Rules))
	conditions := make(map[string]bool, len(contract.Rules))
	var conditionKeys []string
	var contractCostShare *decimal.Decimal
	for index, rule := range contract.Rules {
		if !ztapiMediaIdentifierPattern.MatchString(rule.ID) || ids[rule.ID] {
			return contract, errors.New("media price rule IDs must be unique canonical identifiers")
		}
		ids[rule.ID] = true
		if rule.BillingUnit != ZTAPIMediaBillingUnitUSDPerMillionTokens {
			return contract, errors.New("unsupported media price billing unit")
		}
		keys, err := validateZTAPIMediaConditions(contract.Modality, rule.Conditions)
		if err != nil {
			return contract, err
		}
		if index == 0 {
			conditionKeys = keys
		} else if strings.Join(conditionKeys, "\x00") != strings.Join(keys, "\x00") {
			return contract, errors.New("media price rules must use one exact condition shape")
		}
		signature := ztapiStringMapSignature(rule.Conditions)
		if conditions[signature] {
			return contract, errors.New("media price rules overlap")
		}
		conditions[signature] = true
		costShare, err := validateZTAPIMediaPriceMaps(rule, contractCostShare)
		if err != nil {
			return contract, fmt.Errorf("media price rule %q: %w", rule.ID, err)
		}
		if contractCostShare == nil {
			contractCostShare = &costShare
		}
	}
	if err := validateZTAPIMediaPriceMatrix(contract.Modality, conditionKeys, contract.Rules); err != nil {
		return contract, err
	}
	return contract, nil
}

func rejectDuplicateZTAPIJSONObjectMembers(raw string) error {
	err := common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw))
	if err == nil {
		return nil
	}
	var duplicate *common.DuplicateJsonObjectMemberError
	if errors.As(err, &duplicate) {
		return fmt.Errorf("duplicate media price contract field %q", duplicate.Name)
	}
	if errors.Is(err, common.ErrMultipleJsonValues) {
		return errors.New("media price contract must contain one JSON value")
	}
	return errors.New("media price contract must be valid JSON")
}

type ztapiMediaExpectedRule struct {
	id         string
	dimensions []string
}

func validateZTAPIMediaPriceMatrix(modality string, conditionKeys []string, rules []ZTAPIMediaPriceRule) error {
	expected := map[string]ztapiMediaExpectedRule{}
	add := func(id string, conditions map[string]string, dimensions ...string) {
		expected[ztapiStringMapSignature(conditions)] = ztapiMediaExpectedRule{id: id, dimensions: dimensions}
	}
	shape := strings.Join(conditionKeys, ",")
	switch {
	case modality == ztapiMediaModalityImage && shape == "token_bucket":
		for _, bucket := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
			add(bucket, map[string]string{"token_bucket": bucket}, bucket)
		}
	case modality == ztapiMediaModalityImage && shape == "prompt_tokens_tier":
		for _, tier := range []string{"lte_200k", "gt_200k"} {
			add(tier, map[string]string{"prompt_tokens_tier": tier}, ztapiMediaDimensionInputTokens, ztapiMediaDimensionOutputTokens)
		}
	case modality == ztapiMediaModalityVideo && shape == "contains_video_input,resolution":
		for _, resolution := range []string{"480p", "720p", "1080p", "4k"} {
			for _, containsVideo := range []string{"false", "true"} {
				id := resolution + "_video_" + containsVideo
				add(id, map[string]string{"contains_video_input": containsVideo, "resolution": resolution}, ztapiMediaDimensionInputTokens)
			}
		}
	case modality == ztapiMediaModalityVideo && shape == "contains_video_input":
		add("without_video_input", map[string]string{"contains_video_input": "false"}, ztapiMediaDimensionInputTokens)
		add("with_video_input", map[string]string{"contains_video_input": "true"}, ztapiMediaDimensionInputTokens)
	default:
		return errors.New("unsupported media price matrix")
	}
	if len(rules) != len(expected) {
		return errors.New("media price matrix is incomplete or contains extra rules")
	}
	for _, rule := range rules {
		want, ok := expected[ztapiStringMapSignature(rule.Conditions)]
		if !ok || rule.ID != want.id || !ztapiStringMapHasExactKeys(rule.CostUSD, want.dimensions) {
			return errors.New("media price matrix contains an unsupported rule")
		}
	}
	return nil
}

func ValidateZTAPIImagePriceProtocolCompatibility(price ZTAPIMediaPriceContract, protocol ZTAPIImageProtocolContract) error {
	if price.Modality != ztapiMediaModalityImage {
		return errors.New("image protocol requires an image media price contract")
	}
	expected := make(map[string]struct{})
	for _, rule := range price.Rules {
		for dimension := range rule.CostUSD {
			expected[dimension] = struct{}{}
		}
	}
	if protocol.ProviderModel == "gpt-image-2" {
		if IsZTAPIGPTImage2NotReportedUsageProtocol(protocol) && ztapiStringSetEquals(expected, []string{
			"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output",
		}) {
			return nil
		}
		return errors.New("gpt-image-2 requires the frozen three-dimensional not-reported-cache usage protocol")
	}
	if len(protocol.Usage.Fields) != len(expected) {
		return errors.New("image protocol usage dimensions do not match media pricing")
	}
	bindings := make(map[string]struct{}, len(protocol.Usage.Fields))
	for dimension, field := range protocol.Usage.Fields {
		if _, ok := expected[dimension]; !ok {
			return fmt.Errorf("image protocol usage dimension %q is not priced", dimension)
		}
		if _, duplicate := bindings[field]; duplicate {
			return errors.New("image protocol usage dimensions contain aliased field bindings")
		}
		bindings[field] = struct{}{}
	}
	wantCacheSemantics := "not_reported"
	for dimension := range expected {
		if strings.Contains(dimension, "cached") {
			wantCacheSemantics = "separate_dimension"
			break
		}
	}
	if protocol.Usage.TotalSemantics != "sum_of_dimensions" || protocol.Usage.CacheSemantics != wantCacheSemantics {
		return errors.New("image protocol usage semantics do not match media pricing")
	}
	return nil
}

// IsZTAPIGPTImage2NotReportedUsageProtocol identifies the frozen generation-only
// exception whose provider response reports three usage buckets and no cache split.
func IsZTAPIGPTImage2NotReportedUsageProtocol(protocol ZTAPIImageProtocolContract) bool {
	if protocol.Version != ZTAPIImageProtocolContractVersionV2 || protocol.ProviderModel != "gpt-image-2" ||
		protocol.EndpointType != ZTAPIImageEndpointGeneration || protocol.Method != "POST" || protocol.Path != "/v1/images/generations" ||
		protocol.Usage.UsageField != "usage" || protocol.Usage.TotalField != "total_tokens" ||
		protocol.Usage.TotalSemantics != "sum_of_dimensions" || protocol.Usage.CacheSemantics != "not_reported" {
		return false
	}
	want := map[string]string{
		"text_input":   "input_tokens_details.text_tokens",
		"image_input":  "input_tokens_details.image_tokens",
		"image_output": "output_tokens_details.image_tokens",
	}
	if len(protocol.Usage.Fields) != len(want) {
		return false
	}
	for dimension, path := range want {
		if protocol.Usage.Fields[dimension] != path {
			return false
		}
	}
	return true
}

func ztapiStringSetEquals(got map[string]struct{}, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, value := range want {
		if _, ok := got[value]; !ok {
			return false
		}
	}
	return true
}

func ValidateZTAPIVideoPriceProtocolCompatibility(price ZTAPIMediaPriceContract, protocol ZTAPIVideoProtocolContract) error {
	if price.Modality != ztapiMediaModalityVideo {
		return errors.New("video protocol requires a video media price contract")
	}
	expected := make(map[string]struct{})
	for _, rule := range price.Rules {
		for dimension := range rule.SaleUSD {
			expected[dimension] = struct{}{}
		}
	}
	if len(protocol.Usage.Fields) != len(expected) {
		return errors.New("video protocol usage dimensions do not match media pricing")
	}
	bindings := make(map[string]struct{}, len(protocol.Usage.Fields))
	for dimension, field := range protocol.Usage.Fields {
		if _, ok := expected[dimension]; !ok {
			return fmt.Errorf("video protocol usage dimension %q is not priced", dimension)
		}
		if _, duplicate := bindings[field]; duplicate {
			return errors.New("video protocol usage dimensions contain aliased field bindings")
		}
		bindings[field] = struct{}{}
	}
	return nil
}

func CalculateZTAPIVideoMaximumReservation(price ZTAPIMediaPriceContract, protocol ZTAPIVideoProtocolContract, selector ZTAPIVideoSelector, quotaPerUnitRaw string) (map[string]string, int64, error) {
	if err := ValidateZTAPIVideoPriceProtocolCompatibility(price, protocol); err != nil {
		return nil, 0, err
	}
	conditions := map[string]string{"contains_video_input": strconv.FormatBool(selector.ContainsVideoInput)}
	if _, ok := price.Rules[0].Conditions["resolution"]; ok {
		conditions["resolution"] = selector.Resolution
	}
	rule, err := SelectZTAPIMediaPriceRuleFromContract(price, ZTAPIMediaPriceSelector{Modality: ztapiMediaModalityVideo, Conditions: conditions})
	if err != nil {
		return nil, 0, err
	}
	authority, ok := protocol.FindReservationAuthority(selector)
	if !ok || len(authority.MaximumDimensions) != len(rule.SaleUSD) {
		return nil, 0, errors.New("video reservation authority does not match selected pricing")
	}
	quotaPerUnit, err := decimal.NewFromString(quotaPerUnitRaw)
	if err != nil || !quotaPerUnit.IsPositive() || quotaPerUnit.String() != quotaPerUnitRaw {
		return nil, 0, errors.New("video quota per unit is invalid")
	}
	maximum := make(map[string]string, len(authority.MaximumDimensions))
	total := decimal.Zero
	for dimension, rawQuantity := range authority.MaximumDimensions {
		rawPrice, priced := rule.SaleUSD[dimension]
		quantity, quantityErr := decimal.NewFromString(rawQuantity)
		priceValue, priceErr := decimal.NewFromString(rawPrice)
		if !priced || quantityErr != nil || priceErr != nil || quantity.IsNegative() || priceValue.IsNegative() ||
			!quantity.Equal(decimal.NewFromInt(quantity.IntPart())) || quantity.String() != rawQuantity {
			return nil, 0, errors.New("video reservation quantity or price is invalid")
		}
		maximum[dimension] = rawQuantity
		total = total.Add(quantity.Mul(priceValue).Mul(quotaPerUnit).Div(decimal.NewFromInt(1_000_000)))
	}
	rounded := total.Round(0)
	if rounded.IsNegative() || rounded.GreaterThan(decimal.NewFromInt(math.MaxInt32)) {
		return nil, 0, errors.New("video maximum reservation exceeds supported quota")
	}
	return maximum, rounded.IntPart(), nil
}

func SelectZTAPIMediaPriceRuleFromContract(contract ZTAPIMediaPriceContract, request ZTAPIMediaPriceSelector) (ZTAPIMediaPriceRule, error) {
	if request.Modality != contract.Modality || len(contract.Rules) == 0 || len(request.Conditions) != len(contract.Rules[0].Conditions) {
		return ZTAPIMediaPriceRule{}, errors.New("media price selector does not match the contract")
	}
	for _, rule := range contract.Rules {
		if equalZTAPIStringMap(rule.Conditions, request.Conditions) {
			return cloneZTAPIMediaPriceRule(rule), nil
		}
	}
	return ZTAPIMediaPriceRule{}, errors.New("no media price rule matches the selector")
}

func ztapiStringMapHasExactKeys(values map[string]string, keys []string) bool {
	if len(values) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func validateZTAPIMediaJSONFields(raw []byte) error {
	var object map[string]json.RawMessage
	if err := common.Unmarshal(raw, &object); err != nil {
		return errors.New("media price contract must be a JSON object")
	}
	if err := exactZTAPIJSONFields(object, "version", "modality", "rules"); err != nil {
		return err
	}
	var rules []json.RawMessage
	if err := common.Unmarshal(object["rules"], &rules); err != nil {
		return errors.New("media price rules must be an array")
	}
	for _, rawRule := range rules {
		var rule map[string]json.RawMessage
		if err := common.Unmarshal(rawRule, &rule); err != nil {
			return errors.New("media price rule must be an object")
		}
		if err := exactZTAPIJSONFields(rule, "id", "conditions", "billing_unit", "cost_usd", "sale_usd", "source_cells"); err != nil {
			return err
		}
	}
	return nil
}

func exactZTAPIJSONFields(object map[string]json.RawMessage, fields ...string) error {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	for field := range object {
		if !allowed[field] {
			return fmt.Errorf("unknown media price contract field %q", field)
		}
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("missing media price contract field %q", field)
		}
	}
	return nil
}

func validateZTAPIMediaConditions(modality string, conditions map[string]string) ([]string, error) {
	if len(conditions) == 0 {
		return nil, errors.New("media price rule conditions are required")
	}
	keys := make([]string, 0, len(conditions))
	for key, value := range conditions {
		if !ztapiMediaConditionKeys[key] || !ztapiMediaIdentifierPattern.MatchString(value) {
			return nil, fmt.Errorf("unsupported media price condition %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	shape := strings.Join(keys, ",")
	validShape := (modality == ztapiMediaModalityImage && (shape == "prompt_tokens_tier" || shape == "token_bucket")) ||
		(modality == ztapiMediaModalityVideo && (shape == "contains_video_input" || shape == "contains_video_input,resolution"))
	if !validShape {
		return nil, errors.New("media price condition shape does not match modality")
	}
	return keys, nil
}

func validateZTAPIMediaPriceMaps(rule ZTAPIMediaPriceRule, expectedShare *decimal.Decimal) (decimal.Decimal, error) {
	if len(rule.CostUSD) == 0 || len(rule.CostUSD) != len(rule.SaleUSD) || len(rule.CostUSD) != len(rule.SourceCells) {
		return decimal.Zero, errors.New("cost, sale, and source-cell dimensions must match")
	}
	var ruleShare *decimal.Decimal
	for dimension, rawCost := range rule.CostUSD {
		if !ztapiMediaDimensions[dimension] {
			return decimal.Zero, fmt.Errorf("unsupported billing dimension %q", dimension)
		}
		rawSale, saleExists := rule.SaleUSD[dimension]
		cell, cellExists := rule.SourceCells[dimension]
		if !saleExists || !cellExists || !ztapiMediaSourceCellPattern.MatchString(cell) {
			return decimal.Zero, fmt.Errorf("missing sale or source cell for %q", dimension)
		}
		if !ztapiMediaDecimalPattern.MatchString(rawCost) || !ztapiMediaDecimalPattern.MatchString(rawSale) {
			return decimal.Zero, fmt.Errorf("prices for %q must be positive plain decimal strings", dimension)
		}
		cost, costErr := decimal.NewFromString(rawCost)
		sale, saleErr := decimal.NewFromString(rawSale)
		if costErr != nil || saleErr != nil || !cost.IsPositive() || !sale.IsPositive() {
			return decimal.Zero, fmt.Errorf("prices for %q must be positive", dimension)
		}
		var matched *decimal.Decimal
		for i := range ztapiSupportedMediaSaleCostShares {
			share := ztapiSupportedMediaSaleCostShares[i]
			if sale.Equal(cost.Div(share).Round(10)) {
				matched = &share
				break
			}
		}
		if matched == nil || (expectedShare != nil && !matched.Equal(*expectedShare)) ||
			(ruleShare != nil && !matched.Equal(*ruleShare)) {
			return decimal.Zero, fmt.Errorf("sale price for %q does not match one consistent supported pricing policy", dimension)
		}
		ruleShare = matched
	}
	return *ruleShare, nil
}

func ztapiStringMapSignature(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, "\x00")
}

func equalZTAPIStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func cloneZTAPIMediaPriceRule(rule ZTAPIMediaPriceRule) ZTAPIMediaPriceRule {
	rule.Conditions = cloneZTAPIStringMap(rule.Conditions)
	rule.CostUSD = cloneZTAPIStringMap(rule.CostUSD)
	rule.SaleUSD = cloneZTAPIStringMap(rule.SaleUSD)
	rule.SourceCells = cloneZTAPIStringMap(rule.SourceCells)
	return rule
}

func cloneZTAPIStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
