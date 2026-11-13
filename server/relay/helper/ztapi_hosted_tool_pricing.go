package helper

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

var errZTAPIHostedToolBilling = errors.New("hosted tool billing contract is unavailable before dispatch")

// Check the final converted body, including overrides. A declaration is not a
// billable call, but an unpriced or unmetered provider tool cannot be dispatched.
func ValidateZTAPIHostedToolPricing(info *relaycommon.RelayInfo, body []byte) *types.NewAPIError {
	if info == nil || info.ZTAPIPublicationSnapshot == nil ||
		(info.ZTAPIPublicationSnapshot.Modality != "" && info.ZTAPIPublicationSnapshot.Modality != "text") {
		return nil
	}
	var root map[string]json.RawMessage
	if common.Unmarshal(body, &root) != nil {
		return ztapiHostedToolBillingError()
	}
	for name := range root {
		if strings.EqualFold(name, "tools") && name != "tools" || strings.EqualFold(name, "web_search_options") && name != "web_search_options" {
			return ztapiHostedToolBillingError()
		}
	}
	if info.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
		for _, field := range []string{"tools", "system", "messages"} {
			if validateZTAPIExplicitCachePricing(info, root[field], 0) != nil {
				return ztapiHostedToolBillingError()
			}
		}
	}
	// Chat search options do not carry a supported per-call receipt here.
	if raw, ok := root["web_search_options"]; ok && string(bytes.TrimSpace(raw)) != "null" {
		return ztapiHostedToolBillingError()
	}
	if err := validateZTAPIHostedToolList(info, root["tools"], 0); err != nil {
		return ztapiHostedToolBillingError()
	}
	if input := bytes.TrimSpace(root["input"]); len(input) > 0 && input[0] == '[' {
		var items []map[string]json.RawMessage
		if common.Unmarshal(input, &items) != nil {
			return ztapiHostedToolBillingError()
		}
		for _, item := range items {
			for field := range item {
				if strings.EqualFold(field, "type") && field != "type" || strings.EqualFold(field, "tools") && field != "tools" {
					return ztapiHostedToolBillingError()
				}
			}
			if ztapiHostedRawString(item["type"]) == "additional_tools" {
				if err := validateZTAPIHostedToolList(info, item["tools"], 0); err != nil {
					return ztapiHostedToolBillingError()
				}
			}
		}
	}
	return nil
}

func ztapiHostedToolBillingError() *types.NewAPIError {
	return types.NewErrorWithStatusCode(errZTAPIHostedToolBilling, types.ErrorCodeHostedToolBillingUnavailable, http.StatusBadRequest,
		types.ErrOptionWithLocalOrigin(), types.ErrOptionWithSkipRetry())
}

// Inspect executable annotations, never client function schemas or text.
func validateZTAPIExplicitCachePricing(info *relaycommon.RelayInfo, raw json.RawMessage, depth int) error {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || (value[0] != '[' && value[0] != '{') {
		return nil
	}
	if depth > 16 {
		return errZTAPIHostedToolBilling
	}
	if value[0] == '[' {
		var items []json.RawMessage
		if common.Unmarshal(value, &items) != nil {
			return errZTAPIHostedToolBilling
		}
		for _, item := range items {
			if err := validateZTAPIExplicitCachePricing(info, item, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	var node map[string]json.RawMessage
	if common.Unmarshal(value, &node) != nil {
		return errZTAPIHostedToolBilling
	}
	if policy, exists := node["cache_control"]; exists && string(bytes.TrimSpace(policy)) != "null" {
		var control struct {
			Type string `json:"type"`
			TTL  string `json:"ttl"`
		}
		if common.Unmarshal(policy, &control) != nil || control.Type != "ephemeral" || (control.TTL != "" && control.TTL != "5m" && control.TTL != "1h") {
			return errZTAPIHostedToolBilling
		}
		dimension, tiered := "cache_write_5m", false
		if control.TTL == "1h" {
			dimension = "cache_write_1h"
		}
		for _, name := range info.ZTAPIPublicationSnapshot.BillingDimensions {
			tiered = tiered || name == "cache_write_5m" || name == "cache_write_1h"
		}
		if !tiered {
			dimension = "cache_write"
		}
		if !ztapiHostedToolHasFrozenPrice(info.ZTAPIPublicationSnapshot, dimension) {
			return errZTAPIHostedToolBilling
		}
	}
	return validateZTAPIExplicitCachePricing(info, node["content"], depth+1)
}

func ztapiHostedRawString(raw json.RawMessage) string {
	var value string
	_ = common.Unmarshal(raw, &value)
	return value
}

func validateZTAPIHostedToolList(info *relaycommon.RelayInfo, raw json.RawMessage, depth int) error {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	var tools []map[string]json.RawMessage
	if depth > 2 || common.Unmarshal(raw, &tools) != nil || len(tools) > 256 {
		return errZTAPIHostedToolBilling
	}
	for _, tool := range tools {
		if tool == nil {
			return errZTAPIHostedToolBilling
		}
		for field := range tool {
			if strings.EqualFold(field, "type") && field != "type" || strings.EqualFold(field, "tools") && field != "tools" {
				return errZTAPIHostedToolBilling
			}
		}
		kind := ztapiHostedRawString(tool["type"])
		switch {
		case kind == "function" || kind == "custom":
			continue
		case kind == "namespace":
			if err := validateZTAPIHostedToolList(info, tool["tools"], depth+1); err != nil {
				return err
			}
			continue
		case info.GetFinalRequestRelayFormat() == types.RelayFormatClaude && ztapiClaudeClientTool(kind):
			// These declarations request execution by the client, not a provider sandbox.
			continue
		case kind == "":
			if _, ok := tool["functionDeclarations"]; ok && len(tool) == 1 && info.GetFinalRequestRelayFormat() == types.RelayFormatGemini {
				continue
			}
			if _, ok := tool["function_declarations"]; ok && len(tool) == 1 && info.GetFinalRequestRelayFormat() == types.RelayFormatGemini {
				continue
			}
			if _, ok := tool["input_schema"]; ok && len(tool["name"]) > 0 && info.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
				continue
			}
		}
		dimension := kind
		metered := info.GetFinalRequestRelayFormat() == types.RelayFormatOpenAIResponses &&
			(kind == "web_search" || kind == "web_search_preview" || kind == "file_search")
		if strings.HasPrefix(kind, "web_search_") && kind != "web_search_preview" && info.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
			dimension, metered = "web_search", true
		}
		if !metered || !ztapiHostedToolHasFrozenPrice(info.ZTAPIPublicationSnapshot, dimension) {
			return errZTAPIHostedToolBilling
		}
	}
	return nil
}

func ztapiClaudeClientTool(kind string) bool {
	for _, prefix := range []string{"bash_", "text_editor_", "computer_", "memory_"} {
		if !strings.HasPrefix(kind, prefix) {
			continue
		}
		version := strings.TrimPrefix(kind, prefix)
		if len(version) != 8 {
			return false
		}
		for _, c := range version {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func ztapiHostedToolHasFrozenPrice(snapshot *relaycommon.ZTAPIPublicationSnapshot, name string) bool {
	if snapshot.PriceSourceID <= 0 || snapshot.PriceSourceVersion == 0 {
		return false
	}
	listed := false
	for _, dimension := range snapshot.BillingDimensions {
		listed = listed || dimension == name
	}
	validPrice := func(raw string) bool {
		if len(raw) == 0 || len(raw) > 64 || strings.ContainsAny(raw, "eE") {
			return false
		}
		price, err := decimal.NewFromString(raw)
		return err == nil && !price.IsNegative()
	}
	if !listed || !validPrice(snapshot.SaleUSD[name]) {
		return false
	}
	if strings.TrimSpace(snapshot.TokenPriceRulesJSON) != "" {
		var tiers []struct {
			Sale map[string]string `json:"sale"`
		}
		if common.UnmarshalJsonStr(snapshot.TokenPriceRulesJSON, &tiers) != nil || len(tiers) == 0 {
			return false
		}
		for _, tier := range tiers {
			if !validPrice(tier.Sale[name]) {
				return false
			}
		}
	}
	return true
}
