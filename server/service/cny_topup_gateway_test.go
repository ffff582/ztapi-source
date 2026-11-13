package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

const cnyFixtureKey = "local-fixture-key-not-a-real-credential"

func TestCNYGatewayCallbackBindsSignedAmountsAndRejectsAmbiguity(t *testing.T) {
	g := CNYGateway{MerchantNo: "fixture", AppID: "fixture-app", key: cnyFixtureKey}
	fields := map[string]string{
		"pid": g.AppID, "trade_no": "trade-1", "out_trade_no": "CNY-order-1", "type": "alipay",
		"money": "100.010", "trade_status": "TRADE_SUCCESS", "param": "CNY-order-1", "sign_type": "MD5",
	}
	encode := func() []byte {
		query := url.Values{}
		for k, v := range fields {
			query.Set(k, v)
		}
		return []byte(query.Encode())
	}
	fields["sign"] = signCNYFields(fields, g.key)
	raw := encode()
	notice, err := g.DecodeNotice(raw)
	require.NoError(t, err)
	require.Equal(t, int64(10001), notice.Evidence().PaidFen)
	require.Equal(t, "TRADE_SUCCESS", notice.Status())
	delete(fields, "param")
	fields["sign"] = signCNYFields(fields, g.key)
	notice, err = g.DecodeNotice(encode())
	require.NoError(t, err, "the merchant may omit the optional param")
	require.Equal(t, "CNY-order-1", notice.Evidence().OrderNo)
	fields["param"] = "CNY-other"
	fields["sign"] = signCNYFields(fields, g.key)
	_, err = g.DecodeNotice(encode())
	require.Error(t, err, "an explicit conflicting param must fail")
	fields["param"] = "CNY-order-1"
	fields["sign"] = signCNYFields(fields, g.key)
	fields["sign_type"] = "SHA256"
	_, err = g.DecodeNotice(encode())
	require.Error(t, err)
	fields["sign_type"] = "MD5"
	_, err = g.DecodeNotice(append(raw, []byte("&%6doney=1")...))
	require.Error(t, err)
	fields["money"] = "100.02"
	_, err = g.DecodeNotice(encode())
	require.Error(t, err)
	fields["param"] = "CNY-order-1&trade_status=TRADE_SUCCESS"
	fields["sign"] = signCNYFields(fields, g.key)
	_, err = g.DecodeNotice(encode())
	require.Error(t, err)
}

type cnyFixtureTransport func(*http.Request) (*http.Response, error)

func (f cnyFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestCNYGatewayCheckoutVerifiesEchoAndAllowedURL(t *testing.T) {
	const nativeQR = "weixin://wxpay/bizpayurl/up?pr=fixture"
	for _, tt := range []struct {
		name, url, qr, paid, method string
		pass                        bool
	}{
		{"valid", "https://checkout.example.test/pay/1", "", "100.010", "alipay", true},
		{"qr-only", "", "https://checkout.example.test/qr/1", "100.010", "alipay", true},
		{"wechat-native-qr-only", "", nativeQR, "100.010", "wxpay", true},
		{"wrong-amount", "https://checkout.example.test/pay/1", "", "100.00", "alipay", false},
		{"wrong-host", "https://checkout.example.test.evil.test/pay/1", "", "100.010", "alipay", false},
		{"non-https", "http://checkout.example.test/pay/1", "", "100.010", "alipay", false},
		{"qr-untrusted-host", "", "https://other.example.test/qr/1", "100.010", "wxpay", false},
		{"qr-unsupported-scheme", "", "javascript:fixture", "100.010", "wxpay", false},
		{"qr-wrong-method", "", nativeQR, "100.010", "alipay", false},
		{"qr-wrong-native-path", "", "weixin://wxpay/another-app?pr=fixture", "100.010", "wxpay", false},
		{"native-uri-not-navigable", nativeQR, "", "100.010", "wxpay", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := CNYGateway{MerchantNo: "fixture", AppID: "fixture-app", key: cnyFixtureKey, checkoutHost: "checkout.example.test", callbackOrigin: "https://cny-test.ztapi.vip"}
			g.client = &http.Client{Transport: cnyFixtureTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "https://ttzf8.com/api/epay/mapi.php", r.URL.String())
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
				require.Equal(t, "ZTAPI-Payments/1.0 (+https://ztapi.vip)", r.Header.Get("User-Agent"))
				require.NoError(t, r.ParseForm())
				request := map[string]string{}
				for k, v := range r.PostForm {
					require.Len(t, v, 1)
					request[k] = v[0]
				}
				require.Equal(t, "fixture-app", request["pid"])
				require.Equal(t, "MD5", request["sign_type"])
				require.NotContains(t, request, "signType")
				require.Equal(t, "100.01", request["money"])
				require.Equal(t, "https://cny-test.ztapi.vip/api/payment/cny/notify", request["notify_url"])
				require.Equal(t, "CNY-order-1", request["out_trade_no"])
				require.Equal(t, "CNY-order-1", request["param"])
				require.Equal(t, tt.method, request["type"])
				require.NotContains(t, request, "return_url")
				require.Equal(t, signCNYFields(request, g.key), request["sign"])
				body := fmt.Sprintf(`{"code":1,"trade_no":"trade-1","money":%q,"payurl":%q,"qrcode":%q}`, tt.paid, tt.url, tt.qr)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			checkout, err := g.CreateCheckout(context.Background(), model.CNYTopUpOrder{OrderNo: "CNY-order-1", MerchantNo: g.MerchantNo, AppID: g.AppID, PayFen: 10001, Method: tt.method}, "192.0.2.1")
			if tt.pass {
				require.NoError(t, err)
				require.Equal(t, CNYCheckout{TradeNo: "trade-1", URL: tt.url, QRCode: tt.qr}, checkout)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCNYGatewayDistinguishesGatewayFromCashierHost(t *testing.T) {
	const cashier = "https://hm367.com/fixture-only"
	const body = `{"code":1,"msg":"success","trade_no":"fixture-platform-order","out_trade_no":"CNY-fixture-order","payurl":"https://hm367.com/fixture-only","qrcode":"","urlscheme":""}`
	g := CNYGateway{MerchantNo: "fixture", AppID: "fixture-app", key: cnyFixtureKey, checkoutHost: "ttzf8.com", callbackOrigin: "https://ztapi.vip"}
	g.client = &http.Client{Transport: cnyFixtureTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "ttzf8.com", r.URL.Host)
		require.Equal(t, "/api/epay/mapi.php", r.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	order := model.CNYTopUpOrder{MerchantNo: g.MerchantNo, AppID: g.AppID, OrderNo: "CNY-fixture-order", Method: "alipay", PayFen: 100}
	_, err := g.CreateCheckout(context.Background(), order, "192.0.2.1")
	require.Error(t, err, "do not treat the API host as the cashier allowlist")
	g.checkoutHost = "hm367.com"
	checkout, err := g.CreateCheckout(context.Background(), order, "192.0.2.1")
	require.NoError(t, err)
	require.Equal(t, CNYCheckout{TradeNo: "fixture-platform-order", URL: cashier}, checkout)
}

func TestCNYGatewayVerifiesExactProviderOrder(t *testing.T) {
	const paid = `{"code":1,"pid":1234,"out_trade_no":"CNY-order-1","trade_no":"trade-1","type":"alipay","money":"1.000","status":1}`
	nullApp := strings.Replace(paid, `"pid":1234`, `"pid":null`, 1)
	for _, tc := range []struct{ name, body string }{
		{"matching", paid},
		{"null-app", nullApp},
		{"empty-app", strings.Replace(paid, `"pid":1234`, `"pid":""`, 1)},
		{"missing-app", strings.Replace(paid, `"pid":1234,`, "", 1)},
		{"wrong-app", strings.Replace(paid, "1234", "4321", 1)},
		{"duplicate-app", strings.Replace(paid, `"pid":1234`, `"pid":4321,"pid":null`, 1)},
		{"wrong-order", strings.Replace(nullApp, "CNY-order-1", "CNY-order-2", 1)},
		{"wrong-trade", strings.Replace(nullApp, `"trade-1"`, `"trade-2"`, 1)},
		{"wrong-method", strings.Replace(nullApp, "alipay", "wxpay", 1)},
		{"wrong-amount", strings.Replace(nullApp, "1.000", "1.010", 1)},
		{"unpaid", strings.Replace(nullApp, `"status":1`, `"status":0`, 1)},
		{"duplicate-status", strings.Replace(paid, `"status":1`, `"status":0,"status":1`, 1)},
		{"missing-status", strings.Replace(paid, `,"status":1`, "", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := CNYGateway{MerchantNo: "fixture", AppID: "1234", key: cnyFixtureKey}
			g.client = &http.Client{Transport: cnyFixtureTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "ttzf8.com", r.URL.Host)
				require.Equal(t, "/api/epay/api.php", r.URL.Path)
				require.NotContains(t, r.URL.String(), cnyFixtureKey)
				require.Equal(t, url.Values{
					"act": {"order"}, "pid": {"1234"}, "out_trade_no": {"CNY-order-1"},
					"key": {signCNYFields(map[string]string{"act": "order", "pid": "1234", "out_trade_no": "CNY-order-1"}, cnyFixtureKey)},
				}, r.URL.Query())
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			notice, err := g.DecodeNotice(cnyFixtureNotice("1234", CNYTradeSuccess))
			require.NoError(t, err)
			err = g.VerifyPaid(context.Background(), notice)
			if tc.name == "matching" || tc.name == "null-app" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCNYGatewayRequiresSignedSuccessForOrderQuery(t *testing.T) {
	g := CNYGateway{MerchantNo: "fixture", AppID: "1234", key: cnyFixtureKey}
	queries := 0
	g.client = &http.Client{Transport: cnyFixtureTransport(func(*http.Request) (*http.Response, error) {
		queries++
		return nil, fmt.Errorf("unexpected order query")
	})}
	wrongApp, err := g.DecodeNotice(cnyFixtureNotice("4321", CNYTradeSuccess))
	require.Error(t, err)
	require.Error(t, g.VerifyPaid(context.Background(), wrongApp))
	for _, status := range []string{"TRADE_PENDING", CNYTradeRefund} {
		notice, err := g.DecodeNotice(cnyFixtureNotice("1234", status))
		require.NoError(t, err)
		require.Error(t, g.VerifyPaid(context.Background(), notice))
	}
	require.Zero(t, queries)
}

func cnyFixtureNotice(app, status string) []byte {
	fields := map[string]string{"pid": app, "trade_no": "trade-1", "out_trade_no": "CNY-order-1", "type": "alipay",
		"money": "1.00", "trade_status": status, "sign_type": "MD5"}
	fields["sign"] = signCNYFields(fields, cnyFixtureKey)
	query := url.Values{}
	for k, v := range fields {
		query.Set(k, v)
	}
	return []byte(query.Encode())
}
