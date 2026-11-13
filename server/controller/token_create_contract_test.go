package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAddTokenCreateResponseContainsOneTimePlaintextContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.Token{}); err != nil {
		t.Fatalf("migrate token table: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	requestBody := bytes.NewBufferString(`{
		"name": "contract-token",
		"expired_time": -1,
		"unlimited_quota": true
	}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/token/", requestBody)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", 91)

	AddToken(ctx)

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if response["success"] != true {
		t.Fatalf("create response was not successful: %s", recorder.Body.String())
	}
	data, ok := response["data"].(map[string]any)
	if !ok {
		t.Fatalf("create response omitted one-time credential data: %s", recorder.Body.String())
	}
	key, ok := data["key"].(string)
	if !ok || !strings.HasPrefix(key, "sk-zt-") {
		t.Fatalf("create response key = %#v, want one-time sk-zt- credential", data["key"])
	}
	if strings.Count(recorder.Body.String(), key) != 1 {
		t.Fatalf("plaintext must appear exactly once in create response: %s", recorder.Body.String())
	}
}
