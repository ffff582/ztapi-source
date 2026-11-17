package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func TestPostSetupCreatesRootUserCompatibleWithZTAPILogin(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	if err := db.AutoMigrate(&model.Option{}, &model.Setup{}); err != nil {
		t.Fatalf("migrate setup tables: %v", err)
	}

	originalSetup := constant.Setup
	originalSelfUseMode := operation_setting.SelfUseModeEnabled
	originalDemoSite := operation_setting.DemoSiteEnabled
	common.OptionMapRWMutex.Lock()
	originalOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	constant.Setup = false
	t.Cleanup(func() {
		constant.Setup = originalSetup
		operation_setting.SelfUseModeEnabled = originalSelfUseMode
		operation_setting.DemoSiteEnabled = originalDemoSite
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	engine := gin.New()
	engine.POST("/setup", PostSetup)
	recorder := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/setup",
		`{"username":"admin","password":"at-least-ten","confirmPassword":"at-least-ten","SelfUseModeEnabled":false,"DemoSiteEnabled":false}`,
		nil,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("setup status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}

	var root model.User
	if err := db.Where("username = ?", "admin").First(&root).Error; err != nil {
		t.Fatalf("load setup root user: %v", err)
	}
	if !service.VerifyZTAPIPassword("at-least-ten", root.Password) {
		t.Fatal("setup root password is incompatible with ZTAPI login")
	}
}
