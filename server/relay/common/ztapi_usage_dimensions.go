package common

import (
	"math"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type ZTAPIQuoteState string

const (
	ZTAPIQuoteStateMissing ZTAPIQuoteState = "missing"
	ZTAPIQuoteStateQuoted  ZTAPIQuoteState = "quoted"
	ZTAPIQuoteStateFree    ZTAPIQuoteState = "free"
)

type ZTAPIUsageDimension struct {
	Dimension    string          `json:"dimension"`
	Quantity     int64           `json:"quantity"`
	Unit         string          `json:"unit"`
	QuoteState   ZTAPIQuoteState `json:"quote_state"`
	UnitPriceUSD string          `json:"unit_price_usd,omitempty"`
}

type ZTAPIUsageDimensionResult struct {
	Managed           bool                  `json:"managed"`
	Pending           bool                  `json:"pending"`
	UsageMissing      bool                  `json:"usage_missing"`
	UsageSemantic     string                `json:"usage_semantic,omitempty"`
	MissingDimensions []string              `json:"missing_dimensions,omitempty"`
	InvalidDimensions []string              `json:"invalid_dimensions,omitempty"`
	Dimensions        []ZTAPIUsageDimension `json:"dimensions,omitempty"`
}

// ValidateZTAPIUsageDimensions is a read-only pre-settlement check. It never
// estimates missing usage, reads live pricing, or changes legacy billing.
// UnitPriceUSD is per single Unit, not the per-million price stored in SaleUSD.
// Only fields retained by dto.Usage and observed tool counters are covered;
// discarded provider-specific billing fields require separate reconciliation.
func ValidateZTAPIUsageDimensions(c *gin.Context, info *RelayInfo, usage *dto.Usage) ZTAPIUsageDimensionResult {
	if info == nil || info.ZTAPIPublicationSnapshot == nil {
		return ZTAPIUsageDimensionResult{}
	}
	result := ZTAPIUsageDimensionResult{Managed: true, UsageMissing: usage == nil}
	invalid := make(map[string]bool)
	quantities := make(map[string]int64)
	units := make(map[string]string)
	add := func(name, unit string, quantity int64) {
		if quantity == 0 {
			return
		}
		if quantity < 0 || quantity > math.MaxInt64/8 || (units[name] != "" && units[name] != unit) {
			invalid[name] = true
		}
		if quantity > 0 && quantities[name] > math.MaxInt64-quantity {
			invalid[name] = true
			return
		}
		quantities[name] += quantity
		units[name] = unit
	}
	if usage != nil {
		semantic := usage.UsageSemantic
		if semantic == "" {
			semantic = "openai"
			if info.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
				semantic = "anthropic"
			}
		}
		result.UsageSemantic = semantic
		if semantic != "anthropic" && semantic != "openai" {
			invalid["usage_semantic"] = true
		}
		for name, values := range map[string][]int{
			"input_tokens":        {usage.PromptTokens, usage.InputTokens},
			"output_tokens":       {usage.CompletionTokens, usage.OutputTokens, usage.CompletionTokenDetails.ReasoningTokens},
			"total_tokens":        {usage.TotalTokens},
			"cache_read":          {usage.PromptTokensDetails.CachedTokens, usage.PromptCacheHitTokens},
			"cache_write":         {usage.PromptTokensDetails.CachedCreationTokens},
			"cache_write_5m":      {usage.ClaudeCacheCreation5mTokens},
			"cache_write_1h":      {usage.ClaudeCacheCreation1hTokens},
			"image_input_tokens":  {usage.PromptTokensDetails.ImageTokens},
			"audio_input_tokens":  {usage.PromptTokensDetails.AudioTokens},
			"image_output_tokens": {usage.CompletionTokenDetails.ImageTokens},
			"audio_output_tokens": {usage.CompletionTokenDetails.AudioTokens},
		} {
			for _, value := range values {
				if value < 0 || int64(value) > math.MaxInt64/8 {
					invalid[name] = true
				}
			}
		}
		if semantic != "anthropic" && usage.PromptTokens != 0 && usage.InputTokens != 0 && usage.PromptTokens != usage.InputTokens {
			invalid["input_tokens"] = true
		}
		if usage.CompletionTokens != 0 && usage.OutputTokens != 0 && usage.CompletionTokens != usage.OutputTokens {
			invalid["output_tokens"] = true
		}
		input, output := int64(usage.PromptTokens), int64(usage.CompletionTokens)
		if input == 0 && semantic != "anthropic" {
			input = int64(usage.InputTokens)
		}
		if output == 0 {
			output = int64(usage.OutputTokens)
		}
		if int64(usage.CompletionTokenDetails.ReasoningTokens) > output {
			invalid["output_tokens"] = true
		}
		details := usage.PromptTokensDetails
		if usage.InputTokensDetails != nil {
			other := usage.InputTokensDetails
			for name, value := range map[string]int{
				"cache_read": other.CachedTokens, "cache_write": other.CachedCreationTokens,
				"image_input_tokens": other.ImageTokens, "audio_input_tokens": other.AudioTokens,
			} {
				if value < 0 || int64(value) > math.MaxInt64/8 {
					invalid[name] = true
				}
			}
			details.CachedTokens = max(details.CachedTokens, other.CachedTokens)
			details.CachedCreationTokens = max(details.CachedCreationTokens, other.CachedCreationTokens)
			details.ImageTokens = max(details.ImageTokens, other.ImageTokens)
			details.AudioTokens = max(details.AudioTokens, other.AudioTokens)
		}
		cacheRead := int64(max(details.CachedTokens, usage.PromptCacheHitTokens))
		write5m, write1h := int64(usage.ClaudeCacheCreation5mTokens), int64(usage.ClaudeCacheCreation1hTokens)
		cacheWrite := max(int64(details.CachedCreationTokens), write5m+write1h)
		// The generic write count is a total, not another charge on top of TTLs.
		add("cache_read", "token", cacheRead)
		add("cache_write", "token", cacheWrite-write5m-write1h)
		add("cache_write_5m", "token", write5m)
		add("cache_write_1h", "token", write1h)
		if semantic != "anthropic" {
			input -= cacheRead + cacheWrite
		}
		input -= int64(details.ImageTokens) + int64(details.AudioTokens)
		output -= int64(usage.CompletionTokenDetails.ImageTokens) + int64(usage.CompletionTokenDetails.AudioTokens)
		add("input_tokens", "token", input)
		add("output_tokens", "token", output)
		// Image/audio unit quotations cannot authorize image/audio token usage.
		add("image_input_tokens", "token", int64(details.ImageTokens))
		add("image_output_tokens", "token", int64(usage.CompletionTokenDetails.ImageTokens))
		add("audio_input_tokens", "token", int64(details.AudioTokens))
		add("audio_output_tokens", "token", int64(usage.CompletionTokenDetails.AudioTokens))
		if usage.TotalTokens > 0 && len(quantities) == 0 {
			invalid["total_tokens"] = true
		}
	}
	if info.ResponsesUsageInfo != nil {
		for name, tool := range info.ResponsesUsageInfo.BuiltInTools {
			if tool != nil {
				add(name, "call", int64(tool.CallCount))
			}
		}
	}
	if c != nil {
		add("web_search", "call", int64(c.GetInt("claude_web_search_requests")))
		if c.GetBool("image_generation_call") {
			add("image_generation:"+c.GetString("image_generation_call_quality")+":"+c.GetString("image_generation_call_size"), "image", 1)
		}
	}
	names := make([]string, 0, len(quantities))
	for name := range quantities {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dimension := ZTAPIUsageDimension{Dimension: name, Quantity: quantities[name], Unit: units[name], QuoteState: ZTAPIQuoteStateMissing}
		dimension.QuoteState, dimension.UnitPriceUSD = ztapiFrozenUnitPrice(info.ZTAPIPublicationSnapshot, name, dimension.Unit)
		if dimension.QuoteState == ZTAPIQuoteStateMissing {
			result.MissingDimensions = append(result.MissingDimensions, name)
		}
		if dimension.Quantity < 0 {
			invalid[name] = true
		}
		result.Dimensions = append(result.Dimensions, dimension)
	}
	if !result.UsageMissing && len(result.Dimensions) == 0 {
		invalid["usage_zero_unconfirmed"] = true
	}
	for name := range invalid {
		result.InvalidDimensions = append(result.InvalidDimensions, name)
	}
	sort.Strings(result.InvalidDimensions)
	result.Pending = result.UsageMissing || len(result.MissingDimensions) != 0 || len(result.InvalidDimensions) != 0
	return result
}

func ztapiFrozenUnitPrice(snapshot *ZTAPIPublicationSnapshot, name, unit string) (ZTAPIQuoteState, string) {
	if snapshot.PriceSourceID <= 0 || snapshot.PriceSourceVersion == 0 {
		return ZTAPIQuoteStateMissing, ""
	}
	listed := false
	for _, dimension := range snapshot.BillingDimensions {
		if dimension == name {
			listed = true
			break
		}
	}
	if !listed {
		return ZTAPIQuoteStateMissing, ""
	}
	price, err := decimal.NewFromString(strings.TrimSpace(snapshot.SaleUSD[name]))
	if err != nil || price.IsNegative() {
		return ZTAPIQuoteStateMissing, ""
	}
	if price.IsZero() {
		return ZTAPIQuoteStateFree, "0"
	}
	if unit == "token" {
		price = price.Shift(-6)
	}
	return ZTAPIQuoteStateQuoted, price.String()
}
