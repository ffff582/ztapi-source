package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
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

const (
	cnyCreateEndpoint = "https://ttzf8.com/api/epay/mapi.php"
	cnyQueryEndpoint  = "https://ttzf8.com/api/epay/api.php"
	CNYTradeSuccess   = "TRADE_SUCCESS"
	CNYTradeRefund    = "TRADE_REFUND"
)
const cnyClientUserAgent = "ZTAPI-Payments/1.0 (+https://ztapi.vip)"

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

type CNYCheckout struct {
	TradeNo, URL, QRCode string
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

func (g CNYGateway) CreateCheckout(ctx context.Context, order model.CNYTopUpOrder, clientIP string) (CNYCheckout, error) {
	if g.client == nil || order.MerchantNo != g.MerchantNo || order.AppID != g.AppID || !model.ValidCNYPaymentID(order.OrderNo) ||
		order.PayFen <= 0 || (order.Method != "alipay" && order.Method != "wxpay") || net.ParseIP(clientIP) == nil {
		return CNYCheckout{}, errCNYGateway
	}
	fields := map[string]string{
		"pid": g.AppID, "money": decimal.New(order.PayFen, -2).StringFixed(2),
		"name": "ZTAPI service credit", "clientip": clientIP, "device": "pc",
		"out_trade_no": order.OrderNo, "type": order.Method, "param": order.OrderNo, "sign_type": "MD5",
		"notify_url": g.callbackOrigin + "/api/payment/cny/notify",
	}
	fields["sign"] = signCNYFields(fields, g.key)
	form := url.Values{}
	for name, value := range fields {
		form.Set(name, value)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cnyCreateEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return CNYCheckout{}, errCNYGateway
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reply, err := g.readReply(req)
	if err != nil {
		return CNYCheckout{}, errCNYGateway
	}
	checkout := CNYCheckout{TradeNo: cnyReplyString(reply["trade_no"])}
	if paymentURL := cnyReplyString(reply["payurl"]); g.validCheckoutURL(paymentURL) {
		checkout.URL = paymentURL
	}
	if qr := cnyReplyString(reply["qrcode"]); g.validCheckoutQR(qr, order.Method) {
		checkout.QRCode = qr
	}
	if !model.ValidCNYPaymentID(checkout.TradeNo) || (checkout.URL == "" && checkout.QRCode == "") {
		return CNYCheckout{}, errCNYGateway
	}
	for name, expected := range map[string]string{"pid": g.AppID, "out_trade_no": order.OrderNo, "type": order.Method, "param": order.OrderNo} {
		if value, present := reply[name]; present && cnyReplyIdentity(value) != expected {
			return CNYCheckout{}, errCNYGateway
		}
	}
	if money, present := reply["money"]; present {
		if fen, err := model.ParseCNYAmountFen(cnyReplyString(money)); err != nil || fen != order.PayFen {
			return CNYCheckout{}, errCNYGateway
		}
	}
	return checkout, nil
}

func (g CNYGateway) readReply(req *http.Request) (map[string]json.RawMessage, error) {
	req.Header.Set("User-Agent", cnyClientUserAgent)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, errCNYGateway
	}
	defer resp.Body.Close()
	const limit = 32 << 10
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || resp.StatusCode != http.StatusOK || len(raw) > limit || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return nil, errCNYGateway
	}
	var reply map[string]json.RawMessage
	if common.Unmarshal(raw, &reply) != nil || string(reply["code"]) != "1" {
		return nil, errCNYGateway
	}
	return reply, nil
}

func cnyReplyString(raw json.RawMessage) string {
	var value string
	_ = common.Unmarshal(raw, &value)
	return value
}

func cnyReplyIdentity(raw json.RawMessage) string {
	if value := cnyReplyString(raw); value != "" {
		return value
	}
	// pid is documented as an integer; compare its exact decimal text.
	if value := string(raw); value != "" && strings.Trim(value, "0123456789") == "" {
		return value
	}
	return ""
}

func (g CNYGateway) VerifyPaid(ctx context.Context, evidence model.CNYPaymentEvidence) error {
	if g.client == nil || g.key == "" || evidence.MerchantNo != g.MerchantNo || evidence.AppID != g.AppID ||
		!model.ValidCNYPaymentID(evidence.OrderNo) || !model.ValidCNYPaymentID(evidence.TradeNo) {
		return errCNYGateway
	}
	fields := map[string]string{"act": "order", "pid": g.AppID, "out_trade_no": evidence.OrderNo}
	query := url.Values{}
	for name, value := range fields {
		query.Set(name, value)
	}
	// The order endpoint takes a derived signature, never the merchant secret.
	query.Set("key", signCNYFields(fields, g.key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cnyQueryEndpoint+"?"+query.Encode(), nil)
	if err != nil {
		return errCNYGateway
	}
	reply, err := g.readReply(req)
	if err != nil {
		return err
	}
	fen, err := model.ParseCNYAmountFen(cnyReplyString(reply["money"]))
	if err != nil || fen != evidence.PaidFen || fen != evidence.OrderFen || string(reply["status"]) != "1" ||
		cnyReplyIdentity(reply["pid"]) != g.AppID || cnyReplyString(reply["out_trade_no"]) != evidence.OrderNo ||
		cnyReplyString(reply["trade_no"]) != evidence.TradeNo || cnyReplyString(reply["type"]) != evidence.Method {
		return errCNYGateway
	}
	return nil
}

func (g CNYGateway) validCheckoutURL(s string) bool {
	if len(s) > 2048 || strings.ContainsAny(s, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Opaque == "" && strings.EqualFold(u.Host, g.checkoutHost) && u.Fragment == ""
}

func (g CNYGateway) validCheckoutQR(s, method string) bool {
	if g.validCheckoutURL(s) {
		return true
	}
	if method != "wxpay" || len(s) > 512 || strings.ContainsAny(s, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "weixin" || u.User != nil || u.Fragment != "" ||
		(u.Host != "wxpay" && u.Host != "pay.weixin.qq.com") {
		return false
	}
	// Native WeChat payment URIs are QR content, never navigation targets.
	path := u.EscapedPath()
	query, err := url.ParseQuery(u.RawQuery)
	return err == nil && (path == "/bizpayurl" || path == "/bizpayurl/up") && len(query["pr"]) == 1 && query.Get("pr") != ""
}

func (g CNYGateway) DecodeNotice(raw []byte) (model.CNYPaymentEvidence, string, error) {
	if len(raw) > 8192 || g.key == "" {
		return model.CNYPaymentEvidence{}, "", errCNYGateway
	}
	query, err := url.ParseQuery(string(raw))
	if err != nil || len(query) > 10 {
		return model.CNYPaymentEvidence{}, "", errCNYGateway
	}
	fields := make(map[string]string, len(query))
	for name, values := range query {
		switch name {
		case "pid", "trade_no", "out_trade_no", "type", "name", "money", "trade_status", "param", "sign_type", "sign":
		default:
			return model.CNYPaymentEvidence{}, "", errCNYGateway
		}
		if len(values) != 1 || len(values[0]) > 512 || strings.ContainsAny(values[0], "&=\r\n\x00") {
			return model.CNYPaymentEvidence{}, "", errCNYGateway
		}
		fields[name] = values[0]
	}
	if fields["sign_type"] != "MD5" || subtle.ConstantTimeCompare([]byte(fields["sign"]), []byte(signCNYFields(fields, g.key))) != 1 {
		return model.CNYPaymentEvidence{}, "", errCNYGateway
	}
	status, method := fields["trade_status"], fields["type"]
	fen, err := model.ParseCNYAmountFen(fields["money"])
	if err != nil || !model.ValidCNYPaymentID(fields["trade_no"]) || !model.ValidCNYPaymentID(fields["out_trade_no"]) ||
		fields["pid"] != g.AppID || (fields["param"] != "" && fields["param"] != fields["out_trade_no"]) ||
		(method != "wxpay" && method != "alipay") ||
		(status != "TRADE_PENDING" && status != CNYTradeSuccess && status != CNYTradeRefund && status != "TRADE_ERROR") {
		return model.CNYPaymentEvidence{}, "", errCNYGateway
	}
	return model.CNYPaymentEvidence{MerchantNo: g.MerchantNo, AppID: g.AppID, TradeNo: fields["trade_no"], Method: method,
		OrderNo: fields["out_trade_no"], OrderFen: fen, PaidFen: fen}, status, nil
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
