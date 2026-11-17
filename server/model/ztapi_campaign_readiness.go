package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// A protected operator-reviewed cost envelope is not a user or admin API input.
// It pins the complete callable catalog and expires; changing a price or route
// invalidates admission of new referrals, never an existing earned obligation.
type ZTAPIReferralCostAttestation struct {
	Version                 int                          `json:"version"`
	CatalogSHA256           string                       `json:"catalog_sha256"`
	TransitionCatalogSHA256 string                       `json:"transition_catalog_sha256,omitempty"`
	TransitionExpiresAt     int64                        `json:"transition_expires_at,omitempty"`
	TransitionPriceGate     *ZTAPIReferralPriceGateInput `json:"transition_price_gate,omitempty"`
	VerifiedAt              int64                        `json:"verified_at"`
	ExpiresAt               int64                        `json:"expires_at"`
	FinanceCapability       string                       `json:"finance_capability"`
	PlatformFeeKnown        bool                         `json:"platform_fee_known"`
	PlatformFeeEvidence     string                       `json:"platform_fee_evidence"`
	PriceGate               ZTAPIReferralPriceGateInput  `json:"price_gate"`
}

var ZTAPIReferralFinanceCapabilityCheck func(context.Context) bool
var ZTAPIReferralWorkerCapabilityCheck func(time.Time) bool

func readZTAPIReferralCostAttestation(path string, at time.Time) (ZTAPIReferralCostAttestation, error) {
	var proof ZTAPIReferralCostAttestation
	if path == "" || !filepath.IsAbs(path) || len(path) > 2048 {
		return proof, ErrZTAPIReferralNotReady
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 1<<20 || (runtime.GOOS != "windows" && before.Mode().Perm()&0077 != 0) {
		return proof, ErrZTAPIReferralNotReady
	}
	f, err := os.Open(path)
	if err != nil {
		return proof, ErrZTAPIReferralNotReady
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return proof, ErrZTAPIReferralNotReady
	}
	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || common.Unmarshal(body, &proof) != nil {
		return proof, ErrZTAPIReferralNotReady
	}
	if proof.Version != 1 || proof.VerifiedAt <= 0 || proof.VerifiedAt > at.Unix() || proof.ExpiresAt <= at.Unix() || proof.ExpiresAt-proof.VerifiedAt > 31*86400 || proof.CatalogSHA256 == "" {
		return proof, ErrZTAPIReferralNotReady
	}
	return proof, nil
}

func ZTAPIReferralCatalogIdentity(ctx context.Context, db *gorm.DB) (string, []string, error) {
	if db == nil || ctx == nil {
		return "", nil, ErrZTAPIReferralNotReady
	}
	db = db.WithContext(ctx)
	var configs []ZTAPIModelConfig
	if err := db.Where("published = ?", true).Order("id").Find(&configs).Error; err != nil {
		return "", nil, err
	}
	if len(configs) == 0 || len(configs) > 1000 {
		return "", nil, ErrZTAPIReferralNotReady
	}
	type line struct {
		Config   ZTAPIModelConfig
		Snapshot ZTAPIModelPublicationSnapshot
		Sources  []ZTAPIModelPriceSource
	}
	lines := make([]line, 0, len(configs))
	required := []string{"hosted-tools/search", "hosted-tools/fetch"}
	channelIDs := map[int]bool{}
	for _, config := range configs {
		var snap ZTAPIModelPublicationSnapshot
		if err := db.First(&snap, config.PublicationSnapshotID).Error; err != nil {
			return "", nil, err
		}
		if config.PublicationSnapshotID <= 0 || snap.ModelConfigID != config.ID || snap.ModelVersion != config.Version {
			return "", nil, ErrZTAPIReferralNotReady
		}
		item := line{Config: config, Snapshot: snap}
		sourceID := snap.PriceSourceID
		seen := map[int64]bool{}
		for sourceID > 0 {
			if seen[sourceID] || len(seen) > 8 {
				return "", nil, ErrZTAPIReferralNotReady
			}
			seen[sourceID] = true
			var source ZTAPIModelPriceSource
			if err := db.First(&source, sourceID).Error; err != nil {
				return "", nil, err
			}
			item.Sources = append(item.Sources, source)
			sourceID = source.CashMarginBaseSourceID
		}
		if len(item.Sources) == 0 {
			return "", nil, ErrZTAPIReferralNotReady
		}
		channels := snap.ChannelIDs()
		if len(channels) == 0 {
			return "", nil, ErrZTAPIReferralNotReady
		}
		if snap.ImageSaleContractJSON != "" {
			quoted, err := ztapiImageSaleReferralItems(config, snap, item.Sources[0])
			if err != nil {
				return "", nil, err
			}
			for _, channel := range channels {
				channelIDs[channel] = true
			}
			for _, item := range quoted {
				required = append(required, item.Key)
			}
			lines = append(lines, item)
			continue
		}
		dimensions := []string{}
		if snap.TokenPriceRulesJSON != "" {
			var rules []struct {
				Sale map[string]json.RawMessage `json:"sale"`
			}
			if common.UnmarshalJsonStr(snap.TokenPriceRulesJSON, &rules) != nil {
				return "", nil, ErrZTAPIReferralNotReady
			}
			for tier, rule := range rules {
				for dimension := range rule.Sale {
					dimensions = append(dimensions, "tier:"+strconv.Itoa(tier+1)+"/"+dimension)
				}
			}
		} else if snap.MediaPriceContractJSON != "" {
			var contract struct {
				Rules []struct {
					ID   string                     `json:"id"`
					Sale map[string]json.RawMessage `json:"sale_usd"`
				} `json:"rules"`
			}
			if common.UnmarshalJsonStr(snap.MediaPriceContractJSON, &contract) != nil {
				return "", nil, ErrZTAPIReferralNotReady
			}
			for _, rule := range contract.Rules {
				for dimension := range rule.Sale {
					dimensions = append(dimensions, "spec:"+rule.ID+"/"+dimension)
				}
			}
		} else {
			var declared []string
			if common.UnmarshalJsonStr(item.Sources[0].BillingDimensions, &declared) != nil {
				return "", nil, ErrZTAPIReferralNotReady
			}
			for _, dimension := range declared {
				dimensions = append(dimensions, "tier:1/"+dimension)
			}
		}
		if len(dimensions) == 0 {
			return "", nil, ErrZTAPIReferralNotReady
		}
		// Executable native tools are already included in frozen sale dimensions;
		// the shared dispatch guard rejects unpriced native tools. Do not invent
		// a callable search dimension for an image or non-search model.
		for _, channel := range channels {
			channelIDs[channel] = true
			for _, dimension := range dimensions {
				required = append(required, config.SourceModel+"/"+snap.Protocol+"/channel:"+strconv.Itoa(channel)+"/"+dimension)
			}
		}
		lines = append(lines, item)
	}
	ids := make([]int, 0, len(channelIDs))
	for id := range channelIDs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	type route struct {
		ID              int
		Type            int
		BaseURL         string
		AccountSHA256   string
		OverridesSHA256 string
		Status          int
		Models          string
		ModelMapping    string
		Group           string
		Priority        *int64
		Weight          uint
	}
	var channels []Channel
	if err := db.Select([]string{"id", "type", "base_url", "key", "ztapi_key_ciphertext", "other", "open_ai_organization", "header_override", "param_override", "setting", "settings", "status", "models", "model_mapping", "group", "priority", "weight"}).Where("id IN ?", ids).Order("id").Find(&channels).Error; err != nil {
		return "", nil, err
	}
	if len(channels) != len(ids) {
		return "", nil, ErrZTAPIReferralNotReady
	}
	routes := make([]route, 0, len(channels))
	for _, channel := range channels {
		account, err := common.Marshal([]any{channel.Key, channel.ZTAPIKeyCiphertext, channel.Other, channel.OpenAIOrganization})
		if err != nil {
			return "", nil, err
		}
		overrides, err := common.Marshal([]any{channel.HeaderOverride, channel.ParamOverride, channel.Setting, channel.OtherSettings})
		if err != nil {
			return "", nil, err
		}
		accountHash, overrideHash := sha256.Sum256(account), sha256.Sum256(overrides)
		item := route{ID: channel.Id, Type: channel.Type, BaseURL: channel.GetBaseURL(), AccountSHA256: hex.EncodeToString(accountHash[:]), OverridesSHA256: hex.EncodeToString(overrideHash[:]), Status: channel.Status, Models: channel.Models, Group: channel.Group, Priority: channel.Priority}
		if channel.ModelMapping != nil {
			item.ModelMapping = *channel.ModelMapping
		}
		if channel.Weight != nil {
			item.Weight = *channel.Weight
		}
		routes = append(routes, item)
	}
	var options []Option
	if err := db.Select([]string{"key", "value"}).Where(commonKeyCol+" IN ?", []string{"GroupRatio", "ModelRatio", "CompletionRatio", "ModelPrice", "CacheRatio", "CreateCacheRatio", "CreateCacheRatio1h", "CreateCacheRatio5m"}).Order(commonKeyCol).Find(&options).Error; err != nil {
		return "", nil, err
	}
	sort.Strings(required)
	body, err := common.Marshal(struct {
		Version      int
		SourceCommit string
		Catalog      []line
		Routes       []route
		Options      []Option
		Required     []string
	}{1, os.Getenv("ZTAPI_SOURCE_COMMIT"), lines, routes, options, required})
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), required, nil
}

func ProductionZTAPIReferralReadiness(ctx context.Context, db *gorm.DB, at time.Time) (ZTAPIReferralReadinessInput, []string, error) {
	input := ZTAPIReferralReadinessInput{WorkerReady: os.Getenv("ZTAPI_REFERRAL_WORKER_ENABLED") == "true" && ZTAPIReferralWorkerCapabilityCheck != nil && ZTAPIReferralWorkerCapabilityCheck(at)}
	fingerprint, required, err := ZTAPIReferralCatalogIdentity(ctx, db)
	if err != nil {
		return input, []string{"catalog_evidence_unavailable"}, err
	}
	input.PriceGate.RequiredKeys = required
	proof, err := readZTAPIReferralCostAttestation(os.Getenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE"), at)
	if err != nil {
		return input, []string{"protected_cost_evidence_missing_or_expired", "supplier_model_mapping_and_usage_upper_bound_unverified", "platform_fee_envelope_unverified"}, nil
	}
	// Select costs only for the exact fingerprint they were reviewed against.
	transition := proof.TransitionCatalogSHA256 == fingerprint && proof.TransitionExpiresAt > at.Unix() && proof.TransitionExpiresAt <= proof.ExpiresAt
	gate := proof.PriceGate
	if proof.CatalogSHA256 != fingerprint && transition && proof.TransitionPriceGate != nil {
		gate = *proof.TransitionPriceGate
	}
	if (proof.CatalogSHA256 != fingerprint && !transition) || !reflect.DeepEqual(gate.RequiredKeys, required) {
		return input, []string{"catalog_prices_or_routes_changed_since_cost_review"}, nil
	}
	for _, key := range required {
		if strings.Contains(key, "/spec:image_sale_") {
			expected, err := ZTAPIReferralImageSaleQuoteItems(ctx, db)
			if err != nil {
				return input, []string{"image_sale_quote_source_unavailable"}, err
			}
			if !ztapiImageSaleAttestationMatches(expected, gate.Items) {
				return input, []string{"image_sale_quoted_evidence_missing_or_changed"}, nil
			}
			break
		}
	}
	input.PriceGate = gate
	input.FinanceReady = proof.FinanceCapability == "referral-v1" && ZTAPIReferralFinanceCapabilityCheck != nil && ZTAPIReferralFinanceCapabilityCheck(ctx)
	input.PlatformFeeKnown = proof.PlatformFeeKnown
	input.PlatformFeeEvidence = strings.TrimSpace(proof.PlatformFeeEvidence)
	return input, nil, nil
}

// Produces quoted financial fields, not an authorization or a fee waiver.
// The release operator must attest model mapping and platform fees explicitly.
func ZTAPIReferralImageSaleQuoteItems(ctx context.Context, db *gorm.DB) ([]ZTAPIReferralPriceGateItem, error) {
	if ctx == nil || db == nil {
		return nil, ErrZTAPIReferralNotReady
	}
	db = db.WithContext(ctx)
	var configs []ZTAPIModelConfig
	if err := db.Where("published = ?", true).Order("id").Limit(1001).Find(&configs).Error; err != nil {
		return nil, err
	}
	if len(configs) > 1000 {
		return nil, ErrZTAPIReferralNotReady
	}
	items := []ZTAPIReferralPriceGateItem{}
	for _, config := range configs {
		var snap ZTAPIModelPublicationSnapshot
		if config.PublicationSnapshotID <= 0 {
			return nil, ErrZTAPIReferralNotReady
		}
		if err := db.First(&snap, config.PublicationSnapshotID).Error; err != nil {
			return nil, err
		}
		if snap.ImageSaleContractJSON == "" {
			continue
		}
		var source ZTAPIModelPriceSource
		if err := db.First(&source, snap.PriceSourceID).Error; err != nil {
			return nil, err
		}
		quoted, err := ztapiImageSaleReferralItems(config, snap, source)
		if err != nil {
			return nil, err
		}
		items = append(items, quoted...)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func ztapiImageSaleReferralItems(config ZTAPIModelConfig, snap ZTAPIModelPublicationSnapshot, source ZTAPIModelPriceSource) ([]ZTAPIReferralPriceGateItem, error) {
	if config.ID != snap.ModelConfigID || config.Version != snap.ModelVersion || config.SourceModel != snap.SourceModel || source.ID != snap.PriceSourceID || source.ModelConfigID != config.ID || source.SourceModel != config.SourceModel {
		return nil, ErrZTAPIReferralNotReady
	}
	protocol, canonical, err := types.ParseZTAPIImageProtocolContract(snap.ImageProtocolContractJSON)
	if err != nil || canonical != snap.ImageProtocolContractJSON || protocol.ProviderModel != config.SourceModel {
		return nil, ErrZTAPIReferralNotReady
	}
	operations := []string{"generation"}
	if protocol.Capabilities.SupportsEdits && protocol.Edit != nil {
		operations = append(operations, "edit")
	}
	channels := snap.ChannelIDs()
	if len(channels) == 0 {
		return nil, ErrZTAPIReferralNotReady
	}
	seen := map[int]bool{}
	for _, channel := range channels {
		if channel <= 0 || seen[channel] {
			return nil, ErrZTAPIReferralNotReady
		}
		seen[channel] = true
	}
	items := []ZTAPIReferralPriceGateItem{}
	for _, operation := range operations {
		quote, err := types.QuoteImageSaleReferralCosts(snap.ImageSaleContractJSON, source.MediaPriceContractJSON, operation)
		if err != nil {
			return nil, err
		}
		frozen, err := types.QuoteImageSaleReferralCosts(snap.ImageSaleContractJSON, snap.MediaPriceContractJSON, operation)
		if err != nil || !reflect.DeepEqual(quote.Items, frozen.Items) {
			return nil, ErrZTAPIReferralNotReady
		}
		for _, quoted := range quote.Items {
			evidence, err := types.BuildZTAPIImageSaleReferralEvidence(quote, operation, quoted.Key)
			if err != nil {
				return nil, err
			}
			raw, err := common.Marshal(evidence)
			if err != nil {
				return nil, err
			}
			dimension, err := types.ZTAPIImageSaleReferralDimension(operation, quoted.Key)
			if err != nil {
				return nil, err
			}
			for _, channel := range channels {
				items = append(items, ZTAPIReferralPriceGateItem{Key: config.SourceModel + "/" + snap.Protocol + "/channel:" + strconv.Itoa(channel) + "/" + dimension,
					CustomerRevenue: quoted.CustomerRevenueUSD, BonusRate: "0.05", GroupMultiplier: "1", CashMultiplier: "1", Evidence: string(raw), CostKnown: true, UsageKnown: true})
			}
		}
	}
	return items, nil
}

func ztapiImageSaleAttestationMatches(expected, supplied []ZTAPIReferralPriceGateItem) bool {
	byKey := map[string]ZTAPIReferralPriceGateItem{}
	for _, item := range expected {
		byKey[item.Key] = item
	}
	seen := map[string]bool{}
	for _, item := range supplied {
		want, ok := byKey[item.Key]
		if !ok {
			continue
		}
		if seen[item.Key] || item.SupplierCostUpperBound != "" || item.UsageUpperBound != "" {
			return false
		}
		seen[item.Key] = true
		evidence, err := types.ParseZTAPIImageSaleReferralEvidence(item.Evidence)
		if err != nil {
			return false
		}
		quoted, err := types.ParseZTAPIImageSaleReferralEvidence(want.Evidence)
		if err != nil || !reflect.DeepEqual(evidence, quoted) {
			return false
		}
	}
	return len(seen) == len(expected)
}

func evaluateZTAPIImageSaleQuotedGateItem(item ZTAPIReferralPriceGateItem) string {
	evidence, err := types.ParseZTAPIImageSaleReferralEvidence(item.Evidence)
	if err != nil {
		return "image_sale_quote_evidence_invalid"
	}
	dimension, err := types.ZTAPIImageSaleReferralDimension(evidence.Operation, evidence.Item.Key)
	if err != nil || !strings.HasSuffix(item.Key, "/"+dimension) || item.SupplierCostUpperBound != "" || item.UsageUpperBound != "" {
		return "image_sale_quote_units_invalid"
	}
	values := []string{item.CustomerRevenue, item.BonusRate, item.GroupMultiplier, item.CashMultiplier, item.PlatformFee}
	numbers := make([]decimal.Decimal, len(values))
	for i, raw := range values {
		if len(raw) == 0 || len(raw) > 40 || strings.Count(raw, ".") > 1 {
			return "amount_or_multiplier_unknown"
		}
		for _, ch := range raw {
			if (ch < '0' || ch > '9') && ch != '.' {
				return "amount_or_multiplier_unknown"
			}
		}
		numbers[i], err = decimal.NewFromString(raw)
		if err != nil || numbers[i].IsNegative() {
			return "amount_or_multiplier_unknown"
		}
	}
	price := decimal.RequireFromString(evidence.Item.CustomerRevenueUSD)
	if !numbers[0].Equal(price) || !numbers[1].Equal(decimal.RequireFromString("0.05")) || !numbers[2].Equal(decimal.NewFromInt(1)) || !numbers[3].Equal(decimal.NewFromInt(1)) {
		return "image_sale_quote_units_or_policy_changed"
	}
	cost := decimal.RequireFromString(evidence.Item.QuotedSupplierCostUSD).Add(numbers[4])
	if price.Mul(decimal.RequireFromString("0.85")).LessThan(cost.Mul(decimal.RequireFromString("1.05"))) {
		return "post_referral_quote_margin_below_10pct"
	}
	return ""
}

func RegisterZTAPIReferralProductionAdmissionGuard() {
	ZTAPIReferralBindingAdmissionGuard = func(tx *gorm.DB, at time.Time) error {
		input, blocked, err := ProductionZTAPIReferralReadiness(tx.Statement.Context, tx, at)
		if err != nil {
			return err
		}
		if len(blocked) > 0 {
			return ErrZTAPIReferralNotReady
		}
		report, err := CheckZTAPIReferralReadiness(tx.Statement.Context, input)
		if err != nil {
			return err
		}
		if !report.Ready {
			return ErrZTAPIReferralNotReady
		}
		return nil
	}
}
