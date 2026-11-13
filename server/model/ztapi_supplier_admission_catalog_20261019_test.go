package model

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestSupplierAdmissionCatalog20261019PreservesAllQuoteRows(t *testing.T) {
	models, err := ZTAPISupplierAdmissionModels20261019()
	require.NoError(t, err)
	require.Len(t, models, 55)
	// Literal workbook codes catch dropped rows and accidental SKU normalization.
	codes := []string{
		"kimi-k3", "kimi-k2.7-code", "kimi-k2.6", "kimi-k2.5",
		"glm-5.2", "glm-5.1", "glm-5", "glm-4.7",
		"deepseek-v4.1-flash-prime", "deepseek-v4-pro-prime", "deepseek-v4-flash-prime",
		"zq-gk-b01\uff08hc\uff09", "zq-gk47\uff08hc\uff09", "zq-gk46\uff08hc\uff09", "zq-gk45\uff08hc\uff09",
		"zq-g-i-2\uff08hc\uff09", "zq-g61-s\uff08hc\uff09", "zq-g6-s\uff08hc\uff09", "zq-g6-l\uff08hc\uff09", "zq-g6-a\uff08hc\uff09",
		"zq-g56-t\uff08hc\uff09", "zq-g56-s\uff08hc\uff09", "zq-g56-l\uff08hc\uff09", "zq-g55\uff08hc\uff09", "zq-g54\uff08hc\uff09",
		"zq-c-s5\uff08hc\uff09", "zq-c-s55\uff08hc\uff09", "zq-c-s46\uff08hc\uff09", "zq-c-o55\uff08hc\uff09",
		"zq-c-o5\uff08hc\uff09", "zq-c-o48\uff08hc\uff09", "zq-c-o47\uff08hc\uff09", "zq-c-o46\uff08hc\uff09",
		"zq-g-nb21", "zq-g-4-a", "zq-g38-f", "zq-g35-f", "zq-g31-p-p", "zq-g31-f-l-p",
		"zq-g31-f-l-i", "zq-g31-f-i", "zq-g3-p-i", "zq-g3-f-p", "zq-g25-f-i", "Vidu\u5168\u7cfb\u5217",
		"zq-db-sda-25", "zq-db-sda-20-mi", "zq-db-sda-20-fa", "zq-db-sda-20",
		"doubao-seed-character-260628", "doubao-seed-2-1-turbo-260628", "doubao-seed-2-1-pro-260628",
		"doubao-seed-2-1-lite-260915", "doubao-seed-2-0-pro-260215", "doubao-seed-2-0-lite-260428",
	}
	for i, entry := range models {
		require.Equal(t, codes[i], entry.ModelCode)
		require.Equal(t, i+2, entry.QuotationRow)
		require.Equal(t, fmt.Sprintf("E%d", i+2), entry.QuotationCell)
		require.Equal(t, codes[i], entry.QuoteDisplayName)
		require.NotEmpty(t, entry.QuoteName)
		require.NotEmpty(t, entry.QuoteVendor)
		require.NotEmpty(t, entry.ProviderFamily)
		require.NotEmpty(t, entry.BillingProfile)
		require.Equal(t, "candidate", entry.ClaimStatus)
		require.Equal(t, "hold", entry.AdmissionStatus)
		require.NotEmpty(t, entry.Blockers)
		if entry.KnownOfficialModelID == "" {
			require.Contains(t, entry.Blockers, "official_model_id_evidence_missing")
			require.Empty(t, entry.OfficialModelIDEvidenceURL)
			require.Empty(t, entry.OfficialModelIDVerifiedDate)
		} else {
			require.NotContains(t, entry.Blockers, "official_model_id_evidence_missing")
			require.NotEmpty(t, entry.OfficialModelIDEvidenceURL)
			require.Equal(t, "2026-10-19", entry.OfficialModelIDVerifiedDate)
		}
		require.Contains(t, entry.Blockers, "supplier_backend_identity_not_live_verified")
		require.Contains(t, entry.Blockers, "live_channel_discovery_and_verification_missing")
		switch {
		case i < 33:
			require.Equal(t, "B", entry.Grade)
			require.Equal(t, "pool", entry.ResourceType)
			require.Equal(t, 2, entry.RequiredChannelID)
		case i == 44:
			require.Equal(t, "S", entry.Grade)
			require.Equal(t, "unresolved", entry.ResourceType)
			require.Zero(t, entry.RequiredChannelID)
		default:
			require.Equal(t, "A", entry.Grade)
			require.Equal(t, "enterprise", entry.ResourceType)
			require.Zero(t, entry.RequiredChannelID)
			require.Contains(t, entry.Blockers, "channel_assignment_requires_explicit_approval")
		}
	}
}

func TestSupplierAdmissionCatalog20261019ExactGPTClaudePhysicalMappings(t *testing.T) {
	for _, tc := range []struct{ name, source, public, upstream, cell string }{
		{"GPT 6.1 Sol", "gpt-6.1-sol", "zt-gpt-6.1-sol", "gpt-6.1-sol-hc", "E18"},
		{"GPT 6 Sol", "gpt-6-sol", "zt-gpt-6-sol", "gpt-6-sol-hc", "E19"},
		{"GPT 6 Luna", "gpt-6-luna", "zt-gpt-6-luna", "gpt-6-luna-hc", "E20"},
		{"GPT 6 Astra", "gpt-6-astra", "zt-gpt-6-astra", "gpt-6-astra-hc", "E21"},
		{"GPT 5.6 Terra", "gpt-5.6-terra", "zt-gpt-5.6-terra", "gpt-5.6-terra-hc", "E22"},
		{"GPT 5.6 Sol", "gpt-5.6-sol", "zt-gpt-5.6-sol", "gpt-5.6-sol-hc", "E23"},
		{"GPT 5.6 Luna", "gpt-5.6-luna", "zt-gpt-5.6-luna", "gpt-5.6-luna-hc", "E24"},
		{"GPT 5.5", "gpt-5.5", "zt-gpt-5.5", "gpt-5.5-hc", "E25"},
		{"GPT 5.4", "gpt-5.4", "zt-gpt-5.4", "gpt-5.4-hc", "E26"},
		{"Claude Sonnet 5.5", "claude-sonnet-5-5", "zt-claude-sonnet-5.5", "claude-sonnet-5-5-hc", "E28"},
		{"Claude Opus 5.5", "claude-opus-5-5", "zt-claude-opus-5.5", "claude-opus-5-5-hc", "E30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry, err := FindZTAPISupplierAdmissionModel20261019(tc.name)
			require.NoError(t, err)
			require.Equal(t, tc.name, entry.ModelName)
			require.Equal(t, tc.source, entry.SourceModel)
			require.Equal(t, tc.public, entry.PublicName)
			require.Equal(t, tc.upstream, entry.UpstreamModel)
			require.Equal(t, tc.cell, entry.QuotationCell)
			require.Contains(t, entry.QuoteName, "\uff08hc\uff09")
		})
	}
}

func TestSupplierAdmissionCatalog20261019HoldsAmbiguousAndMediaModels(t *testing.T) {
	for _, tc := range []struct{ name, upstream string }{
		{"Grok Build 0.1", "grok-build-0.1-hc"},
		{"Grok 4.7", "grok-4.7-hc"}, {"Grok 4.6", "grok-4.6-hc"}, {"Grok 4.5", "grok-4.5-hc"},
	} {
		entry, err := FindZTAPISupplierAdmissionModel20261019(tc.name)
		require.NoError(t, err)
		require.Equal(t, tc.upstream, entry.UpstreamModel)
		require.Equal(t, "xai", entry.ProviderFamily)
	}
	prime, err := FindZTAPISupplierAdmissionModel20261019("DeepSeek V4.1 Flash (Prime)")
	require.NoError(t, err)
	require.Equal(t, "deepseek-v4.1-flash-prime", prime.SourceModel)
	require.Empty(t, prime.UpstreamModel)
	require.Empty(t, prime.KnownOfficialModelID)
	require.Equal(t, "deepseek-flash-prime", prime.ObservedDirectoryModel)
	require.Contains(t, prime.Blockers, "quote_upstream_exact_version_mismatch")
	turbo, err := FindZTAPISupplierAdmissionModel20261019("doubao-seed-2-1-turbo-260628")
	require.NoError(t, err)
	require.Equal(t, "doubao-seed-2-1-turbo-260628", turbo.SourceModel)
	require.Empty(t, turbo.UpstreamModel)
	require.Empty(t, turbo.KnownOfficialModelID)
	require.Equal(t, "doubao-seed-2.1-turbo", turbo.ObservedDirectoryModel)
	require.Contains(t, turbo.Blockers, "quote_upstream_exact_version_mismatch")
	vidu, err := FindZTAPISupplierAdmissionModel20261019("Vidu\u5168\u7cfb\u5217")
	require.NoError(t, err)
	require.Empty(t, vidu.SourceModel)
	require.Empty(t, vidu.PublicName)
	require.Empty(t, vidu.UpstreamModel)
	require.Contains(t, vidu.Blockers, "series_quote_requires_explicit_sku_breakdown")
	models, err := ZTAPISupplierAdmissionModels20261019()
	require.NoError(t, err)
	for _, entry := range models {
		if entry.Modality == "image" || entry.Modality == "video" {
			require.Contains(t, entry.Blockers, "media_protocol_and_actual_result_evidence_incomplete", entry.ModelName)
			require.Contains(t, entry.Blockers, "media_metering_and_price_contract_incomplete", entry.ModelName)
		}
		if entry.Modality == "video" {
			require.Contains(t, entry.Blockers, "video_terminal_recovery_and_idempotent_settlement_unverified", entry.ModelName)
		}
		if entry.UpstreamModel == "" {
			require.Contains(t, entry.Blockers, "upstream_physical_model_id_evidence_missing", entry.ModelName)
		}
	}
}

func TestSupplierAdmissionCatalog20261019LookupDoesNotFuzzyMapOrLeakMutations(t *testing.T) {
	for _, name := range []string{"", " GPT 6 Sol", "GPT 6 Sol ", "gpt 6 sol", "GPT 6", "gpt-6-sol", "zt-gpt-6-sol", "gpt-6-sol-hc", "zq-g6-s\uff08hc\uff09", "GPT 6 Sol\x00"} {
		entry, err := FindZTAPISupplierAdmissionModel20261019(name)
		require.Error(t, err, name)
		require.Equal(t, ZTAPISupplierAdmissionModel20261019{}, entry)
	}
	models, err := ZTAPISupplierAdmissionModels20261019()
	require.NoError(t, err)
	models[0].Blockers[0] = "caller_modified"
	models[0].PublicName = "caller_modified"
	entry, err := FindZTAPISupplierAdmissionModel20261019("Kimi K3")
	require.NoError(t, err)
	require.Equal(t, "zt-kimi-k3", entry.PublicName)
	require.NotContains(t, entry.Blockers, "caller_modified")
	entry.Blockers[0] = "lookup_modified"
	entry, err = FindZTAPISupplierAdmissionModel20261019("Kimi K3")
	require.NoError(t, err)
	require.NotContains(t, entry.Blockers, "lookup_modified")
}

func TestSupplierAdmissionCatalog20261019PreservesLegacyIdentitiesAndTables(t *testing.T) {
	before, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	frozenClaims, err := common.Marshal(ztapiABFrozenIdentityClaims)
	require.NoError(t, err)
	introducedClaims, err := common.Marshal(ztapiABIntroducedIdentityClaims)
	require.NoError(t, err)
	models, err := ZTAPISupplierAdmissionModels20261019()
	require.NoError(t, err)
	for _, entry := range models {
		if old, exists := ztapiABFrozenIdentityClaims[entry.ModelName]; exists {
			require.Equal(t, old.source, entry.SourceModel, entry.ModelName)
			require.Equal(t, old.public, entry.PublicName, entry.ModelName)
		}
		if old, exists := ztapiABIntroducedIdentityClaims[entry.ModelName]; exists && entry.ModelName != "Seedance 2.5" {
			require.Equal(t, old.SourceModel, entry.SourceModel, entry.ModelName)
			require.Equal(t, old.PublicName, entry.PublicName, entry.ModelName)
		}
	}
	after, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	require.Equal(t, before, after)
	afterFrozen, err := common.Marshal(ztapiABFrozenIdentityClaims)
	require.NoError(t, err)
	require.Equal(t, frozenClaims, afterFrozen)
	afterIntroduced, err := common.Marshal(ztapiABIntroducedIdentityClaims)
	require.NoError(t, err)
	require.Equal(t, introducedClaims, afterIntroduced)
}

func TestSupplierAdmissionCatalog20261019DeclaresMediaByExactSource(t *testing.T) {
	models, err := ZTAPISupplierAdmissionModels20261019()
	require.NoError(t, err)
	for source, modality := range map[string]string{
		"gemini-nano-banana-2.1":      "image",
		"gemini-3.1-flash-lite-image": "image",
		"gemini-3.1-flash-image":      "image",
		"gemini-3-pro-image":          "image",
		"doubao-seedance-2-5-260628":  "video",
	} {
		matches := 0
		for _, entry := range models {
			if entry.SourceModel == source {
				matches++
				require.Equal(t, modality, entry.Modality)
				require.NotEmpty(t, entry.Blockers)
			}
		}
		require.Equal(t, 1, matches, source)
	}
	for _, name := range []string{"unknown-image", "gemini-made-up-image", "doubao-seedance-2-5-999999", "Vidu made-up video"} {
		entry, err := FindZTAPISupplierAdmissionModel20261019(name)
		require.ErrorIs(t, err, ErrZTAPISupplierAdmissionModelNotFound20261019)
		require.Empty(t, entry.Modality)
	}
}

func TestSupplierAdmissionCatalog20261019ContainsOnlyPublicDeclarationFields(t *testing.T) {
	models, err := ZTAPISupplierAdmissionModels20261019()
	require.NoError(t, err)
	typ := reflect.TypeOf(models[0])
	allowed := strings.Fields("ModelName SourceModel PublicName UpstreamModel KnownOfficialModelID OfficialModelIDEvidenceURL OfficialModelIDVerifiedDate ObservedDirectoryModel ProviderFamily Modality ResourceType ModelCode QuotationCell Fraction BillingProfile RequiredChannelID Blockers QuotationRow QuoteName QuoteDisplayName QuoteVendor Grade ClaimStatus AdmissionStatus IdentityEvidence UpstreamEvidence")
	for i := 0; i < typ.NumField(); i++ {
		require.Contains(t, allowed, typ.Field(i).Name, "catalog must not grow credential/configuration fields")
	}
	raw, err := os.ReadFile("ztapi_supplier_admission_catalog_20261019.json")
	require.NoError(t, err)
	for _, secretMarker := range []string{"Bearer ", "sk-", "api_key", "apiKey", "password", "authorization", "base_url", "credential"} {
		require.NotContains(t, string(raw), secretMarker)
	}
	var manifest struct {
		WorkbookSHA256 string                                `json:"workbook_sha256"`
		Entries        []ZTAPISupplierAdmissionModel20261019 `json:"entries"`
	}
	require.NoError(t, common.Unmarshal(raw, &manifest))
	require.Equal(t, "16171b71f8da8c8f52744f182278fa6ea02db61660bd54f6c7b61e9da990e9aa", manifest.WorkbookSHA256)
	require.Equal(t, models, manifest.Entries)
}

func TestSupplierAdmissionCatalog20261019RejectsInvalidOrWidenedDeclarations(t *testing.T) {
	raw, err := os.ReadFile("ztapi_supplier_admission_catalog_20261019.json")
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any, []any)
	}{
		{"wrong workbook", func(m map[string]any, _ []any) { m["workbook_sha256"] = "untrusted" }},
		{"missing row", func(m map[string]any, rows []any) { m["entries"] = rows[:54] }},
		{"duplicate model name", func(_ map[string]any, rows []any) {
			rows[1].(map[string]any)["model_name"] = rows[0].(map[string]any)["model_name"]
		}},
		{"duplicate quote code", func(_ map[string]any, rows []any) {
			rows[1].(map[string]any)["model_code"] = rows[0].(map[string]any)["model_code"]
		}},
		{"duplicate source", func(_ map[string]any, rows []any) {
			rows[1].(map[string]any)["source_model"] = rows[0].(map[string]any)["source_model"]
		}},
		{"duplicate public name", func(_ map[string]any, rows []any) {
			rows[1].(map[string]any)["public_name"] = rows[0].(map[string]any)["public_name"]
		}},
		{"wrong cell", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["quotation_cell"] = "E99" }},
		{"zero discount", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["fraction"] = "0" }},
		{"excess discount", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["fraction"] = "1.1" }},
		{"pool channel fallback", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["required_channel_id"] = 3 }},
		{"enterprise auto approval", func(_ map[string]any, rows []any) { rows[33].(map[string]any)["required_channel_id"] = 3 }},
		{"unknown grade", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["grade"] = "C" }},
		{"verified claim", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["claim_status"] = "verified" }},
		{"automatic admission", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["admission_status"] = "supported" }},
		{"invented official ID", func(_ map[string]any, rows []any) {
			rows[0].(map[string]any)["known_official_model_id"] = "invented-official-id"
		}},
		{"removed hold", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["blockers"] = []string{} }},
		{"removed mismatch hold", func(_ map[string]any, rows []any) {
			rows[8].(map[string]any)["blockers"] = rows[0].(map[string]any)["blockers"]
		}},
		{"removed media hold", func(_ map[string]any, rows []any) {
			rows[15].(map[string]any)["blockers"] = rows[0].(map[string]any)["blockers"]
		}},
		{"series collapsed to SKU", func(_ map[string]any, rows []any) { rows[44].(map[string]any)["upstream_model"] = "vidu-made-up-sku" }},
		{"credential field", func(_ map[string]any, rows []any) { rows[0].(map[string]any)["api_key"] = "not-a-real-key" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var manifest map[string]any
			require.NoError(t, common.Unmarshal(raw, &manifest))
			tc.mutate(manifest, manifest["entries"].([]any))
			changed, err := common.Marshal(manifest)
			require.NoError(t, err)
			models, err := parseZTAPISupplierAdmissionCatalog20261019(changed)
			require.Error(t, err)
			require.Nil(t, models, "invalid manifests must not return partially usable candidates")
		})
	}
	for _, malformed := range [][]byte{[]byte("{"), append(append([]byte{}, raw...), []byte(" {}")...), []byte(`{"version":1,"version":2}`)} {
		models, err := parseZTAPISupplierAdmissionCatalog20261019(malformed)
		require.Error(t, err)
		require.Nil(t, models)
	}
}

func TestSupplierAdmissionCatalog20261019OfficialIDsDoNotVerifySupplierBackends(t *testing.T) {
	for _, tc := range []struct{ name, official, url string }{
		{"GPT 6.1 Sol", "gpt-6.1-sol", "https://developers.openai.com/api/docs/models/gpt-6.1-sol"},
		{"GPT 6 Sol", "gpt-6-sol", "https://developers.openai.com/api/docs/pricing"},
		{"GPT 6 Luna", "gpt-6-luna", "https://developers.openai.com/api/docs/pricing"},
		{"GPT 6 Astra", "gpt-6-astra", "https://developers.openai.com/api/docs/pricing"},
		{"GPT 5.6 Sol", "gpt-5.6-sol", "https://developers.openai.com/api/docs/pricing"},
		{"GPT 5.6 Terra", "gpt-5.6-terra", "https://developers.openai.com/api/docs/pricing"},
		{"GPT 5.6 Luna", "gpt-5.6-luna", "https://developers.openai.com/api/docs/pricing"},
		{"GPT 5.5", "gpt-5.5", "https://developers.openai.com/api/docs/pricing"},
		{"GPT 5.4", "gpt-5.4", "https://developers.openai.com/api/docs/pricing"},
		{"Claude Opus 5.5", "claude-opus-5-5", "https://platform.claude.com/docs/en/models/overview"},
		{"Claude Sonnet 5.5", "claude-sonnet-5-5", "https://platform.claude.com/docs/en/models/overview"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry, err := FindZTAPISupplierAdmissionModel20261019(tc.name)
			require.NoError(t, err)
			raw, err := common.Marshal(entry)
			require.NoError(t, err)
			var declaration map[string]any
			require.NoError(t, common.Unmarshal(raw, &declaration))
			require.Equal(t, tc.official, declaration["known_official_model_id"])
			require.Equal(t, tc.url, declaration["official_model_id_evidence_url"])
			require.Equal(t, "2026-10-19", declaration["official_model_id_verified_date"])
			require.NotEqual(t, tc.official, entry.UpstreamModel)
			require.Equal(t, "candidate", entry.ClaimStatus)
			require.Equal(t, "hold", entry.AdmissionStatus)
			require.Contains(t, entry.Blockers, "live_channel_discovery_and_verification_missing")
			require.NotContains(t, entry.Blockers, "official_model_id_evidence_missing")
		})
	}
}
