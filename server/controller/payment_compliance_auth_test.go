package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

func TestConfirmPaymentComplianceAllowsZTAPIJWTButRejectsStaticAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("static access token", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/option/payment_compliance", strings.NewReader(`{"confirmed":true}`))
		ctx.Set("use_access_token", true)

		ConfirmPaymentCompliance(ctx)

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
		}
	})

	t.Run("ZTAPI JWT", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/option/payment_compliance", strings.NewReader(`{"confirmed":false}`))
		ctx.Set("use_access_token", true)
		common.SetContextKey(ctx, constant.ContextKeyZTAPIJWTAuthenticated, true)

		ConfirmPaymentCompliance(ctx)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
	})
}
