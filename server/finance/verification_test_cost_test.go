package finance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func verificationCostSubmission(t *testing.T, id int64) Submission {
	t.Helper()
	var in Submission
	raw := fmt.Sprintf(`{"kind":"test_cost","ref":"test:verification:%d:1:7","test_cost":{"verification_id":%d,"request_id":"verification:%d","attempt":1,"channel_id":7,"status":"estimate","evidence_type":"quote","coverage":"unknown","amount":"0.12","refund":"0","currency":"USDT","rate":"1","evidence":"Frozen quote for both managed verifier calls; not supplier debit"},"idempotency_key":"verification-test-%d-quote"}`, id, id, id, id)
	require.NoError(t, common.UnmarshalJsonStr(raw, &in))
	return in
}

func verificationCostSnapshot() Snapshot {
	s := fixture()
	s.Verifications = []Verification{{ID: 41, ChannelID: 7, ModelConfigID: 901,
		VerifiedAt: moment("2026-09-30T15:59:59Z").Unix()}}
	s.Channels = []Channel{{ID: 7, Name: "Xcloud GPT Plus", BaseURL: "https://www.xcloudtoken.com"}}
	return s
}

func verificationCostFact(in Submission, id uint) Fact {
	return Fact{ID: id, Kind: in.Kind, Ref: in.Ref, Payload: *in.TestCost,
		PreviousID: in.PreviousID, CreatedAt: moment("2026-10-01T00:00:00Z")}
}

func verificationCostBody(t *testing.T, in Submission) string {
	t.Helper()
	raw, err := common.Marshal(in)
	require.NoError(t, err)
	return string(raw)
}

func TestFinanceVerificationCostWireFieldAndLegacyOmission(t *testing.T) {
	in := verificationCostSubmission(t, 41)
	require.Contains(t, verificationCostBody(t, in), `"verification_id":41`)
	legacy := revenueTestCost()
	raw, err := common.Marshal(legacy)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "verification_id")
}

func TestFinanceVerificationCostQuoteHTTPWithoutSettlement(t *testing.T) {
	base, token := api(t, common.RoleFinanceUser)
	h := base.(*handler)
	s := verificationCostSnapshot()
	s.Settlements, s.Attempts, s.Ledgers, s.Payments = nil, nil, nil, nil
	h.source = &testSource{s: s, role: common.RoleFinanceUser}
	in := verificationCostSubmission(t, 41)
	w := request(h, token, "POST", prefix+"facts", verificationCostBody(t, in), h.origin)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	w = request(h, token, "GET", prefix+"report?month=2026-09", "", "")
	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Data Report `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "0.120000", response.Data.TestEstimateTotal)
	require.Equal(t, "0.000000", response.Data.TestConfirmedTotal)
	require.Equal(t, "0.000000", response.Data.KnownRevenue)
	require.Equal(t, "0.000000", response.Data.RecordedCost)
	require.Nil(t, response.Data.ActualCost)
	require.Len(t, response.Data.TestCosts, 1)
	require.Equal(t, "verification:41", response.Data.TestCosts[0].SourceRef)
	require.Equal(t, "model_config:901", response.Data.TestCosts[0].Model)
	require.Empty(t, response.Data.TestCosts[0].SupplierBillID)
	require.Contains(t, w.Body.String(), `"verification_id":41`)
}

func TestFinanceVerificationCostSourceRequiresUniqueExactVerification(t *testing.T) {
	in := verificationCostSubmission(t, 41)
	require.True(t, validSourceReference(verificationCostSnapshot(), in))
	for name, mutate := range map[string]func(*Snapshot){
		"missing":          func(s *Snapshot) { s.Verifications = nil },
		"different-id":     func(s *Snapshot) { s.Verifications[0].ID = 42 },
		"wrong-channel":    func(s *Snapshot) { s.Verifications[0].ChannelID = 6 },
		"zero-time":        func(s *Snapshot) { s.Verifications[0].VerifiedAt = 0 },
		"negative-time":    func(s *Snapshot) { s.Verifications[0].VerifiedAt = -1 },
		"future-time":      func(s *Snapshot) { s.Verifications[0].VerifiedAt = time.Now().Add(24 * time.Hour).Unix() },
		"missing-model-id": func(s *Snapshot) { s.Verifications[0].ModelConfigID = 0 },
		"duplicate-id":     func(s *Snapshot) { s.Verifications = append(s.Verifications, s.Verifications[0]) },
		"duplicate-id-on-other-channel": func(s *Snapshot) {
			other := s.Verifications[0]
			other.ChannelID = 6
			s.Verifications = append(s.Verifications, other)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := verificationCostSnapshot()
			mutate(&s)
			require.False(t, validSourceReference(s, in))
			r := calculate(t, s, append(facts(), verificationCostFact(in, 3)))
			require.Empty(t, r.TestCosts)
			require.Equal(t, "0.000000", r.TestEstimateTotal)
			require.False(t, r.Confirmed)
		})
	}
}

func TestFinanceVerificationCostPayloadRejectsBadReferencesAndCustomerService(t *testing.T) {
	now := moment("2026-10-26T00:00:00Z")
	for _, purpose := range []string{"", "management_test"} {
		in := verificationCostSubmission(t, 41)
		in.TestCost.Purpose = purpose
		require.NoError(t, validateSubmission(in, now))
	}
	for name, mutate := range map[string]func(*Submission){
		"wrong-id-reference": func(in *Submission) { in.TestCost.RequestID = "verification:42" },
		"padded-id":          func(in *Submission) { in.TestCost.RequestID = "verification:041" },
		"customer-request":   func(in *Submission) { in.TestCost.RequestID = "r1" },
		"second-attempt":     func(in *Submission) { in.TestCost.Attempt = 2 },
		"customer-service":   func(in *Submission) { in.TestCost.Purpose = customerServicePurpose },
		"confirmed-quote":    func(in *Submission) { in.TestCost.Status = "confirmed" },
		"estimated-credit":   func(in *Submission) { in.TestCost.Currency, in.TestCost.Rate = "XCLOUD_CREDIT", "0.5" },
	} {
		t.Run(name, func(t *testing.T) {
			in := verificationCostSubmission(t, 41)
			mutate(&in)
			in.Ref = fmt.Sprintf("test:%s:%d:%d", in.TestCost.RequestID, in.TestCost.Attempt, in.TestCost.ChannelID)
			require.ErrorIs(t, validateSubmission(in, now), ErrInvalid)
			if name != "confirmed-quote" && name != "estimated-credit" {
				require.False(t, validSourceReference(verificationCostSnapshot(), in))
			}
		})
	}
	negative := verificationCostSubmission(t, -1)
	require.ErrorIs(t, validateSubmission(negative, now), ErrInvalid)
}

func TestFinanceVerificationCostHTTPUsesReportClockAndNeverFallsBackToSettlement(t *testing.T) {
	for _, scenario := range []string{"future-verification", "missing-verification-with-colliding-request", "customer-service"} {
		t.Run(scenario, func(t *testing.T) {
			base, token := api(t, common.RoleFinanceUser)
			h := base.(*handler)
			s := verificationCostSnapshot()
			in := verificationCostSubmission(t, 41)
			s.Settlements[0].RequestID = "verification:41"
			s.Attempts[0].ChannelID = 7
			s.Users[0].Role = common.RoleRootUser
			switch scenario {
			case "future-verification":
				s.Verifications[0].VerifiedAt = h.now().Add(time.Second).Unix()
			case "missing-verification-with-colliding-request":
				s.Verifications = nil
			case "customer-service":
				in.TestCost.Purpose = customerServicePurpose
			}
			h.source = &testSource{s: s, role: common.RoleFinanceUser}
			w := request(h, token, "POST", prefix+"facts", verificationCostBody(t, in), h.origin)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			rows, err := h.store.List(context.Background())
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	}
}

func TestFinanceVerificationCostCannotClassifyCollidingCustomerRevenueAsTest(t *testing.T) {
	s := verificationCostSnapshot()
	s.Settlements[0].RequestID = "verification:41"
	s.Ledgers[1].RequestID = "verification:41"
	s.Attempts[0].ChannelID = 7
	base := facts()
	base[1].Ref = "2026-09:7"
	base[1].Payload = Cost{Month: "2026-09", ChannelID: 7, Supplier: "Xcloud", Gross: "15", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "invoice-1"}
	in := verificationCostSubmission(t, 41)
	r := calculate(t, s, append(base, verificationCostFact(in, 3)))
	require.Equal(t, "20.000000", r.KnownRevenue)
	require.Equal(t, int64(10500000), r.ChargedQuota)
	require.Len(t, r.TestCosts, 1)
	require.Equal(t, "model_config:901", r.TestCosts[0].Model)
	require.Empty(t, r.CustomerServiceCosts)
}

func TestFinanceVerificationCostUsesVerificationMonthNotSettlementOrPostingTime(t *testing.T) {
	s := verificationCostSnapshot()
	first, second := verificationCostSubmission(t, 41), verificationCostSubmission(t, 42)
	s.Verifications = append(s.Verifications, Verification{ID: 42, ChannelID: 7, ModelConfigID: 902, VerifiedAt: moment("2026-09-30T16:00:00Z").Unix()})
	one, two := verificationCostFact(first, 3), verificationCostFact(second, 4)
	one.CreatedAt, two.CreatedAt = moment("2026-11-01T00:00:00Z"), moment("2026-11-01T00:00:00Z")
	for _, tc := range []struct{ month, source, model string }{
		{"2026-09", "verification:41", "model_config:901"},
		{"2026-10", "verification:42", "model_config:902"},
	} {
		r, err := Calculate(s, append(facts(), one, two), tc.month, moment("2026-11-02T00:00:00Z"))
		require.NoError(t, err)
		require.Len(t, r.TestCosts, 1)
		require.Equal(t, tc.source, r.TestCosts[0].SourceRef)
		require.Equal(t, tc.model, r.TestCosts[0].Model)
		require.Equal(t, "0.120000", r.TestEstimateTotal)
	}
}

func TestFinanceVerificationCostEstimateRevisionAndActualReceiptStayAppendOnly(t *testing.T) {
	store := newStore(t)
	ctx, now := context.Background(), moment("2026-10-26T00:00:00Z")
	in := verificationCostSubmission(t, 41)
	quote, err := store.Append(ctx, in, 1, now)
	require.NoError(t, err)
	retry, err := store.Append(ctx, in, 1, now)
	require.NoError(t, err)
	require.Equal(t, quote.ID, retry.ID)
	in.IdempotencyKey = "verification-duplicate-with-new-key"
	_, err = store.Append(ctx, in, 1, now)
	require.ErrorIs(t, err, ErrConflict)

	in.PreviousID, in.IdempotencyKey = quote.ID, "verification-actual-receipt-41"
	c := in.TestCost
	c.Status, c.EvidenceType, c.Amount = "confirmed", "supplier_statement", "0.20"
	c.Currency, c.Rate, c.RateEvidence = "XCLOUD_CREDIT", "0.5", "Synthetic receipt fixture: 100 USDT buys 200 credits"
	c.Evidence, c.SupplierBillID = "Synthetic supplier statement covering both verification calls", "synthetic-verification-bill-41"
	confirmed, err := store.Append(ctx, in, 1, now)
	require.NoError(t, err)
	all, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, "estimate", all[0].Payload.(TestCost).Status)
	r := calculate(t, verificationCostSnapshot(), append(facts(), all...))
	require.Len(t, r.TestCosts, 1)
	require.Equal(t, confirmed.ID, r.TestCosts[0].FactID)
	require.Equal(t, "0.000000", r.TestEstimateTotal)
	require.Equal(t, "0.100000", r.TestConfirmedTotal)
	require.Equal(t, "0.20000000", r.TestXcloudCreditTotal)
	require.Equal(t, "15.000000", r.RecordedCost, "unknown invoice coverage must not double-book the receipt")
	require.Nil(t, r.ActualCost)

	legacy := in
	legacy.TestCost = new(TestCost)
	raw, err := common.Marshal(in.TestCost)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(raw, &payload))
	delete(payload, "verification_id")
	raw, err = common.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, legacy.TestCost))
	legacy.PreviousID, legacy.IdempotencyKey = confirmed.ID, "verification-cannot-become-customer"
	_, err = store.Append(ctx, legacy, 1, now)
	require.ErrorIs(t, err, ErrConflict)
}

func TestFinanceVerificationCostReportAndCSVExposeSourceWithoutFakeBill(t *testing.T) {
	in := verificationCostSubmission(t, 41)
	r := calculate(t, verificationCostSnapshot(), append(facts(), verificationCostFact(in, 3)))
	require.Len(t, r.TestCosts, 1)
	w := httptest.NewRecorder()
	exportReport(w, r)
	require.Contains(t, w.Body.String(), "verification:41,1,7,model_config:901")
	require.NotContains(t, w.Body.String(), "fake-bill")
	raw, err := common.Marshal(r.TestCosts[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"verification_id":41`)
	require.Contains(t, string(raw), `"source_ref":"verification:41"`)
}

func TestFinanceVerificationCostQuoteCannotIncreaseActualCostOrInventSupplierBill(t *testing.T) {
	in := verificationCostSubmission(t, 41)
	in.TestCost.Coverage, in.TestCost.CoverageEvidence = "extra", "Quoted expense outside the supplier month invoice"
	fact := verificationCostFact(in, 3)
	r := calculate(t, verificationCostSnapshot(), append(facts(), fact, fact))
	require.Equal(t, "0.120000", r.TestEstimateTotal, "duplicate reads are not duplicate expenditure")
	require.Equal(t, "0.000000", r.TestConfirmedTotal)
	require.Equal(t, "15.000000", r.RecordedCost)
	require.Nil(t, r.ActualCost)
	require.Len(t, r.TestCosts, 1)
	require.Empty(t, r.TestCosts[0].SupplierBillID)
}

func TestFinanceVerificationCostUnconvertedCreditKeepsVerificationProvenance(t *testing.T) {
	in := verificationCostSubmission(t, 41)
	c := in.TestCost
	c.Status, c.EvidenceType, c.Currency = "confirmed", "supplier_statement", "XCLOUD_CREDIT"
	c.Amount, c.Rate, c.Evidence = "0.20", "", "Synthetic supplier statement for verification 41"
	require.NoError(t, validateSubmission(in, moment("2026-10-26T00:00:00Z")))
	r := calculate(t, verificationCostSnapshot(), append(facts(), verificationCostFact(in, 3)))
	require.Len(t, r.TestCosts, 1)
	require.Equal(t, "verification:41", r.TestCosts[0].SourceRef)
	require.Equal(t, "model_config:901", r.TestCosts[0].Model)
	require.Equal(t, "0.20000000", r.TestCosts[0].NetCredit)
	require.Empty(t, r.TestCosts[0].NetUSDT)
	require.Equal(t, "0.000000", r.TestConfirmedTotal)
	require.Nil(t, r.ActualCost)
}

func TestFinanceVerificationCostActualBillIdentityCannotBeReusedByDirectCost(t *testing.T) {
	store := newStore(t)
	now := moment("2026-10-26T00:00:00Z")
	in := verificationCostSubmission(t, 41)
	in.TestCost.Status, in.TestCost.EvidenceType = "confirmed", "supplier_statement"
	in.TestCost.Evidence, in.TestCost.SupplierBillID = "Synthetic supplier statement", "synthetic-shared-verification-bill"
	_, err := store.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	_, err = store.Append(context.Background(), directBill("SYNTHETIC-SHARED-VERIFICATION-BILL"), 1, now)
	require.ErrorIs(t, err, ErrConflict)
}

func TestFinanceVerificationCostZeroFieldRetainsLegacyCustomerSource(t *testing.T) {
	legacy := revenueTestCost()
	raw, err := common.Marshal(legacy)
	require.NoError(t, err)
	var in Submission
	body := fmt.Sprintf(`{"kind":"test_cost","ref":"test:r1:1:2","test_cost":%s,"idempotency_key":"legacy-zero-verification"}`, raw)
	body = strings.Replace(body, `"request_id":"r1"`, `"verification_id":0,"request_id":"r1"`, 1)
	require.NoError(t, common.UnmarshalJsonStr(body, &in))
	require.NoError(t, validateSubmission(in, moment("2026-10-26T00:00:00Z")))
	require.True(t, validSourceReference(fixture(), in))
	require.NotContains(t, verificationCostBody(t, in), "verification_id")
	r := calculate(t, fixture(), append(facts(), verificationCostFact(in, 3)))
	require.Equal(t, "0.000000", r.KnownRevenue)
	require.Equal(t, "15.000000", r.RecordedCost)
	require.Len(t, r.TestCosts, 1)
	require.Empty(t, r.TestCosts[0].SourceRef)
}
