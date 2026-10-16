package helper

import (
	"strings"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ZTAPIReasoningOutputFloor is the smallest output budget a reasoning model is
// given. Clients often send a few tokens to "test the connection"; a reasoning
// model spends them all thinking and returns nothing, which the client reports
// as a broken key. Only actual usage is billed, so raising a tiny cap costs the
// customer nothing it would not have paid for an answer.
const ZTAPIReasoningOutputFloor = 1024

// ztapiReasoningFamilyPrefixes covers reasoning models not yet listed in the
// capability table. Claude is left out: it reasons only when thinking is
// requested, so a small cap there is a deliberate length limit.
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

// applyZTAPIReasoningOutputFloor raises an explicit output cap below
// ZTAPIReasoningOutputFloor for a managed reasoning model. A request without a
// cap is left to the supplier default.
func applyZTAPIReasoningOutputFloor(info *relaycommon.RelayInfo, body []byte, format types.RelayFormat) []byte {
	if info == nil || info.ZTAPIPublicationSnapshot == nil || !ztapiModelReasonsByDefault(info.ZTAPIPublicationSnapshot.SourceModel) {
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
		if !value.Exists() || value.Type != gjson.Number || value.Int() >= ZTAPIReasoningOutputFloor {
			continue
		}
		if updated, err := sjson.SetBytes(body, path, ZTAPIReasoningOutputFloor); err == nil {
			body = updated
		}
	}
	return body
}
