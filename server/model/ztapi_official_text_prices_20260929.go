package model

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Frozen first-party prices in USD per million tokens, observed 2026-09-29.
// GPT 5.6 Sol's promotional price must be reviewed before its 2026-11-21 expiry.
type ztapiOfficialTextPrice struct {
	URL   string
	Tiers []map[string]string
}

var ztapiOfficialTextPrices20260929 = map[string]ztapiOfficialTextPrice{
	"gpt-6-astra": {"https://developers.openai.com/api/docs/models/gpt-6-astra", []map[string]string{
		{"input_tokens": "10", "cache_read": "1", "cache_write": "12.5", "output_tokens": "50"},
		{"input_tokens": "20", "cache_read": "2", "cache_write": "25", "output_tokens": "75"},
	}},
	"gpt-5.6-sol": {"https://developers.openai.com/api/docs/models/gpt-5.6-sol", []map[string]string{
		{"input_tokens": "4", "cache_read": "0.4", "cache_write": "5", "output_tokens": "20"},
		{"input_tokens": "8", "cache_read": "0.8", "cache_write": "10", "output_tokens": "30"},
	}},
	"gpt-5.6-terra": {"https://developers.openai.com/api/docs/models/gpt-5.6-terra", []map[string]string{
		{"input_tokens": "2", "cache_read": "0.2", "cache_write": "2.5", "output_tokens": "12"},
		{"input_tokens": "4", "cache_read": "0.4", "cache_write": "5", "output_tokens": "18"},
	}},
	"gpt-5.6-luna": {"https://developers.openai.com/api/docs/models/gpt-5.6-luna", []map[string]string{
		{"input_tokens": "0.2", "cache_read": "0.02", "cache_write": "0.25", "output_tokens": "1.2"},
		{"input_tokens": "0.4", "cache_read": "0.04", "cache_write": "0.5", "output_tokens": "1.8"},
	}},
	"gpt-5.5": {"https://developers.openai.com/api/docs/models/gpt-5.5", []map[string]string{
		{"input_tokens": "5", "cache_read": "0.5", "output_tokens": "30"},
		{"input_tokens": "10", "cache_read": "1", "output_tokens": "45"},
	}},
	"gpt-5.4": {"https://developers.openai.com/api/docs/models/gpt-5.4", []map[string]string{
		{"input_tokens": "2.5", "cache_read": "0.25", "output_tokens": "15"},
		{"input_tokens": "5", "cache_read": "0.5", "output_tokens": "22.5"},
	}},
	"claude-fable-5":            {"https://platform.claude.com/docs/en/about-claude/pricing", []map[string]string{{"input_tokens": "10", "cache_read": "1", "cache_write_5m": "12.5", "cache_write_1h": "20", "output_tokens": "50"}}},
	"claude-opus-5":             {"https://platform.claude.com/docs/en/about-claude/pricing", []map[string]string{{"input_tokens": "5", "cache_read": "0.5", "cache_write_5m": "6.25", "cache_write_1h": "10", "output_tokens": "25"}}},
	"claude-opus-4-8":           {"https://platform.claude.com/docs/en/about-claude/pricing", []map[string]string{{"input_tokens": "5", "cache_read": "0.5", "cache_write_5m": "6.25", "cache_write_1h": "10", "output_tokens": "25"}}},
	"claude-opus-4-7":           {"https://platform.claude.com/docs/en/about-claude/pricing", []map[string]string{{"input_tokens": "5", "cache_read": "0.5", "cache_write_5m": "6.25", "cache_write_1h": "10", "output_tokens": "25"}}},
	"claude-opus-4-6":           {"https://platform.claude.com/docs/en/about-claude/pricing", []map[string]string{{"input_tokens": "5", "cache_read": "0.5", "cache_write_5m": "6.25", "cache_write_1h": "10", "output_tokens": "25"}}},
	"claude-sonnet-5":           {"https://platform.claude.com/docs/en/about-claude/pricing", []map[string]string{{"input_tokens": "2", "cache_read": "0.2", "cache_write_5m": "2.5", "cache_write_1h": "4", "output_tokens": "10"}}},
	"claude-sonnet-4-6":         {"https://platform.claude.com/docs/en/about-claude/pricing", []map[string]string{{"input_tokens": "3", "cache_read": "0.3", "cache_write_5m": "3.75", "cache_write_1h": "6", "output_tokens": "15"}}},
	"claude-haiku-4-5-20251001": {"https://platform.claude.com/docs/en/about-claude/pricing", []map[string]string{{"input_tokens": "1", "cache_read": "0.1", "cache_write_5m": "1.25", "cache_write_1h": "2", "output_tokens": "5"}}},
}

func ztapiOfficial78Sale(sourceModel string, tier int, dimension string) (decimal.Decimal, error) {
	price, ok := ztapiOfficialTextPrices20260929[sourceModel]
	if !ok || tier >= len(price.Tiers) || tier < 0 {
		return decimal.Zero, fmt.Errorf("no frozen first-party price for %s tier %d", sourceModel, tier)
	}
	raw, ok := price.Tiers[tier][dimension]
	if !ok {
		return decimal.Zero, fmt.Errorf("no first-party %s price for %s", dimension, sourceModel)
	}
	value, err := decimal.NewFromString(raw)
	if err != nil || !value.IsPositive() {
		return decimal.Zero, fmt.Errorf("invalid frozen first-party price for %s", dimension)
	}
	return value.Mul(decimal.RequireFromString("0.78")).Round(10), nil
}
