package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierReprice20261019RequiresConfirmationHashAndVersions(t *testing.T) {
	old := ztapiSupplierRepricer20261019
	t.Cleanup(func() { ztapiSupplierRepricer20261019 = old })
	calls := 0
	ztapiSupplierRepricer20261019 = func(operatorID int, checksum string, names []string, versions map[string]uint64) (model.ZTAPICommercialRepricingResult, error) {
		calls++
		require.Equal(t, 7, operatorID)
		require.Equal(t, model.ZTAPISupplierWorkbook20261019SHA256, checksum)
		require.Equal(t, uint64(4), versions["zt-claude-opus-5"])
		return model.ZTAPICommercialRepricingResult{Republished: 1}, nil
	}
	run := func(body string) int {
		r := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(r)
		ctx.Set("id", 7)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		RepriceZTAPISupplierCatalog20261019(ctx)
		return r.Code
	}
	require.Equal(t, 400, run(`{"confirm":false}`))
	require.Equal(t, 400, run(`{"confirm":true,"models":["zt-claude-opus-5"],"workbook_sha256":"wrong"}`))
	valid := `{"confirm":true,"models":["zt-claude-opus-5"],"workbook_sha256":"` + model.ZTAPISupplierWorkbook20261019SHA256 + `"`
	require.Equal(t, 400, run(valid+`}`))
	require.Zero(t, calls)
	require.Equal(t, 200, run(valid+`,"expected_versions":{"zt-claude-opus-5":4}}`))
	require.Equal(t, 1, calls)
}
