package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Check the configured supplier identity before forwarding to a local fixture.
type workbenchXcloudLoopback struct {
	target *url.URL
	local  partialImageLoopbackTransport
	path   string
}

func (f workbenchXcloudLoopback) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.URL.String() != "https://www.xcloudtoken.com"+f.path {
		f.local.blocked.Add(1)
		return nil, errors.New("unexpected Xcloud loopback test destination")
	}
	copyReq := req.Clone(req.Context())
	copyURL := *req.URL
	copyURL.Scheme, copyURL.Host = f.target.Scheme, f.target.Host
	copyReq.URL, copyReq.Host = &copyURL, f.target.Host
	resp, err := f.local.RoundTrip(copyReq)
	if resp != nil {
		// Preserve the virtual supplier identity while all HTTP stays on loopback.
		resp.Request = req
	}
	return resp, err
}

func TestZTAPIWorkbenchNativeRelayLoopback(t *testing.T) {
	t.Run("native", func(t *testing.T) { testZTAPIWorkbenchRelayLoopback(t, "gpt-5.5", "/v1/responses", "") })
	t.Run("claude", func(t *testing.T) {
		testZTAPIWorkbenchRelayLoopback(t, "claude-sonnet-5", "/v1/chat/completions", "")
	})
	t.Run("acceptance-native", func(t *testing.T) { testZTAPIWorkbenchRelayLoopback(t, "gpt-5.5", "/v1/responses", "acceptance") })
	t.Run("acceptance-yunxin-hub", func(t *testing.T) {
		testZTAPIWorkbenchRelayLoopback(t, "gpt-6.1-sol", "/hub/v1/responses", "acceptance")
	})
	t.Run("acceptance-claude", func(t *testing.T) {
		testZTAPIWorkbenchRelayLoopback(t, "claude-sonnet-5", "/v1/chat/completions", "acceptance")
	})
	t.Run("first-party-native", func(t *testing.T) {
		testZTAPIWorkbenchRelayLoopback(t, "gpt-5.5", "/v1/responses", "first-party")
	})
	t.Run("first-party-claude", func(t *testing.T) {
		testZTAPIWorkbenchRelayLoopback(t, "claude-sonnet-5", "/v1/chat/completions", "first-party")
	})
}

func TestZTAPIRc10NativeTaskProof(t *testing.T) {
	for _, tc := range []struct{ source, path string }{
		{"claude-sonnet-5", "/v1/chat/completions"},
		{"claude-fable-5", "/v1/chat/completions"},
		{"glm-5.3", "/v1/responses"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			testZTAPIWorkbenchRelayLoopback(t, tc.source, tc.path, "rc10-proof")
		})
	}
}

func TestZTAPIRc10FirstPartyNativeTaskProof(t *testing.T) {
	for _, tc := range []struct{ source, path string }{
		{"claude-sonnet-5", "/v1/chat/completions"},
		{"claude-fable-5", "/v1/chat/completions"},
		{"glm-5.3", "/v1/responses"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			if tc.source == "claude-fable-5" {
				for _, scenario := range []string{"short", "long"} {
					t.Run(scenario, func(t *testing.T) {
						testZTAPIWorkbenchRelayLoopback(t, tc.source, tc.path, "rc10-first-party-proof-"+scenario)
					})
				}
				return
			}
			testZTAPIWorkbenchRelayLoopback(t, tc.source, tc.path, "rc10-first-party-proof")
		})
	}
}

func TestZTAPIWorkbenchFablePilotHardCap(t *testing.T) {
	t.Run("wallet-config", verifyWorkbenchFablePilotUsesZTAPIWalletLimit)
	for _, mode := range []string{"pilot-count", "pilot-cash", "pilot-missing", "pilot-expired"} {
		t.Run(mode, func(t *testing.T) {
			testZTAPIWorkbenchRelayLoopback(t, "claude-fable-5", "/v1/chat/completions", mode)
		})
	}
}

func testZTAPIWorkbenchRelayLoopback(t *testing.T, source, upstreamPath, mode string) {
	firstPartyProof := strings.HasPrefix(mode, "rc10-first-party-proof")
	proofScenario := strings.TrimPrefix(mode, "rc10-first-party-proof-")
	if proofScenario == mode {
		proofScenario = ""
	}
	xcloudReceipt := firstPartyProof && source == "claude-fable-5" && proofScenario == "long"
	supplierID := func(hit int32) string {
		if xcloudReceipt {
			return fmt.Sprintf("%027dAaBbCcDdEeFf", hit)
		}
		return fmt.Sprintf("resp_r36_loopback_%d", hit)
	}
	rc10Proof := firstPartyProof || mode == "rc10-proof"
	pilotMode := ""
	if strings.HasPrefix(mode, "pilot-") {
		pilotMode = mode
	}
	if firstPartyProof || pilotMode != "" {
		mode = "first-party"
	} else if rc10Proof {
		mode = "acceptance"
	}
	accountFlow := mode != ""
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	t.Setenv("ZTAPI_HOSTED_SEARCH_ENABLED", "0")
	translate := common.TranslateMessage
	require.NoError(t, i18n.Init())
	t.Cleanup(func() { common.TranslateMessage = translate })
	initializeRouterRelayTestColumnNames(t)
	db := setupZTAPIAuthRouterTestDB(t)
	if source == "gpt-6.1-sol" {
		raw := os.Getenv("ZTAPI_REDIS_TEST_URL")
		if raw == "" {
			if os.Getenv("ZTAPI_REQUIRE_REDIS_INTEGRATION") == "true" {
				t.Fatal("a dedicated Redis integration service is required")
			}
			t.Skip("dedicated Redis integration service not configured")
		}
		opts, err := redis.ParseURL(raw)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(opts.Addr, "127.0.0.1:"))
		require.Equal(t, 13, opts.DB)
		require.NotEmpty(t, opts.Password)
		cache := redis.NewClient(opts)
		oldClient, oldEnabled := common.RDB, common.RedisEnabled
		t.Cleanup(func() {
			common.RDB, common.RedisEnabled = oldClient, oldEnabled
			require.NoError(t, cache.Close())
		})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, cache.Ping(ctx).Err())
		common.RDB, common.RedisEnabled = cache, true
	}
	oldRate, oldBatch, oldQuota := setting.ModelRequestRateLimitEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit
	setting.ModelRequestRateLimitEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = false, false, 500000
	t.Cleanup(func() {
		setting.ModelRequestRateLimitEnabled, common.BatchUpdateEnabled, common.QuotaPerUnit = oldRate, oldBatch, oldQuota
		model.InvalidateZTAPIAliasCache()
	})
	ratio_setting.InitRatioSettings()
	service.InitTokenEncoders()
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.BalanceLedger{}, &model.BillingRefundPending{},
		&model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIWorkbenchTask{},
		&model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}, &model.ZTAPIHealthState{}))
	require.NoError(t, model.MigrateZTAPISupplierRefund(db))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	require.NoError(t, model.MigrateZTAPISettlementLogOutbox(db))
	require.NoError(t, model.MigrateLogDBZTAPISettlementLogs(db))
	require.NoError(t, model.MigrateZTAPIFinanceAlerts(db))
	public := "zt-" + source
	supplierModel, upstreamPrefix, channelID := source, "", 47
	if upstreamPath == "/hub/v1/responses" {
		supplierModel, upstreamPrefix, channelID = "gpt-6.1-sol-hc", "/hub", 2
	}
	if source == "glm-5.3" {
		channelID = 8
	}
	chat := strings.HasSuffix(upstreamPath, "/chat/completions")
	var hits, blocked atomic.Int32
	type outboundSize struct{ Bytes, MaxOutput int }
	var measured sync.Map
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := hits.Add(1)
		b, err := io.ReadAll(r.Body)
		var request struct {
			Model          string
			MaxOutput      int `json:"max_output_tokens"`
			MaxTokens      int `json:"max_tokens"`
			MaxCompletions int `json:"max_completion_tokens"`
		}
		if err != nil || r.Method != "POST" || r.URL.Path != upstreamPath || json.Unmarshal(b, &request) != nil || request.Model != supplierModel {
			t.Errorf("unexpected loopback request: %s %s: %v", r.Method, r.URL.Path, err)
			w.WriteHeader(400)
			return
		}
		id := supplierID(hit)
		measured.Store(id, outboundSize{len(b), max(request.MaxOutput, request.MaxTokens, request.MaxCompletions)})
		w.Header().Set("Content-Type", "text/event-stream")
		if xcloudReceipt {
			w.Header().Set(common.RequestIdKey, id)
		} else {
			w.Header().Set("X-Request-ID", id)
		}
		wire := string(b)
		if strings.Contains(wire, "r37-flow-fixture") || strings.Contains(wire, "r37-child-fixture") ||
			strings.Contains(wire, "This is a small synthetic addition bug.") || strings.Contains(wire, "r37-short-child-") ||
			strings.Contains(wire, "This is a long synthetic addition task.") {
			writeR37FlowFixture(t, w, b, id, supplierModel, strings.TrimPrefix(upstreamPath, upstreamPrefix))
			return
		}
		if strings.Contains(wire, "r36-tool-fixture") {
			if !strings.Contains(wire, `"name":"read_fixture"`) || !strings.Contains(wire, `"tools"`) {
				t.Errorf("missing synthetic tool declaration")
				w.WriteHeader(400)
				return
			}
			if !strings.Contains(wire, "r36 local fixture contents") {
				if chat {
					_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_r36_fixture\",\"type\":\"function\",\"function\":{\"name\":\"read_fixture\",\"arguments\":\"{}\"}}]}}]}\n\ndata: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\ndata: [DONE]\n\n", id, source, id, source)
				} else {
					_, _ = io.WriteString(w, "event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"{}\",\"name\":\"read_fixture\",\"item_id\":\"fc_fixture\",\"output_index\":0}\n\n")
					_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"model\":%q,\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_fixture\",\"call_id\":\"call_r36_fixture\",\"name\":\"read_fixture\",\"arguments\":\"{}\",\"status\":\"completed\"}],\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15}}}\n\n", id, source)
				}
				return
			}
			wantHistory := `"type":"function_call_output"`
			if chat {
				wantHistory = `"role":"tool"`
			}
			if !strings.Contains(wire, wantHistory) || !strings.Contains(wire, "call_r36_fixture") {
				t.Errorf("unexpected synthetic tool history")
				w.WriteHeader(400)
				return
			}
		}
		if chat {
			_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"test\"}}]}\n\n", id, source)
			w.(http.Flusher).Flush()
			if strings.Contains(string(b), "truncate") {
				panic(http.ErrAbortHandler)
			}
			_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\ndata: [DONE]\n\n", id, source)
			return
		}
		_, _ = fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":%q,\"model\":%q,\"status\":\"in_progress\"}}\n\n", id, source)
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"test\"}\n\n")
		w.(http.Flusher).Flush()
		if strings.Contains(string(b), "truncate") {
			panic(http.ErrAbortHandler)
		}
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"model\":%q,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15}}}\n\n", id, source)
	}))
	t.Cleanup(upstream.Close)
	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	httpClient := service.GetHttpClient()
	oldTransport := httpClient.Transport
	transport := &http.Transport{Proxy: nil}
	guard := partialImageLoopbackTransport{url: upstream.URL, base: transport, blocked: &blocked}
	httpClient.Transport = guard
	if source == "glm-5.3" || xcloudReceipt {
		target, err := url.Parse(upstream.URL)
		require.NoError(t, err)
		httpClient.Transport = workbenchXcloudLoopback{target: target, local: guard, path: upstreamPath}
	}
	t.Cleanup(func() { httpClient.Transport = oldTransport; transport.CloseIdleConnections() })
	user := model.User{Username: "r36-native", AffCode: "r36-native", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Group: "default", Quota: 1000000}
	if accountFlow {
		var err error
		user.Password, err = service.HashZTAPIPassword("synthetic-password")
		require.NoError(t, err)
	}
	require.NoError(t, db.Create(&user).Error)
	secret, hash, prefix, err := common.GenerateZTAPIWorkbenchKey()
	require.NoError(t, err)
	key := model.Token{UserId: user.Id, KeyHash: hash, KeyPrefix: prefix, Status: common.TokenStatusEnabled, RemainQuota: user.Quota,
		Group: "default", ExpiredTime: time.Now().Add(10 * time.Minute).Unix(), ModelLimitsEnabled: true, ModelLimits: public, WorkbenchOnly: true}
	initialKeyQuota := user.Quota
	if accountFlow {
		initialKeyQuota = workbenchAcceptanceKeyLimit
		key.Name, key.Group, key.RemainQuota = "workbench-r37-"+uuid.NewString(), "", initialKeyQuota
	}
	require.NoError(t, db.Create(&key).Error)
	baseURL := upstream.URL + upstreamPrefix
	if source == "glm-5.3" || xcloudReceipt {
		baseURL = "https://www.xcloudtoken.com"
	}
	channel := model.Channel{Id: channelID, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "r36-loopback-only", Key: "synthetic-not-a-provider-key", BaseURL: &baseURL, Models: source, Group: "default"}
	if supplierModel != source {
		mapping, err := json.Marshal(map[string]string{source: supplierModel})
		require.NoError(t, err)
		value := string(mapping)
		channel.ModelMapping = &value
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: source, ChannelId: channel.Id, Enabled: true, Weight: 1}).Error)
	var extraChannels []int
	if mode == "first-party" && source == "claude-sonnet-5" {
		fallback := "https://fallback.invalid"
		require.NoError(t, db.Create(&model.Channel{Id: 48, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled,
			Name: "unexpected-fallback", Key: "synthetic-fallback", BaseURL: &fallback, Models: source, Group: "default"}).Error)
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: source, ChannelId: 48, Enabled: true, Weight: 1000}).Error)
		extraChannels = []int{48}
	}
	seedWorkbenchRouterPublicModel(t, db, channel.Id, source, public, extraChannels...)
	if source == "claude-fable-5" {
		pub, err := model.GetZTAPIRuntimePublication(public)
		require.NoError(t, err)
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
		middleware.PinZTAPIWorkbenchPublication(ctx, public, pub.Version, pub.SnapshotID, channel.Id)
		snapshot, err := middleware.LoadZTAPIWorkbenchPublication(ctx, public)
		require.NoError(t, err)
		quote, err := service.ZTAPIWorkbenchQuote(snapshot, 2*44373+512, 2048)
		require.NoError(t, err)
		require.Greater(t, quote, int64(workbenchAcceptanceKeyLimit))
		t.Logf("Fable first denied wire: 44373 bytes, output cap 2048, reserve quota %d, task and key cap %d", quote, workbenchAcceptanceKeyLimit)
	}
	if len(extraChannels) > 0 {
		pub, err := model.GetZTAPIRuntimePublication(public)
		require.NoError(t, err)
		require.ElementsMatch(t, []int{47, 48}, pub.AllowedChannelIDs)
		t.Cleanup(func() {
			pub, err := model.GetZTAPIRuntimePublication(public)
			require.NoError(t, err)
			require.ElementsMatch(t, []int{47, 48}, pub.AllowedChannelIDs, "workbench must not modify the ordinary publication")
		})
	}

	engine := gin.New()
	engine.Use(middleware.RequestId(), middleware.I18n(), middleware.BodyStorageCleanup())
	var pilotConfig *workbenchFablePilotCap
	if accountFlow {
		var fixtureWalletMu sync.Mutex
		var drained int64
		engine.POST("/fixture-wallet/:action", func(c *gin.Context) {
			fixtureWalletMu.Lock()
			defer fixtureWalletMu.Unlock()
			var current model.User
			if err := db.First(&current, user.Id).Error; err != nil {
				c.Status(http.StatusInternalServerError)
				return
			}
			action := c.Param("action")
			var delta int64
			switch {
			case action == "drain" && drained == 0 && current.Quota > 0:
				drained = int64(current.Quota)
				delta = -drained
			case action == "restore" && drained > 0 && current.Quota == 0:
				delta, drained = drained, 0
			default:
				c.Status(http.StatusConflict)
				return
			}
			if _, _, err := model.AdjustUserBalance(model.BalanceAdjustment{UserID: user.Id, Delta: delta, Reason: "isolated budget fixture",
				IdempotencyKey: "r37-wallet-fixture-" + action, SourceType: model.BalanceLedgerSourceAdmin}); err != nil {
				c.Status(http.StatusInternalServerError)
				return
			}
			c.Status(http.StatusNoContent)
		})
		pub, err := model.GetZTAPIRuntimePublication(public)
		require.NoError(t, err)
		config := WorkbenchAcceptanceConfig{
			TaskLimitQuota: 500000, UserIDs: []int{user.Id}, Models: []WorkbenchAcceptanceModel{
				{Model: public, Version: pub.Version, SnapshotID: pub.SnapshotID, ChannelID: channel.Id},
			},
		}
		if mode == "first-party" {
			config.Models[0].UpstreamOrigin = baseURL
			config.Models[0].CredentialSHA256 = common.HashZTAPIKey(channel.Key)
			if source == "claude-fable-5" && pilotMode != "pilot-missing" {
				pilotConfig = &workbenchFablePilotCap{
					BatchID: uuid.NewString(), ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
					BudgetBasis: "ztapi_wallet", MaxRequests: 14, BudgetNanoU: 3_000_000_000,
				}
				if pilotMode == "pilot-cash" {
					pilotConfig.BudgetNanoU = 1_000_000
				}
				if pilotMode == "pilot-expired" {
					pilotConfig.ExpiresAt = time.Now().Add(-time.Second).Unix()
				}
				require.NoError(t, db.AutoMigrate(&model.ZTAPIWorkbenchPilotSpend{}))
			}
			encoded, err := common.Marshal(workbenchPilotConfig{WorkbenchAcceptanceConfig: config, DispatchEnabled: true, FableCap: pilotConfig})
			require.NoError(t, err)
			configPath := filepath.Join(t.TempDir(), "pilot.json")
			require.NoError(t, os.WriteFile(configPath, encoded, 0600))
			t.Setenv(workbenchPilotConfigEnv, configPath)
			master := common.IsMasterNode
			common.IsMasterNode = false
			t.Cleanup(func() { common.IsMasterNode = master })
			t.Setenv("FRONTEND_BASE_URL", "https://frontend.invalid")
			SetRouter(engine, ThemeAssets{})
			if pilotMode == "pilot-missing" || pilotMode == "pilot-expired" {
				for _, route := range engine.Routes() {
					require.NotEqual(t, "/__workbench_budget/responses", route.Path)
				}
				require.Zero(t, hits.Load())
				return
			}
		} else {
			require.NoError(t, MountZTAPIWorkbenchAcceptance(engine, config))
		}
	} else {
		MountZTAPIWorkbenchRoutes(engine, 500000)
	}
	api := httptest.NewServer(engine)
	t.Cleanup(api.Close)
	apiOrigin := api.URL
	if firstPartyProof {
		if locations := os.Getenv("R37_QA_NGINX_LOCATIONS"); locations != "" {
			apiOrigin = workbenchNginxFixture(t, api, locations)
		}
	}
	capability, err := api.Client().Get(apiOrigin + "/.well-known/ztapi-workbench")
	require.NoError(t, err)
	manifest, err := io.ReadAll(io.LimitReader(capability.Body, 4096))
	require.NoError(t, err)
	require.NoError(t, capability.Body.Close())
	require.Equal(t, http.StatusOK, capability.StatusCode, "%s", manifest)
	var advertised map[string]string
	require.NoError(t, json.Unmarshal(manifest, &advertised), "%s: %q", capability.Header.Get("Content-Type"), manifest)
	if apiOrigin != api.URL {
		require.Equal(t, "no-store", capability.Header.Get("Cache-Control"))
	}
	wantCapabilities := map[string]string{"protocol": "ztapi-workbench-first-party-v1", "budget": "wire-bound-v1", "dispatch": "enabled"}
	if mode == "acceptance" {
		wantCapabilities["mode"] = "acceptance"
	} else if mode == "first-party" {
		wantCapabilities["account_routes"] = "workbench-v1"
	}
	require.Equal(t, wantCapabilities, advertised)
	post := func(path, task, hold, body string) (int, http.Header, string) {
		t.Helper()
		req, e := http.NewRequest("POST", apiOrigin+path, strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("X-ZTAPI-Task", task)
		req.Header.Set("X-ZTAPI-Hold", hold)
		r, e := api.Client().Do(req)
		require.NoError(t, e)
		defer r.Body.Close()
		data, e := io.ReadAll(r.Body)
		require.NoError(t, e)
		return r.StatusCode, r.Header, string(data)
	}
	var debit, exactQuota int64
	reserve := func(task, request, body string) (int, string) {
		reserveBody := fmt.Sprintf(`{"task":%q,"request":%q,"model":%q,"legs":[{"stage":"main","wire":%q}]}`, task, request, public, body)
		status, _, raw := post("/__workbench_budget/reserve", task, request, reserveBody)
		if status != 200 {
			return status, raw
		}
		var proof struct{ Holds []struct{ ID string } }
		require.NoError(t, json.Unmarshal([]byte(raw), &proof))
		require.Len(t, proof.Holds, 1)
		return status, proof.Holds[0].ID
	}
	if pilotMode != "" {
		const path = "/__workbench_budget/responses"
		body := fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":"cap fixture"}],"stream":true,"store":false,"max_output_tokens":1024}`, public)
		want := 14
		if pilotMode == "pilot-cash" {
			before := user.Quota
			code, raw := reserve(uuid.NewString(), uuid.NewString(), body)
			require.Equal(t, http.StatusPaymentRequired, code, raw)
			var fresh model.User
			require.NoError(t, db.First(&fresh, user.Id).Error)
			require.Equal(t, before, fresh.Quota)
			require.Zero(t, hits.Load())
			var attempts int64
			require.NoError(t, db.Model(&model.ZTAPIRequestAttempt{}).Count(&attempts).Error)
			require.Zero(t, attempts)
			return
		}
		for i := 0; i < want-1; i++ {
			task, request := uuid.NewString(), uuid.NewString()
			wire := body
			if i == 0 {
				wire = strings.Replace(body, "cap fixture", "cap fixture truncate", 1)
			}
			code, hold := reserve(task, request, wire)
			require.Equal(t, 200, code, hold)
			code, _, raw := post(path, task, hold, wire)
			if i == 0 {
				require.Equal(t, http.StatusBadGateway, code, raw)
				code, _, _ = post(path, task, hold, wire)
				require.Equal(t, 409, code)
			} else {
				require.Equal(t, 200, code, raw)
			}
		}
		type pilotBid struct{ task, hold string }
		var bids []pilotBid
		for range 3 {
			task := uuid.NewString()
			code, hold := reserve(task, uuid.NewString(), body)
			require.Equal(t, 200, code, hold)
			bids = append(bids, pilotBid{task, hold})
		}
		codes := make(chan int, len(bids))
		for _, bid := range bids {
			go func() {
				req, err := http.NewRequest("POST", apiOrigin+path, strings.NewReader(body))
				if err != nil {
					codes <- 0
					return
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+secret)
				req.Header.Set("X-ZTAPI-Task", bid.task)
				req.Header.Set("X-ZTAPI-Hold", bid.hold)
				resp, err := api.Client().Do(req)
				if err != nil {
					codes <- 0
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				codes <- resp.StatusCode
			}()
		}
		require.ElementsMatch(t, []int{http.StatusOK, http.StatusTooManyRequests, http.StatusTooManyRequests},
			[]int{<-codes, <-codes, <-codes})
		// Remount the pilot from the same configuration: its limits survive a restart.
		restarted := gin.New()
		restarted.Use(middleware.RequestId(), middleware.I18n(), middleware.BodyStorageCleanup())
		SetRouter(restarted, ThemeAssets{})
		api := httptest.NewServer(restarted)
		defer api.Close()
		apiOrigin = api.URL
		task, request := uuid.NewString(), uuid.NewString()
		code, hold := reserve(task, request, body)
		require.Equal(t, 200, code, hold)
		code, headers, raw := post(path, task, hold, body)
		require.Equal(t, http.StatusTooManyRequests, code, raw)
		require.Equal(t, "pilot-limit", headers.Get("X-ZTAPI-Workbench-Error"))
		require.EqualValues(t, want, hits.Load())
		require.Zero(t, blocked.Load())
		var row model.ZTAPIRequestSettlement
		require.NoError(t, db.Where("operation_id = ?", hold).Take(&row).Error)
		require.False(t, row.Dispatched)
		var attempts int64
		require.NoError(t, db.Model(&model.ZTAPIRequestAttempt{}).Count(&attempts).Error)
		require.EqualValues(t, want, attempts)
		var batch model.ZTAPIWorkbenchPilotSpend
		require.NoError(t, db.Where("id = ?", pilotConfig.BatchID).Take(&batch).Error)
		var claims []struct {
			SettlementID uint  `json:"settlement_id"`
			ReserveQuota int64 `json:"reserve_quota"`
		}
		require.NoError(t, json.Unmarshal([]byte(batch.ClaimsJSON), &claims))
		require.Len(t, claims, want)
		for _, claim := range claims {
			var settlement model.ZTAPIRequestSettlement
			require.NoError(t, db.First(&settlement, claim.SettlementID).Error)
			require.Equal(t, settlement.ReservedQuota, claim.ReserveQuota)
			var attempt model.ZTAPIRequestAttempt
			require.NoError(t, db.Where("settlement_id = ?", claim.SettlementID).Take(&attempt).Error)
			require.Equal(t, channel.Id, attempt.ChannelID)
		}
		var fresh model.User
		require.NoError(t, db.First(&fresh, user.Id).Error)
		limit, ok := model.ZTAPIWorkbenchPilotWalletQuota(pilotConfig.BudgetNanoU)
		require.True(t, ok)
		require.LessOrEqual(t, int64(user.Quota-fresh.Quota), limit)
		t.Logf("upstream hits=%d, platform wallet debit including holds=%d quota", want, user.Quota-fresh.Quota)
		return
	}
	if source == "claude-fable-5" {
		t.Run("reject-oversized-reservation-with-funded-wallet", func(t *testing.T) {
			var beforeUser, afterUser model.User
			var beforeKey, afterKey model.Token
			require.NoError(t, db.First(&beforeUser, user.Id).Error)
			require.NoError(t, db.First(&beforeKey, key.Id).Error)
			require.Greater(t, beforeUser.Quota, 878745)
			require.Equal(t, workbenchAcceptanceKeyLimit, beforeKey.RemainQuota)
			task, request := uuid.NewString(), uuid.NewString()
			body := fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":%q}],"stream":true,"store":false,"max_output_tokens":2048}`, public, strings.Repeat("x", 44373))
			beforeHits := hits.Load()
			var beforeLedger, afterLedger, rows int64
			require.NoError(t, db.Model(&model.BalanceLedger{}).Count(&beforeLedger).Error)
			for range 2 {
				reserveBody := fmt.Sprintf(`{"task":%q,"request":%q,"model":%q,"legs":[{"stage":"main","wire":%q}]}`, task, request, public, body)
				code, headers, raw := post("/__workbench_budget/reserve", task, request, reserveBody)
				require.Equal(t, http.StatusPaymentRequired, code, raw)
				require.Equal(t, "task-limit", headers.Get("X-ZTAPI-Workbench-Error"))
			}
			require.NoError(t, db.First(&afterUser, user.Id).Error)
			require.NoError(t, db.First(&afterKey, key.Id).Error)
			require.Equal(t, beforeUser.Quota, afterUser.Quota)
			require.Equal(t, beforeKey.RemainQuota, afterKey.RemainQuota)
			require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Where("workbench_task_id = ?", task).Count(&rows).Error)
			require.Zero(t, rows)
			require.NoError(t, db.Model(&model.BalanceLedger{}).Count(&afterLedger).Error)
			require.Equal(t, beforeLedger, afterLedger)
			require.Equal(t, beforeHits, hits.Load())
		})
	}
	const relayPath = "/__workbench_budget/responses"
	snapshotReleased := false
	validationInputs := []string{"complete", "truncate"}
	if proofScenario != "" {
		// The proof is its own purchase-authorized batch, not shared with smoke-check traffic.
		validationInputs = nil
	}
	for _, input := range validationInputs {
		task, request := uuid.NewString(), uuid.NewString()
		body := fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":%q}],"stream":true,"store":false,"max_output_tokens":1024}`, public, input)
		code, hold := reserve(task, request, body)
		require.Equal(t, 200, code, hold)
		code, duplicate := reserve(task, request, body)
		require.Equal(t, 200, code, duplicate)
		require.Equal(t, hold, duplicate)
		code, _ = reserve(task, request, strings.Replace(body, input, "changed", 1))
		require.Equal(t, 409, code)
		status, _, _ := post(relayPath, task, hold, strings.Replace(body, input, "changed", 1))
		require.Equal(t, 409, status)
		if upstreamPrefix == "" {
			_, err := model.ClaimZTAPIWorkbenchNativeAttempt(task, user.Id, key.Id, hold, channel.Id,
				common.HashZTAPIKey(channel.Key), "/hub/v1/responses")
			require.ErrorIs(t, err, model.ErrZTAPISettlementInvalid)
		}
		before := hits.Load()
		status, headers, raw := post(relayPath, task, hold, body)
		var row model.ZTAPIRequestSettlement
		require.NoError(t, db.Where("operation_id = ?", hold).Take(&row).Error)
		if input == "complete" {
			require.Equal(t, 200, status, raw)
			require.Equal(t, hold, headers.Get("X-ZTAPI-Budget-Hold"))
			require.Contains(t, raw, "event: response.completed")
			require.Equal(t, model.ZTAPISettlementSettled, row.Status)
			require.Positive(t, row.ChargedQuota)
			exactQuota = row.InitialReservedQuota
			debit += row.ChargedQuota
		} else {
			require.Equal(t, 502, status, raw)
			require.Empty(t, headers.Get("X-ZTAPI-Budget-Hold"))
			require.Equal(t, model.ZTAPISettlementPending, row.Status)
			require.Zero(t, row.ChargedQuota)
			debit += row.ReservedQuota
		}
		status, _, _ = post(relayPath, task, hold, body)
		require.Equal(t, 409, status)
		require.Equal(t, before+1, hits.Load())
		var attempts, reservations, settlements int64
		require.NoError(t, db.Model(&model.ZTAPIRequestAttempt{}).Where("settlement_id = ?", row.ID).Count(&attempts).Error)
		require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ? AND source_type = ?", hold, model.BalanceLedgerSourceUsageReservation).Count(&reservations).Error)
		require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ? AND source_type = ?", hold, model.BalanceLedgerSourceUsageSettlement).Count(&settlements).Error)
		require.EqualValues(t, 1, attempts)
		require.EqualValues(t, 1, reservations)
		if input == "complete" {
			require.EqualValues(t, 1, settlements)
			var attempt model.ZTAPIRequestAttempt
			require.NoError(t, db.Where("settlement_id = ?", row.ID).Take(&attempt).Error)
			require.Equal(t, channel.Id, attempt.ChannelID)
			require.Equal(t, upstreamPath, attempt.Protocol)
			require.Equal(t, supplierID(1), attempt.UpstreamRequestID)
			if xcloudReceipt {
				require.NotEqual(t, attempt.UpstreamRequestID, headers.Get(common.RequestIdKey))
			}
			require.Equal(t, 200, attempt.HTTPStatus)
			if accountFlow {
				t.Run("reconcile-snapshot", func(t *testing.T) {
					code, releasedHold := reserve(task, uuid.NewString(), body)
					require.Equal(t, 200, code, releasedHold)
					code, _, release := post("/__workbench_budget/release", task, releasedHold, fmt.Sprintf(`{"task":%q,"hold":%q}`, task, releasedHold))
					require.Equal(t, 200, code, release)
					require.Contains(t, release, `"released":true`)
					snapshotReleased = true
					snapshotDir := filepath.Join(t.TempDir(), "r37-acceptance-router-fixture")
					require.NoError(t, os.Mkdir(snapshotDir, 0700))
					snapshot := filepath.Join(snapshotDir, "acceptance.db")
					require.NoError(t, db.Exec("VACUUM INTO ?", filepath.ToSlash(snapshot)).Error)
					identity := map[string]any{"channelId": channel.Id, "supplier": "synthetic", "group": "default", "model": supplierModel, "unit": "fixture-credits"}
					receipt := map[string]any{"upstreamRequestId": attempt.UpstreamRequestID, "billId": "fixture-bill-1", "status": "actual", "charged": "0.00003",
						"usage": map[string]int{"input_tokens": 12, "output_tokens": 3, "cache_read": 0, "cache_write": 0, "cache_write_5m": 0, "cache_write_1h": 0}}
					for key, value := range identity {
						receipt[key] = value
					}
					raw, err := json.Marshal(map[string]any{"route": identity, "receipts": []any{receipt}})
					require.NoError(t, err)
					receiptFile := filepath.Join(snapshotDir, "synthetic-receipt.json")
					require.NoError(t, os.WriteFile(receiptFile, raw, 0600))
					auditor := os.Getenv("R37_RC10_TASK_AUDITOR")
					if xcloudReceipt {
						require.NotEmpty(t, auditor, "RC18 requires the single-receipt snapshot audit too")
					}
					if auditor == "" {
						t.Skip("requires the desktop acceptance receipt auditor")
					}
					cmd := exec.Command("node", "--no-warnings", auditor, snapshot, task, receiptFile)
					output, err := cmd.CombinedOutput()
					if err != nil {
						var tables []string
						require.NoError(t, db.Raw("SELECT name FROM sqlite_schema WHERE type='table'").Scan(&tables).Error)
						t.Logf("fixture tables: %v", tables)
					}
					require.NoError(t, err, "%s", output)
					var audit struct {
						Reconciled          bool
						GatewayChargedQuota int64
						Rows                []struct{ RequestID, Status string }
					}
					require.NoError(t, json.Unmarshal(output, &audit))
					require.True(t, audit.Reconciled)
					require.Equal(t, row.ChargedQuota, audit.GatewayChargedQuota)
					require.Len(t, audit.Rows, 2)
					require.Equal(t, "matched", audit.Rows[0].Status)
					require.Equal(t, releasedHold, audit.Rows[1].RequestID)
					require.Equal(t, "released_undispatched", audit.Rows[1].Status)
				})
			}
		} else {
			require.Zero(t, settlements)
		}
	}
	wantRows := len(validationInputs)
	if snapshotReleased {
		wantRows++
	}
	if !chat {
		task := uuid.NewString()
		tooSmall := fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":"output-floor"}],"stream":true,"store":false,"max_output_tokens":8}`, public)
		code, hold := reserve(task, uuid.NewString(), tooSmall)
		require.Equal(t, 200, code, hold)
		status, _, raw := post(relayPath, task, hold, tooSmall)
		require.Equal(t, 402, status, raw)
		var unclaimed model.ZTAPIRequestSettlement
		require.NoError(t, db.Where("operation_id = ?", hold).Take(&unclaimed).Error)
		require.False(t, unclaimed.Dispatched)
		status, _, raw = post("/__workbench_budget/release", task, hold, fmt.Sprintf(`{"task":%q,"hold":%q}`, task, hold))
		require.Equal(t, 200, status, raw)
		wantRows++
	}
	wantHits := int32(len(validationInputs))
	require.Equal(t, wantHits, hits.Load())
	node, err := exec.LookPath("node")
	require.NoError(t, err, "R36 client/server probe requires Node")
	if probePath := os.Getenv("R37_BUDGET_CONTRACT_PROBE"); probePath != "" && proofScenario == "" {
		probe := exec.Command(node, "--no-warnings", probePath)
		probe.Env = append(probe.Environ(), "R36_LOOPBACK_ORIGIN="+apiOrigin, "R36_LOOPBACK_KEY="+secret, "R36_LOOPBACK_MODEL="+public)
		proof, err := probe.CombinedOutput()
		require.NoError(t, err, "%s", proof)
		var paired []string
		require.NoError(t, json.Unmarshal(proof, &paired))
		require.Len(t, paired, 4)
		for _, hold := range paired {
			var row model.ZTAPIRequestSettlement
			require.NoError(t, db.Where("operation_id = ?", hold).Take(&row).Error)
			require.Equal(t, model.ZTAPISettlementSettled, row.Status)
			require.NotEmpty(t, row.WorkbenchBodySHA256)
			require.Equal(t, 1, row.FinalAttempt)
			debit += row.ChargedQuota
		}
		wantRows += len(paired)
		wantHits += int32(len(paired))
	}
	require.Equal(t, wantHits, hits.Load())
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Count(&count).Error)
	require.EqualValues(t, wantRows, count)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&key, key.Id).Error)
	require.EqualValues(t, 1000000-debit, user.Quota)
	require.EqualValues(t, int64(initialKeyQuota)-debit, key.RemainQuota)
	if proofScenario == "" {
		require.NoError(t, db.Model(&key).Update("remain_quota", exactQuota).Error)
		exactTask, exactRequest := uuid.NewString(), uuid.NewString()
		exactBody := fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":"complete"}],"stream":true,"store":false,"max_output_tokens":1024}`, public)
		code, exactHold := reserve(exactTask, exactRequest, exactBody)
		require.Equal(t, 200, code, exactHold)
		require.NoError(t, db.First(&key, key.Id).Error)
		require.Zero(t, key.RemainQuota)
		require.Equal(t, common.TokenStatusExhausted, key.Status)
		code, replay := reserve(exactTask, exactRequest, exactBody)
		require.Equal(t, 200, code, replay)
		require.Equal(t, exactHold, replay)
		code, _ = reserve(exactTask, uuid.NewString(), exactBody)
		require.Equal(t, 402, code)
		code, _, raw := post(relayPath, exactTask, exactHold, exactBody)
		require.Equal(t, 200, code, raw)
		require.Equal(t, wantHits+1, hits.Load())
	}
	require.Zero(t, blocked.Load())
	if accountFlow && proofScenario == "" {
		t.Run("r37-full-flow", func(t *testing.T) {
			require.NoError(t, db.First(&key, key.Id).Error)
			previousKeyQuota := key.RemainQuota
			require.NoError(t, db.First(&user, user.Id).Error)
			walletBefore := user.Quota
			start := hits.Load()
			script := os.Getenv("R37_NATIVE_QA_FLOW")
			if source == "claude-fable-5" {
				script = os.Getenv("R37_FABLE_QA_FLOW")
			}
			if script == "" {
				t.Skip("requires the desktop native-flow fixture")
			}
			cmd := exec.Command(node, "--no-warnings", script)
			cmd.Env = append(cmd.Environ(), "R36_LOOPBACK_ORIGIN="+apiOrigin, "R36_LOOPBACK_MODEL="+public, "R37_QA_ROUTE_MODE="+mode)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			var proof struct {
				Task                    string
				Main, Child, Checkpoint int
				Grants                  []int64
				WalletDebited           int64
			}
			require.NoError(t, json.Unmarshal(output, &proof))
			mainSteps, checkpoints := 7, 1
			if source == "claude-fable-5" {
				// The earlier checkpoint changes when the mock chooses to finish reading.
				mainSteps, checkpoints = 6, 2
			}
			require.Equal(t, mainSteps, proof.Main)
			require.Equal(t, 2, proof.Child)
			require.Equal(t, checkpoints, proof.Checkpoint)
			var rows []model.ZTAPIRequestSettlement
			require.NoError(t, db.Where("workbench_task_id = ?", proof.Task).Find(&rows).Error)
			require.Len(t, rows, proof.Main+proof.Child+proof.Checkpoint)
			require.EqualValues(t, len(rows), hits.Load()-start)
			var total int64
			childCount := 0
			byKey := make(map[int]int64)
			childByKey := make(map[int]int)
			var outbound []outboundSize
			for _, row := range rows {
				require.NotEqual(t, key.Id, row.TokenID)
				require.Equal(t, model.ZTAPISettlementSettled, row.Status)
				require.True(t, row.Dispatched)
				require.Equal(t, 1, row.FinalAttempt)
				require.Positive(t, row.ChargedQuota)
				require.Equal(t, row.ChargedQuota, row.TokenChargedQuota)
				require.LessOrEqual(t, row.ChargedQuota, row.ReservedQuota)
				var attempt model.ZTAPIRequestAttempt
				require.NoError(t, db.Where("settlement_id = ?", row.ID).Take(&attempt).Error)
				measurement, ok := measured.Load(attempt.UpstreamRequestID)
				require.True(t, ok, "wire size must be bound to the recorded supplier attempt")
				wire := measurement.(outboundSize)
				require.Positive(t, wire.Bytes)
				require.Positive(t, wire.MaxOutput)
				require.LessOrEqual(t, wire.Bytes, row.WorkbenchOutboundMax)
				require.LessOrEqual(t, wire.MaxOutput, row.WorkbenchOutputMax)
				outbound = append(outbound, wire)
				var ledger []model.BalanceLedger
				require.NoError(t, db.Where("request_id = ? AND user_id = ?", row.RequestID, user.Id).Order("id").Find(&ledger).Error)
				require.Len(t, ledger, 2)
				require.Equal(t, model.BalanceLedgerSourceUsageReservation, ledger[0].SourceType)
				require.EqualValues(t, -row.ReservedQuota, ledger[0].Delta)
				require.Equal(t, model.BalanceLedgerSourceUsageSettlement, ledger[1].SourceType)
				require.EqualValues(t, row.ReservedQuota-row.ChargedQuota, ledger[1].Delta)
				if row.WorkbenchStage == "child" {
					childCount++
					childByKey[row.TokenID]++
				}
				total += row.ChargedQuota
				byKey[row.TokenID] += row.ChargedQuota
			}
			require.Equal(t, 2, childCount)
			var taskKeys []model.Token
			require.NoError(t, db.Unscoped().Where("user_id = ? AND id <> ?", user.Id, key.Id).Order("id").Find(&taskKeys).Error)
			require.Len(t, taskKeys, 2, "one key before pause and one after resume")
			require.Len(t, byKey, 2)
			require.Len(t, proof.Grants, 2)
			require.EqualValues(t, min(walletBefore, workbenchAcceptanceKeyLimit), proof.Grants[0])
			for i, taskKey := range taskKeys {
				charged := byKey[taskKey.Id]
				require.Positive(t, charged)
				require.Equal(t, 1, childByKey[taskKey.Id])
				require.True(t, taskKey.DeletedAt.Valid, "client must revoke the temporary key")
				require.False(t, taskKey.UnlimitedQuota)
				require.True(t, taskKey.ModelLimitsEnabled)
				require.Equal(t, public, taskKey.ModelLimits)
				require.EqualValues(t, proof.Grants[i]-charged, taskKey.RemainQuota)
				require.EqualValues(t, charged, taskKey.UsedQuota)
			}
			require.Equal(t, total, proof.WalletDebited)
			require.NoError(t, db.First(&key, key.Id).Error)
			require.Equal(t, previousKeyQuota, key.RemainQuota)
			require.NoError(t, db.First(&user, user.Id).Error)
			require.EqualValues(t, int64(walletBefore)-total, user.Quota)
			t.Logf("R37 mock full-flow: main=%d child=%d compaction=%d dispatched=%d charged_quota=%d", proof.Main, proof.Child, proof.Checkpoint, len(rows), total)
			if mode == "acceptance" && source == "claude-sonnet-5" {
				t.Logf("R37 Sonnet supplier-wire measurements (bytes, maximum output tokens): %v", outbound)
			}
		})
	}
	if mode == "acceptance" && source == "claude-sonnet-5" {
		t.Run("r37-short-preflight-refusal", func(t *testing.T) {
			require.NoError(t, db.First(&user, user.Id).Error)
			walletBefore := user.Quota
			start := hits.Load()
			probeNode := node
			if app := os.Getenv("R37_QA_APP_ROOT"); app != "" && os.PathSeparator == '\\' {
				probeNode = filepath.Join(app, "bin", "runtime", "node.exe")
			}
			shortProbe := os.Getenv("R37_SHORT_REFUSAL_PROBE")
			if shortProbe == "" {
				t.Skip("requires the desktop refusal fixture")
			}
			probe := exec.Command(probeNode, shortProbe)
			probe.Env = append(probe.Environ(), "R36_LOOPBACK_ORIGIN="+apiOrigin, "R36_LOOPBACK_MODEL="+public)
			output, err := probe.CombinedOutput()
			require.NoError(t, err, "%s", output)
			var task string
			require.NoError(t, json.Unmarshal(output, &task))
			var row model.ZTAPIRequestSettlement
			require.NoError(t, db.Where("workbench_task_id = ?", task).Take(&row).Error)
			require.Equal(t, model.ZTAPISettlementReleased, row.Status)
			require.False(t, row.Dispatched)
			require.Zero(t, row.ChargedQuota)
			var attempts, reversals int64
			require.NoError(t, db.Model(&model.ZTAPIRequestAttempt{}).Where("settlement_id = ?", row.ID).Count(&attempts).Error)
			require.NoError(t, db.Model(&model.BalanceLedger{}).Where("request_id = ? AND source_type = ? AND delta = ?", row.RequestID, model.BalanceLedgerSourceBillingRefund, row.ReservedQuota).Count(&reversals).Error)
			require.Zero(t, attempts)
			require.EqualValues(t, 1, reversals)
			require.Equal(t, start, hits.Load())
			require.NoError(t, db.First(&user, user.Id).Error)
			require.Equal(t, walletBefore, user.Quota)
		})
	}
	if rc10Proof || mode == "acceptance" && (source == "claude-sonnet-5" || source == "gpt-6.1-sol") {
		for _, scenario := range []struct {
			name, mode  string
			checkpoints int
		}{
			{"r37-short-native-acceptance", "short", 0},
			{"r37-long-native-acceptance", "long", 1},
		} {
			if proofScenario != "" && scenario.mode != proofScenario {
				continue
			}
			t.Run(scenario.name, func(t *testing.T) {
				app := os.Getenv("R37_QA_APP_ROOT")
				if app == "" || os.PathSeparator != '\\' {
					if rc10Proof {
						t.Fatal("requires the sealed Windows candidate and native sandbox")
					}
					t.Skip("requires extracted Windows candidate and native sandbox")
				}
				const password = "r37-local-0123456789abcdef0123456789abcdef"
				hash, err := service.HashZTAPIPassword(password)
				require.NoError(t, err)
				username, quota := "r37-sonnet-qa", 250000
				if source == "gpt-6.1-sol" {
					username = "r37-gpt-qa"
				}
				runner := filepath.Join("..", "..", "verify-r37-sonnet-task.mjs")
				auditor := filepath.Join("..", "..", "reconcile-acceptance.mjs")
				if rc10Proof {
					runner, auditor = os.Getenv("R37_RC10_TASK_RUNNER"), os.Getenv("R37_RC10_TASK_AUDITOR")
					if xcloudReceipt {
						runner, auditor = os.Getenv("R37_RC18_TASK_RUNNER"), os.Getenv("R37_RC18_TASK_AUDITOR")
					}
					require.True(t, filepath.IsAbs(runner) && filepath.IsAbs(auditor))
					if source == "claude-fable-5" {
						username, quota = "r37-fable-qa", 500000
					} else if source == "glm-5.3" {
						username = "r37-glm-qa"
					}
				}
				require.NoError(t, db.Model(&user).Updates(map[string]any{"username": username, "password": hash, "quota": quota}).Error)
				state := filepath.Join(t.TempDir(), "r37-acceptance-"+scenario.mode+"-fixture")
				require.NoError(t, os.Mkdir(state, 0700))
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				args := []string{runner, app, state, apiOrigin, scenario.mode, public}
				if firstPartyProof {
					args = append(args, "first-party-loopback")
				}
				cmd := exec.CommandContext(ctx, filepath.Join(app, "bin", "runtime", "node.exe"), args...)
				cmd.Stdin = strings.NewReader(password)
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", output)
				var proof struct {
					TaskID, Status, Model, Protocol   string
					Main, Child, Checkpoint, Tests    int
					TestExitCodes                     []int
					Review, ApprovedWriteback, Logout bool
				}
				require.NoError(t, json.Unmarshal(output, &proof))
				require.Equal(t, "task_passed", proof.Status)
				require.Equal(t, public, proof.Model)
				if firstPartyProof {
					require.Equal(t, "first-party-loopback", proof.Protocol)
				}
				require.True(t, proof.Review && proof.ApprovedWriteback && proof.Logout)
				require.Equal(t, 2, proof.Child)
				require.Equal(t, 1, proof.Tests)
				require.Equal(t, []int{0}, proof.TestExitCodes)
				if rc10Proof && source == "claude-fable-5" && scenario.mode == "long" {
					require.Equal(t, 2, proof.Checkpoint)
				} else {
					require.Equal(t, scenario.checkpoints, proof.Checkpoint)
				}
				var rows []model.ZTAPIRequestSettlement
				require.NoError(t, db.Where("workbench_task_id = ?", proof.TaskID).Order("id").Find(&rows).Error)
				require.Len(t, rows, proof.Main+proof.Child+proof.Checkpoint)
				var outbound []outboundSize
				route := map[string]any{"channelId": channel.Id, "supplier": "synthetic", "group": "default", "model": supplierModel, "unit": "fixture-credits"}
				var receipts []map[string]any
				seenSupplierIDs := make(map[string]bool)
				for _, row := range rows {
					require.Equal(t, model.ZTAPISettlementSettled, row.Status)
					require.Equal(t, public, row.PublicModel)
					var attempts []model.ZTAPIRequestAttempt
					require.NoError(t, db.Where("settlement_id = ?", row.ID).Find(&attempts).Error)
					require.Len(t, attempts, 1)
					attempt := attempts[0]
					require.Equal(t, upstreamPath, attempt.Protocol)
					if xcloudReceipt {
						require.Equal(t, channel.Id, attempt.ChannelID)
						require.NotEmpty(t, attempt.UpstreamRequestID)
						require.False(t, seenSupplierIDs[attempt.UpstreamRequestID], "one supplier receipt per dispatch")
						seenSupplierIDs[attempt.UpstreamRequestID] = true
					}
					measurement, ok := measured.Load(attempt.UpstreamRequestID)
					require.True(t, ok)
					outbound = append(outbound, measurement.(outboundSize))
					receipt := map[string]any{"upstreamRequestId": attempt.UpstreamRequestID, "billId": attempt.UpstreamRequestID,
						"status": "actual", "charged": "0.00003", "usage": map[string]int{"input_tokens": 12, "output_tokens": 3,
							"cache_read": 0, "cache_write": 0, "cache_write_5m": 0, "cache_write_1h": 0}}
					for key, value := range route {
						receipt[key] = value
					}
					receipts = append(receipts, receipt)
				}
				snapshot := filepath.Join(state, "acceptance.db")
				require.NoError(t, db.Exec("VACUUM INTO ?", filepath.ToSlash(snapshot)).Error)
				receiptFile := filepath.Join(state, "synthetic-receipts.json")
				raw, err := json.Marshal(map[string]any{"route": route, "receipts": receipts})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(receiptFile, raw, 0600))
				verify := exec.CommandContext(ctx, filepath.Join(app, "bin", "runtime", "node.exe"), "--no-warnings",
					auditor, snapshot, proof.TaskID, receiptFile,
					filepath.Join(state, "task-proof.json"), scenario.mode)
				audit, err := verify.CombinedOutput()
				require.NoError(t, err, "%s", audit)
				require.Contains(t, string(audit), `"passed": true`)
				if proofScenario != "" {
					var batch model.ZTAPIWorkbenchPilotSpend
					require.NoError(t, db.Where("id = ?", pilotConfig.BatchID).Take(&batch).Error)
					var claims []struct {
						ReserveQuota int64 `json:"reserve_quota"`
					}
					require.NoError(t, json.Unmarshal([]byte(batch.ClaimsJSON), &claims))
					require.Len(t, claims, len(rows))
					require.EqualValues(t, len(rows), hits.Load())
					var debitQuota int64
					for _, row := range rows {
						require.LessOrEqual(t, row.ChargedQuota, row.ReservedQuota)
						debitQuota += row.ChargedQuota
					}
					limit, ok := model.ZTAPIWorkbenchPilotWalletQuota(pilotConfig.BudgetNanoU)
					require.True(t, ok)
					require.LessOrEqual(t, debitQuota, limit)
					t.Logf("Fable %s cap proof: %d simulated dispatches, %d quota settled", scenario.mode, len(claims), debitQuota)
				}
				t.Logf("R37 %s %s native task wire (bytes, maximum output tokens): %v", public, scenario.mode, outbound)
			})
		}
	}
}
