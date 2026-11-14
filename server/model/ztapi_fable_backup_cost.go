package model

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Evidence describes a reviewed supplier-to-customer usage bound and fees for
// one dimension. The caller must supply these; a quote alone proves neither.
type ZTAPIFableBackupUsageEnvelope struct {
	UsageUpperBound string
	PlatformFee     string
	Evidence        string
}

// StageZTAPIFableYunxinBackup is for the locked release operator, not an API
// caller. The installer must atomically stage the dual proof, using a
// compare-and-swap against the supplied current proof.
func StageZTAPIFableYunxinBackup(ctx context.Context, operator int, version uint64, snapshotID, primaryID, backupID int64, evidence map[string]ZTAPIFableBackupUsageEnvelope, install func(current, transition ZTAPIReferralCostAttestation) error) (*ZTAPIModelPublicationSnapshot, error) {
	if DB == nil || ctx == nil || install == nil {
		return nil, ErrZTAPIReferralNotReady
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
	return RestoreZTAPIFableYunxinBackup(operator, version, snapshotID, primaryID, backupID, func(tx *gorm.DB) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var config ZTAPIModelConfig
		if err := tx.Where("source_model = ? AND published = ?", "claude-fable-5", true).First(&config).Error; err != nil {
			return err
		}
		var snapshot ZTAPIModelPublicationSnapshot
		if err := tx.First(&snapshot, config.PublicationSnapshotID).Error; err != nil {
			return err
		}
		prospective, required, err := ZTAPIReferralCatalogIdentity(ctx, tx)
		if err != nil {
			return err
		}
		transition, err := BuildZTAPIFableBackupCostTransition(old, prospective, required, &snapshot, evidence, time.Now().UTC())
		if err != nil {
			return err
		}
		current, err := readZTAPIReferralCostAttestation(path, time.Now().UTC())
		if err != nil || !reflect.DeepEqual(current, old) {
			return errors.New("protected live proof changed during Fable publication")
		}
		if err := install(old, transition); err != nil {
			return err
		}
		installed, err := readZTAPIReferralCostAttestation(path, time.Now().UTC())
		if err != nil || !reflect.DeepEqual(installed, transition) {
			return errors.New("protected transition readback failed")
		}
		input, blockers, err := ProductionZTAPIReferralReadiness(ctx, tx, time.Now().UTC())
		if err != nil || len(blockers) != 0 || !input.PlatformFeeKnown || !reflect.DeepEqual(input.PriceGate, *transition.TransitionPriceGate) {
			return errors.New("protected transition does not match the prospective catalog")
		}
		return ctx.Err()
	})
}

// BuildZTAPIFableBackupCostTransition prepares, but never installs, a proof
// for the snapshot in the guarded publication transaction.
func BuildZTAPIFableBackupCostTransition(old ZTAPIReferralCostAttestation, candidate string, required []string, snapshot *ZTAPIModelPublicationSnapshot, evidence map[string]ZTAPIFableBackupUsageEnvelope, at time.Time) (ZTAPIReferralCostAttestation, error) {
	var invalid ZTAPIReferralCostAttestation
	validHash := func(s string) bool { _, err := hex.DecodeString(s); return len(s) == 64 && err == nil }
	if old.Version != 1 || !validHash(old.CatalogSHA256) || !validHash(candidate) || old.CatalogSHA256 == candidate ||
		old.TransitionCatalogSHA256 != "" || old.TransitionPriceGate != nil || old.TransitionExpiresAt != 0 ||
		old.VerifiedAt <= 0 || old.VerifiedAt > at.Unix() || at.Unix() >= old.ExpiresAt || old.ExpiresAt-old.VerifiedAt > 31*86400 ||
		!old.PlatformFeeKnown || strings.TrimSpace(old.PlatformFeeEvidence) == "" || old.FinanceCapability != "referral-v1" {
		return invalid, errors.New("invalid live cost attestation or candidate identity")
	}
	oldVerdict, err := EvaluateZTAPIReferralPriceGate(old.PriceGate)
	if err != nil || !oldVerdict.Allowed {
		return invalid, errors.New("existing cost attestation did not pass")
	}
	if snapshot == nil || snapshot.ID <= 0 || snapshot.PriceSourceID <= 0 || snapshot.ModelConfigID != 48 ||
		snapshot.SourceModel != "claude-fable-5" || snapshot.PublicName != "zt-claude-fable-5" ||
		snapshot.Protocol != ZTAPIProtocolOpenAICompatible || snapshot.ProviderFamily != ZTAPIProviderAnthropic ||
		snapshot.PricePolicy != string(ZTAPIPricePolicyReduction20261107) || snapshot.CacheWritePricingMode != ZTAPIFableCashFloorCacheWriteMode ||
		!slices.Equal(snapshot.ChannelIDs(), []int{2, 4}) {
		return invalid, errors.New("candidate does not match the reviewed Fable cash-floor publication")
	}
	baseline, _, _, quotes, err := ztapiReductionApproval20261107(snapshot.ModelConfigID)
	if err != nil || baseline.SourceModel != snapshot.SourceModel || len(quotes) != 5 || len(evidence) != len(quotes) {
		return invalid, errors.New("Fable backup requires exactly five reviewed usage and cost dimensions")
	}
	var tiers []ztapiABFrozenSaleRule
	if common.UnmarshalJsonStr(snapshot.TokenPriceRulesJSON, &tiers) != nil || len(tiers) != 1 ||
		len(tiers[0].Conditions) != 0 || len(tiers[0].Sale) != len(quotes) {
		return invalid, errors.New("unexpected frozen Fable retail dimensions")
	}
	gate := old.PriceGate
	gate.Items = slices.Clone(old.PriceGate.Items)
	gate.RequiredKeys = slices.Clone(required)
	for _, quote := range quotes {
		if _, err := ztapiReductionRowCost20261107(quote); err != nil {
			return invalid, err
		}
		key := "claude-fable-5/openai_compatible/channel:2/tier:1/" + quote.Dimension
		envelope, ok := evidence[key]
		price, quoted := tiers[0].Sale[quote.Dimension]
		if !ok || !quoted || quote.Tier != "1" || !slices.Equal(quote.Channels, []int{2, 4}) {
			return invalid, errors.New("backup cost dimension or reviewed usage is missing")
		}
		// Convert the frozen native Yunxin quote once, rounding the cash ceiling up.
		cash := decimal.RequireFromString(quote.YunxinCost).Shift(12).Ceil().Shift(-12)
		gate.Items = append(gate.Items, ZTAPIReferralPriceGateItem{Key: key, CustomerRevenue: price,
			SupplierCostUpperBound: cash.StringFixed(12), UsageUpperBound: envelope.UsageUpperBound,
			BonusRate: "0.05", GroupMultiplier: "1", CashMultiplier: "1", PlatformFee: envelope.PlatformFee,
			Evidence:          quote.Quotation + " " + quote.Checksum + "; " + envelope.Evidence,
			ModelMappingKnown: true, CostKnown: true, UsageKnown: strings.TrimSpace(envelope.Evidence) != "",
			PlatformFeeKnown: strings.TrimSpace(envelope.Evidence) != ""})
	}
	verdict, err := EvaluateZTAPIReferralPriceGate(gate)
	if err != nil || !verdict.Allowed {
		return invalid, errors.New("prospective backup cost evidence does not cover the catalog or meet the 10 percent floor")
	}
	next := old
	next.TransitionCatalogSHA256 = candidate
	next.TransitionPriceGate = &gate
	next.TransitionExpiresAt = min(old.ExpiresAt, at.Add(10*time.Minute).Unix())
	return next, nil
}
