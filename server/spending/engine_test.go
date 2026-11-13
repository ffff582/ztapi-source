package spending

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func str(s string) *string { return &s }
func TestPreferencesCASAndOwnerDefaults(t *testing.T) {
	ctx := context.Background()
	s, err := NewStore(fixture(t))
	require.NoError(t, err)
	p, err := s.Preferences(ctx, 1)
	require.NoError(t, err)
	require.False(t, p.Enabled)
	require.Zero(t, p.Version)
	p.Enabled = true
	p.DailySpendU = "0.1"
	p, err = s.SavePreferences(ctx, 1, p, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, p.Version)
	p.Version = 0
	_, err = s.SavePreferences(ctx, 1, p, time.Now())
	require.ErrorIs(t, err, ErrConflict)
	other, err := s.Preferences(ctx, 2)
	require.NoError(t, err)
	require.False(t, other.Enabled)
	for _, bad := range []string{"-1", "1e2", "NaN", "0.0000001", "1000001", " 1"} {
		p.Version = 1
		p.DailySpendU = bad
		_, err = s.SavePreferences(ctx, 1, p, time.Now())
		require.ErrorIs(t, err, ErrInvalid)
	}
}
func TestEngineOffDedupRestartCooldownRearmAndIncomplete(t *testing.T) {
	ctx := context.Background()
	db := fixture(t)
	store, err := NewStore(db)
	require.NoError(t, err)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, Beijing)
	snap := Snapshot{Overview: Overview{Complete: true, ConfirmedDailyU: str("2"), ConfirmedHourlyU: str("2"), AvailableBalanceU: str("0.1")}, Inputs: []Input{{RequestID: "req", Tokens: 100, At: now}}}
	require.NoError(t, store.Evaluate(ctx, 1, snap, now))
	rows, _, err := store.Alerts(ctx, 1, 100, 0)
	require.NoError(t, err)
	require.Empty(t, rows)
	p, err := store.SavePreferences(ctx, 1, Preferences{Enabled: true, DailySpendU: "1", HourlySpendU: "1", LowBalanceU: "1", InputTokens: 50}, now)
	require.NoError(t, err)
	require.NoError(t, store.Evaluate(ctx, 1, snap, now))
	require.NoError(t, store.Evaluate(ctx, 1, snap, now))
	restarted, err := NewStore(db)
	require.NoError(t, err)
	require.NoError(t, restarted.Evaluate(ctx, 1, snap, now.Add(59*time.Minute)))
	rows, unread, err := store.Alerts(ctx, 1, 100, 0)
	require.NoError(t, err)
	require.Len(t, rows, 4)
	require.EqualValues(t, 4, unread)
	require.NoError(t, restarted.Evaluate(ctx, 1, snap, now.Add(time.Hour)))
	rows, _, _ = store.Alerts(ctx, 1, 100, 0)
	require.Len(t, rows, 5)
	require.NoError(t, store.ReadAlert(ctx, 1, rows[0].ID, now))
	require.NoError(t, store.ReadAlert(ctx, 1, rows[0].ID, now))
	require.ErrorIs(t, store.ReadAlert(ctx, 2, rows[0].ID, now), ErrNotFound)
	p.Enabled = false
	_, err = store.SavePreferences(ctx, 1, p, now)
	require.NoError(t, err)
	require.NoError(t, store.Evaluate(ctx, 1, snap, now.Add(48*time.Hour)))
	after, _, _ := store.Alerts(ctx, 1, 100, 0)
	require.Len(t, after, 5)
}
func TestIncompleteTotalsNeverTriggerSpendingRules(t *testing.T) {
	s, err := NewStore(fixture(t))
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now()
	_, err = s.SavePreferences(ctx, 1, Preferences{Enabled: true, DailySpendU: "1"}, now)
	require.NoError(t, err)
	require.NoError(t, s.Evaluate(ctx, 1, Snapshot{Overview: Overview{Complete: false, ConfirmedDailyU: str("100"), AvailableBalanceU: str("0")}}, now))
	rows, _, err := s.Alerts(ctx, 1, 20, 0)
	require.NoError(t, err)
	require.Empty(t, rows)
}
