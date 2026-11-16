package support

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/support/bridgeapi"
	"github.com/stretchr/testify/require"
)

func TestTelegramBridgeBindingGenerationScopesEveryPrivateResult(t *testing.T) {
	b, _, source, _ := bindingHarness(t)
	ctx := context.Background()
	read := func(in bridgeapi.Request) string {
		t.Helper()
		out, err := b.Execute(ctx, in)
		require.NoError(t, err)
		raw, err := json.Marshal(out)
		require.NoError(t, err)
		require.NotContains(t, string(raw), string(b.cfg.Secret))
		require.NotContains(t, string(raw), hex.EncodeToString(b.cfg.Secret))
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &fields))
		require.NotContains(t, fields, "user_id")
		require.NotContains(t, fields, "account_id")
		require.NotContains(t, fields, "secret")
		require.NotContains(t, fields, "token")
		var generation string
		if value, ok := fields["binding_generation"]; ok {
			require.NoError(t, json.Unmarshal(value, &generation))
		}
		if out.Bound {
			require.Regexp(t, `^[a-f0-9]{32}$`, generation)
		} else {
			require.Empty(t, generation)
		}
		return generation
	}

	require.Empty(t, read(bridgeapi.Request{Action: "binding_status", TelegramID: 42}))
	bindTestUser(t, b, 8, 42, "generation-account-a")
	first := read(bridgeapi.Request{Action: "binding_status", TelegramID: 42})
	for _, in := range []bridgeapi.Request{
		{Action: "recent_requests", TelegramID: 42},
		{Action: "diagnose", TelegramID: 42, RequestID: "own-request"},
		{Action: "create_ticket", TelegramID: 42, Content: "connection failure", IdempotencyKey: "generation-ticket"},
	} {
		require.Equal(t, first, read(in))
	}
	_, err := b.UpdateBinding(ctx, 8, BindingInput{Action: "unbind", IdempotencyKey: "generation-unbind-a"})
	require.NoError(t, err)
	require.Empty(t, read(bridgeapi.Request{Action: "binding_status", TelegramID: 42}))
	bindTestUser(t, b, 9, 42, "generation-account-b")
	require.NotEqual(t, first, read(bridgeapi.Request{Action: "binding_status", TelegramID: 42}))
	source.status = 2
	require.Empty(t, read(bridgeapi.Request{Action: "binding_status", TelegramID: 42}))
}
