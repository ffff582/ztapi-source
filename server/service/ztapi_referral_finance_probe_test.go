package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIReferralFinanceProbeNeedsLiveExplicitCapability(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		want   bool
	}{{`{"ok":true,"capabilities":{"referral_v1":true}}`, 200, true}, {`{"ok":true}`, 200, false}, {`{"ok":true,"capabilities":{"referral_v1":false}}`, 200, false}, {`{"ok":false,"capabilities":{"referral_v1":true}}`, 200, false}, {`{"ok":true,"capabilities":{"referral_v1":true}}`, 503, false}} {
		client := &http.Client{Transport: referralReceiptTransport(func(r *http.Request) (*http.Response, error) {
			require.Equal(t, "http://ztapi-finance-support-20261102-live:3011/healthz", r.URL.String())
			require.Empty(t, r.Header.Get("Authorization"))
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
		})}
		require.Equal(t, tc.want, probeZTAPIReferralFinanceCapability(context.Background(), client))
	}
}
