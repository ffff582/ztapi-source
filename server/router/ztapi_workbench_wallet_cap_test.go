package router

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func verifyWorkbenchFablePilotUsesZTAPIWalletLimit(t *testing.T) {
	config := workbenchPilotConfig{DispatchEnabled: true}
	config.UserIDs = []int{107}
	config.Models = []WorkbenchAcceptanceModel{{Model: "zt-claude-fable-5", ChannelID: 4}}
	for _, test := range []struct {
		name, basis string
		budget      int64
		accepted    bool
	}{
		{"exactly 3 wallet U", "ztapi_wallet", 3_000_000_000, true},
		{"reject over 3 U", "ztapi_wallet", 3_000_000_001, false},
		{"reject supplier basis", "supplier", 2_550_000_000, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"batch_id":"e97da904-d7fa-4537-9c4c-b559cddd04f2","expires_at":%d,"max_requests":14,"budget_nano_u":%d,"budget_basis":%q}`,
				time.Now().Add(10*time.Minute).Unix(), test.budget, test.basis)
			require.NoError(t, json.Unmarshal([]byte(raw), &config.FableCap))
			policy, err := config.fablePolicy()
			if !test.accepted {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 14, policy.MaxRequests)
			require.EqualValues(t, int64(3*common.QuotaPerUnit), policy.BudgetQuota)
		})
	}
}
