package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
)

type ZTAPIKimiCacheWriteHeaders struct {
	Tokens5m int64 `json:"tokens_5m"`
	Tokens1h int64 `json:"tokens_1h"`
}

// Metering evidence deliberately excludes prompts, output text and credentials.
type ZTAPIMeteringEvidence struct {
	Version                int                              `json:"version"`
	EntryBodyBytes         *int64                           `json:"entry_body_bytes,omitempty"`
	OutboundBodyBytes      *int64                           `json:"outbound_body_bytes,omitempty"`
	Messages               int                              `json:"message_count"`
	Tools                  int                              `json:"tool_count"`
	ToolDefinitionBytes    int                              `json:"tool_definition_json_bytes"`
	ToolsMissingEstimate   bool                             `json:"tools_missing_from_entry_estimate,omitempty"`
	CombinedTextBytes      int                              `json:"token_meta_text_bytes"`
	MaxOutputTokens        int                              `json:"max_output_tokens"`
	EstimatedInputTokens   int                              `json:"entry_estimated_input_tokens"`
	EntryProtocol          string                           `json:"entry_protocol,omitempty"`
	UpstreamProtocol       string                           `json:"upstream_protocol,omitempty"`
	UsageSource            string                           `json:"usage_source,omitempty"`
	UsageLocation          string                           `json:"usage_location,omitempty"`
	UsageObservations      int                              `json:"usage_observations"`
	RawUsage               json.RawMessage                  `json:"raw_numeric_usage,omitempty"`
	RawUsageSHA256         string                           `json:"raw_numeric_usage_sha256,omitempty"`
	RawUsageOmitted        bool                             `json:"raw_numeric_usage_omitted,omitempty"`
	UsageIntegrityComplete bool                             `json:"usage_integrity_complete,omitempty"`
	UsageIntegrityReasons  []string                         `json:"usage_integrity_reasons,omitempty"`
	UnverifiedUsage        *ZTAPIUnverifiedUsageObservation `json:"unverified_usage_observation,omitempty"`
	KimiCacheWriteHeaders  *ZTAPIKimiCacheWriteHeaders      `json:"kimi_cache_write_headers,omitempty"`
}

// Only these fixed diagnostics may cross the metering persistence boundary.
func SanitizeZTAPIUsageIntegrityReasons(reasons []string) []string {
	var safe []string
	for _, allowed := range []string{
		"usage_invalid_json", "usage_invalid_type", "usage_invalid_quantity", "usage_incomplete",
		"usage_alias_conflict", "usage_total_mismatch", "usage_semantic_conflict", "usage_snapshot_conflict",
	} {
		for _, reason := range reasons {
			if reason == allowed {
				safe = append(safe, allowed)
				break
			}
		}
	}
	return safe
}

func (e ZTAPIMeteringEvidence) Sanitized() *ZTAPIMeteringEvidence {
	if e.Version != 1 || e.Messages < 0 || e.Tools < 0 || e.ToolDefinitionBytes < 0 ||
		e.CombinedTextBytes < 0 || e.MaxOutputTokens < 0 || e.EstimatedInputTokens < 0 ||
		e.UsageObservations < 0 || len(e.RawUsage) > 65536 ||
		(e.EntryBodyBytes != nil && *e.EntryBodyBytes < 0) ||
		(e.OutboundBodyBytes != nil && *e.OutboundBodyBytes < 0) {
		return nil
	}
	if e.KimiCacheWriteHeaders != nil {
		counts := *e.KimiCacheWriteHeaders
		if counts.Tokens5m < 0 || counts.Tokens1h < 0 || counts.Tokens5m > math.MaxInt64-counts.Tokens1h {
			return nil
		}
		e.KimiCacheWriteHeaders = &counts
	}
	switch e.UsageSource {
	case "", "upstream_reported", "local_estimate", "missing", "unverified":
	default:
		e.UsageSource = "unverified"
	}
	switch e.UsageLocation {
	case "", "stream", "nonstream":
	default:
		e.UsageLocation = ""
	}
	e.EntryProtocol = safeZTAPIMeteringProtocol(e.EntryProtocol)
	e.UpstreamProtocol = safeZTAPIMeteringProtocol(e.UpstreamProtocol)
	e.UsageIntegrityReasons = SanitizeZTAPIUsageIntegrityReasons(e.UsageIntegrityReasons)
	e.UnverifiedUsage = e.UnverifiedUsage.Sanitized()
	filtered := FilterZTAPINumericUsage(e.RawUsage)
	e.RawUsage, e.RawUsageSHA256 = nil, ""
	if len(filtered) > 0 {
		if encoded, err := common.Marshal(filtered); err == nil && len(encoded) <= 8192 {
			e.RawUsage = encoded
			hash := sha256.Sum256(encoded)
			e.RawUsageSHA256 = hex.EncodeToString(hash[:])
		} else {
			e.RawUsageOmitted = true
		}
	}
	return &e
}

func safeZTAPIMeteringProtocol(value string) string {
	switch RelayFormat(value) {
	case RelayFormatOpenAI, RelayFormatClaude, RelayFormatGemini, RelayFormatOpenAIResponses,
		RelayFormatOpenAIResponsesCompaction, RelayFormatOpenAIAudio, RelayFormatOpenAIRealtime:
		return value
	default:
		return ""
	}
}

func FilterZTAPINumericUsage(raw json.RawMessage) map[string]json.RawMessage {
	return filterZTAPINumericUsage(raw, 0)
}

func filterZTAPINumericUsage(raw json.RawMessage, depth int) map[string]json.RawMessage {
	var values map[string]json.RawMessage
	if len(raw) > 65536 || common.Unmarshal(raw, &values) != nil {
		return nil
	}
	safe := make(map[string]json.RawMessage)
	for name, value := range values {
		switch name {
		case "prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens",
			"cached_tokens", "cached_creation_tokens", "cache_write_tokens", "cache_read_input_tokens", "cache_creation_input_tokens",
			"prompt_cache_hit_tokens", "prompt_cache_miss_tokens", "reasoning_tokens", "text_tokens", "image_tokens", "audio_tokens",
			"accepted_prediction_tokens", "rejected_prediction_tokens", "claude_cache_creation_5_m_tokens", "claude_cache_creation_1_h_tokens",
			"ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens":
			if len(value) <= 64 && gjson.ParseBytes(value).Type == gjson.Number {
				safe[name] = value
			}
		case "prompt_tokens_details", "completion_tokens_details", "input_tokens_details", "output_tokens_details", "cache_creation":
			if depth != 0 {
				continue
			}
			if nested := filterZTAPINumericUsage(value, depth+1); len(nested) > 0 {
				if encoded, err := common.Marshal(nested); err == nil {
					safe[name] = encoded
				}
			}
		}
	}
	return safe
}
