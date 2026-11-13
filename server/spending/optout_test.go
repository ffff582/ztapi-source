package spending

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestDisabledOptOutRetainsTelegramPreferenceWithoutBinding(t *testing.T) {
	for _, kind := range []string{"unbound", "unavailable", "temporary_read_failure"} {
		t.Run(kind, func(t *testing.T) {
			r, now := sourceFixture(t)
			store, err := NewStore(fixture(t))
			require.NoError(t, err)
			ctx := context.Background()
			_, err = store.SavePreferences(ctx, 1, Preferences{Enabled: true, Telegram: true, DailySpendU: "1"}, now)
			require.NoError(t, err)
			oldBinding := Binding{TelegramID: 123, Generation: "old"}
			snap := Snapshot{Overview: Overview{Complete: true, ConfirmedDailyU: str("2")}, Binding: &oldBinding}
			require.NoError(t, store.Evaluate(ctx, 1, snap, now))
			require.NoError(t, service.ConfigureZTAPISessionSigningKey(strings.Repeat("o", 32)))
			token, err := service.IssueZTAPIAccessToken(1, now)
			require.NoError(t, err)
			binding := &testBindings{err: ErrNotFound}
			available := true
			if kind == "unavailable" {
				available = false
			}
			if kind == "temporary_read_failure" {
				binding.err = ErrUnavailable
			}
			handler := NewHandler(r, store, HandlerConfig{PublicOrigin: "https://ztapi.vip", Now: func() time.Time { return now }, Bindings: binding, TelegramAvailable: available})
			put := func(body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest("PUT", "/api/spending/config", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("New-Api-User", "1")
				req.Header.Set("Origin", "https://ztapi.vip")
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				return w
			}
			require.Equal(t, 200, put(`{"enabled":false,"daily_spend_u":"1","hourly_spend_u":"","low_balance_u":"","input_tokens":0,"telegram":true,"version":1}`).Code)
			saved, err := store.Preferences(ctx, 1)
			require.NoError(t, err)
			require.False(t, saved.Enabled)
			require.True(t, saved.Telegram)
			require.EqualValues(t, 2, saved.Version)
			require.Zero(t, binding.calls)
			require.NoError(t, store.Evaluate(ctx, 1, snap, now.Add(time.Hour)))
			sender := &testSender{result: SendResult{Status: "sent"}}
			engine := &Engine{Store: store, Source: r, Bindings: binding, Sender: sender}
			require.NoError(t, engine.Deliver(ctx, now))
			require.Zero(t, sender.calls)
			rows, _, err := store.Alerts(ctx, 1, 20, 0)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "cancelled", rows[0].DeliveryStatus)
			require.Equal(t, 409, put(`{"enabled":true,"daily_spend_u":"1","hourly_spend_u":"","low_balance_u":"","input_tokens":0,"telegram":true,"version":2}`).Code)
			saved, err = store.Preferences(ctx, 1)
			require.NoError(t, err)
			require.False(t, saved.Enabled)
			require.EqualValues(t, 2, saved.Version)
		})
	}
}
