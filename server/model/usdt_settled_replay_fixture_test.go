package model

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestUSDTSettledReplaySyntheticFixture(t *testing.T) {
	fixture := syntheticUSDTSettledReplayFixture()
	require.Equal(t, fixture, syntheticUSDTSettledReplayFixture(), "fixture must be deterministic")
	require.Equal(t, "usdt-replay-fixture", fixture.User.Username)
	for _, value := range []string{fixture.User.Email, fixture.User.GitHubId, fixture.User.DiscordId,
		fixture.User.OidcId, fixture.User.WeChatId, fixture.User.TelegramId, fixture.User.LinuxDOId,
		fixture.User.StripeCustomer, fixture.User.Remark, fixture.User.Setting} {
		require.Empty(t, value, "fixture must not contain customer profile data")
	}
	require.Nil(t, fixture.User.AccessToken)
	for _, value := range []string{*fixture.Order.TxID, *fixture.Order.TxFrom,
		fixture.Order.ReceivingAddress, fixture.Order.ContractAddress, fixture.Order.TradeNo} {
		require.True(t, strings.HasPrefix(value, "synthetic-"))
	}
	require.Equal(t, USDTTopUpStatusSettled, fixture.Order.Status)
	require.Equal(t, common.TopUpStatusSuccess, fixture.TopUp.Status)
	require.Equal(t, fixture.User.Id, fixture.Order.UserID)
	require.Equal(t, fixture.User.Id, fixture.TopUp.UserId)
	require.Equal(t, fixture.User.Id, fixture.Ledger.UserID)
	require.Equal(t, fixture.TopUp.Id, fixture.Order.TopUpID)
	require.Equal(t, fixture.Order.TradeNo, fixture.TopUp.TradeNo)
	require.Equal(t, fixture.Order.TradeNo, fixture.Ledger.RequestID)
	require.Equal(t, "usdt-trc20:"+*fixture.Order.TxID, fixture.Ledger.IdempotencyKey)
	amount, err := fixture.Order.ValidateExactPayAmount(1)
	require.NoError(t, err)
	require.Equal(t, fixture.Order.PayAmountMicros, amount)
	require.Equal(t, fixture.Ledger.BalanceBefore+fixture.Ledger.Delta, fixture.Ledger.BalanceAfter)
	require.Equal(t, fixture.Ledger.BalanceAfter-100_000, int64(fixture.User.Quota))
	transfer := TRC20Transfer{TxID: *fixture.Order.TxID, From: *fixture.Order.TxFrom,
		To: fixture.Order.ReceivingAddress, ContractAddress: fixture.Order.ContractAddress,
		AmountMicros: amount, BlockTimestampMS: *fixture.Order.BlockTimestampMS}
	require.NoError(t, validateUSDTSettlementTransfer(transfer))
	require.True(t, settlementEvidenceMatches(&fixture.Order, transfer))
}

func TestUSDTSettledReplayConfigurationGate(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, required, dsn, output string
		fails                       bool
	}{
		{name: "local unconfigured skips", output: "--- SKIP: TestUSDTAlreadySettledReplayMySQL"},
		{name: "mandatory unconfigured fails", required: "true", fails: true, output: "local replay MySQL DSN is required"},
		{name: "remote host rejected before connection", required: "true", fails: true,
			dsn: "fixture:fixture@tcp(192.0.2.1:3306)/ztapi_usdt_replay_test", output: "only the loopback test container is permitted"},
		{name: "other database rejected before connection", required: "true", fails: true,
			dsn: "fixture:fixture@tcp(127.0.0.1:1)/unrelated_test", output: "ztapi_usdt_replay_test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestUSDTAlreadySettledReplayMySQL$", "-test.v", "-test.timeout=10s")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if !strings.EqualFold(key, "ZTAPI_USDT_REPLAY_MYSQL_TEST_DSN") &&
					!strings.EqualFold(key, "ZTAPI_REQUIRE_USDT_REPLAY_TEST") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "ZTAPI_USDT_REPLAY_MYSQL_TEST_DSN="+tc.dsn, "ZTAPI_REQUIRE_USDT_REPLAY_TEST="+tc.required)
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "configuration checks must finish without database access")
			if tc.fails {
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError, "%s", output)
				require.Equal(t, 1, exitError.ExitCode(), "%s", output)
				require.NotContains(t, string(output), "--- SKIP:")
			} else {
				require.NoError(t, err, "%s", output)
			}
			require.Contains(t, string(output), tc.output)
		})
	}
}
