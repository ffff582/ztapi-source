package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestZTAPISelfPasswordChangeKeepsAccountLoginCompatible(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	engine := newZTAPIUserPasswordEngine()
	currentPassword := "current-password-12345678901234567890"
	user := createZTAPIPasswordUser(t, db, "self-password", currentPassword, common.RoleCommonUser)
	login := loginZTAPIPasswordUser(t, engine, user.Username, currentPassword)

	response := performZTAPIUserPasswordRequest(
		engine,
		http.MethodPut,
		"/user/self",
		login.Data.AccessToken,
		login.Data.User.ID,
		fmt.Sprintf(`{"username":"self-password","display_name":"Self Password","original_password":%q,"password":"replacement-password-456"}`, currentPassword),
	)
	assertZTAPIPasswordUpdateSucceeded(t, response)
	assertZTAPIStoredPassword(t, db, user.Id, "replacement-password-456")
	assertZTAPIPasswordLoginRejected(t, engine, user.Username, currentPassword)
	loginZTAPIPasswordUser(t, engine, user.Username, "replacement-password-456")
}

func TestZTAPIAdministratorPasswordChangeKeepsTargetLoginCompatible(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	if err := db.AutoMigrate(&model.Log{}); err != nil {
		t.Fatal(err)
	}
	engine := newZTAPIUserPasswordEngine()
	root := createZTAPIPasswordUser(t, db, "password-root", "root-password-123", common.RoleRootUser)
	target := createZTAPIPasswordUser(t, db, "password-target", "target-password-123", common.RoleCommonUser)
	login := loginZTAPIPasswordUser(t, engine, root.Username, "root-password-123")
	body := fmt.Sprintf(
		`{"id":%d,"username":"password-target","display_name":"Password Target","role":%d,"group":"default","password":"target-replacement-456"}`,
		target.Id,
		common.RoleCommonUser,
	)
	response := performZTAPIUserPasswordRequest(
		engine,
		http.MethodPut,
		"/user/",
		login.Data.AccessToken,
		login.Data.User.ID,
		body,
	)
	assertZTAPIPasswordUpdateSucceeded(t, response)
	assertZTAPIStoredPassword(t, db, target.Id, "target-replacement-456")
	assertZTAPIPasswordLoginRejected(t, engine, target.Username, "target-password-123")
	loginZTAPIPasswordUser(t, engine, target.Username, "target-replacement-456")
}

func TestZTAPIAdministratorCreatedUserCanLogin(t *testing.T) {
	db := setupZTAPIAuthControllerTest(t)
	if err := db.AutoMigrate(&model.Log{}); err != nil {
		t.Fatal(err)
	}
	engine := newZTAPIUserPasswordEngine()
	root := createZTAPIPasswordUser(t, db, "create-root", "root-password-123", common.RoleRootUser)
	rootLogin := loginZTAPIPasswordUser(t, engine, root.Username, "root-password-123")
	password := "managed-password-12345678901234567890"

	response := performZTAPIUserPasswordRequest(
		engine,
		http.MethodPost,
		"/user/",
		rootLogin.Data.AccessToken,
		rootLogin.Data.User.ID,
		fmt.Sprintf(`{"username":"managed-user","display_name":"Managed User","role":%d,"password":%q}`, common.RoleCommonUser, password),
	)
	assertZTAPIPasswordUpdateSucceeded(t, response)

	var created model.User
	if err := db.Where("username = ?", "managed-user").First(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.DisplayName != "Managed User" || created.Role != common.RoleCommonUser {
		t.Fatalf("created user fields = display_name %q role %d", created.DisplayName, created.Role)
	}
	if created.Status != common.UserStatusEnabled || created.Group != "default" {
		t.Fatalf("created user defaults = status %d group %q", created.Status, created.Group)
	}
	if created.GetSetting().SidebarModules == "" {
		t.Fatal("created user is missing the role-based sidebar configuration")
	}
	assertZTAPIStoredPassword(t, db, created.Id, password)
	loginZTAPIPasswordUser(t, engine, created.Username, password)
}

func TestLegacyPasswordEncodingRemainsBcryptCompatible(t *testing.T) {
	currentHash, err := common.Password2Hash("legacy-password")
	if err != nil {
		t.Fatal(err)
	}
	updatedHash, err := encodeUpdatedPassword(currentHash, "legacy-replacement")
	if err != nil {
		t.Fatal(err)
	}
	if !common.ValidatePasswordAndHash("legacy-replacement", updatedHash) {
		t.Fatal("legacy password update no longer produces a bcrypt-compatible hash")
	}
	if _, isZTAPI := service.ZTAPIPasswordHashOrDummy(updatedHash); isZTAPI {
		t.Fatal("legacy bcrypt account was unexpectedly migrated to the ZTAPI password scheme")
	}
}

func newZTAPIUserPasswordEngine() *gin.Engine {
	engine := newZTAPIAuthControllerEngine()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("ztapi-password-test"))))
	engine.PUT("/user/self", middleware.UserAuth(), UpdateSelf)
	engine.PUT("/user/", middleware.AdminAuth(), UpdateUser)
	engine.POST("/user/", middleware.AdminAuth(), CreateUser)
	return engine
}

func createZTAPIPasswordUser(t *testing.T, db *gorm.DB, username, password string, role int) model.User {
	t.Helper()
	encoded, err := service.HashZTAPIPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	user, err := model.CreateZTAPIUserWithEncodedPassword(username, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.Id).Update("role", role).Error; err != nil {
		t.Fatal(err)
	}
	user.Role = role
	return *user
}

func loginZTAPIPasswordUser(t *testing.T, engine *gin.Engine, username, password string) ztAPIAuthResponse {
	t.Helper()
	response := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		fmt.Sprintf(`{"username":%q,"password":%q}`, username, password),
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	decoded := decodeZTAPIAuthResponse(t, response)
	if !decoded.Success || decoded.Data.AccessToken == "" {
		t.Fatalf("login failed: %s", response.Body.String())
	}
	return decoded
}

func assertZTAPIPasswordLoginRejected(t *testing.T, engine *gin.Engine, username, password string) {
	t.Helper()
	response := performZTAPIAuthRequest(
		t,
		engine,
		http.MethodPost,
		"/auth/login",
		fmt.Sprintf(`{"username":%q,"password":%q}`, username, password),
		nil,
	)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("old password status = %d, want 401; body=%s", response.Code, response.Body.String())
	}
}

func performZTAPIUserPasswordRequest(engine *gin.Engine, method, target, token string, userID int, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-API-User", fmt.Sprintf("%d", userID))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func assertZTAPIPasswordUpdateSucceeded(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode password response: %v; body=%s", err, recorder.Body.String())
	}
	if recorder.Code != http.StatusOK || !response.Success {
		t.Fatalf("password update failed: status=%d response=%#v", recorder.Code, response)
	}
}

func assertZTAPIStoredPassword(t *testing.T, db *gorm.DB, userID int, password string) {
	t.Helper()
	var persisted model.User
	if err := db.First(&persisted, userID).Error; err != nil {
		t.Fatal(err)
	}
	if !service.VerifyZTAPIPassword(password, persisted.Password) {
		t.Fatal("updated password is not stored as a ZTAPI-compatible Argon2id hash")
	}
}
