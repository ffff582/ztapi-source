package model

import (
	"reflect"
	"strings"
	"testing"
)

func cashTestInput() CashMarginPreviewRequest {
	return CashMarginPreviewRequest{
		BonusRate: "0.05", TargetMargin: "0.20", SaleScale: 10,
		Models: []CashMarginModel{{
			Name: "zt-haiku", Dimensions: []string{"input_tokens", "output_tokens"},
			Channels: []CashMarginChannel{{
				ID: "xcloud-claude-max", UpstreamModel: "claude-haiku-4-5",
				Currency: "USD", CashUSDTPerUnit: "1", GroupFactor: "1.5", CashFactor: "0.5",
				Tiers: []CashMarginTier{{MinInputTokens: 0, MaxInputTokens: nil, Source: "bill:sha256:example", Prices: map[string]string{"input_tokens": "1", "output_tokens": "5"}}},
			}},
		}},
	}
}

func hasCashBlocker(p CashMarginModelPreview, code string) bool {
	for _, b := range p.Blockers {
		if b.Code == code {
			return true
		}
	}
	return false
}

func TestCashMarginPreviewExactRatesAndMultipleChannels(t *testing.T) {
	r := cashTestInput()
	r.Models[0].Channels = append(r.Models[0].Channels, CashMarginChannel{
		ID: "yunxin", UpstreamModel: "physical-hc", Currency: "USDT", CashUSDTPerUnit: "1", GroupFactor: "1", CashFactor: "1",
		Tiers: []CashMarginTier{{Source: "quote:yunxin", Prices: map[string]string{"input_tokens": "0.8", "output_tokens": "3"}}},
	})
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 1 || len(p.Models[0].Blockers) != 0 {
		t.Fatalf("unexpected preview: %+v", p)
	}
	if got := p.Models[0].FloorUSDTPerMillion["input_tokens"]; got != "1.05" {
		t.Fatalf("input floor = %s", got)
	}
	if got := p.Models[0].FloorUSDTPerMillion["output_tokens"]; got != "4.921875" {
		t.Fatalf("output floor = %s", got)
	}
	if p.BonusRate != "0.05" || p.TargetMargin != "0.2" {
		t.Fatalf("assumptions missing: %+v", p)
	}
}

func TestCashMarginPreviewXcloudGLM53(t *testing.T) {
	r := cashTestInput()
	r.Models[0] = CashMarginModel{
		Name: "glm-5.3", Dimensions: []string{"input_tokens", "output_tokens", "cache_read"},
		Channels: []CashMarginChannel{{
			ID: "xcloud-cn", UpstreamModel: "glm-5.3", Currency: "USD",
			CashUSDTPerUnit: "1", GroupFactor: "0.6", CashFactor: "0.5",
			Tiers: []CashMarginTier{{Source: "xcloud-pricing-20261030", Prices: map[string]string{
				"input_tokens": "8", "output_tokens": "28", "cache_read": "2",
			}}},
		}},
	}
	p, err := PreviewCashMargin(r)
	if err != nil || len(p.Models) != 1 || len(p.Models[0].Blockers) != 0 {
		t.Fatalf("unexpected preview: %+v, error: %v", p, err)
	}
	want := map[string]string{"input_tokens": "3.15", "output_tokens": "11.025", "cache_read": "0.7875"}
	if !reflect.DeepEqual(p.Models[0].FloorUSDTPerMillion, want) {
		t.Fatalf("GLM 5.3 floors = %+v, want %+v", p.Models[0].FloorUSDTPerMillion, want)
	}
}

func TestCashMarginPreviewMissingQuoteAndCacheOverlapBlock(t *testing.T) {
	r := cashTestInput()
	r.Models[0].Dimensions = append(r.Models[0].Dimensions, "cache_write", "cache_write_5m", "cache_write_1h")
	r.Models[0].Channels[0].Tiers[0].Prices["cache_write"] = "1.25"
	r.Models[0].Channels[0].Tiers[0].Prices["cache_write_5m"] = "1.25"
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "overlapping_cache_dimensions") || !hasCashBlocker(p.Models[0], "missing_price") {
		t.Fatalf("missing blockers: %+v", p.Models[0])
	}
	if len(p.Models[0].FloorUSDTPerMillion) != 0 {
		t.Fatalf("blocked model must not have actionable prices: %+v", p.Models[0])
	}
}

func TestCashMarginPreviewTierCoverageAndRoundUp(t *testing.T) {
	r := cashTestInput()
	r.SaleScale = 2
	end := uint64(100)
	r.Models[0].Channels[0].Tiers = []CashMarginTier{
		{MinInputTokens: 0, MaxInputTokens: &end, Source: "bill:1", Prices: map[string]string{"input_tokens": "1", "output_tokens": "5"}},
		{MinInputTokens: 100, Source: "bill:2", Prices: map[string]string{"input_tokens": "1.001", "output_tokens": "4"}},
	}
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Models[0].FloorUSDTPerMillion["input_tokens"]; got != "0.99" {
		t.Fatalf("rounded floor = %q; blockers = %+v", got, p.Models[0].Blockers)
	}
	r.Models[0].Channels[0].Tiers[1].MinInputTokens = 101
	p, err = PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "tier_coverage") {
		t.Fatalf("gap not blocked: %+v", p.Models[0])
	}
}

func TestCashMarginPreviewSevenModelsAndExplicitMissingEvidence(t *testing.T) {
	r := cashTestInput()
	first := r.Models[0]
	r.Models = nil
	for _, name := range []string{"haiku-4.5", "opus-4.6", "opus-4.7", "opus-4.8", "glm-5.3", "unconfirmed-6", "unconfirmed-7"} {
		m := first
		m.Name = name
		r.Models = append(r.Models, m)
	}
	r.Models[6].Channels = nil
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 7 || !hasCashBlocker(p.Models[6], "missing_channel") {
		t.Fatalf("unexpected seven model preview: %+v", p)
	}
}

func TestCashMarginPreviewRejectsBadAssumptionsAndDuplicateEvidence(t *testing.T) {
	r := cashTestInput()
	r.TargetMargin = "1"
	if _, err := PreviewCashMargin(r); err == nil {
		t.Fatal("100% margin accepted")
	}
	r = cashTestInput()
	r.Models[0].Channels = append(r.Models[0].Channels, r.Models[0].Channels[0])
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "duplicate_channel") {
		t.Fatalf("duplicate route accepted: %+v", p.Models[0])
	}
	r = cashTestInput()
	r.Models[0].Channels[0].Tiers[0].Source = " "
	p, err = PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "missing_source") {
		t.Fatalf("missing source accepted: %+v", p.Models[0])
	}
	if !strings.Contains(p.Disclaimer, "assumptions") {
		t.Fatalf("no limitation disclaimer: %q", p.Disclaimer)
	}
}

func TestCashMarginPreviewRejectsPartialTTLAndUnexpectedAggregate(t *testing.T) {
	r := cashTestInput()
	r.Models[0].Dimensions = append(r.Models[0].Dimensions, "cache_write_5m")
	r.Models[0].Channels[0].Tiers[0].Prices["cache_write_5m"] = "1.25"
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "incomplete_ttl_dimensions") {
		t.Fatalf("partial TTL accepted: %+v", p.Models[0])
	}
	r.Models[0].Dimensions = append(r.Models[0].Dimensions, "cache_write_1h")
	r.Models[0].Channels[0].Tiers[0].Prices["cache_write_1h"] = "2"
	r.Models[0].Channels[0].Tiers[0].Prices["cache_write"] = "1.25"
	p, err = PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "unexpected_dimension") || len(p.Models[0].FloorUSDTPerMillion) != 0 {
		t.Fatalf("aggregate counted alongside TTL: %+v", p.Models[0])
	}
}

func TestCashMarginPreviewBlocksEachMissingFallbackAndInvalidFactors(t *testing.T) {
	r := cashTestInput()
	c := r.Models[0].Channels[0]
	c.ID = "fallback"
	c.Tiers = []CashMarginTier{{Source: "fallback", Prices: map[string]string{"input_tokens": "1"}}}
	r.Models[0].Channels = append(r.Models[0].Channels, c)
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "missing_price") || len(p.Models[0].FloorUSDTPerMillion) != 0 {
		t.Fatalf("missing fallback cost: %+v", p.Models[0])
	}
	r.Models[0].Channels[1].Tiers[0].Prices["output_tokens"] = "2"
	r.Models[0].Channels[1].CashFactor = "1.5"
	p, err = PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "invalid_cost_factors") {
		t.Fatalf("invalid discount accepted: %+v", p.Models[0])
	}
}

func TestCashMarginPreviewOverlappingTiersAndNonTerminatingDivision(t *testing.T) {
	r := cashTestInput()
	r.BonusRate, r.TargetMargin, r.SaleScale = "0", "0.7", 2
	r.Models[0].Channels[0].GroupFactor, r.Models[0].Channels[0].CashFactor = "1", "1"
	r.Models[0].Channels[0].Tiers[0].Prices["input_tokens"] = "1"
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Models[0].FloorUSDTPerMillion["input_tokens"]; got != "3.34" {
		t.Fatalf("floor = %s", got)
	}
	end := uint64(100)
	tier := r.Models[0].Channels[0].Tiers[0]
	tier.MaxInputTokens = &end
	r.Models[0].Channels[0].Tiers = []CashMarginTier{tier, {MinInputTokens: 99, Source: "overlap", Prices: tier.Prices}}
	p, err = PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCashBlocker(p.Models[0], "tier_coverage") {
		t.Fatalf("overlap accepted: %+v", p.Models[0])
	}
}

func TestCashMarginPreviewGLMAndClaudeTable(t *testing.T) {
	for _, tc := range []struct{ name, in, out, group, wantIn, wantOut string }{
		{"haiku", "1", "5", "1.5", "0.984375", "4.921875"},
		{"opus", "5", "25", "1.5", "4.921875", "24.609375"},
		{"glm", "4.8", "16.8", "1", "3.15", "11.025"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cashTestInput()
			r.Models[0].Channels[0].GroupFactor = tc.group
			r.Models[0].Channels[0].Tiers[0].Prices = map[string]string{"input_tokens": tc.in, "output_tokens": tc.out}
			p, err := PreviewCashMargin(r)
			if err != nil {
				t.Fatal(err)
			}
			if p.Models[0].FloorUSDTPerMillion["input_tokens"] != tc.wantIn || p.Models[0].FloorUSDTPerMillion["output_tokens"] != tc.wantOut {
				t.Fatalf("unexpected prices: %+v", p.Models[0])
			}
		})
	}
}

func TestCashMarginPreviewCacheDimensionsAreExclusiveAndPriced(t *testing.T) {
	r := cashTestInput()
	r.Models[0].Dimensions = append(r.Models[0].Dimensions, "cache_read", "cache_write_5m", "cache_write_1h")
	prices := r.Models[0].Channels[0].Tiers[0].Prices
	prices["cache_read"], prices["cache_write_5m"], prices["cache_write_1h"] = "0.1", "1.25", "2"
	p, err := PreviewCashMargin(r)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"input_tokens": "0.984375", "output_tokens": "4.921875", "cache_read": "0.0984375", "cache_write_5m": "1.23046875", "cache_write_1h": "1.96875"}
	if !reflect.DeepEqual(p.Models[0].FloorUSDTPerMillion, want) || len(p.Models[0].Costs) != 5 {
		t.Fatalf("unexpected cache preview: %+v", p.Models[0])
	}
}

func TestCashMarginPreviewUnexpectedDimensionBlockersAreDeterministic(t *testing.T) {
	r := cashTestInput()
	for _, k := range []string{"z", "y", "x", "w", "v", "u", "t", "s"} {
		r.Models[0].Channels[0].Tiers[0].Prices[k] = "1"
	}
	for i := 0; i < 30; i++ {
		p, err := PreviewCashMargin(r)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, b := range p.Models[0].Blockers {
			if b.Code == "unexpected_dimension" {
				got = append(got, b.Dimension)
			}
		}
		if !reflect.DeepEqual(got, []string{"s", "t", "u", "v", "w", "x", "y", "z"}) {
			t.Fatalf("unstable blockers: %v", got)
		}
	}
}
