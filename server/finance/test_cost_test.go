package finance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

const testCostBody = `{"kind":"test_cost","ref":"test:r1:1:2","test_cost":{"request_id":"r1","attempt":1,"channel_id":2,"status":"confirmed","evidence_type":"supplier_statement","coverage":"included","amount":"2","refund":"0","currency":"USDT","rate":"1","evidence":"supplier receipt 1","coverage_evidence":"invoice-1"},"previous_id":0,"idempotency_key":"test-cost-key-1"}`

func TestConvertedXcloudTestCostPreservesCreditsAndCashWithoutDoubleDeduction(t *testing.T) {
	c := TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, Status: "confirmed", EvidenceType: "supplier_statement", Coverage: "included", Amount: "2", Refund: "0", Currency: "XCLOUD_CREDIT", Rate: "0.5", RateEvidence: "owner confirmed 100 paid for 200 credit", Evidence: "supplier bill", CoverageEvidence: "invoice-1"}
	in := Submission{Kind: "test_cost", Ref: "test:r1:1:2", TestCost: &c, IdempotencyKey: "converted-credit"}
	require.NoError(t, validateSubmission(in, moment("2026-10-26T00:00:00Z")))
	r := calculate(t, fixture(), append(facts(), Fact{ID: 3, Kind: "test_cost", Ref: in.Ref, Payload: c}))
	require.Equal(t, "2.00000000", r.TestXcloudCreditTotal)
	require.Equal(t, "1.000000", r.TestConfirmedTotal)
	require.Equal(t, "15.000000", r.RecordedCost)
	require.Equal(t, "2.00000000", r.TestCosts[0].NetCredit)
	require.True(t, r.Confirmed)
	c.RateEvidence = ""
	require.Error(t, validateSubmission(in, moment("2026-10-26T00:00:00Z")))
}

func TestTestCostHTTPAuthorizationIdentityAndIdempotency(t *testing.T) {
	h, token := api(t, common.RoleFinanceUser)
	path := "/api/admin/finance-reporting/facts"
	origin := "https://admin.example.test"
	require.Equal(t, http.StatusForbidden, request(h, token, "POST", path, testCostBody, "https://evil.example").Code)
	require.Equal(t, http.StatusCreated, request(h, token, "POST", path, testCostBody, origin).Code)
	require.Equal(t, http.StatusCreated, request(h, token, "POST", path, testCostBody, origin).Code)
	require.Equal(t, http.StatusConflict, request(h, token, "POST", path, strings.Replace(testCostBody, `"amount":"2"`, `"amount":"3"`, 1), origin).Code)
	require.Equal(t, http.StatusBadRequest, request(h, token, "POST", path, strings.ReplaceAll(testCostBody, "r1", "missing"), origin).Code)
	require.Equal(t, http.StatusBadRequest, request(h, token, "POST", path, strings.Replace(testCostBody, `"evidence":"supplier receipt 1"`, `"evidence":""`, 1), origin).Code)
	require.Equal(t, http.StatusRequestEntityTooLarge, request(h, token, "POST", path, testCostBody+strings.Repeat(" ", 8192), origin).Code)
	h, token = api(t, common.RoleSupportUser)
	require.Equal(t, http.StatusForbidden, request(h, token, "POST", path, testCostBody, origin).Code)
}

func TestTestCostCSVIncludesEvidenceAndEscapesFormula(t *testing.T) {
	w := httptest.NewRecorder()
	exportReport(w, Report{Month: "2026-09", TestCosts: []TestCostRow{{TestCost: TestCost{RequestID: "=danger", Attempt: 2, ChannelID: 3, Status: "estimate", Coverage: "unknown", Evidence: "quote"}, FactID: 9, AttemptAt: moment("2026-09-30T15:59:59Z"), NetUSDT: "1.000000"}}})
	require.Contains(t, w.Body.String(), "test_request_id,attempt,channel_id")
	require.Contains(t, w.Body.String(), "'=danger,2,3,,,,2026-09-30T23:59:59+08:00,estimate,unknown")
}

func TestTestCostQuoteCannotBeConfirmed(t *testing.T) {
	h, token := api(t, common.RoleFinanceUser)
	path := "/api/admin/finance-reporting/facts"
	origin := "https://admin.example.test"
	quote := strings.Replace(testCostBody, `"status":"confirmed","evidence_type":"supplier_statement"`, `"status":"estimate","evidence_type":"quote"`, 1)
	require.Equal(t, http.StatusCreated, request(h, token, "POST", path, quote, origin).Code)
	confirmed := strings.Replace(quote, `"status":"estimate"`, `"status":"confirmed"`, 1)
	require.Equal(t, http.StatusBadRequest, request(h, token, "POST", path, confirmed, origin).Code)
}

func TestTestCostRetriesMonthBoundaryAndMissingLineage(t *testing.T) {
	s := fixture()
	s.Settlements[0].Status = "released"
	s.Attempts = []Attempt{
		{SettlementID: 1, Attempt: 1, ChannelID: 2, CreatedAt: moment("2026-09-30T15:59:59Z")},
		{SettlementID: 1, Attempt: 2, ChannelID: 2, CreatedAt: moment("2026-09-30T16:00:00Z")},
	}
	c := TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, Status: "confirmed", EvidenceType: "supplier_statement", Coverage: "included", Amount: "2", Refund: "0", Currency: "USDT", Rate: "1", Evidence: "supplier receipt", CoverageEvidence: "invoice-1"}
	c2 := c
	c2.Attempt = 2
	f := append(facts(), Fact{ID: 3, Kind: "test_cost", Ref: "test:r1:1:2", Payload: c}, Fact{ID: 4, Kind: "test_cost", Ref: "test:r1:2:2", Payload: c2})
	r := calculate(t, s, f)
	require.Len(t, r.TestCosts, 1)
	require.Equal(t, 1, r.TestCosts[0].Attempt)
	r, err := Calculate(s, f, "2026-10", moment("2026-11-01T00:00:00Z"))
	require.NoError(t, err)
	require.Len(t, r.TestCosts, 1)
	require.Equal(t, 2, r.TestCosts[0].Attempt)
	s.Attempts = s.Attempts[:1]
	r = calculate(t, s, f)
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
}

func TestTestCostAmbiguousAttemptDoesNotConfirmProfit(t *testing.T) {
	s := fixture()
	s.Attempts = append(s.Attempts, s.Attempts[0])
	c := TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, Status: "confirmed", EvidenceType: "supplier_statement", Coverage: "included", Amount: "2", Refund: "0", Currency: "USDT", Rate: "1", Evidence: "supplier receipt", CoverageEvidence: "invoice-1"}
	r := calculate(t, s, append(facts(), Fact{ID: 3, Kind: "test_cost", Ref: "test:r1:1:2", Payload: c}))
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
}

func TestIncludedTestCostsCannotExceedSupplierBill(t *testing.T) {
	c := TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, Status: "confirmed", EvidenceType: "supplier_statement", Coverage: "included", Amount: "16", Refund: "0", Currency: "USDT", Rate: "1", Evidence: "supplier receipt", CoverageEvidence: "invoice-1"}
	r := calculate(t, fixture(), append(facts(), Fact{ID: 3, Kind: "test_cost", Ref: "test:r1:1:2", Payload: c}))
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
}

func TestUnconvertedXcloudCreditRetainsEvidenceWithoutClaimingCashProfit(t *testing.T) {
	input, output := int64(125), int64(24)
	c := TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, InputTokens: &input, OutputTokens: &output, Status: "confirmed", EvidenceType: "supplier_statement", Coverage: "unknown", Amount: "0.001726", Refund: "0", Currency: "XCLOUD_CREDIT", Rate: "", Evidence: "xcloud receipt 123"}
	in := Submission{Kind: "test_cost", Ref: "test:r1:1:2", TestCost: &c, IdempotencyKey: "xcloud-credit-test"}
	require.NoError(t, validateSubmission(in, moment("2026-10-26T00:00:00Z")))
	r := calculate(t, fixture(), append(facts(), Fact{ID: 3, Kind: "test_cost", Ref: in.Ref, Payload: c}))
	require.Equal(t, "0.00172600", r.TestXcloudCreditTotal)
	require.Len(t, r.TestCosts, 1)
	require.Equal(t, int64(125), *r.TestCosts[0].InputTokens)
	require.Equal(t, int64(24), *r.TestCosts[0].OutputTokens)
	require.Empty(t, r.TestCosts[0].NetUSDT)
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
	c.Amount = "0.00000001"
	r = calculate(t, fixture(), append(facts(), Fact{ID: 3, Kind: "test_cost", Ref: in.Ref, Payload: c}))
	require.Equal(t, "0.00000001", r.TestXcloudCreditTotal)
	c.Coverage = "extra"
	require.Error(t, validateSubmission(in, moment("2026-10-26T00:00:00Z")), "unconverted credit cannot be added to cash profit")
	c.Coverage = "unknown"
	input = -1
	require.Error(t, validateSubmission(in, moment("2026-10-26T00:00:00Z")), "negative usage must be rejected")
}

func TestTestCostRevisionAndRefundEvidenceCannotBeReused(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-26T00:00:00Z")
	c := &TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, Status: "confirmed", EvidenceType: "supplier_statement", Coverage: "extra", Amount: "2", Refund: "1", Currency: "USDT", Rate: "1", Evidence: "receipt-1", CoverageEvidence: "outside bill", RefundEvidence: "credit-1"}
	in := Submission{Kind: "test_cost", Ref: "test:r1:1:2", TestCost: c, IdempotencyKey: "test-cost-01"}
	a, err := s.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	in.IdempotencyKey = "test-cost-02"
	in.PreviousID = a.ID
	in.TestCost.Amount = "3"
	b, err := s.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	require.NotEqual(t, a.ID, b.ID)
	in.PreviousID = 0
	in.Ref = "test:r1:2:2"
	in.TestCost.Attempt = 2
	in.IdempotencyKey = "test-cost-03"
	_, err = s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, err, ErrConflict)
}

func TestTestCostAccountingCoverageAndRefund(t *testing.T) {
	base := TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, Status: "confirmed", EvidenceType: "supplier_statement", Coverage: "included", Amount: "2", Refund: "0", Currency: "USDT", Rate: "1", Evidence: "supplier receipt 1", CoverageEvidence: "invoice-1"}
	for _, tc := range []struct {
		name      string
		cost      TestCost
		wantCost  string
		confirmed bool
	}{
		{"covered", base, "15.000000", true},
		{"extra", func() TestCost {
			c := base
			c.Coverage = "extra"
			c.CoverageEvidence = "outside invoice"
			c.Refund = "0.5"
			c.RefundEvidence = "supplier credit"
			return c
		}(), "16.500000", true},
		{"unknown", func() TestCost { c := base; c.Coverage = "unknown"; c.CoverageEvidence = ""; return c }(), "15.000000", false},
		{"estimate extra", func() TestCost {
			c := base
			c.Status = "estimate"
			c.EvidenceType = "quote"
			c.Coverage = "extra"
			c.CoverageEvidence = "outside invoice"
			return c
		}(), "15.000000", false},
		{"wrong invoice", func() TestCost { c := base; c.CoverageEvidence = "different invoice"; return c }(), "15.000000", false},
		{"extra cites included invoice", func() TestCost { c := base; c.Coverage = "extra"; return c }(), "15.000000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := append(facts(), Fact{ID: 3, Kind: "test_cost", Ref: "test:r1:1:2", Payload: tc.cost})
			r := calculate(t, fixture(), f)
			require.Equal(t, tc.confirmed, r.Confirmed, "%+v", r.Issues)
			require.Equal(t, tc.wantCost, r.RecordedCost)
			if !tc.confirmed {
				require.Nil(t, r.GrossProfit)
			}
			raw, err := common.Marshal(r)
			require.NoError(t, err)
			require.Contains(t, string(raw), fmt.Sprintf(`"status":"%s"`, tc.cost.Status))
		})
	}
}
