package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchFirstPartyRouter(t *testing.T) {
	initializeRouterRelayTestColumnNames(t)
	require.NoError(t, i18n.Init())
	db := setupZTAPIAuthRouterTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ZTAPIModelConfig{},
		&model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}, &model.ZTAPIHealthState{}, &model.ZTAPIWorkbenchTask{},
		&model.BalanceLedger{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}))
	t.Cleanup(model.InvalidateZTAPIAliasCache)
	master := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = master })
	t.Setenv("FRONTEND_BASE_URL", "https://frontend.invalid")
	seedZTAPIRouterUser(t, "workbench-pilot", "synthetic-password")
	seedZTAPIRouterUser(t, "workbench-other", "synthetic-password")
	var user model.User
	require.NoError(t, db.Where("username = ?", "workbench-pilot").Take(&user).Error)
	require.NoError(t, db.Model(&user).Updates(map[string]any{"group": "default", "quota": 265000}).Error)
	const gpt = "zt-gpt-5.5"
	const claude = "zt-claude-sonnet-5"
	pins := make([]WorkbenchAcceptanceModel, 0, 2)
	for i, pair := range [][2]string{{"gpt-5.5", gpt}, {"claude-sonnet-5", claude}, {"gpt-4.1", "zt-gpt-4.1"}} {
		channelID := 61 + i
		origin := "http://127.0.0.1:1"
		channel := model.Channel{Id: channelID, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled,
			Name: "fixture", Key: "synthetic", BaseURL: &origin, Models: pair[0], Group: "default"}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: pair[0], ChannelId: channelID, Enabled: true}).Error)
		seedWorkbenchRouterPublicModel(t, db, channelID, pair[0], pair[1])
		if i < 2 {
			pub, err := model.GetZTAPIRuntimePublication(pair[1])
			require.NoError(t, err, "fixture publication %s", pair[1])
			pins = append(pins, WorkbenchAcceptanceModel{Model: pair[1], Version: pub.Version, SnapshotID: pub.SnapshotID,
				ChannelID: channelID, UpstreamOrigin: origin, CredentialSHA256: common.HashZTAPIKey(channel.Key)})
		}
	}
	const manifest = "/.well-known/ztapi-workbench"
	const directory = "/api/workbench/models"
	const keyRoute = "/api/workbench/keys"
	const budgetRoute = "/__workbench_budget/reserve"
	const configEnv = "ZTAPI_WORKBENCH_PILOT_CONFIG"
	path := filepath.Join(t.TempDir(), "pilot.json")
	config := WorkbenchAcceptanceConfig{TaskLimitQuota: 250000, UserIDs: []int{user.Id}, Models: pins}
	encoded, err := common.Marshal(config)
	require.NoError(t, err)
	build := func(configPath string) *gin.Engine {
		t.Helper()
		t.Setenv(configEnv, configPath)
		engine := gin.New()
		SetRouter(engine, ThemeAssets{})
		return engine
	}
	do := func(engine *gin.Engine, method, route, body, token string) (int, string) {
		t.Helper()
		if token != "" {
			token = "Bearer " + token
		}
		rec := performZTAPIRouterRequest(engine, method, route, body, token, "127.0.0.1:1100")
		return rec.Code, rec.Body.String()
	}
	checkDisabled := func(t *testing.T, engine *gin.Engine) {
		t.Helper()
		for _, route := range engine.Routes() {
			require.NotContains(t, []string{manifest, directory, keyRoute, budgetRoute}, route.Path)
		}
		status, _ := do(engine, "GET", "/api/status", "", "")
		require.Equal(t, http.StatusOK, status)
	}
	checkDisabled(t, build(""))
	require.NoError(t, os.WriteFile(path, []byte(`{"unknown":true}`), 0600))
	checkDisabled(t, build(path))
	for _, settings := range []string{"", `"read_only":false,`, `"dispatch_enabled":false,`,
		`"read_only":true,"dispatch_enabled":true,`, `"drain_only":true,"dispatch_enabled":true,`,
		`"read_only":true,"drain_only":true,`, `"dispatch_enabled":true,"dispatch_enabled":false,`} {
		payload := append([]byte("{"+settings), encoded[1:]...)
		require.NoError(t, os.WriteFile(path, payload, 0600))
		checkDisabled(t, build(path))
	}
	for _, binding := range []struct {
		origin string
		digest string
	}{
		{"", ""},
		{"", pins[0].CredentialSHA256},
		{pins[0].UpstreamOrigin, "not-a-digest"},
		{"http://upstream.invalid", pins[0].CredentialSHA256},
		{"https://upstream.invalid?unexpected=1", pins[0].CredentialSHA256},
	} {
		broken := config
		broken.Models = append([]WorkbenchAcceptanceModel(nil), pins...)
		broken.Models[0].UpstreamOrigin = binding.origin
		broken.Models[0].CredentialSHA256 = binding.digest
		data, err := common.Marshal(workbenchPilotConfig{WorkbenchAcceptanceConfig: broken, DispatchEnabled: true})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0600))
		checkDisabled(t, build(path))
	}
	dispatchConfig := append([]byte(`{"dispatch_enabled":true,`), encoded[1:]...)
	require.NoError(t, os.WriteFile(path, dispatchConfig, 0600))
	require.NoError(t, db.Migrator().DropTable(&model.ZTAPIWorkbenchTask{}))
	checkDisabled(t, build(path))
	require.NoError(t, db.AutoMigrate(&model.ZTAPIWorkbenchTask{}))
	t.Run("partial-settlement-migration", func(t *testing.T) {
		require.NoError(t, db.Migrator().DropColumn(&model.ZTAPIRequestSettlement{}, "WorkbenchBodySHA256"))
		t.Cleanup(func() {
			require.NoError(t, db.Migrator().AddColumn(&model.ZTAPIRequestSettlement{}, "WorkbenchBodySHA256"))
		})
		checkDisabled(t, build(path))
	})
	engine := build(path)
	code, body := do(engine, "GET", manifest, "", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"account_routes":"workbench-v1"`)
	require.NotContains(t, body, `"mode":"acceptance"`)
	manifestResponse := performZTAPIRouterRequest(engine, "GET", manifest, "", "", "127.0.0.1:1100")
	require.Equal(t, "no-store", manifestResponse.Header().Get("Cache-Control"))
	login := func(name string) string {
		t.Helper()
		code, body := do(engine, "POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":"synthetic-password"}`, name), "")
		require.Equal(t, http.StatusOK, code, body)
		var result ztAPIRealRouterResponse
		require.NoError(t, common.Unmarshal([]byte(body), &result))
		require.NotEmpty(t, result.Data.AccessToken)
		return result.Data.AccessToken
	}
	access, other := login("workbench-pilot"), login("workbench-other")
	code, _ = do(engine, "GET", directory, "", "")
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = do(engine, "GET", directory, "", other)
	require.Equal(t, http.StatusForbidden, code)
	code, body = do(engine, "GET", directory, "", access)
	require.Equal(t, http.StatusOK, code)
	for _, name := range []string{gpt, claude} {
		require.Contains(t, body, name)
	}
	require.Contains(t, body, `"project_tools":true`)
	for _, secret := range []string{"zt-gpt-4.1", "channel_id", "snapshot_id", "synthetic"} {
		require.NotContains(t, body, secret)
	}
	policy := func(id string) string {
		return fmt.Sprintf(`{"name":%q,"status":1,"expired_time":%d,"remain_quota":2000,"unlimited_quota":false,"model_limits_enabled":true,"model_limits":%q,"group":"","cross_group_retry":false}`,
			"workbench-r37-"+uuid.NewString(), time.Now().Add(20*time.Minute).Unix(), id)
	}
	readOnlyConfig := append([]byte(`{"read_only":true,`), encoded[1:]...)
	require.NoError(t, os.WriteFile(path, readOnlyConfig, 0600))
	readOnly := build(path)
	code, body = do(readOnly, "GET", manifest, "", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"dispatch":"locked"`)
	require.Contains(t, body, `"auth":"enabled"`)
	code, body = do(readOnly, "POST", "/api/auth/login", `{"username":"workbench-pilot","password":"synthetic-password"}`, "")
	require.Equal(t, http.StatusOK, code)
	var readOnlyLogin ztAPIRealRouterResponse
	require.NoError(t, common.Unmarshal([]byte(body), &readOnlyLogin))
	readAccess := readOnlyLogin.Data.AccessToken
	code, body = do(readOnly, "GET", "/api/workbench/self", "", readAccess)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"quota":265000`)
	code, body = do(readOnly, "GET", directory, "", readAccess)
	require.Equal(t, http.StatusOK, code)
	for _, name := range []string{gpt, claude} {
		require.Contains(t, body, name)
	}
	require.Contains(t, body, `"project_tools":false`)
	require.NotContains(t, body, `"project_tools":true`)
	require.NotContains(t, body, "zt-gpt-4.1")
	code, _ = do(readOnly, "GET", directory, "", other)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(readOnly, "POST", keyRoute, policy(gpt), readAccess)
	require.Equal(t, http.StatusServiceUnavailable, code)
	for _, route := range []string{budgetRoute, "/__workbench_budget/responses", "/__workbench_budget/release"} {
		code, _ = do(readOnly, "POST", route, "{}", readAccess)
		require.Equal(t, http.StatusServiceUnavailable, code)
	}
	t.Run("client-read-only", func(t *testing.T) {
		probePath := os.Getenv("R37_READONLY_ACCOUNT_PROBE")
		if probePath == "" {
			t.Skip("requires the desktop read-only account fixture")
		}
		api := httptest.NewServer(readOnly)
		t.Cleanup(api.Close)
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		node := "node"
		if app := os.Getenv("R37_QA_APP_ROOT"); app != "" && runtime.GOOS == "windows" {
			node = filepath.Join(app, "bin", "runtime", "node.exe")
		}
		probe := exec.CommandContext(ctx, node, probePath)
		probe.Env = append(probe.Environ(), "R37_READONLY_LOOPBACK="+api.URL, "NODE_OPTIONS=")
		output, err := probe.CombinedOutput()
		require.NoError(t, err, "%s", output)
		require.Equal(t, "R37_READONLY_PACKAGED_ACCOUNT_OK", strings.TrimSpace(string(output)))
	})
	for _, table := range []any{&model.Token{}, &model.ZTAPIRequestSettlement{}, &model.BalanceLedger{}} {
		var rows int64
		require.NoError(t, db.Model(table).Count(&rows).Error)
		require.Zero(t, rows)
	}
	code, _ = do(engine, "POST", keyRoute, policy(gpt), other)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(engine, "POST", keyRoute, policy("zt-gpt-4.1"), access)
	require.Equal(t, http.StatusForbidden, code)
	code, body = do(engine, "POST", keyRoute, policy(gpt), access)
	require.Equal(t, http.StatusOK, code, body)
	var issued struct {
		Data struct {
			ID  int
			Key string
		}
	}
	require.NoError(t, common.Unmarshal([]byte(body), &issued))
	require.True(t, common.IsValidZTAPIWorkbenchKey(issued.Data.Key))
	keyPath := fmt.Sprintf("%s/%d", keyRoute, issued.Data.ID)
	code, _ = do(engine, "GET", keyPath, "", other)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(engine, "GET", keyPath, "", access)
	require.Equal(t, http.StatusOK, code)
	code, _ = do(engine, "POST", "/v1/responses", "{}", issued.Data.Key)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(engine, "POST", budgetRoute, "{}", issued.Data.Key)
	require.Equal(t, http.StatusBadRequest, code)
	task, unused, dispatched := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, hold := range []string{unused, dispatched} {
		_, err := model.BeginZTAPIWorkbenchBatch(task, config.TaskLimitQuota, []model.ZTAPIWorkbenchLeg{{
			Stage: "main", Native: &model.ZTAPIWorkbenchNativeBounds{BodySHA256: common.HashZTAPIKey("fixture"), OutboundBytes: 1024, MaxOutput: 64},
			Settlement: model.ZTAPIRequestSettlement{OperationID: hold, RequestID: hold, UserID: user.Id, TokenID: issued.Data.ID,
				PublicModel: gpt, PriceSnapshotJSON: "{}", ReservedQuota: 100},
		}})
		require.NoError(t, err)
	}
	require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Where("operation_id = ?", dispatched).Update("dispatched", true).Error)
	drainConfig := append([]byte(`{"drain_only":true,`), encoded[1:]...)
	require.NoError(t, os.WriteFile(path, drainConfig, 0600))
	drain := build(path)
	code, body = do(drain, "GET", manifest, "", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"dispatch":"locked"`)
	code, _ = do(drain, "POST", keyRoute, policy(gpt), access)
	require.Equal(t, http.StatusServiceUnavailable, code)
	code, body = do(drain, "GET", directory, "", access)
	require.Equal(t, http.StatusOK, code)
	require.JSONEq(t, `{"success":true,"data":[],"catalog":[]}`, body)
	code, _ = do(drain, "GET", "/api/status", "", "")
	require.Equal(t, http.StatusOK, code)
	for _, route := range []string{budgetRoute, "/__workbench_budget/responses"} {
		code, _ = do(drain, "POST", route, "{}", issued.Data.Key)
		require.Equal(t, http.StatusServiceUnavailable, code)
	}
	release := func(hold, key string) int {
		code, _ := do(drain, "POST", "/__workbench_budget/release", fmt.Sprintf(`{"task":%q,"hold":%q}`, task, hold), key)
		return code
	}
	require.Equal(t, http.StatusUnauthorized, release(unused, ""))
	require.Equal(t, http.StatusConflict, release(dispatched, issued.Data.Key))
	require.Equal(t, http.StatusOK, release(unused, issued.Data.Key))
	require.Equal(t, http.StatusOK, release(unused, issued.Data.Key))
	var account model.User
	require.NoError(t, db.First(&account, user.Id).Error)
	require.Equal(t, 264900, account.Quota)
	var ledger []model.BalanceLedger
	require.NoError(t, db.Where("request_id = ?", unused).Order("id").Find(&ledger).Error)
	require.Len(t, ledger, 2)
	require.Equal(t, model.BalanceLedgerSourceUsageReservation, ledger[0].SourceType)
	require.Equal(t, model.BalanceLedgerSourceBillingRefund, ledger[1].SourceType)
	var held model.ZTAPIRequestSettlement
	require.NoError(t, db.Where("operation_id = ?", dispatched).Take(&held).Error)
	require.True(t, held.Dispatched)
	require.Equal(t, model.ZTAPISettlementReserved, held.Status)
	require.NoError(t, db.Model(&model.ZTAPIModelConfig{}).Where("public_name = ?", gpt).Update("version", pins[0].Version+1).Error)
	model.InvalidateZTAPIAliasCache()
	code, body = do(engine, "GET", directory, "", access)
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, body, gpt)
	code, _ = do(engine, "POST", budgetRoute, "{}", issued.Data.Key)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(drain, "DELETE", keyPath, "", access)
	require.Equal(t, http.StatusOK, code)
	code, _ = do(engine, "POST", budgetRoute, "{}", issued.Data.Key)
	require.Equal(t, http.StatusUnauthorized, code)
	checkDisabled(t, build(""))
}
