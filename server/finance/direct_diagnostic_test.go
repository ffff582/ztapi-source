package finance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func diagnosticExample() DirectCost {
	input, output, cache := int64(4400), int64(5), int64(3225)
	return DirectCost{
		Supplier: "Xcloud", Account: "reviewed account", BillID: "supplier-bill-a",
		ChannelID: 7, OccurredAt: moment("2026-09-16T10:00:00+08:00"),
		Model: "gpt-5.6-terra", InputTokens: &input, OutputTokens: &output,
		InputTokenScope: "inclusive", CacheRead: &cache,
		Amount: "0.011076", Currency: "XCLOUD_CREDIT", Rate: "0.5",
		RateEvidence: "confirmed recharge conversion", Evidence: "supplier receipt",
		LinkEvidence: "in-house diagnostic capture and supplier usage ledger", Coverage: "unknown",
		Diagnostic: &DirectCostDiagnostic{
			RequestID: "example-probe-request-id", EvidenceSHA256: strings.Repeat("a", 64),
		},
	}
}

func TestStandaloneDiagnosticRequiresExactApprovalAndSupplier(t *testing.T) {
	cost := diagnosticExample()
	s := Snapshot{Channels: []Channel{{ID: 7, BaseURL: "https://www.xcloudtoken.com"}}}
	now := moment("2026-11-16T00:00:00Z")
	require.NoError(t, validateDirectCost(&cost, now))
	digest := directDiagnosticDigest(&cost)
	require.False(t, directDiagnosticApproved(s, &cost, ""))
	require.False(t, directDiagnosticApproved(Snapshot{}, &cost, digest))
	require.True(t, directDiagnosticApproved(s, &cost, digest))
	for _, field := range []func(*DirectCost){
		func(c *DirectCost) { c.Amount = "0.011077" },
		func(c *DirectCost) { c.BillID = "other-bill" },
		func(c *DirectCost) { c.Model = "gpt-5.6-sol" },
		func(c *DirectCost) { c.Diagnostic.RequestID = "other-probe-request-id" },
	} {
		changed := diagnosticExample()
		field(&changed)
		require.False(t, directDiagnosticApproved(s, &changed, digest))
	}
	cost.VerificationID = 1
	require.ErrorIs(t, validateDirectCost(&cost, now), ErrInvalid)
}

func TestStandaloneDiagnosticRequiresProofAndStaysOutOfClosedProfit(t *testing.T) {
	cost := diagnosticExample()
	now := moment("2026-11-16T00:00:00Z")
	s := newStore(t)
	ctx := context.Background()
	in := Submission{Kind: "direct_cost", DirectCost: &cost, IdempotencyKey: "standalone-conflict-test"}
	t.Setenv(directDiagnosticApprovalEnv, "")
	_, err := s.Append(ctx, in, 1, now)
	require.ErrorIs(t, err, ErrInvalid)
	t.Setenv(directDiagnosticApprovalEnv, directDiagnosticDigest(&cost))
	_, err = s.Append(ctx, in, 1, now)
	require.NoError(t, err)
	in.IdempotencyKey = "standalone-duplicate-test"
	_, err = s.Append(ctx, in, 1, now)
	require.ErrorIs(t, err, ErrConflict)
	facts, err := s.List(ctx)
	require.NoError(t, err)
	require.Len(t, facts, 1)
	require.Equal(t, cost.Diagnostic, facts[0].Payload.(DirectCost).Diagnostic)
	managed := cost
	managed.Diagnostic, managed.VerificationID = nil, 500
	in.DirectCost, in.PreviousID = &managed, facts[0].ID
	_, err = s.Append(ctx, in, 1, now)
	require.ErrorIs(t, err, ErrConflict)
	t.Setenv(directDiagnosticApprovalEnv, "")
	r, err := Calculate(fixture(), facts, "2026-09", now)
	require.NoError(t, err)
	require.Equal(t, "0.005538", r.DirectConfirmedTotal)
	require.Nil(t, r.GrossProfit)
	require.Contains(t, r.Issues, Issue{Code: "direct_cost_coverage_missing", Ref: directCostRef(&cost)})
	cost.Diagnostic = nil
	require.ErrorIs(t, validateDirectCost(&cost, now), ErrInvalid)
}

func TestStandaloneDiagnosticIntakeRequiresRootAndExactEvidence(t *testing.T) {
	_, token := api(t, common.RoleRootUser)
	cost := diagnosticExample()
	submission := Submission{Kind: "direct_cost", DirectCost: &cost, IdempotencyKey: "standalone-http-test"}
	body, err := common.Marshal(submission)
	require.NoError(t, err)
	source := &testSource{Snapshot{Channels: []Channel{{ID: 7, BaseURL: "https://www.xcloudtoken.com"}}}, common.RoleFinanceUser}
	h := NewHandler(source, newStore(t), "https://admin.example.test", func() time.Time {
		return moment("2026-10-26T00:01:00Z")
	})
	t.Setenv(directDiagnosticApprovalEnv, directDiagnosticDigest(&cost))
	admit := func(payload string) int {
		return request(h, token, "POST", prefix+"facts", payload, "https://admin.example.test").Code
	}
	require.Equal(t, 403, admit(string(body)))
	source.role = common.RoleRootUser
	require.Equal(t, 400, admit(strings.Replace(string(body), "0.011076", "0.011077", 1)))
	require.Equal(t, 201, admit(string(body)))
	require.Equal(t, 201, admit(string(body)))
}

func TestStandaloneDiagnosticCanOnlyAmendMonthlyCoverageAfterApprovalIsRemoved(t *testing.T) {
	cost := diagnosticExample()
	now := moment("2026-11-16T00:00:00Z")
	ctx := context.Background()
	store := newStore(t)
	t.Setenv(directDiagnosticApprovalEnv, directDiagnosticDigest(&cost))
	in := Submission{Kind: "direct_cost", DirectCost: &cost, IdempotencyKey: "initial-standalone"}
	accepted, err := store.Append(ctx, in, 1, now)
	require.NoError(t, err)
	t.Setenv(directDiagnosticApprovalEnv, "")
	reportFacts := facts()
	monthly, err := store.Append(ctx, Submission{
		Kind: "cost", Ref: "2026-09:7", IdempotencyKey: "standalone-month-invoice",
		Cost: &Cost{Month: "2026-09", ChannelID: 7, Supplier: "Xcloud",
			Gross: "1", Refund: "0", Currency: "USDT", Rate: "1", Complete: true, Evidence: "reviewed September invoice"},
	}, 1, now)
	require.NoError(t, err)
	reportFacts = append(reportFacts, Fact{ID: 10, Kind: "cost", Ref: monthly.Ref, Payload: monthly.Payload})
	reportFacts = append(reportFacts, Fact{ID: 11, Kind: accepted.Kind, Ref: accepted.Ref, Payload: accepted.Payload})
	before := calculate(t, fixture(), reportFacts)
	require.False(t, before.Confirmed)
	cost.Coverage, cost.CoverageEvidence, cost.MonthCostFactID = "included", "reviewed September invoice", monthly.ID
	in.IdempotencyKey, in.PreviousID = "amend-standalone-coverage", accepted.ID
	changed := cost
	changed.Amount = "0.011077"
	bad := in
	bad.DirectCost = &changed
	_, err = store.Append(ctx, bad, 1, now)
	require.ErrorIs(t, err, ErrConflict)
	amended, err := store.Append(ctx, in, 1, now)
	require.NoError(t, err)
	require.NotEqual(t, accepted.ID, amended.ID)
	require.Equal(t, accepted.ID, amended.PreviousID)
	rebased := amended.Payload.(DirectCost)
	rebased.MonthCostFactID = 10
	reportFacts = append(reportFacts, Fact{ID: 12, Kind: amended.Kind, Ref: amended.Ref, Payload: rebased})
	after := calculate(t, fixture(), reportFacts)
	require.True(t, after.Confirmed)
	require.Equal(t, "16.000000", after.RecordedCost)
	require.Equal(t, "0.005538", after.DirectConfirmedTotal)
}
