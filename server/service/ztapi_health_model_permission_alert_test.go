package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestZTAPIHealthModelPermissionSurvivesStoredAlertProjection(t *testing.T) {
	w, now, _ := productionWorkerFixture(t)
	db := w.backend.Probes.DB
	incident := model.ZTAPIHealthIncident{ModelID: 1, Generation: 1, PublicModel: "public-1", Rule: "consecutive_2", Failures: 2, ValidSamples: 2, ConsecutiveFailures: 2, WindowStart: now.Add(-time.Hour).Unix(), OpenedAt: now.Unix(), TriggerEventID: 1}
	require.NoError(t, db.Create(&incident).Error)
	event := model.ZTAPIHealthEvent{ID: 1, ExecutionID: "model-permission-alert", ModelID: 1, Generation: 1, CompletionSequence: 1, Reason: "upstream_model_permission", HTTPStatus: 403, UpstreamRequestID: "req-model-permission", Outcome: `{"FinishReasons":[],"ProviderErrorCode":"model_not_granted"}`}
	require.NoError(t, db.Create(&event).Error)
	job := model.ZTAPIHealthOutbox{DedupKey: "model-permission-alert", Kind: "alert", ModelID: 1, Generation: 1, IncidentID: incident.ID, EventID: event.ID, Status: "pending", NextAttemptAt: now.Unix()}
	require.NoError(t, db.Create(&job).Error)
	items, err := w.backend.ClaimOutbox(context.Background(), "alert", *now, time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	for _, destination := range []string{"webhook", "telegram"} {
		t.Run(destination, func(t *testing.T) {
			config := w.config
			config.AlertWebhookURL = "https://alerts.invalid/health"
			config.TelegramConfigured = destination == "telegram"
			if config.TelegramConfigured {
				config.TelegramBotToken = "123456:offline_test_only_token"
				config.TelegramChatID = "42"
			}
			calls := 0
			config.AlertTransport = healthWorkerTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, common.Unmarshal(body, &payload))
				if destination == "telegram" {
					text := payload["text"].(string)
					require.Contains(t, text, "error_code: upstream_model_permission")
					require.Contains(t, text, "http_status: 403")
					require.Contains(t, text, "upstream_request_id: req-model-permission")
					return workerResponse(200, `{"ok":true,"result":{"message_id":1,"date":2000000000}}`), nil
				}
				details := payload["details"].(map[string]any)
				require.Equal(t, "upstream_model_permission", details["error_code"])
				require.EqualValues(t, 403, details["http_status"])
				require.Equal(t, "req-model-permission", details["upstream_request_id"])
				return workerResponse(204, ""), nil
			})
			accepted, _ := sendZTAPIHealthAlert(context.Background(), config, items[0])
			require.True(t, accepted)
			require.Equal(t, 1, calls)
		})
	}
}

func TestZTAPIHealthTelegramPermissionMetadataBoundaries(t *testing.T) {
	for _, status := range []int{0, 99, 100, 599, 600} {
		t.Run(http.StatusText(status)+time.Duration(status).String(), func(t *testing.T) {
			item := ZTAPIHealthWorkItem{Alert: ZTAPIHealthAlertMetadata{Model: "zt-glm-5.2", ErrorCode: "secret-error-must-not-leak", UpstreamRequestID: "sk-secret-must-not-leak"}}
			if status != 0 {
				item.Alert.HTTPStatus = &status
			}
			text := ztapiHealthTelegramText(item, time.Unix(2_000_000_000, 0))
			require.Contains(t, text, "error_code: unknown")
			require.Contains(t, text, "upstream_request_id: unknown")
			require.NotContains(t, text, "must-not-leak")
			if status >= 100 && status <= 599 {
				require.NotContains(t, text, "http_status: unknown")
			} else {
				require.Contains(t, text, "http_status: unknown")
			}
		})
	}
}
