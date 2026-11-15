package model

import (
	"fmt"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

const workbenchTestTask = "5afe26a0-c454-456b-862e-c87197ae8098"

func workbenchTestLeg(input ZTAPIRequestSettlement, suffix, stage string, quota int64) ZTAPIWorkbenchLeg {
	input.OperationID += suffix
	input.RequestID += suffix
	input.ReservedQuota = quota
	return ZTAPIWorkbenchLeg{Stage: stage, Settlement: input}
}

func TestZTAPIWorkbenchParallelCannotOverbookTask(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}))
	_, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 700, []ZTAPIWorkbenchLeg{workbenchTestLeg(input, "-main", "main", 200)})
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := BeginZTAPIWorkbenchBatch(workbenchTestTask, 700, []ZTAPIWorkbenchLeg{workbenchTestLeg(input, fmt.Sprintf("-child-%d", i), "child", 300)})
			results <- e
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var succeeded, denied int
	for e := range results {
		if e == nil {
			succeeded++
		} else {
			require.ErrorIs(t, e, ErrZTAPIWorkbenchBudget)
			denied++
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, denied)
	assertZTAPISettlementBalances(t, db, input, 500, 500)
}

func TestZTAPIWorkbenchParallelTasksAndKeysCannotOverbookWallet(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}))
	second := Token{UserId: input.UserID, KeyHash: "second-workbench-key", Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1}
	require.NoError(t, db.Create(&second).Error)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, task := range []string{workbenchTestTask, "ae171d22-3820-46ab-87c4-8d49774e7831"} {
		leg := workbenchTestLeg(input, fmt.Sprintf("-task-%d", i), "main", 600)
		if i == 1 {
			leg.Settlement.TokenID = second.Id
		}
		go func() {
			<-start
			_, err := BeginZTAPIWorkbenchBatch(task, 1000, []ZTAPIWorkbenchLeg{leg})
			results <- err
		}()
	}
	close(start)
	first, secondErr := <-results, <-results
	if first != nil {
		first, secondErr = secondErr, first
	}
	require.NoError(t, first)
	require.ErrorIs(t, secondErr, ErrBalanceLedgerNegativeBalance)
	var holds []ZTAPIRequestSettlement
	require.NoError(t, db.Find(&holds).Error)
	require.Len(t, holds, 1)
	assertZTAPISettlementBalances(t, db, holds[0], 400, 400)
	other := input.TokenID
	if holds[0].TokenID == other {
		other = second.Id
	}
	var unused Token
	require.NoError(t, db.First(&unused, other).Error)
	require.Equal(t, 1000, unused.RemainQuota)
}

func TestZTAPIWorkbenchAtomicPairAndIdempotence(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}))
	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Update("remain_quota", 400).Error)
	pair := []ZTAPIWorkbenchLeg{workbenchTestLeg(input, "-summary", "summary", 200), workbenchTestLeg(input, "-continuation", "continuation", 300)}
	_, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 800, pair)
	require.ErrorIs(t, err, ErrInsufficientTokenQuota)
	assertZTAPISettlementBalances(t, db, input, 1000, 400)
	var holds int64
	require.NoError(t, db.Model(&ZTAPIRequestSettlement{}).Count(&holds).Error)
	require.Zero(t, holds)

	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Update("remain_quota", 700).Error)
	first, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 800, pair)
	require.NoError(t, err)
	require.Len(t, first, 2)
	again, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 800, pair)
	require.NoError(t, err)
	require.Equal(t, first[0].ID, again[0].ID)
	require.Equal(t, first[1].ID, again[1].ID)
	assertZTAPISettlementBalances(t, db, input, 500, 200)
	changed := []ZTAPIWorkbenchLeg{pair[0], workbenchTestLeg(input, "-different", "continuation", 100)}
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 800, changed)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	_, err = ReserveZTAPIRequestSettlement(first[0].OperationID, 201)
	require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
	assertZTAPISettlementBalances(t, db, input, 500, 200)
}

func TestZTAPIWorkbenchOwnerModelAndRenewedToken(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}))
	main := workbenchTestLeg(input, "-main", "main", 200)
	_, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 500, []ZTAPIWorkbenchLeg{main})
	require.NoError(t, err)
	_, err = PendZTAPIRequestSettlement(main.Settlement.OperationID, `{}`, `["usage_unconfirmed"]`)
	require.NoError(t, err)

	child := workbenchTestLeg(input, "-child", "child", 301)
	rotated := Token{UserId: input.UserID, KeyHash: "workbench-renewed", Status: common.TokenStatusEnabled, RemainQuota: 800, ExpiredTime: -1}
	require.NoError(t, db.Create(&rotated).Error)
	child.Settlement.TokenID = rotated.Id
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 500, []ZTAPIWorkbenchLeg{child})
	require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
	child.Settlement.ReservedQuota = 300
	child.Settlement.PublicModel = "another-model"
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 500, []ZTAPIWorkbenchLeg{child})
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	child.Settlement.PublicModel = input.PublicModel
	child.Settlement.UserID++
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 500, []ZTAPIWorkbenchLeg{child})
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	child.Settlement.UserID = input.UserID
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 500, []ZTAPIWorkbenchLeg{child})
	require.NoError(t, err)
	assertZTAPISettlementBalances(t, db, child.Settlement, 500, 500)
}

func TestZTAPIWorkbenchActualOverrunBlocksNewReservations(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}))
	main := workbenchTestLeg(input, "-main", "main", 200)
	_, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 700, []ZTAPIWorkbenchLeg{main})
	require.NoError(t, err)
	priorChild := workbenchTestLeg(input, "-prior-child", "child", 100)
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 700, []ZTAPIWorkbenchLeg{priorChild})
	require.NoError(t, err)
	require.NoError(t, ClaimZTAPIWorkbenchDispatch(workbenchTestTask, input.UserID, input.TokenID, main.Settlement.OperationID))
	_, err = FinalizeZTAPIRequestSettlement(main.Settlement.OperationID, 201, `{"input":201}`, `[{"dimension":"input_tokens","units":"201","unit_quota":"1","charged_quota":201}]`)
	require.NoError(t, err)
	child := workbenchTestLeg(input, "-child", "child", 100)
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 700, []ZTAPIWorkbenchLeg{child})
	require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
	require.ErrorIs(t, ClaimZTAPIWorkbenchDispatch(workbenchTestTask, input.UserID, input.TokenID, priorChild.Settlement.OperationID), ErrZTAPIWorkbenchBudget)
	_, err = ReleaseZTAPIRequestSettlement(priorChild.Settlement.OperationID)
	require.NoError(t, err)
	assertZTAPISettlementBalances(t, db, input, 799, 799)
}

func TestZTAPIWorkbenchUndispatchedReleaseReturnsCapacity(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}))
	main := workbenchTestLeg(input, "-main", "main", 500)
	_, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 600, []ZTAPIWorkbenchLeg{main})
	require.NoError(t, err)
	_, err = ReleaseZTAPIRequestSettlement(main.Settlement.OperationID)
	require.NoError(t, err)
	_, err = BeginZTAPIRequestSettlement(main.Settlement)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	again, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 600, []ZTAPIWorkbenchLeg{main})
	require.NoError(t, err)
	require.Equal(t, ZTAPISettlementReleased, again[0].Status)
	assertZTAPISettlementBalances(t, db, input, 1000, 1000)
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 600, []ZTAPIWorkbenchLeg{workbenchTestLeg(input, "-child", "child", 600)})
	require.NoError(t, err)
	assertZTAPISettlementBalances(t, db, input, 400, 400)
}

func TestZTAPIWorkbenchDispatchClaimIsSingleUseAndWaitsForSummary(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}))
	pair := []ZTAPIWorkbenchLeg{workbenchTestLeg(input, "-summary", "summary", 200), workbenchTestLeg(input, "-continuation", "continuation", 300)}
	rows, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 800, pair)
	require.NoError(t, err)
	claim := func(op string) error {
		return ClaimZTAPIWorkbenchDispatch(workbenchTestTask, input.UserID, input.TokenID, op)
	}
	require.ErrorIs(t, ClaimZTAPIWorkbenchDispatch(workbenchTestTask, input.UserID+1, input.TokenID, rows[0].OperationID), ErrZTAPISettlementConflict)
	require.ErrorIs(t, ClaimZTAPIWorkbenchDispatch(workbenchTestTask, input.UserID, input.TokenID+1, rows[0].OperationID), ErrZTAPISettlementConflict)
	require.ErrorIs(t, claim(rows[1].OperationID), ErrZTAPISettlementPending)
	require.ErrorIs(t, MarkZTAPIRequestDispatched(rows[0].OperationID), ErrZTAPISettlementConflict)
	require.NoError(t, claim(rows[0].OperationID))
	require.ErrorIs(t, claim(rows[0].OperationID), ErrZTAPISettlementConflict)
	require.ErrorIs(t, claim(rows[1].OperationID), ErrZTAPISettlementPending)
	_, err = ReleaseZTAPIRequestSettlement(rows[0].OperationID)
	require.ErrorIs(t, err, ErrZTAPISettlementPending)
	_, err = FinalizeZTAPIRequestSettlement(rows[0].OperationID, 100, `{"input":100}`, `[{"dimension":"input_tokens","units":"100","unit_quota":"1","charged_quota":100}]`)
	require.NoError(t, err)
	require.NoError(t, claim(rows[1].OperationID))
	require.ErrorIs(t, claim(rows[1].OperationID), ErrZTAPISettlementConflict)
	assertZTAPISettlementBalances(t, db, input, 600, 600)
}
