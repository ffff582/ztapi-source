package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestZTAPICustomerAlertChineseText(t *testing.T) {
	item := ZTAPIHealthWorkItem{Kind: "customer_alert", ID: 55}
	require.NoError(t, common.UnmarshalJsonStr(`{"model":"zt-claude-haiku-4.5","username":"teemo","user_id":154,"channel_id":2,"channel_name":"Yunxin pool","error_code":"upstream_http_error","http_status":502,"opened_at":1790841600,"trigger_request_id":"req-client","upstream_request_id":"req-upstream","occurrences":3}`, &item.Alert))
	text := ztapiHealthTelegramText(item, time.Unix(1790841600, 0))
	for _, want := range []string{"【客户调用异常】", "用户：teemo", "模型：zt-claude-haiku-4.5", "号池", "上游服务异常", "502", "req-client", "req-upstream", "3 次", "不代表模型已下架", "Codex"} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, text, "已自动下架")
}

func TestZTAPICustomerAlertDurableDeliveryWithUsernameAndNoProbe(t *testing.T) {
	w, now, probeCalls := healthWorkerFixture(t)
	w.config.ProbeKey = ""
	c := telegramTestConfig(t)
	w.config.TelegramBotToken, w.config.TelegramChatID = c.TelegramBotToken, c.TelegramChatID
	db := w.backend.Probes.DB
	require.NoError(t, model.MigrateZTAPIHealth(db))
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}))
	require.NoError(t, db.Create(&model.User{Id: 154, Username: "teemo"}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 2, Name: "Yunxin pool"}).Error)
	r := model.ZTAPIHealthRequest{ExecutionID: "offline-customer", RequestID: "req-customer", ModelID: 49, Generation: 1, UserID: 154, Source: "real"}
	require.NoError(t, db.Create(&r).Error)
	e := model.ZTAPIHealthEvent{ExecutionID: r.ExecutionID, RequestID: r.RequestID, ModelID: r.ModelID, Generation: r.Generation, PublicModel: "zt-claude-haiku-4.5", Source: "real", ChannelID: 2, Reason: "upstream_http_error", HTTPStatus: 502, UpstreamRequestID: "req-upstream"}
	require.NoError(t, db.Create(&e).Error)
	job := model.ZTAPIHealthOutbox{DedupKey: "customer:49:154:2:1", Kind: "customer_alert", ModelID: 49, Generation: 1, EventID: e.ID, Status: "pending", Occurrences: 2, CreatedAt: now.Unix(), NextAttemptAt: now.Unix()}
	require.NoError(t, db.Create(&job).Error)
	s := model.NewZTAPIHealthStore(db)
	s.Now = w.config.Now
	var err error
	w.backend, err = AttachZTAPIHealthStore(w.backend, s, 999)
	require.NoError(t, err)
	sends := 0
	w.config.AlertTransport = healthWorkerTransport(func(req *http.Request) (*http.Response, error) {
		sends++
		var body struct{ Text string }
		require.NoError(t, common.DecodeJson(req.Body, &body))
		for _, want := range []string{"【客户调用异常】", "用户：teemo", "号池", "req-customer", "req-upstream", "2 次"} {
			require.Contains(t, body.Text, want)
		}
		if sends == 1 {
			return workerResponse(502, `{}`), nil
		}
		return workerResponse(200, `{"ok":true,"result":{"message_id":44,"date":2000000000}}`), nil
	})
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, db.First(&job, job.ID).Error)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, "alert_http_status", job.LastError)
	*now = now.Add(time.Minute)
	w, err = NewZTAPIHealthWorker(w.backend, w.config)
	require.NoError(t, err)
	require.NoError(t, w.RunOnce(context.Background()))
	require.NoError(t, db.First(&job, job.ID).Error)
	require.Equal(t, "done", job.Status)
	require.Equal(t, 2, job.Attempts)
	require.JSONEq(t, `{"ok":true,"result":{"message_id":44,"date":2000000000}}`, job.DeliveryReceipt)
	require.Zero(t, *probeCalls)
	var incidents int64
	require.NoError(t, db.Model(&model.ZTAPIHealthIncident{}).Count(&incidents).Error)
	require.Zero(t, incidents)
}

func TestZTAPICustomerAlertLabelsCannotInjectLinesOrKeys(t *testing.T) {
	item := ZTAPIHealthWorkItem{Kind: "customer_alert", Alert: ZTAPIHealthAlertMetadata{Username: "teemo\n伪造通知", ChannelName: "sk-private-key"}}
	text := ztapiHealthTelegramText(item, time.Now())
	require.NotContains(t, text, "伪造通知")
	require.NotContains(t, text, "sk-private-key")
}

func TestZTAPICustomerMissingMetadataRetriesInsteadOfMisattribution(t *testing.T) {
	c := telegramTestConfig(t)
	c.AlertTransport = healthWorkerTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("missing customer identity must not produce a misleading alert")
		return nil, nil
	})
	ok, code := sendZTAPIHealthAlert(context.Background(), c, ZTAPIHealthWorkItem{Kind: "customer_alert"})
	require.False(t, ok)
	require.Equal(t, "customer_alert_metadata_unavailable", code)
}
