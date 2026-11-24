package router

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestZTAPISonnetPilotOnlyRoutesAndCaps(t *testing.T) {
	initializeRouterRelayTestColumnNames(t)
	db := setupZTAPIAuthRouterTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.ZTAPIWorkbenchTask{},
		&model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{},
		&model.BalanceLedger{}, &model.ZTAPIWorkbenchPilotSpend{}))

	const userID = 73001
	seed := func() workbenchPilotConfig {
		return workbenchPilotConfig{
			WorkbenchAcceptanceConfig: WorkbenchAcceptanceConfig{
				TaskLimitQuota: 500000, UserIDs: []int{userID},
				Models: []WorkbenchAcceptanceModel{{Model: "zt-claude-sonnet-5", Version: 1,
					SnapshotID: 1, ChannelID: 73002, UpstreamOrigin: "http://127.0.0.1:1", CredentialSHA256: strings.Repeat("a", 64)}},
			},
			DispatchEnabled: true,
			WalletCap: &workbenchFablePilotCap{BatchID: uuid.NewString(), ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
				BudgetBasis: "ztapi_wallet", MaxRequests: 14, BudgetNanoU: 3000000000, UpstreamProtocol: "chat-completions"},
		}
	}
	write := func(t *testing.T, config workbenchPilotConfig) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "pilot.json")
		body, err := common.Marshal(config)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, body, 0600))
		return path
	}
	for _, tc := range []struct {
		name string
		edit func(*workbenchPilotConfig)
	}{
		{"missing wallet cap", func(c *workbenchPilotConfig) { c.WalletCap = nil }},
		{"wrong account", func(c *workbenchPilotConfig) { c.UserIDs[0]++ }},
		{"second account", func(c *workbenchPilotConfig) { c.UserIDs = append(c.UserIDs, userID+1) }},
		{"wrong model", func(c *workbenchPilotConfig) { c.Models[0].Model = "zt-claude-fable-5" }},
		{"one quantum over", func(c *workbenchPilotConfig) { c.WalletCap.BudgetNanoU++ }},
		{"unverified upstream credential", func(c *workbenchPilotConfig) { c.Models[0].CredentialSHA256 = strings.Repeat("0", 64) }},
		{"extra dispatch", func(c *workbenchPilotConfig) { c.WalletCap.MaxRequests++ }},
		{"expired", func(c *workbenchPilotConfig) { c.WalletCap.ExpiresAt = time.Now().Add(-time.Minute).Unix() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := seed()
			tc.edit(&config)
			engine := gin.New()
			require.Error(t, MountZTAPISonnetPilot(engine, write(t, config), userID))
			require.Empty(t, engine.Routes())
		})
	}

	engine := gin.New()
	require.NoError(t, MountZTAPISonnetPilot(engine, write(t, seed()), userID))
	rec := performZTAPIRouterRequest(engine, http.MethodGet, "/.well-known/ztapi-workbench", "", "", "127.0.0.1:1100")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Contains(t, rec.Body.String(), `"dispatch":"enabled"`)
	rec = performZTAPIRouterRequest(engine, http.MethodGet, "/api/workbench/self", "", "", "127.0.0.1:1100")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	for _, p := range []string{"/api/status", "/api/auth/login", "/api/admin/users", "/v1/chat/completions", "/__workbench_budget/responses"} {
		for _, route := range engine.Routes() {
			require.NotEqual(t, p, route.Path)
		}
	}
}
