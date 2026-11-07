package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func referralReceiptFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	txID := strings.Repeat("a", 64)
	payload := map[string]any{"id": txID, "blockNumber": 10, "blockTimeStamp": int64(1800000000000), "receipt": map[string]any{"result": "SUCCESS"}, "log": []any{map[string]any{
		"address": "a614f803b6fd780986a42c78ec9c7f77e6ded13c",
		"topics":  []string{"ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", strings.Repeat("0", 64), strings.Repeat("0", 24) + "a614f803b6fd780986a42c78ec9c7f77e6ded13c"},
		"data":    strings.Repeat("0", 56) + "0121eac0", // 19 USDT
	}}}
	body, err := common.Marshal(payload)
	require.NoError(t, err)
	return body, txID
}

func TestZTAPIReferralReceiptRequiresExactConfirmedTransfer(t *testing.T) {
	body, id := referralReceiptFixture(t)
	evidence, err := decodeZTAPIReferralReceipt(body, id, model.USDTTopUpContractAddress, 19000000, 1799999999)
	require.NoError(t, err)
	require.Equal(t, 0, evidence.LogIndex)
	require.Equal(t, int64(19000000), evidence.AmountMicros)
	require.Len(t, evidence.ReceiptSHA256, 64)
	for name, change := range map[string]func(map[string]any){
		"failed":                    func(p map[string]any) { p["result"] = "FAILED" },
		"unknown top-level result":  func(p map[string]any) { p["result"] = "UNRECOGNIZED" },
		"revert":                    func(p map[string]any) { p["receipt"] = map[string]any{"result": "REVERT"} },
		"no receipt":                func(p map[string]any) { delete(p, "receipt") },
		"wrong tx":                  func(p map[string]any) { p["id"] = strings.Repeat("b", 64) },
		"before request":            func(p map[string]any) { p["blockTimeStamp"] = int64(1700000000000) },
		"missing solidified block":  func(p map[string]any) { delete(p, "blockNumber") },
		"duplicate matching events": func(p map[string]any) { logs := p["log"].([]any); p["log"] = append(logs, logs[0]) },
		"wrong contract":            func(p map[string]any) { p["log"].([]any)[0].(map[string]any)["address"] = strings.Repeat("b", 40) },
		"oversized amount":          func(p map[string]any) { p["log"].([]any)[0].(map[string]any)["data"] = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, common.Unmarshal(body, &payload))
			change(payload)
			bad, err := common.Marshal(payload)
			require.NoError(t, err)
			_, err = decodeZTAPIReferralReceipt(bad, id, model.USDTTopUpContractAddress, 19000000, 1799999999)
			require.Error(t, err)
		})
	}
	_, err = decodeZTAPIReferralReceipt(body, id, model.USDTTopUpContractAddress, 18000000, 1799999999)
	require.Error(t, err)
}

type referralReceiptTransport func(*http.Request) (*http.Response, error)

func (f referralReceiptTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestZTAPIReferralReceiptHTTPPinsSolidityEndpointAndRedactsFailures(t *testing.T) {
	body, id := referralReceiptFixture(t)
	calls := 0
	client := &http.Client{Transport: referralReceiptTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "https://api.trongrid.io/walletsolidity/gettransactioninfobyid", r.URL.String())
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "secret-for-test", r.Header.Get("TRON-PRO-API-KEY"))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	_, err := verifyZTAPIReferralTronPayment(context.Background(), client, "secret-for-test", id, model.USDTTopUpContractAddress, 19000000, 1799999999)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	_, err = verifyZTAPIReferralTronPayment(context.Background(), client, "", id, model.USDTTopUpContractAddress, 19000000, 1799999999)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}
