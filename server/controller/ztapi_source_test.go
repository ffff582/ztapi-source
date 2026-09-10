package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/router"
	"github.com/gin-gonic/gin"
)

func TestZTAPISourceMetadataIsAnonymousAndImmutable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("ZTAPI_SOURCE_REPOSITORY", "https://github.com/ffff582/ztapi-source")
	t.Setenv("ZTAPI_SOURCE_TAG", "production-0123456789abcdef0123456789abcdef01234567")
	previousVersion := common.Version
	common.Version = "0123456789abcdef0123456789abcdef01234567"
	t.Cleanup(func() { common.Version = previousVersion })

	engine := gin.New()
	router.SetApiRouter(engine)
	request := httptest.NewRequest(http.MethodGet, "/.well-known/source", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected anonymous source metadata to return 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			License            string `json:"license"`
			SourceRepository   string `json:"source_repository"`
			ProductionCommit   string `json:"production_commit"`
			SourceTag          string `json:"source_tag"`
			UpstreamRepository string `json:"upstream_repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode source metadata: %v", err)
	}
	if !response.Success {
		t.Fatal("source metadata response must report success")
	}
	if response.Data.License != "AGPL-3.0-or-later" {
		t.Fatalf("unexpected license: %q", response.Data.License)
	}
	if response.Data.SourceRepository != "https://github.com/ffff582/ztapi-source" {
		t.Fatalf("unexpected source repository: %q", response.Data.SourceRepository)
	}
	if response.Data.ProductionCommit != common.Version {
		t.Fatalf("unexpected production commit: %q", response.Data.ProductionCommit)
	}
	if response.Data.SourceTag != "production-"+common.Version {
		t.Fatalf("unexpected source tag: %q", response.Data.SourceTag)
	}
	if response.Data.UpstreamRepository != "https://github.com/QuantumNous/new-api" {
		t.Fatalf("unexpected upstream repository: %q", response.Data.UpstreamRepository)
	}
}
