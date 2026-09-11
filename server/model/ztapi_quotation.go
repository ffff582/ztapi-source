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
}

var ztapiMediaPriceIdentities = map[string]ztapiMediaPriceIdentity{
	"gp-image-2": {
		modality: ZTAPIModalityImage, sheet: "\u56fd\u5916\u6a21\u578b", cell: "C42",
		resource: "\u4f01\u4e1a\u8d44\u6e90", discount: "78", currency: "USD",
		contractHash: "13f399079208f8b05d3d902b4d9cbe2e55c1f4db467d9be4fac234732de16126",
	},
	"gm25-fl-IMAGE": {
		modality: ZTAPIModalityImage, sheet: "\u56fd\u5916\u6a21\u578b", cell: "C67",
		resource: "\u4f01\u4e1a\u8d44\u6e90", discount: "82", currency: "USD",
		contractHash: "9e2607140055a66dfaa510541e0b902b85c662f29edc751e10ba94f3d95c944e",
	},
	"seedance-2.0": {
		modality: ZTAPIModalityVideo, sheet: "\u89c6\u9891\u6a21\u578b", cell: "C2",
		resource: "\u539f\u5382\u8d44\u6e90", discount: "95", currency: "CNY", cnyPerUSD: "7.2", fxBuffer: "1.03",
		contractHash: "8bcf34caa0bd7a80a01c057e25e8c24f4b3628d769ac2bc5f1971db51bce4a93",
	},
	"Seedance 2.0 Fast": {
		modality: ZTAPIModalityVideo, sheet: "\u89c6\u9891\u6a21\u578b", cell: "C5",
		resource: "\u539f\u5382\u8d44\u6e90", discount: "95", currency: "CNY", cnyPerUSD: "7.2", fxBuffer: "1.03",
		contractHash: "0197ba25928817a622c716f10863380d6da595ab489b5c5f3f8e0acf73bb1068",
	},
	"Seedance 2.0 Mini": {
		modality: ZTAPIModalityVideo, sheet: "\u89c6\u9891\u6a21\u578b", cell: "C6",
		resource: "\u539f\u5382\u8d44\u6e90", discount: "95", currency: "CNY", cnyPerUSD: "7.2", fxBuffer: "1.03",
		contractHash: "040693b0dcf6b7b9a43afde46b455a0818b185930216bf1607e73f7302f4a051",
	},
}

var ztapiPoolPriceDimensions = []string{
	ZTAPIBillingDimensionInputTokens,
	ZTAPIBillingDimensionOutputTokens,
	ZTAPIBillingDimensionCacheRead,
	ZTAPIBillingDimensionCacheWrite5m,
	ZTAPIBillingDimensionCacheWrite1h,
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
			row.PricePolicy != string(ZTAPIPricePolicyEnterprise40Margin) || row.Currency != identity.currency ||
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
	return ok && entry.Label == identity.entryLabel && len(entry.QuoteRows) == 1 &&
		row.Sheet == identity.sheet && row.Cell == identity.cell && row.Label == identity.rowLabel &&
		row.Resource == identity.resource && row.DiscountPercent == identity.discount &&
		row.PricePolicy == string(ZTAPIPricePolicyPool60Margin)
}

func ztapiPoolPricePolicyContractFromManifest(manifest ztapiQuotationManifest, sourceModel string) (map[string]decimal.Decimal, bool) {
	var matched *ZTAPIQuotationRow
	for _, entry := range manifest.Entries {
		if entry.SourceModel != sourceModel {
			continue
		}
		if matched != nil || len(entry.QuoteRows) != 1 ||
			!ztapiQuotationRowMatchesPoolPolicyIdentity(entry, entry.QuoteRows[0]) {
			return nil, false
		}
		row := entry.QuoteRows[0]
		matched = &row
	}
	if matched == nil || len(matched.RawPriceUSDPerMillion) != len(ztapiPoolPriceDimensions) {
		return nil, false
	}
	discount, err := decimal.NewFromString(matched.DiscountPercent)
	if err != nil || !discount.IsPositive() || discount.GreaterThan(decimal.NewFromInt(100)) {
		return nil, false
	}
	discount = discount.Div(decimal.NewFromInt(100))
	expected := make(map[string]decimal.Decimal, len(ztapiPoolPriceDimensions))
	for _, dimension := range ztapiPoolPriceDimensions {
		raw, ok := matched.RawPriceUSDPerMillion[dimension]
		if !ok {
			return nil, false
		}
		price, err := decimal.NewFromString(raw)
		if err != nil || !price.IsPositive() {
			return nil, false
		}
		expected[dimension] = price.Mul(discount)
	}
	return expected, true
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

// Official/original resources remain the default price basis. The only pool
// bases are the two policy-bearing quotation exceptions frozen in the manifest.
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
