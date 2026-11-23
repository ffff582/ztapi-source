package controller

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIDisplayPriceCurrencyInUserCatalog(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	seedPublicModelListSnapshot(t, db, "gpt-5.5", "zt-gpt-5.5")
	require.NoError(t, db.Create(&model.User{Id: 1003, Username: "price-currency-user", Password: "password", Group: "default", Status: common.UserStatusEnabled}).Error)
	old := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = old })
	for _, tc := range []struct {
		name string
		rate float64
		want bool
	}{
		{"configured", 7.3, true}, {"zero", 0, false}, {"negative", -1, false}, {"nan", math.NaN(), false}, {"infinity", math.Inf(1), false}, {"extreme", 1e100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.USDExchangeRate = tc.rate
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/models", nil)
			ctx.Set("id", 1003)
			GetUserModels(ctx)
			var body struct {
				Success bool     `json:"success"`
				Data    []string `json:"data"`
				Rate    *float64 `json:"display_cny_per_unit"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
			require.True(t, body.Success)
			require.Equal(t, []string{"zt-gpt-5.5"}, body.Data)
			if tc.want {
				require.NotNil(t, body.Rate)
				require.Equal(t, tc.rate, *body.Rate)
			} else {
				require.Nil(t, body.Rate)
			}
		})
	}
}
