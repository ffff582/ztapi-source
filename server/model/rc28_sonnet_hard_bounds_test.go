package model

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRC28SonnetExactTrialLimits(t *testing.T) {
	const (
		modelID = "zt-claude-sonnet-5"
		channel = 41
		capU    = 3_000_000_000
	)
	for _, scenario := range []string{"dispatch-count", "wallet-with-pending"} {
		t.Run(scenario, func(t *testing.T) {
			db, input := setupZTAPISettlement(t)
			require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}, &ZTAPIWorkbenchPilotSpend{}, &ZTAPIRequestAttempt{}))
			capQuota, ok := ZTAPIWorkbenchPilotWalletQuota(capU)
			require.True(t, ok)
			u, ok := ZTAPIWorkbenchPilotWalletQuota(1_000_000_000)
			require.True(t, ok)
			require.Equal(t, 3*u, capQuota)
			balance := 4 * u
			policy := ZTAPIWorkbenchPilotPolicy{BatchID: uuid.NewString(), PolicySHA256: strings.Repeat("a", 64),
				ExpiresAt: time.Now().Add(29 * time.Minute).Unix(), MaxRequests: 14, BudgetQuota: capQuota,
				UserID: input.UserID, ChannelID: channel, PublicModel: modelID}
			input.PublicModel = modelID
			require.NoError(t, db.Model(&User{}).Where("id = ?", input.UserID).Update("quota", balance).Error)
			require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Updates(map[string]any{
				"model_limits_enabled": true, "model_limits": modelID, "expired_time": policy.ExpiresAt,
				"remain_quota": balance, "workbench_only": true,
			}).Error)
			reserve := func(task, suffix string, amount int64) (string, error) {
				leg := workbenchTestLeg(input, suffix, "main", amount)
				leg.Native = &ZTAPIWorkbenchNativeBounds{BodySHA256: strings.Repeat("b", 64), OutboundBytes: 2000, MaxOutput: 1024}
				_, err := BeginZTAPIWorkbenchBatch(task, 2*u, []ZTAPIWorkbenchLeg{leg}, policy)
				return leg.Settlement.OperationID, err
			}
			claim := func(task, operation string) error {
				_, err := ClaimZTAPIWorkbenchNativeAttempt(task, input.UserID, input.TokenID,
					operation, channel, strings.Repeat("c", 64), ZTAPIWorkbenchMessagesPath, policy)
				return err
			}
			if scenario == "dispatch-count" {
				const offered = 16
				var tasks, operations [offered]string
				for i := range tasks {
					tasks[i] = uuid.NewString()
					operation, err := reserve(tasks[i], fmt.Sprintf("-count-%d", i), 1)
					require.NoError(t, err)
					operations[i] = operation
				}
				succeeded, blocked := 0, 0
				for _, err := range workbenchConcurrentActions(offered, func(i int) error { return claim(tasks[i], operations[i]) }) {
					if err == nil {
						succeeded++
					} else {
						require.ErrorIs(t, err, ErrZTAPIWorkbenchPilotLimit)
						blocked++
					}
				}
				require.Equal(t, 14, succeeded)
				require.Equal(t, 2, blocked)
				var attempts int64
				require.NoError(t, db.Model(&ZTAPIRequestAttempt{}).Count(&attempts).Error)
				require.EqualValues(t, 14, attempts)
				t.Logf("Sonnet synthetic dispatches: accepted=%d blocked=%d persisted=%d", succeeded, blocked, attempts)
				return
			}

			firstTask := uuid.NewString()
			first, err := reserve(firstTask, "-settled", u)
			require.NoError(t, err)
			require.NoError(t, claim(firstTask, first))
			_, err = FinalizeZTAPIRequestSettlement(first, u, fmt.Sprintf(`{"input":%d}`, u),
				fmt.Sprintf(`[{"dimension":"input_tokens","units":"%d","unit_quota":"1","charged_quota":%d}]`, u, u))
			require.NoError(t, err)
			pending, err := reserve(uuid.NewString(), "-pending", u)
			require.NoError(t, err)
			_, err = reserve(uuid.NewString(), "-at-limit", u)
			require.NoError(t, err)
			_, err = reserve(uuid.NewString(), "-one-quantum-over", 1)
			require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
			assertZTAPISettlementBalances(t, db, input, int(u), int(u))
			_, err = ReleaseZTAPIRequestSettlement(pending)
			require.NoError(t, err)
			_, err = reserve(uuid.NewString(), "-release-plus-one", u+1)
			require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
			_, err = reserve(uuid.NewString(), "-release-exact", u)
			require.NoError(t, err)
			assertZTAPISettlementBalances(t, db, input, int(u), int(u))
			t.Logf("Sonnet synthetic wallet: cap=%d quota, settled=%d, pending=%d, plus one refused", capQuota, u, 2*u)
		})
	}
}
