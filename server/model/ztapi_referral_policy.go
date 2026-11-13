package model

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ParseZTAPIReferralMicros(value string) (int64, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, ErrZTAPIReferralInvalid
	}
	for _, p := range parts {
		if len(p) == 0 {
			return 0, ErrZTAPIReferralInvalid
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return 0, ErrZTAPIReferralInvalid
			}
		}
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
		if len(frac) > 6 {
			return 0, ErrZTAPIReferralInvalid
		}
	}
	units, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || units > math.MaxInt64/1_000_000 {
		return 0, ErrZTAPIReferralOverflow
	}
	frac += strings.Repeat("0", 6-len(frac))
	micros, err := strconv.ParseInt(frac, 10, 64)
	if err != nil || micros > math.MaxInt64-units*1_000_000 {
		return 0, ErrZTAPIReferralOverflow
	}
	return units*1_000_000 + micros, nil
}

func FormatZTAPIReferralMicros(value int64) string {
	negative := value < 0
	var magnitude uint64
	if negative {
		magnitude = uint64(-(value + 1)) + 1
	} else {
		magnitude = uint64(value)
	}
	frac := strconv.FormatUint(magnitude%1_000_000, 10)
	result := strconv.FormatUint(magnitude/1_000_000, 10) + "." + strings.Repeat("0", 6-len(frac)) + frac
	if negative {
		result = "-" + result
	}
	return result
}

// Each key identifies one allowed model/channel/tier/billing dimension. Explicit
// multipliers are applied once; costs and zero fees both require evidence.
type ZTAPIReferralPriceGateItem struct {
	Key, CustomerRevenue, SupplierCostUpperBound, UsageUpperBound     string
	BonusRate, GroupMultiplier, CashMultiplier, PlatformFee, Evidence string
	ModelMappingKnown, CostKnown, UsageKnown, PlatformFeeKnown        bool
}

type ZTAPIReferralPriceGateInput struct {
	CatalogComplete bool
	CatalogEvidence string
	RequiredKeys    []string
	Items           []ZTAPIReferralPriceGateItem
}

type ZTAPIReferralPriceGateReport struct {
	Allowed        bool     `json:"allowed"`
	BlockedReasons []string `json:"blocked_reasons"`
}

func EvaluateZTAPIReferralPriceGate(in ZTAPIReferralPriceGateInput) (ZTAPIReferralPriceGateReport, error) {
	r := ZTAPIReferralPriceGateReport{BlockedReasons: []string{}}
	block := func(reason string) { r.BlockedReasons = append(r.BlockedReasons, reason) }
	if !in.CatalogComplete || strings.TrimSpace(in.CatalogEvidence) == "" || len(in.RequiredKeys) == 0 {
		block("catalog_or_allowed_routes_unknown")
	}
	if len(in.Items) > 10000 || len(in.RequiredKeys) > 10000 {
		return r, ErrZTAPIReferralInvalid
	}
	seen := make(map[string]bool)
	for _, item := range in.Items {
		if item.Key == "" || seen[item.Key] {
			block("duplicate_or_empty_dimension")
			continue
		}
		seen[item.Key] = true
		if !item.ModelMappingKnown || !item.CostKnown || !item.UsageKnown || !item.PlatformFeeKnown || strings.TrimSpace(item.Evidence) == "" {
			block(item.Key + ":evidence_unknown")
			continue
		}
		if strings.Contains(item.Key, "/spec:image_sale_") {
			if reason := evaluateZTAPIImageSaleQuotedGateItem(item); reason != "" {
				block(item.Key + ":" + reason)
			}
			continue
		}
		values := []string{item.CustomerRevenue, item.SupplierCostUpperBound, item.UsageUpperBound, item.BonusRate, item.GroupMultiplier, item.CashMultiplier, item.PlatformFee}
		d := make([]decimal.Decimal, len(values))
		valid := true
		for i, v := range values {
			// Bounded plain decimals avoid exponent-based allocation/CPU abuse.
			if len(v) == 0 || len(v) > 40 || strings.Count(v, ".") > 1 {
				valid = false
				break
			}
			for _, ch := range v {
				if (ch < '0' || ch > '9') && ch != '.' {
					valid = false
				}
			}
			parsed, err := decimal.NewFromString(v)
			if err != nil || parsed.IsNegative() {
				valid = false
				break
			}
			d[i] = parsed
		}
		if !valid || !d[0].IsPositive() || !d[2].IsPositive() || !d[4].IsPositive() || !d[5].IsPositive() || d[3].GreaterThan(decimal.NewFromInt(1)) {
			block(item.Key + ":amount_or_multiplier_unknown")
			continue
		}
		// Avoid division and rounding at the exact 10% threshold:
		// revenue * (1 - commission - margin) >= (cost + fee) * (1+bonus).
		revenue := d[0]
		cost := d[1].Mul(d[2]).Mul(d[4]).Mul(d[5]).Add(d[6])
		if revenue.Mul(decimal.RequireFromString("0.85")).LessThan(cost.Mul(decimal.NewFromInt(1).Add(d[3]))) {
			block(item.Key + ":post_referral_margin_below_10pct")
		}
	}
	required := make(map[string]bool)
	for _, key := range in.RequiredKeys {
		if key == "" || required[key] || !seen[key] {
			block(key + ":required_dimension_missing_or_duplicate")
		}
		required[key] = true
	}
	for key := range seen {
		if !required[key] {
			block(key + ":dimension_not_in_catalog")
		}
	}
	r.Allowed = len(r.BlockedReasons) == 0
	return r, nil
}

type ZTAPIReferralReadinessInput struct {
	PriceGate                 ZTAPIReferralPriceGateInput
	FinanceReady, WorkerReady bool
	PlatformFeeEvidence       string
	PlatformFeeKnown          bool
}

type ZTAPIReferralReadinessReport struct {
	Ready          bool                         `json:"ready"`
	SchemaReady    bool                         `json:"schema_ready"`
	FinanceReady   bool                         `json:"finance_ready"`
	WorkerReady    bool                         `json:"worker_ready"`
	PriceGate      ZTAPIReferralPriceGateReport `json:"price_gate"`
	BlockedReasons []string                     `json:"blocked_reasons"`
}

func CheckZTAPIReferralReadiness(ctx context.Context, in ZTAPIReferralReadinessInput) (ZTAPIReferralReadinessReport, error) {
	r := ZTAPIReferralReadinessReport{SchemaReady: true, FinanceReady: in.FinanceReady, WorkerReady: in.WorkerReady, BlockedReasons: []string{}}
	db, err := referralDB(ctx)
	if err != nil {
		return r, err
	}
	for _, table := range ZTAPIReferralModels() {
		if !db.Migrator().HasTable(table) {
			r.SchemaReady = false
		}
	}
	r.PriceGate, err = EvaluateZTAPIReferralPriceGate(in.PriceGate)
	if err != nil {
		return r, err
	}
	r.BlockedReasons = append(r.BlockedReasons, r.PriceGate.BlockedReasons...)
	if !r.SchemaReady {
		r.BlockedReasons = append(r.BlockedReasons, "schema_missing")
	}
	if !in.FinanceReady {
		r.BlockedReasons = append(r.BlockedReasons, "finance_capability_unknown")
	}
	if !in.WorkerReady {
		r.BlockedReasons = append(r.BlockedReasons, "worker_not_ready")
	}
	if !in.PlatformFeeKnown || strings.TrimSpace(in.PlatformFeeEvidence) == "" {
		r.BlockedReasons = append(r.BlockedReasons, "platform_fee_unknown")
	}
	r.Ready = len(r.BlockedReasons) == 0
	return r, nil
}

type ZTAPIReferralCampaignUpdate struct {
	ExpectedVersion int64
	OperatorID      int
	Enabled         bool
	Now             time.Time
	Readiness       ZTAPIReferralReadinessInput
}

func GetZTAPIReferralCampaign(ctx context.Context) (*ZTAPIReferralCampaign, error) {
	db, err := referralDB(ctx)
	if err != nil {
		return nil, err
	}
	c := ZTAPIReferralCampaign{ID: 1, RuleVersion: ZTAPIReferralRuleVersion}
	err = db.First(&c, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &ZTAPIReferralCampaign{ID: 1, RuleVersion: ZTAPIReferralRuleVersion}, nil
	}
	return &c, err
}

func SetZTAPIReferralCampaign(ctx context.Context, in ZTAPIReferralCampaignUpdate) (*ZTAPIReferralCampaign, error) {
	if in.OperatorID <= 0 || in.ExpectedVersion < 0 || referralNow(in.Now) != nil {
		return nil, ErrZTAPIReferralInvalid
	}
	if in.Enabled {
		r, err := CheckZTAPIReferralReadiness(ctx, in.Readiness)
		if err != nil {
			return nil, err
		}
		if !r.Ready {
			return nil, ErrZTAPIReferralNotReady
		}
	}
	var c ZTAPIReferralCampaign
	err := referralTransaction(ctx, func(tx *gorm.DB) error {
		seed := ZTAPIReferralCampaign{ID: 1, RuleVersion: ZTAPIReferralRuleVersion}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
			return err
		}
		if err := referralForUpdate(tx).First(&c, 1).Error; err != nil {
			return err
		}
		if c.Version != in.ExpectedVersion || in.Now.Unix() < c.UpdatedAt {
			return ErrZTAPIReferralConflict
		}
		if c.Version == math.MaxInt64 {
			return ErrZTAPIReferralOverflow
		}
		identity, payload, err := referralIdentity(in)
		if err != nil {
			return err
		}
		if c.StartedAt == 0 && in.Enabled {
			c.StartedAt = in.Now.Unix()
		}
		c.Enabled = in.Enabled
		c.Version++
		c.UpdatedAt = in.Now.Unix()
		if err = tx.Save(&c).Error; err != nil {
			return err
		}
		return referralAudit(tx, 0, in.OperatorID, c.ID, "campaign", "campaign:"+strconv.FormatInt(c.Version, 10), identity, payload, in.Now.Unix())
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func referralAnniversary(at time.Time) time.Time {
	at = at.UTC()
	year, month, day := at.Date()
	last := time.Date(year+1, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		day = last
	}
	return time.Date(year+1, month, day, at.Hour(), at.Minute(), at.Second(), 0, time.UTC)
}

// Install once during initialization with a trusted, server-side evidence guard.
// A nil guard is also rejected at the new-binding admission point.
var ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error {
	return ErrZTAPIReferralNotReady
}

// Called only inside the registration transaction after the user insert. This
// helper never starts/commits that transaction and never infers legacy bindings.
func RecordZTAPIReferralBindingTx(tx *gorm.DB, userID, inviterID int, at time.Time) error {
	if tx == nil || userID <= 0 || referralNow(at) != nil {
		return ErrZTAPIReferralInvalid
	}
	if inviterID == 0 {
		return nil
	}
	if inviterID < 0 || userID == inviterID {
		return ErrZTAPIReferralInvalid
	}
	var existing ZTAPIReferralBinding
	err := tx.Where("user_id = ?", userID).First(&existing).Error
	if err == nil {
		if existing.InviterID != inviterID {
			return ErrZTAPIReferralConflict
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var c ZTAPIReferralCampaign
	err = referralForUpdate(tx).First(&c, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !c.Enabled {
		return nil
	}
	if c.StartedAt <= 0 || at.Unix() < c.StartedAt {
		return ErrZTAPIReferralInvalid
	}
	if ZTAPIReferralBindingAdmissionGuard == nil {
		return ErrZTAPIReferralNotReady
	}
	if err = ZTAPIReferralBindingAdmissionGuard(tx, at); err != nil {
		return err
	}
	if err = referralLockAccount(tx, inviterID); err != nil {
		return err
	}
	for _, id := range []int{userID, inviterID} {
		var u User
		query := tx.Select("id", "status")
		if id == inviterID {
			query = referralForUpdate(query)
		}
		if err = query.First(&u, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrZTAPIReferralInvalid
			}
			return err
		}
		if u.Status != common.UserStatusEnabled {
			return ErrZTAPIReferralInvalid
		}
	}
	b := ZTAPIReferralBinding{UserID: userID, InviterID: inviterID, CampaignID: c.ID, BoundAt: at.Unix(), ExpiresAt: referralAnniversary(at).Unix(), CampaignStartedAt: c.StartedAt, RuleVersion: ZTAPIReferralRuleVersion}
	if err = tx.Create(&b).Error; err != nil {
		return err
	}
	identity, payload, err := referralIdentity(b)
	if err != nil {
		return err
	}
	return referralAudit(tx, inviterID, 0, b.ID, "binding", "binding:"+strconv.Itoa(userID), identity, payload, at.Unix())
}
