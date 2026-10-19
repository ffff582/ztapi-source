package common

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const usageIntegrityReasonsKey = "ztapi_usage_integrity_reasons"
const usageIntegrityCompleteKey = "ztapi_usage_integrity_complete"

func RecordZTAPIUsageIntegrityFailure(c *gin.Context, info *RelayInfo, reason string) {
	if len(types.SanitizeZTAPIUsageIntegrityReasons([]string{reason})) == 0 {
		return
	}
	evidence := ensureZTAPIMetering(info)
	if evidence == nil {
		return
	}
	evidence.UsageIntegrityReasons = types.SanitizeZTAPIUsageIntegrityReasons(append(evidence.UsageIntegrityReasons, reason))
	if c != nil {
		previous, _ := c.Get(usageIntegrityReasonsKey)
		reasons, _ := previous.([]string)
		c.Set(usageIntegrityReasonsKey, types.SanitizeZTAPIUsageIntegrityReasons(append(reasons, reason)))
	}
}

// ObserveZTAPIOpenAIUsage validates raw presence before dto.Usage erases absent
// versus explicit zero fields. Call only for managed Chat/Responses upstreams,
// never for native Anthropic. Invalid observations are request-sticky.
func ObserveZTAPIOpenAIUsage(c *gin.Context, info *RelayInfo, raw []byte) *dto.Usage {
	return observeZTAPIOpenAIUsage(c, info, raw, nil)
}

func observeZTAPIOpenAIUsage(c *gin.Context, info *RelayInfo, raw []byte, chatHeader http.Header) *dto.Usage {
	if ensureZTAPIMetering(info) == nil {
		return nil
	}
	invalid := false
	var observation *types.ZTAPIUnverifiedUsageObservation
	fail := func(reason string) {
		invalid = true
		RecordZTAPIUsageIntegrityFailure(c, info, reason)
		previous := info.ZTAPIMetering.UnverifiedUsage
		if observation != nil && (previous == nil || len(types.SanitizeZTAPIUsageIntegrityReasons([]string{previous.Reason})) == 0) {
			copy := *observation
			copy.Reason = reason
			info.ZTAPIMetering.UnverifiedUsage = copy.Sanitized()
		}
	}
	var envelope map[string]json.RawMessage
	if basecommon.Unmarshal(raw, &envelope) != nil {
		fail("usage_invalid_json")
		return nil
	}
	duplicates, err := basecommon.FindDuplicateJsonObjectMembers(bytes.NewReader(raw))
	if err != nil {
		fail("usage_invalid_json")
		return nil
	}
	for _, duplicate := range duplicates {
		path := duplicate.Path
		if len(path) > 0 && (path[0] == "usage" ||
			(path[0] == "response" && (len(path) == 1 || path[1] == "usage"))) {
			fail("usage_invalid_json")
			return nil
		}
	}
	usageRaw := envelope["usage"]
	if len(usageRaw) == 0 && len(envelope["response"]) > 0 {
		var response map[string]json.RawMessage
		if basecommon.Unmarshal(envelope["response"], &response) != nil {
			fail("usage_invalid_json")
			return nil
		}
		usageRaw = response["usage"]
	}
	if len(usageRaw) == 0 || string(usageRaw) == "null" {
		return nil
	}
	var values map[string]json.RawMessage
	if basecommon.Unmarshal(usageRaw, &values) != nil {
		fail("usage_invalid_type")
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	location := "nonstream"
	if info.IsStream {
		location = "stream"
	}
	observation = (&types.ZTAPIUnverifiedUsageObservation{Location: location, RawUsage: usageRaw}).Sanitized()
	if observation != nil && info.ZTAPIMetering.UnverifiedUsage == nil {
		info.ZTAPIMetering.UnverifiedUsage = observation
	}
	numbers := make(map[string]int64)
	var readNumbers func(map[string]json.RawMessage, string)
	readNumbers = func(fields map[string]json.RawMessage, prefix string) {
		for name, value := range fields {
			path := prefix + name
			switch name {
			case "prompt_tokens_details", "input_tokens_details", "completion_tokens_details", "output_tokens_details", "cache_creation":
				if prefix != "" {
					fail("usage_incomplete")
					continue
				}
				if string(value) == "null" {
					if name == "cache_creation" {
						fail("usage_invalid_type")
					}
					continue
				}
				var nested map[string]json.RawMessage
				if basecommon.Unmarshal(value, &nested) != nil {
					fail("usage_invalid_type")
					continue
				}
				readNumbers(nested, path+".")
			case "prompt_tokens", "completion_tokens", "input_tokens", "output_tokens", "total_tokens",
				"cached_tokens", "cached_creation_tokens", "cache_write_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens",
				"reasoning_tokens", "text_tokens", "image_tokens", "audio_tokens", "accepted_prediction_tokens", "rejected_prediction_tokens",
				"claude_cache_creation_5_m_tokens", "claude_cache_creation_1_h_tokens", "ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens",
				"cache_write_5m_tokens", "cache_write_1h_tokens":
				if strings.Contains(name, "cache") || strings.HasPrefix(name, "ephemeral_") {
					switch path {
					case "cached_tokens", "cached_creation_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens",
						"prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens",
						"prompt_tokens_details.cached_creation_tokens", "input_tokens_details.cached_creation_tokens",
						"prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_write_tokens",
						"claude_cache_creation_5_m_tokens", "claude_cache_creation_1_h_tokens",
						"cache_write_5m_tokens", "cache_write_1h_tokens",
						"cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens":
					default:
						fail("usage_incomplete")
					}
				}
				number, err := strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
				if err != nil {
					if parseErr, ok := err.(*strconv.NumError); ok && parseErr.Err == strconv.ErrRange {
						fail("usage_invalid_quantity")
					} else {
						fail("usage_invalid_type")
					}
					continue
				}
				if number < 0 || number > math.MaxInt64/8 || number > int64(math.MaxInt) {
					fail("usage_invalid_quantity")
					continue
				}
				numbers[path] = number
			case "cache_read_input_tokens", "cache_creation_input_tokens":
				// Native Messages counts exclude caches from input_tokens. They
				// cannot establish inclusive Chat/Responses accounting.
				fail("usage_semantic_conflict")
			default:
				if strings.Contains(name, "cache") || prefix == "cache_creation." {
					fail("usage_incomplete")
				}
			}
		}
	}
	readNumbers(values, "")
	var usage dto.Usage
	if basecommon.Unmarshal(usageRaw, &usage) != nil {
		fail("usage_invalid_type")
		return nil
	}
	if usage.UsageSemantic != "" && usage.UsageSemantic != "openai" {
		fail("usage_semantic_conflict")
	}
	if len(numbers) == 0 {
		return nil
	}
	alias := func(names ...string) (int64, bool) {
		var value int64
		found := false
		for _, name := range names {
			if other, ok := numbers[name]; ok {
				if found && value != other {
					fail("usage_alias_conflict")
				} else {
					value, found = other, true
				}
			}
		}
		return value, found
	}
	input, hasInput := alias("prompt_tokens", "input_tokens")
	output, hasOutput := alias("completion_tokens", "output_tokens")
	cacheRead, hasRead := alias("prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens", "prompt_cache_hit_tokens", "cached_tokens")
	// Kimi Chat/Responses documents cache_write_tokens as an inclusive input
	// bucket; normalize it to the existing generic write counter, not a new fee.
	cacheWrite, hasWrite := alias("prompt_tokens_details.cached_creation_tokens", "input_tokens_details.cached_creation_tokens",
		"prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_write_tokens", "cached_creation_tokens")
	write5m, has5m := alias("claude_cache_creation_5_m_tokens", "cache_creation.ephemeral_5m_input_tokens", "cache_write_5m_tokens")
	write1h, has1h := alias("claude_cache_creation_1_h_tokens", "cache_creation.ephemeral_1h_input_tokens", "cache_write_1h_tokens")
	_, pool5m := numbers["cache_write_5m_tokens"]
	_, pool1h := numbers["cache_write_1h_tokens"]
	strictCachePresence := ztapiIsKimiK3(info)
	if strictCachePresence {
		header5m, header1h, present, reason := parseZTAPIKimiCacheWriteHeaders(chatHeader)
		if reason != "" {
			fail(reason)
		} else if present {
			if !hasWrite {
				fail("usage_incomplete")
			} else if cacheWrite != header5m+header1h {
				fail("usage_alias_conflict")
			}
			if (has5m && write5m != header5m) || (has1h && write1h != header1h) {
				fail("usage_alias_conflict")
			}
			if !invalid {
				write5m, write1h = header5m, header1h
				has5m, has1h = true, true
			}
		}
	}
	if (pool5m || pool1h) && has5m != has1h {
		fail("usage_incomplete")
	}
	// Frozen prices do not establish a presence contract for legacy suppliers.
	// Only the documented K3 version currently requires complete cache counters.
	if strictCachePresence {
		if has5m != has1h {
			fail("usage_incomplete")
		}
		for _, dimension := range info.ZTAPIPublicationSnapshot.BillingDimensions {
			if (dimension == "cache_read" && !hasRead) ||
				((dimension == "cache_write" || dimension == "cache_write_5m" || dimension == "cache_write_1h") && !hasWrite && !has5m && !has1h) {
				fail("usage_incomplete")
			}
		}
	}
	for _, name := range []string{"image_tokens", "audio_tokens", "text_tokens"} {
		alias("prompt_tokens_details."+name, "input_tokens_details."+name)
	}
	for _, name := range []string{"reasoning_tokens", "image_tokens", "audio_tokens", "text_tokens"} {
		alias("completion_tokens_details."+name, "output_tokens_details."+name)
	}
	if !hasInput || !hasOutput {
		fail("usage_incomplete")
	}
	if has5m || has1h {
		if hasWrite && cacheWrite < write5m+write1h {
			fail("usage_alias_conflict")
			return nil
		}
		if !hasWrite {
			cacheWrite = write5m + write1h
		}
	}
	if invalid {
		return nil
	}
	if miss, ok := numbers["prompt_cache_miss_tokens"]; ok {
		if !hasRead {
			fail("usage_incomplete")
		} else if miss+cacheRead != input {
			fail("usage_total_mismatch")
		}
		if invalid {
			return nil
		}
	}
	if total, ok := numbers["total_tokens"]; ok && total != input+output {
		fail("usage_total_mismatch")
		return nil
	}
	usage.PromptTokens, usage.CompletionTokens = int(input), int(output)
	usage.TotalTokens = int(input + output)
	usage.PromptTokensDetails.CachedTokens = int(cacheRead)
	usage.PromptTokensDetails.CachedCreationTokens = int(cacheWrite)
	usage.ClaudeCacheCreation5mTokens, usage.ClaudeCacheCreation1hTokens = int(write5m), int(write1h)
	usage.UsageSemantic, usage.UsageSource = "openai", "upstream_reported"
	if value, ok := values["output_tokens_details"]; ok && len(values["completion_tokens_details"]) == 0 {
		_ = basecommon.Unmarshal(value, &usage.CompletionTokenDetails)
	}
	check := ValidateZTAPIUsageDimensions(nil, &RelayInfo{ZTAPIPublicationSnapshot: info.ZTAPIPublicationSnapshot}, &usage)
	for _, dimension := range check.InvalidDimensions {
		if dimension != "usage_zero_unconfirmed" {
			fail("usage_invalid_quantity")
		}
	}
	current := map[string]int64{
		"input": input, "output": output, "cache_read": cacheRead,
		"cache_write": cacheWrite, "cache_write_5m": write5m, "cache_write_1h": write1h,
		"input_uncached":          input - cacheRead - cacheWrite,
		"cache_write_unspecified": cacheWrite - write5m - write1h,
	}
	for name, previous := range info.ZTAPIUsageLastSnapshot {
		if current[name] < previous {
			fail("usage_snapshot_conflict")
		}
	}
	if invalid {
		return nil
	}
	info.ZTAPIUsageLastSnapshot = current
	info.ZTAPIMetering.UsageIntegrityComplete = true
	if c != nil {
		c.Set(usageIntegrityCompleteKey, true)
	}
	return &usage
}
