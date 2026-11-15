package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchFixedLimitDiffersFromPendingCapacity(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}))
	hold := workbenchTestLeg(input, "-hold", "main", 500)
	leg := workbenchTestLeg(input, "-next", "child", 200)
	_, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 600, []ZTAPIWorkbenchLeg{hold})
	require.NoError(t, err)
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 600, []ZTAPIWorkbenchLeg{leg})
	require.ErrorIs(t, err, ErrZTAPIWorkbenchBudget)
	require.NotErrorIs(t, err, ErrZTAPIWorkbenchTaskLimit)
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 600, []ZTAPIWorkbenchLeg{workbenchTestLeg(input, "-too-big", "main", 601)})
	require.ErrorIs(t, err, ErrZTAPIWorkbenchTaskLimit)
	assertZTAPISettlementBalances(t, db, input, 500, 500)
	_, err = ReleaseZTAPIRequestSettlement(hold.Settlement.OperationID)
	require.NoError(t, err)
	_, err = BeginZTAPIWorkbenchBatch(workbenchTestTask, 600, []ZTAPIWorkbenchLeg{leg})
	require.NoError(t, err)
	assertZTAPISettlementBalances(t, db, input, 800, 800)
}
