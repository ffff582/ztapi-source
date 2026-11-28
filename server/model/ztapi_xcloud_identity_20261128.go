package model

import (
	"fmt"
	"sort"
)

type ZTAPIXcloudIdentityPlan20261128 struct {
	SourceModel      string `json:"source_model"`
	PublicName       string `json:"public_name"`
	ExistingConfigID int    `json:"existing_config_id"`
	ExistingVersion  uint64 `json:"existing_version"`
	Published        bool   `json:"published"`
	Action           string `json:"action"`
}

// ZTAPIXcloudPublicIdentity20261128 accepts exact dated SKUs, not family prefixes.
func ZTAPIXcloudPublicIdentity20261128(source string) (publicName, provider, protocol string, err error) {
	switch source {
	case "claude-fable-5", "claude-fable-5-1", "claude-fable-5-2",
		"claude-haiku-4-5-20251001", "claude-haiku-5-5",
		"claude-opus-5", "claude-opus-5-5", "claude-sonnet-5-5":
		provider = ZTAPIProviderAnthropic
	case "deepseek-v4.1-flash":
		provider = ZTAPIProviderDeepSeek
	case "gemini-4-neon":
		provider = ZTAPIProviderGoogle
	case "glm-5.2", "glm-5.3", "glm-5.3-flash":
		provider = ZTAPIProviderGLM
	case "gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol",
		"gpt-6.1-terra", "gpt-6.2-astra", "gpt-6.2-sol":
		provider = ZTAPIProviderOpenAI
	case "kimi-k3":
		provider = ZTAPIProviderMoonshot
	case "qwen3.8-flash", "qwen3.8-max":
		provider = ZTAPIProviderQwen
	default:
		return "", "", "", fmt.Errorf("unknown exact Xcloud source model %q", source)
	}

	publicName = "zt-" + source
	switch source {
	case "claude-haiku-4-5-20251001":
		publicName = "zt-claude-haiku-4.5"
	case "claude-haiku-5-5":
		publicName = "zt-claude-haiku-5.5"
	case "claude-opus-5-5":
		publicName = "zt-claude-opus-5.5"
	case "claude-sonnet-5-5":
		publicName = "zt-claude-sonnet-5.5"
	case "qwen3.8-max":
		publicName = "zt-qwen-3.8-max"
	}
	return publicName, provider, ZTAPIProtocolOpenAICompatible, nil
}

// BuildZTAPIXcloudIdentityPlan20261128 only plans identities; it never writes
// configs or grants publication authority. Existing nonempty aliases must match.
func BuildZTAPIXcloudIdentityPlan20261128(existing []ZTAPIModelConfig) ([]ZTAPIXcloudIdentityPlan20261128, error) {
	bySource := make(map[string]ZTAPIModelConfig, len(existing))
	publicOwners := make(map[string]string, len(existing))
	for _, config := range existing {
		if previous, found := bySource[config.SourceModel]; found {
			return nil, fmt.Errorf("duplicate source %q in configs %d and %d", config.SourceModel, previous.ID, config.ID)
		}
		bySource[config.SourceModel] = config
		if public := config.PublicNameValue(); public != "" {
			if owner, found := publicOwners[public]; found && owner != config.SourceModel {
				return nil, fmt.Errorf("public alias %q is claimed by sources %q and %q", public, owner, config.SourceModel)
			}
			publicOwners[public] = config.SourceModel
		}
	}

	quotes, err := ZTAPIXcloudCatalogQuotes20261128()
	if err != nil {
		return nil, err
	}
	// Group and context quotes must produce one identity per exact source SKU.
	seen := make(map[string]bool)
	var sources []string
	for _, quote := range quotes {
		if !seen[quote.SourceModel] {
			seen[quote.SourceModel] = true
			sources = append(sources, quote.SourceModel)
		}
	}
	sort.Strings(sources)
	plans := make([]ZTAPIXcloudIdentityPlan20261128, 0, len(sources))
	for _, source := range sources {
		public, _, _, err := ZTAPIXcloudPublicIdentity20261128(source)
		if err != nil {
			return nil, err
		}
		if owner, found := publicOwners[public]; found && owner != source {
			return nil, fmt.Errorf("public alias %q belongs to source %q, not %q", public, owner, source)
		}
		if _, found := bySource[public]; found {
			return nil, fmt.Errorf("public alias %q collides with an existing source model", public)
		}
		if owner, found := publicOwners[source]; found && owner != source {
			return nil, fmt.Errorf("source model %q is occupied by the public alias of %q", source, owner)
		}
		plan := ZTAPIXcloudIdentityPlan20261128{SourceModel: source, PublicName: public, Action: "new_candidate"}
		if config, found := bySource[source]; found {
			actual := config.PublicNameValue()
			if (actual != "" && actual != public) || (config.Published && actual == "") {
				return nil, fmt.Errorf("existing config %d public alias %q does not match %q", config.ID, actual, public)
			}
			plan.ExistingConfigID, plan.ExistingVersion, plan.Published = config.ID, config.Version, config.Published
			plan.Action = "existing_draft"
			if config.Published {
				plan.Action = "existing_published"
			}
		}
		publicOwners[public] = source
		plans = append(plans, plan)
	}
	return plans, nil
}
