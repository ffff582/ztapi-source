package finance

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

type hostedToolReportView struct {
	Tool                     string  `json:"tool"`
	PublicModel              string  `json:"public_model"`
	ChannelID                int     `json:"channel_id"`
	Unit                     string  `json:"unit"`
	SuccessfulCalls          int     `json:"successful_calls"`
	ZeroChargeCalls          int     `json:"zero_charge_calls"`
	UnknownCalls             int     `json:"unknown_calls"`
	TestSuccessfulCalls      int     `json:"test_successful_calls"`
	TestUnknownCalls         int     `json:"test_unknown_calls"`
	ChargedQuota             int64   `json:"charged_quota"`
	CustomerChargedQuota     int64   `json:"customer_charged_quota"`
	TestChargedQuota         int64   `json:"test_charged_quota"`
	CustomerKnownRevenueUSDT string  `json:"customer_known_revenue_usdt"`
	CustomerRevenueUSDT      *string `json:"customer_revenue_usdt"`
	CustomerQuotedSaleCNY    string  `json:"customer_quoted_sale_cny"`
	QuotedCostCNY            string  `json:"quoted_cost_cny"`
	QuotedCostUSDT           string  `json:"quoted_cost_usdt"`
	TestQuotedCostCNY        string  `json:"test_quoted_cost_cny"`
	TestQuotedCostUSDT       string  `json:"test_quoted_cost_usdt"`
	TestConfirmedCostUSDT    string  `json:"test_confirmed_cost_usdt"`
	TestEstimateCostUSDT     string  `json:"test_estimate_cost_usdt"`
	QuotedCostIsEstimate     bool    `json:"quoted_cost_is_estimate"`
	QuoteEstimateComplete    bool    `json:"quote_estimate_complete"`
}

func hostedToolSnapshot(tool string) string {
	return fmt.Sprintf(`{"schema_version":1,"billing_kind":"hosted_tool","provider":"deepseek_official","tool":%q,"unit":"call","currency":"CNY","sale_per_call_cny":"0.01","quoted_cost_per_call_cny":"0.005","cny_per_u":"5","quota_per_unit":500000,"fx_policy_id":4,"channel_id":2,"credential_version":"official-v1","parent_request_id":"model-parent"}`, tool)
}

func hostedToolFixture(tool string) (Snapshot, []Fact) {
	s := Snapshot{Referral: referralFixture(), QuotaPerUnit: 500000, Users: []User{{ID: 1, Username: "customer"}},
		Channels: []Channel{{ID: 2, Name: "official tools"}},
		Ledgers:  []Ledger{ledger(1, 10000, 0, "admin_adjustment", "", "fund"), ledger(2, -1000, 10000, "usage_reservation", "tool-r1", "ztapi:tool-op1:begin")},
		Settlements: []Settlement{{ID: 1, UserID: 1, RequestID: "tool-r1", OperationID: "tool-op1", PublicModel: "ztapi-tool-" + tool,
			PriceSnapshotJSON: hostedToolSnapshot(tool), Status: "settled", ReservedQuota: 1000, ChargedQuota: 1000,
			FinalAttempt: 1, CreatedAt: moment("2026-09-16T01:00:02Z"), CompletedAt: moment("2026-09-16T01:01:00Z").Unix()}},
		Attempts: []Attempt{{SettlementID: 1, Attempt: 1, ChannelID: 2, CreatedAt: moment("2026-09-16T01:00:02Z")}}}
	f := []Fact{{ID: 1, Kind: "funding", Ref: "ledger:1", Payload: Funding{Kind: "cash", Amount: "0.01", Currency: "USDT", Rate: "1", ReceivedAt: moment("2026-09-16T00:00:00Z").Unix(), Evidence: "cash receipt"}},
		{ID: 2, Kind: "cost", Ref: "2026-09:2", Payload: Cost{Month: "2026-09", ChannelID: 2, Supplier: "official tools", Gross: "0.02", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "supplier invoice"}}}
	return s, f
}

func hostedToolRows(t *testing.T, report Report) []hostedToolReportView {
	t.Helper()
	raw, err := common.Marshal(report)
	require.NoError(t, err)
	var view struct {
		Tools []hostedToolReportView `json:"hosted_tools"`
	}
	require.NoError(t, common.Unmarshal(raw, &view))
	require.NotEmpty(t, view.Tools, "report must separately identify hosted tool calls")
	return view.Tools
}

func hostedToolTestFact(status, coverage string) Fact {
	c := TestCost{RequestID: "tool-r1", Attempt: 1, ChannelID: 2, Status: status, EvidenceType: "quote", Coverage: coverage,
		Amount: "0.005", Refund: "0", Currency: "CNY", Rate: "0.2", Evidence: "official tool quote", CoverageEvidence: "supplier invoice"}
	if status == "confirmed" {
		c.EvidenceType, c.Evidence = "supplier_statement", "official supplier statement"
	}
	if coverage == "unknown" {
		c.CoverageEvidence = ""
	} else if coverage == "extra" {
		c.CoverageEvidence = "excluded from supplier invoice"
	}
	return Fact{ID: 3, Kind: "test_cost", Ref: "test:tool-r1:1:2", Payload: c}
}

func TestHostedToolReportSeparatesCashRevenueAndQuoteFromSupplierDebit(t *testing.T) {
	for _, tool := range []string{"search", "fetch"} {
		t.Run(tool, func(t *testing.T) {
			s, f := hostedToolFixture(tool)
			r := calculate(t, s, f)
			row := hostedToolRows(t, r)[0]
			require.Equal(t, tool, row.Tool)
			require.Equal(t, "ztapi-tool-"+tool, row.PublicModel)
			require.Equal(t, "call", row.Unit)
			require.Equal(t, 1, row.SuccessfulCalls)
			require.Zero(t, row.UnknownCalls)
			require.Equal(t, int64(1000), row.CustomerChargedQuota)
			require.Equal(t, "0.001000", row.CustomerKnownRevenueUSDT, "cash basis is discounted; nominal sale is not cash revenue")
			require.NotNil(t, row.CustomerRevenueUSDT)
			require.Equal(t, "0.010000", row.CustomerQuotedSaleCNY)
			require.Equal(t, "0.005000", row.QuotedCostCNY)
			require.Equal(t, "0.001000", row.QuotedCostUSDT)
			require.True(t, row.QuotedCostIsEstimate)
			require.True(t, row.QuoteEstimateComplete)
			require.Equal(t, "0.020000", r.RecordedCost, "quote is not another supplier debit")
			require.Equal(t, "-0.019000", *r.GrossProfit)
			require.Nil(t, r.EstimatedCost, "tool quote cannot replace the model estimate")
		})
	}
}

func TestHostedToolReportUnknownAndConfirmedZeroAreNotSuccess(t *testing.T) {
	for _, status := range []string{"pending", "reserved", "settled", "released"} {
		t.Run(status, func(t *testing.T) {
			s, f := hostedToolFixture("search")
			s.Settlements[0].Status, s.Settlements[0].ChargedQuota = status, 0
			unknown := status == "pending" || status == "reserved"
			if unknown {
				s.Settlements[0].CompletedAt, s.Settlements[0].FinalAttempt = 0, 0
			} else {
				s.Ledgers = append(s.Ledgers, ledger(3, 1000, 9000, "billing_refund", "tool-r1", "release"))
			}
			r := calculate(t, s, f)
			row := hostedToolRows(t, r)[0]
			require.Zero(t, row.SuccessfulCalls)
			require.Equal(t, "0.000000", row.QuotedCostCNY)
			if unknown {
				require.Equal(t, 1, row.UnknownCalls)
				require.False(t, row.QuoteEstimateComplete)
				require.Equal(t, int64(1000), r.PendingQuota)
			} else {
				require.Equal(t, 1, row.ZeroChargeCalls)
				require.Zero(t, row.UnknownCalls)
			}
		})
	}
}

func TestHostedToolTestCostsReuseFactsAndDoNotDoubleMonthlyCost(t *testing.T) {
	for _, status := range []string{"estimate", "confirmed"} {
		for _, coverage := range []string{"included", "extra", "unknown"} {
			t.Run(status+"/"+coverage, func(t *testing.T) {
				s, f := hostedToolFixture("fetch")
				fact := hostedToolTestFact(status, coverage)
				r := calculate(t, s, append(f, fact, fact))
				row := hostedToolRows(t, r)[0]
				require.Equal(t, 1, row.TestSuccessfulCalls)
				require.Zero(t, row.CustomerChargedQuota)
				require.Equal(t, int64(1000), row.TestChargedQuota)
				require.Equal(t, "0.000000", row.CustomerKnownRevenueUSDT)
				require.Equal(t, "0.005000", row.TestQuotedCostCNY)
				require.Equal(t, "0.000000", *r.Revenue)
				require.Len(t, r.TestCosts, 1)
				if status == "confirmed" {
					require.Equal(t, "0.001000", row.TestConfirmedCostUSDT)
				} else {
					require.Equal(t, "0.001000", row.TestEstimateCostUSDT)
				}
				want := "0.020000"
				if status == "confirmed" && coverage == "extra" {
					want = "0.021000"
				}
				require.Equal(t, want, r.RecordedCost)
			})
		}
	}
}

func TestHostedToolQuoteUsesFrozenFXAndNeverTokenEstimator(t *testing.T) {
	s, f := hostedToolFixture("search")
	s.Channels[0].Name, s.Channels[0].BaseURL = "xcloud tool label", "https://api.xcloud.example"
	s.Settlements[0].PriceSnapshotJSON = stringsReplace(hostedToolSnapshot("search"), `"cny_per_u":"5"`, `"cny_per_u":"10"`)
	s.Settlements[0].ChargedQuota, s.Settlements[0].ReservedQuota = 500, 500
	s.Ledgers[1] = ledger(2, -500, 10000, "usage_reservation", "tool-r1", "ztapi:tool-op1:begin")
	r := calculate(t, s, f)
	row := hostedToolRows(t, r)[0]
	require.Equal(t, "0.000500", row.QuotedCostUSDT)
	require.Empty(t, r.Estimates, "tools must not be priced as token models even if channel labels overlap")
}

func TestHostedToolInvalidSnapshotCannotManufactureCompleteEstimate(t *testing.T) {
	for _, raw := range []string{`{}`, hostedToolSnapshot("fetch"),
		stringsReplace(hostedToolSnapshot("search"), `"cny_per_u":"5"`, `"cny_per_u":"0"`),
		stringsReplace(hostedToolSnapshot("search"), `"currency":"CNY"`, `"currency":"USD"`),
		stringsReplace(hostedToolSnapshot("search"), `"currency":"CNY"`, `"currency":"CNY","currency":"CNY"`),
		stringsReplace(hostedToolSnapshot("search"), `"channel_id":2`, `"channel_id":3`)} {
		s, f := hostedToolFixture("search")
		s.Settlements[0].PriceSnapshotJSON = raw
		r := calculate(t, s, f)
		row := hostedToolRows(t, r)[0]
		require.False(t, row.QuoteEstimateComplete)
		require.Equal(t, "0.000000", row.QuotedCostCNY)
		require.Equal(t, "0.001000", row.CustomerKnownRevenueUSDT, "bad quote cannot erase actual wallet cash attribution")
		require.Contains(t, fmt.Sprint(r.EstimateIssues), "hosted_tool_snapshot_invalid")
	}
}

func TestHostedToolRefundAndCrossMonthDoNotRecountSuccessfulCall(t *testing.T) {
	s, f := hostedToolFixture("search")
	refund := ledger(3, 1000, 9000, "supplier_refund", "tool-r1", "supplier-refund:tool")
	refund.CreatedAt = moment("2026-10-02T00:00:00Z")
	s.Ledgers = append(s.Ledgers, refund)
	f = append(f, Fact{ID: 4, Kind: "cost", Ref: "2026-10:2", Payload: Cost{Month: "2026-10", ChannelID: 2, Supplier: "official tools", Gross: "0", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "October statement"}})
	r, err := Calculate(s, f, "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, err)
	row := hostedToolRows(t, r)[0]
	require.Zero(t, row.SuccessfulCalls)
	require.Equal(t, int64(-1000), row.CustomerChargedQuota)
	require.Equal(t, "-0.001000", row.CustomerKnownRevenueUSDT)
	require.Equal(t, "0.000000", row.QuotedCostCNY, "customer refund does not assert any supplier quote refund")
}

func TestHostedToolTestCostsRemainVisibleInAttemptMonth(t *testing.T) {
	s, f := hostedToolFixture("search")
	s.Settlements[0].CompletedAt = moment("2026-10-01T00:01:00Z").Unix()
	r := calculate(t, s, append(f, hostedToolTestFact("confirmed", "included")))
	row := hostedToolRows(t, r)[0]
	require.Equal(t, "0.001000", row.TestConfirmedCostUSDT)
	require.Equal(t, 1, row.TestUnknownCalls, "not completed at the month cutoff")
	require.Zero(t, row.TestSuccessfulCalls)
	r, err := Calculate(s, f, "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, err)
	row = hostedToolRows(t, r)[0]
	require.Equal(t, 1, row.SuccessfulCalls)
	require.Equal(t, "0.000000", row.TestConfirmedCostUSDT, "do not count September's supplier test fact again")

	s.Settlements[0].CreatedAt = moment("2026-08-31T15:59:59Z")
	s.Settlements[0].CompletedAt = moment("2026-08-31T15:59:59Z").Unix()
	r = calculate(t, s, append(f, hostedToolTestFact("confirmed", "included")))
	row = hostedToolRows(t, r)[0]
	require.Zero(t, row.SuccessfulCalls)
	require.Equal(t, "0.001000", row.TestConfirmedCostUSDT, "fact lineage follows the attempt month, not settlement creation")
}

func TestHostedToolAmbiguousLineageCannotConfirmQuote(t *testing.T) {
	for _, mutation := range []string{"no attempt", "duplicate attempt", "duplicate request", "duplicate settlement ID"} {
		t.Run(mutation, func(t *testing.T) {
			s, f := hostedToolFixture("search")
			switch mutation {
			case "no attempt":
				s.Attempts = nil
			case "duplicate attempt":
				s.Attempts = append(s.Attempts, s.Attempts[0])
			case "duplicate request":
				duplicate := s.Settlements[0]
				duplicate.ID = 2
				s.Settlements = append(s.Settlements, duplicate)
			case "duplicate settlement ID":
				duplicate := s.Settlements[0]
				duplicate.RequestID, duplicate.PublicModel = "other", "ordinary-model"
				s.Settlements = append(s.Settlements, duplicate)
			}
			r := calculate(t, s, f)
			row := hostedToolRows(t, r)[0]
			require.False(t, row.QuoteEstimateComplete)
			require.Equal(t, "0.000000", row.QuotedCostCNY)
			require.Contains(t, fmt.Sprint(r.EstimateIssues), "hosted_tool_lineage_missing")
		})
	}
}

func TestHostedToolRevenueGiftAndMissingBasisAreNotNominalSale(t *testing.T) {
	for _, kind := range []string{"gift", "missing"} {
		t.Run(kind, func(t *testing.T) {
			s, f := hostedToolFixture("search")
			if kind == "gift" {
				f[0].Payload = Funding{Kind: "gift", Amount: "0", Evidence: "gift classification"}
			} else {
				f = f[1:]
			}
			row := hostedToolRows(t, calculate(t, s, f))[0]
			require.Equal(t, "0.000000", row.CustomerKnownRevenueUSDT)
			if kind == "missing" {
				require.Nil(t, row.CustomerRevenueUSDT)
			} else {
				require.NotNil(t, row.CustomerRevenueUSDT)
			}
		})
	}
}

func TestHostedToolTestCostTotalsRoundOnlyAfterAggregation(t *testing.T) {
	s, f := hostedToolFixture("search")
	s.Ledgers = append(s.Ledgers, ledger(3, -1000, 9000, "usage_reservation", "tool-r2", "ztapi:tool-op2:begin"))
	second := s.Settlements[0]
	second.ID, second.RequestID, second.OperationID = 2, "tool-r2", "tool-op2"
	s.Settlements = append(s.Settlements, second)
	s.Attempts = append(s.Attempts, Attempt{SettlementID: 2, Attempt: 1, ChannelID: 2, CreatedAt: s.Attempts[0].CreatedAt})
	firstFact := hostedToolTestFact("confirmed", "included")
	cost := firstFact.Payload.(TestCost)
	cost.Rate = "0.142857142857"
	firstFact.Payload = cost
	secondFact := firstFact
	secondFact.ID, secondFact.Ref = 4, "test:tool-r2:1:2"
	cost.RequestID = "tool-r2"
	secondFact.Payload = cost
	r := calculate(t, s, append(f, firstFact, secondFact))
	row := hostedToolRows(t, r)[0]
	require.Equal(t, "0.001429", row.TestConfirmedCostUSDT)
	require.Equal(t, r.TestConfirmedTotal, row.TestConfirmedCostUSDT)
}

func TestHostedToolLegacyFactReaderAndReportCompatibility(t *testing.T) {
	store := newStore(t)
	fact := hostedToolTestFact("estimate", "unknown")
	cost := fact.Payload.(TestCost)
	in := Submission{Kind: "test_cost", Ref: fact.Ref, TestCost: &cost, IdempotencyKey: "hosted-tool-test-cost"}
	now := moment("2026-10-26T00:00:00Z")
	saved, err := store.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	again, err := store.Append(context.Background(), in, 1, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, saved.ID, again.ID)
	oldReader := &Store{db: store.db}
	loaded, err := oldReader.List(context.Background())
	require.NoError(t, err, "the pre-tool Store reader must recognize every written fact kind")
	require.Len(t, loaded, 1)
	require.Equal(t, "test_cost", loaded[0].Kind)
	require.Nil(t, loaded[0].Payload.(TestCost).InputTokens)
	require.Nil(t, loaded[0].Payload.(TestCost).OutputTokens)
	s, f := hostedToolFixture("search")
	before, err := common.Marshal([]any{s, f})
	require.NoError(t, err)
	r := calculate(t, s, f)
	var legacy struct {
		Month        string `json:"month"`
		KnownRevenue string `json:"known_revenue"`
	}
	raw, err := common.Marshal(r)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, &legacy))
	require.Equal(t, r.KnownRevenue, legacy.KnownRevenue)
	after, err := common.Marshal([]any{s, f})
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	_ = hostedToolRows(t, r)
}

func stringsReplace(input, old, replacement string) string {
	return strings.ReplaceAll(input, old, replacement)
}
