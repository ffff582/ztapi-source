package model

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchOpusBetaDispatchIsWalletBoundAndSingleUse(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}, &ZTAPIWorkbenchPilotSpend{}, &ZTAPIRequestAttempt{}))
	input.PublicModel = "zt-claude-opus-5.5"
	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Updates(map[string]any{
		"expired_time": time.Now().Add(20 * time.Minute).Unix(), "workbench_only": true,
		"model_limits_enabled": true, "model_limits": input.PublicModel,
	}).Error)
	leg := workbenchTestLeg(input, "-beta", "main", 300)
	leg.Native = &ZTAPIWorkbenchNativeBounds{BodySHA256: strings.Repeat("a", 64), OutboundBytes: 2048, MaxOutput: 1024}
	_, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 500, []ZTAPIWorkbenchLeg{leg})
	require.NoError(t, err)
	child := workbenchTestLeg(input, "-child", "child", 201)
	child.Native = leg.Native
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 500, []ZTAPIWorkbenchLeg{child})
	require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)

	hold := leg.Settlement.OperationID
	credential := strings.Repeat("b", 64)
	_, err = ClaimZTAPIWorkbenchNativeAttempt(workbenchTestTask, input.UserID, input.TokenID, hold, 2, credential, ZTAPIWorkbenchHubChatPath)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	claim := func(user, token, channel int, path string) (*ZTAPIRequestAttempt, error) {
		return ClaimZTAPIWorkbenchOpusBetaAttempt(workbenchTestTask, user, token, hold, channel, credential, path)
	}
	for _, bad := range []struct {
		user, token, channel int
		path                 string
	}{
		{input.UserID + 1, input.TokenID, 2, ZTAPIWorkbenchHubChatPath},
		{input.UserID, input.TokenID + 1, 2, ZTAPIWorkbenchHubChatPath},
		{input.UserID, input.TokenID, 4, ZTAPIWorkbenchHubChatPath},
		{input.UserID, input.TokenID, 2, ZTAPIWorkbenchChatPath},
	} {
		_, err := claim(bad.user, bad.token, bad.channel, bad.path)
		require.Error(t, err)
	}
	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Update("workbench_only", false).Error)
	_, err = claim(input.UserID, input.TokenID, 2, ZTAPIWorkbenchHubChatPath)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Update("workbench_only", true).Error)
	attempt, err := claim(input.UserID, input.TokenID, 2, ZTAPIWorkbenchHubChatPath)
	require.NoError(t, err)
	require.Equal(t, 1, attempt.Attempt)
	require.Equal(t, 2, attempt.ChannelID)
	require.Equal(t, ZTAPIWorkbenchHubChatPath, attempt.Protocol)
	_, err = claim(input.UserID, input.TokenID, 2, ZTAPIWorkbenchHubChatPath)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	var rows, pilots int64
	require.NoError(t, db.Model(&ZTAPIRequestAttempt{}).Count(&rows).Error)
	require.NoError(t, db.Model(&ZTAPIWorkbenchPilotSpend{}).Count(&pilots).Error)
	require.EqualValues(t, 1, rows)
	require.Zero(t, pilots)
	assertZTAPISettlementBalances(t, db, input, 700, 700)
}

func TestZTAPIWorkbenchOpusBetaCannotDispatchPilotReservation(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}, &ZTAPIWorkbenchPilotSpend{}, &ZTAPIRequestAttempt{}))
	input.PublicModel = ZTAPIWorkbenchOpusBetaModel
	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Updates(map[string]any{
		"expired_time": time.Now().Add(20 * time.Minute).Unix(), "workbench_only": true,
		"model_limits_enabled": true, "model_limits": input.PublicModel,
	}).Error)
	pilot := ZTAPIWorkbenchPilotPolicy{
		BatchID: "5cd3840c-6e36-4222-a4ca-5ac0879ec1ff", PolicySHA256: strings.Repeat("b", 64),
		ExpiresAt: time.Now().Add(20 * time.Minute).Unix(), MaxRequests: 5, BudgetQuota: 500,
		UserID: input.UserID, ChannelID: ZTAPIWorkbenchOpusBetaChannel, PublicModel: input.PublicModel,
		UpstreamPath: ZTAPIWorkbenchChatPath, WirePath: ZTAPIWorkbenchHubChatPath, UpstreamModel: ZTAPIWorkbenchOpusBetaMapping,
	}
	leg := workbenchTestLeg(input, "-pilot", "main", 100)
	leg.Native = &ZTAPIWorkbenchNativeBounds{BodySHA256: strings.Repeat("a", 64), OutboundBytes: 2048, MaxOutput: 1024}
	_, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 500, []ZTAPIWorkbenchLeg{leg}, pilot)
	require.NoError(t, err)
	hold := leg.Settlement.OperationID
	credential := strings.Repeat("c", 64)
	_, err = ClaimZTAPIWorkbenchOpusBetaAttempt(workbenchTestTask, input.UserID, input.TokenID, hold, 2, credential, ZTAPIWorkbenchHubChatPath)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	_, err = ClaimZTAPIWorkbenchNativeAttempt(workbenchTestTask, input.UserID, input.TokenID, hold, 2, credential, ZTAPIWorkbenchHubChatPath, pilot)
	require.NoError(t, err)
}
