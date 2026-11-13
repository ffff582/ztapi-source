package model

import (
	"fmt"
	"reflect"
	"sort"
)

type ZTAPIABModelIdentity struct {
	ModelName      string
	SourceModel    string
	PublicName     string
	Protocol       string
	ProviderFamily string
	Modality       string
	FrozenLabel    string
	QuotationCells []string
}

type ZTAPIABIdentityReport struct {
	WorkbookSHA256 string
	FrozenSHA256   string
	Mapped         []ZTAPIABModelIdentity
	Unmatched      []string
}

type ztapiABFrozenIdentity struct {
	source string
	public string
}

// Each pair is an existing, frozen source/public identity, not a generated slug.
var ztapiABFrozenIdentityClaims = map[string]ztapiABFrozenIdentity{
	"Claude Fable 5":                {"claude-fable-5", "zt-claude-fable-5"},
	"Claude Haiku 4.5":              {"claude-haiku-4-5-20251001", "zt-claude-haiku-4.5"},
	"Claude Opus 4.6":               {"claude-opus-4-6", "zt-claude-opus-4.6"},
	"Claude Opus 4.7":               {"claude-opus-4-7", "zt-claude-opus-4.7"},
	"Claude Opus 4.8":               {"claude-opus-4-8", "zt-claude-opus-4.8"},
	"Claude Opus 5":                 {"claude-opus-5", "zt-claude-opus-5"},
	"Claude Sonnet 4.6":             {"claude-sonnet-4-6", "zt-claude-sonnet-4.6"},
	"Claude Sonnet 5":               {"claude-sonnet-5", "zt-claude-sonnet-5"},
	"DeepSeek V4 Flash":             {"deepseek-v4-flash", "zt-deepseek-v4-flash"},
	"DeepSeek V4 Pro":               {"deepseek-v4-pro", "zt-deepseek-v4-pro"},
	"Gemini 2.5 Flash Image":        {"gemini-2.5-flash-image", "zt-gemini-2.5-flash-image"},
	"Gemini 3 Flash Preview":        {"gemini-3-flash-preview", "zt-gemini-3-flash-preview"},
	"Gemini 3.1 Flash Lite Preview": {"gemini-3.1-flash-lite-preview", "zt-gemini-3.1-flash-lite-preview"},
	"Gemini 3.1 Pro Preview":        {"gemini-3.1-pro-preview", "zt-gemini-3.1-pro-preview"},
	"Gemini 3.5 Flash":              {"gemini-3.5-flash", "zt-gemini-3.5-flash"},
	"GLM 4.7":                       {"glm-4.7", "zt-glm-4.7"},
	"GLM 5":                         {"glm-5", "zt-glm-5"},
	"GLM 5.1":                       {"glm-5.1", "zt-glm-5.1"},
	"GLM 5.2":                       {"glm-5.2", "zt-glm-5.2"},
	"GPT 4.1":                       {"gpt-4.1", "zt-gpt-4.1"},
	"GPT 4.1 Mini":                  {"gpt-4.1-mini", "zt-gpt-4.1-mini"},
	"GPT 4.1 Nano":                  {"gpt-4.1-nano", "zt-gpt-4.1-nano"},
	"GPT 4o Mini":                   {"gpt-4o-mini", "zt-gpt-4o-mini-2024-07-18"},
	"GPT 5 Mini":                    {"gpt-5-mini", "zt-gpt-5-mini"},
	"GPT 5 Nano":                    {"gpt-5-nano", "zt-gpt-5-nano"},
	"GPT 5.4":                       {"gpt-5.4", "zt-gpt-5.4"},
	"GPT 5.4 Mini":                  {"gpt-5.4-mini", "zt-gpt-5.4-mini"},
	"GPT 5.4 Nano":                  {"gpt-5.4-nano", "zt-gpt-5.4-nano"},
	"GPT 5.4 Pro":                   {"gpt-5.4-pro", "zt-gpt-5.4-pro"},
	"GPT 5.5":                       {"gpt-5.5", "zt-gpt-5.5"},
	"GPT 5.6 Luna":                  {"gpt-5.6-luna", "zt-gpt-5.6-luna"},
	"GPT 5.6 Sol":                   {"gpt-5.6-sol", "zt-gpt-5.6-sol"},
	"GPT 5.6 Terra":                 {"gpt-5.6-terra", "zt-gpt-5.6-terra"},
	"GPT Image 2":                   {"gpt-image-2", "zt-gp-image-2"},
	"Kimi K2.5":                     {"kimi-k2.5", "zt-kimi-k2.5"},
	"Kimi K2.6":                     {"kimi-k2.6", "zt-kimi-k2.6"},
	"Kimi K2.7 Code":                {"kimi-k2.7-code", "zt-kimi-k2.7-code"},
	"Seedance 2.0":                  {"doubao-seedance-2.0", "zt-seedance-2.0"},
	"Seedance 2.0 Fast":             {"doubao-seedance-2-0-fast", "zt-seedance-2.0-fast"},
	"Seedance 2.0 Mini":             {"doubao-seedance-2-0-mini", "zt-seedance-2.0-mini"},
	"Text Embedding 3 Small":        {"text-embedding-3-small", "zt-text-embedding-3-small"},
	"Text Embedding Ada 002":        {"text-embedding-ada-002", "zt-text-embedding-ada-002"},
}

// The 2026-09-15 workbook quotes models the first quotation never listed, so
// they have no frozen row to inherit an identity from. Each one is declared
// here against that workbook: the upstream ID is the provider's own model ID,
// which a release confirms against the channel's fetched model list before it
// imports a price or publishes anything.
var ztapiABIntroducedIdentityClaims = map[string]ZTAPIABModelIdentity{
	"GLM 5.3":       {SourceModel: "glm-5.3", PublicName: "zt-glm-5.3", Protocol: "openai_compatible", ProviderFamily: "glm", Modality: "text"},
	"GLM 5.3 Flash": {SourceModel: "glm-5.3-flash", PublicName: "zt-glm-5.3-flash", Protocol: "openai_compatible", ProviderFamily: "glm", Modality: "text"},
	"GPT 6 Astra":   {SourceModel: "gpt-6-astra", PublicName: "zt-gpt-6-astra", Protocol: "openai_compatible", ProviderFamily: "openai", Modality: "text"},
	"Kimi K3":       {SourceModel: "kimi-k3", PublicName: "zt-kimi-k3", Protocol: "openai_compatible", ProviderFamily: "moonshot", Modality: "text"},
	"Seedance 2.5":  {SourceModel: "doubao-seedance-2-5", PublicName: "zt-seedance-2.5", Protocol: "openai_compatible", ProviderFamily: "seedance", Modality: "video"},
}

// BuildZTAPIABQuotationIdentityBridge reports evidence only; it does not grant publication authority.
func BuildZTAPIABQuotationIdentityBridge(quote ZTAPIABQuotationManifest, frozen []ZTAPIQuotationEntry) (ZTAPIABIdentityReport, error) {
	report := ZTAPIABIdentityReport{WorkbookSHA256: quote.WorkbookSHA256, FrozenSHA256: ZTAPIQuotationSHA256}
	if ztapiQuotationLoadError != nil {
		return report, ztapiQuotationLoadError
	}
	if !reflect.DeepEqual(frozen, ztapiQuotation.Entries) {
		return report, fmt.Errorf("frozen quotation identity evidence differs from audited manifest: %w", ErrZTAPIQuotationIdentityMismatch)
	}
	frozenBySource := make(map[string]ZTAPIQuotationEntry, len(frozen))
	for _, entry := range frozen {
		if entry.Status == "mapped" {
			if _, exists := frozenBySource[entry.SourceModel]; exists {
				return report, fmt.Errorf("duplicate frozen source identity %q: %w", entry.SourceModel, ErrZTAPIQuotationIdentityMismatch)
			}
			frozenBySource[entry.SourceModel] = entry
		}
	}
	cellsByName := make(map[string]map[string]bool)
	for _, entry := range quote.Entries {
		if entry.ModelName == "" || entry.QuotationCell == "" {
			return report, fmt.Errorf("A/B quotation identity evidence is incomplete")
		}
		if cellsByName[entry.ModelName] == nil {
			cellsByName[entry.ModelName] = make(map[string]bool)
		}
		cellsByName[entry.ModelName][entry.QuotationCell] = true
	}
	frozenByPublic := make(map[string]ZTAPIQuotationEntry, len(frozenBySource))
	for _, entry := range frozenBySource {
		frozenByPublic[entry.PublicName] = entry
	}
	introducedSeen := make(map[string]string, len(ztapiABIntroducedIdentityClaims))
	for modelName, cells := range cellsByName {
		claim, claimed := ztapiABFrozenIdentityClaims[modelName]
		if !claimed {
			introduced, declared := ztapiABIntroducedIdentityClaims[modelName]
			if !declared {
				report.Unmatched = append(report.Unmatched, modelName)
				continue
			}
			// A model introduced by this workbook must not take over an
			// identity the frozen quotation already carries, and two of them
			// must not claim the same one.
			if _, taken := frozenBySource[introduced.SourceModel]; taken {
				return report, fmt.Errorf("introduced identity %q is already frozen: %w", modelName, ErrZTAPIQuotationIdentityMismatch)
			}
			if _, taken := frozenByPublic[introduced.PublicName]; taken {
				return report, fmt.Errorf("introduced public identity %q is already frozen: %w", modelName, ErrZTAPIQuotationIdentityMismatch)
			}
			for _, key := range []string{"source:" + introduced.SourceModel, "public:" + introduced.PublicName} {
				if other, duplicate := introducedSeen[key]; duplicate {
					return report, fmt.Errorf("introduced identity %q repeats %q: %w", modelName, other, ErrZTAPIQuotationIdentityMismatch)
				}
				introducedSeen[key] = modelName
			}
			introduced.ModelName = modelName
			introduced.QuotationCells = sortedZTAPIQuotationCells(cells)
			report.Mapped = append(report.Mapped, introduced)
			continue
		}
		old, present := frozenBySource[claim.source]
		if !present {
			report.Unmatched = append(report.Unmatched, modelName)
			continue
		}
		if old.PublicName != claim.public {
			return report, fmt.Errorf("frozen public identity changed for %q: %w", modelName, ErrZTAPIQuotationIdentityMismatch)
		}
		identity := ZTAPIABModelIdentity{
			ModelName: modelName, SourceModel: old.SourceModel, PublicName: old.PublicName,
			Protocol: old.Protocol, ProviderFamily: old.ProviderFamily, Modality: old.Modality,
			FrozenLabel: old.Label,
		}
		identity.QuotationCells = sortedZTAPIQuotationCells(cells)
		report.Mapped = append(report.Mapped, identity)
	}
	sort.Slice(report.Mapped, func(i, j int) bool { return report.Mapped[i].ModelName < report.Mapped[j].ModelName })
	sort.Strings(report.Unmatched)
	return report, nil
}

func sortedZTAPIQuotationCells(cells map[string]bool) []string {
	ordered := make([]string, 0, len(cells))
	for cell := range cells {
		ordered = append(ordered, cell)
	}
	sort.Strings(ordered)
	return ordered
}

func CurrentZTAPIABQuotationIdentityBridge() (ZTAPIABIdentityReport, error) {
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return ZTAPIABIdentityReport{}, err
	}
	frozen, err := ZTAPIQuotationEntries()
	if err != nil {
		return ZTAPIABIdentityReport{}, err
	}
	return BuildZTAPIABQuotationIdentityBridge(quote, frozen)
}
