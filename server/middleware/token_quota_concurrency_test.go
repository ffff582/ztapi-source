package middleware

import (
	"errors"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
)

func TestConcurrentTokenQuotaReservation(t *testing.T) {
	const (
		reservation = 25
		initialUsed = 7
	)
	fixture := setupRelaySecurityFixture(t, "gpt-test")
	fixture.token.RemainQuota = reservation
	fixture.token.UsedQuota = initialUsed
	if err := fixture.db.Model(fixture.token).Updates(map[string]any{
		"remain_quota": reservation,
		"used_quota":   initialUsed,
	}).Error; err != nil {
		t.Fatalf("set finite token quota: %v", err)
	}
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatalf("get SQL database: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)

	common.BatchUpdateEnabled = true
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			relayInfo := &relaycommon.RelayInfo{
				TokenId:        fixture.token.Id,
				TokenKeyHash:   fixture.token.KeyHash,
				TokenUnlimited: false,
			}
			ready <- struct{}{}
			<-start
			results <- service.PreConsumeTokenQuota(relayInfo, reservation)
		}()
	}
	<-ready
	<-ready
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	insufficient := 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, model.ErrInsufficientTokenQuota):
			insufficient++
		default:
			t.Fatalf("unexpected reservation error: %v", result)
		}
	}
	if successes != 1 || insufficient != 1 {
		t.Fatalf("reservation results: successes=%d insufficient=%d, want 1/1", successes, insufficient)
	}

	var stored model.Token
	if err := fixture.db.First(&stored, fixture.token.Id).Error; err != nil {
		t.Fatalf("reload finite token: %v", err)
	}
	if stored.RemainQuota != 0 {
		t.Fatalf("remain quota=%d, want 0", stored.RemainQuota)
	}
	if stored.UsedQuota != initialUsed+reservation {
		t.Fatalf("used quota=%d, want %d", stored.UsedQuota, initialUsed+reservation)
	}
}

func TestUnlimitedTokenSkipsFiniteQuotaReservation(t *testing.T) {
	fixture := setupRelaySecurityFixture(t, "gpt-test")
	fixture.token.UnlimitedQuota = true
	fixture.token.RemainQuota = 100
	fixture.token.UsedQuota = 11
	if err := fixture.db.Model(fixture.token).Updates(map[string]any{
		"unlimited_quota": true,
		"remain_quota":    100,
		"used_quota":      11,
	}).Error; err != nil {
		t.Fatalf("set unlimited token quota: %v", err)
	}

	relayInfo := &relaycommon.RelayInfo{
		TokenId:        fixture.token.Id,
		TokenKeyHash:   fixture.token.KeyHash,
		TokenUnlimited: true,
	}
	if err := service.PreConsumeTokenQuota(relayInfo, 25); err != nil {
		t.Fatalf("unlimited token reservation returned error: %v", err)
	}

	var stored model.Token
	if err := fixture.db.First(&stored, fixture.token.Id).Error; err != nil {
		t.Fatalf("reload unlimited token: %v", err)
	}
	if stored.RemainQuota != 100 || stored.UsedQuota != 11 {
		t.Fatalf(
			"unlimited token was charged: remain=%d used=%d",
			stored.RemainQuota,
			stored.UsedQuota,
		)
	}
}
