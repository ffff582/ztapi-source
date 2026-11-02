package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/spending"
	"github.com/stretchr/testify/require"
)

func TestSourceColumnsExportIsIndependentOfRuntimeConfiguration(t *testing.T) {
	t.Setenv("SPENDING_READ_DSN", "")
	raw, err := sourceColumnsJSON()
	require.NoError(t, err)
	var columns map[string][]string
	require.NoError(t, common.Unmarshal(raw, &columns))
	require.Equal(t, spending.RequiredSourceColumns(), columns)
	require.Contains(t, columns["ztapi_request_settlements"], "charge_dimensions_json")
	require.NotContains(t, columns["users"], "password")
	require.NotContains(t, columns, "tokens")
}

type auditFixture struct{}

func (auditFixture) Load(_ context.Context, owner int, now time.Time) (spending.Snapshot, error) {
	text := func(v string) *string { return &v }
	s := spending.Snapshot{Overview: spending.Overview{AsOf: now, Timezone: "Asia/Shanghai", Complete: true, ConfirmedDailyU: text("0.123456"), ConfirmedHourlyU: text("0"), ConfirmedRefundsDailyU: text("0"), AvailableBalanceU: text("-0.1")}}
	switch owner {
	case 2:
		s.Overview.Complete = false
		s.Overview.ConfirmedDailyU = nil
		s.Overview.IncompleteRequests = 1
	case 3:
		return spending.Snapshot{}, errors.New("private database detail must never be output")
	case 4:
		s.Overview.ConfirmedDailyU = nil
	}
	return s, nil
}

func TestAuditSeparatesCompleteIncompleteUnavailableWithoutCustomerAmounts(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	report := auditSnapshots(context.Background(), auditFixture{}, []int{1, 2, 3, 4}, now)
	require.Equal(t, 4, report.CheckedUsers)
	require.Equal(t, 1, report.CompleteUsers)
	require.Equal(t, 1, report.IncompleteUsers)
	require.Equal(t, 2, report.UnavailableUsers)
	require.Equal(t, 1, report.IncompleteRequests)
	require.Equal(t, 0, report.PaidModelCalls)
	require.Equal(t, now, report.AsOf)
}

func TestAuditDoesNotContinueAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := auditSnapshots(ctx, auditFixture{}, []int{1, 2}, time.Now())
	require.Zero(t, report.CheckedUsers)
	require.True(t, report.Interrupted)
}

func TestAuditRequiresOnlySelectPrivileges(t *testing.T) {
	require.True(t, selectOnlyGrants([]string{"GRANT USAGE ON *.* TO `reader`@`%`", "GRANT SELECT (`id`, `quota`) ON `ztapi`.`users` TO `reader`@`%`"}))
	for _, grants := range [][]string{
		nil,
		{"GRANT ALL PRIVILEGES ON *.* TO `reader`@`%`"},
		{"GRANT SELECT, UPDATE ON `ztapi`.* TO `reader`@`%`"},
		{"GRANT SELECT ON `ztapi`.* TO `reader`@`%` WITH GRANT OPTION"},
		{"GRANT USAGE ON *.* TO `reader`@`%`"},
	} {
		require.False(t, selectOnlyGrants(grants))
	}
}
