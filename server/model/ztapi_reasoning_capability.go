package model

import (
	"errors"
	"strings"
)

var ErrZTAPIReasoningEffortUnsupported = errors.New("ztapi reasoning effort is unsupported for this model")

type ZTAPIReasoningCapability struct {
	SourceModel            string   `json:"source_model"`
	Modality               string   `json:"modality"`
	ReasoningEfforts       []string `json:"reasoning_efforts"`
	EvidenceQuote          string   `json:"evidence_quote"`
	LiveValidationRequired bool     `json:"live_validation_required"`
}

func pendingTextReasoningCapability(sourceModel string) ZTAPIReasoningCapability {
	return ZTAPIReasoningCapability{
		SourceModel:            sourceModel,
		Modality:               ZTAPIModalityText,
		ReasoningEfforts:       []string{},
		EvidenceQuote:          "No exact supported-effort declaration is available; live upstream validation is required.",
		LiveValidationRequired: true,
	}
}

func nonReasoningCapability(sourceModel, modality string) ZTAPIReasoningCapability {
	return ZTAPIReasoningCapability{
		SourceModel:      sourceModel,
		Modality:         modality,
		ReasoningEfforts: []string{},
		EvidenceQuote:    "No reasoning-effort capability is declared for this quotation-backed model.",
	}
}

// Keys are exact source_model values from ztapi_quotation_v1.json. Empty
// effort lists mean the provider contract still needs live validation. Standard
// wire values pass through for compatibility, while local aliases remain closed.
var ztapiReasoningCapabilities = map[string]ZTAPIReasoningCapability{
	"claude-fable-5":                pendingTextReasoningCapability("claude-fable-5"),
	"claude-haiku-4-5-20251001":     pendingTextReasoningCapability("claude-haiku-4-5-20251001"),
	"claude-opus-4-6":               pendingTextReasoningCapability("claude-opus-4-6"),
	"claude-opus-4-7":               pendingTextReasoningCapability("claude-opus-4-7"),
	"claude-opus-4-8":               pendingTextReasoningCapability("claude-opus-4-8"),
	"claude-opus-5":                 pendingTextReasoningCapability("claude-opus-5"),
	"claude-sonnet-4-6":             pendingTextReasoningCapability("claude-sonnet-4-6"),
	"claude-sonnet-5":               pendingTextReasoningCapability("claude-sonnet-5"),
	"deepseek-v4-flash":             pendingTextReasoningCapability("deepseek-v4-flash"),
	"deepseek-v4-pro":               pendingTextReasoningCapability("deepseek-v4-pro"),
	"gemini-3-flash-preview":        pendingTextReasoningCapability("gemini-3-flash-preview"),
	"gemini-3.1-flash-lite-preview": pendingTextReasoningCapability("gemini-3.1-flash-lite-preview"),
	"gemini-3.1-pro-preview":        pendingTextReasoningCapability("gemini-3.1-pro-preview"),
	"gemini-3.5-flash":              pendingTextReasoningCapability("gemini-3.5-flash"),
	"glm-4.7":                       pendingTextReasoningCapability("glm-4.7"),
	"glm-5":                         pendingTextReasoningCapability("glm-5"),
	"glm-5.1":                       pendingTextReasoningCapability("glm-5.1"),
	"glm-5.2":                       pendingTextReasoningCapability("glm-5.2"),
	"gpt-4.1":                       nonReasoningCapability("gpt-4.1", ZTAPIModalityText),
	"gpt-4.1-mini":                  nonReasoningCapability("gpt-4.1-mini", ZTAPIModalityText),
	"gpt-4.1-nano":                  nonReasoningCapability("gpt-4.1-nano", ZTAPIModalityText),
	"gpt-4o-mini":                   nonReasoningCapability("gpt-4o-mini", ZTAPIModalityText),
	"gpt-5-mini":                    pendingTextReasoningCapability("gpt-5-mini"),
	"gpt-5-nano":                    pendingTextReasoningCapability("gpt-5-nano"),
	"gpt-5.4":                       pendingTextReasoningCapability("gpt-5.4"),
	"gpt-5.4-mini":                  pendingTextReasoningCapability("gpt-5.4-mini"),
	"gpt-5.4-nano":                  pendingTextReasoningCapability("gpt-5.4-nano"),
	"gpt-5.4-pro":                   pendingTextReasoningCapability("gpt-5.4-pro"),
	"gpt-5.5":                       pendingTextReasoningCapability("gpt-5.5"),
	"gpt-5.6-luna":                  pendingTextReasoningCapability("gpt-5.6-luna"),
	"gpt-5.6-sol": {
		SourceModel:            "gpt-5.6-sol",
		Modality:               ZTAPIModalityText,
		ReasoningEfforts:       []string{"high", "low", "max", "medium", "none", "xhigh"},
		EvidenceQuote:          "Owner-approved contract: none, low, medium, high, xhigh, and max; ultra is a local alias that maps to max.",
		LiveValidationRequired: true,
	},
	"gpt-5.6-terra":          pendingTextReasoningCapability("gpt-5.6-terra"),
	"kimi-k2.5":              pendingTextReasoningCapability("kimi-k2.5"),
	"kimi-k2.6":              pendingTextReasoningCapability("kimi-k2.6"),
	"kimi-k2.7-code":         pendingTextReasoningCapability("kimi-k2.7-code"),
	"qwen3.5-flash":          pendingTextReasoningCapability("qwen3.5-flash"),
	"qwen3.7-max":            pendingTextReasoningCapability("qwen3.7-max"),
	"qwen3.7-plus":           pendingTextReasoningCapability("qwen3.7-plus"),
	"qwen3.8-max":            pendingTextReasoningCapability("qwen3.8-max"),
	"text-embedding-ada-002": nonReasoningCapability("text-embedding-ada-002", ZTAPIModalityEmbedding),
	"text-embedding-3-small": nonReasoningCapability("text-embedding-3-small", ZTAPIModalityEmbedding),
	"gpt-image-2":            nonReasoningCapability("gpt-image-2", ZTAPIModalityImage),
	"gemini-2.5-flash-image": nonReasoningCapability("gemini-2.5-flash-image", ZTAPIModalityImage),
}

func ZTAPIReasoningCapabilityFor(sourceModel string) (ZTAPIReasoningCapability, bool) {
	capability, ok := ztapiReasoningCapabilities[strings.TrimSpace(sourceModel)]
	if !ok {
		return ZTAPIReasoningCapability{}, false
	}
	capability.ReasoningEfforts = append([]string(nil), capability.ReasoningEfforts...)
	return capability, true
}

func NormalizeZTAPIReasoningEffort(sourceModel, requested string) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		return "", nil
	}
	capability, ok := ZTAPIReasoningCapabilityFor(sourceModel)
	if !ok {
		return "", ErrZTAPIReasoningEffortUnsupported
	}
	if requested == "ultra" {
		requested = "max"
		for _, supported := range capability.ReasoningEfforts {
			if requested == supported {
				return requested, nil
			}
		}
		return "", ErrZTAPIReasoningEffortUnsupported
	}
	if len(capability.ReasoningEfforts) == 0 && capability.LiveValidationRequired {
		switch requested {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max":
			return requested, nil
		default:
			return "", ErrZTAPIReasoningEffortUnsupported
		}
	}
	for _, supported := range capability.ReasoningEfforts {
		if requested == supported {
			return requested, nil
		}
	}
	return "", ErrZTAPIReasoningEffortUnsupported
}
