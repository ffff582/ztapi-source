package controller

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const ztapiE2EInitialQuota = 1_000_000

type ztapiE2EReply struct {
	status int
	body   string
	stream bool
}

type ztapiE2EFixture struct {
	db                           *gorm.DB
	router                       *gin.Engine
	user                         model.User
	token                        *model.Token
	key, publicName, sourceModel string
	config                       model.ZTAPIModelConfig
	mu                           sync.Mutex
	replies                      []ztapiE2EReply
	calls                        atomic.Int64
	quotaWrites                  atomic.Int64
}

func newZTAPIHealthE2EFixture(t *testing.T) *ztapiE2EFixture {
	t.Helper()
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	t.Setenv("ZTAPI_HEALTH_PROBE_USER_ID", "0")
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", strings.Repeat("local-e2e-only-", 3))
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldSQLite, oldMySQL, oldPostgres := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
	oldRedis, oldMemory, oldBatch := common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled
	oldRetry, oldCount := common.RetryTimes, constant.CountToken
	settings := operation_setting.GetGeneralSetting()
	oldPing := settings.PingIntervalEnabled
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = oldSQLite, oldMySQL, oldPostgres
		common.RedisEnabled, common.MemoryCacheEnabled, common.BatchUpdateEnabled = oldRedis, oldMemory, oldBatch
		common.RetryTimes, constant.CountToken = oldRetry, oldCount
		settings.PingIntervalEnabled = oldPing
		model.InvalidateZTAPIAliasCache()
		model.InvalidatePricingCache()
		if oldMemory && oldDB != nil {
			model.InitChannelCache()
		}
	})
	common.MemoryCacheEnabled = true
	common.BatchUpdateEnabled = false
	common.RetryTimes = 1
	constant.CountToken = false
	settings.PingIntervalEnabled = false
	require.NoError(t, i18n.Init())
	service.InitTokenEncoders() // Embedded codecs only, no downloads.
	f := &ztapiE2EFixture{db: setupTokenControllerTestDB(t), publicName: "zt-gpt-5.5"}
	f.sourceModel = "gpt-5.5"
	require.NoError(t, f.db.AutoMigrate(&model.User{}, &model.Channel{}, &model.BalanceLedger{}, &model.BillingRefundPending{}, &model.Log{}, &model.ZTAPIAuditEvent{}, &model.ZTAPICatalogLock{}))
	require.NoError(t, f.db.AutoMigrate(
		&model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPIPendingResolution{},
		&model.ZTAPISettlementFinalizationIntent{},
		&model.ZTAPIAttemptBillingReview{}, &model.ZTAPIAttemptBillingProof{}, &model.ZTAPIAttemptBillingApproval{},
		&model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{},
		&model.ZTAPISupplierRefundCharge{}, &model.ZTAPISupplierRefund{}, &model.ZTAPISupplierRefundApproval{},
	))
	model.LOG_DB = f.db
	require.NoError(t, model.MigrateZTAPIHealth(f.db))
	f.user = model.User{Username: "health-e2e-user", Password: "unused-test-password", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Group: "default", Quota: ztapiE2EInitialQuota, Setting: `{"billing_preference":"wallet_only"}`}
	require.NoError(t, f.db.Create(&f.user).Error)
	f.token, f.key = seedToken(t, f.db, f.user.Id, "health-e2e-key")
	require.NoError(t, f.db.Model(f.token).Updates(map[string]any{"unlimited_quota": false, "remain_quota": ztapiE2EInitialQuota, "group": "", "model_limits_enabled": true, "model_limits": f.publicName}).Error)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		var body struct {
			Model string `json:"model"`
		}
		if err := common.DecodeJson(io.LimitReader(r.Body, 64<<10), &body); err != nil {
			t.Errorf("decode outbound request: %v", err)
		}
		if body.Model != f.sourceModel {
			t.Errorf("publication did not map outbound model: %q", body.Model)
		}
		if r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer local-e2e-upstream-key" {
			t.Error("real channel authentication was not applied")
		}
		var reservations int64
		if err := f.db.Model(&model.BalanceLedger{}).Where("source_type = ?", model.BalanceLedgerSourceUsageReservation).Count(&reservations).Error; err != nil || reservations == 0 {
			t.Errorf("upstream dispatched without real wallet reservation: count=%d err=%v", reservations, err)
		}
		var token model.Token
		if err := f.db.First(&token, f.token.Id).Error; err != nil || token.RemainQuota >= ztapiE2EInitialQuota {
			t.Errorf("finite token was not precharged: remain=%d err=%v", token.RemainQuota, err)
		}
		var held model.ZTAPIRequestSettlement
		if err := f.db.Order("id DESC").Take(&held).Error; err != nil {
			t.Errorf("dispatch has no durable settlement: %v", err)
		} else {
			assert.Equal(t, model.ZTAPISettlementReserved, held.Status)
			assert.True(t, held.Dispatched, "dispatch intent must commit before network I/O")
			var attempts []model.ZTAPIRequestAttempt
			assert.NoError(t, f.db.Where("settlement_id = ?", held.ID).Order("attempt").Find(&attempts).Error)
			if assert.NotEmpty(t, attempts, "upstream attempt identity must already be durable") {
				assert.LessOrEqual(t, len(attempts), 2)
				assert.Equal(t, len(attempts), attempts[len(attempts)-1].Attempt)
			}
		}
		f.mu.Lock()
		if len(f.replies) == 0 {
			f.mu.Unlock()
			t.Error("unexpected extra upstream dispatch")
			http.Error(w, "unexpected dispatch", 500)
			return
		}
		reply := f.replies[0]
		f.replies = f.replies[1:]
		f.mu.Unlock()
		w.Header().Set("X-Request-ID", fmt.Sprintf("local-upstream-%d", n))
		w.Header().Set("Content-Type", "application/json")
		if reply.stream {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
		if reply.stream {
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(upstream.Close)
	channel := model.Channel{Id: 1, Name: "health-e2e-loopback", Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Key: "local-e2e-upstream-key", BaseURL: &upstream.URL, Models: f.sourceModel, Group: "default", AutoBan: common.GetPointer(0), ZTAPIManaged: true}
	require.NoError(t, f.db.Create(&channel).Error)
	require.NoError(t, f.db.Create(&model.Ability{ChannelId: 1, Model: f.sourceModel, Group: "default", Enabled: true, Weight: 100}).Error)
	seedEnabledPublicModel(t, f.db, f.publicName)
	require.NoError(t, f.db.First(&f.config, "public_name = ?", f.publicName).Error)
	require.NoError(t, f.db.Model(&model.ZTAPIModelPriceSource{}).Where("model_config_id = ?", f.config.ID).Update("resource_type", "enterprise").Error)
	f.assertPublishedPrice(t)
	model.InitChannelCache()
	model.InvalidatePricingCache()
	// Observe actual SQL mutations, including precharge followed by a refund.
	require.NoError(t, f.db.Callback().Update().After("gorm:update").Register("health_e2e_quota_observer", func(tx *gorm.DB) {
		query := tx.Statement.SQL.String()
		if tx.Statement.Table == "tokens" && (strings.Contains(query, "remain_quota") || strings.Contains(query, "used_quota")) {
			f.quotaWrites.Add(1)
		}
	}))
	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	client := service.GetHttpClient()
	oldClient := *client
	// A real TCP transport, constrained to this fixture's loopback upstream.
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != upstream.Listener.Addr().String() {
			return nil, fmt.Errorf("external network blocked in health E2E: %s", address)
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, address)
	}}
	*client = http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() { transport.CloseIdleConnections(); *client = oldClient })
	f.router = gin.New()
	f.router.Use(gin.Recovery(), middleware.RequestId(), middleware.BodyStorageCleanup(), middleware.TokenAuth(), middleware.Distribute())
	f.router.POST("/v1/chat/completions", func(c *gin.Context) { Relay(c, types.RelayFormatOpenAI) })
	f.router.POST("/v1/responses", func(c *gin.Context) { Relay(c, types.RelayFormatOpenAIResponses) })
	return f
}

func (f *ztapiE2EFixture) queue(replies ...ztapiE2EReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, replies...)
}

func (f *ztapiE2EFixture) request(t *testing.T, path, modelName, key, requestID string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"model": modelName, "stream": stream, "max_tokens": 64, "messages": []map[string]string{{"role": "user", "content": "Say hello."}}}
	if path == "/v1/responses" {
		body = map[string]any{"model": modelName, "stream": stream, "max_output_tokens": 64, "input": "Say hello."}
	}
	encoded, err := common.Marshal(body)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("X-Request-ID", requestID)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

type ztapiE2EBalances struct {
	UserQuota, TokenRemain, TokenUsed          int
	LedgerRows, QuotaWrites, Admissions, Calls int64
	Settlements, Attempts, Logs                int64
}

func (f *ztapiE2EFixture) balances(t *testing.T) ztapiE2EBalances {
	t.Helper()
	var user model.User
	var token model.Token
	require.NoError(t, f.db.First(&user, f.user.Id).Error)
	require.NoError(t, f.db.First(&token, f.token.Id).Error)
	s := ztapiE2EBalances{UserQuota: user.Quota, TokenRemain: token.RemainQuota, TokenUsed: token.UsedQuota, QuotaWrites: f.quotaWrites.Load(), Calls: f.calls.Load()}
	require.NoError(t, f.db.Model(&model.BalanceLedger{}).Count(&s.LedgerRows).Error)
	require.NoError(t, f.db.Model(&model.ZTAPIHealthRequest{}).Count(&s.Admissions).Error)
	require.NoError(t, f.db.Model(&model.ZTAPIRequestSettlement{}).Count(&s.Settlements).Error)
	require.NoError(t, f.db.Model(&model.ZTAPIRequestAttempt{}).Count(&s.Attempts).Error)
	require.NoError(t, f.db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&s.Logs).Error)
	return s
}

func (f *ztapiE2EFixture) assertPublishedPrice(t *testing.T) {
	t.Helper()
	publication, err := model.GetZTAPIRuntimePublication(f.publicName)
	require.NoError(t, err)
	var source model.ZTAPIModelPriceSource
	require.NoError(t, f.db.First(&source, publication.PriceSourceID).Error)
	require.Equal(t, f.config.ID, source.ModelConfigID)
	require.Equal(t, f.sourceModel, source.SourceModel)
	require.Equal(t, "enterprise", source.ResourceType)
	require.Equal(t, model.ZTAPIQuotationSHA256, source.SourceDocumentChecksum)
	require.Equal(t, source.Version, publication.PriceSourceVersion)
	require.Equal(t, []string{"input_tokens", "output_tokens"}, publication.BillingDimensions)
	require.Equal(t, map[string]string{"input_tokens": "1.6666666667", "output_tokens": "3.3333333333"}, publication.SaleUSD)
}

func (f *ztapiE2EFixture) assertSettlement(t *testing.T, index int, status string, channels []int) model.ZTAPIRequestSettlement {
	t.Helper()
	var rows []model.ZTAPIRequestSettlement
	require.NoError(t, f.db.Order("id").Find(&rows).Error)
	require.Len(t, rows, index+1, "one durable settlement per logical request")
	row := rows[index]
	require.Equal(t, status, row.Status)
	require.True(t, row.Dispatched)
	require.Equal(t, f.user.Id, row.UserID)
	require.Equal(t, f.token.Id, row.TokenID)
	require.Positive(t, row.ReservedQuota)
	require.Equal(t, row.InitialReservedQuota, row.ReservedQuota, "fallback must not reserve twice")
	require.Equal(t, row.ReservedQuota, row.TokenReservedQuota)
	var frozen relaycommon.ZTAPIPublicationSnapshot
	require.NoError(t, common.UnmarshalJsonStr(row.PriceSnapshotJSON, &frozen))
	require.Equal(t, f.publicName, frozen.PublicName)
	require.Positive(t, frozen.PriceSourceID)
	require.EqualValues(t, 1, frozen.PriceSourceVersion)
	require.Equal(t, []string{"input_tokens", "output_tokens"}, frozen.BillingDimensions)
	require.Equal(t, map[string]string{"input_tokens": "1.6666666667", "output_tokens": "3.3333333333"}, frozen.SaleUSD)
	var attempts []model.ZTAPIRequestAttempt
	require.NoError(t, f.db.Where("settlement_id = ?", row.ID).Order("attempt").Find(&attempts).Error)
	require.Len(t, attempts, len(channels))
	seen := map[int]bool{}
	for i, attempt := range attempts {
		require.Equal(t, i+1, attempt.Attempt)
		require.Equal(t, channels[i], attempt.ChannelID)
		require.False(t, seen[attempt.ChannelID], "fallback must use a distinct channel")
		seen[attempt.ChannelID] = true
		require.Len(t, attempt.CredentialVersion, 64)
		require.NotEmpty(t, attempt.UpstreamRequestID)
	}
	var reservations int64
	require.NoError(t, f.db.Model(&model.BalanceLedger{}).Where("request_id = ? AND source_type = ?", row.RequestID, model.BalanceLedgerSourceUsageReservation).Count(&reservations).Error)
	require.EqualValues(t, 1, reservations)
	var logs []model.Log
	require.NoError(t, f.db.Where("request_id = ? AND type = ?", row.RequestID, model.LogTypeConsume).Find(&logs).Error)
	var outboxes []model.ZTAPISettlementLogOutbox
	require.NoError(t, f.db.Where("operation_id = ?", row.OperationID).Find(&outboxes).Error)
	if status == model.ZTAPISettlementPending {
		require.Zero(t, row.ChargedQuota)
		require.Zero(t, row.TokenChargedQuota)
		require.Zero(t, row.FinalAttempt)
		require.Empty(t, logs, "unknown billing must not produce a final consume log")
		require.Empty(t, outboxes)
		require.NotEqual(t, "[]", row.MissingDimensionsJSON)
	} else {
		require.Equal(t, model.ZTAPISettlementSettled, status)
		require.EqualValues(t, 17, row.ChargedQuota)
		require.Equal(t, row.ChargedQuota, row.TokenChargedQuota)
		require.Equal(t, len(channels), row.FinalAttempt)
		require.Len(t, logs, 1)
		require.EqualValues(t, row.ChargedQuota, logs[0].Quota)
		require.Equal(t, channels[len(channels)-1], logs[0].ChannelId)
		require.Len(t, outboxes, 1)
		var receipts int64
		require.NoError(t, f.db.Model(&model.ZTAPISettlementLogReceipt{}).Where("operation_id = ?", row.OperationID).Count(&receipts).Error)
		require.EqualValues(t, 1, receipts)
	}
	var held, charged int64
	for _, settlement := range rows {
		if settlement.Status == model.ZTAPISettlementPending || settlement.Status == model.ZTAPISettlementReserved {
			held += settlement.ReservedQuota
		}
		charged += settlement.ChargedQuota
	}
	balances := f.balances(t)
	require.EqualValues(t, ztapiE2EInitialQuota-held-charged, balances.UserQuota)
	require.Equal(t, balances.UserQuota, balances.TokenRemain)
	require.EqualValues(t, held+charged, balances.TokenUsed)
	return row
}

func (f *ztapiE2EFixture) events(t *testing.T) []model.ZTAPIHealthEvent {
	t.Helper()
	var events []model.ZTAPIHealthEvent
	require.NoError(t, f.db.Order("completion_sequence").Find(&events).Error)
	return events
}

func ztapiE2EFailure() ztapiE2EReply {
	return ztapiE2EReply{status: 502, body: `{"error":{"type":"server_error","code":"upstream_unavailable","message":"local fixture failure"}}`}
}
func ztapiE2ESuccess(path string) ztapiE2EReply {
	body := `{"id":"chat-local","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	if path == "/v1/responses" {
		body = `{"id":"resp-local","object":"response","status":"completed","output":[{"id":"msg-local","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello."}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
	}
	return ztapiE2EReply{status: 200, body: body}
}

func TestZTAPIHealthE2EFinalFailuresTripBeforePrecharge(t *testing.T) {
	for _, tt := range []struct {
		path   string
		status int
	}{
		{"/v1/chat/completions", 502}, {"/v1/responses", 502},
		{"/v1/chat/completions", 429}, {"/v1/responses", 429},
	} {
		t.Run(fmt.Sprintf("%s/%d", tt.path, tt.status), func(t *testing.T) {
			path := tt.path
			f := newZTAPIHealthE2EFixture(t)
			ids := configureZTAPIRetryChannels(t, f, 2)
			before := f.balances(t)
			badKey := f.request(t, path, f.publicName, "invalid-local-key", "e2e-bad-key", false)
			require.Equal(t, 401, badKey.Code, badKey.Body.String())
			private := f.request(t, path, f.sourceModel, f.key, "e2e-private-name", false)
			require.Equal(t, 403, private.Code, private.Body.String())
			require.Equal(t, before, f.balances(t), "auth/model rejection must not precharge or dispatch")
			failure := ztapiE2EFailure()
			if tt.status == 429 {
				failure = ztapiE2EReply{status: 429, body: `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"local fixture route rate limit"}}`}
			}
			for i := 0; i < 2; i++ {
				f.queue(failure, failure)
				w := f.request(t, path, f.publicName, f.key, "e2e-shared-client-id", false)
				require.Equal(t, tt.status, w.Code, w.Body.String())
				events := f.events(t)
				require.Len(t, events, i+1, "one durable final event per downstream request, not per retry")
				require.Equal(t, "failure", events[i].Result)
				require.True(t, events[i].Counted)
				var outcome types.ZTAPIHealthOutcome
				require.NoError(t, common.UnmarshalJsonStr(events[i].Outcome, &outcome))
				require.Len(t, outcome.Attempts, 2)
				if tt.status == 429 {
					require.Equal(t, "upstream_rate_limit", outcome.Reason)
				}
				require.Equal(t, int64((i+1)*2), f.calls.Load())
				f.assertSettlement(t, i, model.ZTAPISettlementPending, ids)
			}
			events := f.events(t)
			require.NotEqual(t, events[0].ExecutionID, events[1].ExecutionID)
			state, err := model.NewZTAPIHealthStore(f.db).GetState(context.Background(), f.config.ID)
			require.NoError(t, err)
			require.True(t, state.Open)
			require.EqualValues(t, 2, state.ConsecutiveFailures)
			before = f.balances(t)
			w := f.request(t, path, f.publicName, f.key, "e2e-circuit-block", false)
			require.Equal(t, 503, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "model_temporarily_unavailable")
			require.Contains(t, w.Body.String(), "e2e-circuit-block")
			require.Equal(t, before, f.balances(t), "circuit must reject before wallet AND token precharge, including refund-masked precharge")
			require.Len(t, f.events(t), 2)
		})
	}
}

func TestZTAPIHealthE2ESuccessResetsAndRetryRecordsOnce(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			ids := configureZTAPIRetryChannels(t, f, 2)
			f.queue(ztapiE2EFailure(), ztapiE2EFailure(), ztapiE2EFailure(), ztapiE2ESuccess(path), ztapiE2EFailure(), ztapiE2EFailure())
			for i, want := range []string{"failure", "success", "failure"} {
				w := f.request(t, path, f.publicName, f.key, fmt.Sprintf("e2e-reset-%d", i), false)
				if want == "success" {
					require.Equal(t, 200, w.Code, w.Body.String())
					require.Contains(t, w.Body.String(), "Hello.")
				} else {
					require.Equal(t, 502, w.Code, w.Body.String())
				}
				events := f.events(t)
				require.Len(t, events, i+1)
				require.Equal(t, want, events[i].Result)
				var out types.ZTAPIHealthOutcome
				require.NoError(t, common.UnmarshalJsonStr(events[i].Outcome, &out))
				require.Len(t, out.Attempts, 2)
				state, err := model.NewZTAPIHealthStore(f.db).GetState(context.Background(), f.config.ID)
				require.NoError(t, err)
				require.False(t, state.Open)
				if want == "success" {
					f.assertSettlement(t, i, model.ZTAPISettlementSettled, ids)
					require.Zero(t, state.ConsecutiveFailures)
					require.Less(t, f.balances(t).UserQuota, ztapiE2EInitialQuota)
					require.Less(t, f.balances(t).TokenRemain, ztapiE2EInitialQuota)
				} else {
					f.assertSettlement(t, i, model.ZTAPISettlementPending, ids)
					require.EqualValues(t, 1, state.ConsecutiveFailures)
				}
			}
			require.EqualValues(t, 6, f.calls.Load())
		})
	}
}

func TestZTAPIHealthE2ESettlementCallbackFailureKeepsUsageAndPendingHold(t *testing.T) {
	f := newZTAPIHealthE2EFixture(t)
	var injected atomic.Bool
	require.NoError(t, f.db.Callback().Update().After("gorm:update").Register("health_e2e_settlement_failure", func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*model.ZTAPIRequestSettlement)
		if ok && row.Status == model.ZTAPISettlementSettled && !injected.Swap(true) {
			tx.AddError(fmt.Errorf("synthetic settlement callback failure"))
		}
	}))
	f.queue(ztapiE2ESuccess("/v1/chat/completions"))
	w := f.request(t, "/v1/chat/completions", f.publicName, f.key, "callback-unknown", false)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "Hello.")
	require.True(t, injected.Load(), "must exercise the settlement transaction failure")
	row := f.assertSettlement(t, 0, model.ZTAPISettlementPending, []int{1})
	require.Contains(t, row.MissingDimensionsJSON, "settlement_retry_required")
	var usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	}
	require.NoError(t, common.UnmarshalJsonStr(row.UsageJSON, &usage))
	require.Equal(t, 10, usage.PromptTokens)
	require.Equal(t, 5, usage.CompletionTokens)
	require.Equal(t, 15, usage.TotalTokens)
	var user model.User
	require.NoError(t, f.db.First(&user, f.user.Id).Error)
	require.Zero(t, user.RequestCount)
	require.Zero(t, user.UsedQuota)
	require.EqualValues(t, 1, f.calls.Load())
}

func TestZTAPIHealthE2ETruncatedSSEAndCommittedError(t *testing.T) {
	chat := "data: {\"id\":\"chat-local\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chat-local\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"
	for _, tt := range []struct {
		name, path, body string
		viaResponses     bool
	}{
		{"chat EOF without DONE", "/v1/chat/completions", chat, false},
		{"responses EOF without completed", "/v1/responses", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", false},
		{"chat malformed final frame", "/v1/chat/completions", chat + "data: {broken\n\n", false},
		{"chat via responses error after committed output", "/v1/chat/completions", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
			"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"server_error\",\"code\":\"upstream_unavailable\",\"message\":\"local fixture failure\"}}}\n\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			if tt.viaResponses {
				settings := model_setting.GetGlobalSettings()
				old := settings.ChatCompletionsToResponsesPolicy
				settings.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{Enabled: true, ChannelIDs: []int{1}, ModelPatterns: []string{`^zt-gpt-5\.5$`}}
				t.Cleanup(func() { settings.ChatCompletionsToResponsesPolicy = old })
			}
			f.queue(ztapiE2EReply{status: 200, stream: true, body: tt.body})
			w := f.request(t, tt.path, f.publicName, f.key, "e2e-stream", true)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "partial")
			require.NotContains(t, w.Body.String(), "[DONE]")
			require.NotContains(t, w.Body.String(), `{"error":`)
			require.NotContains(t, w.Body.String(), "new_api_error")
			require.EqualValues(t, 1, f.calls.Load(), "committed stream cannot retry")
			events := f.events(t)
			require.Len(t, events, 1)
			require.Equal(t, "failure", events[0].Result)
			require.True(t, events[0].Stream)
			require.True(t, events[0].Counted)
			var out types.ZTAPIHealthOutcome
			require.NoError(t, common.UnmarshalJsonStr(events[0].Outcome, &out))
			require.Len(t, out.Attempts, 1)
			require.True(t, out.HasText)
			if tt.viaResponses {
				require.Equal(t, "responses", out.UpstreamProtocol)
				require.Equal(t, "failed", out.TerminalStatus)
				require.Equal(t, "upstream_unavailable", out.ProviderErrorCode)
			}
		})
	}
}
