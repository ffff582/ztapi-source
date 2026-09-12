package model

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const ZTAPIQuotationSHA256 = "3671d5d915b222177b584b19c777712f4f9ebbd6497c514cf8fd580115cb56b3"

var (
	ErrZTAPIQuotationModelNotQuoted   = errors.New("quotation_model_not_quoted")
	ErrZTAPIQuotationMappingPending   = errors.New("quotation_mapping_pending")
	ErrZTAPIQuotationIdentityMismatch = errors.New("quotation_identity_mismatch")
	ErrZTAPIQuotationVersionMismatch  = errors.New("quotation_version_mismatch")
	ErrZTAPIQuotationManifestInvalid  = errors.New("quotation_manifest_invalid")
)

//go:embed ztapi_quotation_v1.json
var ztapiQuotationManifestJSON []byte

type ZTAPIQuotationRow struct {
	Sheet                  string            `json:"sheet"`
	Cell                   string            `json:"cell"`
	Label                  string            `json:"label"`
	Resource               string            `json:"resource"`
	DiscountPercent        string            `json:"discount_percent,omitempty"`
	PricePolicy            string            `json:"price_policy,omitempty"`
	Currency               string            `json:"currency,omitempty"`
	CNYPerUSD              string            `json:"cny_per_usd,omitempty"`
	FXBuffer               string            `json:"fx_buffer,omitempty"`
	MediaPriceContractJSON string            `json:"media_price_contract,omitempty"`
	RawPriceUSDPerMillion  map[string]string `json:"raw_price_usd_per_million,omitempty"`
}

type ZTAPIQuotationEntry struct {
	Label          string              `json:"label"`
	SourceModel    string              `json:"source_model"`
	PublicName     string              `json:"public_name"`
	Protocol       string              `json:"protocol"`
	ProviderFamily string              `json:"provider_family"`
	Modality       string              `json:"modality"`
	Status         string              `json:"status"`
	QuoteRows      []ZTAPIQuotationRow `json:"quote_rows"`
}

type ZTAPIMediaQuotationReference struct {
	Modality        string
	Sheet           string
	Cell            string
	Label           string
	Resource        string
	DiscountPercent string
	Currency        string
}

func GetZTAPIMediaQuotationReference(label string) (ZTAPIMediaQuotationReference, bool) {
	identity, ok := ztapiMediaPriceIdentities[strings.TrimSpace(label)]
	if !ok {
		return ZTAPIMediaQuotationReference{}, false
	}
	return ZTAPIMediaQuotationReference{
		Modality: identity.modality,
		Sheet:    identity.sheet, Cell: identity.cell, Label: strings.TrimSpace(label),
		Resource: identity.resource, DiscountPercent: identity.discount, Currency: identity.currency,
	}, true
}

type ztapiQuotationManifest struct {
	Version int                   `json:"version"`
	SHA256  string                `json:"sha256"`
	Entries []ZTAPIQuotationEntry `json:"entries"`
}

type ztapiPoolPricePolicyIdentity struct {
	entryLabel string
	sheet      string
	cell       string
	rowLabel   string
	resource   string
	discount   string
}

type ztapiMediaPriceIdentity struct {
	modality     string
	sheet        string
	cell         string
	resource     string
	discount     string
	pricePolicy  string
	currency     string
	cnyPerUSD    string
	fxBuffer     string
	contractHash string
}

var ztapiPoolPricePolicyIdentities = map[string]ztapiPoolPricePolicyIdentity{
	"claude-fable-5": {
		entryLabel: "cl\u2011fb5", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C8",
		rowLabel: "cl\u2011fb5", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "65",
	},
	"claude-opus-5": {
		entryLabel: "cl\u2011op5", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C9",
		rowLabel: "cl\u2011op5", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "44",
	},
	"claude-opus-4-8":           {entryLabel: "cl-op48", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C10", rowLabel: "cl\u2011op48", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "44"},
	"claude-opus-4-7":           {entryLabel: "cl-op47", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C11", rowLabel: "cl\u2011op47", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "44"},
	"claude-opus-4-6":           {entryLabel: "cl-op46", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C12", rowLabel: "cl\u2011op46", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "44"},
	"claude-sonnet-5":           {entryLabel: "cl-sn5", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C13", rowLabel: "cl\u2011sn5", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "44"},
	"claude-sonnet-4-6":         {entryLabel: "cl-sn46", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C14", rowLabel: "cl\u2011sn46", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "44"},
	"claude-haiku-4-5-20251001": {entryLabel: "cl-hk45", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C15", rowLabel: "cl\u2011hk45", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "44"},
	"gpt-5.6-sol":               {entryLabel: "gp56-sl", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C44", rowLabel: "gp56\u2011sl", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "33"},
	"gpt-5.6-terra":             {entryLabel: "gp56-tr", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C46", rowLabel: "gp56\u2011tr", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "33"},
	"gpt-5.6-luna":              {entryLabel: "gp56-ln", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C48", rowLabel: "gp56-ln", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "33"},
	"gpt-5.5":                   {entryLabel: "gp55", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C50", rowLabel: "gp55", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "33"},
	"gpt-5.4":                   {entryLabel: "gp54", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C52", rowLabel: "gp54", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "33"},
	"gpt-5.4-mini":              {entryLabel: "gp54-mi", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C54", rowLabel: "gp54-mi", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "33"},
	"gpt-image-2":               {entryLabel: "gp-image-2", sheet: "\u56fd\u5916\u6a21\u578b", cell: "C56", rowLabel: "gp-image-2", resource: "\u53f7\u6c60\u8d44\u6e90", discount: "33"},
}

var ztapiMediaPriceIdentities = map[string]ztapiMediaPriceIdentity{
	"gp-image-2": {
		modality: ZTAPIModalityImage, sheet: "\u56fd\u5916\u6a21\u578b", cell: "C56",
		resource: "\u53f7\u6c60\u8d44\u6e90", discount: "33", pricePolicy: string(ZTAPIPricePolicyPoolOfficial80), currency: "USD",
		contractHash: "2cbe422e8a4cadd07f3001d3750e65ef1a90c20c8b6f0d0e56716451bcb6bb41",
	},
	"gm25-fl-IMAGE": {
		modality: ZTAPIModalityImage, sheet: "\u56fd\u5916\u6a21\u578b", cell: "C67",
		resource: "\u4f01\u4e1a\u8d44\u6e90", discount: "82", pricePolicy: string(ZTAPIPricePolicyEnterprise20Margin), currency: "USD",
		contractHash: "5f2a82f9ac86cfbdc1766078282cb918269feb4b0c433b2010793a7a9596174a",
	},
	"seedance-2.0": {
		modality: ZTAPIModalityVideo, sheet: "\u89c6\u9891\u6a21\u578b", cell: "C2",
		resource: "\u539f\u5382\u8d44\u6e90", discount: "95", pricePolicy: string(ZTAPIPricePolicyEnterprise20Margin), currency: "CNY", cnyPerUSD: "7.2", fxBuffer: "1.03",
		contractHash: "6aaa5e2072c7417c1da029ebd8f1231e5d81764a023e2ede9927f34ae7a9c6b5",
	},
	"Seedance 2.0 Fast": {
		modality: ZTAPIModalityVideo, sheet: "\u89c6\u9891\u6a21\u578b", cell: "C5",
		resource: "\u539f\u5382\u8d44\u6e90", discount: "95", pricePolicy: string(ZTAPIPricePolicyEnterprise20Margin), currency: "CNY", cnyPerUSD: "7.2", fxBuffer: "1.03",
		contractHash: "d1eaf7aac89a15b9e202e44254a93def1ef96e4b162948d7b2cbeca8576b723b",
	},
	"Seedance 2.0 Mini": {
		modality: ZTAPIModalityVideo, sheet: "\u89c6\u9891\u6a21\u578b", cell: "C6",
		resource: "\u539f\u5382\u8d44\u6e90", discount: "95", pricePolicy: string(ZTAPIPricePolicyEnterprise20Margin), currency: "CNY", cnyPerUSD: "7.2", fxBuffer: "1.03",
		contractHash: "64cc784bed072b0864055e4dcab6e16489a24b09bc0b1e6d2ee00120fd001427",
	},
}

func ztapiQuotationRowHasPricePolicyData(row ZTAPIQuotationRow) bool {
	return row.PricePolicy != "" || row.DiscountPercent != "" || len(row.RawPriceUSDPerMillion) != 0
}

func ztapiQuotationRowHasMediaPriceData(row ZTAPIQuotationRow) bool {
	return row.Currency != "" || row.CNYPerUSD != "" || row.FXBuffer != "" || row.MediaPriceContractJSON != ""
}

func validateZTAPIMediaQuotationEntry(entry ZTAPIQuotationEntry, identity ztapiMediaPriceIdentity) bool {
	if entry.Modality != identity.modality {
		return false
	}
	switch entry.Label {
	case "gp-image-2":
		if entry.Status != "mapped" || entry.SourceModel != "gpt-image-2" || entry.PublicName != "zt-gp-image-2" ||
			entry.Protocol != ZTAPIProtocolOpenAICompatible || entry.ProviderFamily != ZTAPIProviderOpenAI {
			return false
		}
	case "gm25-fl-IMAGE":
		if entry.Status != "mapped" || entry.SourceModel != "gemini-2.5-flash-image" || entry.PublicName != "zt-gemini-2.5-flash-image" ||
			entry.Protocol != ZTAPIProtocolOpenAICompatible || entry.ProviderFamily != ZTAPIProviderGoogle {
			return false
		}
	default:
		if entry.Status != "mapping_pending" || entry.SourceModel != "" || entry.PublicName != "" ||
			entry.Protocol != "" || entry.ProviderFamily != "" {
			return false
		}
	}
	contractRows := 0
	for _, row := range entry.QuoteRows {
		if row.MediaPriceContractJSON == "" {
			if ztapiQuotationRowHasPricePolicyData(row) || ztapiQuotationRowHasMediaPriceData(row) {
				return false
			}
			continue
		}
		contractRows++
		if row.Sheet != identity.sheet || row.Cell != identity.cell || row.Label != entry.Label ||
			row.Resource != identity.resource || row.DiscountPercent != identity.discount ||
			row.PricePolicy != identity.pricePolicy || row.Currency != identity.currency ||
			row.CNYPerUSD != identity.cnyPerUSD || row.FXBuffer != identity.fxBuffer {
			return false
		}
		canonical, err := canonicalizeZTAPIMediaPriceContract(row.MediaPriceContractJSON)
		if err != nil || canonical != row.MediaPriceContractJSON {
			return false
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
		if digest != identity.contractHash {
			return false
		}
	}
	return contractRows == 1
}

func ztapiQuotationRowMatchesPoolPolicyIdentity(entry ZTAPIQuotationEntry, row ZTAPIQuotationRow) bool {
	identity, ok := ztapiPoolPricePolicyIdentities[entry.SourceModel]
	return ok && entry.Label == identity.entryLabel &&
		row.Sheet == identity.sheet && row.Cell == identity.cell && row.Label == identity.rowLabel &&
		row.Resource == identity.resource && row.DiscountPercent == identity.discount &&
		(row.PricePolicy == string(ZTAPIPricePolicyPool60Margin) || row.PricePolicy == string(ZTAPIPricePolicyPoolOfficial80))
}

func ztapiPoolPricePolicyContractFromManifest(manifest ztapiQuotationManifest, sourceModel string) (map[string]decimal.Decimal, bool) {
	official, discount, ok := ztapiPoolOfficialPriceContractFromManifest(manifest, sourceModel)
	if !ok {
		return nil, false
	}
	expected := make(map[string]decimal.Decimal, len(official))
	for dimension, price := range official {
		expected[dimension] = price.Mul(discount)
	}
	return expected, true
}

func ztapiPoolOfficialPriceContractFromManifest(manifest ztapiQuotationManifest, sourceModel string) (map[string]decimal.Decimal, decimal.Decimal, bool) {
	var matched *ZTAPIQuotationRow
	for _, entry := range manifest.Entries {
		if entry.SourceModel != sourceModel {
			continue
		}
		for i := range entry.QuoteRows {
			if !ztapiQuotationRowMatchesPoolPolicyIdentity(entry, entry.QuoteRows[i]) {
				continue
			}
			if matched != nil {
				return nil, decimal.Zero, false
			}
			row := entry.QuoteRows[i]
			matched = &row
		}
	}
	if matched == nil || len(matched.RawPriceUSDPerMillion) == 0 {
		return nil, decimal.Zero, false
	}
	discount, err := decimal.NewFromString(matched.DiscountPercent)
	if err != nil || !discount.IsPositive() || discount.GreaterThan(decimal.NewFromInt(100)) {
		return nil, decimal.Zero, false
	}
	discount = discount.Div(decimal.NewFromInt(100))
	official := make(map[string]decimal.Decimal, len(matched.RawPriceUSDPerMillion))
	for dimension, raw := range matched.RawPriceUSDPerMillion {
		if _, err := parseZTAPIBillingDimensions(`["` + dimension + `"]`); err != nil {
			return nil, decimal.Zero, false
		}
		price, err := decimal.NewFromString(raw)
		if err != nil || !price.IsPositive() {
			return nil, decimal.Zero, false
		}
		official[dimension] = price
	}
	return official, discount, true
}

func ztapiMediaPriceRowFromManifest(manifest ztapiQuotationManifest, sourceModel string) (ZTAPIQuotationRow, bool) {
	var matched *ZTAPIQuotationRow
	for _, entry := range manifest.Entries {
		if entry.SourceModel != sourceModel {
			continue
		}
		for i := range entry.QuoteRows {
			if entry.QuoteRows[i].MediaPriceContractJSON == "" {
				continue
			}
			if matched != nil {
				return ZTAPIQuotationRow{}, false
			}
			row := entry.QuoteRows[i]
			matched = &row
		}
	}
	if matched == nil {
		return ZTAPIQuotationRow{}, false
	}
	return *matched, true
}

func validateZTAPIPoolPriceSource(source *ZTAPIModelPriceSource, dimensions []string, values map[string]string) error {
	expected, ok := ztapiPoolPricePolicyContractFromManifest(ztapiQuotation, source.SourceModel)
	if !ok || source.Currency != "USD" || source.SourceDocumentChecksum != ZTAPIQuotationSHA256 ||
		len(dimensions) != len(expected) {
		return errors.New("pool price source does not match exact quotation contract")
	}
	billed := make(map[string]struct{}, len(dimensions))
	for _, dimension := range dimensions {
		want, required := expected[dimension]
		got, err := decimal.NewFromString(strings.TrimSpace(values[dimension]))
		if !required || err != nil || !got.Equal(want) {
			return errors.New("pool price source does not match exact quotation cost tuple")
		}
		billed[dimension] = struct{}{}
	}
	for dimension, raw := range values {
		if _, required := billed[dimension]; required {
			continue
		}
		value, err := decimal.NewFromString(strings.TrimSpace(raw))
		if err != nil || !value.IsZero() {
			return errors.New("pool price source contains an unquoted cost dimension")
		}
	}
	return nil
}

func parseZTAPIQuotationManifest(raw []byte) (ztapiQuotationManifest, error) {
	var manifest ztapiQuotationManifest
	if common.Unmarshal(raw, &manifest) != nil || manifest.Version != 1 ||
		manifest.SHA256 != ZTAPIQuotationSHA256 || len(manifest.Entries) != 46 {
		return manifest, ErrZTAPIQuotationManifestInvalid
	}
	labels, sources, aliases, rows := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	cellPattern := regexp.MustCompile(`^C[1-9][0-9]*$`)
	mapped, pending, text, embedding, media := 0, 0, 0, 0, 0
	for _, entry := range manifest.Entries {
		if entry.Label == "" || labels[entry.Label] || len(entry.QuoteRows) == 0 {
			return manifest, ErrZTAPIQuotationManifestInvalid
		}
		labels[entry.Label] = true
		mediaIdentity, mediaEntry := ztapiMediaPriceIdentities[entry.Label]
		if mediaEntry && !validateZTAPIMediaQuotationEntry(entry, mediaIdentity) {
			return manifest, ErrZTAPIQuotationManifestInvalid
		}
		if mediaEntry {
			media++
		}
		for _, row := range entry.QuoteRows {
			key := row.Sheet + "\x00" + row.Cell
			if row.Sheet == "" || !cellPattern.MatchString(row.Cell) || row.Label == "" || row.Resource == "" || rows[key] {
				return manifest, ErrZTAPIQuotationManifestInvalid
			}
			rows[key] = true
			if !mediaEntry && (ztapiQuotationRowHasPricePolicyData(row) || ztapiQuotationRowHasMediaPriceData(row)) {
				if !ztapiQuotationRowMatchesPoolPolicyIdentity(entry, row) {
					return manifest, ErrZTAPIQuotationManifestInvalid
				}
			}
		}
		switch entry.Status {
		case "mapped":
			if entry.SourceModel == "" || entry.PublicName == "" || entry.SourceModel == entry.PublicName ||
				entry.Protocol != ZTAPIProtocolOpenAICompatible || !IsSupportedZTAPIProviderFamily(entry.ProviderFamily) ||
				(entry.Modality != "text" && entry.Modality != "embedding" && entry.Modality != "image") ||
				sources[entry.SourceModel] || aliases[entry.PublicName] {
				return manifest, ErrZTAPIQuotationManifestInvalid
			}
			if entry.Modality == "embedding" {
				exact := map[string]string{"text-emb-ada-002": "text-embedding-ada-002", "text-emb-3-small": "text-embedding-3-small"}
				if exact[entry.Label] != entry.SourceModel || entry.PublicName != "zt-"+entry.SourceModel || entry.ProviderFamily != ZTAPIProviderOpenAI {
					return manifest, ErrZTAPIQuotationManifestInvalid
				}
				embedding++
			} else if entry.Modality == "text" {
				text++
			}
			sources[entry.SourceModel], aliases[entry.PublicName] = true, true
			mapped++
		case "mapping_pending":
			if entry.SourceModel != "" || entry.PublicName != "" || entry.Protocol != "" || entry.ProviderFamily != "" ||
				(entry.Modality != "embedding" && entry.Modality != "image" && entry.Modality != "video") {
				return manifest, ErrZTAPIQuotationManifestInvalid
			}
			pending++
		default:
			return manifest, ErrZTAPIQuotationManifestInvalid
		}
	}
	for source := range sources {
		if aliases[source] {
			return manifest, ErrZTAPIQuotationManifestInvalid
		}
	}
	if mapped != 43 || pending != 3 || text != 39 || embedding != 2 || media != len(ztapiMediaPriceIdentities) {
		return manifest, ErrZTAPIQuotationManifestInvalid
	}
	for sourceModel := range ztapiPoolPricePolicyIdentities {
		if _, ok := ztapiPoolPricePolicyContractFromManifest(manifest, sourceModel); !ok {
			return manifest, ErrZTAPIQuotationManifestInvalid
		}
	}
	return manifest, nil
}

var ztapiQuotation, ztapiQuotationLoadError = parseZTAPIQuotationManifest(ztapiQuotationManifestJSON)

// ZTAPIQuotationEntries returns evidence metadata, never publication authority.
// Pending entries deliberately have no guessed source ID, public alias or protocol.
func ZTAPIQuotationEntries() ([]ZTAPIQuotationEntry, error) {
	if ztapiQuotationLoadError != nil {
		return nil, ztapiQuotationLoadError
	}
	entries := append([]ZTAPIQuotationEntry(nil), ztapiQuotation.Entries...)
	for i := range entries {
		entries[i].QuoteRows = append([]ZTAPIQuotationRow(nil), entries[i].QuoteRows...)
		for j := range entries[i].QuoteRows {
			prices := entries[i].QuoteRows[j].RawPriceUSDPerMillion
			if prices != nil {
				entries[i].QuoteRows[j].RawPriceUSDPerMillion = make(map[string]string, len(prices))
				for dimension, price := range prices {
					entries[i].QuoteRows[j].RawPriceUSDPerMillion[dimension] = price
				}
			}
		}
	}
	return entries, nil
}

// ValidateZTAPIQuotationIdentity uses exact audited identities, not provider
// naming conventions, upstream discovery, mutable aliases or fuzzy matching.
func ValidateZTAPIQuotationIdentity(sourceModel, publicName, protocol, providerFamily, checksum string) error {
	if ztapiQuotationLoadError != nil {
		return ztapiQuotationLoadError
	}
	for _, entry := range ztapiQuotation.Entries {
		if entry.Status == "mapping_pending" {
			if sourceModel == entry.Label {
				return ErrZTAPIQuotationMappingPending
			}
			continue
		}
		if entry.SourceModel != sourceModel {
			continue
		}
		if entry.PublicName != publicName || entry.Protocol != protocol || entry.ProviderFamily != providerFamily {
			return ErrZTAPIQuotationIdentityMismatch
		}
		if checksum != ZTAPIQuotationSHA256 {
			return ErrZTAPIQuotationVersionMismatch
		}
		return nil
	}
	return ErrZTAPIQuotationModelNotQuoted
}

// Validate the price source pinned by each snapshot, never the latest mutable
// price import. This filters old publications without changing historical rows.
func loadZTAPIQuotedPublications(db *gorm.DB) ([]ZTAPIRuntimePublication, error) {
	publications, err := loadZTAPIActivePublications(db)
	if err != nil || len(publications) == 0 {
		return publications, err
	}
	ids := make([]int64, 0, len(publications))
	for _, publication := range publications {
		ids = append(ids, publication.SnapshotID)
	}
	checksums, err := ztapiQuotationChecksums(db, ids)
	if err != nil {
		return nil, err
	}
	result := make([]ZTAPIRuntimePublication, 0, len(publications))
	for _, publication := range publications {
		if err := ValidateZTAPIQuotationIdentity(publication.SourceModel, publication.PublicName,
			publication.Protocol, publication.ProviderFamily, checksums[publication.SnapshotID]); err == nil {
			result = append(result, publication)
		}
	}
	return result, nil
}

func ztapiQuotationChecksums(db *gorm.DB, ids []int64) (map[int64]string, error) {
	if db == nil {
		return nil, errors.New("ZTAPI database is not initialized")
	}
	var evidence []struct {
		ZTAPIModelPriceSource `gorm:"embedded"`
		SnapshotID            int64
		SnapshotPricePolicy   string `gorm:"column:snapshot_price_policy"`
		CacheReadRatio        float64
		CacheCreationRatio    float64
		CacheCreation5mRatio  float64 `gorm:"column:cache_creation_5m_ratio"`
		CacheCreation1hRatio  float64 `gorm:"column:cache_creation_1h_ratio"`
	}
	err := db.Table("ztapi_model_publication_snapshots AS p").
		Select("s.*, p.id AS snapshot_id, p.price_policy AS snapshot_price_policy, p.cache_read_ratio, p.cache_creation_ratio, p.cache_creation5m_ratio AS cache_creation_5m_ratio, p.cache_creation1h_ratio AS cache_creation_1h_ratio").
		Joins("JOIN ztapi_model_price_sources AS s ON s.id = p.price_source_id AND s.model_config_id = p.model_config_id AND s.source_model = p.source_model").
		Where("p.id IN ?", ids).Scan(&evidence).Error
	if err != nil {
		return nil, err
	}
	checksums := make(map[int64]string, len(evidence))
	for _, row := range evidence {
		preview, err := BuildZTAPIModelPricePreview(&row.ZTAPIModelPriceSource)
		if err == nil && row.SnapshotPricePolicy == row.PricePolicy &&
			ztapiEnterprisePriceBasis(row.SourceModel, row.ResourceType) &&
			ztapiCacheRatiosMatchPreview(&ZTAPIModelConfig{
				CacheReadRatio: row.CacheReadRatio, CacheCreationRatio: row.CacheCreationRatio,
				CacheCreation5mRatio: row.CacheCreation5mRatio, CacheCreation1hRatio: row.CacheCreation1hRatio,
			}, preview) {
			checksums[row.SnapshotID] = row.SourceDocumentChecksum
		}
	}
	return checksums, nil
}

// Official/original resources remain the default price basis. Pool pricing is
// limited to the exact policy-bearing identities frozen in the manifest.
func ztapiEnterprisePriceBasis(sourceModel, resourceType string) bool {
	if ztapiQuotationLoadError != nil {
		return false
	}
	for _, entry := range ztapiQuotation.Entries {
		if entry.Status != "mapped" || entry.SourceModel != sourceModel {
			continue
		}
		for _, row := range entry.QuoteRows {
			if (resourceType == "enterprise" || resourceType == "official" || resourceType == "original_resource") &&
				(row.Resource == "\u4f01\u4e1a\u8d44\u6e90" || row.Resource == "\u539f\u5382\u8d44\u6e90") {
				return true
			}
			if resourceType == "pool" && ztapiQuotationRowMatchesPoolPolicyIdentity(entry, row) {
				return true
			}
		}
	}
	return false
}

// Price evidence is rechecked even on warm reads. Database writers may delete
// or alter an old price row without invalidating this process's alias cache.
func filterZTAPIQuotationAliasCache() error {
	ztapiAliasCache.RLock()
	epoch := ztapiAliasCache.epoch
	ids := make([]int64, 0, len(ztapiAliasCache.aliases))
	for _, publication := range ztapiAliasCache.aliases {
		ids = append(ids, publication.SnapshotID)
	}
	ztapiAliasCache.RUnlock()
	if len(ids) == 0 {
		return nil
	}
	checksums, err := ztapiQuotationChecksums(DB, ids)
	if err != nil {
		return err
	}
	ztapiAliasCache.Lock()
	defer ztapiAliasCache.Unlock()
	if epoch != ztapiAliasCache.epoch {
		return ErrZTAPIModelVersionConflict
	}
	valid := func(p ztapiPublishedModel) bool {
		return ValidateZTAPIQuotationIdentity(p.SourceModel, p.PublicName, p.Protocol,
			p.ProviderFamily, checksums[p.SnapshotID]) == nil
	}
	for name, publication := range ztapiAliasCache.aliases {
		if !valid(publication) {
			delete(ztapiAliasCache.aliases, name)
		}
	}
	for name, publication := range ztapiAliasCache.sources {
		if publication.PublicName != "" && !valid(publication) {
			ztapiAliasCache.sources[name] = ztapiPublishedModel{ModelConfigID: publication.ModelConfigID, SourceModel: publication.SourceModel}
		}
	}
	return nil
}
