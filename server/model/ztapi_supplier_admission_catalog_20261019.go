package model

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// These declarations are not discovery, verification, pricing, or publication authority.
type ZTAPISupplierAdmissionModel20261019 struct {
	ModelName                   string   `json:"model_name"`
	SourceModel                 string   `json:"source_model"`
	PublicName                  string   `json:"public_name"`
	UpstreamModel               string   `json:"upstream_model"`
	KnownOfficialModelID        string   `json:"known_official_model_id"`
	OfficialModelIDEvidenceURL  string   `json:"official_model_id_evidence_url"`
	OfficialModelIDVerifiedDate string   `json:"official_model_id_verified_date"`
	ObservedDirectoryModel      string   `json:"observed_directory_model"`
	ProviderFamily              string   `json:"provider_family"`
	Modality                    string   `json:"modality"`
	ResourceType                string   `json:"resource_type"`
	ModelCode                   string   `json:"model_code"`
	QuotationCell               string   `json:"quotation_cell"`
	Fraction                    string   `json:"fraction"`
	BillingProfile              string   `json:"billing_profile"`
	RequiredChannelID           int      `json:"required_channel_id"`
	Blockers                    []string `json:"blockers"`
	QuotationRow                int      `json:"quotation_row"`
	QuoteName                   string   `json:"quote_name"`
	QuoteDisplayName            string   `json:"quote_display_name"`
	QuoteVendor                 string   `json:"quote_vendor"`
	Grade                       string   `json:"grade"`
	ClaimStatus                 string   `json:"claim_status"`
	AdmissionStatus             string   `json:"admission_status"`
	IdentityEvidence            string   `json:"identity_evidence"`
	UpstreamEvidence            string   `json:"upstream_evidence"`
}

var ErrZTAPISupplierAdmissionModelNotFound20261019 = errors.New("supplier_admission_20261019_model_not_found")

//go:embed ztapi_supplier_admission_catalog_20261019.json
var ztapiSupplierAdmissionCatalog20261019JSON []byte

var (
	ztapiSupplierAdmissionCatalog20261019Once    sync.Once
	ztapiSupplierAdmissionCatalog20261019Entries []ZTAPISupplierAdmissionModel20261019
	ztapiSupplierAdmissionCatalog20261019Error   error
)

type ztapiSupplierAdmissionCatalog20261019 struct {
	Version        int                                   `json:"version"`
	WorkbookSHA256 string                                `json:"workbook_sha256"`
	Sheet          string                                `json:"sheet"`
	Entries        []ZTAPISupplierAdmissionModel20261019 `json:"entries"`
}

// ZTAPISupplierAdmissionModels20261019 returns detached candidates, including holds.
// It never imports prices, changes routes, or evaluates a live admission gate.
func ZTAPISupplierAdmissionModels20261019() ([]ZTAPISupplierAdmissionModel20261019, error) {
	entries, err := loadZTAPISupplierAdmissionModels20261019()
	if err != nil {
		return nil, err
	}
	result := make([]ZTAPISupplierAdmissionModel20261019, len(entries))
	for i, entry := range entries {
		result[i] = cloneZTAPISupplierAdmissionModel20261019(entry)
	}
	return result, nil
}

func loadZTAPISupplierAdmissionModels20261019() ([]ZTAPISupplierAdmissionModel20261019, error) {
	ztapiSupplierAdmissionCatalog20261019Once.Do(func() {
		ztapiSupplierAdmissionCatalog20261019Entries, ztapiSupplierAdmissionCatalog20261019Error =
			parseZTAPISupplierAdmissionCatalog20261019(ztapiSupplierAdmissionCatalog20261019JSON)
	})
	return ztapiSupplierAdmissionCatalog20261019Entries, ztapiSupplierAdmissionCatalog20261019Error
}

func cloneZTAPISupplierAdmissionModel20261019(entry ZTAPISupplierAdmissionModel20261019) ZTAPISupplierAdmissionModel20261019 {
	entry.Blockers = append([]string(nil), entry.Blockers...)
	return entry
}

// FindZTAPISupplierAdmissionModel20261019 matches only the exact ModelName.
// Quote codes, source/public/physical IDs, case folding, and suffix inference are not aliases.
func FindZTAPISupplierAdmissionModel20261019(name string) (ZTAPISupplierAdmissionModel20261019, error) {
	entries, err := loadZTAPISupplierAdmissionModels20261019()
	if err != nil {
		return ZTAPISupplierAdmissionModel20261019{}, err
	}
	for _, entry := range entries {
		if entry.ModelName == name {
			return cloneZTAPISupplierAdmissionModel20261019(entry), nil
		}
	}
	return ZTAPISupplierAdmissionModel20261019{}, ErrZTAPISupplierAdmissionModelNotFound20261019
}

func parseZTAPISupplierAdmissionCatalog20261019(raw []byte) ([]ZTAPISupplierAdmissionModel20261019, error) {
	if err := common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("invalid supplier admission catalog: %w", err)
	}
	var catalog ztapiSupplierAdmissionCatalog20261019
	if err := common.DecodeJsonStrict(bytes.NewReader(raw), &catalog); err != nil {
		return nil, fmt.Errorf("invalid supplier admission catalog: %w", err)
	}
	if catalog.Version != 1 || catalog.WorkbookSHA256 != "16171b71f8da8c8f52744f182278fa6ea02db61660bd54f6c7b61e9da990e9aa" ||
		catalog.Sheet == "" || len(catalog.Entries) != 55 {
		return nil, errors.New("supplier admission catalog requires the exact 55-row 2026-10-19 workbook")
	}
	seen := make(map[string]bool, len(catalog.Entries)*4)
	for i, entry := range catalog.Entries {
		if err := validateZTAPISupplierAdmissionModel20261019(entry, i+2); err != nil {
			return nil, fmt.Errorf("invalid supplier admission row %d: %w", i+2, err)
		}
		for kind, value := range map[string]string{"name": entry.ModelName, "code": entry.ModelCode, "source": entry.SourceModel, "public": entry.PublicName} {
			if value == "" {
				continue
			}
			key := kind + ":" + value
			if seen[key] {
				return nil, fmt.Errorf("duplicate supplier admission %s at row %d", kind, entry.QuotationRow)
			}
			seen[key] = true
		}
	}
	return catalog.Entries, nil
}

func validateZTAPISupplierAdmissionModel20261019(entry ZTAPISupplierAdmissionModel20261019, row int) error {
	for _, value := range []string{entry.ModelName, entry.ModelCode, entry.QuoteName, entry.QuoteDisplayName, entry.QuoteVendor,
		entry.ProviderFamily, entry.Modality, entry.ResourceType, entry.BillingProfile, entry.IdentityEvidence} {
		if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("missing or invalid candidate declaration field")
		}
	}
	if entry.QuotationRow != row || entry.QuotationCell != fmt.Sprintf("E%d", row) || entry.QuoteDisplayName != entry.ModelCode {
		return errors.New("quotation row, cell, or original code differs")
	}
	fraction, err := decimal.NewFromString(entry.Fraction)
	if err != nil || !fraction.IsPositive() || fraction.GreaterThan(decimal.NewFromInt(1)) {
		return errors.New("quotation fraction must be in (0, 1]")
	}
	if entry.ClaimStatus != "candidate" || entry.AdmissionStatus != "hold" {
		return errors.New("candidate declarations cannot assert verified supplier admission")
	}
	blockers := make(map[string]bool, len(entry.Blockers))
	for _, blocker := range entry.Blockers {
		if blocker == "" || blocker != strings.TrimSpace(blocker) || blockers[blocker] || strings.ContainsAny(blocker, "\r\n\x00") {
			return errors.New("invalid or duplicate candidate blocker")
		}
		blockers[blocker] = true
	}
	required := []string{"live_channel_discovery_and_verification_missing", "supplier_backend_identity_not_live_verified"}
	if entry.KnownOfficialModelID == "" {
		if entry.OfficialModelIDEvidenceURL != "" || entry.OfficialModelIDVerifiedDate != "" {
			return errors.New("official identity evidence requires a known official model ID")
		}
		required = append(required, "official_model_id_evidence_missing")
	} else {
		evidenceURL, err := url.Parse(entry.OfficialModelIDEvidenceURL)
		officialHost := map[string]string{"openai": "developers.openai.com", "anthropic": "platform.claude.com", "moonshot": "platform.kimi.com", "google": "ai.google.dev"}[entry.ProviderFamily]
		if err != nil || evidenceURL.Scheme != "https" || officialHost == "" || evidenceURL.Host != officialHost || evidenceURL.User != nil ||
			entry.KnownOfficialModelID != entry.SourceModel || entry.OfficialModelIDVerifiedDate != "2026-10-19" || blockers["official_model_id_evidence_missing"] {
			return errors.New("known official ID requires exact identity, dated primary evidence, and separate supplier verification")
		}
	}
	switch entry.Grade {
	case "B":
		if entry.ResourceType != "pool" || entry.RequiredChannelID != 2 {
			return errors.New("B candidates require pool resource and channel 2, without fallback")
		}
	case "A":
		if entry.ResourceType != "enterprise" || entry.RequiredChannelID != 0 {
			return errors.New("A candidates require explicit channel approval; no automatic assignment")
		}
		required = append(required, "channel_assignment_requires_explicit_approval")
	case "S":
		if row != 46 || entry.ProviderFamily != "vidu" || entry.ResourceType != "unresolved" || entry.RequiredChannelID != 0 ||
			entry.SourceModel != "" || entry.PublicName != "" || entry.UpstreamModel != "" {
			return errors.New("the Vidu series quotation does not identify a single SKU or route")
		}
		required = append(required, "series_quote_requires_explicit_sku_breakdown", "resource_type_unresolved")
	default:
		return errors.New("unknown quotation grade")
	}
	if entry.Grade != "S" && (entry.SourceModel == "" || entry.PublicName == "") {
		return errors.New("missing logical candidate identity")
	}
	if entry.UpstreamModel == "" {
		required = append(required, "upstream_physical_model_id_evidence_missing")
	} else if entry.UpstreamEvidence == "" {
		return errors.New("physical candidate requires its evidence boundary")
	}
	if row == 10 || row == 52 {
		if entry.UpstreamModel != "" || entry.ObservedDirectoryModel == "" || entry.KnownOfficialModelID != "" {
			return errors.New("Prime and Turbo directory observations are not established SKU mappings")
		}
		required = append(required, "quote_upstream_exact_version_mismatch")
	}
	switch entry.Modality {
	case "text":
	case "image", "video":
		required = append(required, "media_protocol_and_actual_result_evidence_incomplete", "media_metering_and_price_contract_incomplete")
		if entry.Modality == "video" {
			required = append(required, "video_terminal_recovery_and_idempotent_settlement_unverified")
		}
	default:
		return errors.New("unknown candidate modality")
	}
	for _, blocker := range required {
		if !blockers[blocker] {
			return fmt.Errorf("required hold is missing: %s", blocker)
		}
	}
	return nil
}
