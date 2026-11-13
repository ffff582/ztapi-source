package relay

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMidjourneyImageProxyDoesNotReturnSupplierErrorBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"environment":{"ARBITRARY":"opaque-secret"}}`))
	}))
	defer upstream.Close()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "image-errors.db")), &gorm.Config{})
	require.NoError(t, err)
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old; raw, _ := db.DB(); _ = raw.Close() })
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}, &model.Channel{}))
	require.NoError(t, db.Create(&model.Midjourney{MjId: "synthetic-task", ImageUrl: upstream.URL + "/image"}).Error)
	setting := system_setting.GetFetchSetting()
	oldProtection := setting.EnableSSRFProtection
	setting.EnableSSRFProtection = false
	t.Cleanup(func() { setting.EnableSSRFProtection = oldProtection })
	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "id", Value: "synthetic-task"}}
	RelayMidjourneyImage(c)
	require.Equal(t, 502, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "opaque-secret")
}
