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
	"gorm.io/gorm"
)

// A protected operator-reviewed cost envelope is not a user or admin API input.
// It pins the complete callable catalog and expires; changing a price or route
// invalidates admission of new referrals, never an existing earned obligation.
type ZTAPIReferralCostAttestation struct {
	Version             int                         `json:"version"`
	CatalogSHA256       string                      `json:"catalog_sha256"`
	VerifiedAt          int64                       `json:"verified_at"`
	ExpiresAt           int64                       `json:"expires_at"`
	FinanceCapability   string                      `json:"finance_capability"`
	PlatformFeeKnown    bool                        `json:"platform_fee_known"`
	PlatformFeeEvidence string                      `json:"platform_fee_evidence"`
	PriceGate           ZTAPIReferralPriceGateInput `json:"price_gate"`
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
	if proof.CatalogSHA256 != fingerprint || !reflect.DeepEqual(proof.PriceGate.RequiredKeys, required) {
		return input, []string{"catalog_prices_or_routes_changed_since_cost_review"}, nil
	}
	input.PriceGate = proof.PriceGate
	input.FinanceReady = proof.FinanceCapability == "referral-v1" && ZTAPIReferralFinanceCapabilityCheck != nil && ZTAPIReferralFinanceCapabilityCheck(ctx)
	input.PlatformFeeKnown = proof.PlatformFeeKnown
	input.PlatformFeeEvidence = strings.TrimSpace(proof.PlatformFeeEvidence)
	return input, nil, nil
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
