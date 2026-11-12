package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupYunxinClosure(t *testing.T) (*gorm.DB, ZTAPIRequestSettlement, int, []ztapiYunxinCloseCase) {
	t.Helper()
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.Model(&User{}).Where("id = ?", input.UserID).Update("quota", 2000).Error)
	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Update("remain_quota", 2000).Error)
	require.NoError(t, db.AutoMigrate(&Channel{}, &ZTAPIRequestAttempt{}, &ZTAPISettlementLogOutbox{}, &ZTAPIPendingResolution{}, &ZTAPISupplierRefundCharge{}, &ZTAPISupplierReconciliationEntry{}, &ZTAPIMediaTask{}))
	require.NoError(t, db.Create(&Channel{Id: 2}).Error)
	operator := createBalanceLedgerTestUser(t, db, "closure-finance", 0)
	require.NoError(t, db.Model(&operator).Update("role", common.RoleFinanceUser).Error)
	cases := ztapiYunxinCloseCases
	for i, item := range cases {
		input.OperationID = fmt.Sprintf("test-closure-%d", item.id)
		input.RequestID, input.PublicModel = fmt.Sprintf("fixture-request-%d", item.id), item.model
		wireID := fmt.Sprintf("fixture-upstream-%d", item.id)
		cases[i].lineageHash = ztapiSupplierRefundHash(input.RequestID + "|" + wireID)
		input.ReservedQuota = item.hold
		// Ensure the settlement primary keys match the approved scope.
		if item.id == 530 || item.id == 1599 {
			prior := input
			prior.ID = item.id - 1
			prior.OperationID = "seed-" + input.OperationID
			prior.RequestID = "seed-" + input.RequestID
			require.NoError(t, db.Create(&prior).Error)
		}
		row, err := BeginZTAPIRequestSettlement(input)
		require.NoError(t, err)
		require.Equal(t, item.id, row.ID)
		_, err = BeginZTAPIRequestAttempt(row.OperationID, 2, "fixture-cred", "/hub/v1/responses")
		require.NoError(t, err)
		require.NoError(t, RecordZTAPIRequestAttemptResponse(row.OperationID, 1, 2, item.httpStatus, wireID))
		usage := "{}"
		if item.actual > 0 {
			usage = `{"prompt_tokens":297,"completion_tokens":11,"total_tokens":308}`
		}
		_, err = PendZTAPIRequestSettlement(row.OperationID, usage, item.missing)
		require.NoError(t, err)
	}
	return db, input, operator.Id, cases[:]
}

func fixtureYunxinClosePrice(parent ZTAPIRequestSettlement, s ZTAPIAttemptBillingSubmission) (ZTAPIAttemptBillingPriced, error) {
	return ZTAPIAttemptBillingPriced{
		Dimensions: []ZTAPISupplierRefundDimension{
			{Dimension: "input_tokens", Units: "297", UnitQuota: "0.2545248869", ChargedQuota: 76},
			{Dimension: "output_tokens", Units: "11", UnitQuota: "0.7635746606", ChargedQuota: 8},
		},
		ConsumeLog: Log{UserId: parent.UserID, TokenId: parent.TokenID, Type: LogTypeConsume, Quota: 84,
			RequestId: parent.RequestID, ModelName: parent.PublicModel, ChannelId: s.ChannelID, PromptTokens: 297,
			CompletionTokens: 11, CreatedAt: parent.CreatedAt.Unix()},
	}, nil
}

func TestZTAPIYunxinClosePreviewApplyAndReplay(t *testing.T) {
	db, input, operator, cases := setupYunxinClosure(t)
	preview, err := applyZTAPIYunxinCloseCases(operator, false, fixtureYunxinClosePrice, cases)
	require.NoError(t, err)
	require.Len(t, preview, 6)
	assertZTAPISettlementBalances(t, db, input, 624, 624)
	receipts, err := applyZTAPIYunxinCloseCases(operator, true, fixtureYunxinClosePrice, cases)
	require.NoError(t, err)
	require.Len(t, receipts, 6)
	assertZTAPISettlementBalances(t, db, input, 1916, 1916)
	var entries []BalanceLedger
	require.NoError(t, db.Where("operator_id = ?", operator).Order("id").Find(&entries).Error)
	require.Len(t, entries, 6)
	require.EqualValues(t, 59, entries[5].Delta)
	require.Contains(t, entries[0].Reason, "platform waiver")
	var row ZTAPIRequestSettlement
	require.NoError(t, db.First(&row, 1599).Error)
	require.Equal(t, ZTAPISettlementSettled, row.Status)
	require.EqualValues(t, 84, row.ChargedQuota)
	var review ZTAPIAttemptBillingReview
	require.NoError(t, db.Where("settlement_id = ?", 530).Take(&review).Error)
	require.Equal(t, "platform_waived", review.Status)
	var proofs int64
	require.NoError(t, db.Model(&ZTAPIPendingResolution{}).Count(&proofs).Error)
	require.Zero(t, proofs)
	_, err = applyZTAPIYunxinCloseCases(operator, true, fixtureYunxinClosePrice, cases)
	require.NoError(t, err)
	assertZTAPISettlementBalances(t, db, input, 1916, 1916)
}

func TestZTAPIYunxinCloseRefusesConflictingRowAtomically(t *testing.T) {
	db, input, operator, cases := setupYunxinClosure(t)
	require.NoError(t, db.Model(&ZTAPIRequestSettlement{}).Where("id = ?", 1599).Update("reserved_quota", 144).Error)
	_, err := applyZTAPIYunxinCloseCases(operator, true, fixtureYunxinClosePrice, cases)
	require.Error(t, err)
	assertZTAPISettlementBalances(t, db, input, 624, 624)
	var settled int64
	require.NoError(t, db.Model(&BalanceLedger{}).Where("operator_id = ?", operator).Count(&settled).Error)
	require.Zero(t, settled)
}

func TestZTAPIYunxinCloseRefusesOrdinaryOperatorAndNewProof(t *testing.T) {
	db, input, operator, cases := setupYunxinClosure(t)
	_, err := applyZTAPIYunxinCloseCases(input.UserID, true, fixtureYunxinClosePrice, cases)
	require.ErrorIs(t, err, ErrZTAPISupplierRefundUnauthorized)
	require.NoError(t, db.Create(&ZTAPIAttemptBillingProof{RequestID: input.RequestID, ProofKey: "fixture"}).Error)
	_, err = applyZTAPIYunxinCloseCases(operator, true, fixtureYunxinClosePrice, cases)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	assertZTAPISettlementBalances(t, db, input, 624, 624)
}
