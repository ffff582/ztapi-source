package model

import (
	_ "embed"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const ZTAPIPricePolicyReduction20261107 ZTAPIPricePolicy = "reduction_15_12_20261107"
const ZTAPIPricePolicyReferralFloor20261107 ZTAPIPricePolicy = "referral_floor_10_20261107"

func isZTAPIDatedRetailPolicy20261107(policy string) bool {
	return policy == string(ZTAPIPricePolicyReduction20261107) || policy == string(ZTAPIPricePolicyReferralFloor20261107)
}

// The manifest is release approval evidence, not runtime configuration.
//
//go:embed ztapi_price_reduction_manifest_20261107.json
var ztapiReductionManifest20261107 string

type ztapiReductionBaseline struct {
	ID          int             `json:"id"`
	Version     uint64          `json:"version"`
	SnapshotID  int64           `json:"snapshot_id"`
	SourceID    int64           `json:"source_id"`
	SourceModel string          `json:"source_model"`
	PublicName  string          `json:"public_name"`
	Channels    string          `json:"channels"`
	PricePolicy string          `json:"price_policy"`
	Input       decimal.Decimal `json:"input"`
	Output      decimal.Decimal `json:"output"`
}

type ztapiReductionModel struct {
	ID           int     `json:"id"`
	Family       string  `json:"family"`
	Protocol     string  `json:"protocol"`
	CacheMode    string  `json:"cache_mode"`
	Rules        string  `json:"rules"`
	Media        string  `json:"media"`
	ReadRatio    float64 `json:"read_ratio"`
	WriteRatio   float64 `json:"write_ratio"`
	Write5mRatio float64 `json:"write5m_ratio"`
	Write1hRatio float64 `json:"write1h_ratio"`
}

type ztapiReductionSource struct {
	ZTAPIModelPriceSource
	Dimensions string `json:"billing_dimensions"`
}

type ZTAPIPriceReductionDimension20261107 struct {
	Model          string `json:"model"`
	SourceModel    string `json:"source_model"`
	Family         string `json:"family"`
	Tier           string `json:"tier"`
	Dimension      string `json:"dimension"`
	QuoteNative    string `json:"quote_native"`
	QuoteCurrency  string `json:"quote_currency"`
	Fraction       string `json:"quotation_fraction"`
	Checksum       string `json:"quote_checksum"`
	Quotation      string `json:"quotation"`
	Upstream       string `json:"upstream_cny"`
	Platform       string `json:"platform_cny"`
	YunxinCost     string `json:"yunxin_cost"`
	XcloudBase     string `json:"xcloud_base"`
	XcloudGroup    string `json:"xcloud_group"`
	XcloudCash     string `json:"xcloud_cash_factor"`
	XcloudCost     string `json:"xcloud_cost"`
	PrimaryChannel int    `json:"primary_channel"`
	PrimaryCost    string `json:"primary_cost"`
	CostCeiling    string `json:"cost_ceiling"`
	OldSale        string `json:"old_sale"`
	ProposedSale   string `json:"proposed_sale"`
	Margin         string `json:"target_margin"`
	Channels       []int  `json:"allowed_channels"`
}

type ztapiReductionManifest struct {
	Baseline []ztapiReductionBaseline               `json:"baseline"`
	Models   []ztapiReductionModel                  `json:"models"`
	Sources  []ztapiReductionSource                 `json:"sources"`
	Rows     []ZTAPIPriceReductionDimension20261107 `json:"rows"`
}

func ztapiReductionApproval20261107(id int) (ztapiReductionBaseline, ztapiReductionModel, ZTAPIModelPriceSource, []ZTAPIPriceReductionDimension20261107, error) {
	var m ztapiReductionManifest
	if err := common.UnmarshalJsonStr(ztapiReductionManifest20261107, &m); err != nil {
		return ztapiReductionBaseline{}, ztapiReductionModel{}, ZTAPIModelPriceSource{}, nil, err
	}
	for _, b := range m.Baseline {
		if b.ID != id {
			continue
		}
		for _, model := range m.Models {
			if model.ID != id {
				continue
			}
			for _, s := range m.Sources {
				if s.ID != b.SourceID {
					continue
				}
				s.BillingDimensions = s.Dimensions
				s.ModelConfigID = id
				rows := []ZTAPIPriceReductionDimension20261107{}
				for _, row := range m.Rows {
					if row.SourceModel == b.SourceModel {
						rows = append(rows, row)
					}
				}
				if len(rows) == 0 {
					break
				}
				return b, model, s.ZTAPIModelPriceSource, rows, nil
			}
		}
	}
	return ztapiReductionBaseline{}, ztapiReductionModel{}, ZTAPIModelPriceSource{}, nil, errors.New("model is outside the dated reduction approval")
}

func ztapiReductionBase20261107(source *ZTAPIModelPriceSource) error {
	if source == nil {
		return errors.New("missing approved original source")
	}
	_, _, expected, _, err := ztapiReductionApproval20261107(source.ModelConfigID)
	if err != nil {
		return err
	}
	// SpendTier was not exported by the read-only baseline. It is inherited,
	// then checked against the persisted parent by the transactional validator.
	expected.SpendTier = source.SpendTier
	if source.ID != expected.ID || source.Version != expected.Version || source.QuotationEffectiveAt != expected.QuotationEffectiveAt ||
		source.CashMarginBaseSourceID != expected.CashMarginBaseSourceID || !ztapiSameDecimal(source.SaleMultiplier, "1") ||
		source.MediaPriceContractJSON != expected.MediaPriceContractJSON || ztapiABPriceSourceEvidenceMismatch(source, &expected) != "" {
		return errors.New("original source differs from frozen approved native-cost evidence")
	}
	return nil
}

func ztapiReductionRowCost20261107(row ZTAPIPriceReductionDimension20261107) (*big.Rat, error) {
	parse := func(s string) (*big.Rat, error) {
		r, ok := new(big.Rat).SetString(s)
		if !ok || r.Sign() <= 0 {
			return nil, errors.New("invalid positive exact quotation decimal")
		}
		return r, nil
	}
	n, err := parse(row.QuoteNative)
	if err != nil {
		return nil, err
	}
	fraction, err := parse(row.Fraction)
	if err != nil {
		return nil, err
	}
	platform, err := parse(row.Platform)
	if err != nil {
		return nil, err
	}
	n.Mul(n, fraction)
	if row.QuoteCurrency == "USD" {
		upstream, err := parse(row.Upstream)
		if err != nil {
			return nil, err
		}
		n.Mul(n, upstream)
	} else if row.QuoteCurrency != "CNY" {
		return nil, errors.New("unsupported native quotation currency")
	}
	n.Quo(n, platform)
	closeTo := func(r *big.Rat, raw string) bool {
		v, ok := new(big.Rat).SetString(raw)
		if !ok {
			return false
		}
		diff := new(big.Rat).Sub(r, v)
		diff.Abs(diff)
		return diff.Cmp(new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(36), nil))) <= 0
	}
	if !closeTo(n, row.YunxinCost) {
		return nil, errors.New("quotation does not reproduce Yunxin cost evidence")
	}
	primary := new(big.Rat).Set(n)
	if len(row.Channels) == 2 {
		if !reflect.DeepEqual(row.Channels, []int{2, 4}) || row.XcloudGroup != "1.5" || row.XcloudCash != "0.5" || row.PrimaryChannel != 4 {
			return nil, errors.New("unapproved Xcloud route or funding factor")
		}
		x, err := parse(row.XcloudBase)
		if err != nil {
			return nil, err
		}
		x.Mul(x, big.NewRat(3, 2)).Mul(x, big.NewRat(1, 2))
		if !closeTo(x, row.XcloudCost) {
			return nil, errors.New("Xcloud factor must apply exactly once")
		}
		primary.Set(x)
		if x.Cmp(n) > 0 {
			n.Set(x)
		}
	}
	if !closeTo(primary, row.PrimaryCost) || !closeTo(n, row.CostCeiling) {
		return nil, errors.New("route ceiling differs from approved native-cost evidence")
	}
	return n, nil
}

func ztapiReductionSale20261107(cost *big.Rat, margin string) (string, error) {
	share, ok := new(big.Rat).SetString(margin)
	if !ok || (margin != "0.15" && margin != "0.12") || cost == nil || cost.Sign() <= 0 {
		return "", errors.New("invalid dated target margin or cost")
	}
	share.Sub(big.NewRat(1, 1), share)
	n := new(big.Rat).Mul(cost, big.NewRat(105, 100))
	n.Quo(n, share).Mul(n, big.NewRat(10000, 1))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n.Num(), n.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return decimal.NewFromBigInt(q, -4).StringFixed(10), nil
}

func deriveZTAPIPriceReduction20261107(base *ZTAPIModelPriceSource, operator int) (ZTAPIModelPriceSource, error) {
	return deriveZTAPIDatedRetail20261107(base, operator, string(ZTAPIPricePolicyReduction20261107))
}

func deriveZTAPIDatedRetail20261107(base *ZTAPIModelPriceSource, operator int, policy string) (ZTAPIModelPriceSource, error) {
	referral := policy == string(ZTAPIPricePolicyReferralFloor20261107)
	if !isZTAPIDatedRetailPolicy20261107(policy) || (referral && (base == nil || !ztapiReferralFloorModel20261107(base.SourceModel))) {
		return ZTAPIModelPriceSource{}, errors.New("model is outside the approved referral price floor")
	}
	if err := ztapiReductionBase20261107(base); err != nil {
		return ZTAPIModelPriceSource{}, err
	}
	b, _, _, rows, err := ztapiReductionApproval20261107(base.ModelConfigID)
	if err != nil || b.ID == 5 || operator <= 0 {
		return ZTAPIModelPriceSource{}, errors.New("Deepseek Flash excluded: uncertain non-Prime Flash alias; retain old price")
	}
	target := *base
	target.ID, target.Version, target.CreatedAt = 0, 0, 0
	target.PricePolicy, target.SaleMultiplier = policy, "1.0000000000"
	target.CashMarginBaseSourceID, target.OperatorID = base.ID, operator
	var rules []ztapiABFrozenSaleRule
	if base.TokenPriceRulesJSON != "" {
		if err := common.UnmarshalJsonStr(base.TokenPriceRulesJSON, &rules); err != nil {
			return target, err
		}
	}
	if len(rules) == 0 && base.MediaPriceContractJSON == "" {
		rules = []ztapiABFrozenSaleRule{{Conditions: []string{}, Sale: map[string]string{}}}
	}
	media, err := func() (ZTAPIMediaPriceContract, error) {
		if base.MediaPriceContractJSON == "" {
			return ZTAPIMediaPriceContract{}, nil
		}
		return parseZTAPIMediaPriceContract(base.MediaPriceContractJSON)
	}()
	if err != nil {
		return target, err
	}
	expectedDimensions := 0
	if len(media.Rules) > 0 {
		for _, rule := range media.Rules {
			expectedDimensions += len(rule.SaleUSD)
		}
	} else if base.TokenPriceRulesJSON != "" {
		for _, rule := range rules {
			expectedDimensions += len(rule.Sale)
		}
	} else {
		dimensions, err := parseZTAPIBillingDimensions(base.BillingDimensions)
		if err != nil {
			return target, err
		}
		expectedDimensions = len(dimensions)
	}
	if len(rows) != expectedDimensions {
		return target, errors.New("approved reduction must cover every original tier and billing dimension")
	}
	var allowedChannels []int
	if err := common.UnmarshalJsonStr(b.Channels, &allowedChannels); err != nil {
		return target, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		key := row.Tier + "/" + row.Dimension
		margin := "0.12"
		if row.Family == "openai" || row.Family == "anthropic" || row.Family == "google" {
			margin = "0.15"
		}
		if row.Family == "gemini" {
			margin = "0.15"
		}
		if seen[key] || row.Margin != margin || row.Model != b.PublicName || row.SourceModel != base.SourceModel || !reflect.DeepEqual(row.Channels, allowedChannels) {
			return target, errors.New("duplicate dimension or target identity mismatch")
		}
		seen[key] = true
		cost, err := ztapiReductionRowCost20261107(row)
		if err != nil {
			return target, err
		}
		if base.SourceModel == "claude-fable-5" && row.Dimension == ZTAPIBillingDimensionCacheWrite5m {
			for _, hourly := range rows {
				if hourly.Tier == row.Tier && hourly.Dimension == ZTAPIBillingDimensionCacheWrite1h {
					cost, err = ztapiReductionRowCost20261107(hourly)
				}
			}
			if err != nil {
				return target, err
			}
		}
		if referral {
			margin = "0.15" // 5% recharge commission leaves the approved 10% quoted margin.
		}
		sale, err := ztapiReductionSale20261107(cost, margin)
		if err != nil {
			return target, err
		}
		if (!referral && !ztapiSameDecimal(sale, row.ProposedSale)) || (referral && decimal.RequireFromString(sale).LessThan(decimal.RequireFromString(row.ProposedSale))) {
			return target, fmt.Errorf("%s %s proposal differs from exact bonus-adjusted floor", b.PublicName, key)
		}
		if len(media.Rules) > 0 {
			found := false
			for i := range media.Rules {
				if media.Rules[i].ID == row.Tier {
					if _, ok := media.Rules[i].SaleUSD[row.Dimension]; !ok {
						return target, errors.New("media dimension absent")
					}
					media.Rules[i].SaleUSD[row.Dimension] = sale
					found = true
				}
			}
			if !found {
				return target, errors.New("media tier absent")
			}
		} else {
			tier, err := strconv.Atoi(row.Tier)
			if err != nil || tier < 1 || tier > len(rules) {
				return target, errors.New("token tier absent")
			}
			rules[tier-1].Sale[row.Dimension] = sale
			rules[tier-1].RetailBasisID = base.ID
		}
	}
	if len(media.Rules) > 0 {
		raw, err := common.Marshal(media)
		if err != nil {
			return target, err
		}
		target.MediaPriceContractJSON = string(raw)
	} else {
		raw, err := common.Marshal(rules)
		if err != nil {
			return target, err
		}
		target.TokenPriceRulesJSON = string(raw)
	}
	if base.SourceModel == "gemini-3.5-flash" {
		// Correct the quotation itself, not just retail: E38 supersedes D113.
		target.SourceDocumentChecksum = ZTAPISupplierWorkbook20261019SHA256
		target.QuotationCell, target.OfficialPriceCell, target.QuotationModelCode = "E38", "", "zq-g35-f"
		target.QuotationEffectiveAt = 1792414023
		for _, row := range rows {
			cost := decimal.RequireFromString(row.QuoteNative).Mul(decimal.RequireFromString("0.86")).Mul(decimal.RequireFromString(row.Upstream))
			if row.Fraction != "0.86" || row.Checksum != target.SourceDocumentChecksum {
				return target, errors.New("Gemini correction evidence mismatch")
			}
			if err := assignZTAPIPriceSourceCost(&target, row.Dimension, cost); err != nil {
				return target, err
			}
		}
	}
	return target, nil
}

func ztapiReductionRules20261107(source *ZTAPIModelPriceSource) ([]ztapiABFrozenSaleRule, map[string]decimal.Decimal, error) {
	if source == nil || !isZTAPIDatedRetailPolicy20261107(source.PricePolicy) || source.OperatorID <= 0 {
		return nil, nil, errors.New("invalid dated reduction source")
	}
	_, _, base, _, err := ztapiReductionApproval20261107(source.ModelConfigID)
	if err != nil {
		return nil, nil, err
	}
	base.SpendTier = source.SpendTier
	base.SaleMultiplier = "1"
	expected, err := deriveZTAPIDatedRetail20261107(&base, source.OperatorID, source.PricePolicy)
	if err != nil {
		return nil, nil, err
	}
	if source.CashMarginBaseSourceID != base.ID || (source.ID > 0 && source.ID <= base.ID) || source.QuotationEffectiveAt != expected.QuotationEffectiveAt ||
		!ztapiSameDecimal(source.SaleMultiplier, "1") || source.MediaPriceContractJSON != expected.MediaPriceContractJSON || ztapiABPriceSourceEvidenceMismatch(source, &expected) != "" {
		return nil, nil, errors.New("dated reduction source differs from approved manifest")
	}
	maximum := map[string]decimal.Decimal{}
	var rules []ztapiABFrozenSaleRule
	if expected.TokenPriceRulesJSON != "" {
		if err := common.UnmarshalJsonStr(expected.TokenPriceRulesJSON, &rules); err != nil {
			return nil, nil, err
		}
	}
	for _, r := range rules {
		for dim, raw := range r.Sale {
			maximum[dim] = decimal.Max(maximum[dim], decimal.RequireFromString(raw))
		}
	}
	if expected.MediaPriceContractJSON != "" {
		media, err := parseZTAPIMediaPriceContract(expected.MediaPriceContractJSON)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range media.Rules {
			for dim, raw := range r.SaleUSD {
				if media.Modality == ZTAPIModalityImage {
					if dim == "image_output" {
						dim = ZTAPIBillingDimensionOutputTokens
					} else {
						dim = ZTAPIBillingDimensionInputTokens
					}
				}
				maximum[dim] = decimal.Max(maximum[dim], decimal.RequireFromString(raw))
			}
		}
	}
	return rules, maximum, nil
}

// ValidateZTAPIPriceReductionSource20261107 re-derives from the original
// persisted source in the caller's transaction; shape validation alone is not authorization.
func ValidateZTAPIPriceReductionSource20261107(db *gorm.DB, source *ZTAPIModelPriceSource) error {
	if _, _, err := ztapiReductionRules20261107(source); err != nil {
		return err
	}
	if db == nil {
		return errors.New("dated reduction validation requires a database")
	}
	var base ZTAPIModelPriceSource
	if err := db.First(&base, source.CashMarginBaseSourceID).Error; err != nil {
		return err
	}
	if err := ztapiReductionBase20261107(&base); err != nil {
		return err
	}
	if !ztapiPublicationPriceBasisPermittedTx(db, &base) {
		return errors.New("original source fails existing financial evidence validation")
	}
	expected, err := deriveZTAPIDatedRetail20261107(&base, source.OperatorID, source.PricePolicy)
	if err != nil {
		return err
	}
	if source.ModelConfigID != base.ModelConfigID || source.SpendTier != base.SpendTier || ztapiABPriceSourceEvidenceMismatch(source, &expected) != "" || source.MediaPriceContractJSON != expected.MediaPriceContractJSON {
		return errors.New("dated reduction source differs from persisted original")
	}
	return nil
}

func ValidateZTAPIPriceReductionRoute20261107(source *ZTAPIModelPriceSource, publicName string, channels []int) error {
	if source == nil || !isZTAPIDatedRetailPolicy20261107(source.PricePolicy) {
		return errors.New("missing dated reduction source")
	}
	b, _, _, _, err := ztapiReductionApproval20261107(source.ModelConfigID)
	var approved []int
	if err != nil || common.UnmarshalJsonStr(b.Channels, &approved) != nil || b.PublicName != publicName || b.SourceModel != source.SourceModel || !reflect.DeepEqual(uniqueSortedInts(channels), approved) {
		return errors.New("dated reduction route differs from original approval")
	}
	return nil
}

func ztapiReductionSortedIDs20261107() []int {
	var m ztapiReductionManifest
	if common.UnmarshalJsonStr(ztapiReductionManifest20261107, &m) != nil {
		return nil
	}
	ids := make([]int, 0, len(m.Baseline))
	for _, b := range m.Baseline {
		ids = append(ids, b.ID)
	}
	sort.Ints(ids)
	return ids
}
