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
)

func reductionHTTP20261107(handler gin.HandlerFunc, body string, root bool) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 7)
	if root {
		c.Set("role", common.RoleRootUser)
	} else {
		c.Set("role", common.RoleAdminUser)
	}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/models/ztapi/price-reduction-20261107", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return w
}

func TestPriceReduction20261107HTTPRootConfirmationSourceGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := ztapiPriceReductionApply20261107
	t.Cleanup(func() { ztapiPriceReductionApply20261107 = original })
	calls := 0
	ztapiPriceReductionApply20261107 = func(operator int, selected []model.ZTAPIPriceReductionSelection20261107, rollback bool) (model.ZTAPICommercialRepricingResult, error) {
		calls++
		require.Equal(t, 7, operator)
		require.Equal(t, int64(1835), selected[0].SourceID)
		if selected[0].Version == 99 {
			return model.ZTAPICommercialRepricingResult{}, model.ErrZTAPIModelVersionConflict
		}
		return model.ZTAPICommercialRepricingResult{Republished: 1}, nil
	}
	good := `{"confirm":true,"models":[{"model_id":7,"version":20,"snapshot_id":1581,"source_id":1835}]}`
	require.Equal(t, 403, reductionHTTP20261107(ApplyZTAPIPriceReduction20261107, good, false).Code)
	for _, body := range []string{`{"models":[]}`, `{"confirm":false,"models":[{"model_id":7,"version":20,"snapshot_id":1581,"source_id":1835}]}`, `{"confirm":true,"models":[{"model_id":7,"version":20,"snapshot_id":1581}]}`, `{"confirm":true,"models":[{"model_id":7,"version":20,"snapshot_id":1581,"source_id":1835},{"model_id":7,"version":20,"snapshot_id":1581,"source_id":1835}]}`} {
		require.Equal(t, 400, reductionHTTP20261107(ApplyZTAPIPriceReduction20261107, body, true).Code)
	}
	require.Zero(t, calls)
	require.Equal(t, 200, reductionHTTP20261107(ApplyZTAPIPriceReduction20261107, good, true).Code)
	require.Equal(t, 400, reductionHTTP20261107(RollbackZTAPIPriceReduction20261107, good, true).Code)
	require.Equal(t, 409, reductionHTTP20261107(ApplyZTAPIPriceReduction20261107, strings.Replace(good, `"version":20`, `"version":99`, 1), true).Code)
}

func TestPriceReduction20261107HTTPPreviewAndDowngradeFailClosed(t *testing.T) {
	original, guard := ztapiPriceReductionPreview20261107, ztapiPriceReductionDowngrade20261107
	t.Cleanup(func() { ztapiPriceReductionPreview20261107 = original; ztapiPriceReductionDowngrade20261107 = guard })
	ztapiPriceReductionPreview20261107 = func(s []model.ZTAPIPriceReductionSelection20261107, rollback bool) ([]model.ZTAPIPriceReductionChange20261107, error) {
		require.True(t, rollback)
		return []model.ZTAPIPriceReductionChange20261107{{Rollback: true}}, nil
	}
	body := `{"rollback":true,"models":[{"model_id":7,"version":21,"snapshot_id":1700,"source_id":1900,"restore_snapshot_id":1581}]}`
	require.Equal(t, 200, reductionHTTP20261107(PreviewZTAPIPriceReduction20261107, body, true).Code)
	ztapiPriceReductionDowngrade20261107 = func() (model.ZTAPICashMarginDowngrade20261030, error) {
		return model.ZTAPICashMarginDowngrade20261030{}, nil
	}
	require.Equal(t, 409, reductionHTTP20261107(CheckZTAPIPriceReductionDowngrade20261107, "", true).Code)
	ztapiPriceReductionDowngrade20261107 = func() (model.ZTAPICashMarginDowngrade20261030, error) {
		return model.ZTAPICashMarginDowngrade20261030{}, errors.New("missing database")
	}
	require.Equal(t, 503, reductionHTTP20261107(CheckZTAPIPriceReductionDowngrade20261107, "", true).Code)
}
