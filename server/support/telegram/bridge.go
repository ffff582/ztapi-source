// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support/bridgeapi"
)

const bridgeEndpoint = "http://ztapi-support-20261102:3012" + bridgeapi.Path

type BridgeClient struct {
	endpoint string
	secret   []byte
	http     *http.Client
}

func NewBridgeClient(endpoint string, secret []byte, client *http.Client) (*BridgeClient, error) {
	u, err := url.Parse(endpoint)
	allowed := endpoint == bridgeEndpoint || endpoint == "http://ztapi-support-experience-20261102:3012"+bridgeapi.Path || endpoint == "http://ztapi-support-experience-20261102-candidate:3012"+bridgeapi.Path
	if err != nil || !allowed || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(secret) != 32 {
		return nil, errors.New("invalid internal Telegram bridge configuration")
	}
	c := http.Client{}
	if client != nil {
		c = *client
	}
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.Timeout <= 0 || c.Timeout > 5*time.Second {
		c.Timeout = 5 * time.Second
	}
	if c.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		c.Transport = transport
	}
	return &BridgeClient{endpoint: endpoint, secret: append([]byte(nil), secret...), http: &c}, nil
}

func (c BridgeClient) String() string   { return "TelegramBridgeClient{credentials:hidden}" }
func (c BridgeClient) GoString() string { return c.String() }

func (c *BridgeClient) Execute(ctx context.Context, request bridgeapi.Request) (bridgeapi.Result, error) {
	var result bridgeapi.Result
	if err := ctx.Err(); err != nil {
		return result, err
	}
	switch request.Action {
	case "claim_binding", "binding_status", "recent_requests", "diagnose", "create_ticket", "poll_notifications", "ack_notification", "authorize_notification":
	default:
		return result, errors.New("Telegram bridge action rejected")
	}
	raw, err := common.Marshal(request)
	if err != nil || len(raw) > 16384 {
		return result, errors.New("Telegram bridge request rejected")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return result, errors.New("Telegram bridge nonce unavailable")
	}
	timestamp, nonceText := strconv.FormatInt(time.Now().Unix(), 10), hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return result, errors.New("Telegram bridge request unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(bridgeapi.HeaderTimestamp, timestamp)
	req.Header.Set(bridgeapi.HeaderNonce, nonceText)
	req.Header.Set(bridgeapi.HeaderSignature, bridgeapi.Signature(c.secret, timestamp, nonceText, http.MethodPost, bridgeapi.Path, raw))
	res, err := c.http.Do(req)
	if err != nil {
		return result, errors.New("Telegram bridge temporarily unavailable")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	var envelope bridgeapi.Response
	if err != nil || len(body) > 65536 || res.StatusCode != http.StatusOK || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(body)) != nil || common.DecodeJsonStrict(bytes.NewReader(body), &envelope) != nil || !envelope.Success {
		return result, errors.New("Telegram bridge response unavailable")
	}
	return envelope.Data, nil
}
