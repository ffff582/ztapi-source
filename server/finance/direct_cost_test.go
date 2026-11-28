package finance

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestDirectCostCacheEvidenceRoundTripAndNoAutomaticPricing(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-30T00:00:00Z")
	body := `{"kind":"direct_cost","direct_cost":{"supplier":"Xcloud","account":"account-1","bill_id":"cache-bill","verification_id":656,"channel_id":2,"occurred_at":"2026-10-29T00:00:00Z","input_tokens":1000000,"output_tokens":0,"cache_read":900000,"cache_write_5m":0,"cache_write_1h":null,"input_token_scope":"inclusive","amount":"2","currency":"XCLOUD_CREDIT","rate":"0.5","rate_evidence":"recharge receipt","evidence":"supplier receipt","link_evidence":"verification link","coverage":"unknown"},"idempotency_key":"cache-bill-key"}`
	var in Submission
	require.NoError(t, common.UnmarshalJsonStr(body, &in))
	_, err := s.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	all, err := s.List(context.Background())
	require.NoError(t, err)
	raw, err := common.Marshal(all[0].Payload)
	require.NoError(t, err)
	for _, fragment := range []string{`"cache_read":900000`, `"cache_write_5m":0`, `"cache_write_1h":null`, `"input_token_scope":"inclusive"`} {
		require.Contains(t, string(raw), fragment)
	}
	r, err := Calculate(fixture(), all, "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, "1.000000", r.DirectCosts[0].NetUSDT)
	for _, scope := range []string{"inclusive", "exclusive", "unknown"} {
		c := all[0].Payload.(DirectCost)
		c.InputTokenScope = scope
		all[0].Payload = c
		result, e := Calculate(fixture(), all, "2026-10", moment("2026-11-02T00:00:00Z"))
		require.NoError(t, e)
		require.Equal(t, "1.000000", result.DirectCosts[0].NetUSDT)
	}
	w := httptest.NewRecorder()
	exportReport(w, r)
	require.Contains(t, w.Body.String(), "input_token_scope,cache_read,cache_write_5m,cache_write_1h")
	require.Contains(t, w.Body.String(), "inclusive,900000,0,")
}

func TestDirectCostCacheValidationAndLegacyUnknowns(t *testing.T) {
	var c DirectCost
	require.NoError(t, common.UnmarshalJsonStr(`{"supplier":"Xcloud","account":"a","bill_id":"b","verification_id":1,"channel_id":2,"occurred_at":"2026-10-29T00:00:00Z","amount":"2","currency":"USDT","rate":"1","evidence":"receipt","link_evidence":"verification","coverage":"unknown"}`, &c))
	raw, err := common.Marshal(c)
	require.NoError(t, err)
	for _, fragment := range []string{`"cache_read":null`, `"cache_write_5m":null`, `"cache_write_1h":null`} {
		require.Contains(t, string(raw), fragment)
	}
	for _, mutation := range []string{`"cache_read":-1`, `"cache_write_5m":-1`, `"cache_write_1h":-1`, `"input_token_scope":"invalid"`} {
		body := strings.Replace(`{"supplier":"Xcloud","account":"a","bill_id":"b","verification_id":1,"channel_id":2,"occurred_at":"2026-10-29T00:00:00Z","amount":"2","currency":"USDT","rate":"1","evidence":"receipt","link_evidence":"verification","coverage":"unknown"}`, `"supplier"`, mutation+`,"supplier"`, 1)
		var candidate DirectCost
		require.NoError(t, common.UnmarshalJsonStr(body, &candidate))
		require.ErrorIs(t, validateDirectCost(&candidate, moment("2026-10-30T00:00:00Z")), ErrInvalid, mutation)
	}
}

func TestDirectCostLegacyIdempotentRetrySurvivesAddedNullFields(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-30T00:00:00Z")
	legacy := `{"supplier":"Xcloud","account":"account-1","bill_id":"legacy-retry","verification_id":656,"channel_id":2,"occurred_at":"2026-10-29T00:00:00Z","amount":"1","currency":"XCLOUD_CREDIT","rate":"","rate_evidence":"","evidence":"receipt","link_evidence":"verification link","coverage":"unknown","coverage_evidence":"","month_cost_fact_id":0}`
	var c DirectCost
	require.NoError(t, common.UnmarshalJsonStr(legacy, &c))
	ref := directCostRef(&c)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("direct_cost\n%s\n0\n%s", ref, legacy))))
	row := factRecord{Kind: "direct_cost", Ref: ref, IdempotencyKey: "legacy-retry-key", Payload: legacy, PayloadHash: digest, OperatorID: 1, CreatedAt: now}
	require.NoError(t, s.db.Create(&row).Error)
	got, err := s.Append(context.Background(), Submission{Kind: "direct_cost", DirectCost: &c, IdempotencyKey: "legacy-retry-key"}, 1, now)
	require.NoError(t, err)
	require.Equal(t, row.ID, got.ID)
	c.OccurredAt = c.OccurredAt.In(time.FixedZone("CST", 8*3600))
	got, err = s.Append(context.Background(), Submission{Kind: "direct_cost", DirectCost: &c, IdempotencyKey: "legacy-retry-key"}, 1, now)
	require.NoError(t, err)
	require.Equal(t, row.ID, got.ID)
	n := int64(1)
	c.CacheRead = &n
	_, err = s.Append(context.Background(), Submission{Kind: "direct_cost", DirectCost: &c, IdempotencyKey: "legacy-retry-key"}, 1, now)
	require.ErrorIs(t, err, ErrConflict)
}

func TestDirectCostVerificationTimeWindow(t *testing.T) {
	verified := moment("2026-09-16T02:00:00Z")
	s := Snapshot{Verifications: []Verification{{ID: 656, ChannelID: 2, VerifiedAt: verified.Unix()}}}
	c := &DirectCost{VerificationID: 656, ChannelID: 2}
	for _, delta := range []time.Duration{-15 * time.Minute, 0, 15 * time.Minute} {
		c.OccurredAt = verified.Add(delta)
		require.True(t, directVerificationMatches(s, c), delta)
	}
	for _, delta := range []time.Duration{-15*time.Minute - time.Second, 15*time.Minute + time.Second} {
		c.OccurredAt = verified.Add(delta)
		require.False(t, directVerificationMatches(s, c), delta)
	}
	other := *c
	other.OccurredAt = verified
	other.ChannelID = 3
	require.False(t, directVerificationMatches(s, &other))
	c.OccurredAt = verified
	s.Verifications = append(s.Verifications, Verification{ID: 656, ChannelID: 2, VerifiedAt: verified.Add(time.Hour).Unix()})
	require.False(t, directVerificationMatches(s, c), "ambiguous duplicate verification IDs must not pass")
}

func TestDirectCostRejectsTokenCountsBeyondExactFrontendRange(t *testing.T) {
	for _, field := range []string{"input_tokens", "output_tokens", "cache_read", "cache_write_5m", "cache_write_1h"} {
		body := fmt.Sprintf(`{"supplier":"Xcloud","account":"a","bill_id":"b","verification_id":1,"channel_id":2,"occurred_at":"2026-10-29T00:00:00Z","amount":"2","currency":"USDT","rate":"1","evidence":"receipt","link_evidence":"verification","coverage":"unknown","%s":9007199254740992}`, field)
		var c DirectCost
		require.NoError(t, common.UnmarshalJsonStr(body, &c))
		require.ErrorIs(t, validateDirectCost(&c, moment("2026-10-30T00:00:00Z")), ErrInvalid, field)
	}
}

func TestOutOfWindowDirectCostCannotConfirmProfit(t *testing.T) {
	c := DirectCost{Supplier: "supplier pool", Account: "a", BillID: "bill-time", VerificationID: 656, ChannelID: 2, OccurredAt: moment("2026-09-16T10:00:00+08:00"), Amount: "2", Currency: "USD", Rate: "1", RateEvidence: "conversion", Evidence: "receipt", LinkEvidence: "link", Coverage: "included", CoverageEvidence: "invoice-1", MonthCostFactID: 2}
	s := fixture()
	s.Verifications = []Verification{{ID: 656, ChannelID: 2, VerifiedAt: c.OccurredAt.Add(-16 * time.Minute).Unix()}}
	r := calculate(t, s, append(facts(), Fact{ID: 3, Kind: "direct_cost", Ref: directCostRef(&c), Payload: c}))
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
	require.Contains(t, r.Issues, Issue{Code: "direct_cost_lineage_missing", Ref: directCostRef(&c)})
}

func TestDirectVerificationBillsAreDistinctAndCannotBeImportedTwice(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-30T00:00:00Z")
	first := `{"kind":"direct_cost","direct_cost":{"supplier":"Xcloud","account":"account-1","bill_id":"202610291432465335980358268d9d6lTSMhN5F","verification_id":656,"channel_id":2,"occurred_at":"2026-10-29T14:32:46+08:00","amount":"0.000022","currency":"XCLOUD_CREDIT","evidence":"statement-1","link_evidence":"verification-656-link","coverage":"unknown"},"idempotency_key":"direct-bill-1"}`
	var in Submission
	require.NoError(t, common.UnmarshalJsonStr(first, &in))
	a, err := s.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	in.IdempotencyKey = "direct-bill-duplicate"
	_, err = s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, err, ErrConflict)
	in.DirectCost.Supplier = "xcloud"
	in.IdempotencyKey = "direct-bill-case"
	_, err = s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, err, ErrConflict, "supplier capitalization must not create a new identity")
	in.DirectCost.Supplier = "other supplier alias"
	in.DirectCost.Account = "other account alias"
	in.IdempotencyKey = "direct-bill-alias"
	_, err = s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, err, ErrConflict, "the same bill cannot be reimported with different supplier or account labels")
	second := `{"kind":"direct_cost","direct_cost":{"supplier":"Xcloud","account":"account-1","bill_id":"202610291432494237804578268d9d6MZKPxGZo","verification_id":656,"channel_id":2,"occurred_at":"2026-10-29T14:32:49+08:00","amount":"0.000020","currency":"XCLOUD_CREDIT","evidence":"statement-2","link_evidence":"verification-656-link","coverage":"unknown"},"idempotency_key":"direct-bill-2"}`
	in = Submission{}
	require.NoError(t, common.UnmarshalJsonStr(second, &in))
	b, err := s.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	require.NotEqual(t, a.Ref, b.Ref)
	facts, err := s.List(context.Background())
	require.NoError(t, err)
	r, err := Calculate(fixture(), facts, "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, err)
	raw, err := common.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"direct_xcloud_credit_total":"0.00004200"`)
	require.Nil(t, r.GrossProfit)
}

func TestDirectCostHTTPRequiresMatchingVerification(t *testing.T) {
	h, token := api(t, common.RoleFinanceUser)
	body := `{"kind":"direct_cost","direct_cost":{"supplier":"Xcloud","account":"account-1","bill_id":"bill-1","verification_id":656,"channel_id":2,"occurred_at":"2026-10-25T14:32:46+08:00","amount":"0.000022","currency":"XCLOUD_CREDIT","evidence":"statement-1","link_evidence":"verification-link","coverage":"unknown"},"idempotency_key":"direct-http-1"}`
	path := "/api/admin/finance-reporting/facts"
	require.Equal(t, http.StatusBadRequest, request(h, token, "POST", path, body, "https://admin.example.test").Code)
	s := fixture()
	s.Verifications = []Verification{{ID: 656, ChannelID: 2, VerifiedAt: moment("2026-10-25T14:33:00+08:00").Unix()}}
	require.NoError(t, service.ConfigureZTAPISessionSigningKey(strings.Repeat("test-only-finance-key", 3)))
	h = NewHandler(&testSource{s, common.RoleFinanceUser}, newStore(t), "https://admin.example.test", func() time.Time { return moment("2026-10-26T00:01:00Z") })
	require.Equal(t, http.StatusCreated, request(h, token, "POST", path, body, "https://admin.example.test").Code)
	require.Equal(t, http.StatusBadRequest, request(h, token, "POST", path, strings.Replace(body, `"channel_id":2`, `"channel_id":3`, 1), "https://admin.example.test").Code)
	require.Equal(t, http.StatusBadRequest, request(h, token, "POST", path, strings.Replace(body, `2026-10-25T14:32:46+08:00`, `2026-10-25T13:32:46+08:00`, 1), "https://admin.example.test").Code)
}

func TestDirectCostCoverageRequiresCurrentCompleteMonthlyBill(t *testing.T) {
	c := DirectCost{Supplier: "supplier pool", Account: "account-1", BillID: "bill-1", VerificationID: 656, ChannelID: 2, OccurredAt: moment("2026-09-16T10:00:00+08:00"), Amount: "2", Currency: "USD", Rate: "1", RateEvidence: "actual conversion", Evidence: "supplier receipt", LinkEvidence: "verification link", Coverage: "included", CoverageEvidence: "invoice-1", MonthCostFactID: 2}
	f := append(facts(), Fact{ID: 3, Kind: "direct_cost", Ref: directCostRef(&c), Payload: c})
	s := fixture()
	s.Verifications = []Verification{{ID: 656, ChannelID: 2, VerifiedAt: moment("2026-09-16T02:00:00Z").Unix()}}
	r := calculate(t, s, f)
	require.Equal(t, "15.000000", r.RecordedCost)
	require.True(t, r.Confirmed)
	require.Equal(t, "2.000000", r.DirectConfirmedTotal, "direct verification is a separate test-cost breakdown, not extra monthly cost")
	c.MonthCostFactID = 999
	f[2].Payload = c
	r = calculate(t, s, f)
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
	c.MonthCostFactID = 2
	c.Coverage = "extra"
	c.CoverageEvidence = "statement explicitly excludes direct account"
	f[2].Payload = c
	r = calculate(t, s, f)
	require.Equal(t, "15.000000", r.RecordedCost)
	require.False(t, r.Confirmed, "an unstructured claim cannot establish a monthly exclusion")
	monthly := f[1].Payload.(Cost)
	raw, err := common.Marshal(monthly)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, common.Unmarshal(raw, &data))
	data["excluded_direct_bill_refs"] = []string{directCostRef(&c)}
	raw, err = common.Marshal(data)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, &monthly))
	f[1].Payload = monthly
	r = calculate(t, s, f)
	require.Equal(t, "17.000000", r.RecordedCost)
	require.True(t, r.Confirmed)
	f = append(f, Fact{ID: 4, Kind: "cost", Ref: "2026-09:3", Payload: Cost{Month: "2026-09", ChannelID: 3, Supplier: "supplier pool", Gross: "1", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "other invoice"}})
	r = calculate(t, s, f)
	require.False(t, r.Confirmed, "another same-supplier monthly bill may include this expense")
	require.Equal(t, "16.000000", r.RecordedCost)
	f = f[:3]
	r = calculate(t, fixture(), f)
	require.False(t, r.Confirmed, "a missing verification cannot support confirmed profit")
	require.Equal(t, "15.000000", r.RecordedCost)
}

func TestIncludedDirectBillMustNotAlsoBeIncludedByAnotherMonthlyBill(t *testing.T) {
	c := DirectCost{Supplier: "supplier pool", Account: "account-1", BillID: "bill-1", VerificationID: 656, ChannelID: 2, OccurredAt: moment("2026-09-16T10:00:00+08:00"), Amount: "2", Currency: "USD", Rate: "1", RateEvidence: "actual conversion", Evidence: "supplier receipt", LinkEvidence: "verification link", Coverage: "included", CoverageEvidence: "invoice-1", MonthCostFactID: 2}
	f := append(facts(), Fact{ID: 3, Kind: "direct_cost", Ref: directCostRef(&c), Payload: c}, Fact{ID: 4, Kind: "cost", Ref: "2026-09:3", Payload: Cost{Month: "2026-09", ChannelID: 3, Supplier: "supplier pool", Gross: "3", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "invoice-2"}})
	s := fixture()
	s.Verifications = []Verification{{ID: 656, ChannelID: 2, VerifiedAt: moment("2026-09-16T02:00:00Z").Unix()}}
	r := calculate(t, s, f)
	require.False(t, r.Confirmed)
	require.Nil(t, r.GrossProfit)
	other := f[3].Payload.(Cost)
	other.ExcludedDirectBillRefs = []string{directCostRef(&c)}
	f[3].Payload = other
	r = calculate(t, s, f)
	require.True(t, r.Confirmed)
}

func TestDirectCostDoesNotConfirmWhenExistingTestCostMayOverlap(t *testing.T) {
	c := DirectCost{Supplier: "supplier pool", Account: "account-1", BillID: "bill-1", VerificationID: 656, ChannelID: 2, OccurredAt: moment("2026-09-16T10:00:00+08:00"), Amount: "2", Currency: "USD", Rate: "1", RateEvidence: "actual conversion", Evidence: "supplier receipt", LinkEvidence: "verification link", Coverage: "extra", CoverageEvidence: "outside monthly invoice", MonthCostFactID: 2}
	f := facts()
	monthly := f[1].Payload.(Cost)
	monthly.ExcludedDirectBillRefs = []string{directCostRef(&c)}
	f[1].Payload = monthly
	test := TestCost{RequestID: "r1", Attempt: 1, ChannelID: 2, Status: "confirmed", EvidenceType: "supplier_statement", Coverage: "included", Amount: "2", Refund: "0", Currency: "USDT", Rate: "1", Evidence: "bill-1", CoverageEvidence: "invoice-1"}
	f = append(f, Fact{ID: 3, Kind: "direct_cost", Ref: directCostRef(&c), Payload: c}, Fact{ID: 4, Kind: "test_cost", Ref: "test:r1:1:2", Payload: test})
	r := calculate(t, fixture(), f)
	require.False(t, r.Confirmed)
	require.Equal(t, "15.000000", r.RecordedCost)
}

func TestDirectCostCSVIncludesIdentityAndEscapesFormula(t *testing.T) {
	w := httptest.NewRecorder()
	input, output := int64(123), int64(0)
	exportReport(w, Report{Month: "2026-09", DirectCosts: []DirectCostRow{{DirectCost: DirectCost{Supplier: "=formula", Account: "account-1", BillID: "bill-1", VerificationID: 656, ChannelID: 2, Model: "model-a", InputTokens: &input, OutputTokens: &output, OccurredAt: moment("2026-09-16T00:00:00Z"), Amount: "0.000022", Currency: "XCLOUD_CREDIT", Evidence: "receipt", LinkEvidence: "link"}, Ref: "identity", FactID: 7}}})
	require.Contains(t, w.Body.String(), "direct_supplier,direct_account,direct_bill_id")
	require.Contains(t, w.Body.String(), "'=formula,account-1,bill-1")
	require.Contains(t, w.Body.String(), "identity")
	require.Contains(t, w.Body.String(), "model,input_tokens,output_tokens")
	require.Contains(t, w.Body.String(), "model-a,123,0")
}

func TestDirectCostConcurrentStoresHaveOneCurrentIdentity(t *testing.T) {
	s := newStore(t)
	other := &Store{db: s.db}
	now := moment("2026-10-30T00:00:00Z")
	c := DirectCost{Supplier: "Xcloud", Account: "account-1", BillID: "bill-1", VerificationID: 656, ChannelID: 2, OccurredAt: moment("2026-10-29T00:00:00Z"), Amount: "0.000022", Currency: "XCLOUD_CREDIT", Evidence: "receipt", LinkEvidence: "verification link", Coverage: "unknown"}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			<-start
			_, errs[i] = store.Append(context.Background(), Submission{Kind: "direct_cost", DirectCost: &c, IdempotencyKey: fmt.Sprintf("parallel-%d", i)}, 1, now)
		}(i, store)
	}
	close(start)
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
	facts, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, facts, 1)
	var heads []directCostIdentity
	require.NoError(t, s.db.Find(&heads).Error)
	require.Len(t, heads, 1)
	require.Equal(t, facts[0].ID, heads[0].LatestID)
}

func TestDirectCostRevisionIsAppendOnlyAndStaleRevisionConflicts(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-30T00:00:00Z")
	c := DirectCost{Supplier: "Xcloud", Account: "account-1", BillID: "bill-1", VerificationID: 656, ChannelID: 2, OccurredAt: moment("2026-10-29T00:00:00Z"), Amount: "0.000022", Currency: "XCLOUD_CREDIT", Evidence: "receipt", LinkEvidence: "verification link", Coverage: "unknown"}
	in := Submission{Kind: "direct_cost", DirectCost: &c, IdempotencyKey: "direct-version-1"}
	a, err := s.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	c.Amount = "0.000020"
	in.PreviousID = a.ID
	in.IdempotencyKey = "direct-version-2"
	b, err := s.Append(context.Background(), in, 1, now.Add(time.Second))
	require.NoError(t, err)
	in.IdempotencyKey = "direct-version-stale"
	_, err = s.Append(context.Background(), in, 1, now.Add(2*time.Second))
	require.ErrorIs(t, err, ErrConflict)
	all, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, "0.000022", all[0].Payload.(DirectCost).Amount)
	require.Equal(t, a.ID, b.PreviousID)
	r, err := Calculate(fixture(), all, "2026-10", moment("2026-11-01T00:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, "0.00002000", r.DirectXcloudCreditTotal)
}

func TestMonthlyExclusionRequiresValidDistinctIdentities(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-26T00:00:00Z")
	c := &Cost{Month: "2026-09", ChannelID: 2, Supplier: "Xcloud", Gross: "2", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "invoice-1", ExcludedDirectBillRefs: []string{"not-an-identity"}}
	in := Submission{Kind: "cost", Ref: "2026-09:2", Cost: c, IdempotencyKey: "bad-exclusion-1"}
	_, err := s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, err, ErrInvalid)
	c.ExcludedDirectBillRefs = []string{strings.Repeat("a", 64), strings.Repeat("a", 64)}
	_, err = s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestLegacyDirectCostCannotBeImportedAgain(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-30T00:00:00Z")
	c := DirectCost{Supplier: "Xcloud", Account: "account-1", BillID: "legacy-bill", VerificationID: 656, ChannelID: 2, OccurredAt: moment("2026-10-29T00:00:00Z"), Amount: "1", Currency: "XCLOUD_CREDIT", Evidence: "receipt", LinkEvidence: "verification link", Coverage: "unknown"}
	raw, err := common.Marshal(c)
	require.NoError(t, err)
	legacy := factRecord{Kind: "direct_cost", Ref: directCostRef(&c), IdempotencyKey: "legacy-direct-key", Payload: string(raw), PayloadHash: "legacy", OperatorID: 1, CreatedAt: now}
	require.NoError(t, s.db.Create(&legacy).Error)
	_, err = s.Append(context.Background(), Submission{Kind: "direct_cost", DirectCost: &c, IdempotencyKey: "new-direct-key"}, 1, now)
	require.ErrorIs(t, err, ErrConflict)
	all, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, all, 1)
	revised, err := s.Append(context.Background(), Submission{Kind: "direct_cost", DirectCost: &c, PreviousID: legacy.ID, IdempotencyKey: "legacy-revision-key"}, 1, now)
	require.NoError(t, err)
	require.Equal(t, legacy.ID, revised.PreviousID)
}

func TestDirectCostRetainsModelTokenEvidenceAndRechargeRate(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-30T00:00:00Z")
	body := `{"kind":"direct_cost","direct_cost":{"supplier":"Xcloud","account":"account-1","bill_id":"model-bill","verification_id":656,"channel_id":2,"occurred_at":"2026-10-29T00:00:00Z","model":"model-a","input_tokens":123,"output_tokens":45,"amount":"2","currency":"XCLOUD_CREDIT","rate":"0.5","rate_evidence":"recharge receipt: 100 USDT for 200 credits","evidence":"supplier receipt","link_evidence":"verification link","coverage":"unknown"},"idempotency_key":"model-bill-key"}`
	var in Submission
	require.NoError(t, common.UnmarshalJsonStr(body, &in))
	_, err := s.Append(context.Background(), in, 1, now)
	require.NoError(t, err)
	all, err := s.List(context.Background())
	require.NoError(t, err)
	raw, err := common.Marshal(all[0].Payload)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"model":"model-a"`)
	require.Contains(t, string(raw), `"input_tokens":123`)
	require.Contains(t, string(raw), `"output_tokens":45`)
	r, err := Calculate(fixture(), all, "2026-10", moment("2026-11-02T00:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, "1.000000", r.DirectCosts[0].NetUSDT)
	require.False(t, r.Confirmed)
}

func TestDirectCostRejectsNegativeTokensAndUnprovenRate(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-30T00:00:00Z")
	body := `{"kind":"direct_cost","direct_cost":{"supplier":"Xcloud","account":"account-1","bill_id":"bill-negative","verification_id":656,"channel_id":2,"occurred_at":"2026-10-29T00:00:00Z","model":"model-a","input_tokens":-1,"amount":"2","currency":"XCLOUD_CREDIT","rate":"0.5","rate_evidence":"recharge receipt","evidence":"supplier receipt","link_evidence":"verification link","coverage":"unknown"},"idempotency_key":"negative-token-key"}`
	var in Submission
	require.NoError(t, common.UnmarshalJsonStr(body, &in))
	_, err := s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, err, ErrInvalid)
	positive := int64(1)
	in.DirectCost.InputTokens = &positive
	in.DirectCost.RateEvidence = ""
	_, err = s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, err, ErrInvalid, "a quoted discount alone is not recharge evidence")
}
