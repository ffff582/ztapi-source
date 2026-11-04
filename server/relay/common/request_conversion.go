package common

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	ReasoningEffortSourceRequest           = "request"
	ReasoningEffortSourceModelSuffix       = "model_suffix"
	ReasoningEffortSourceParameterOverride = "parameter_override"
	ReasoningEffortSourceDefault           = "default"
	ReasoningEffortSourceCodexAliasMapping = "codex_alias_mapping"
)

func normalizedReasoningEffort(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func IsReasoningEffortValue(value string) bool {
	switch normalizedReasoningEffort(value) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func IsReasoningEffortSource(value string) bool {
	switch value {
	case ReasoningEffortSourceRequest, ReasoningEffortSourceModelSuffix,
		ReasoningEffortSourceParameterOverride, ReasoningEffortSourceDefault,
		ReasoningEffortSourceCodexAliasMapping:
		return true
	default:
		return false
	}
}

func reasoningEffortFromRequest(req any) string {
	switch request := req.(type) {
	case *dto.GeneralOpenAIRequest:
		if request == nil {
			return ""
		}
		if effort := normalizedReasoningEffort(request.ReasoningEffort); effort != "" {
			return effort
		}
		return normalizedReasoningEffort(gjson.GetBytes(request.Reasoning, "effort").String())
	case dto.GeneralOpenAIRequest:
		return reasoningEffortFromRequest(&request)
	case *dto.OpenAIResponsesRequest:
		if request != nil && request.Reasoning != nil {
			return normalizedReasoningEffort(request.Reasoning.Effort)
		}
	case dto.OpenAIResponsesRequest:
		return reasoningEffortFromRequest(&request)
	case *dto.ClaudeRequest:
		if request != nil {
			return normalizedReasoningEffort(request.GetEfforts())
		}
	case dto.ClaudeRequest:
		return reasoningEffortFromRequest(&request)
	case *dto.GeminiChatRequest:
		if request != nil && request.GenerationConfig.ThinkingConfig != nil {
			return normalizedReasoningEffort(request.GenerationConfig.ThinkingConfig.ThinkingLevel)
		}
	case dto.GeminiChatRequest:
		return reasoningEffortFromRequest(&request)
	}
	return ""
}

func ReasoningEffortFromRequest(req any) string {
	return reasoningEffortFromRequest(req)
}

func SetReasoningEffortForRequest(req any, effort string) error {
	effort = normalizedReasoningEffort(effort)
	if effort == "" {
		return dropReasoningEffortForRequest(req)
	}
	switch request := req.(type) {
	case *dto.GeneralOpenAIRequest:
		if request == nil {
			return nil
		}
		if normalizedReasoningEffort(request.ReasoningEffort) != "" {
			request.ReasoningEffort = effort
			return nil
		}
		body := request.Reasoning
		if len(body) == 0 {
			body = []byte(`{}`)
		}
		updated, err := sjson.SetBytes(body, "effort", effort)
		if err != nil {
			return err
		}
		request.Reasoning = updated
		return nil
	case *dto.OpenAIResponsesRequest:
		if request == nil {
			return nil
		}
		if request.Reasoning == nil {
			request.Reasoning = &dto.Reasoning{}
		}
		request.Reasoning.Effort = effort
		return nil
	case *dto.ClaudeRequest:
		if request == nil {
			return nil
		}
		body := request.OutputConfig
		if len(body) == 0 {
			body = []byte(`{}`)
		}
		updated, err := sjson.SetBytes(body, "effort", effort)
		if err != nil {
			return err
		}
		request.OutputConfig = updated
		return nil
	case *dto.GeminiChatRequest:
		if request == nil {
			return nil
		}
		if request.GenerationConfig.ThinkingConfig == nil {
			request.GenerationConfig.ThinkingConfig = &dto.GeminiThinkingConfig{}
		}
		request.GenerationConfig.ThinkingConfig.ThinkingLevel = effort
		return nil
	default:
		return fmt.Errorf("unsupported reasoning request type %T", req)
	}
}

func dropEffortObject(body []byte) ([]byte, error) {
	if !gjson.GetBytes(body, "effort").Exists() {
		return body, nil
	}
	updated, err := sjson.DeleteBytes(body, "effort")
	if err == nil && len(gjson.ParseBytes(updated).Map()) == 0 {
		return nil, nil
	}
	return updated, err
}

func dropReasoningEffortForRequest(req any) error {
	switch request := req.(type) {
	case *dto.GeneralOpenAIRequest:
		if request == nil {
			return nil
		}
		request.ReasoningEffort = ""
		updated, err := dropEffortObject(request.Reasoning)
		request.Reasoning = updated
		return err
	case *dto.OpenAIResponsesRequest:
		if request != nil && request.Reasoning != nil {
			request.Reasoning.Effort = ""
			if request.Reasoning.Summary == "" {
				request.Reasoning = nil
			}
		}
	case *dto.ClaudeRequest:
		if request == nil {
			return nil
		}
		updated, err := dropEffortObject(request.OutputConfig)
		request.OutputConfig = updated
		return err
	case *dto.GeminiChatRequest:
		if request != nil && request.GenerationConfig.ThinkingConfig != nil {
			config := request.GenerationConfig.ThinkingConfig
			config.ThinkingLevel = ""
			if config.ThinkingBudget == nil && !config.IncludeThoughts {
				request.GenerationConfig.ThinkingConfig = nil
			}
		}
	default:
		return fmt.Errorf("unsupported reasoning request type %T", req)
	}
	return nil
}

func CaptureReasoningEffortReceived(info *RelayInfo, req any) {
	if info == nil {
		return
	}
	info.ReasoningEffortReceived = reasoningEffortFromRequest(req)
	if info.ReasoningEffortReceived != "" {
		info.ReasoningEffortSource = ReasoningEffortSourceRequest
	}
}

func MarkReasoningEffortSource(info *RelayInfo, source string) {
	if info == nil {
		return
	}
	if IsReasoningEffortSource(source) {
		info.ReasoningEffortSource = source
	}
}

func reasoningEffortJSONPaths(format types.RelayFormat) []string {
	switch format {
	case types.RelayFormatOpenAI:
		return []string{"reasoning_effort", "reasoning.effort"}
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		return []string{"reasoning.effort"}
	case types.RelayFormatClaude:
		return []string{"output_config.effort"}
	case types.RelayFormatGemini:
		return []string{
			"generationConfig.thinkingConfig.thinkingLevel",
			"generation_config.thinking_config.thinking_level",
		}
	}
	return nil
}

func reasoningEffortFromJSON(body []byte, format types.RelayFormat) string {
	for _, path := range reasoningEffortJSONPaths(format) {
		if effort := normalizedReasoningEffort(gjson.GetBytes(body, path).String()); effort != "" {
			return effort
		}
	}
	return ""
}

func ReasoningEffortFromJSON(body []byte, format types.RelayFormat) string {
	return reasoningEffortFromJSON(body, format)
}

func SetReasoningEffortInJSON(body []byte, format types.RelayFormat, effort string) ([]byte, error) {
	paths := reasoningEffortJSONPaths(format)
	if len(paths) == 0 {
		return body, fmt.Errorf("unsupported reasoning relay format %q", format)
	}
	if normalizedReasoningEffort(effort) == "" {
		for _, path := range paths {
			if !gjson.GetBytes(body, path).Exists() {
				continue
			}
			var err error
			body, err = sjson.DeleteBytes(body, path)
			if err != nil {
				return nil, err
			}
			if index := strings.LastIndex(path, "."); index >= 0 {
				parent := path[:index]
				if value := gjson.GetBytes(body, parent); value.IsObject() && len(value.Map()) == 0 {
					body, err = sjson.DeleteBytes(body, parent)
					if err != nil {
						return nil, err
					}
				}
			}
		}
		return body, nil
	}
	for _, path := range paths {
		if result := gjson.GetBytes(body, path); result.Exists() {
			return sjson.SetBytes(body, path, normalizedReasoningEffort(effort))
		}
	}
	return body, nil
}

// Normalize every recognized field independently; dropping one must not remove another.
func NormalizeReasoningEffortsInJSON(body []byte, format types.RelayFormat, normalize func(string) string) ([]byte, error) {
	for _, path := range reasoningEffortJSONPaths(format) {
		requested := normalizedReasoningEffort(gjson.GetBytes(body, path).String())
		if requested == "" {
			continue
		}
		effort := normalizedReasoningEffort(normalize(requested))
		if effort == requested {
			continue
		}
		var err error
		if effort != "" {
			body, err = sjson.SetBytes(body, path, effort)
		} else {
			body, err = sjson.DeleteBytes(body, path)
			if err == nil {
				if index := strings.LastIndex(path, "."); index >= 0 {
					parent := path[:index]
					if value := gjson.GetBytes(body, parent); value.IsObject() && len(value.Map()) == 0 {
						body, err = sjson.DeleteBytes(body, parent)
					}
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func reasoningEffortOverridden(info *RelayInfo) bool {
	if info == nil {
		return false
	}
	for _, line := range info.ParamOverrideAudit {
		for _, path := range []string{
			"reasoning_effort", "reasoning.effort", "output_config.effort",
			"generationConfig.thinkingConfig.thinkingLevel",
			"generation_config.thinking_config.thinking_level",
		} {
			if strings.Contains(line, path) {
				return true
			}
		}
	}
	return false
}

func CaptureReasoningEffortForwardedJSON(info *RelayInfo, body []byte, format types.RelayFormat) {
	if info == nil {
		return
	}
	info.ReasoningEffortForwarded = reasoningEffortFromJSON(body, format)
	info.ReasoningEffort = info.ReasoningEffortForwarded
	if reasoningEffortOverridden(info) {
		info.ReasoningEffortSource = ReasoningEffortSourceParameterOverride
	} else if info.ReasoningEffortForwarded == "" {
		if info.ReasoningEffortReceived == "" {
			info.ReasoningEffortSource = ""
		}
	} else if info.ReasoningEffortSource == "" {
		info.ReasoningEffortSource = ReasoningEffortSourceDefault
	}
}

func GuessRelayFormatFromRequest(req any) (types.RelayFormat, bool) {
	switch req.(type) {
	case *dto.GeneralOpenAIRequest, dto.GeneralOpenAIRequest:
		return types.RelayFormatOpenAI, true
	case *dto.OpenAIResponsesRequest, dto.OpenAIResponsesRequest:
		return types.RelayFormatOpenAIResponses, true
	case *dto.ClaudeRequest, dto.ClaudeRequest:
		return types.RelayFormatClaude, true
	case *dto.GeminiChatRequest, dto.GeminiChatRequest:
		return types.RelayFormatGemini, true
	case *dto.EmbeddingRequest, dto.EmbeddingRequest:
		return types.RelayFormatEmbedding, true
	case *dto.RerankRequest, dto.RerankRequest:
		return types.RelayFormatRerank, true
	case *dto.ImageRequest, dto.ImageRequest:
		return types.RelayFormatOpenAIImage, true
	case *dto.AudioRequest, dto.AudioRequest:
		return types.RelayFormatOpenAIAudio, true
	default:
		return "", false
	}
}

func AppendRequestConversionFromRequest(info *RelayInfo, req any) {
	if info == nil {
		return
	}
	format, ok := GuessRelayFormatFromRequest(req)
	if !ok {
		return
	}
	info.AppendRequestConversion(format)
}
