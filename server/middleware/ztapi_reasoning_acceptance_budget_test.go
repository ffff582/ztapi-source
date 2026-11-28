package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningAcceptanceModelBudgetRequiresPrivateOptIn(t *testing.T) {
	for _, tc := range []struct {
		source, rounds, proto string
		accepted, bounded     bool
	}{
		{"1", "1", "http", true, true}, {"1", "", "http", true, false}, {"1", "1", "https", false, false}, {"", "1", "http", false, false}, {"1", "2", "http", true, false},
	} {
		t.Run(tc.source+"/"+tc.rounds+"/"+tc.proto, func(t *testing.T) {
			router := gin.New()
			router.Use(ztapiHealthProbePinMiddleware(0, nil, nil))
			router.GET("/", func(c *gin.Context) {
				require.Equal(t, tc.accepted, common.GetContextKeyBool(c, constant.ContextKeyZTAPIInternalAcceptance))
				require.Equal(t, tc.bounded, common.GetContextKeyBool(c, constant.ContextKeyZTAPIAcceptanceSingleRound))
				bounded, _ := c.Request.Context().Value(constant.ContextKeyZTAPIAcceptanceSingleRound).(bool)
				require.Equal(t, tc.bounded, bounded)
				require.Empty(t, c.GetHeader(ZTAPIInternalAcceptanceHeader))
				require.Empty(t, c.GetHeader(ZTAPIAcceptanceModelRoundsHeader))
				c.Status(204)
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(ZTAPIInternalAcceptanceHeader, tc.source)
			req.Header.Set(ZTAPIAcceptanceModelRoundsHeader, tc.rounds)
			req.Header.Set("X-Forwarded-Proto", tc.proto)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			require.Equal(t, 204, out.Code)
		})
	}
}
