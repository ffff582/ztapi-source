package gemini

import (
	"encoding/json"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func geminiUsageMetadataJSON(info *relaycommon.RelayInfo, raw []byte) json.RawMessage {
	if info.ZTAPIPublicationSnapshot == nil {
		return nil
	}
	var frame struct {
		Usage json.RawMessage `json:"usageMetadata"`
	}
	if common.Unmarshal(raw, &frame) != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(frame.Usage, &fields) != nil || len(fields) == 0 {
		return nil
	}
	for _, name := range []string{"promptTokenCount", "toolUsePromptTokenCount", "candidatesTokenCount",
		"thoughtsTokenCount", "totalTokenCount", "cachedContentTokenCount", "promptTokensDetails",
		"toolUsePromptTokensDetails", "candidatesTokensDetails"} {
		if _, exists := fields[name]; exists {
			return frame.Usage
		}
	}
	return nil
}

func geminiResponseUsage(c *gin.Context, info *relaycommon.RelayInfo, metadata dto.GeminiUsageMetadata, rawMetadata json.RawMessage, location string, final bool) dto.Usage {
	if info.ZTAPIPublicationSnapshot == nil {
		return buildUsageFromGeminiMetadata(metadata, info.GetEstimatePromptTokens())
	}
	usage := buildUsageFromGeminiMetadata(metadata, 0)
	// A derived total difference or local estimate is not a supplier receipt.
	usage.CompletionTokens = metadata.CandidatesTokenCount + metadata.ThoughtsTokenCount
	usage.UsageSemantic, usage.UsageSource = "openai", "upstream_incomplete"
	var fields struct {
		Prompt     *int `json:"promptTokenCount"`
		ToolPrompt *int `json:"toolUsePromptTokenCount"`
		Completion *int `json:"candidatesTokenCount"`
		Thoughts   *int `json:"thoughtsTokenCount"`
		Total      *int `json:"totalTokenCount"`
	}
	if len(rawMetadata) == 0 {
		if final {
			relaycommon.RecordZTAPIUsageIntegrityFailure(c, info, "usage_incomplete")
		}
		return usage
	}
	if len(rawMetadata) > 65536 || common.Unmarshal(rawMetadata, &fields) != nil {
		relaycommon.RecordZTAPIUsageIntegrityFailure(c, info, "usage_incomplete")
		return usage
	}
	reason := ""
	complete := fields.Prompt != nil && fields.Completion != nil && fields.Total != nil
	// Partial intermediate frames are normal; invalid quantities are not.
	if !complete && final {
		reason = "usage_incomplete"
		relaycommon.RecordZTAPIUsageIntegrityFailure(c, info, reason)
	}
	for _, count := range []int{metadata.PromptTokenCount, metadata.ToolUsePromptTokenCount,
		metadata.CandidatesTokenCount, metadata.ThoughtsTokenCount, metadata.TotalTokenCount, metadata.CachedContentTokenCount} {
		if count < 0 || int64(count) > math.MaxInt64/8 {
			complete = false
			reason = "usage_invalid_quantity"
			relaycommon.RecordZTAPIUsageIntegrityFailure(c, info, reason)
		}
	}
	if complete && usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		complete = false
		reason = "usage_total_mismatch"
		relaycommon.RecordZTAPIUsageIntegrityFailure(c, info, reason)
	}
	if complete {
		usage.UsageSource = "upstream_reported"
	}
	// Persist only normalized numeric fields actually reported in usageMetadata.
	numeric := make(map[string]any)
	if fields.Prompt != nil || fields.ToolPrompt != nil {
		numeric["prompt_tokens"] = usage.PromptTokens
	}
	if fields.Completion != nil || fields.Thoughts != nil {
		numeric["completion_tokens"] = usage.CompletionTokens
	}
	if fields.Total != nil {
		numeric["total_tokens"] = usage.TotalTokens
	}
	if metadata.CachedContentTokenCount != 0 || len(metadata.PromptTokensDetails) != 0 || len(metadata.ToolUsePromptTokensDetails) != 0 {
		numeric["prompt_tokens_details"] = usage.PromptTokensDetails
	}
	if metadata.ThoughtsTokenCount != 0 || len(metadata.CandidatesTokensDetails) != 0 {
		numeric["completion_tokens_details"] = usage.CompletionTokenDetails
	}
	if len(numeric) != 0 {
		if reason != "" && info.ZTAPIMetering != nil && info.ZTAPIMetering.UnverifiedUsage == nil {
			if raw, err := common.Marshal(numeric); err == nil {
				info.ZTAPIMetering.UnverifiedUsage = (&types.ZTAPIUnverifiedUsageObservation{
					Location: location, Reason: reason, RawUsage: raw,
				}).Sanitized()
			}
		}
		if final {
			if raw, err := common.Marshal(map[string]any{"usage": numeric}); err == nil {
				relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, raw, location)
			}
		}
	}
	if final && complete && info.ZTAPIMetering != nil {
		info.ZTAPIMetering.UsageIntegrityComplete = true
	}
	return usage
}
