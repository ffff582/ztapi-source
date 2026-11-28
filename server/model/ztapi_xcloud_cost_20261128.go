package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// A quote cannot establish a supplier/customer usage ratio or platform fees.
// The locked release operator must review the referenced receipt and supply
// every field, including an explicit zero fee when a waiver is evidenced.
// ReceiptReference is an operator-audited durable receipt reference, not a bill
// ID or a claim that a quote is an actual supplier debit.
// UsageUpperBound is the supplier/customer usage ratio ceiling, not a token
// count. PlatformFee uses the same USD/unit basis as the frozen dimension.
type ZTAPIXcloudUsageEnvelope20261128 struct {
	VerificationID   int64
	ReceiptReference string
	UsageUpperBound  string
	PlatformFee      string
	Evidence         string
}

// ApplyZTAPIXcloudPublicationWithCost20261128 is a release-operator entry point,
// not an HTTP handler. Usage keys are source/protocol/channel:N/tier:N/dimension
// (one-based frozen tier order), and VerificationID must equal req.VerificationID.
// The caller must hold the exclusive release/proof lock for the entire call.
// install must CAS current, durably and atomically install transition, and leave
// current untouched on failure. It must not mutate the catalog or this request.
// No protected file is written here. SQL rollback cannot undo an external write:
// a successfully installed dual proof retains the old primary gate, including
// its expiry, even if later checks or transaction commit fail. Finalizing that
// transition is a separate operator action; this function never promotes it.
func ApplyZTAPIXcloudPublicationWithCost20261128(ctx context.Context, operator int, req ZTAPIXcloudPublish20261128, usage map[string]ZTAPIXcloudUsageEnvelope20261128, install func(current, transition ZTAPIReferralCostAttestation) error) (*ZTAPIModelPublicationSnapshot, error) {
	if DB == nil || ctx == nil || install == nil {
		return nil, ErrZTAPIReferralNotReady
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := os.Getenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE")
	oldHash, oldKeys, err := ZTAPIReferralCatalogIdentity(ctx, DB)
	if err != nil {
		return nil, err
	}
	old, err := readZTAPIReferralCostAttestation(path, time.Now().UTC())
	if err != nil || old.CatalogSHA256 != oldHash || !slices.Equal(oldKeys, old.PriceGate.RequiredKeys) {
		return nil, errors.New("current catalog and protected cost proof differ")
	}
	var current ZTAPIModelConfig
	if err := DB.WithContext(ctx).First(&current, req.ConfigID).Error; err != nil {
		return nil, err
	}
	if current.Version != req.Version || current.PublicationSnapshotID != req.SnapshotID {
		return nil, ErrZTAPIModelVersionConflict
	}
	previous, err := ztapiXcloudPublishedBaseline20261128(DB.WithContext(ctx), current)
	if err != nil {
		return nil, err
	}
	var stagedSource ZTAPIModelPriceSource
	if err := DB.WithContext(ctx).Where("model_config_id = ?", req.ConfigID).First(&stagedSource, req.SourceID).Error; err != nil {
		return nil, err
	}
	// Apply's public price projection can reject zero retail before check(tx).
	// Give the operator a concrete blocker without weakening that shared gate.
	if err := ztapiXcloudCostZeroRetail20261128(&stagedSource); err != nil {
		return nil, err
	}
	// Clone the operator's inputs before entering the candidate transaction.
	usageCopy := make(map[string]ZTAPIXcloudUsageEnvelope20261128, len(usage))
	for key, envelope := range usage {
		usageCopy[key] = envelope
	}
	before := func(tx *gorm.DB) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// This runs under Apply's catalog lock, before routes or snapshots change.
		// An unchanged proof alone cannot authorize an unreviewed catalog change.
		baselineHash, baselineKeys, err := ZTAPIReferralCatalogIdentity(ctx, tx)
		if err != nil {
			return fmt.Errorf("xcloud_cost_baseline_changed: catalog identity unavailable: %w", err)
		}
		if baselineHash != oldHash || !slices.Equal(baselineKeys, oldKeys) {
			return errors.New("xcloud_cost_baseline_changed: catalog fingerprint or required keys differ")
		}
		live, err := readZTAPIReferralCostAttestation(path, time.Now().UTC())
		if os.Getenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE") != path || err != nil || !reflect.DeepEqual(live, old) {
			return errors.New("xcloud_cost_baseline_changed: protected live proof differs")
		}
		return nil
	}
	return applyZTAPIXcloudPublicationChecks20261128(operator, req, before, func(tx *gorm.DB) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx = tx.WithContext(ctx)
		var active ZTAPIModelConfig
		if err := tx.First(&active, req.ConfigID).Error; err != nil {
			return err
		}
		var snapshot ZTAPIModelPublicationSnapshot
		if err := tx.First(&snapshot, active.PublicationSnapshotID).Error; err != nil {
			return err
		}
		var source ZTAPIModelPriceSource
		if err := tx.First(&source, req.SourceID).Error; err != nil {
			return err
		}
		candidate, required, err := ZTAPIReferralCatalogIdentity(ctx, tx)
		if err != nil {
			return err
		}
		transition, err := ztapiXcloudCostTransition20261128(old, candidate, required, previous, &snapshot, &source, req.VerificationID, usageCopy, time.Now().UTC())
		if err != nil {
			return err
		}
		live, err := readZTAPIReferralCostAttestation(path, time.Now().UTC())
		if os.Getenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE") != path || err != nil || !reflect.DeepEqual(live, old) {
			return errors.New("protected live proof changed during Xcloud publication")
		}
		// Keep our expected proof independent of callback-owned slices/pointers.
		installOld, err := ztapiXcloudCloneCostProof20261128(old)
		if err != nil {
			return err
		}
		installNext, err := ztapiXcloudCloneCostProof20261128(transition)
		if err != nil {
			return err
		}
		if err := install(installOld, installNext); err != nil {
			return err
		}
		installed, err := readZTAPIReferralCostAttestation(path, time.Now().UTC())
		if os.Getenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE") != path || err != nil || !reflect.DeepEqual(installed, transition) {
			return errors.New("protected Xcloud transition readback failed")
		}
		input, blockers, err := ProductionZTAPIReferralReadiness(ctx, tx, time.Now().UTC())
		if err != nil {
			return err
		}
		if len(blockers) != 0 || !input.PlatformFeeKnown || strings.TrimSpace(input.PlatformFeeEvidence) == "" ||
			!input.FinanceReady || !input.WorkerReady || !reflect.DeepEqual(input.PriceGate, *transition.TransitionPriceGate) {
			return fmt.Errorf("protected Xcloud transition readiness failed (finance=%t worker=%t): %s", input.FinanceReady, input.WorkerReady, strings.Join(blockers, "; "))
		}
		verdict, err := EvaluateZTAPIReferralPriceGate(input.PriceGate)
		if err != nil || !verdict.Allowed {
			return fmt.Errorf("protected Xcloud transition price gate failed: %s", strings.Join(verdict.BlockedReasons, "; "))
		}
		return ctx.Err()
	})
}

func ztapiXcloudCostZeroRetail20261128(source *ZTAPIModelPriceSource) error {
	if _, err := ztapiXcloudRules20261128(source); err != nil {
		return err
	}
	_, _, protocol, err := ZTAPIXcloudPublicIdentity20261128(source.SourceModel)
	if err != nil {
		return err
	}
	channel := 0
	for _, id := range []int{4, 5, 6, 7, 8} {
		if group, _ := ZTAPIXcloudChannelGroup20261128(id); group == source.SpendTier {
			channel = id
		}
	}
	var rules []ztapiABFrozenSaleRule
	if common.UnmarshalJsonStr(source.TokenPriceRulesJSON, &rules) != nil || channel == 0 {
		return errors.New("exact Xcloud frozen retail and channel are required")
	}
	for i, rule := range rules {
		for dimension, price := range rule.Sale {
			if ztapiSameDecimal(price, "0") {
				return fmt.Errorf("xcloud_zero_retail_dimension_unsupported: %s/%s/channel:%d/tier:%d/%s (generic cost gate requires positive retail; candidate remains unpublished)", source.SourceModel, protocol, channel, i+1, dimension)
			}
		}
	}
	return nil
}

func ztapiXcloudCloneCostProof20261128(proof ZTAPIReferralCostAttestation) (ZTAPIReferralCostAttestation, error) {
	var copy ZTAPIReferralCostAttestation
	raw, err := common.Marshal(proof)
	if err != nil {
		return copy, err
	}
	err = common.Unmarshal(raw, &copy)
	return copy, err
}

func ztapiXcloudCostTransition20261128(old ZTAPIReferralCostAttestation, candidate string, required []string, previous, snapshot *ZTAPIModelPublicationSnapshot, source *ZTAPIModelPriceSource, verificationID int64, usage map[string]ZTAPIXcloudUsageEnvelope20261128, at time.Time) (ZTAPIReferralCostAttestation, error) {
	var invalid ZTAPIReferralCostAttestation
	if old.Version != 1 || !referralHex64(old.CatalogSHA256) || !referralHex64(candidate) || old.CatalogSHA256 == candidate ||
		old.TransitionCatalogSHA256 != "" || old.TransitionPriceGate != nil || old.TransitionExpiresAt != 0 ||
		old.VerifiedAt <= 0 || old.VerifiedAt > at.Unix() || at.Unix() >= old.ExpiresAt || old.ExpiresAt-old.VerifiedAt > 31*86400 ||
		!old.PlatformFeeKnown || strings.TrimSpace(old.PlatformFeeEvidence) == "" || old.FinanceCapability != "referral-v1" {
		return invalid, errors.New("invalid live Xcloud cost attestation or candidate identity")
	}
	oldVerdict, err := EvaluateZTAPIReferralPriceGate(old.PriceGate)
	if err != nil || !oldVerdict.Allowed {
		return invalid, errors.New("existing protected cost attestation did not pass")
	}
	if err := ztapiValidateXcloudSnapshot20261128(source, snapshot); err != nil {
		return invalid, err
	}
	if snapshot.ID <= 0 || source.ID <= 0 || verificationID <= 0 {
		return invalid, errors.New("persisted exact Xcloud snapshot, source and verification are required")
	}
	var verifications []int64
	if common.UnmarshalJsonStr(snapshot.VerificationIDs, &verifications) != nil || !slices.Contains(verifications, verificationID) {
		return invalid, errors.New("receipt verification does not match the candidate snapshot")
	}
	quotes, _, _, err := ztapiXcloudSourceQuotes20261128(source.SourceModel, source.SpendTier)
	if err != nil {
		return invalid, err
	}
	var rules []ztapiABFrozenSaleRule
	if common.UnmarshalJsonStr(snapshot.TokenPriceRulesJSON, &rules) != nil || len(rules) != len(quotes) {
		return invalid, errors.New("candidate frozen Xcloud retail tiers are missing")
	}
	channel := snapshot.ChannelIDs()[0]
	replacements := map[string]ZTAPIReferralPriceGateItem{}
	for i, quote := range quotes {
		for dimension, cash := range quote.CashRates {
			key := fmt.Sprintf("%s/%s/channel:%d/tier:%d/%s", source.SourceModel, snapshot.Protocol, channel, i+1, dimension)
			price := rules[i].Sale[dimension]
			if ztapiSameDecimal(price, "0") {
				return invalid, fmt.Errorf("xcloud_zero_retail_dimension_unsupported: %s (generic cost gate requires positive retail; candidate remains unpublished)", key)
			}
			envelope, ok := usage[key]
			if !ok || envelope.VerificationID != verificationID || strings.TrimSpace(envelope.ReceiptReference) == "" ||
				strings.TrimSpace(envelope.Evidence) == "" || envelope.UsageUpperBound == "" || envelope.PlatformFee == "" {
				return invalid, fmt.Errorf("receipt-bound usage upper bound and platform fee are required: %s", key)
			}
			// CashRates already includes supplier group and recharge discounts.
			replacements[key] = ZTAPIReferralPriceGateItem{Key: key, CustomerRevenue: price, SupplierCostUpperBound: cash.String(),
				UsageUpperBound: envelope.UsageUpperBound, PlatformFee: envelope.PlatformFee, BonusRate: "0.05", GroupMultiplier: "1", CashMultiplier: "1",
				Evidence:          fmt.Sprintf("Xcloud source:%d quote:%s verification:%d receipt:%s; %s", source.ID, source.SourceDocumentChecksum, verificationID, envelope.ReceiptReference, envelope.Evidence),
				ModelMappingKnown: true, CostKnown: true, UsageKnown: true, PlatformFeeKnown: true}
		}
	}
	if len(usage) != len(replacements) {
		return invalid, errors.New("Xcloud usage evidence must cover exactly the candidate dimensions")
	}
	oldTarget := func(key string) bool {
		if previous == nil {
			return false
		}
		for _, id := range previous.ChannelIDs() {
			if strings.HasPrefix(key, fmt.Sprintf("%s/%s/channel:%d/tier:", previous.SourceModel, previous.Protocol, id)) {
				return true
			}
		}
		return false
	}
	if previous != nil && (previous.ModelConfigID != snapshot.ModelConfigID || previous.SourceModel != snapshot.SourceModel || previous.Protocol != snapshot.Protocol) {
		return invalid, errors.New("previous Xcloud publication is not the target source")
	}
	gate := old.PriceGate
	gate.RequiredKeys = slices.Clone(required)
	gate.Items = make([]ZTAPIReferralPriceGateItem, 0, len(old.PriceGate.Items)+len(replacements))
	for _, item := range old.PriceGate.Items {
		if oldTarget(item.Key) {
			continue
		}
		gate.Items = append(gate.Items, item)
	}
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		gate.Items = append(gate.Items, replacements[key])
	}
	verdict, err := EvaluateZTAPIReferralPriceGate(gate)
	if err != nil || !verdict.Allowed {
		return invalid, fmt.Errorf("prospective Xcloud cost evidence or 10 percent floor failed: %s", strings.Join(verdict.BlockedReasons, "; "))
	}
	next := old
	next.TransitionCatalogSHA256 = candidate
	next.TransitionPriceGate = &gate
	next.TransitionExpiresAt = min(old.ExpiresAt, at.Add(10*time.Minute).Unix())
	return next, nil
}
