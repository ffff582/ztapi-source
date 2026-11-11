package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

const cnyFixtureKey = "local-fixture-key-not-a-real-credential"

func TestCNYGatewayCallbackBindsSignedAmountsAndRejectsAmbiguity(t *testing.T) {
	g := CNYGateway{MerchantNo: "fixture", AppID: "fixture-app", key: cnyFixtureKey}
	fields := map[string]string{
		"mchNo": g.MerchantNo, "appId": g.AppID, "tradeNo": "trade-1", "mchOrderNo": "CNY-order-1",
		"orderAmount": "100.010", "payAmount": "100.01", "payStatus": "2", "param": "CNY-order-1", "sign_type": "MD5",
	}
	fields["sign"] = signCNYFields(fields, g.key)
	raw, err := common.Marshal(fields)
	require.NoError(t, err)
	receipt, status, err := g.DecodeNotice(raw)
	require.NoError(t, err)
	require.Equal(t, int64(10001), receipt.PaidFen)
	require.Equal(t, "2", status)
	_, _, err = g.DecodeNotice([]byte(`{"payAmount":"1",` + string(raw[1:])))
	require.Error(t, err)
	fields["payAmount"] = "100.02"
	raw, _ = common.Marshal(fields)
	_, _, err = g.DecodeNotice(raw)
	require.Error(t, err)
	fields["param"] = "CNY-order-1&payStatus=2"
	fields["sign"] = signCNYFields(fields, g.key)
	raw, _ = common.Marshal(fields)
	_, _, err = g.DecodeNotice(raw)
	require.Error(t, err)
}

type cnyFixtureTransport func(*http.Request) (*http.Response, error)

func (f cnyFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestCNYGatewayCheckoutVerifiesEchoAndAllowedURL(t *testing.T) {
	for _, tt := range []struct {
		name, url, paid string
		pass            bool
	}{
		{"valid", "https://checkout.example.test/pay/1", "100.010", true},
		{"wrong-amount", "https://checkout.example.test/pay/1", "100.00", false},
		{"wrong-host", "https://checkout.example.test.evil.test/pay/1", "100.010", false},
		{"non-https", "http://checkout.example.test/pay/1", "100.010", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := CNYGateway{MerchantNo: "fixture", AppID: "fixture-app", key: cnyFixtureKey, checkoutHost: "checkout.example.test", callbackOrigin: "https://cny-test.ztapi.vip"}
			g.client = &http.Client{Transport: cnyFixtureTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, cnyCreateEndpoint, r.URL.String())
				var request map[string]string
				require.NoError(t, common.DecodeJson(r.Body, &request))
				require.Equal(t, "CNY", request["currency"])
				require.Equal(t, "100.01", request["amount"])
				require.Equal(t, "CNY-order-1", request["param"])
				require.Equal(t, signCNYFields(request, g.key), request["sign"])
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"mchNo":"fixture","appId":"fixture-app","mchOrderNo":"CNY-order-1","tradeNo":"trade-1","orderAmount":"100.01","payAmount":"` + tt.paid + `","param":"CNY-order-1","payUrl":"` + tt.url + `"}}`))}, nil
			})}
			trade, url, err := g.CreateCheckout(context.Background(), model.CNYTopUpOrder{OrderNo: "CNY-order-1", MerchantNo: g.MerchantNo, AppID: g.AppID, PayFen: 10001, Method: "alipay"}, "192.0.2.1")
			if tt.pass {
				require.NoError(t, err)
				require.Equal(t, "trade-1", trade)
				require.Equal(t, tt.url, url)
			} else {
				require.Error(t, err)
			}
		})
	}
}
