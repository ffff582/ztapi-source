package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	healthmodel "github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type healthRoundTripper func(*http.Request) (*http.Response, error)

func (f healthRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestZTAPIHealthVerificationPinRejectsBeforeAnyUpstreamHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"zt-model"}`))
	c.Set("channel_id", 9)
	c.Set("original_model", "zt-model")
	c.Set(base.RequestIdKey, "ztapi-health:case-1:1")
	c.Request.Header.Set("X-Request-ID", "ztapi-health:case-1:1")

	info := &relaycommon.RelayInfo{OriginModelName: "zt-model", UserId: 42, ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "current-secret"}}
	req, err := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/chat/completions", strings.NewReader(`{"model":"upstream-model"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer current-secret")
	credentialVersion, err := relaycommon.ZTAPIHealthCredentialVersion(ztapiHealthCredentialMaterial(info, req.Header))
	require.NoError(t, err)
	relaycommon.SetZTAPIHealthProbePin(c, &relaycommon.ZTAPIHealthProbePin{
		CaseID: "case-1", LeaseToken: "lease-1", RequestID: "ztapi-health:case-1:1",
		ModelID: 8, PublicModel: "zt-model", ChannelID: 9, EntryProtocol: "chat", Protocol: "chat",
		CredentialVersion: credentialVersion, Generation: 3,
	})

	validated, admitted := 0, 0
	_, err = relaycommon.StartZTAPIHealthRequest(c, info, relaycommon.ZTAPIHealthBackend{
		AdmitRequest: func(_ context.Context, _, executionID, requestID string, _ int, stream bool) (*types.ZTAPIHealthTicket, error) {
			return &types.ZTAPIHealthTicket{ExecutionID: executionID, RequestID: requestID, ModelID: 8, Generation: 3, PublicModel: "zt-model", EntryProtocol: "chat", Stream: stream, Source: "probe"}, nil
		},
		ValidateProbeRoute: func(context.Context, relaycommon.ZTAPIHealthProbeRouteCheck) error {
			validated++
			return errors.New("route changed before dispatch")
		},
		AdmitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string, string) error {
			admitted++
			return nil
		},
	})
	require.NoError(t, err)

	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	client := service.GetHttpClient()
	oldTransport := client.Transport
	t.Cleanup(func() { client.Transport = oldTransport })
	sends := 0
	client.Transport = healthRoundTripper(func(*http.Request) (*http.Response, error) {
		sends++
		return nil, errors.New("must not send")
	})

	response, requestErr := DoRequest(c, req, info)
	require.Nil(t, response)
	require.Error(t, requestErr)
	require.Equal(t, 1, validated)
	require.Zero(t, admitted)
	require.Zero(t, sends)
}

type healthHeaderOverrideAdaptor struct {
	Adaptor
	url string
}

func (a healthHeaderOverrideAdaptor) GetRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.url, nil
}

func (healthHeaderOverrideAdaptor) SetupRequestHeader(_ *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	header.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

func TestZTAPIHealthDoRequestPersistsActualOutboundCredentialFingerprintWithoutRawKey(t *testing.T) {
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "health-route.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&healthmodel.ZTAPIModelConfig{}, &healthmodel.ZTAPIAuditEvent{}, &healthmodel.ZTAPICatalogLock{}))
	require.NoError(t, healthmodel.MigrateZTAPIHealth(db))
	publicName := "zt-health-route"
	config := healthmodel.ZTAPIModelConfig{SourceModel: "upstream-health-route", PublicName: &publicName, Published: true, Version: 7, EnabledGroups: `["default"]`}
	require.NoError(t, db.Create(&config).Error)
	store := healthmodel.NewZTAPIHealthStore(db)
	store.Now = func() time.Time { return time.Unix(2_000_000_000, 0).UTC() }

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("private inbound"))
	c.Request.Header.Set("X-Customer-Trace", "customer-header-must-not-be-health-identity")
	c.Set("channel_id", 7)
	c.Set("original_model", publicName)
	c.Set(base.RequestIdKey, "customer-request-id")
	rawKeys := []string{"raw-first-route-key-that-must-never-persist", "raw-final-route-key-that-must-never-persist"}
	overriddenAuthorization := []string{"Bearer effective-first-route-credential", "Bearer effective-final-route-credential"}
	info := &relaycommon.RelayInfo{OriginModelName: publicName, UserId: 2, ChannelMeta: &relaycommon.ChannelMeta{ApiKey: rawKeys[0]}}
	session, err := relaycommon.StartZTAPIHealthRequest(c, info, relaycommon.ZTAPIHealthBackend{
		AdmitRequest:  store.AdmitRequest,
		AdmitAttempt:  store.AdmitAttempt,
		RecordOutcome: store.RecordOutcome,
		CheckAvailable: func(modelName string) error {
			return store.CheckAvailable(context.Background(), modelName)
		},
		CircuitOpen: healthmodel.ErrZTAPIHealthCircuitOpen,
	})
	require.NoError(t, err)

	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	client := service.GetHttpClient()
	oldTransport := client.Transport
	t.Cleanup(func() { client.Transport = oldTransport })
	sends := 0
	client.Transport = healthRoundTripper(func(request *http.Request) (*http.Response, error) {
		sends++
		status := http.StatusOK
		body := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"customer answer"}]}]}`
		if sends == 1 {
			status = http.StatusBadGateway
			body = `{"error":{"code":"server_error"}}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})

	for i, rawKey := range rawKeys {
		session.PrepareRelayAttempt()
		info.ApiKey = rawKey
		if i == 0 {
			info.HeadersOverride = map[string]any{"*": "", "AUTHORIZATION": overriddenAuthorization[i]}
			info.UseRuntimeHeadersOverride = false
			info.RuntimeHeadersOverride = nil
		} else {
			info.UseRuntimeHeadersOverride = true
			info.RuntimeHeadersOverride = map[string]any{"authorization": overriddenAuthorization[i]}
		}
		response, requestErr := DoApiRequest(healthHeaderOverrideAdaptor{url: "https://upstream.invalid/v1/responses"}, c, info, strings.NewReader("private upstream"))
		require.NoError(t, requestErr)
		_, requestErr = io.Copy(io.Discard, response.Body)
		require.NoError(t, requestErr)
		require.NoError(t, response.Body.Close())
	}
	require.NoError(t, session.Finalize(c.Request.Context(), false))

	var event healthmodel.ZTAPIHealthEvent
	require.NoError(t, db.First(&event).Error)
	expectedDigests := make([]string, len(rawKeys))
	for i, rawKey := range rawKeys {
		digest := sha256.Sum256([]byte("authorization\x00" + overriddenAuthorization[i]))
		expectedDigests[i] = hex.EncodeToString(digest[:])
		require.NotContains(t, event.Outcome, rawKey)
		require.NotContains(t, event.Outcome, overriddenAuthorization[i])
		baseDigest := sha256.Sum256([]byte(rawKey))
		require.NotEqual(t, hex.EncodeToString(baseDigest[:]), expectedDigests[i], "header override must replace the route credential identity")
	}
	require.Equal(t, expectedDigests[1], event.CredentialVersion)
	require.Contains(t, event.Outcome, expectedDigests[1])
	var persistedOutcome map[string]any
	require.NoError(t, json.Unmarshal([]byte(event.Outcome), &persistedOutcome))
	require.Equal(t, expectedDigests[1], persistedOutcome["CredentialVersion"])
	attempts, ok := persistedOutcome["Attempts"].([]any)
	require.True(t, ok)
	require.Len(t, attempts, 2)
	for i, persistedAttempt := range attempts {
		attempt, ok := persistedAttempt.(map[string]any)
		require.True(t, ok)
		require.Equal(t, expectedDigests[i], attempt["CredentialVersion"])
	}
	persistedEvent, err := json.Marshal(event)
	require.NoError(t, err)
	for _, rawKey := range rawKeys {
		require.NotContains(t, string(persistedEvent), rawKey)
	}
	for _, authorization := range overriddenAuthorization {
		require.NotContains(t, string(persistedEvent), authorization)
	}
	require.NotContains(t, string(persistedEvent), "customer-header-must-not-be-health-identity")
}

func TestZTAPIHealthCredentialMaterialCoversCustomAndRealtimeOverrides(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)

	t.Run("custom authentication header", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: "unused-base-key",
			ChannelSetting: dto.ChannelSettings{
				ZTAPIHealthCredentialHeaders: []string{"X-Upstream-Auth"},
			},
			HeadersOverride: map[string]any{
				"Authorization":   "",
				"X-Upstream-Auth": "custom-effective-credential",
			},
		}}
		resolved, err := processHeaderOverride(info, c)
		require.NoError(t, err)
		header := http.Header{}
		applyHeaderOverrideToRequest(&http.Request{Header: header}, resolved)
		require.Equal(t, "x-upstream-auth\x00custom-effective-credential", ztapiHealthCredentialMaterial(info, header))
	})

	t.Run("runtime customer passthrough does not change route identity", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			RequestHeaders: map[string]string{"Originator": "client-a", "Session_Id": "session-a"},
			ChannelMeta: &relaycommon.ChannelMeta{
				ApiKey: "stable-channel-key",
				ParamOverride: map[string]any{"operations": []any{
					map[string]any{"mode": "pass_headers", "value": []any{"Originator", "Session_Id"}},
				}},
			},
		}
		_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"test"}`), info)
		require.NoError(t, err)
		resolved, err := processHeaderOverride(info, c)
		require.NoError(t, err)
		header := http.Header{"Authorization": []string{"Bearer stable-channel-key"}}
		applyHeaderOverrideToRequest(&http.Request{Header: header}, resolved)
		require.Equal(t, "authorization\x00Bearer stable-channel-key", ztapiHealthCredentialMaterial(info, header))

		info.RequestHeaders = map[string]string{"Originator": "client-b", "Session_Id": "session-b"}
		_, err = relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"test"}`), info)
		require.NoError(t, err)
		resolved, err = processHeaderOverride(info, c)
		require.NoError(t, err)
		header = http.Header{"Authorization": []string{"Bearer stable-channel-key"}}
		applyHeaderOverrideToRequest(&http.Request{Header: header}, resolved)
		require.Equal(t, "authorization\x00Bearer stable-channel-key", ztapiHealthCredentialMaterial(info, header))
	})

	t.Run("ordinary static header override does not change route identity", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: "stable-channel-key",
			HeadersOverride: map[string]any{
				"Authorization": "Bearer stable-channel-key",
				"User-Agent":    "channel-specific-client",
				"X-Trace-Tag":   "diagnostic-only",
			},
		}}
		resolved, err := processHeaderOverride(info, c)
		require.NoError(t, err)
		header := http.Header{}
		applyHeaderOverrideToRequest(&http.Request{Header: header}, resolved)
		require.Equal(t, "authorization\x00Bearer stable-channel-key", ztapiHealthCredentialMaterial(info, header))
	})

	t.Run("custom header only counts when explicitly declared", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "fallback-key"}}
		header := http.Header{"X-Upstream-Auth": []string{"custom-credential"}}
		require.Equal(t, "fallback-key", ztapiHealthCredentialMaterial(info, header))

		info.ChannelSetting.ZTAPIHealthCredentialHeaders = []string{"X-Upstream-Auth"}
		require.Equal(t, "x-upstream-auth\x00custom-credential", ztapiHealthCredentialMaterial(info, header))
	})

	t.Run("realtime websocket subprotocol", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: "unused-base-key",
			HeadersOverride: map[string]any{
				"sec-websocket-protocol": "realtime,openai-insecure-api-key.effective-realtime-key,openai-beta.realtime-v1",
			},
		}}
		resolved, err := processHeaderOverride(info, c)
		require.NoError(t, err)
		header := http.Header{}
		applyHeaderOverrideToRequest(&http.Request{Header: header}, resolved)
		require.Equal(t,
			"sec-websocket-protocol\x00openai-insecure-api-key.effective-realtime-key",
			ztapiHealthCredentialMaterial(info, header),
		)
	})
}

func TestZTAPIHealthDoRequestObservesRetryOnceWithoutNetwork(t *testing.T) {
	for _, test := range []string{"retry", "truncated", "transport_error", "cancelled", "admission_denied"} {
		t.Run(test, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("private inbound")).WithContext(ctx)
			c.Set("channel_id", 7)
			c.Set("original_model", "public-model")
			c.Set(base.RequestIdKey, "client-supplied-repeat")
			c.Request.Header.Set("X-ZTAPI-Health-Source", "probe")
			info := &relaycommon.RelayInfo{OriginModelName: "public-model", UserId: 2, IsStream: test == "truncated", ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "test-upstream-key"}}
			var outcome types.ZTAPIHealthOutcome
			var recorded, admitted, sends int
			circuit := errors.New("circuit open")
			s, err := relaycommon.StartZTAPIHealthRequest(c, info, relaycommon.ZTAPIHealthBackend{
				AdmitRequest: func(ctx context.Context, requested, id, requestID string, user int, stream bool) (*types.ZTAPIHealthTicket, error) {
					if id == requestID || len(id) != 36 || requestID != "client-supplied-repeat" {
						t.Fatalf("bad execution identity %q %q", id, requestID)
					}
					return &types.ZTAPIHealthTicket{ExecutionID: id, RequestID: requestID, Source: "real", Stream: stream}, nil
				},
				AdmitAttempt: func(ctx context.Context, ticket *types.ZTAPIHealthTicket, id int, protocol, credentialVersion string) error {
					admitted++
					if protocol != "responses" || id != 7 {
						t.Fatalf("outbound identity %s %d", protocol, id)
					}
					if credentialVersion == "" {
						t.Fatal("outbound credential fingerprint is empty")
					}
					if test == "admission_denied" {
						return circuit
					}
					return nil
				},
				RecordOutcome: func(ctx context.Context, ticket *types.ZTAPIHealthTicket, o types.ZTAPIHealthOutcome) error {
					recorded++
					outcome = o
					return nil
				},
				CircuitOpen: circuit,
			})
			if err != nil {
				t.Fatal(err)
			}
			if service.GetHttpClient() == nil {
				service.InitHttpClient()
			}
			client := service.GetHttpClient()
			old := client.Transport
			t.Cleanup(func() { client.Transport = old })
			client.Transport = healthRoundTripper(func(r *http.Request) (*http.Response, error) {
				sends++
				if test == "transport_error" {
					return nil, io.ErrUnexpectedEOF
				}
				status := 200
				body := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"customer answer"}]}]}`
				headers := http.Header{"X-Request-Id": {"upstream-id"}}
				if test == "retry" && sends == 1 {
					status = 502
					body = `{"error":{"code":"server_error"}}`
				}
				if test == "truncated" {
					headers.Set("Content-Type", "text/event-stream")
					body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			if test == "cancelled" {
				cancel()
			}
			attempts := 1
			if test == "retry" {
				attempts = 2
			}
			var requestErr error
			for i := 0; i < attempts; i++ {
				s.PrepareRelayAttempt()
				req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/responses", strings.NewReader("private upstream"))
				resp, e := DoRequest(c, req, info)
				requestErr = e
				if resp != nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			}
			if err := s.Finalize(c.Request.Context(), requestErr != nil); err != nil {
				t.Fatal(err)
			}
			_ = s.Finalize(c.Request.Context(), false)
			if recorded != 1 {
				t.Fatalf("recorded %d times", recorded)
			}
			switch test {
			case "retry":
				if outcome.Result != "success" || len(outcome.Attempts) != 2 || admitted != 2 || outcome.Attempts[0].HTTPStatus != 502 || outcome.HTTPStatus != 200 {
					t.Fatalf("retry outcome %+v", outcome)
				}
			case "truncated", "transport_error":
				if outcome.Result != "failure" {
					t.Fatalf("failure became %+v", outcome)
				}
			case "cancelled":
				if sends != 0 || outcome.Result != "excluded" || outcome.Dispatched {
					t.Fatalf("cancel dispatched=%d %+v", sends, outcome)
				}
			case "admission_denied":
				var e *types.NewAPIError
				if sends != 0 || !errors.As(requestErr, &e) || e.StatusCode != 503 {
					t.Fatalf("gate sends=%d err=%v", sends, requestErr)
				}
			}
		})
	}
}
