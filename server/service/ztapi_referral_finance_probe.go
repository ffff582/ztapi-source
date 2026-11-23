package service

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var referralFinanceCapabilityCache struct {
	sync.Mutex
	until time.Time
	ready bool
}

func ZTAPIReferralFinanceCapabilityReady(ctx context.Context) bool {
	referralFinanceCapabilityCache.Lock()
	defer referralFinanceCapabilityCache.Unlock()
	if time.Now().Before(referralFinanceCapabilityCache.until) {
		return referralFinanceCapabilityCache.ready
	}
	ready := probeZTAPIReferralFinanceCapability(ctx, &http.Client{Timeout: 2 * time.Second})
	referralFinanceCapabilityCache.ready = ready
	referralFinanceCapabilityCache.until = time.Now().Add(10 * time.Second)
	return ready
}

func probeZTAPIReferralFinanceCapability(ctx context.Context, client *http.Client) bool {
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(bounded, http.MethodGet, "http://ztapi-finance-support-20261102-live:3011/healthz", nil)
	if err != nil || client == nil {
		return false
	}
	request.Header.Set("Cache-Control", "no-cache")
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout <= 0 || copyClient.Timeout > 2*time.Second {
		copyClient.Timeout = 2 * time.Second
	}
	response, err := copyClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return false
	}
	var payload struct {
		OK           bool `json:"ok"`
		Capabilities struct {
			Referral bool `json:"referral_v1"`
		} `json:"capabilities"`
	}
	return common.Unmarshal(body, &payload) == nil && payload.OK && payload.Capabilities.Referral
}
