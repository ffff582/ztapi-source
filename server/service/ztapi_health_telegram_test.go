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
		for _, want := range []string{"zt-test", "consecutive_2", "content_filter", "request_id", "trigger_time"} {
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
		require.Contains(t, string(body), "TEST ALERT - NO MODEL WAS CHANGED")
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
	require.Contains(t, message, "modality: video")
	require.Contains(t, message, "operation: video_fetch")
	require.Contains(t, message, "latency_ms: 731")
	require.Contains(t, message, "result_valid: false")
	require.Contains(t, message, "content_filter")
	require.Contains(t, message, now.UTC().Format(time.RFC3339))
	item.Alert.UpstreamRequestID = "sk-private-key"
	require.NotContains(t, ztapiHealthTelegramText(item, *now), "sk-private-key")
}
