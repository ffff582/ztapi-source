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

func TestZTAPIABRepriceRequiresExactQuoteAndExplicitConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldPreview, oldApply := ztapiABPreviewer, ztapiABRepricer
	t.Cleanup(func() { ztapiABPreviewer, ztapiABRepricer = oldPreview, oldApply })
	ztapiABPreviewer = func() (model.ZTAPIABQuotePricingPreview, error) {
		return model.ZTAPIABQuotePricingPreview{WorkbookSHA256: strings.Repeat("a", 64), QuotedModels: 50}, nil
	}
	calls := 0
	ztapiABRepricer = func(operatorID int, sha string, names []string) (model.ZTAPICommercialRepricingResult, error) {
		calls++
		require.Equal(t, 7, operatorID)
		require.Equal(t, strings.Repeat("a", 64), sha)
		require.Equal(t, []string{"GLM 5.2"}, names)
		return model.ZTAPICommercialRepricingResult{Imported: 1, Republished: 1}, nil
	}
	run := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Set("id", 7)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/models/ztapi/reprice-ab-20260915", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		RepriceZTAPIABCatalog(ctx)
		return recorder
	}
	require.Equal(t, http.StatusBadRequest, run(`{"confirm":false,"workbook_sha256":"`+strings.Repeat("a", 64)+`","models":["GLM 5.2"]}`).Code)
	require.Equal(t, http.StatusBadRequest, run(`{"confirm":true,"workbook_sha256":"`+strings.Repeat("b", 64)+`","models":["GLM 5.2"]}`).Code)
	require.Zero(t, calls)
	require.Equal(t, http.StatusOK, run(`{"confirm":true,"workbook_sha256":"`+strings.Repeat("a", 64)+`","models":["GLM 5.2"]}`).Code)
	require.Equal(t, 1, calls)
}
