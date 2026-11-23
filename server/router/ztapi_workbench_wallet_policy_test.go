package router

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchWalletPolicyForApprovedNonFableModel(t *testing.T) {
	cap := &workbenchFablePilotCap{BatchID: "10101010-2020-4030-8040-505050505050", ExpiresAt: time.Now().Add(20 * time.Minute).Unix(),
		BudgetBasis: "ztapi_wallet", MaxRequests: 14, BudgetNanoU: 3_000_000_000}
	cfg := workbenchPilotConfig{DispatchEnabled: true, WalletCap: cap,
		WorkbenchAcceptanceConfig: WorkbenchAcceptanceConfig{UserIDs: []int{199},
			Models: []WorkbenchAcceptanceModel{{Model: "zt-claude-sonnet-5", Version: 3, SnapshotID: 32, ChannelID: 2}}}}
	policy, err := cfg.fablePolicy()
	require.NoError(t, err)
	require.True(t, policy.ValidAt(time.Now()))
	require.Equal(t, 199, policy.UserID)
	require.Equal(t, 2, policy.ChannelID)
	require.Equal(t, 14, policy.MaxRequests)
	require.Equal(t, "/v1/messages", policy.ExpectedUpstreamPath())
	bridge := cfg
	bridgeCap := *cap
	bridgeCap.UpstreamProtocol = "chat-completions"
	bridge.WalletCap = &bridgeCap
	bridgePolicy, err := bridge.fablePolicy()
	require.NoError(t, err)
	require.Equal(t, "/v1/chat/completions", bridgePolicy.ExpectedUpstreamPath())
	require.NotEqual(t, policy.PolicySHA256, bridgePolicy.PolicySHA256)
	other := cfg
	other.Models = []WorkbenchAcceptanceModel{{Model: "zt-claude-opus-5", Version: 3, SnapshotID: 32, ChannelID: 2}}
	otherPolicy, err := other.fablePolicy()
	require.NoError(t, err)
	require.NotEqual(t, policy.PolicySHA256, otherPolicy.PolicySHA256)

	for name, change := range map[string]func(*workbenchPilotConfig){
		"two_wallets": func(c *workbenchPilotConfig) { c.FableCap = c.WalletCap },
		"two_owners":  func(c *workbenchPilotConfig) { c.UserIDs = append(c.UserIDs, 200) },
		"two_models":  func(c *workbenchPilotConfig) { c.Models = append(c.Models, other.Models[0]) },
		"not_claude": func(c *workbenchPilotConfig) {
			c.Models = []WorkbenchAcceptanceModel{{Model: "zt-gpt-6.1-sol", Version: 3, SnapshotID: 32, ChannelID: 2}}
		},
		"legacy_non_fable":   func(c *workbenchPilotConfig) { c.FableCap, c.WalletCap = c.WalletCap, nil },
		"not_dispatched":     func(c *workbenchPilotConfig) { c.DispatchEnabled = false },
		"over_wallet_limit":  func(c *workbenchPilotConfig) { c.WalletCap.BudgetNanoU++ },
		"over_request_limit": func(c *workbenchPilotConfig) { c.WalletCap.MaxRequests++ },
		"expired":            func(c *workbenchPilotConfig) { c.WalletCap.ExpiresAt = time.Now().Unix() },
		"unknown_protocol":   func(c *workbenchPilotConfig) { c.WalletCap.UpstreamProtocol = "automatic" },
	} {
		t.Run(name, func(t *testing.T) {
			broken, ownCap := cfg, *cap
			broken.WalletCap = &ownCap
			change(&broken)
			_, err := broken.fablePolicy()
			require.Error(t, err)
		})
	}
}
