package finance

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func revenueTestCost() TestCost {
	return TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, Status: "confirmed", EvidenceType: "supplier_statement",
		Coverage: "included", Amount: "2", Refund: "0", Currency: "USDT", Rate: "1", Evidence: "supplier test receipt", CoverageEvidence: "invoice-1"}
}

func revenueTestFact(c TestCost) Fact {
	return Fact{ID: 3, Kind: "test_cost", Ref: "test:r1:1:2", Payload: c}
}

func TestTestCostRevenueExcludesCashFundedTestsWithoutDoubleCost(t *testing.T) {
	for _, coverage := range []string{"included", "extra"} {
		t.Run(coverage, func(t *testing.T) {
			s := fixture()
			s.Users[0].Role = common.RoleAdminUser
			c := revenueTestCost()
			c.Coverage = coverage
			wantCost, wantProfit := "15.000000", "-15.000000"
			if coverage == "extra" {
				c.CoverageEvidence, c.Refund, c.RefundEvidence = "outside monthly invoice", "0.5", "supplier refund receipt"
				wantCost, wantProfit = "16.500000", "-16.500000"
			}
			f := append(facts(), revenueTestFact(c))
			before, err := common.Marshal([]any{s, f})
			require.NoError(t, err)
			r := calculate(t, s, f)
			require.Equal(t, "0.000000", r.KnownRevenue)
			require.NotNil(t, r.Revenue)
			require.Equal(t, "0.000000", *r.Revenue)
			require.True(t, r.Confirmed, "%+v", r.Issues)
			require.Equal(t, wantCost, *r.ActualCost)
			require.Equal(t, wantProfit, *r.GrossProfit)
			require.Nil(t, r.Margin)
			require.Equal(t, "100.000000", r.ReceiptsTotal, "cash funding is still a receipt, not consumption revenue")
			require.Equal(t, int64(10500000), r.ChargedQuota, "do not rewrite actual wallet consumption")
			require.Len(t, r.TestCosts, 1)
			after, err := common.Marshal([]any{s, f})
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "reporting must not mutate source facts or wallet entries")
		})
	}
}

func TestTestCostRevenueDoesNotExcludeUnmarkedAdminRequests(t *testing.T) {
	s := fixture()
	s.Users[0].Role = common.RoleAdminUser
	r := calculate(t, s, facts())
	require.Equal(t, "20.000000", *r.Revenue)
	s.Ledgers = append(s.Ledgers, ledger(3, -10500000, 42000000, "usage_reservation", "r2", "ztapi:op2:begin"))
	s.Settlements = append(s.Settlements, Settlement{ID: 2, UserID: 1, RequestID: "r2", OperationID: "op2", Status: "settled",
		ReservedQuota: 10500000, ChargedQuota: 10500000, FinalAttempt: 1, CompletedAt: moment("2026-09-16T01:02:00Z").Unix()})
	s.Attempts = append(s.Attempts, Attempt{SettlementID: 2, Attempt: 1, ChannelID: 2, CreatedAt: moment("2026-09-16T01:01:01Z")})
	r = calculate(t, s, append(facts(), revenueTestFact(revenueTestCost())))
	require.Equal(t, "20.000000", *r.Revenue, "only the fact-linked request is a management test")
	require.Equal(t, "5.000000", *r.GrossProfit)
	require.Equal(t, int64(21000000), r.ChargedQuota)
}

func TestTestCostRevenueRejectsInvalidOrAmbiguousFacts(t *testing.T) {
	mutations := map[string]func(*Snapshot, *Fact){
		"wrong ref":     func(_ *Snapshot, f *Fact) { f.Ref = "test:r1:1:3" },
		"wrong payload": func(_ *Snapshot, f *Fact) { f.Payload = Funding{Kind: "cash"} },
		"orphan request": func(_ *Snapshot, f *Fact) {
			c := f.Payload.(TestCost)
			c.RequestID, f.Ref = "missing", "test:missing:1:2"
			f.Payload = c
		},
		"wrong attempt": func(_ *Snapshot, f *Fact) {
			c := f.Payload.(TestCost)
			c.Attempt, f.Ref = 2, "test:r1:2:2"
			f.Payload = c
		},
		"wrong channel": func(_ *Snapshot, f *Fact) {
			c := f.Payload.(TestCost)
			c.ChannelID, f.Ref = 3, "test:r1:1:3"
			f.Payload = c
		},
		"missing attempt":   func(s *Snapshot, _ *Fact) { s.Attempts = nil },
		"zero attempt time": func(s *Snapshot, _ *Fact) { s.Attempts[0].CreatedAt = time.Time{} },
		"future attempt":    func(s *Snapshot, _ *Fact) { s.Attempts[0].CreatedAt = moment("2026-10-27T00:00:00Z") },
		"duplicate attempt": func(s *Snapshot, _ *Fact) { s.Attempts = append(s.Attempts, s.Attempts[0]) },
		"conflicting attempt channel": func(s *Snapshot, _ *Fact) {
			a := s.Attempts[0]
			a.ChannelID = 3
			s.Attempts = append(s.Attempts, a)
		},
		"duplicate request": func(s *Snapshot, _ *Fact) {
			other := s.Settlements[0]
			other.ID = 2
			s.Settlements = append(s.Settlements, other)
		},
		"duplicate settlement id": func(s *Snapshot, _ *Fact) {
			other := s.Settlements[0]
			other.RequestID, other.Status = "other", "released"
			s.Settlements = append(s.Settlements, other)
		},
		"future fact": func(_ *Snapshot, f *Fact) { f.CreatedAt = moment("2026-10-27T00:00:00Z") },
	}
	for name, change := range map[string]func(*TestCost){
		"missing evidence":          func(c *TestCost) { c.Evidence = "" },
		"negative amount":           func(c *TestCost) { c.Amount = "-1" },
		"invalid rate":              func(c *TestCost) { c.Rate = "0" },
		"confirmed quote":           func(c *TestCost) { c.EvidenceType = "quote" },
		"unknown status":            func(c *TestCost) { c.Status = "invalid" },
		"invalid coverage":          func(c *TestCost) { c.Coverage = "invalid" },
		"missing coverage evidence": func(c *TestCost) { c.CoverageEvidence = "" },
		"refund without evidence":   func(c *TestCost) { c.Refund = "1" },
		"refund above cost":         func(c *TestCost) { c.Refund, c.RefundEvidence = "3", "refund receipt" },
		"negative tokens":           func(c *TestCost) { n := int64(-1); c.InputTokens = &n },
		"invalid supplier bill":     func(c *TestCost) { c.SupplierBillID = " padded " },
	} {
		mutations[name] = func(_ *Snapshot, f *Fact) {
			c := f.Payload.(TestCost)
			change(&c)
			f.Payload = c
		}
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s, f := fixture(), revenueTestFact(revenueTestCost())
			mutate(&s, &f)
			r := calculate(t, s, append(facts(), f))
			require.Equal(t, "20.000000", r.KnownRevenue, "invalid test facts cannot hide cash-backed consumption")
			require.NotNil(t, r.Revenue)
			require.Equal(t, "20.000000", *r.Revenue)
		})
	}
}

func TestTestCostRevenueUsesLatestVisibleFactOnce(t *testing.T) {
	c := revenueTestCost()
	c.Coverage, c.CoverageEvidence = "extra", "outside invoice"
	old := revenueTestFact(c)
	c.Amount = "3"
	latest := revenueTestFact(c)
	latest.ID, latest.PreviousID = 4, old.ID
	r := calculate(t, fixture(), append(facts(), old, latest, latest))
	require.Equal(t, "0.000000", *r.Revenue)
	require.Equal(t, "18.000000", *r.ActualCost)
	require.Equal(t, "3.000000", r.TestConfirmedTotal)
	require.Len(t, r.TestCosts, 1)

	c.Evidence = ""
	latest.Payload = c
	r = calculate(t, fixture(), append(facts(), old, latest))
	require.Equal(t, "20.000000", *r.Revenue, "an invalid current revision cannot fall back to a valid old marker")
	latest.CreatedAt = moment("2026-10-27T00:00:00Z")
	r = calculate(t, fixture(), append(facts(), old, latest))
	require.Equal(t, "0.000000", *r.Revenue, "future revisions cannot erase a marker visible as of the report")
	require.Equal(t, "17.000000", *r.ActualCost)
}

func TestTestCostRevenueKeepsEstimateAndUnconvertedCostsUnconfirmed(t *testing.T) {
	for _, kind := range []string{"estimate", "unconverted credits"} {
		t.Run(kind, func(t *testing.T) {
			c := revenueTestCost()
			c.Coverage, c.CoverageEvidence = "unknown", ""
			if kind == "estimate" {
				c.Status, c.EvidenceType = "estimate", "quote"
			} else {
				c.Currency, c.Rate = "XCLOUD_CREDIT", ""
			}
			r := calculate(t, fixture(), append(facts(), revenueTestFact(c)))
			require.Equal(t, "0.000000", *r.Revenue)
			require.Equal(t, "15.000000", r.RecordedCost)
			require.False(t, r.Confirmed)
			require.Nil(t, r.ActualCost)
			require.Nil(t, r.GrossProfit)
			require.Len(t, r.TestCosts, 1)
			if kind == "estimate" {
				require.Equal(t, "2.000000", r.TestEstimateTotal)
				require.Equal(t, "0.000000", r.TestConfirmedTotal)
			} else {
				require.Equal(t, "2.00000000", r.TestXcloudCreditTotal)
				require.Empty(t, r.TestCosts[0].NetUSDT)
			}
		})
	}
}

func TestTestCostRevenueCrossMonthSettlementAndRefund(t *testing.T) {
	s := fixture()
	s.Attempts[0].CreatedAt = moment("2026-09-30T15:59:59Z")
	s.Settlements[0].CompletedAt = moment("2026-09-30T16:01:00Z").Unix()
	f := append(facts(), revenueTestFact(revenueTestCost()))
	r := calculate(t, s, f)
	require.Equal(t, "0.000000", *r.Revenue)
	require.Len(t, r.TestCosts, 1, "test costs follow the attempt month")
	f = append(f, Fact{ID: 4, Kind: "cost", Ref: "2026-10:2", Payload: Cost{Month: "2026-10", ChannelID: 2,
		Supplier: "supplier pool", Gross: "3", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "October invoice"}})
	r, err := Calculate(s, f, "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, "0.000000", *r.Revenue, "a prior-month test attempt still identifies its later settlement")
	require.Empty(t, r.TestCosts)
	require.True(t, r.Confirmed, "%+v", r.Issues)
	require.Equal(t, "-3.000000", *r.GrossProfit)

	s = fixture()
	refund := ledger(3, 5250000, 42000000, "supplier_refund", "r1", "supplier-refund:test")
	refund.CreatedAt = moment("2026-10-01T00:00:00Z")
	s.Ledgers = append(s.Ledgers, refund)
	r = calculate(t, s, f)
	require.Equal(t, "0.000000", *r.Revenue)
	r, err = Calculate(s, f, "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, "0.000000", *r.Revenue, "test refunds cannot create negative customer revenue")
	require.Equal(t, int64(-5250000), r.ChargedQuota)
	refund.CreatedAt = moment("2026-09-17T00:00:00Z")
	s.Ledgers[2] = refund
	r = calculate(t, s, f)
	require.Equal(t, "0.000000", *r.Revenue)
	require.Equal(t, int64(5250000), r.ChargedQuota)
	require.Equal(t, "15.000000", *r.ActualCost)
}

func TestTestCostRevenueExcludesRetryChargesAndTheirRefund(t *testing.T) {
	s := fixture()
	s.Attempts = append(s.Attempts, Attempt{SettlementID: 1, Attempt: 2, ChannelID: 3, CreatedAt: moment("2026-09-16T01:00:03Z")})
	s.Ledgers = append(s.Ledgers, ledger(3, -5250000, 42000000, "usage_settlement", "r1", "attempt-billing:retry"),
		ledger(4, 5250000, 36750000, "supplier_refund", "r1", "supplier-refund:retry"))
	s.Charges = []Charge{{ID: 1, RequestID: "r1", UserID: 1, ChargedQuota: 10500000, OriginalLedgerID: 2},
		{ID: 2, RequestID: "r1", UserID: 1, ChargedQuota: 5250000, OriginalLedgerID: 3, BillingProofID: 1,
			PostedAt: moment("2026-09-16T01:00:03Z").Unix(), ChannelID: 3}}
	s.Refunds = []Refund{{LedgerID: 4, ChargeID: 2, RefundedQuota: 5250000}}
	c := revenueTestCost()
	c.Attempt, c.ChannelID, c.Coverage, c.CoverageEvidence = 2, 3, "extra", "outside monthly invoice"
	f := revenueTestFact(c)
	f.Ref = "test:r1:2:3"
	costFacts := append(facts(), f, Fact{ID: 4, Kind: "cost", Ref: "2026-09:3", Payload: Cost{
		Month: "2026-09", ChannelID: 3, Supplier: "retry supplier", Gross: "5", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "retry invoice"}})
	withoutRefund := s
	withoutRefund.Ledgers, withoutRefund.Refunds = s.Ledgers[:3], nil
	r := calculate(t, withoutRefund, costFacts)
	require.Equal(t, "0.000000", *r.Revenue, "additional attempt charges cannot become customer revenue even before a refund")
	require.Equal(t, int64(15750000), r.ChargedQuota)
	r = calculate(t, s, costFacts)
	require.Equal(t, "0.000000", *r.Revenue)
	require.Equal(t, int64(10500000), r.ChargedQuota)
	require.True(t, r.Confirmed, "%+v", r.Issues)
	require.Equal(t, "22.000000", *r.ActualCost)
	require.Equal(t, "-22.000000", *r.GrossProfit)
}

func TestTestCostRevenueStillRequiresCostForCrossMonthTestSettlement(t *testing.T) {
	s := fixture()
	s.Settlements[0].CompletedAt = moment("2026-10-01T00:00:00Z").Unix()
	f := append(facts(), revenueTestFact(revenueTestCost()), Fact{ID: 4, Kind: "cost", Ref: "2026-10:99", Payload: Cost{
		Month: "2026-10", ChannelID: 99, Supplier: "unrelated supplier", Gross: "1", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "unrelated invoice"}})
	r, err := Calculate(s, f, "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, "0.000000", *r.Revenue)
	require.False(t, r.Confirmed)
	require.Nil(t, r.ActualCost, "excluding test revenue must not remove the obligation to cover its upstream channel")
	require.Nil(t, r.GrossProfit)
	require.Contains(t, r.Issues, Issue{Code: "supplier_cost_missing", Ref: "channel:2"})
}

func TestTestCostRevenueIsExcludedFromSupplierEstimates(t *testing.T) {
	s := fixture()
	s.Channels = []Channel{{ID: 2, Name: "Xcloud", BaseURL: "https://www.xcloudtoken.com"}}
	s.Settlements[0].PriceSnapshotJSON = `{"source_model":"claude-fable-5"}`
	s.Settlements[0].ChargeDimensionsJSON = `[{"dimension":"input_tokens","quantity":1000000}]`
	s.Settlements[0].MissingDimensionsJSON = `[]`
	r := calculate(t, s, append(facts(), revenueTestFact(revenueTestCost())))
	require.Len(t, r.Estimates, 1)
	require.Equal(t, 1, r.Estimates[0].Requests, "excluding revenue must not erase upstream usage or estimated costs")
	require.Equal(t, "0.000000", r.Estimates[0].RevenueUSDT)
	require.Equal(t, int64(1000000), r.Estimates[0].Dimensions["input_tokens"])
}
