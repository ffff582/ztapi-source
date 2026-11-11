package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

const cnyCreateEndpoint = "https://ttzf8.com/api/pay/v1/create"

var errCNYGateway = errors.New("CNY payment gateway response is not verifiable")

var cnyHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 12 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}()

type CNYGateway struct {
	MerchantNo, AppID                 string
	key, checkoutHost, callbackOrigin string
	client                            *http.Client
}

func LoadCNYGateway() (CNYGateway, bool) {
	g := CNYGateway{MerchantNo: os.Getenv("ZTAPI_CNY_TOPUP_MERCHANT"), AppID: os.Getenv("ZTAPI_CNY_TOPUP_APP"),
		key: os.Getenv("ZTAPI_CNY_TOPUP_KEY"), checkoutHost: os.Getenv("ZTAPI_CNY_TOPUP_CHECKOUT_HOST"),
		callbackOrigin: os.Getenv("ZTAPI_CNY_TOPUP_CALLBACK_ORIGIN")}
	if !model.ValidCNYPaymentID(g.MerchantNo) || !model.ValidCNYPaymentID(g.AppID) || len(g.key) < 20 || len(g.key) > 128 ||
		g.checkoutHost == "" || strings.ContainsAny(g.checkoutHost, ":/?#@ ") || net.ParseIP(g.checkoutHost) != nil ||
		(g.callbackOrigin != "https://ztapi.vip" && g.callbackOrigin != "https://cny-test.ztapi.vip") {
		return CNYGateway{}, false
	}
	g.client = cnyHTTPClient
	return g, true
}

func (g CNYGateway) CreateCheckout(ctx context.Context, order model.CNYTopUpOrder, clientIP string) (string, string, error) {
	if g.client == nil || order.MerchantNo != g.MerchantNo || order.AppID != g.AppID || !model.ValidCNYPaymentID(order.OrderNo) ||
		order.PayFen <= 0 || (order.Method != "alipay" && order.Method != "wxpay") || net.ParseIP(clientIP) == nil {
		return "", "", errCNYGateway
	}
	fields := map[string]string{
		"mchNo": g.MerchantNo, "appId": g.AppID, "amount": decimal.New(order.PayFen, -2).StringFixed(2),
		"subject": "ZTAPI service credit", "clientIp": clientIP, "device": "pc", "currency": "CNY",
		"mchOrderNo": order.OrderNo, "type": order.Method, "param": order.OrderNo, "sign_type": "MD5",
		"notifyUrl": g.callbackOrigin + "/api/payment/cny/notify", "returnUrl": g.callbackOrigin + "/console/wallet",
	}
	fields["sign"] = signCNYFields(fields, g.key)
	body, err := common.Marshal(fields)
	if err != nil {
		return "", "", errCNYGateway
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cnyCreateEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", errCNYGateway
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return "", "", errCNYGateway
	}
	defer resp.Body.Close()
	const limit = 32 << 10
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || resp.StatusCode != http.StatusOK || len(raw) > limit || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return "", "", errCNYGateway
	}
	var reply struct {
		Code int `json:"code"`
		Data struct {
			Merchant string `json:"mchNo"`
			App      string `json:"appId"`
			Order    string `json:"mchOrderNo"`
			Trade    string `json:"tradeNo"`
			OrderAmt string `json:"orderAmount"`
			PaidAmt  string `json:"payAmount"`
			Param    string `json:"param"`
			PayURL   string `json:"payUrl"`
			Currency string `json:"currency"`
			Type     string `json:"type"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &reply) != nil || reply.Code != 200 {
		return "", "", errCNYGateway
	}
	d := reply.Data
	ordered, first := model.ParseCNYAmountFen(d.OrderAmt)
	paid, second := model.ParseCNYAmountFen(d.PaidAmt)
	if first != nil || second != nil || ordered != order.PayFen || paid != order.PayFen || d.Merchant != g.MerchantNo ||
		d.App != g.AppID || d.Order != order.OrderNo || d.Param != order.OrderNo || !model.ValidCNYPaymentID(d.Trade) ||
		(d.Currency != "" && d.Currency != "CNY") || (d.Type != "" && d.Type != order.Method) || !g.validCheckoutURL(d.PayURL) {
		return "", "", errCNYGateway
	}
	return d.Trade, d.PayURL, nil
}

func (g CNYGateway) validCheckoutURL(s string) bool {
	if len(s) > 2048 || strings.ContainsAny(s, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Opaque == "" && strings.EqualFold(u.Host, g.checkoutHost) && u.Fragment == ""
}

func (g CNYGateway) DecodeNotice(raw []byte) (model.CNYPaymentEvidence, string, error) {
	if len(raw) > 8192 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return model.CNYPaymentEvidence{}, "", errCNYGateway
	}
	var fields map[string]string
	if common.Unmarshal(raw, &fields) != nil || len(fields) > 12 || fields["sign_type"] != "MD5" || g.key == "" {
		return model.CNYPaymentEvidence{}, "", errCNYGateway
	}
	for name, value := range fields {
		switch name {
		case "mchNo", "appId", "tradeNo", "mchOrderNo", "orderAmount", "payAmount", "payStatus", "param", "sign_type", "sign", "currency":
		default:
			return model.CNYPaymentEvidence{}, "", errCNYGateway
		}
		if len(value) > 128 || strings.ContainsAny(value, "&=\r\n\x00") {
			return model.CNYPaymentEvidence{}, "", errCNYGateway
		}
	}
	if subtle.ConstantTimeCompare([]byte(fields["sign"]), []byte(signCNYFields(fields, g.key))) != 1 {
		return model.CNYPaymentEvidence{}, "", errCNYGateway
	}
	status := fields["payStatus"]
	ordered, first := model.ParseCNYAmountFen(fields["orderAmount"])
	paid, second := model.ParseCNYAmountFen(fields["payAmount"])
	if first != nil || second != nil || ordered != paid || !model.ValidCNYPaymentID(fields["tradeNo"]) ||
		!model.ValidCNYPaymentID(fields["mchOrderNo"]) || fields["mchNo"] != g.MerchantNo || fields["appId"] != g.AppID ||
		fields["param"] != fields["mchOrderNo"] || (fields["currency"] != "" && fields["currency"] != "CNY") ||
		(status != "1" && status != "2" && status != "3") {
		return model.CNYPaymentEvidence{}, "", errCNYGateway
	}
	return model.CNYPaymentEvidence{MerchantNo: g.MerchantNo, AppID: g.AppID, TradeNo: fields["tradeNo"],
		OrderNo: fields["mchOrderNo"], OrderFen: ordered, PaidFen: paid}, status, nil
}

func signCNYFields(fields map[string]string, key string) string {
	keys := make([]string, 0, len(fields))
	for k, v := range fields {
		if v != "" && k != "sign" && k != "sign_type" && k != "signType" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+fields[k])
	}
	// This is the documented merchant protocol, not a password hash.
	digest := md5.Sum([]byte(strings.Join(pairs, "&") + key))
	return hex.EncodeToString(digest[:])
}
