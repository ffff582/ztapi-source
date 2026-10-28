package finance

import (
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func moment(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRepeatedAllocationsKeepBoundedFinancialPrecision(t *testing.T) {
	p := basis{quota: decimal.NewFromInt(1000000000), cash: decimal.NewFromInt(1000)}
	for i := 0; i < 100; i++ {
		p.take(decimal.NewFromInt(1))
	}
	require.GreaterOrEqual(t, p.cash.Exponent(), int32(-24), "allocation must not grow arbitrary-precision integers per request")
}

func TestMissingEvidenceCannotDisappearThroughPrecisionRounding(t *testing.T) {
	p := basis{quota: decimal.NewFromInt(1000000000), cash: decimal.NewFromInt(1000), unverified: true}
	for i := 0; i < 3000; i++ {
		b := p.take(decimal.NewFromInt(1))
		require.True(t, b.unverified)
		require.GreaterOrEqual(t, p.cash.Exponent(), int32(-24))
	}
}
func ledger(id int, delta, before int64, source, req, key string) Ledger {
	return Ledger{ID: id, UserID: 1, Delta: delta, BalanceBefore: before, BalanceAfter: before + delta, SourceType: source, RequestID: req, IdempotencyKey: key, CreatedAt: moment("2026-09-16T01:00:00Z").Add(time.Duration(id) * time.Second)}
}
func fixture() Snapshot {
	return Snapshot{QuotaPerUnit: 500000, Users: []User{{ID: 1, Username: "customer"}}, Ledgers: []Ledger{
		ledger(1, 52500000, 0, "admin_adjustment", "", "fund"),
		ledger(2, -10500000, 52500000, "usage_reservation", "r1", "ztapi:op1:begin"),
	}, Settlements: []Settlement{{ID: 1, UserID: 1, RequestID: "r1", OperationID: "op1", Status: "settled", ReservedQuota: 10500000, ChargedQuota: 10500000, CompletedAt: moment("2026-09-16T01:01:00Z").Unix(), FinalAttempt: 1}}, Attempts: []Attempt{{SettlementID: 1, Attempt: 1, ChannelID: 2, CreatedAt: moment("2026-09-16T01:00:02Z")}}}
}
func facts() []Fact {
	return []Fact{{ID: 1, Kind: "funding", Ref: "ledger:1", Payload: Funding{Kind: "cash", Amount: "100", Currency: "USDT", Rate: "1", ReceivedAt: moment("2026-09-16T00:00:00Z").Unix(), Evidence: "receipt-1"}},
		{ID: 2, Kind: "cost", Ref: "2026-09:2", Payload: Cost{Month: "2026-09", ChannelID: 2, Supplier: "supplier pool", Gross: "15", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "invoice-1"}}}
}
func calculate(t *testing.T, s Snapshot, f []Fact) Report {
	t.Helper()
	r, e := Calculate(s, f, "2026-09", moment("2026-10-26T00:00:00Z"))
	require.NoError(t, e)
	return r
}
func TestRepayingUntracedOpeningDebtCannotConfirmProfit(t *testing.T) {
	s := fixture()
	s.Ledgers = s.Ledgers[:1]
	s.Ledgers[0].BalanceBefore = -52500000
	s.Ledgers[0].BalanceAfter = 0
	s.Settlements = nil
	s.Attempts = nil
	f := append(facts(), Fact{ID: 3, Kind: "funding", Ref: "opening:1", Payload: Funding{Kind: "correction", Amount: "0", Evidence: "legacy negative opening balance"}})
	r := calculate(t, s, f)
	require.False(t, r.Confirmed)
	require.Nil(t, r.Revenue)
}

func TestGiftedFundingRecognizesCashNotQuota(t *testing.T) {
	r := calculate(t, fixture(), facts())
	require.True(t, r.Confirmed, "%+v", r.Issues)
	require.Equal(t, "20.000000", *r.Revenue)
	require.Equal(t, "15.000000", *r.ActualCost)
	require.Equal(t, "5.000000", *r.GrossProfit)
	require.Equal(t, "100.000000", r.Receipts[0].AmountUSDT)
	require.Equal(t, int64(10500000), r.ChargedQuota)
}

func TestBasisConservesCashAndClearsMissingEvidenceOnlyOnDepletion(t *testing.T) {
	p := basis{quota: decimal.NewFromInt(1000000), cash: decimal.NewFromInt(100), unverified: true}
	cash := decimal.Zero
	for i := 0; i < 1000; i++ {
		b := p.take(decimal.NewFromInt(1))
		cash = cash.Add(b.cash)
		require.True(t, b.unverified)
	}
	last := p.take(p.quota)
	cash = cash.Add(last.cash)
	require.True(t, last.unverified)
	require.True(t, cash.Equal(decimal.NewFromInt(100)))
	require.True(t, p.cash.IsZero())
	require.False(t, p.unverified)
	p.add(basis{quota: decimal.NewFromInt(50), cash: decimal.NewFromInt(25)})
	fresh := p.take(decimal.NewFromInt(50))
	require.False(t, fresh.unverified)
	require.Equal(t, "25.000000", fixed(fresh.cash))
}

func TestPartialAndExcessDebtRepaymentCannotFundTheSameCashTwice(t *testing.T) {
	p := basis{quota: decimal.NewFromInt(-100), unverified: true}
	p.add(basis{quota: decimal.NewFromInt(50), cash: decimal.NewFromInt(50)})
	require.True(t, p.cash.IsZero())
	require.Equal(t, int64(-50), p.quota.IntPart())
	p.add(basis{quota: decimal.NewFromInt(75), cash: decimal.NewFromInt(75)})
	require.Equal(t, int64(25), p.quota.IntPart())
	b := p.take(decimal.NewFromInt(25))
	require.Equal(t, "25.000000", fixed(b.cash))
	require.True(t, b.unverified)
}
func TestPartialSettlementReturnsOriginalBasis(t *testing.T) {
	s := fixture()
	s.Ledgers = append(s.Ledgers, ledger(3, 5250000, 42000000, "usage_settlement", "r1", "ztapi:op1:settle"))
	s.Settlements[0].ChargedQuota = 5250000
	r := calculate(t, s, facts())
	require.Equal(t, "10.000000", *r.Revenue)
}
func TestReleaseAndPendingAreNotRevenue(t *testing.T) {
	for _, status := range []string{"pending", "released"} {
		t.Run(status, func(t *testing.T) {
			s := fixture()
			s.Settlements[0].Status = status
			s.Settlements[0].CompletedAt = 0
			s.Settlements[0].ChargedQuota = 0
			if status == "released" {
				s.Ledgers = append(s.Ledgers, ledger(3, 10500000, 42000000, "billing_refund", "r1", "ztapi:op1:release"))
			}
			r := calculate(t, s, facts())
			require.Equal(t, "0.000000", *r.Revenue)
			require.Equal(t, int64(0), r.ChargedQuota)
		})
	}
}
func TestUnknownFundingNeverBecomesZeroCostRevenue(t *testing.T) {
	r := calculate(t, fixture(), facts()[1:])
	require.Nil(t, r.Revenue)
	require.Nil(t, r.GrossProfit)
	require.False(t, r.Confirmed)
	require.NotEmpty(t, r.ManualFunding)
}
func TestFutureReceiptDoesNotRepricePriorConsumption(t *testing.T) {
	s := fixture()
	next := ledger(3, 50000000, 42000000, "admin_adjustment", "", "second")
	next.CreatedAt = moment("2026-09-17T00:00:00Z")
	s.Ledgers = append(s.Ledgers, next)
	f := append(facts(), Fact{ID: 3, Kind: "funding", Ref: "ledger:3", Payload: Funding{Kind: "cash", Amount: "10", Currency: "USDT", Rate: "1", ReceivedAt: next.CreatedAt.Unix(), Evidence: "receipt-2"}})
	r := calculate(t, s, f)
	require.Equal(t, "20.000000", *r.Revenue)
}
func TestCustomerRefundReversesOriginalRevenueOnce(t *testing.T) {
	s := fixture()
	refund := ledger(3, 5250000, 42000000, "supplier_refund", "r1", "supplier-refund:p1")
	refund.CreatedAt = moment("2026-09-17T01:00:00Z")
	s.Ledgers = append(s.Ledgers, refund)
	r := calculate(t, s, facts())
	require.Equal(t, "10.000000", *r.Revenue)
	require.Equal(t, int64(5250000), r.ChargedQuota)
}
func TestOpeningBalanceAndInconsistentLedgerBlockProfit(t *testing.T) {
	s := fixture()
	s.Ledgers[0].BalanceBefore = 500000
	s.Ledgers[0].BalanceAfter += 500000
	s.Ledgers[1].BalanceBefore += 500000
	s.Ledgers[1].BalanceAfter += 500000
	r := calculate(t, s, facts())
	require.Nil(t, r.Revenue)
	require.Nil(t, r.GrossProfit)
}
func TestMissingCostDoesNotMeanFreeSupplier(t *testing.T) {
	r := calculate(t, fixture(), facts()[:1])
	require.Equal(t, "20.000000", *r.Revenue)
	require.Nil(t, r.ActualCost)
	require.Nil(t, r.GrossProfit)
}
func TestMonthUsesShanghaiHalfOpenBoundary(t *testing.T) {
	s := fixture()
	s.Settlements[0].CompletedAt = moment("2026-09-30T16:00:00Z").Unix()
	r := calculate(t, s, facts())
	require.Equal(t, "0.000000", *r.Revenue)
	require.Equal(t, int64(0), r.ChargedQuota)
}
func TestCurrentMonthIsNotClosedProfit(t *testing.T) {
	r, e := Calculate(fixture(), facts(), "2026-09", moment("2026-09-20T00:00:00Z"))
	require.NoError(t, e)
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
}

func TestAdditionalAttemptAndItsRefundHaveIndependentBasis(t *testing.T) {
	s := fixture()
	s.Ledgers = append(s.Ledgers, ledger(3, -5250000, 42000000, "usage_settlement", "r1", "attempt-billing:p1"))
	s.Charges = []Charge{{ID: 1, RequestID: "r1", UserID: 1, ChargedQuota: 10500000, OriginalLedgerID: 2}, {ID: 2, RequestID: "r1", UserID: 1, ChargedQuota: 5250000, OriginalLedgerID: 3, BillingProofID: 1, PostedAt: moment("2026-09-16T01:00:03Z").Unix(), ChannelID: 2}}
	refund := ledger(4, 5250000, 36750000, "supplier_refund", "r1", "supplier-refund:p2")
	s.Ledgers = append(s.Ledgers, refund)
	s.Refunds = []Refund{{LedgerID: 4, ChargeID: 2, RefundedQuota: 5250000}}
	r := calculate(t, s, facts())
	require.Equal(t, "20.000000", *r.Revenue)
	require.Equal(t, int64(10500000), r.ChargedQuota)
}

func TestOverdraftNeverInventsCashRevenue(t *testing.T) {
	s := fixture()
	s.Ledgers = append(s.Ledgers, ledger(3, -50000000, 42000000, "usage_settlement", "r1", "ztapi:op1:settle"))
	s.Settlements[0].ChargedQuota = 60500000
	r := calculate(t, s, facts())
	require.Nil(t, r.Revenue)
	require.Nil(t, r.GrossProfit)
}

func TestReceiptPaidBeforeCreditPostingStillBelongsToReceiptMonth(t *testing.T) {
	s := fixture()
	later := ledger(3, 5000000, 42000000, "admin_adjustment", "", "late-credit")
	later.CreatedAt = moment("2026-10-01T01:00:00Z")
	s.Ledgers = append(s.Ledgers, later)
	f := append(facts(), Fact{ID: 3, Kind: "funding", Ref: "ledger:3", Payload: Funding{Kind: "cash", Amount: "10", Currency: "USDT", Rate: "1", ReceivedAt: moment("2026-09-30T12:00:00Z").Unix(), Evidence: "late-credit-receipt"}})
	r := calculate(t, s, f)
	require.Equal(t, "110.000000", r.ReceiptsTotal)
	require.Equal(t, "20.000000", *r.Revenue)
}

func TestUnboundedDecimalExponentIsRejected(t *testing.T) {
	_, e := money("1e999999999", "USDT", "1")
	require.Error(t, e)
	_, e = money("1", "CNY", "1e999999999")
	require.Error(t, e)
}

func TestCrossMonthSettlementStillRequiresChannelCostEvidence(t *testing.T) {
	s := fixture()
	s.Settlements[0].CompletedAt = moment("2026-10-01T01:00:00Z").Unix()
	r, e := Calculate(s, facts()[:1], "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, e)
	require.Nil(t, r.ActualCost)
	require.False(t, r.Confirmed)
}

func TestXcloudEstimateUsesSettledDimensionsAndSeparatesActualCost(t *testing.T) {
	s := fixture()
	s.Channels = []Channel{{ID: 4, Name: "Xcloud", BaseURL: "https://www.xcloudtoken.com"}}
	s.Attempts[0].ChannelID = 4
	s.Settlements[0].PriceSnapshotJSON = `{"source_model":"claude-opus-4-8"}`
	s.Settlements[0].ChargeDimensionsJSON = `[{"dimension":"input_tokens","quantity":1000000},{"dimension":"output_tokens","quantity":1000000}]`
	s.Settlements[0].MissingDimensionsJSON = `[]`
	r := calculate(t, s, facts()[:1])
	require.True(t, r.EstimateComplete, "%+v", r.EstimateIssues)
	require.Equal(t, "12.000000", *r.EstimatedCost)
	require.Equal(t, "8.000000", *r.EstimatedGrossProfit)
	require.Nil(t, r.ActualCost, "public quote must not become confirmed supplier cost")
	require.Len(t, r.Estimates, 1)
}

func TestOrphanReservationIsVisibleAndBlocksProfit(t *testing.T) {
	s := fixture()
	s.Settlements = nil
	r := calculate(t, s, facts())
	require.Equal(t, 1, r.PendingRequests)
	require.Equal(t, int64(10500000), r.PendingQuota)
	require.False(t, r.Confirmed)
	require.Contains(t, r.Issues, Issue{Code: "orphan_reservation", Ref: "r1", UserID: 1})
}
func TestUnusedUnclassifiedCreditsStillBlockClosedMonth(t *testing.T) {
	s := fixture()
	s.Ledgers = append(s.Ledgers, ledger(3, 5000000, 42000000, "admin_adjustment", "", "unknown"))
	r := calculate(t, s, facts())
	require.Equal(t, "20.000000", *r.Revenue)
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
}
func TestRetryRevenueUsesItsImmutablePostingTime(t *testing.T) {
	s := fixture()
	l := ledger(3, -5250000, 42000000, "usage_settlement", "r1", "attempt-billing:p1")
	l.CreatedAt = moment("2026-09-30T15:59:59Z")
	s.Ledgers = append(s.Ledgers, l)
	s.Charges = []Charge{{ID: 2, RequestID: "r1", UserID: 1, OriginalLedgerID: 3, ChargedQuota: 5250000, BillingProofID: 1, PostedAt: moment("2026-09-30T16:00:00Z").Unix(), ChannelID: 2}}
	r := calculate(t, s, facts())
	require.Equal(t, "20.000000", *r.Revenue)
	r, e := Calculate(s, facts(), "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, e)
	require.Equal(t, "10.000000", *r.Revenue)
	require.Nil(t, r.ActualCost)
	s.Charges[0].PostedAt = 0
	r = calculate(t, s, facts())
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
}
