package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPICustomerFailureAlertsImmediatelyAndCoalesces(t *testing.T) {
	s, c, now := healthFixture(t)
	for i := 0; i < 3; i++ {
		ticket := healthAdmit(t, s, c, fmt.Sprintf("customer-alert-%d", i))
		outcome := healthRouteOutcome(t, "failure", "upstream_http_error", "chat", 2)
		healthAdmitOutcome(t, s, ticket, outcome)
		require.NoError(t, s.RecordOutcome(context.Background(), ticket, outcome))
		if i == 0 {
			jobs, err := s.ClaimOutbox(context.Background(), "customer_alert", 10, 60)
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			require.NoError(t, s.FinishOutboxWithReceipt(context.Background(), jobs[0].ID, jobs[0].LeaseToken, "", 0, `{"ok":true,"result":{"message_id":42,"date":2000000000}}`))
		}
		*now += 30
	}
	var jobs []ZTAPIHealthOutbox
	require.NoError(t, s.DB.Where("kind = ?", "customer_alert").Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.Equal(t, "done", jobs[0].Status)
	var row struct{ Occurrences int64 }
	require.NoError(t, s.DB.Table("ztapi_health_outbox").Select("occurrences").Where("id = ?", jobs[0].ID).Scan(&row).Error)
	require.EqualValues(t, 3, row.Occurrences)
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.False(t, state.Open)
	*now += 600
	ticket := healthAdmit(t, s, c, "customer-alert-later")
	outcome := healthRouteOutcome(t, "failure", "empty_output", "chat", 2)
	healthAdmitOutcome(t, s, ticket, outcome)
	require.NoError(t, s.RecordOutcome(context.Background(), ticket, outcome))
	require.NoError(t, s.DB.Where("kind = ?", "customer_alert").Find(&jobs).Error)
	require.Len(t, jobs, 2)
}

func TestZTAPICustomerAlertSeparatesUsersAndChannels(t *testing.T) {
	s, c, _ := healthFixture(t)
	for i, pair := range [][2]int{{1, 2}, {2, 2}, {1, 3}} {
		ticket, err := s.AdmitRequest(context.Background(), c.PublicNameValue(), fmt.Sprintf("customer-separate-%d", i), "req", pair[0], false)
		require.NoError(t, err)
		outcome := healthRouteOutcome(t, "failure", "upstream_transport_error", "chat", pair[1])
		healthAdmitOutcome(t, s, ticket, outcome)
		require.NoError(t, s.RecordOutcome(context.Background(), ticket, outcome))
	}
	var count int64
	require.NoError(t, s.DB.Model(&ZTAPIHealthOutbox{}).Where("kind = ?", "customer_alert").Count(&count).Error)
	require.EqualValues(t, 3, count)
}

func TestZTAPICustomerAlertExcludesProbeAndClientMistakes(t *testing.T) {
	for _, reason := range []string{"invalid_input", "safety_refusal", "client_cancelled", "customer_timeout", "length_without_visible_output"} {
		t.Run(reason, func(t *testing.T) {
			s, c, _ := healthFixture(t)
			ticket := healthAdmit(t, s, c, "excluded-alert")
			outcome := healthRouteOutcome(t, "failure", reason, "chat", 2)
			healthAdmitOutcome(t, s, ticket, outcome)
			require.NoError(t, s.RecordOutcome(context.Background(), ticket, outcome))
			var count int64
			require.NoError(t, s.DB.Model(&ZTAPIHealthOutbox{}).Where("kind = ?", "customer_alert").Count(&count).Error)
			require.Zero(t, count)
		})
	}
	s, c, _ := healthFixture(t)
	ticket := healthAdmit(t, s, c, "probe-no-customer-alert")
	healthMarkDiagnostic(t, s, ticket)
	healthRecord(t, s, ticket, "failure")
	var count int64
	require.NoError(t, s.DB.Model(&ZTAPIHealthOutbox{}).Where("kind = ?", "customer_alert").Count(&count).Error)
	require.Zero(t, count)
}
