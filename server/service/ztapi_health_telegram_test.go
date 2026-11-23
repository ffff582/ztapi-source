package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func telegramTestConfig(t *testing.T) ZTAPIHealthWorkerConfig {
	t.Helper()
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN", "123456:offline_test_token")
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_CHAT_ID", "123456789")
	t.Setenv("ZTAPI_HEALTH_ALERT_WEBHOOK_URL", "")
	c, err := ZTAPIHealthWorkerConfigFromEnv()
	require.NoError(t, err)
	c.Now = func() time.Time { return time.Unix(2000000000, 0) }
	return c
}

func TestZTAPIHealthTelegramEnvironmentAndPayload(t *testing.T) {
	c := telegramTestConfig(t)
	calls := 0
	c.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "https", r.URL.Scheme)
		require.Equal(t, "api.telegram.org", r.URL.Host)
		require.Equal(t, "/bot123456:offline_test_token/sendMessage", r.URL.Path)
		var body map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &body))
		require.Equal(t, "123456789", body["chat_id"])
		text := body["text"].(string)
		for _, want := range []string{"【ZTAPI 模型下架】", "zt-test", "连续 2 次失败", "content_filter", "诊断上游请求编号", "发生时间（北京时间）", "建议处理：请询问上游"} {
			require.Contains(t, text, want)
		}
		require.NotContains(t, text, "offline_test_token")
		require.NotContains(t, body, "parse_mode")
		return workerResponse(200, `{"ok":true,"result":{"message_id":42,"date":2000000000,"chat":{"id":123456789},"text":"ignored"}}`), nil
	})
	ok, code := sendZTAPIHealthAlert(context.Background(), c, ZTAPIHealthWorkItem{ID: 9, Alert: ZTAPIHealthAlertMetadata{Model: "zt-test", Rule: "consecutive_2", FinishReasons: []string{"content_filter"}}})
	require.True(t, ok, code)
	require.Equal(t, "telegram_accepted", code)
	require.Equal(t, 1, calls)
}

func TestZTAPIHealthTelegramRejectsFalseSuccessAndSecretErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"false_ok", 200, `{"ok":false,"description":"secret"}`},
		{"missing_message_id", 200, `{"ok":true,"result":{}}`},
		{"invalid_json", 200, "not-json"},
		{"negative_message_id", 200, `{"ok":true,"result":{"message_id":-1}}`},
		{"redirect", 302, ""},
		{"rate_limit", 429, `{"ok":false,"parameters":{"retry_after":60}}`},
		{"too_large", 200, strings.Repeat("x", 65537)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := telegramTestConfig(t)
			c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) { return workerResponse(tc.status, tc.body), nil })
			ok, code := sendZTAPIHealthAlert(context.Background(), c, ZTAPIHealthWorkItem{})
			require.False(t, ok)
			require.NotContains(t, code, "secret")
		})
	}
	c := telegramTestConfig(t)
	c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://api.telegram.org/bot123456:offline_test_token/sendMessage")
	})
	ok, code := sendZTAPIHealthAlert(context.Background(), c, ZTAPIHealthWorkItem{})
	require.False(t, ok)
	require.Equal(t, "alert_transport_error", code)
}

func TestZTAPIHealthTelegramPartialConfigurationDoesNotFallback(t *testing.T) {
	c := telegramTestConfig(t)
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_CHAT_ID", "")
	t.Setenv("ZTAPI_HEALTH_ALERT_WEBHOOK_URL", "https://example.invalid/alert")
	c, err := ZTAPIHealthWorkerConfigFromEnv()
	require.NoError(t, err)
	c.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		_, _ = io.ReadAll(r.Body)
		t.Fatal("invalid Telegram must not dispatch to another recipient")
		return nil, nil
	})
	ok, _ := sendZTAPIHealthAlert(context.Background(), c, ZTAPIHealthWorkItem{})
	require.False(t, ok)
	t.Setenv("ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN", "")
	blank, err := ZTAPIHealthWorkerConfigFromEnv()
	require.NoError(t, err)
	require.False(t, validZTAPIHealthAlertRecipient(blank))
}

func TestZTAPIHealthTelegramDurableTestRetryAndReceipt(t *testing.T) {
	w, now, _ := healthWorkerFixture(t)
	c := telegramTestConfig(t)
	w.config.TelegramBotToken, w.config.TelegramChatID = c.TelegramBotToken, c.TelegramChatID
	w.config.ProbeKey = ""
	require.NoError(t, model.MigrateZTAPIHealth(w.backend.Probes.DB))
	s := model.NewZTAPIHealthStore(w.backend.Probes.DB)
	s.Now = w.config.Now
	var err error
	w.backend, err = AttachZTAPIHealthStore(w.backend, s, 999)
	require.NoError(t, err)
	job := model.ZTAPIHealthOutbox{DedupKey: "offline-test", Kind: "alert_test", Status: "pending", CreatedAt: now.Unix(), NextAttemptAt: now.Unix()}
	require.NoError(t, s.DB.Create(&job).Error)
	calls := 0
	w.config.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "【ZTAPI 测试通知】")
		require.Contains(t, string(body), "告警通道测试成功，本次没有修改或下架任何模型")
		if calls == 1 {
			return workerResponse(200, `{"ok":false,"description":"private text"}`), nil
		}
		return workerResponse(200, `{"ok":true,"result":{"message_id":43,"date":2000000000,"chat":{"id":123456789},"text":"private text"}}`), nil
	})
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, s.DB.First(&job, job.ID).Error)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, "alert_telegram_rejected", job.LastError)
	require.Equal(t, 1, job.Attempts)
	require.Empty(t, job.DeliveryReceipt)
	*now = now.Add(time.Minute)
	// A new worker instance uses the original durable outbox, not an in-memory retry.
	w, err = NewZTAPIHealthWorker(w.backend, w.config)
	require.NoError(t, err)
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, s.DB.First(&job, job.ID).Error)
	require.Equal(t, "done", job.Status)
	require.Equal(t, 2, job.Attempts)
	require.JSONEq(t, `{"ok":true,"result":{"message_id":43,"date":2000000000}}`, job.DeliveryReceipt)
	for _, table := range []any{&model.ZTAPIHealthState{}, &model.ZTAPIHealthEvent{}, &model.ZTAPIHealthIncident{}} {
		var count int64
		require.NoError(t, s.DB.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestZTAPIHealthTelegramIncidentLoadsOriginalRequestID(t *testing.T) {
	w, now, _ := healthWorkerFixture(t)
	db := w.backend.Probes.DB
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelConfig{}))
	require.NoError(t, db.Create(&model.ZTAPIModelConfig{ID: 1}).Error)
	event := model.ZTAPIHealthEvent{
		ExecutionID: "offline-trigger", ModelID: 1, Generation: 1,
		Modality: model.ZTAPIModalityVideo, Operation: types.ZTAPIHealthOperationVideoFetch,
		UpstreamRequestID: "req_original_42", UpstreamTaskID: "task_original_43",
		Reason: "empty_output", HTTPStatus: 200, LatencyMilliseconds: 731, ResultValid: false,
		Outcome: `{"FinishReasons":["content_filter"]}`,
	}
	require.NoError(t, db.Create(&event).Error)
	incident := model.ZTAPIHealthIncident{ModelID: 1, Generation: 1, PublicModel: "zt-fable", Rule: "consecutive_2", OpenedAt: now.Unix(), TriggerEventID: event.ID}
	require.NoError(t, db.Create(&incident).Error)
	s := model.NewZTAPIHealthStore(db)
	item := ZTAPIHealthWorkItem{ModelID: 1, Generation: 1, IncidentID: incident.ID}
	item.Alert = ztapiHealthLoadAlertMetadata(context.Background(), s, item)
	message := ztapiHealthTelegramText(item, *now)
	require.Contains(t, message, "req_original_42")
	require.Contains(t, message, "task_original_43")
	require.Contains(t, message, "类型：视频")
	require.Contains(t, message, "操作：查询视频结果")
	require.Contains(t, message, "耗时：731 毫秒")
	require.Contains(t, message, "结果有效：否")
	require.Contains(t, message, "content_filter")
	require.Contains(t, message, now.In(time.FixedZone("北京时间", 8*60*60)).Format("2006-01-02 15:04:05"))
	require.Contains(t, message, "建议处理：请询问上游为什么触发内容过滤，并把上游请求编号一并发给对方")
	item.Alert.UpstreamRequestID = "sk-private-key"
	require.NotContains(t, ztapiHealthTelegramText(item, *now), "sk-private-key")
}

func TestZTAPIHealthTelegramDistinguishesRouteModelAndRecovery(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	openedAt := now.Unix()
	status := 502
	metadata := ZTAPIHealthAlertMetadata{
		MetadataStatus: "available", Model: "zt-gpt-test", Rule: "verified_route_2",
		Modality: model.ZTAPIModalityText, ErrorCode: "upstream_http_error", HTTPStatus: &status,
		FinishReasons: []string{"unknown"}, UpstreamRequestID: "req_upstream_probe",
		ProbeRequestID: "ztapi-health-verify-case", TriggerRequestID: "req_customer_trigger", OpenedAt: &openedAt,
		ChannelID: 7, EntryProtocol: "chat", UpstreamProtocol: "responses",
	}
	routeMessage := ztapiHealthTelegramText(ZTAPIHealthWorkItem{Kind: "route_alert", ID: 101, ModelID: 1, Alert: metadata}, now)
	for _, want := range []string{
		"【ZTAPI 线路降级】", "模型仍可正常调用", "故障线路：通道 #7，客户入口 chat，最终上游 responses，非流式", "独立诊断探针连续 2 次失败", "诊断探针编号：ztapi-health-verify-case",
		"触发用户请求编号：req_customer_trigger（仅用于定位最初线索，不是下架证据）", "建议处理：",
	} {
		require.Contains(t, routeMessage, want)
	}
	require.NotContains(t, routeMessage, "已自动下架")

	metadata.Rule = "verified_all_routes"
	modelMessage := ztapiHealthTelegramText(ZTAPIHealthWorkItem{Kind: "alert", ID: 102, ModelID: 1, IncidentID: 9, Alert: metadata}, now)
	for _, want := range []string{"【ZTAPI 模型下架】", "所有已授权线路均经独立探针确认不可用", "已自动下架"} {
		require.Contains(t, modelMessage, want)
	}

	metadata.Rule = "manual_verified_recovery"
	recoveryMessage := ztapiHealthTelegramText(ZTAPIHealthWorkItem{Kind: "recovery_alert", ID: 103, ModelID: 1, IncidentID: 9, Alert: metadata}, now)
	for _, want := range []string{"【ZTAPI 复核恢复】", "故障状态已由管理员复核关闭", "仍需管理员重新上架"} {
		require.Contains(t, recoveryMessage, want)
	}
}

func TestZTAPIHealthRouteAlertLoadsPersistedProbeEvidence(t *testing.T) {
	w, now, _ := healthWorkerFixture(t)
	db := w.backend.Probes.DB
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelConfig{}))
	publicName := "zt-gpt-5.6-sol"
	require.NoError(t, db.Create(&model.ZTAPIModelConfig{ID: 1, SourceModel: "gpt-5.6-sol", PublicName: &publicName, Published: true}).Error)

	trigger := model.ZTAPIHealthEvent{
		ExecutionID: "customer-trigger-execution", RequestID: "customer-request-42",
		ModelID: 1, Generation: 1, PublicModel: publicName, Source: "real",
		CompletionSequence: 1,
		Modality:           model.ZTAPIModalityText, Result: "failure", Reason: "empty_output",
		HTTPStatus: 200, UpstreamRequestID: "customer-upstream-clue", ResultValid: false,
	}
	require.NoError(t, db.Create(&trigger).Error)
	verification := model.ZTAPIHealthVerificationCase{
		ID: "d88aac42-974f-4a2b-96dc-a2ee529133b4", ModelID: 1, ChannelID: 7,
		EntryProtocol: "chat", Protocol: "chat", Generation: 1, SourceEventID: trigger.ID,
		State: "completed", Attempts: 2, ProbeRequestID: "ztapi-health-verify-42",
		Result: "failure", CreatedAt: now.UnixMilli(), CompletedAt: now.UnixMilli(),
	}
	require.NoError(t, db.Create(&verification).Error)
	otherVerification := model.ZTAPIHealthVerificationCase{
		ID: "a88aac42-974f-4a2b-96dc-a2ee529133b5", ModelID: 1, ChannelID: 8,
		EntryProtocol: "chat", Protocol: "responses", Generation: 1, SourceEventID: trigger.ID,
		State: "completed", Attempts: 2, ProbeRequestID: "ztapi-health-verify-wrong-route",
		Result: "failure", CreatedAt: now.Add(time.Second).UnixMilli(), CompletedAt: now.Add(time.Second).UnixMilli(),
	}
	require.NoError(t, db.Create(&otherVerification).Error)
	probe := model.ZTAPIHealthEvent{
		ExecutionID: "probe-execution", RequestID: verification.ProbeRequestID,
		ModelID: 1, Generation: 1, PublicModel: publicName, Source: "probe",
		CompletionSequence: 2,
		Modality:           model.ZTAPIModalityText, Result: "failure", Reason: "upstream_http_error",
		HTTPStatus: 502, UpstreamRequestID: "probe-upstream-proof", LatencyMilliseconds: 918,
		ResultValid: false, Outcome: `{"FinishReasons":["upstream_error"]}`,
	}
	require.NoError(t, db.Create(&probe).Error)
	wrongProbe := model.ZTAPIHealthEvent{
		ExecutionID: "wrong-probe-execution", RequestID: otherVerification.ProbeRequestID,
		ModelID: 1, Generation: 1, PublicModel: publicName, Source: "probe",
		CompletionSequence: 3, Modality: model.ZTAPIModalityText, Result: "failure", Reason: "upstream_rate_limit",
		HTTPStatus: 429, UpstreamRequestID: "wrong-upstream-proof", ResultValid: false,
	}
	require.NoError(t, db.Create(&wrongProbe).Error)
	outbox := model.ZTAPIHealthOutbox{
		DedupKey: "verification:" + verification.ID + ":route-alert", Kind: "route_alert",
		ModelID: 1, Generation: 1, EventID: trigger.ID, VerificationCaseID: verification.ID, Status: "pending",
		NextAttemptAt: now.Unix(), CreatedAt: now.Unix(),
	}
	require.NoError(t, db.Create(&outbox).Error)

	store := model.NewZTAPIHealthStore(db)
	store.Now = w.config.Now
	backend, err := AttachZTAPIHealthStore(w.backend, store, 999)
	require.NoError(t, err)
	items, err := backend.ClaimOutbox(context.Background(), "route_alert", *now, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	item := items[0]
	require.Equal(t, "route_alert", item.Kind)
	require.Equal(t, verification.ProbeRequestID, item.Alert.ProbeRequestID)
	require.Equal(t, probe.UpstreamRequestID, item.Alert.UpstreamRequestID)
	require.Equal(t, trigger.RequestID, item.Alert.TriggerRequestID)
	require.Equal(t, trigger.UpstreamRequestID, item.Alert.TriggerUpstreamRequestID)
	require.Equal(t, verification.ChannelID, item.Alert.ChannelID)
	require.Equal(t, verification.EntryProtocol, item.Alert.EntryProtocol)
	require.Equal(t, verification.Protocol, item.Alert.UpstreamProtocol)
	require.Equal(t, 7, item.Alert.ChannelID)
	require.Equal(t, "chat", item.Alert.EntryProtocol)
	require.Equal(t, "chat", item.Alert.UpstreamProtocol)
	require.NotEqual(t, otherVerification.ProbeRequestID, item.Alert.ProbeRequestID)
	require.NotEqual(t, wrongProbe.UpstreamRequestID, item.Alert.UpstreamRequestID)

	message := ztapiHealthTelegramText(item, *now)
	for _, want := range []string{
		"【ZTAPI 线路降级】", "错误：upstream_http_error", "HTTP 状态：502",
		"诊断探针编号：ztapi-health-verify-42", "诊断上游请求编号：probe-upstream-proof",
		"故障线路：通道 #7，客户入口 chat，最终上游 chat，非流式",
		"触发用户请求编号：customer-request-42（仅用于定位最初线索，不是下架证据）",
	} {
		require.Contains(t, message, want)
	}
	require.NotContains(t, message, "已自动下架")
}
