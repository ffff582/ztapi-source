package helper

import (
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPIHostedToolPricingStopsUnquotedProviderTools(t *testing.T) {
	for _, raw := range []string{
		`{"tools":[{"type":"web_search"}]}`,
		`{"Tools":[{"type":"web_search"}]}`,
		`{"TOOLS":[{"type":"web_search"}]}`,
		`{"tools":[{"type":"web_search_preview"}]}`,
		`{"tools":[{"type":"file_search","vector_store_ids":["vs_fixture"]}]}`,
		`{"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`,
		`{"tools":[{"type":"computer_use_preview"}]}`,
		`{"tools":[{"type":"image_generation"}]}`,
		`{"tools":[{"type":"mcp","server_label":"external","server_url":"https://example.com"}]}`,
		`{"tools":[{"type":"namespace","name":"remote","tools":[{"type":"web_search"}]}]}`,
		`{"input":[{"type":"additional_tools","tools":[{"type":"web_search"}]}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
		`{"tools":[{"googleSearch":{}}]}`,
		`{"tools":[{"google_search_retrieval":{}}]}`,
		`{"tools":[{"functionDeclarations":[{"name":"read"}],"googleSearch":{}}]}`,
		`{"input":[{"Type":"additional_tools","tools":[{"type":"web_search"}]}]}`,
		`{"\u0074ools":[{"type":"web_search"}]}`,
		`{"web_search_options":{}}`,
	} {
		info := &relaycommon.RelayInfo{FinalRequestRelayFormat: types.RelayFormatOpenAIResponses, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			Modality: "text", PriceSourceID: 1, PriceSourceVersion: 1,
			BillingDimensions: []string{"input_tokens", "output_tokens"},
			SaleUSD:           map[string]string{"input_tokens": "1", "output_tokens": "2"},
		}}
		apiErr := ValidateZTAPIHostedToolPricing(info, []byte(raw))
		require.NotNil(t, apiErr, raw)
		require.Equal(t, 400, apiErr.StatusCode)
		public := apiErr.ToPublicOpenAIError()
		require.Contains(t, public.Message, "not sent upstream")
		require.NotContains(t, public.Message, "example.com")
		require.NotContains(t, public.Message, "vs_fixture")
	}
}

func TestZTAPIHostedToolPricingPreservesClientToolsAndPlainRequests(t *testing.T) {
	for _, raw := range []string{
		`{"input":"hi"}`,
		`{"tools":[{"type":"function","name":"web_search","parameters":{"type":"object"}}]}`,
		`{"tools":[{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}`,
		`{"tools":[{"type":"namespace","name":"local","tools":[{"type":"function","name":"read"}]}]}`,
		`{"tools":[{"name":"WebSearch","input_schema":{"type":"object"}}]}`,
		`{"tools":[{"functionDeclarations":[{"name":"read"}]}]}`,
		`{"tools":[{"type":"bash_20250124","name":"bash"}]}`,
		`{"tools":[{"type":"text_editor_20250124","name":"str_replace_editor"}]}`,
		`{"input":[{"type":"web_search_call","id":"ws_historical","status":"completed"}]}`,
	} {
		format := types.RelayFormat(types.RelayFormatOpenAIResponses)
		if strings.Contains(raw, "input_schema") || strings.Contains(raw, "bash_") || strings.Contains(raw, "text_editor_") {
			format = types.RelayFormatClaude
		}
		if strings.Contains(raw, "functionDeclarations") {
			format = types.RelayFormatGemini
		}
		info := &relaycommon.RelayInfo{FinalRequestRelayFormat: format, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "text"}}
		require.Nil(t, ValidateZTAPIHostedToolPricing(info, []byte(raw)), raw)
	}
	// This contract does not change legacy billing or media contracts.
	require.Nil(t, ValidateZTAPIHostedToolPricing(&relaycommon.RelayInfo{}, []byte(`{"tools":[{"type":"web_search"}]}`)))
}

func TestZTAPIHostedToolPricingRequiresExplicitFrozenPricesInEveryTier(t *testing.T) {
	info := &relaycommon.RelayInfo{FinalRequestRelayFormat: types.RelayFormatOpenAIResponses, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
		Modality: "text", PriceSourceID: 2, PriceSourceVersion: 3,
		BillingDimensions: []string{"input_tokens", "web_search"},
		SaleUSD:           map[string]string{"input_tokens": "1", "web_search": "0.01"},
	}}
	raw := []byte(`{"tools":[{"type":"web_search"}]}`)
	require.Nil(t, ValidateZTAPIHostedToolPricing(info, raw))
	info.ZTAPIPublicationSnapshot.TokenPriceRulesJSON = `[{"conditions":["fixture"],"sale":{"input_tokens":"1"}}]`
	require.NotNil(t, ValidateZTAPIHostedToolPricing(info, raw), "a tool price absent from a possible frozen tier is not authorized")
	info.ZTAPIPublicationSnapshot.TokenPriceRulesJSON = `[{"conditions":["fixture"],"sale":{"input_tokens":"1","web_search":"0"}}]`
	require.Nil(t, ValidateZTAPIHostedToolPricing(info, raw), "explicitly quoted free is distinct from absent")
	for _, value := range []string{"", "-1", "https://example.com/private", "1e1000000000", strings.Repeat("1", 1000)} {
		info.ZTAPIPublicationSnapshot.TokenPriceRulesJSON = ""
		info.ZTAPIPublicationSnapshot.SaleUSD["web_search"] = value
		require.NotNil(t, ValidateZTAPIHostedToolPricing(info, raw))
	}
}

func TestZTAPIExplicitCachePolicyRequiresFrozenWritePrice(t *testing.T) {
	info := &relaycommon.RelayInfo{FinalRequestRelayFormat: types.RelayFormatClaude, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
		Modality: "text", PriceSourceID: 2, PriceSourceVersion: 3,
		BillingDimensions: []string{"input_tokens", "output_tokens", "cache_write_5m"},
		SaleUSD:           map[string]string{"input_tokens": "1", "output_tokens": "2", "cache_write_5m": "1.25"},
	}}
	short := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`)
	long := []byte(strings.Replace(string(short), `"type":"ephemeral"`, `"type":"ephemeral","ttl":"1h"`, 1))
	require.Nil(t, ValidateZTAPIHostedToolPricing(info, short))
	require.NotNil(t, ValidateZTAPIHostedToolPricing(info, long), "positive legacy ratios are not an approved 1h quote")
	info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "cache_write_1h")
	info.ZTAPIPublicationSnapshot.SaleUSD["cache_write_1h"] = "2"
	require.Nil(t, ValidateZTAPIHostedToolPricing(info, long))
	info.ZTAPIPublicationSnapshot.BillingDimensions = []string{"input_tokens", "output_tokens", "cache_write"}
	info.ZTAPIPublicationSnapshot.SaleUSD["cache_write"] = "2"
	require.Nil(t, ValidateZTAPIHostedToolPricing(info, long), "published unified cache pricing stays supported")
	// A client's function schema is not an executable cache policy.
	require.Nil(t, ValidateZTAPIHostedToolPricing(info, []byte(`{"tools":[{"name":"cache","input_schema":{"properties":{"cache_control":{"type":"object"}}}}]}`)))
}
