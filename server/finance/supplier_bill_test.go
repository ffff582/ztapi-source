package finance

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func requestBill(t *testing.T, request, bill string) Submission {
	t.Helper()
	var in Submission
	body := fmt.Sprintf(`{"kind":"test_cost","ref":"test:%s:1:2","test_cost":{"request_id":"%s","attempt":1,"channel_id":2,"supplier_bill_id":%q,"status":"confirmed","evidence_type":"supplier_statement","coverage":"included","amount":"2","refund":"0","currency":"XCLOUD_CREDIT","rate":"0.5","rate_evidence":"paid 100 USDT for 200 credits","evidence":"supplier receipt","coverage_evidence":"invoice-1"},"idempotency_key":"request-bill-%s"}`, request, request, bill, request)
	require.NoError(t, common.UnmarshalJsonStr(body, &in))
	return in
}

func directBill(bill string) Submission {
	return Submission{Kind: "direct_cost", DirectCost: &DirectCost{Supplier: "supplier pool", Account: "account-1", BillID: bill, VerificationID: 656, ChannelID: 2, OccurredAt: moment("2026-09-16T02:00:00Z"), Amount: "2", Currency: "XCLOUD_CREDIT", Rate: "0.5", RateEvidence: "paid 100 USDT for 200 credits", Evidence: "supplier receipt", LinkEvidence: "verification link", Coverage: "included", CoverageEvidence: "invoice-1", MonthCostFactID: 2}, IdempotencyKey: "direct-bill-identity"}
}

func TestSupplierBillIdentityRejectsCrossKindAndCrossRequestReuse(t *testing.T) {
	for _, scenario := range []string{"direct then test", "test then direct", "two requests", "two attempts"} {
		t.Run(scenario, func(t *testing.T) {
			s := newStore(t)
			first, second := requestBill(t, "r1", "Shared-Bill"), requestBill(t, "r2", "shared-bill")
			switch scenario {
			case "direct then test":
				first = directBill("Shared-Bill")
			case "test then direct":
				second = directBill("shared-bill")
			case "two attempts":
				second = requestBill(t, "r1", "shared-bill")
				second.TestCost.Attempt = 2
				second.Ref = "test:r1:2:2"
				second.IdempotencyKey = "second-attempt-key"
			}
			head, err := s.Append(context.Background(), first, 1, moment("2026-10-30T00:00:00Z"))
			require.NoError(t, err)
			for _, previous := range []uint{0, head.ID} {
				second.PreviousID = previous
				_, err = s.Append(context.Background(), second, 1, moment("2026-10-30T00:00:00Z"))
				require.ErrorIs(t, err, ErrConflict, "a foreign head must not authorize another record")
			}
			all, err := s.List(context.Background())
			require.NoError(t, err)
			require.Len(t, all, 1)
		})
	}
}

func TestSupplierBillLegacyRevisionAndRetriesAreAppendOnly(t *testing.T) {
	s := newStore(t)
	ctx, now := context.Background(), moment("2026-10-30T00:00:00Z")
	legacy := requestBill(t, "r1", "")
	a, err := s.Append(ctx, legacy, 1, now)
	require.NoError(t, err)
	var original factRecord
	require.NoError(t, s.db.First(&original, a.ID).Error)
	in := requestBill(t, "r1", "new-supplier-bill")
	in.PreviousID, in.IdempotencyKey = a.ID, "legacy-bill-revision"
	b, err := s.Append(ctx, in, 1, now)
	require.NoError(t, err)
	for _, retry := range []Submission{legacy, in} {
		got, err := s.Append(ctx, retry, 1, now)
		require.NoError(t, err)
		if retry.PreviousID == 0 {
			require.Equal(t, a.ID, got.ID)
		} else {
			require.Equal(t, b.ID, got.ID)
		}
	}
	in.PreviousID, in.IdempotencyKey = b.ID, "bill-amount-revision"
	in.TestCost.Amount = "3"
	c, err := s.Append(ctx, in, 1, now)
	require.NoError(t, err)
	require.Equal(t, b.ID, c.PreviousID)
	in.IdempotencyKey = "stale-bill-revision"
	_, err = s.Append(ctx, in, 1, now)
	require.ErrorIs(t, err, ErrConflict)
	var unchanged factRecord
	require.NoError(t, s.db.First(&unchanged, a.ID).Error)
	require.Equal(t, original, unchanged)
	all, err := s.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 3)
	raw, err := common.Marshal(all[1].Payload)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"supplier_bill_id":"new-supplier-bill"`)
	duplicate := requestBill(t, "r2", "NEW-SUPPLIER-BILL")
	_, err = s.Append(ctx, duplicate, 1, now)
	require.ErrorIs(t, err, ErrConflict)
}

func TestSupplierBillTestCostCannotClaimLegacyDirectIdentity(t *testing.T) {
	s := newStore(t)
	in := directBill("legacy-direct-bill")
	raw, err := common.Marshal(in.DirectCost)
	require.NoError(t, err)
	row := factRecord{Kind: "direct_cost", Ref: directCostRef(in.DirectCost), Payload: string(raw), PayloadHash: "legacy", OperatorID: 1, IdempotencyKey: "legacy-direct-bill"}
	require.NoError(t, s.db.Create(&row).Error)
	_, err = s.Append(context.Background(), requestBill(t, "r1", "LEGACY-DIRECT-BILL"), 1, moment("2026-10-30T00:00:00Z"))
	require.ErrorIs(t, err, ErrConflict)
}

func TestSupplierBillIdentityDoesNotBypassRefundDeduplication(t *testing.T) {
	s := newStore(t)
	for i, request := range []string{"r1", "r2"} {
		in := requestBill(t, request, "bill-"+request)
		in.TestCost.Coverage, in.TestCost.CoverageEvidence = "extra", "outside invoice"
		in.TestCost.Refund, in.TestCost.RefundEvidence = "1", "shared-credit-note"
		_, err := s.Append(context.Background(), in, 1, moment("2026-10-30T00:00:00Z"))
		if i == 0 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrConflict)
		}
	}
}

func TestDistinctSupplierBillsPreserveCoverageAndCountMonthlyInvoiceOnce(t *testing.T) {
	for _, tc := range []struct {
		name, bill, coverage string
		confirmed            bool
		cost                 string
	}{
		{"distinct included", "request-bill", "included", true, "15.000000"},
		{"distinct extra", "request-bill", "extra", true, "16.000000"},
		{"legacy unknown identity", "", "included", false, "15.000000"},
		{"same identity", "DIRECT-BILL", "included", false, "15.000000"},
		{"distinct unknown coverage", "request-bill", "unknown", false, "15.000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			direct := *directBill("direct-bill").DirectCost
			direct.Coverage = tc.coverage
			f := facts()
			if tc.coverage == "extra" {
				direct.CoverageEvidence = "outside invoice"
				monthly := f[1].Payload.(Cost)
				monthly.ExcludedDirectBillRefs = []string{directCostRef(&direct)}
				f[1].Payload = monthly
			} else if tc.coverage == "unknown" {
				direct.CoverageEvidence, direct.MonthCostFactID = "", 0
			}
			request := requestBill(t, "r1", tc.bill)
			f = append(f, Fact{ID: 3, Kind: "direct_cost", Ref: directCostRef(&direct), Payload: direct}, Fact{ID: 4, Kind: "test_cost", Ref: request.Ref, Payload: *request.TestCost})
			s := fixture()
			s.Verifications = []Verification{{ID: 656, ChannelID: 2, VerifiedAt: direct.OccurredAt.Unix()}}
			r := calculate(t, s, f)
			require.Equal(t, tc.confirmed, r.Confirmed, "%+v", r.Issues)
			require.Equal(t, tc.cost, r.RecordedCost)
			require.Equal(t, "1.000000", r.DirectConfirmedTotal)
			require.Equal(t, "2.00000000", r.TestXcloudCreditTotal)
			if tc.confirmed {
				require.Equal(t, tc.cost, *r.ActualCost)
				if tc.coverage == "included" {
					require.Equal(t, "-15.000000", *r.GrossProfit, "the marked test has no customer revenue; the monthly invoice is still deducted once")
				}
			} else {
				require.Nil(t, r.GrossProfit)
			}
		})
	}
}

func TestSupplierBillIDValidationAndCSVPreserveMissingValues(t *testing.T) {
	for _, bill := range []string{" ", " bill", "bill ", strings.Repeat("b", 129)} {
		require.ErrorIs(t, validateSubmission(requestBill(t, "r1", bill), moment("2026-10-30T00:00:00Z")), ErrInvalid)
	}
	w := httptest.NewRecorder()
	in := requestBill(t, "r1", "=supplier-bill")
	exportReport(w, Report{Month: "2026-09", TestCosts: []TestCostRow{{TestCost: *in.TestCost}, {TestCost: *requestBill(t, "r2", "").TestCost}}})
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff")))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	require.NoError(t, err)
	for i, row := range rows {
		if row[0] != "test_request_id" {
			continue
		}
		require.Equal(t, "supplier_bill_id", row[len(row)-1])
		require.Len(t, rows[i+1], len(row))
		require.Equal(t, "'=supplier-bill", rows[i+1][len(row)-1])
		require.Equal(t, "", rows[i+2][len(row)-1])
		require.Equal(t, "", rows[i+1][4], "unknown input tokens stay blank")
		return
	}
	t.Fatal("missing request test-cost CSV section")
}
