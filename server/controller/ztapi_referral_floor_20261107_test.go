package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReferralFloor20261107ControllerRequiresRootConfirmationAndVersions(t *testing.T) {
	original := ztapiReferralFloorApply20261107
	defer func() { ztapiReferralFloorApply20261107 = original }()
	called := 0
	ztapiReferralFloorApply20261107 = func(operator int, items []model.ZTAPIPriceReductionSelection20261107, rollback bool) (model.ZTAPICommercialRepricingResult, error) {
		called++
		require.Equal(t, 1, operator)
		require.False(t, rollback)
		return model.ZTAPICommercialRepricingResult{}, model.ErrZTAPIModelVersionConflict
	}
	for _, tc := range []struct {
		role int
		body string
		want int
	}{{common.RoleAdminUser, `{"confirm":true,"models":[{"model_id":4,"version":1,"snapshot_id":1,"source_id":1}]}`, 403}, {common.RoleRootUser, `{"models":[{"model_id":4,"version":1,"snapshot_id":1,"source_id":1}]}`, 400}, {common.RoleRootUser, `{"confirm":true,"models":[{"model_id":4,"version":0,"snapshot_id":1,"source_id":1}]}`, 400}, {common.RoleRootUser, `{"confirm":true,"models":[{"model_id":4,"version":1,"snapshot_id":1,"source_id":1}]}`, 409}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("role", tc.role)
		c.Set("id", 1)
		c.Request = httptest.NewRequest("POST", "/api/models/ztapi/referral-floor-20261107/apply", strings.NewReader(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		ApplyZTAPIReferralFloor20261107(c)
		require.Equal(t, tc.want, w.Code)
	}
	require.Equal(t, 1, called)
}
