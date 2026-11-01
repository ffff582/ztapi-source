package finance

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func supportUsageFixture() support.FinancialUsage {
	in, out, cache := int64(100), int64(0), int64(20)
	quote, credit := "0.03", "0.06"
	completed := moment("2026-09-16T01:01:00Z")
	return support.FinancialUsage{CallID: "support-call-1", StartedAt: moment("2026-09-16T01:00:00Z"), CompletedAt: &completed,
		ChannelID: 2, Supplier: "Xcloud", Model: "deepseek-flash", Purpose: "customer_service", Environment: "production",
		Dispatched: true, InputTokens: &in, OutputTokens: &out, CacheReadTokens: &cache, InputTokenScope: "inclusive",
		QuoteCostU: &quote, UpstreamCredit: &credit, QuoteEvidence: "frozen group quote and cash conversion"}
}

func snapshotWithSupport(t *testing.T, s Snapshot, status string, usages ...support.FinancialUsage) Snapshot {
	t.Helper()
	b, err := common.Marshal(s)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, common.Unmarshal(b, &fields))
	fields["support_usages"], fields["support_source_status"] = usages, status
	b, err = common.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(b, &s))
	return s
}

func TestSupportUsageQuoteIsAutomaticAndNeverCustomerSettlement(t *testing.T) {
	s := snapshotWithSupport(t, fixture(), "available", supportUsageFixture())
	before, err := common.Marshal(s)
	require.NoError(t, err)
	f := facts()
	factsBefore, err := common.Marshal(f)
	require.NoError(t, err)
	r := calculate(t, s, f)
	require.Len(t, r.CustomerServiceCosts, 1)
	row := r.CustomerServiceCosts[0]
	require.Zero(t, row.FactID)
	require.Equal(t, "support-call-1", row.RequestID)
	require.Equal(t, "estimate", row.Status)
	require.Equal(t, "0.030000", row.NetUSDT, "frozen cash quotation must not be discounted a second time")
	require.Equal(t, "0.06000000", row.NetCredit)
	require.Equal(t, "0.030000", r.CustomerServiceEstimateTotal)
	require.Equal(t, "0.000000", r.CustomerServiceConfirmedTotal)
	require.Equal(t, "20.000000", *r.Revenue)
	require.Equal(t, int64(10500000), r.ChargedQuota)
	require.Equal(t, "15.000000", *r.ActualCost, "complete channel invoice already includes platform credentials")
	require.Equal(t, "5.000000", *r.GrossProfit)
	require.Empty(t, r.TestCosts)
	after, err := common.Marshal(s)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	factsAfter, err := common.Marshal(f)
	require.NoError(t, err)
	require.Equal(t, string(factsBefore), string(factsAfter))
}

func TestSupportUsageProvenNonDispatchIsKnownZeroNotUnknownSupplierCost(t *testing.T) {
	u := supportUsageFixture()
	zero := "0"
	u.Dispatched, u.Outcome = false, "not_dispatched"
	u.InputTokens, u.OutputTokens, u.CacheReadTokens = nil, nil, nil
	u.QuoteCostU, u.QuoteEvidence = &zero, "local_dispatch_log:not_dispatched"
	r := calculate(t, snapshotWithSupport(t, fixture(), "available", u), facts())
	require.Len(t, r.CustomerServiceCosts, 1)
	require.Equal(t, "confirmed", r.CustomerServiceCosts[0].Status)
	require.Equal(t, "local_execution", r.CustomerServiceCosts[0].EvidenceType)
	require.Equal(t, "0.000000", r.CustomerServiceCosts[0].NetUSDT)
	require.Equal(t, "15.000000", *r.ActualCost)
	require.Equal(t, "5.000000", *r.GrossProfit)
	u.Dispatched = true
	r = calculate(t, snapshotWithSupport(t, fixture(), "available", u), facts())
	require.Equal(t, "unconfirmed", r.CustomerServiceCosts[0].Status, "a dispatch flag can never be overridden by local zero text")
}

func TestSupportUsageQuoteIsAddedToEstimatedProfitOnce(t *testing.T) {
	s := Snapshot{QuotaPerUnit: 500000}
	u := supportUsageFixture()
	s = snapshotWithSupport(t, s, "available", u, u)
	r := calculate(t, s, nil)
	require.Len(t, r.CustomerServiceCosts, 1)
	require.Equal(t, "0.030000", r.CustomerServiceEstimateTotal)
	require.NotNil(t, r.EstimatedCost)
	require.Equal(t, "0.030000", *r.EstimatedCost)
	require.Equal(t, "-0.030000", *r.EstimatedGrossProfit)
	require.True(t, r.EstimateComplete)
	require.Nil(t, r.ActualCost)
	require.Nil(t, r.GrossProfit)
}

func TestSupportUnknownCacheUpperBoundIsEstimateOnly(t *testing.T) {
	u := supportUsageFixture()
	u.CacheReadTokens, u.Outcome = nil, "completed"
	u.QuoteEvidence += ";cache_scope=unknown;quote_basis=all_input_at_max_rate_upper_bound"
	r := calculate(t, snapshotWithSupport(t, Snapshot{QuotaPerUnit: 500000}, "available", u), nil)
	require.Len(t, r.CustomerServiceCosts, 1)
	require.Equal(t, "estimate", r.CustomerServiceCosts[0].Status)
	require.Equal(t, "quote_upper_bound", r.CustomerServiceCosts[0].EvidenceType)
	require.Nil(t, r.CustomerServiceCosts[0].CacheReadTokens)
	require.Equal(t, "0.030000", *r.EstimatedCost)
	require.Nil(t, r.ActualCost)
	actual := "0.01"
	u.ActualCostU, u.ActualEvidence = &actual, "not enough to confirm cache scope"
	r = calculate(t, snapshotWithSupport(t, Snapshot{QuotaPerUnit: 500000}, "available", u), nil)
	require.Equal(t, "unconfirmed", r.CustomerServiceCosts[0].Status)
	require.Nil(t, r.ActualCost)
}

func TestSupportUsageActualRequiresEvidenceAndMonthlyCoverageIsOnce(t *testing.T) {
	for _, evidence := range []string{"", "supplier receipt-123"} {
		t.Run(evidence, func(t *testing.T) {
			u := supportUsageFixture()
			actual := "2"
			u.ActualCostU, u.ActualEvidence = &actual, evidence
			r := calculate(t, snapshotWithSupport(t, fixture(), "available", u), facts())
			require.Len(t, r.CustomerServiceCosts, 1)
			require.Equal(t, "15.000000", *r.ActualCost)
			require.Equal(t, "5.000000", *r.GrossProfit)
			if evidence == "" {
				require.Equal(t, "estimate", r.CustomerServiceCosts[0].Status)
				require.Equal(t, "0.000000", r.CustomerServiceConfirmedTotal)
			} else {
				require.Equal(t, "confirmed", r.CustomerServiceCosts[0].Status)
				require.Equal(t, "2.000000", r.CustomerServiceConfirmedTotal)
			}
		})
	}
}

func TestSupportUsageMissingOrConflictingEvidenceNeverConfirms(t *testing.T) {
	for _, name := range []string{"pending", "nil usage", "nil cache", "nil cost", "unknown channel", "bad scope", "cache overlap", "duplicate conflict", "missing id"} {
		t.Run(name, func(t *testing.T) {
			u := supportUsageFixture()
			switch name {
			case "pending":
				u.CompletedAt = nil
			case "nil usage":
				u.InputTokens = nil
			case "nil cache":
				u.CacheReadTokens = nil
			case "nil cost":
				u.QuoteCostU = nil
			case "unknown channel":
				u.ChannelID = 0
			case "bad scope":
				u.InputTokenScope = ""
			case "cache overlap":
				n := int64(101)
				u.CacheReadTokens = &n
			case "missing id":
				u.CallID = ""
			}
			usages := []support.FinancialUsage{u}
			if name == "duplicate conflict" {
				other := u
				quote := "8"
				other.QuoteCostU = &quote
				usages = append(usages, other)
			}
			r := calculate(t, snapshotWithSupport(t, fixture(), "available", usages...), facts())
			require.False(t, r.Confirmed)
			require.Nil(t, r.ActualCost)
			require.Nil(t, r.GrossProfit)
			require.Nil(t, r.EstimatedCost)
			require.False(t, r.EstimateComplete)
			require.NotZero(t, r.IssueCount)
			if len(r.CustomerServiceCosts) > 0 {
				require.Equal(t, "unconfirmed", r.CustomerServiceCosts[0].Status)
				require.Empty(t, r.CustomerServiceCosts[0].NetUSDT)
			}
		})
	}
}

func TestSupportUsageUnavailableRevokesFinancialConfirmation(t *testing.T) {
	r := calculate(t, snapshotWithSupport(t, fixture(), "unavailable"), facts())
	require.False(t, r.Confirmed)
	require.Nil(t, r.ActualCost)
	require.Nil(t, r.GrossProfit)
	require.Nil(t, r.EstimatedCost)
	require.Equal(t, "unavailable", reportMap(t, r)["support_source_status"])
	require.Contains(t, r.Issues, Issue{Code: "support_source_unavailable", Ref: "support"})
}

func TestSupportUsageMonthAndAsOfBoundaries(t *testing.T) {
	u := supportUsageFixture()
	for _, start := range []string{"2026-08-31T15:59:59Z", "2026-09-30T16:00:00Z", "2026-10-01T00:00:00Z"} {
		u.StartedAt = moment(start)
		u.CompletedAt = nil
		r := calculate(t, snapshotWithSupport(t, fixture(), "available", u), facts())
		require.Empty(t, r.CustomerServiceCosts)
	}
}

func TestSupportUsageReceiptTimeDeterminesMonthNotCallStart(t *testing.T) {
	u := supportUsageFixture()
	u.StartedAt = moment("2026-08-31T15:59:59Z")
	completed := moment("2026-08-31T16:00:00Z")
	u.CompletedAt = &completed
	r := calculate(t, snapshotWithSupport(t, fixture(), "available", u), facts())
	require.Len(t, r.CustomerServiceCosts, 1, "August call with September receipt belongs in September")
	require.Equal(t, "0.030000", r.CustomerServiceEstimateTotal)
	u.StartedAt = moment("2026-09-30T15:59:59Z")
	completed = moment("2026-09-30T16:00:00Z")
	r = calculate(t, snapshotWithSupport(t, fixture(), "available", u), facts())
	require.Empty(t, r.CustomerServiceCosts, "September call with October receipt belongs only in October")
}

func TestSupportUsageAccumulatesTwelveDecimalQuotesBeforeRounding(t *testing.T) {
	usages := make([]support.FinancialUsage, 0, 3)
	for _, id := range []string{"precision-1", "precision-2", "precision-3"} {
		u := supportUsageFixture()
		quote := "0.000000200001"
		u.CallID, u.QuoteCostU = id, &quote
		usages = append(usages, u)
	}
	r := calculate(t, snapshotWithSupport(t, Snapshot{QuotaPerUnit: 500000}, "available", usages...), nil)
	require.Equal(t, "0.000001", r.CustomerServiceEstimateTotal)
	require.Equal(t, "0.000001", *r.EstimatedCost)
	require.Equal(t, "-0.000001", *r.EstimatedGrossProfit)
}

func TestSupportUsageAndLegacyIncludedActualCannotExceedSameInvoice(t *testing.T) {
	u := supportUsageFixture()
	actual := "2"
	u.ActualCostU, u.ActualEvidence = &actual, "supplier auto receipt"
	s := fixture()
	s.Users[0].Role = common.RoleAdminUser
	c := customerServiceCost(t, "")
	c.Amount = "14"
	r := calculate(t, snapshotWithSupport(t, s, "available", u), append(facts(), revenueTestFact(c)))
	require.Len(t, r.CustomerServiceCosts, 2, "legacy facts must remain visible")
	require.Equal(t, "16.000000", r.CustomerServiceConfirmedTotal)
	require.Nil(t, r.ActualCost, "14 legacy + 2 automatic cannot both fit a 15 invoice")
	require.Nil(t, r.GrossProfit)
}

func TestSupportUsageAddsToExistingEstimateWithoutChangingItsRevenueScope(t *testing.T) {
	report := Report{EstimatedCost: stringPtr("10.000000"), EstimatedGrossProfit: stringPtr("7.000000"), EstimateComplete: true}
	r := replay{report: &report, start: moment("2026-08-31T16:00:00Z"), end: moment("2026-09-30T16:00:00Z"), known: decimal.NewFromInt(20)}
	s := snapshotWithSupport(t, Snapshot{}, "available", supportUsageFixture())
	cost, complete := zero, true
	r.buildSupportUsage(s, &cost, &complete, map[int]decimal.Decimal{})
	require.Equal(t, "10.030000", *report.EstimatedCost)
	require.Equal(t, "6.970000", *report.EstimatedGrossProfit, "append support costs without expanding the existing estimate's revenue scope")
}

func TestSupportUsageCoverageComparesUnroundedInvoiceAmounts(t *testing.T) {
	u := supportUsageFixture()
	actual := "0.000000300001"
	u.ActualCostU, u.ActualEvidence = &actual, "micro debit receipt"
	f := facts()
	cost := f[1].Payload.(Cost)
	cost.Gross = "0.0000004"
	f[1].Payload = cost
	r := calculate(t, snapshotWithSupport(t, fixture(), "available", u), f)
	require.NotNil(t, r.ActualCost, "round only for display, not when comparing covered receipts")
	require.True(t, r.Confirmed)
}
