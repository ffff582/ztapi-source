package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestZTAPIHealthDistributeCircuitBeforeUnpublishedAndPrecharge(t *testing.T) {
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "gate.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	if err = db.AutoMigrate(&model.ZTAPIModelConfig{}, &model.ZTAPIHealthState{}); err != nil {
		t.Fatal(err)
	}
	alias := "zt-circuit-test"
	config := model.ZTAPIModelConfig{PublicName: &alias, SourceModel: "source-test", Published: false, Version: 1}
	if err = db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.ZTAPIHealthState{ModelID: config.ID, Open: true}).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	called := false
	r.POST("/v1/chat/completions", func(c *gin.Context) { c.Set(common.RequestIdKey, "test-request-id"); c.Next() }, Distribute(), func(c *gin.Context) { called = true; c.Status(200) })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"zt-circuit-test","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if called || w.Code != 503 || !strings.Contains(w.Body.String(), "model_temporarily_unavailable") || !strings.Contains(w.Body.String(), "test-request-id") {
		t.Fatalf("gate reached handler=%t status=%d body=%s", called, w.Code, w.Body.String())
	}
}
