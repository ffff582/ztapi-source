package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func cashMarginHTTP(t *testing.T, handler gin.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 7)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/models/ztapi/cash-margin-20261030", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return w
}

func TestCashMargin20261030HTTPDowngradeFailsClosed(t *testing.T) {
	original := ztapiCashMarginDowngrade20261030
	t.Cleanup(func() { ztapiCashMarginDowngrade20261030 = original })
	ztapiCashMarginDowngrade20261030 = func() (model.ZTAPICashMarginDowngrade20261030, error) {
		return model.ZTAPICashMarginDowngrade20261030{CanDowngrade: false, UnsettledRequestIDs: []uint{12}}, nil
	}
	require.Equal(t, http.StatusConflict, cashMarginHTTP(t, CheckZTAPICashMarginDowngrade20261030, "").Code)
	ztapiCashMarginDowngrade20261030 = func() (model.ZTAPICashMarginDowngrade20261030, error) {
		return model.ZTAPICashMarginDowngrade20261030{CanDowngrade: true}, nil
	}
	require.Equal(t, http.StatusOK, cashMarginHTTP(t, CheckZTAPICashMarginDowngrade20261030, "").Code)
	ztapiCashMarginDowngrade20261030 = func() (model.ZTAPICashMarginDowngrade20261030, error) {
		return model.ZTAPICashMarginDowngrade20261030{}, errors.New("unavailable")
	}
	require.Equal(t, http.StatusServiceUnavailable, cashMarginHTTP(t, CheckZTAPICashMarginDowngrade20261030, "").Code)
}

func TestCashMargin20261030HTTPConfirmSelectionAndConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := ztapiCashMarginApply20261030
	t.Cleanup(func() { ztapiCashMarginApply20261030 = original })
	calls := 0
	ztapiCashMarginApply20261030 = func(operator int, models []model.ZTAPICashMarginSelection20261030, rollback bool) (model.ZTAPICommercialRepricingResult, error) {
		calls++
		require.Equal(t, 7, operator)
		require.Len(t, models, 1)
		if models[0].Version == 99 {
			return model.ZTAPICommercialRepricingResult{}, model.ErrZTAPIModelVersionConflict
		}
		if rollback {
			require.Equal(t, int64(10), models[0].RestoreSnapshotID)
		}
		return model.ZTAPICommercialRepricingResult{Republished: 1}, nil
	}
	for _, body := range []string{
		`{"confirm":false,"models":[{"model_id":1,"version":2,"snapshot_id":3}]}`,
		`{"confirm":true,"models":[]}`,
		`{"confirm":true,"models":[{"model_id":1,"version":0,"snapshot_id":3}]}`,
		`{"confirm":true,"models":[{"model_id":1,"version":2,"snapshot_id":3},{"model_id":1,"version":2,"snapshot_id":3}]}`,
	} {
		require.Equal(t, http.StatusBadRequest, cashMarginHTTP(t, ApplyZTAPICashMargin20261030, body).Code)
	}
	require.Zero(t, calls)
	good := `{"confirm":true,"models":[{"model_id":1,"version":2,"snapshot_id":3}]}`
	require.Equal(t, http.StatusOK, cashMarginHTTP(t, ApplyZTAPICashMargin20261030, good).Code)
	require.Equal(t, http.StatusBadRequest, cashMarginHTTP(t, RestoreZTAPICashMargin20261030, good).Code)
	require.Equal(t, http.StatusOK, cashMarginHTTP(t, RestoreZTAPICashMargin20261030,
		`{"confirm":true,"models":[{"model_id":1,"version":2,"snapshot_id":3,"restore_snapshot_id":10}]}`).Code)
	require.Equal(t, http.StatusConflict, cashMarginHTTP(t, ApplyZTAPICashMargin20261030,
		`{"confirm":true,"models":[{"model_id":1,"version":99,"snapshot_id":3}]}`).Code)
}

func TestCashMargin20261030HTTPPreviewIsReadOnlyAndExplicitAboutRoute(t *testing.T) {
	original := ztapiCashMarginPreview20261030
	t.Cleanup(func() { ztapiCashMarginPreview20261030 = original })
	ztapiCashMarginPreview20261030 = func(models []model.ZTAPICashMarginSelection20261030, rollback bool) ([]model.ZTAPICashMarginChange20261030, error) {
		require.False(t, rollback)
		return []model.ZTAPICashMarginChange20261030{{ProcurementResourceType: "enterprise", AllowedChannels: []int{2}, TargetMargin: "0.30"}}, nil
	}
	w := cashMarginHTTP(t, PreviewZTAPICashMargin20261030, `{"models":[{"model_id":1,"version":2,"snapshot_id":3}]}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"procurement_resource_type":"enterprise"`)
	require.Contains(t, w.Body.String(), `"allowed_channels":[2]`)
	require.Contains(t, w.Body.String(), `"target_margin":"0.30"`)
}
