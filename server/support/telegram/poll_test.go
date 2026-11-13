// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func failPoll(h *harness, status int) {
	base := h.client.http.Transport
	h.client.http.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			if status == 0 {
				return nil, errors.New("private transport detail " + testToken)
			}
			return testResponse(status, `{"ok":false,"error_code":`+strconv.Itoa(status)+`}`), nil
		}
		return base.RoundTrip(r)
	})
}

func TestPollTransientDoesNotAlertOrInfer(t *testing.T) {
	h := newHarness(t)
	failPoll(h, 0)
	if err := h.worker.Step(context.Background()); err == nil {
		t.Fatal("transport failure was hidden")
	}
	if len(h.attempts) != 0 || h.provider.calls != 0 {
		t.Fatal("one transient poll failure sent an outage alert or invoked inference")
	}
}

func TestPollIncidentRecoveryPersistsAndNextIncidentGetsNewIdentity(t *testing.T) {
	h := newHarness(t)
	base := h.client.http.Transport
	failPoll(h, 401)
	_ = h.worker.Step(context.Background())
	if len(h.sent) != 1 || !strings.Contains(h.sent[0].Text, "auth") || h.worker.PollHealth().OK {
		t.Fatal("auth failure not classified or immediately alerted")
	}
	h.client.http.Transport = base
	h.now = h.now.Add(20 * time.Second)
	h.step()
	h.restart()
	h.step()
	if len(h.sent) != 2 || !strings.Contains(h.sent[1].Text, "已恢复") {
		t.Fatalf("missing/duplicate recovery: %+v", h.sent)
	}
	if status := h.worker.PollHealth(); !status.OK || status.ConsecutiveFailures != 0 || status.LastSuccessAt == nil || status.ErrorCategory != "" {
		t.Fatalf("bad recovered status: %+v", status)
	}
	failPoll(h, 409)
	_ = h.worker.Step(context.Background())
	if len(h.sent) != 3 || !strings.Contains(h.sent[2].Text, "P000002") || !strings.Contains(h.sent[2].Text, "receiver_conflict") {
		t.Fatal("new incident reused prior notification")
	}
	if h.provider.calls != 0 {
		t.Fatal("status monitoring called inference")
	}
}

func TestPollAmbiguousStatusIsNotResentAfterRestart(t *testing.T) {
	h := newHarness(t)
	base := h.client.http.Transport
	attempts := 0
	h.client.http.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			return testResponse(401, `{"ok":false,"error_code":401}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			attempts++
			return nil, errors.New("response lost " + testToken)
		}
		return base.RoundTrip(r)
	})
	_ = h.worker.Step(context.Background())
	h.now = h.now.Add(time.Minute)
	h.restart()
	_ = h.worker.Step(context.Background())
	var row outbox
	if err := h.db.Where("key = ?", "poll_alert:1").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || row.State != "delivery_unknown" || row.Attempts != 1 {
		t.Fatalf("ambiguous notification replayed: attempts=%d row=%+v", attempts, row)
	}
	h.client.http.Transport = base
	h.step()
	if len(h.sent) != 1 || !strings.Contains(h.sent[0].Text, "未确认送达") {
		t.Fatal("recovery lost ambiguous-send evidence")
	}
}

func TestPollCrashDuringDispatchIsNotReplayed(t *testing.T) {
	h := newHarness(t)
	row := outbox{Key: "poll_alert:88", Kind: "poll_alert", ChatID: h.cfg.OwnerID, Text: "status", State: "dispatching", Attempts: 1, CreatedAt: h.now, NextAttemptAt: h.now}
	if err := h.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.step()
	if err := h.db.Where("key = ?", row.Key).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "delivery_unknown" || len(h.attempts) != 0 {
		t.Fatal("crash caused ambiguous status replay")
	}
}

func TestPollExplicitRateLimitRetriesAndRecoverySupersedesUnsentAlert(t *testing.T) {
	h := newHarness(t)
	base := h.client.http.Transport
	failPoll(h, 401)
	h.sendFailures, h.retryAfter = 1, 120
	_ = h.worker.Step(context.Background())
	var row outbox
	if err := h.db.Where("key = ?", "poll_alert:1").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "pending" || row.NextAttemptAt.Before(h.now.Add(120*time.Second)) {
		t.Fatal("explicit rate limit lost retry-after")
	}
	h.now = h.now.Add(time.Second)
	_ = h.worker.Step(context.Background())
	if len(h.attempts) != 1 {
		t.Fatal("rate limit retried too soon")
	}
	h.client.http.Transport = base
	h.step()
	h.now = h.now.Add(5 * time.Minute)
	h.step()
	if len(h.sent) != 1 || !strings.Contains(h.sent[0].Text, "已恢复") {
		t.Fatal("stale outage delivered after recovery")
	}
	if err := h.db.Where("key = ?", row.Key).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "suppressed" {
		t.Fatal("unsent stale incident remained pending")
	}
}

func TestPollHealthTracksStallWithoutBlockingAndAllowsActiveWork(t *testing.T) {
	h := newHarness(t)
	if h.worker.PollHealth().OK {
		t.Fatal("unproven startup reported healthy")
	}
	h.step()
	h.now = h.now.Add(4 * time.Minute)
	if h.worker.PollHealth().OK {
		t.Fatal("stalled receiver reported healthy")
	}
	h.worker.pollTouch("processing")
	if !h.worker.PollHealth().OK {
		t.Fatal("active work misreported as failed polling")
	}
	h.now = h.now.Add(91 * time.Second)
	if h.worker.PollHealth().OK {
		t.Fatal("hung processing remained healthy")
	}
	h.worker.mu.Lock()
	done := make(chan struct{})
	go func() { _ = h.worker.PollHealth(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("health blocks on poll mutex")
	}
	h.worker.mu.Unlock()
}

func TestPollBackoffHonorsRetryAfterAndIsBounded(t *testing.T) {
	h := newHarness(t)
	for failures := 1; failures <= 20; failures++ {
		h.worker.poll.Failures = failures
		for i := 0; i < 20; i++ {
			delay := h.worker.pollRetryDelay(&APIError{Category: "network"})
			if delay < time.Second || delay > 30*time.Second {
				t.Fatalf("unsafe retry %v", delay)
			}
		}
	}
	if delay := h.worker.pollRetryDelay(&APIError{RetryAfter: 5 * time.Minute}); delay < 5*time.Minute {
		t.Fatal("poll ignored retry-after")
	}
}

func TestPollHealthConcurrentWithRealStateUpdates(t *testing.T) {
	h := newHarness(t)
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for i := 0; i < 1000; i++ {
			_ = h.worker.PollHealth()
		}
	}()
	for i := 0; i < 10; i++ {
		h.step()
	}
	readers.Wait()
}

func TestPollStatusTargetsOnlyOwner(t *testing.T) {
	h := newHarness(t)
	row := outbox{Key: "poll_alert:wrong", Kind: "poll_alert", ChatID: -99, Text: "private status", State: "pending", CreatedAt: h.now, NextAttemptAt: h.now}
	if err := h.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.worker.flush(context.Background()); err == nil || len(h.attempts) != 0 {
		t.Fatal("private poll status reached group")
	}
}

func TestPollRestartRequiresFreshSuccessAndStorageBlockIsUnhealthy(t *testing.T) {
	h := newHarness(t)
	h.step()
	h.now = h.now.Add(time.Second)
	h.restart()
	if h.worker.PollHealth().OK {
		t.Fatal("restart trusted prior process poll instead of verifying this receiver")
	}
	h.step()
	h.worker.cfg.AdmitWrite = func() error { return errors.New("private storage detail " + testToken) }
	if err := h.worker.Step(context.Background()); err == nil {
		t.Fatal("storage block ignored")
	}
	if status := h.worker.PollHealth(); status.OK || status.State != "blocked" {
		t.Fatal("blocked worker reported healthy")
	}
}

func TestPollOutageRequiresDurationAndCount(t *testing.T) {
	h := newHarness(t)
	failPoll(h, 503)
	for _, elapsed := range []time.Duration{0, 30 * time.Second} {
		h.now = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC).Add(elapsed)
		_ = h.worker.Step(context.Background())
		if len(h.attempts) != 0 {
			t.Fatal("outage alert sent before threshold")
		}
	}
	h.now = h.now.Add(30 * time.Second)
	_ = h.worker.Step(context.Background())
	if len(h.sent) != 1 || h.sent[0].ChatID != h.cfg.OwnerID {
		t.Fatalf("sustained outage did not send one private alert: %+v", h.sent)
	}
	h.now = h.now.Add(time.Hour)
	h.restart()
	_ = h.worker.Step(context.Background())
	if len(h.sent) != 1 || h.provider.calls != 0 {
		t.Fatal("restart or bucket change duplicated outage/model")
	}
}
