package helper

import (
	"strings"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ZTAPIReasoningOutputFloor protects tiny caps from being exhausted by hidden
// reasoning before visible output. A larger budget can increase actual usage;
// managed Claude chat reservations must cover the effective output budget.
const ZTAPIReasoningOutputFloor = 1024

// ztapiReasoningFamilyPrefixes covers reasoning models not yet listed in the
// capability table. Native Claude requests keep their deliberate limits;
// managed Claude chat bridges are handled separately below.
var ztapiReasoningFamilyPrefixes = []string{"gpt-5", "o1", "o3", "o4", "deepseek", "gemini-2.5", "gemini-3", "glm-4.5", "glm-4.6", "glm-4.7", "glm-5", "kimi-k2", "kimi-k3", "qwen3"}

func ztapiModelReasonsByDefault(sourceModel string) bool {
	sourceModel = strings.ToLower(strings.TrimSpace(sourceModel))
	if sourceModel == "" || strings.HasPrefix(sourceModel, "claude-") {
		return false
	}
	if capability, ok := model.ZTAPIReasoningCapabilityFor(sourceModel); ok {
		return capability.Modality == model.ZTAPIModalityText && (len(capability.ReasoningEfforts) > 0 || capability.LiveValidationRequired)
	}
	for _, prefix := range ztapiReasoningFamilyPrefixes {
		if strings.HasPrefix(sourceModel, prefix) {
			return true
		}
	}
	return false
}

func ztapiClaudeChatReasonsByDefault(sourceModel string) bool {
	sourceModel = strings.ToLower(strings.TrimSpace(sourceModel))
	return strings.HasPrefix(sourceModel, "claude-sonnet-") ||
		strings.HasPrefix(sourceModel, "claude-opus-") ||
		strings.HasPrefix(sourceModel, "claude-fable-")
}

// applyZTAPIReasoningOutputFloor raises an explicit output cap below
// ZTAPIReasoningOutputFloor for a managed reasoning model. A request without a
// cap is left to the supplier default.
func applyZTAPIReasoningOutputFloor(info *relaycommon.RelayInfo, body []byte, format types.RelayFormat) []byte {
	if info == nil || info.ZTAPIPublicationSnapshot == nil {
		return body
	}
	sourceModel := info.ZTAPIPublicationSnapshot.SourceModel
	if !ztapiModelReasonsByDefault(sourceModel) &&
		!(format == types.RelayFormatOpenAI && ztapiClaudeChatReasonsByDefault(sourceModel)) {
		return body
	}
	var paths []string
	switch format {
	case types.RelayFormatOpenAI:
		paths = []string{"max_tokens", "max_completion_tokens"}
	case types.RelayFormatOpenAIResponses:
		paths = []string{"max_output_tokens"}
	case types.RelayFormatClaude:
		paths = []string{"max_tokens"}
	case types.RelayFormatGemini:
		paths = []string{"generationConfig.maxOutputTokens"}
	default:
		return body
	}
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if !value.Exists() || value.Type != gjson.Number || value.Int() <= 0 ||
			value.Float() != float64(value.Int()) || value.Int() >= ZTAPIReasoningOutputFloor {
			continue
		}
		if updated, err := sjson.SetBytes(body, path, ZTAPIReasoningOutputFloor); err == nil {
			body = updated
			if format == types.RelayFormatOpenAI && ztapiClaudeChatReasonsByDefault(sourceModel) {
				info.ZTAPIReasoningOutputBudget = ZTAPIReasoningOutputFloor
			}
		}
	}
	return body
}
