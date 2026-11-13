package router

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This transport is an egress guard, not a replacement for the relay pipeline.
type partialImageLoopbackTransport struct {
	url     string
	base    http.RoundTripper
	blocked *atomic.Int32
}

func (p partialImageLoopbackTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != p.url {
		p.blocked.Add(1)
		return nil, fmt.Errorf("image integration forbids egress to %s", r.URL.Host)
	}
	return p.base.RoundTrip(r)
}

func partialImageProtocol(t *testing.T) string {
	t.Helper()
	fields := map[string]string{}
	for _, key := range []string{"model", "prompt", "n", "size", "quality", "response_format"} {
		fields[key] = types.ZTAPIImageRequestFieldRequired
	}
	fields["response_format"] = types.ZTAPIImageRequestFieldOmit
	p := types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersionV3, ProviderModel: "gpt-image-2",
		EndpointType: types.ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		WireProtocol: types.ZTAPIImageWireProtocolOpenAIImages, ProviderPath: "/v1/images/generations",
		UpstreamRequestFields: fields, EvidenceVersion: types.ZTAPIImageEvidenceVersion,
		RequestIDSource: types.ZTAPIResponseIDSourceHeader, RequestIDKey: "X-Request-ID",
		Capabilities: types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024", "1152x2048", "2048x2048", "4096x4096", "3840x2160"}, Qualities: []string{"low", "high"}, ResponseFormats: []string{"b64_json"}, MinCount: 1, MaxCount: 2, SupportsEdits: true},
		Response:     types.ZTAPIImageResponseContract{Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"b64_json": "b64_json"}},
		Usage:        types.ZTAPIImageUsageContract{UsageField: "usage", Fields: map[string]string{"text_input": "input_tokens_details.text_tokens", "image_input": "input_tokens_details.image_tokens", "image_output": "output_tokens_details.image_tokens"}, TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported"},
		Edit:         &types.ZTAPIImageEditEndpointContract{Method: "POST", Path: "/v1/images/edits", ProviderPath: "/v1/images/edits", ContentType: "multipart/form-data", WireProtocol: types.ZTAPIImageWireProtocolOpenAIImagesEdit, InputField: "image", MaxInputFiles: 15, MaxInputBytes: 20 << 20, MaxTotalInputBytes: 256 << 20, AllowedMimeTypes: []string{"image/png"}, RequestFields: fields},
	}
	// Broader protocol authority makes rejection a retail-policy assertion,
	// rather than an accidental unsupported-provider-selector failure.
	for i, size := range p.Capabilities.Sizes {
		resolution := []string{"1K", "2K", "2K", "4K", "4K"}[i]
		for _, quality := range p.Capabilities.Qualities {
			p.Capabilities.RenderOptions = append(p.Capabilities.RenderOptions, types.ZTAPIImageRenderOption{Size: size, Quality: quality, Resolution: resolution, AspectRatio: size + "-" + quality})
			for _, op := range []string{"generation", "edit"} {
				for n := 1; n <= 2; n++ {
					p.Reservations = append(p.Reservations, types.ZTAPIImageReservationAuthority{Operation: op, Size: size, Quality: quality, ResponseFormat: "b64_json", N: n, MaximumDimensions: map[string]string{"text_input": "20000", "image_input": "20000", "image_output": "20000"}})
				}
			}
		}
	}
	_, raw, err := types.SealZTAPIImageProtocolContract(p)
	require.NoError(t, err)
	return raw
}

func partialImageRouterFixture(t *testing.T, upstream string) (*gorm.DB, *gin.Engine, model.User, model.Token, string) {
	t.Helper()
	mode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(mode) })
	gin.SetMode(gin.TestMode)
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldSQLite, oldMySQL, oldPostgres := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
	oldRedis, oldMemory := common.RedisEnabled, common.MemoryCacheEnabled
	common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = true, false, false
	common.RedisEnabled, common.MemoryCacheEnabled = false, false
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = oldSQLite, oldMySQL, oldPostgres
		common.RedisEnabled, common.MemoryCacheEnabled = oldRedis, oldMemory
		model.InvalidateZTAPIAliasCache()
		require.NoError(t, sqlDB.Close())
	})
	// Initialize the model's private dialect quoting through its public startup
	// API. Force a second in-memory SQLite connection, never an environment DSN.
	oldPath, oldMaster, oldLogType := common.SQLitePath, common.IsMasterNode, common.LogSqlType
	t.Cleanup(func() { common.SQLitePath, common.IsMasterNode, common.LogSqlType = oldPath, oldMaster, oldLogType })
	common.SQLitePath, common.IsMasterNode = "file:"+strings.ReplaceAll(t.Name(), "/", "_")+"_dialect?mode=memory&cache=shared", false
	t.Setenv("LOG_SQL_DSN", "local")
	require.NoError(t, model.InitLogDB())
	dialectDB, err := model.LOG_DB.DB()
	require.NoError(t, err)
	require.NoError(t, dialectDB.Close())
	model.LOG_DB = db
	common.SQLitePath, common.IsMasterNode, common.LogSqlType = oldPath, oldMaster, oldLogType
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}))
	oldBatch, oldQuota := common.BatchUpdateEnabled, common.QuotaPerUnit
	oldRateLimit := setting.ModelRequestRateLimitEnabled
	setting.ModelRequestRateLimitEnabled = false
	t.Cleanup(func() { setting.ModelRequestRateLimitEnabled = oldRateLimit })
	common.BatchUpdateEnabled, common.QuotaPerUnit = false, 500000
	t.Cleanup(func() {
		common.BatchUpdateEnabled, common.QuotaPerUnit = oldBatch, oldQuota
		model.InvalidateZTAPIAliasCache()
	})
	model.InvalidateZTAPIAliasCache()
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.BalanceLedger{}, &model.BillingRefundPending{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}, &model.ZTAPIHealthState{}))
	require.NoError(t, model.MigrateZTAPISupplierRefund(db))
	require.NoError(t, model.MigrateZTAPIAttemptBilling(db))
	require.NoError(t, model.MigrateZTAPISettlementLogOutbox(db))
	require.NoError(t, model.MigrateLogDBZTAPISettlementLogs(db))
	require.NoError(t, model.MigrateZTAPIFinanceAlerts(db))
	user := model.User{Username: "partial-image-router", AffCode: "partial-image-router", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Group: "default", Quota: 1000000}
	require.NoError(t, db.Create(&user).Error)
	key, hash, prefix, err := common.GenerateZTAPIKey()
	require.NoError(t, err)
	token := model.Token{UserId: user.Id, KeyHash: hash, KeyPrefix: prefix, Status: common.TokenStatusEnabled, Name: "synthetic-image-router", ExpiredTime: -1, RemainQuota: user.Quota, UnlimitedQuota: true, Group: "default"}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Id: 41, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "loopback-image-only", Key: "synthetic-not-a-provider-key", BaseURL: &upstream, Models: "gpt-image-2", Group: "default"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "gpt-image-2", ChannelId: channel.Id, Enabled: true, Weight: 1}).Error)
	protocolJSON := partialImageProtocol(t)
	protocol, _, err := types.ParseZTAPIImageProtocolContract(protocolJSON)
	require.NoError(t, err)
	quotation, err := model.ZTAPIQuotationABEntries()
	require.NoError(t, err)
	name := "zt-gp-image-2"
	config := model.ZTAPIModelConfig{ID: 85, SourceModel: "gpt-image-2", PublicName: &name, Protocol: model.ZTAPIProtocolOpenAICompatible, ProviderFamily: model.ZTAPIProviderOpenAI, EnabledGroups: `["default"]`, Published: true, Version: 1, PublicationSnapshotID: 1632}
	require.NoError(t, db.Create(&config).Error)
	source, err := model.BuildZTAPIABImage2PriceSource(quotation, model.ZTAPIModelPriceSource{ModelConfigID: config.ID, SourceModel: config.SourceModel, SourceDocumentChecksum: model.ZTAPIQuotationSHA256}, &protocol, 7, 1787000000)
	require.NoError(t, err)
	source.ID = 1873
	media := source.MediaPriceContractJSON
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&source).Error)
	preview, err := model.BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err, "synthetic procurement prices must pass production validation")
	snapshot := model.ZTAPIModelPublicationSnapshot{ID: 1632, ModelConfigID: config.ID, ModelVersion: config.Version, SourceModel: config.SourceModel, PublicName: name, Protocol: config.Protocol, ProviderFamily: config.ProviderFamily, EnabledGroups: config.EnabledGroups, AllowedChannelIDs: `[41]`, PriceSourceID: source.ID, PricePolicy: source.PricePolicy, MediaPriceContractJSON: media, ImageProtocolContractJSON: protocolJSON, ImageSaleContractJSON: types.ZTAPIImageSalePartial20261109JSON()}
	snapshot.InputPricePerMillion, err = strconv.ParseFloat(preview.InputSaleUSDPerMillion, 64)
	require.NoError(t, err)
	snapshot.OutputPricePerMillion, err = strconv.ParseFloat(preview.OutputSaleUSDPerMillion, 64)
	require.NoError(t, err)
	snapshot.CacheReadRatio, snapshot.CacheCreationRatio, snapshot.CacheCreation5mRatio, snapshot.CacheCreation1hRatio = 1, 1, 1, 1
	snapshot.ImageRatio, snapshot.AudioRatio, snapshot.AudioCompletionRatio = 1, 1, 1
	require.NoError(t, db.Create(&snapshot).Error)
	model.InvalidateZTAPIAliasCache()
	publication, err := model.GetZTAPIRuntimePublication(name)
	require.NoError(t, err, "fixture must be admitted by the real publication loader")
	require.Equal(t, snapshot.ImageSaleContractJSON, publication.ImageSaleContractJSON)
	engine := gin.New()
	engine.Use(middleware.RequestId(), middleware.I18n())
	SetRelayRouter(engine)
	return db, engine, user, token, key
}

func TestZTAPIImageSalePartialHTTPIntegration(t *testing.T) {
	t.Setenv("ZTAPI_HOSTED_SEARCH_ENABLED", "0")
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	translate := common.TranslateMessage
	require.NoError(t, i18n.Init())
	t.Cleanup(func() { common.TranslateMessage = translate })
	service.InitTokenEncoders()
	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	client := service.GetHttpClient()
	oldTransport := client.Transport
	t.Cleanup(func() { client.Transport = oldTransport })
	for _, tc := range []struct {
		name, op, size, quality, prompt string
		n, refs, width, height          int
		allowed                         bool
	}{
		{"generation_1k", "generation", "1024x1024", "low", "a red square", 1, 0, 1, 1, true},
		{"generation_2k_portrait", "generation", "1152x2048", "low", "a red square", 1, 0, 1, 1, true},
		{"edit_1k_max_reference_pixels", "edit", "1024x1024", "low", "a red square", 1, 1, 2048, 2048, true},
		{"reject_4k_generation", "generation", "3840x2160", "low", "a red square", 1, 0, 1, 1, false},
		{"reject_4k_edit", "edit", "3840x2160", "low", "a red square", 1, 1, 1, 1, false},
		{"reject_unsupported_4k_square", "generation", "4096x4096", "low", "a red square", 1, 0, 1, 1, false},
		{"reject_2k_edit", "edit", "1152x2048", "low", "a red square", 1, 1, 1, 1, false},
		{"reject_multiple_images", "generation", "1024x1024", "low", "a red square", 2, 0, 1, 1, false},
		{"reject_multiple_edit_images", "edit", "1024x1024", "low", "a red square", 2, 1, 1, 1, false},
		{"reject_multiple_references", "edit", "1024x1024", "low", "a red square", 1, 2, 1, 1, false},
		{"reject_reference_pixels", "edit", "1024x1024", "low", "a red square", 1, 1, 2049, 2048, false},
		{"reject_high_quality", "generation", "1024x1024", "high", "a red square", 1, 0, 1, 1, false},
		{"reject_other_size", "generation", "2048x2048", "low", "a red square", 1, 0, 1, 1, false},
		{"reject_long_prompt", "generation", "1024x1024", "low", strings.Repeat("hello world ", 2100), 1, 0, 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/v1/images/generations"
			if tc.op == "edit" {
				path = "/v1/images/edits"
			}
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != path {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
				}
				if tc.op == "edit" {
					if err := r.ParseMultipartForm(32 << 20); err != nil {
						t.Errorf("upstream multipart: %v", err)
					} else {
						defer r.MultipartForm.RemoveAll()
						if len(r.MultipartForm.File["image"]) != 1 || r.FormValue("model") != "gpt-image-2" || r.FormValue("size") != tc.size || r.FormValue("quality") != "low" || r.FormValue("n") != "1" {
							t.Error("edit dispatch changed reference count or selectors")
						}
					}
				} else {
					var payload struct {
						Model, Size, Quality string
						N                    int
					}
					if err := common.DecodeJson(r.Body, &payload); err != nil {
						t.Errorf("upstream JSON: %v", err)
					} else if payload.Model != "gpt-image-2" || payload.Size != tc.size || payload.Quality != "low" || payload.N != 1 {
						t.Errorf("generation dispatch changed selectors: %+v", payload)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "synthetic-partial-image")
				_, _ = io.WriteString(w, `{"data":[{"b64_json":"YQ=="}],"usage":{"input_tokens":10,"output_tokens":7,"total_tokens":17,"input_tokens_details":{"text_tokens":10,"image_tokens":0},"output_tokens_details":{"text_tokens":0,"image_tokens":7}}}`)
			}))
			t.Cleanup(upstream.Close)
			transport := &http.Transport{Proxy: nil}
			t.Cleanup(transport.CloseIdleConnections)
			var blocked atomic.Int32
			client.Transport = partialImageLoopbackTransport{url: upstream.URL, base: transport, blocked: &blocked}
			db, engine, user, token, key := partialImageRouterFixture(t, upstream.URL)
			fields := map[string]string{"model": "zt-gp-image-2", "prompt": tc.prompt, "size": tc.size, "quality": tc.quality, "n": strconv.Itoa(tc.n), "response_format": "b64_json"}
			if tc.op == "edit" {
				delete(fields, "response_format")
			}
			var body bytes.Buffer
			contentType := "application/json"
			if tc.op == "edit" {
				writer := multipart.NewWriter(&body)
				for k, v := range fields {
					require.NoError(t, writer.WriteField(k, v))
				}
				for i := 0; i < tc.refs; i++ {
					part, err := writer.CreateFormFile("image", "synthetic.png")
					require.NoError(t, err)
					require.NoError(t, png.Encode(part, image.NewRGBA(image.Rect(0, 0, tc.width, tc.height))))
				}
				require.NoError(t, writer.Close())
				contentType = writer.FormDataContentType()
			} else {
				payload := map[string]any{}
				for k, v := range fields {
					payload[k] = v
				}
				payload["n"] = tc.n
				raw, err := common.Marshal(payload)
				require.NoError(t, err)
				body.Write(raw)
			}
			request := httptest.NewRequest(http.MethodPost, path, &body)
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Authorization", "Bearer "+key)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Zero(t, blocked.Load(), "relay attempted a non-fixture network destination")
			var fresh model.User
			require.NoError(t, db.First(&fresh, user.Id).Error)
			var settlements []model.ZTAPIRequestSettlement
			require.NoError(t, db.Find(&settlements).Error)
			var ledgers []model.BalanceLedger
			require.NoError(t, db.Find(&ledgers).Error)
			var freshToken model.Token
			require.NoError(t, db.First(&freshToken, token.Id).Error)
			if tc.allowed {
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				require.Contains(t, recorder.Body.String(), `"b64_json":"YQ=="`)
				require.EqualValues(t, 1, hits.Load())
				require.Len(t, settlements, 1)
				if settlements[0].Status != model.ZTAPISettlementSettled {
					t.Logf("HTTP settlement status=%s reserved=%d charged=%d missing=%s", settlements[0].Status, settlements[0].ReservedQuota, settlements[0].ChargedQuota, settlements[0].MissingDimensionsJSON)
				}
				require.Equal(t, model.ZTAPISettlementSettled, settlements[0].Status)
				require.EqualValues(t, 150000, settlements[0].ChargedQuota, "0.30 USD at 500000 quota/USD")
				require.Equal(t, user.Quota-150000, fresh.Quota)
				var delta int64
				for _, ledger := range ledgers {
					delta += ledger.Delta
				}
				require.EqualValues(t, -150000, delta)
				var logs int64
				require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logs).Error)
				require.EqualValues(t, 1, logs)
				// Replay the evidence stored by the HTTP settlement, not another
				// client POST (which would correctly represent another purchase).
				row := settlements[0]
				_, err := model.FinalizeZTAPIRequestSettlement(row.OperationID, row.ChargedQuota, row.UsageJSON, row.ChargeDimensionsJSON)
				require.NoError(t, err)
				require.NoError(t, db.First(&fresh, user.Id).Error)
				require.Equal(t, user.Quota-150000, fresh.Quota)
				var ledgerCount int64
				require.NoError(t, db.Model(&model.BalanceLedger{}).Count(&ledgerCount).Error)
				require.EqualValues(t, len(ledgers), ledgerCount)
				require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logs).Error)
				require.EqualValues(t, 1, logs)
				require.EqualValues(t, 1, hits.Load())
			} else {
				require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
				require.Zero(t, hits.Load(), "invalid retail request reached upstream")
				require.Empty(t, settlements, "invalid request must be rejected before reservation")
				require.Empty(t, ledgers, "a reserve followed by refund is not pre-reserve rejection")
				require.Equal(t, user.Quota, fresh.Quota)
				require.Equal(t, token.RemainQuota, freshToken.RemainQuota)
				var attempts int64
				require.NoError(t, db.Model(&model.ZTAPIRequestAttempt{}).Count(&attempts).Error)
				require.Zero(t, attempts)
			}
		})
	}
}
