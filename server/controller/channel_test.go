package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/router"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const channelTestSecret = "task-seven-test-secret-never-return"

func TestZTAPIChannelResponsesMaskCredentialsAndFilterProviders(t *testing.T) {
	db, engine := setupChannelControllerTest(t)
	reader, token := createChannelOperator(t, db, "channel-reader", common.RoleSupportUser)
	supported := createTestChannel(t, db, "OpenAI upstream", 1, channelTestSecret)
	nestedSecret := "nested-task-seven-secret-never-return"
	if err := db.Model(&supported).Updates(map[string]interface{}{
		"header_override": `{"Authorization":"Bearer ` + nestedSecret + `"}`,
		"param_override":  `{"signed_url":"https://example.invalid/?token=` + nestedSecret + `"}`,
		"other":           nestedSecret,
		"other_info":      nestedSecret,
		"setting":         nestedSecret,
		"settings":        nestedSecret,
	}).Error; err != nil {
		t.Fatalf("seed nested credentials: %v", err)
	}
	createTestChannel(t, db, "Unsupported upstream", 4, "unsupported-provider-secret")

	for _, target := range []string{
		"/api/channel/ztapi/?p=1&page_size=20",
		"/api/channel/ztapi/search?keyword=OpenAI&p=1&page_size=20",
		fmt.Sprintf("/api/channel/ztapi/%d", supported.Id),
	} {
		recorder := performChannelRequest(t, engine, http.MethodGet, target, reader, token, "", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d; body=%s", target, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if strings.Contains(body, channelTestSecret) || strings.Contains(body, nestedSecret) || strings.Contains(body, "unsupported-provider-secret") {
			t.Fatalf("GET %s leaked a credential: %s", target, body)
		}
		if !strings.Contains(body, `"key":"********"`) {
			t.Fatalf("GET %s did not return the server-side mask: %s", target, body)
		}
		if strings.Contains(body, "Unsupported upstream") {
			t.Fatalf("GET %s exposed an unsupported provider: %s", target, body)
		}
	}

	for _, target := range []string{
		"/api/channel/?p=1&page_size=20",
		"/api/channel/search?keyword=OpenAI&p=1&page_size=20",
		fmt.Sprintf("/api/channel/%d", supported.Id),
	} {
		recorder := performChannelRequest(t, engine, http.MethodGet, target, reader, token, "", "")
		assertChannelRequestDenied(t, recorder)
		if strings.Contains(recorder.Body.String(), nestedSecret) {
			t.Fatalf("denied legacy GET %s leaked nested credentials: %s", target, recorder.Body.String())
		}
	}
}

func TestZTAPIChannelWritesRestrictProvidersAndPreserveBlankKey(t *testing.T) {
	db, engine := setupChannelControllerTest(t)
	admin, token := createChannelOperator(t, db, "ztapi-channel-writer", common.RoleAdminUser)

	unsupportedCreate := performChannelRequest(t, engine, http.MethodPost, "/api/channel/ztapi/", admin, token,
		`{"name":"unsupported","type":4,"key":"secret","base_url":"https://example.invalid","models":"test","group":"default"}`, "")
	assertChannelRequestDenied(t, unsupportedCreate)
	var unsupportedCount int64
	if err := db.Model(&model.Channel{}).Where("name = ?", "unsupported").Count(&unsupportedCount).Error; err != nil {
		t.Fatalf("count unsupported channels: %v", err)
	}
	if unsupportedCount != 0 {
		t.Fatalf("unsupported provider was created")
	}

	created := performChannelRequest(t, engine, http.MethodPost, "/api/channel/ztapi/", admin, token,
		`{"name":"ZTAPI OpenAI","type":1,"key":"original-write-secret","base_url":"https://8.8.8.8","models":"zt-gpt-test","group":"default","priority":100,"weight":7,"model_mapping":"{\"zt-gpt-test\":\"gpt-test\"}"}`, "")
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"key":"********"`) {
		t.Fatalf("supported create = %d %s", created.Code, created.Body.String())
	}
	for _, expected := range []string{`"priority":100`, `"weight":7`, `"model_mapping":"{\"zt-gpt-test\":\"gpt-test\"}"`} {
		if !strings.Contains(created.Body.String(), expected) {
			t.Fatalf("supported create dropped routing field %s: %s", expected, created.Body.String())
		}
	}
	var channel model.Channel
	if err := db.Where("name = ?", "ZTAPI OpenAI").First(&channel).Error; err != nil {
		t.Fatalf("load created channel: %v", err)
	}
	if !channel.ZTAPIManaged || channel.ZTAPIFamily != model.ZTAPIModelFamilyOpenAI {
		t.Fatalf("created channel trust metadata = managed:%v family:%q, want managed OpenAI", channel.ZTAPIManaged, channel.ZTAPIFamily)
	}
	if channel.GetPriority() != 100 || channel.GetWeight() != 7 || channel.GetModelMapping() != `{"zt-gpt-test":"gpt-test"}` {
		t.Fatalf("created channel routing = priority:%d weight:%d mapping:%q", channel.GetPriority(), channel.GetWeight(), channel.GetModelMapping())
	}

	unsupportedUpdate := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/channel/ztapi/%d", channel.Id), admin, token,
		`{"name":"converted","type":4,"key":"","base_url":"https://example.invalid","models":"test","group":"default","status":1}`, "")
	assertChannelRequestDenied(t, unsupportedUpdate)
	if err := db.First(&channel, channel.Id).Error; err != nil {
		t.Fatalf("reload rejected conversion: %v", err)
	}
	if channel.Type != 1 || channel.Name != "ZTAPI OpenAI" {
		t.Fatalf("rejected conversion changed channel: type=%d name=%q", channel.Type, channel.Name)
	}

	blankKeyUpdate := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/channel/ztapi/%d", channel.Id), admin, token,
		`{"name":"ZTAPI OpenAI updated","type":1,"key":"","base_url":"https://8.8.8.8/v1","models":"zt-gpt-test","group":"default","status":1,"priority":90,"weight":3,"model_mapping":"{\"zt-gpt-test\":\"gpt-test-v2\"}"}`, "")
	if blankKeyUpdate.Code != http.StatusOK || !strings.Contains(blankKeyUpdate.Body.String(), `"key":"********"`) {
		t.Fatalf("blank-key update = %d %s", blankKeyUpdate.Code, blankKeyUpdate.Body.String())
	}
	if err := db.First(&channel, channel.Id).Error; err != nil {
		t.Fatalf("reload blank-key update: %v", err)
	}
	if channel.Key != "original-write-secret" {
		t.Fatalf("blank-key update replaced secret with %q", channel.Key)
	}
	if channel.GetPriority() != 90 || channel.GetWeight() != 3 || channel.GetModelMapping() != `{"zt-gpt-test":"gpt-test-v2"}` {
		t.Fatalf("updated channel routing = priority:%d weight:%d mapping:%q", channel.GetPriority(), channel.GetWeight(), channel.GetModelMapping())
	}
	if !channel.ZTAPIManaged || channel.ZTAPIFamily != model.ZTAPIModelFamilyOpenAI {
		t.Fatalf("updated channel trust metadata = managed:%v family:%q, want managed OpenAI", channel.ZTAPIManaged, channel.ZTAPIFamily)
	}

	statusUpdate := performChannelRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/channel/ztapi/%d/status", channel.Id), admin, token,
		`{"status":2}`, "")
	if statusUpdate.Code != http.StatusOK {
		t.Fatalf("status update = %d %s", statusUpdate.Code, statusUpdate.Body.String())
	}
	if err := db.First(&channel, channel.Id).Error; err != nil {
		t.Fatalf("reload status update: %v", err)
	}
	if channel.Status != 2 {
		t.Fatalf("status = %d, want 2", channel.Status)
	}
}

func TestZTAPIChannelWritesAllowOnlyDisabledBlankPlaceholder(t *testing.T) {
	db, engine := setupChannelControllerTest(t)
	admin, token := createChannelOperator(t, db, "ztapi-placeholder-writer", common.RoleAdminUser)

	disabled := performChannelRequest(t, engine, http.MethodPost, "/api/channel/ztapi/", admin, token,
		`{"name":"APIKEY FUN placeholder","type":1,"key":"","base_url":"https://8.8.8.8","models":"gpt-placeholder","group":"default","status":2,"priority":100,"weight":1}`, "")
	if disabled.Code != http.StatusOK {
		t.Fatalf("disabled blank placeholder = %d %s", disabled.Code, disabled.Body.String())
	}
	var placeholder model.Channel
	if err := db.Where("name = ?", "APIKEY FUN placeholder").First(&placeholder).Error; err != nil {
		t.Fatalf("load placeholder: %v", err)
	}
	if placeholder.Status != common.ChannelStatusManuallyDisabled || placeholder.Key != "" || placeholder.ZTAPIKeyCiphertext != "" {
		t.Fatalf("placeholder status/key/ciphertext = %d/%q/%q", placeholder.Status, placeholder.Key, placeholder.ZTAPIKeyCiphertext)
	}

	enableByStatus := performChannelRequest(t, engine, http.MethodPatch, fmt.Sprintf("/api/channel/ztapi/%d/status", placeholder.Id), admin, token,
		`{"status":1}`, "")
	if enableByStatus.Code != http.StatusBadRequest {
		t.Fatalf("enable blank placeholder by status = %d %s", enableByStatus.Code, enableByStatus.Body.String())
	}

	enableByEdit := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/channel/ztapi/%d", placeholder.Id), admin, token,
		`{"name":"APIKEY FUN placeholder","type":1,"key":"","base_url":"https://8.8.8.8","models":"gpt-placeholder","group":"default","status":1,"priority":100,"weight":1}`, "")
	if enableByEdit.Code != http.StatusBadRequest {
		t.Fatalf("enable blank placeholder by edit = %d %s", enableByEdit.Code, enableByEdit.Body.String())
	}
	if err := db.First(&placeholder, placeholder.Id).Error; err != nil {
		t.Fatalf("reload rejected placeholder activation: %v", err)
	}
	if placeholder.Status != common.ChannelStatusManuallyDisabled {
		t.Fatalf("rejected placeholder activation changed status to %d", placeholder.Status)
	}

	enabled := performChannelRequest(t, engine, http.MethodPost, "/api/channel/ztapi/", admin, token,
		`{"name":"invalid enabled placeholder","type":1,"key":"","base_url":"https://8.8.8.8","models":"gpt-placeholder","group":"default","status":1}`, "")
	if enabled.Code != http.StatusBadRequest {
		t.Fatalf("enabled blank placeholder = %d %s", enabled.Code, enabled.Body.String())
	}

	invalidMapping := performChannelRequest(t, engine, http.MethodPost, "/api/channel/ztapi/", admin, token,
		`{"name":"invalid mapping","type":1,"key":"test-key","base_url":"https://8.8.8.8","models":"gpt-placeholder","group":"default","status":2,"model_mapping":"{bad-json"}`, "")
	if invalidMapping.Code != http.StatusBadRequest {
		t.Fatalf("invalid model mapping = %d %s", invalidMapping.Code, invalidMapping.Body.String())
	}
}

func TestZTAPIChannelWritesRejectUnsafeBaseURLs(t *testing.T) {
	db, engine := setupChannelControllerTest(t)
	admin, token := createChannelOperator(t, db, "ztapi-channel-ssrf", common.RoleAdminUser)
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "plaintext", baseURL: "http://8.8.8.8"},
		{name: "loopback", baseURL: "https://127.0.0.1"},
		{name: "private", baseURL: "https://10.0.0.1"},
		{name: "metadata", baseURL: "https://169.254.169.254"},
		{name: "nonstandard-port", baseURL: "https://8.8.8.8:8443"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(
				`{"name":"unsafe-%s","type":1,"key":"secret","base_url":%q,"models":"gpt-test","group":"default"}`,
				test.name,
				test.baseURL,
			)
			recorder := performChannelRequest(t, engine, http.MethodPost, "/api/channel/ztapi/", admin, token, body, "")
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("unsafe base URL %q status=%d body=%s", test.baseURL, recorder.Code, recorder.Body.String())
			}
		})
	}

	var count int64
	if err := db.Model(&model.Channel{}).Where("name LIKE ?", "unsafe-%").Count(&count).Error; err != nil {
		t.Fatalf("count unsafe channels: %v", err)
	}
	if count != 0 {
		t.Fatalf("unsafe channels persisted: %d", count)
	}
}

func TestChannelKeyRevealRequiresRootSecureVerificationAndAuditsWithoutSecrets(t *testing.T) {
	db, engine := setupChannelControllerTest(t)
	admin, adminToken := createChannelOperator(t, db, "channel-admin", common.RoleAdminUser)
	root, rootToken := createChannelOperator(t, db, "channel-root", common.RoleRootUser)
	channel := createTestChannel(t, db, "Reveal target", 1, channelTestSecret)
	target := fmt.Sprintf("/api/channel/%d/key", channel.Id)

	adminDenied := performChannelRequest(t, engine, http.MethodPost, target, admin, adminToken, "", "")
	assertChannelRequestDenied(t, adminDenied)
	assertNoStoreHeaders(t, adminDenied)

	unverified := performChannelRequest(t, engine, http.MethodPost, target, root, rootToken, "", "")
	if unverified.Code != http.StatusForbidden || !strings.Contains(unverified.Body.String(), "VERIFICATION_REQUIRED") {
		t.Fatalf("unverified reveal = %d %s", unverified.Code, unverified.Body.String())
	}
	assertNoStoreHeaders(t, unverified)

	cookieValue := establishChannelSecureVerification(t, engine, root, rootToken)
	revealed := performChannelRequest(t, engine, http.MethodPost, target, root, rootToken, "", cookieValue)
	if revealed.Code != http.StatusOK || !strings.Contains(revealed.Body.String(), channelTestSecret) {
		t.Fatalf("verified reveal = %d %s", revealed.Code, revealed.Body.String())
	}
	assertNoStoreHeaders(t, revealed)

	var audit model.Log
	if err := db.Where("type = ? AND other LIKE ?", model.LogTypeManage, "%channel.key_view%").Order("id DESC").First(&audit).Error; err != nil {
		t.Fatalf("load reveal audit: %v", err)
	}
	if strings.Contains(audit.Content, channelTestSecret) || strings.Contains(audit.Other, channelTestSecret) {
		t.Fatalf("reveal audit leaked the credential: content=%q other=%s", audit.Content, audit.Other)
	}
}

func TestChannelOperationalRoutesRejectReadOnlyOperators(t *testing.T) {
	db, engine := setupChannelControllerTest(t)
	reader, token := createChannelOperator(t, db, "channel-read-only", common.RoleSupportUser)
	channel := createTestChannel(t, db, "Read only target", 1, channelTestSecret)

	requests := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, "/api/channel/ztapi/", `{"name":"denied","type":1,"key":"test-only","base_url":"https://example.invalid","models":"gpt-test","group":"default"}`},
		{http.MethodPut, fmt.Sprintf("/api/channel/ztapi/%d", channel.Id), `{"name":"denied","type":1,"key":"","base_url":"https://example.invalid","models":"gpt-test","group":"default","status":1}`},
		{http.MethodPatch, fmt.Sprintf("/api/channel/ztapi/%d/status", channel.Id), `{"status":2}`},
		{http.MethodPost, "/api/channel/", `{"mode":"single","channel":{"name":"denied","type":1,"key":"test-only","models":"gpt-test","group":"default"}}`},
		{http.MethodPut, "/api/channel/", fmt.Sprintf(`{"id":%d,"name":"denied","type":1,"status":2,"key":""}`, channel.Id)},
		{http.MethodGet, fmt.Sprintf("/api/channel/ztapi/test/%d", channel.Id), ""},
		{http.MethodGet, fmt.Sprintf("/api/channel/ztapi/fetch_models/%d", channel.Id), ""},
		{http.MethodPost, "/api/channel/multi_key/manage", fmt.Sprintf(`{"channel_id":%d,"action":"disable_key","key_index":0}`, channel.Id)},
	}

	for _, request := range requests {
		recorder := performChannelRequest(t, engine, request.method, request.target, reader, token, request.body, "")
		assertChannelRequestDenied(t, recorder)
	}
}

func TestMultiKeyStatusMasksEveryKeyForReadOnlyOperators(t *testing.T) {
	db, engine := setupChannelControllerTest(t)
	reader, token := createChannelOperator(t, db, "multi-key-reader", common.RoleSupportUser)
	channel := createTestChannel(t, db, "Multi-key target", 1, "task-seven-multi-key-one\ntask-seven-multi-key-two")
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = 2
	channel.ChannelInfo.MultiKeyMode = "random"
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{1: 3}
	channel.ChannelInfo.MultiKeyDisabledReason = map[int]string{1: "upstream body with bearer multi-key-disabled-secret"}
	if err := db.Model(&channel).Update("channel_info", channel.ChannelInfo).Error; err != nil {
		t.Fatalf("update multi-key metadata: %v", err)
	}

	recorder := performChannelRequest(
		t,
		engine,
		http.MethodPost,
		"/api/channel/multi_key/manage",
		reader,
		token,
		fmt.Sprintf(`{"channel_id":%d,"action":"get_key_status"}`, channel.Id),
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("multi-key status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "task-seven") || strings.Contains(body, "multi-key-disabled-secret") || strings.Contains(body, "upstream body") {
		t.Fatalf("multi-key response leaked key contents: %s", body)
	}
	if strings.Count(body, `"key_preview":"********"`) != 2 {
		t.Fatalf("multi-key response did not mask every key: %s", body)
	}
}

func setupChannelControllerTest(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", "ztapi-controller-test-master-key-0123456789")
	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLogDB := model.LOG_DB
	originalSQLite := common.UsingSQLite
	originalMySQL := common.UsingMySQL
	originalPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalGlobalRateLimit := common.GlobalApiRateLimitEnable
	originalCriticalRateLimit := common.CriticalRateLimitEnable
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.GlobalApiRateLimitEnable = false
	common.CriticalRateLimitEnable = false
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access sqlite: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}, &model.ZTAPIModelConfig{}, &model.Log{}); err != nil {
		t.Fatalf("migrate channel tables: %v", err)
	}

	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("channel-controller-test"))))
	engine.Use(middleware.RequestId())
	engine.GET("/__test/secure-verification", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set(middleware.SecureVerificationSessionKey, time.Now().Unix())
		if err := session.Save(); err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})
	router.SetApiRouter(engine)
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for gopool.WorkerCount() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if workers := gopool.WorkerCount(); workers != 0 {
			t.Errorf("audit workers still running during cleanup: %d", workers)
		}
		_ = sqlDB.Close()
		model.DB = originalDB
		model.LOG_DB = originalLogDB
		common.UsingSQLite = originalSQLite
		common.UsingMySQL = originalMySQL
		common.UsingPostgreSQL = originalPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.GlobalApiRateLimitEnable = originalGlobalRateLimit
		common.CriticalRateLimitEnable = originalCriticalRateLimit
	})
	return db, engine
}

func createChannelOperator(t *testing.T, db *gorm.DB, username string, role int) (model.User, string) {
	t.Helper()
	token := username + "-access-token"
	user := model.User{Username: username, Password: "not-used", Role: role, Status: common.UserStatusEnabled, AffCode: username + "-aff"}
	user.SetAccessToken(token)
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}
	return user, token
}

func createTestChannel(t *testing.T, db *gorm.DB, name string, channelType int, key string) model.Channel {
	t.Helper()
	baseURL := "https://example.invalid"
	weight := uint(1)
	priority := int64(0)
	autoBan := 1
	channel := model.Channel{Name: name, Type: channelType, Key: key, Status: common.ChannelStatusEnabled, BaseURL: &baseURL, Models: "gpt-test", Group: "default", Weight: &weight, Priority: &priority, AutoBan: &autoBan}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return channel
}

func establishChannelSecureVerification(t *testing.T, engine *gin.Engine, operator model.User, token string) string {
	t.Helper()
	recorder := performChannelRequest(t, engine, http.MethodGet, "/__test/secure-verification", operator, token, "", "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("establish verification status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("secure verification did not set a session cookie")
	}
	return cookies[0].Name + "=" + cookies[0].Value
}

func performChannelRequest(t *testing.T, engine *gin.Engine, method, target string, operator model.User, token, body, cookieValue string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("New-API-User", fmt.Sprintf("%d", operator.Id))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookieValue != "" {
		request.Header.Set("Cookie", cookieValue)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

func assertChannelRequestDenied(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	var response struct {
		Success bool `json:"success"`
	}
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode denied response: %v; body=%s", err, recorder.Body.String())
	}
	if response.Success {
		t.Fatalf("request unexpectedly succeeded: %s", recorder.Body.String())
	}
}

func assertNoStoreHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cacheControl := strings.ToLower(recorder.Header().Get("Cache-Control"))
	if !strings.Contains(cacheControl, "no-store") || !strings.Contains(cacheControl, "no-cache") {
		t.Fatalf("Cache-Control = %q, want no-store and no-cache", cacheControl)
	}
	if strings.ToLower(recorder.Header().Get("Pragma")) != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", recorder.Header().Get("Pragma"))
	}
}
