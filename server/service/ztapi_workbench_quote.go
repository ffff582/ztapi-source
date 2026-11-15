package service

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/shopspring/decimal"
)

const ZTAPIWorkbenchMaxWire = 196608

type workbenchResponsesBody struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	Instructions string          `json:"instructions"`
	Stream       bool            `json:"stream"`
	Store        *bool           `json:"store"`
	Output       int             `json:"max_output_tokens"`
	Tools        json.RawMessage `json:"tools"`
	ToolChoice   string          `json:"tool_choice"`
	Parallel     *bool           `json:"parallel_tool_calls"`
	CacheKey     string          `json:"prompt_cache_key"`
}

func decodeWorkbenchWire(data []byte, dst any) bool {
	return common.RejectDuplicateJsonObjectMembers(bytes.NewReader(data)) == nil && common.DecodeJsonStrict(bytes.NewReader(data), dst) == nil
}

func workbenchText(raw json.RawMessage, parts bool) bool {
	var text string
	if len(raw) > 0 && raw[0] == '"' && common.Unmarshal(raw, &text) == nil {
		return true
	}
	var content []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if !parts || !decodeWorkbenchWire(raw, &content) || len(content) == 0 {
		return false
	}
	for _, part := range content {
		if (part.Type != "input_text" && part.Type != "output_text" && part.Type != "text") || part.Text == nil {
			return false
		}
	}
	return true
}

func ZTAPIWorkbenchWireOutput(data []byte, expectedModel string) (int, error) {
	var body workbenchResponsesBody
	if len(data) > ZTAPIWorkbenchMaxWire || !decodeWorkbenchWire(data, &body) || body.Model != expectedModel ||
		!body.Stream || body.Store == nil || *body.Store || body.Output < 1 || body.Output > 4096 || len(body.CacheKey) > 128 ||
		!workbenchToolChoice(body.ToolChoice) || !workbenchToolDeclarations(body.Tools, true) || !workbenchTextInput(body.Input, true) {
		return 0, model.ErrZTAPISettlementInvalid
	}
	return body.Output, nil
}

// ZTAPIWorkbenchQuote covers the bounded text wire, at the highest applicable
// input/cache rate and output rate. Unknown billing dimensions fail closed.
func ZTAPIWorkbenchQuote(snapshot *relaycommon.ZTAPIPublicationSnapshot, outboundBytes, maxOutput int) (int64, error) {
	if snapshot == nil || snapshot.Modality != model.ZTAPIModalityText || snapshot.PriceSourceID <= 0 || len(snapshot.AllowedChannelIDs) == 0 ||
		outboundBytes < 1 || outboundBytes > ZTAPIWorkbenchMaxWire || maxOutput < 1 || maxOutput > 4096 ||
		common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return 0, model.ErrZTAPISettlementInvalid
	}
	priceMaps := []map[string]string{snapshot.SaleUSD}
	if raw := strings.TrimSpace(snapshot.TokenPriceRulesJSON); raw != "" {
		var rules []struct {
			Conditions []string          `json:"conditions"`
			Sale       map[string]string `json:"sale"`
		}
		if !decodeWorkbenchWire([]byte(raw), &rules) || len(rules) == 0 || len(rules) > 128 {
			return 0, model.ErrZTAPISettlementInvalid
		}
		for _, rule := range rules {
			priceMaps = append(priceMaps, rule.Sale)
		}
	}
	var input, output decimal.Decimal
	for _, prices := range priceMaps {
		in, out, err := workbenchMaxTokenRates(prices)
		if err != nil {
			return 0, err
		}
		input, output = decimal.Max(input, in), decimal.Max(output, out)
	}
	inputTokens := max(outboundBytes+256, common.PreConsumedQuota)
	quota := input.Mul(decimal.NewFromInt(int64(inputTokens))).Add(decimal.Max(input, output).Mul(decimal.NewFromInt(int64(maxOutput)))).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).Div(decimal.NewFromInt(1000000)).Ceil()
	if !quota.IsPositive() || quota.GreaterThan(decimal.NewFromInt(math.MaxInt32)) {
		return 0, model.ErrZTAPIWorkbenchBudget
	}
	return quota.IntPart(), nil
}

func workbenchMaxTokenRates(prices map[string]string) (decimal.Decimal, decimal.Decimal, error) {
	var input, output decimal.Decimal
	for dim, raw := range prices {
		price, err := decimal.NewFromString(raw)
		if err != nil || price.IsNegative() {
			return input, output, model.ErrZTAPISettlementInvalid
		}
		switch dim {
		case model.ZTAPIBillingDimensionInputTokens, model.ZTAPIBillingDimensionCacheRead, model.ZTAPIBillingDimensionCacheWrite,
			model.ZTAPIBillingDimensionCacheWrite5m, model.ZTAPIBillingDimensionCacheWrite1h:
			input = decimal.Max(input, price)
		case model.ZTAPIBillingDimensionOutputTokens:
			output = price
		default:
			return input, output, model.ErrZTAPISettlementInvalid
		}
	}
	if _, ok := prices[model.ZTAPIBillingDimensionInputTokens]; !ok || !input.IsPositive() || !output.IsPositive() {
		return input, output, model.ErrZTAPISettlementInvalid
	}
	return input, output, nil
}
