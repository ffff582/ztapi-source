package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRestoreZTAPIFableBackupRequiresRootConfirmationAndCostProof(t *testing.T) {
	old := restoreZTAPIFableBackup
	t.Cleanup(func() { restoreZTAPIFableBackup = old })
	const valid = `{"confirm":true,"version":34,"snapshot_id":1630,"primary_verification_id":677,"backup_verification_id":700}`
	calls := 0
	restoreZTAPIFableBackup = func(user int, version uint64, snapshot, primary, backup int64, check func(*gorm.DB) error) (*model.ZTAPIModelPublicationSnapshot, error) {
		calls++
		require.Equal(t, 7, user)
		require.EqualValues(t, 34, version)
		require.EqualValues(t, 1630, snapshot)
		require.EqualValues(t, 677, primary)
		require.EqualValues(t, 700, backup)
		require.NotNil(t, check)
		return nil, errors.New("missing protected cost proof")
	}
	for _, tc := range []struct {
		name, body              string
		role, status, wantCalls int
	}{
		{"ordinary admin", valid, common.RoleAdminUser, http.StatusForbidden, 0},
		{"missing confirmation", strings.Replace(valid, "true", "false", 1), common.RoleRootUser, http.StatusBadRequest, 0},
		{"same verification", strings.Replace(valid, "700", "677", 1), common.RoleRootUser, http.StatusBadRequest, 0},
		{"root with unproven costs", valid, common.RoleRootUser, http.StatusConflict, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			c.Set("id", 7)
			c.Set("role", tc.role)
			RestoreZTAPIFableBackup(c)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.wantCalls, calls)
		})
	}
}
