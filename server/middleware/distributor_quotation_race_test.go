package middleware

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDistributeQuotationLookupFailureCannotSkipSnapshot(t *testing.T) {
	f := setupRelaySecurityFixture(t, "gpt-5.5")
	require.NoError(t, model.MigrateZTAPIHealth(f.db))
	publishRelaySecurityAlias(t, f.db, f.channel.Id, "zt-gpt-5.5", "gpt-5.5")
	_, err := model.GetZTAPIRuntimePublication("zt-gpt-5.5")
	require.NoError(t, err)

	reads := 0
	callback := "test:quotation-authority-unavailable"
	require.NoError(t, f.db.Callback().Row().Before("gorm:row").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.TableExpr == nil || !strings.Contains(tx.Statement.TableExpr.SQL, "ztapi_model_configs AS c") {
			return
		}
		reads++
		if reads >= 2 {
			tx.AddError(errors.New("test publication authority unavailable"))
		}
	}))
	t.Cleanup(func() { _ = f.db.Callback().Row().Remove(callback) })
	called := false
	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	}, Distribute(), func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"zt-gpt-5.5","messages":[]}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	require.GreaterOrEqual(t, reads, 2, "fault must occur after initial model resolution")
	require.False(t, called, "failed revalidation must never enter relay without an approved snapshot")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}
