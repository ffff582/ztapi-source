package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testTronWallet     = "T111111111111111111111111111111111"
	testTronUSDT       = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	testTronWindowMS   = int64(1_787_680_000_000)
	testTronGridAPIKey = "synthetic-secret-key"
)

func TestNewTronGridClientAppliesBoundedDefaultTimeout(t *testing.T) {
	rawClient := &http.Client{}

	client := NewTronGridClient("https://api.trongrid.io", testTronGridAPIKey, rawClient)

	require.Equal(t, 15*time.Second, client.httpClient.Timeout)
	require.Zero(t, rawClient.Timeout, "constructor must not mutate a shared caller client")
}

func TestTronGridClientReturnsOnlyValidatedIncomingUSDT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/accounts/"+testTronWallet+"/transactions/trc20", r.URL.Path)
		require.Equal(t, testTronGridAPIKey, r.Header.Get("TRON-PRO-API-KEY"))
		require.Equal(t, "true", r.URL.Query().Get("only_confirmed"))
		require.Equal(t, "true", r.URL.Query().Get("only_to"))
		require.Equal(t, testTronUSDT, r.URL.Query().Get("contract_address"))
		require.Equal(t, strconv.FormatInt(testTronWindowMS, 10), r.URL.Query().Get("min_timestamp"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
          "success": true,
          "data": [
            %s,
            %s,
            %s,
            %s,
            %s,
            %s
          ],
          "meta": {}
        }`,
			tronGridTransferJSON("valid", testTronWallet, testTronUSDT, 6, "10370000", testTronWindowMS+1000, true),
			tronGridTransferJSON("wrong-to", "TWrongDestination1111111111111111111", testTronUSDT, 6, "10370000", testTronWindowMS+1000, true),
			tronGridTransferJSON("wrong-contract", testTronWallet, "TWrongContract111111111111111111111", 6, "10370000", testTronWindowMS+1000, true),
			tronGridTransferJSON("wrong-decimals", testTronWallet, testTronUSDT, 18, "10370000", testTronWindowMS+1000, true),
			tronGridTransferJSON("unconfirmed", testTronWallet, testTronUSDT, 6, "10370000", testTronWindowMS+1000, false),
			tronGridTransferJSON("too-old", testTronWallet, testTronUSDT, 6, "10370000", testTronWindowMS-1, true),
		)
	}))
	t.Cleanup(server.Close)

	client := NewTronGridClient(server.URL, testTronGridAPIKey, server.Client())
	transfers, err := client.ListConfirmedIncomingUSDT(context.Background(), testTronWallet, testTronUSDT, testTronWindowMS)

	require.NoError(t, err)
	require.Len(t, transfers, 1)
	require.Equal(t, "valid", transfers[0].TxID)
	require.Equal(t, int64(10_370_000), transfers[0].AmountMicros)
	require.Equal(t, testTronWallet, transfers[0].To)
	require.Equal(t, testTronWindowMS+1000, transfers[0].BlockTimestampMS)
}

func TestTronGridClientFollowsBoundedFingerprintPagination(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			require.Empty(t, r.URL.Query().Get("fingerprint"))
			_, _ = fmt.Fprintf(w, `{"success":true,"data":[%s],"meta":{"fingerprint":"page-two"}}`, tronGridTransferJSON("page-1", testTronWallet, testTronUSDT, 6, "10010000", testTronWindowMS+1, true))
			return
		}
		require.Equal(t, "page-two", r.URL.Query().Get("fingerprint"))
		_, _ = fmt.Fprintf(w, `{"success":true,"data":[%s],"meta":{}}`, tronGridTransferJSON("page-2", testTronWallet, testTronUSDT, 6, "10020000", testTronWindowMS+2, true))
	}))
	t.Cleanup(server.Close)

	client := NewTronGridClient(server.URL, testTronGridAPIKey, server.Client())
	transfers, err := client.ListConfirmedIncomingUSDT(context.Background(), testTronWallet, testTronUSDT, testTronWindowMS)

	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
	require.Len(t, transfers, 2)
}

func TestTronGridClientReturnsTypedSanitizedErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       error
	}{
		{name: "rate limited", statusCode: http.StatusTooManyRequests, body: `{"error":"quota for synthetic-secret-key"}`, want: ErrTronGridRateLimited},
		{name: "forbidden", statusCode: http.StatusForbidden, body: `{"error":"synthetic-secret-key forbidden"}`, want: ErrTronGridForbidden},
		{name: "malformed json", statusCode: http.StatusOK, body: `{"data":[`, want: ErrTronGridMalformed},
		{name: "non-integer value", statusCode: http.StatusOK, body: `{"success":true,"data":[{"transaction_id":"bad-value","block_timestamp":1787680000001,"from":"TSender","to":"T111111111111111111111111111111111","type":"Transfer","value":"10.37","confirmed":true,"token_info":{"address":"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t","decimals":6}}],"meta":{}}`, want: ErrTronGridMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			client := NewTronGridClient(server.URL, testTronGridAPIKey, server.Client())
			transfers, err := client.ListConfirmedIncomingUSDT(context.Background(), testTronWallet, testTronUSDT, testTronWindowMS)

			require.ErrorIs(t, err, tt.want)
			require.Nil(t, transfers)
			require.NotContains(t, err.Error(), testTronGridAPIKey)
			require.NotContains(t, err.Error(), tt.body)
		})
	}
}

func TestTronGridClientSanitizesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"success":true,"data":[],"meta":{}}`))
	}))
	t.Cleanup(server.Close)
	httpClient := server.Client()
	httpClient.Timeout = 10 * time.Millisecond
	client := NewTronGridClient(server.URL, testTronGridAPIKey, httpClient)

	transfers, err := client.ListConfirmedIncomingUSDT(context.Background(), testTronWallet, testTronUSDT, testTronWindowMS)

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrTronGridUnavailable))
	require.Nil(t, transfers)
	require.NotContains(t, err.Error(), testTronGridAPIKey)
	require.NotContains(t, strings.ToLower(err.Error()), "tron-pro-api-key")
}

func tronGridTransferJSON(txID, to, contract string, decimals int, value string, timestampMS int64, confirmed bool) string {
	return fmt.Sprintf(`{
      "transaction_id":%q,
      "block_timestamp":%d,
      "from":"TSender1111111111111111111111111111",
      "to":%q,
      "type":"Transfer",
      "value":%q,
      "confirmed":%t,
      "token_info":{"address":%q,"decimals":%d,"symbol":"USDT"}
    }`, txID, timestampMS, to, value, confirmed, contract, decimals)
}
