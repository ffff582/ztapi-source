package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchAcceptanceSelectedChannelRejectsDrift(t *testing.T) {
	const origin = "https://approved-provider.invalid"
	const key = "synthetic-provider-credential"
	const public = "zt-fixture"
	p := workbenchAcceptancePolicy{models: map[string]WorkbenchAcceptanceModel{public: {
		ChannelID: 41, Version: 1, UpstreamOrigin: origin, CredentialSHA256: common.HashZTAPIKey(key),
	}}}
	for _, tc := range []struct {
		name, base, credential, proxy string
		multi                         bool
		want                          int
	}{
		{"approved", origin, key, "", false, 204},
		{"changed-origin", "https://other.invalid", key, "", false, 403},
		{"changed-credential", origin, "different-credential", "", false, 403},
		{"proxy", origin, key, "http://proxy.invalid", false, 403},
		{"pool", origin, key, "", true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.POST("/", func(c *gin.Context) {
				relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{PublicName: public, Version: 1, AllowedChannelIDs: []int{41}})
				common.SetContextKey(c, constant.ContextKeyChannelId, 41)
				common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, tc.base)
				common.SetContextKey(c, constant.ContextKeyChannelKey, tc.credential)
				common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, tc.multi)
				common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{Proxy: tc.proxy})
			}, p.selectedChannelGate, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
			require.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestZTAPIWorkbenchAcceptanceCatalogAndDispatchPolicy(t *testing.T) {
	initializeRouterRelayTestColumnNames(t)
	require.NoError(t, i18n.Init())
	db := setupZTAPIAuthRouterTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ZTAPIModelConfig{},
		&model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}, &model.ZTAPIHealthState{}))
	t.Cleanup(model.InvalidateZTAPIAliasCache)
	seedZTAPIRouterUser(t, "r37-qa", "synthetic-password")
	var user model.User
	require.NoError(t, db.Where("username = ?", "r37-qa").Take(&user).Error)
	require.NoError(t, db.Model(&user).Updates(map[string]any{"group": "default", "quota": 265000}).Error)
	seedZTAPIRouterUser(t, "r37-other", "synthetic-password")
	const gpt = "zt-gpt-5.5"
	const claude = "zt-claude-sonnet-5"
	for i, pair := range [][2]string{{"gpt-5.5", gpt}, {"claude-sonnet-5", claude}} {
		channelID := 41 + i
		origin := "http://127.0.0.1:1"
		channel := model.Channel{Id: channelID, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled,
			Name: "fixture", Key: "synthetic", BaseURL: &origin, Models: pair[0], Group: "default"}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: pair[0], ChannelId: channelID, Enabled: true}).Error)
		seedRouterPublicModel(t, db, channelID, pair[0], pair[1])
	}
	pub, err := model.GetZTAPIRuntimePublication(gpt)
	require.NoError(t, err)
	engine := gin.New()
	// Auth-only public route fixtures; never connect to a provider.
	engine.POST("/v1/responses", middleware.TokenAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	engine.GET("/fixture/token-usage", middleware.TokenAuthReadOnly(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	engine.POST("/fixture/tokens", middleware.ZTAPIUserAuth(), controller.AddToken)
	engine.PUT("/fixture/tokens", middleware.ZTAPIUserAuth(), controller.UpdateToken)
	engine.POST("/fixture/tokens/:id/regenerate", middleware.ZTAPIUserAuth(), controller.RegenerateTokenKey)
	require.NoError(t, MountZTAPIWorkbenchAcceptance(engine, WorkbenchAcceptanceConfig{
		TaskLimitQuota: 500000, UserIDs: []int{user.Id}, Models: []WorkbenchAcceptanceModel{
			{Model: gpt, Version: pub.Version, SnapshotID: pub.SnapshotID, ChannelID: 41},
		},
	}))
	do := func(method, route, body, token string) (int, string) {
		t.Helper()
		auth := ""
		if token != "" {
			auth = "Bearer " + token
		}
		rec := performZTAPIRouterRequest(engine, method, route, body, auth, "127.0.0.1:1100")
		return rec.Code, rec.Body.String()
	}
	login := func(name string) string {
		t.Helper()
		status, body := do("POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":"synthetic-password"}`, name), "")
		require.Equal(t, http.StatusOK, status)
		var result ztAPIRealRouterResponse
		require.NoError(t, common.Unmarshal([]byte(body), &result))
		require.NotEmpty(t, result.Data.AccessToken)
		return result.Data.AccessToken
	}
	access := login("r37-qa")
	code, body := do("GET", "/api/user/self", "", access)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"quota":265000`)
	require.NotContains(t, body, "email")
	code, body = do("GET", "/api/user/models", "", access)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, gpt)
	require.NotContains(t, body, claude)
	require.Contains(t, body, `"project_tools":true`)
	require.NotContains(t, body, "channel")
	code, _ = do("GET", "/api/user/models", "", login("r37-other"))
	require.Equal(t, http.StatusForbidden, code)
	for _, route := range []string{"/api/status", "/api/user/topup", "/v1/models", "/v1/responses"} {
		code, _ = do("GET", route, "", access)
		require.Equal(t, http.StatusNotFound, code)
	}
	secret, hash, prefix, err := common.GenerateZTAPIKey()
	require.NoError(t, err)
	key := model.Token{UserId: user.Id, KeyHash: hash, KeyPrefix: prefix, Status: common.TokenStatusEnabled, RemainQuota: user.Quota,
		ExpiredTime: time.Now().Add(10 * time.Minute).Unix(), ModelLimitsEnabled: true, ModelLimits: claude}
	require.NoError(t, db.Create(&key).Error)
	for _, route := range []string{"/__workbench_budget/reserve", "/__workbench_budget/responses"} {
		code, _ = do("POST", route, "{}", secret)
		require.Equal(t, http.StatusForbidden, code)
	}
	code, _ = do("POST", "/api/token/", fmt.Sprintf(`{"name":"wrong-model","remain_quota":10,"expired_time":%d,"model_limits_enabled":true,"model_limits":%q}`,
		time.Now().Add(10*time.Minute).Unix(), claude), access)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do("GET", "/api/token/"+fmt.Sprint(key.Id), "", access)
	require.Equal(t, http.StatusForbidden, code)

	name := "workbench-r37-" + uuid.NewString()
	expiry := time.Now().Add(20 * time.Minute).Unix()
	validPolicy := fmt.Sprintf(`{"name":%q,"status":1,"expired_time":%d,"remain_quota":2000,"unlimited_quota":false,"model_limits_enabled":true,"model_limits":%q,"group":"","cross_group_retry":false}`, name, expiry, gpt)
	code, body = do("POST", "/fixture/tokens", strings.TrimSuffix(validPolicy, "}")+`,"workbench_only":true}`, access)
	require.Equal(t, http.StatusOK, code)
	var ordinary struct{ Data struct{ Key string } }
	require.NoError(t, common.Unmarshal([]byte(body), &ordinary))
	require.NotEmpty(t, ordinary.Data.Key)
	code, _ = do("POST", "/v1/responses", "{}", ordinary.Data.Key)
	require.Equal(t, http.StatusNoContent, code)
	code, _ = do("POST", "/__workbench_budget/reserve", "{}", ordinary.Data.Key)
	require.Equal(t, http.StatusForbidden, code)
	code, body = do("POST", "/api/token/", validPolicy, access)
	require.Equal(t, http.StatusOK, code)
	var issued struct {
		Success bool
		Data    struct {
			ID  int
			Key string
		}
	}
	require.NoError(t, common.Unmarshal([]byte(body), &issued))
	require.True(t, issued.Success, body)
	require.Positive(t, issued.Data.ID)
	require.NotEmpty(t, issued.Data.Key)
	require.False(t, common.IsValidZTAPIKey(issued.Data.Key), "older nodes must reject workbench credentials during rollback")
	keyPath := "/api/token/" + fmt.Sprint(issued.Data.ID)
	code, _ = do("POST", "/v1/responses", "{}", issued.Data.Key)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do("GET", "/fixture/token-usage", "", issued.Data.Key)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do("PUT", "/fixture/tokens", fmt.Sprintf(`{"id":%d,"name":"changed","unlimited_quota":true}`, issued.Data.ID), access)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do("POST", fmt.Sprintf("/fixture/tokens/%d/regenerate", issued.Data.ID), "{}", access)
	require.Equal(t, http.StatusForbidden, code)
	code, body = do("GET", keyPath, "", access)
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, body, issued.Data.Key)
	require.Contains(t, body, name)
	code, _ = do("POST", "/__workbench_budget/reserve", "{}", issued.Data.Key)
	require.Equal(t, http.StatusBadRequest, code)
	require.NoError(t, db.Model(&model.ZTAPIModelConfig{}).Where("id = ?", pub.ModelConfigID).Update("version", pub.Version+1).Error)
	model.InvalidateZTAPIAliasCache()
	code, body = do("GET", "/api/user/models", "", access)
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, body, gpt)
	code, _ = do("POST", "/__workbench_budget/reserve", "{}", issued.Data.Key)
	require.Equal(t, http.StatusForbidden, code)
	code, body = do("DELETE", keyPath, "", access)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"success":true`)
	code, _ = do("POST", "/__workbench_budget/reserve", "{}", issued.Data.Key)
	require.Equal(t, http.StatusUnauthorized, code)

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"r37-qa","password":"synthetic-password"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	engine.ServeHTTP(loginRecorder, loginRequest)
	require.Equal(t, http.StatusOK, loginRecorder.Code)
	var refresh *http.Cookie
	for _, candidate := range loginRecorder.Result().Cookies() {
		if candidate.Name == "ztapi_refresh" {
			refresh = candidate
		}
	}
	require.NotNil(t, refresh)
	withRefresh := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(refresh)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, http.StatusOK, withRefresh("/api/auth/logout").Code)
	require.Equal(t, http.StatusUnauthorized, withRefresh("/api/auth/refresh").Code)
}
