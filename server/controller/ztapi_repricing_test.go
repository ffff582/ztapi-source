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

func TestRepriceZTAPICommercialCatalogRequiresConfirmationAndUsesOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := ztapiCommercialRepricer
	t.Cleanup(func() { ztapiCommercialRepricer = original })
	called := 0
	ztapiCommercialRepricer = func(operatorID int) (model.ZTAPICommercialRepricingResult, error) {
		called++
		require.Equal(t, 7, operatorID)
		return model.ZTAPICommercialRepricingResult{Imported: 2, Republished: 2}, nil
	}

	run := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", 7)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/models/ztapi/reprice-commercial-v2", strings.NewReader(body))
		context.Request.Header.Set("Content-Type", "application/json")
		RepriceZTAPICommercialCatalog(context)
		return recorder
	}

	rejected := run(`{"confirm":false}`)
	require.Equal(t, http.StatusBadRequest, rejected.Code)
	require.Zero(t, called)

	accepted := run(`{"confirm":true}`)
	require.Equal(t, http.StatusOK, accepted.Code)
	require.Equal(t, 1, called)
	require.Contains(t, accepted.Body.String(), `"imported":2`)
}

func TestRepriceZTAPISaleMultiplierRequiresExactConfirmationAndUsesOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := ztapiSaleMultiplierRepricer
	t.Cleanup(func() { ztapiSaleMultiplierRepricer = original })
	called := 0
	ztapiSaleMultiplierRepricer = func(operatorID int, multiplier string) (model.ZTAPICommercialRepricingResult, error) {
		called++
		require.Equal(t, 7, operatorID)
		require.Equal(t, "0.9", multiplier)
		return model.ZTAPICommercialRepricingResult{Imported: 47, Republished: 47}, nil
	}

	run := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", 7)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/models/ztapi/reprice-sale-multiplier", strings.NewReader(body))
		context.Request.Header.Set("Content-Type", "application/json")
		RepriceZTAPISaleMultiplier(context)
		return recorder
	}

	require.Equal(t, http.StatusBadRequest, run(`{"confirm":false,"sale_multiplier":"0.9"}`).Code)
	require.Equal(t, http.StatusBadRequest, run(`{"confirm":true,"sale_multiplier":0.9}`).Code)
	require.Zero(t, called)

	accepted := run(`{"confirm":true,"sale_multiplier":"0.9"}`)
	require.Equal(t, http.StatusOK, accepted.Code)
	require.Equal(t, 1, called)
	require.Contains(t, accepted.Body.String(), `"republished":47`)
}
