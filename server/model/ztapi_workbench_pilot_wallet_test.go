package model

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchPilotWalletCountsPendingAndRefusesOverDebit(t *testing.T) {
	t.Run("legacy-fable", func(t *testing.T) { verifyWorkbenchPilotWallet(t, "zt-claude-fable-5", "") })
	t.Run("pinned-sonnet", func(t *testing.T) { verifyWorkbenchPilotWallet(t, "zt-claude-sonnet-5", "zt-claude-sonnet-5") })
}

func verifyWorkbenchPilotWallet(t *testing.T, publicModel, pin string) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}, &ZTAPIWorkbenchPilotSpend{}, &ZTAPIRequestAttempt{}))
	input.PublicModel = publicModel
	policy := ZTAPIWorkbenchPilotPolicy{BatchID: uuid.NewString(), PolicySHA256: strings.Repeat("a", 64),
		ExpiresAt: time.Now().Add(10 * time.Minute).Unix(), MaxRequests: 14, BudgetQuota: 800,
		UserID: input.UserID, ChannelID: 4, PublicModel: pin}
	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Updates(map[string]any{
		"model_limits_enabled": true, "model_limits": input.PublicModel, "expired_time": policy.ExpiresAt,
	}).Error)
	makeLeg := func(suffix string, quota int64) ZTAPIWorkbenchLeg {
		leg := workbenchTestLeg(input, suffix, "main", quota)
		leg.Native = &ZTAPIWorkbenchNativeBounds{BodySHA256: strings.Repeat("b", 64), OutboundBytes: 2000, MaxOutput: 1024}
		return leg
	}
	reserve := func(task string, leg ZTAPIWorkbenchLeg) error {
		_, err := BeginZTAPIWorkbenchBatch(task, 1000, []ZTAPIWorkbenchLeg{leg}, policy)
		return err
	}
	task := uuid.NewString()
	first := makeLeg("-first", 600)
	worse := makeLeg("-wrong-model", 10)
	worse.Settlement.PublicModel = "zt-claude-opus-5"
	require.ErrorIs(t, reserve(task, worse), ErrZTAPISettlementConflict)
	assertZTAPISettlementBalances(t, db, input, 1000, 1000)
	require.NoError(t, reserve(task, first))
	second := makeLeg("-second", 201)
	require.ErrorIs(t, reserve(uuid.NewString(), second), ErrZTAPIWorkbenchBudget)
	assertZTAPISettlementBalances(t, db, input, 400, 400)
	path := "/v1/chat/completions"
	if pin != "" {
		_, err := ClaimZTAPIWorkbenchNativeAttempt(task, input.UserID, input.TokenID, first.Settlement.OperationID, 4,
			strings.Repeat("c", 64), path, policy)
		require.ErrorIs(t, err, ErrZTAPISettlementConflict)
		path = "/v1/messages"
	}
	_, err := ClaimZTAPIWorkbenchNativeAttempt(task, input.UserID, input.TokenID, first.Settlement.OperationID, 4,
		strings.Repeat("c", 64), path)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	_, err = ClaimZTAPIWorkbenchNativeAttempt(task, input.UserID, input.TokenID, first.Settlement.OperationID, 4,
		strings.Repeat("c", 64), path, policy)
	require.NoError(t, err)
	_, err = FinalizeZTAPIRequestSettlement(first.Settlement.OperationID, 601, `{"input":601}`,
		`[{"dimension":"input_tokens","units":"601","unit_quota":"1","charged_quota":601}]`)
	require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
	assertZTAPISettlementBalances(t, db, input, 400, 400)
	require.ErrorIs(t, reserve(uuid.NewString(), second), ErrZTAPIWorkbenchBudget)
	_, err = FinalizeZTAPIRequestSettlement(first.Settlement.OperationID, 150, `{"input":150}`,
		`[{"dimension":"input_tokens","units":"150","unit_quota":"1","charged_quota":150}]`)
	require.NoError(t, err)
	require.NoError(t, reserve(uuid.NewString(), makeLeg("-exact", 650)))
	require.ErrorIs(t, reserve(uuid.NewString(), makeLeg("-over", 1)), ErrZTAPIWorkbenchBudget)
	assertZTAPISettlementBalances(t, db, input, 200, 200)
}
