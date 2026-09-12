package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
)

const (
	tronGridPageLimit      = 200
	tronGridMaxPages       = 10
	tronGridMaxBodyBytes   = 4 << 20
	tronGridDefaultTimeout = 15 * time.Second
)

var (
	ErrTronGridRateLimited = errors.New("TronGrid rate limited")
	ErrTronGridForbidden   = errors.New("TronGrid access forbidden")
	ErrTronGridMalformed   = errors.New("TronGrid returned malformed data")
	ErrTronGridUnavailable = errors.New("TronGrid unavailable")
)

type TRC20Transfer = model.TRC20Transfer

type TronGridClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewTronGridClient(baseURL string, apiKey string, httpClient *http.Client) *TronGridClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: tronGridDefaultTimeout}
	} else {
		cloned := *httpClient
		if cloned.Timeout <= 0 {
			cloned.Timeout = tronGridDefaultTimeout
		}
		httpClient = &cloned
	}
	return &TronGridClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: httpClient,
	}
}

func (client *TronGridClient) ListConfirmedIncomingUSDT(ctx context.Context, address string, contract string, minTimestampMS int64) ([]TRC20Transfer, error) {
	if client == nil || client.httpClient == nil || client.baseURL == "" || client.apiKey == "" || strings.TrimSpace(address) == "" || strings.TrimSpace(contract) == "" || minTimestampMS < 0 {
		return nil, fmt.Errorf("%w: invalid client configuration", ErrTronGridMalformed)
	}
	parsedBase, err := url.Parse(client.baseURL)
	if err != nil || parsedBase.Scheme == "" || parsedBase.Host == "" {
		return nil, fmt.Errorf("%w: invalid endpoint", ErrTronGridMalformed)
	}

	fingerprint := ""
	seenFingerprints := make(map[string]struct{})
	seenTransactions := make(map[string]struct{})
	transfers := make([]TRC20Transfer, 0)
	for page := 0; page < tronGridMaxPages; page++ {
		response, requestErr := client.fetchTRC20Page(ctx, parsedBase, strings.TrimSpace(address), strings.TrimSpace(contract), minTimestampMS, fingerprint)
		if requestErr != nil {
			return nil, requestErr
		}
		for _, row := range response.Data {
			transfer, include, parseErr := normalizeTronGridTransfer(row, strings.TrimSpace(address), strings.TrimSpace(contract), minTimestampMS)
			if parseErr != nil {
				return nil, parseErr
			}
			if !include {
				continue
			}
			if _, exists := seenTransactions[transfer.TxID]; exists {
				continue
			}
			seenTransactions[transfer.TxID] = struct{}{}
			transfers = append(transfers, transfer)
		}

		next := strings.TrimSpace(response.Meta.Fingerprint)
		if next == "" {
			return transfers, nil
		}
		if _, repeated := seenFingerprints[next]; repeated {
			return nil, fmt.Errorf("%w: repeated pagination fingerprint", ErrTronGridMalformed)
		}
		seenFingerprints[next] = struct{}{}
		fingerprint = next
	}
	return nil, fmt.Errorf("%w: pagination limit exceeded", ErrTronGridMalformed)
}

func (client *TronGridClient) fetchTRC20Page(ctx context.Context, base *url.URL, address string, contract string, minTimestampMS int64, fingerprint string) (*tronGridTRC20Response, error) {
	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/accounts/" + url.PathEscape(address) + "/transactions/trc20"
	query := endpoint.Query()
	query.Set("only_confirmed", "true")
	query.Set("only_to", "true")
	query.Set("contract_address", contract)
	query.Set("min_timestamp", strconv.FormatInt(minTimestampMS, 10))
	query.Set("limit", strconv.Itoa(tronGridPageLimit))
	if fingerprint != "" {
		query.Set("fingerprint", fingerprint)
	}
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid request", ErrTronGridMalformed)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("TRON-PRO-API-KEY", client.apiKey)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: request failed", ErrTronGridUnavailable)
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusTooManyRequests:
		return nil, ErrTronGridRateLimited
	case http.StatusForbidden:
		return nil, ErrTronGridForbidden
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: upstream status category %dxx", ErrTronGridUnavailable, response.StatusCode/100)
	}

	limited := io.LimitReader(response.Body, tronGridMaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > tronGridMaxBodyBytes {
		return nil, fmt.Errorf("%w: unreadable response", ErrTronGridMalformed)
	}
	var payload tronGridTRC20Response
	if err := json.Unmarshal(body, &payload); err != nil || !payload.Success {
		return nil, fmt.Errorf("%w: invalid response envelope", ErrTronGridMalformed)
	}
	return &payload, nil
}

type tronGridTRC20Response struct {
	Success bool                  `json:"success"`
	Data    []tronGridTRC20Record `json:"data"`
	Meta    struct {
		Fingerprint string `json:"fingerprint"`
	} `json:"meta"`
}

type tronGridTRC20Record struct {
	TransactionID string `json:"transaction_id"`
	BlockTimeMS   int64  `json:"block_timestamp"`
	From          string `json:"from"`
	To            string `json:"to"`
	Type          string `json:"type"`
	Value         string `json:"value"`
	Confirmed     *bool  `json:"confirmed"`
	TokenInfo     struct {
		Address  string `json:"address"`
		Decimals int    `json:"decimals"`
	} `json:"token_info"`
}

func normalizeTronGridTransfer(row tronGridTRC20Record, address string, contract string, minTimestampMS int64) (TRC20Transfer, bool, error) {
	if strings.TrimSpace(row.TransactionID) == "" || strings.TrimSpace(row.From) == "" || strings.TrimSpace(row.To) == "" || strings.TrimSpace(row.TokenInfo.Address) == "" || row.BlockTimeMS <= 0 {
		return TRC20Transfer{}, false, fmt.Errorf("%w: missing transfer evidence", ErrTronGridMalformed)
	}
	if row.Confirmed != nil && !*row.Confirmed {
		return TRC20Transfer{}, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(row.Type), "transfer") || row.To != address || row.TokenInfo.Address != contract || row.TokenInfo.Decimals != 6 || row.BlockTimeMS < minTimestampMS {
		return TRC20Transfer{}, false, nil
	}
	amount, err := strconv.ParseInt(row.Value, 10, 64)
	if err != nil || amount <= 0 {
		return TRC20Transfer{}, false, fmt.Errorf("%w: invalid token value", ErrTronGridMalformed)
	}
	return TRC20Transfer{
		TxID:             row.TransactionID,
		From:             row.From,
		To:               row.To,
		ContractAddress:  row.TokenInfo.Address,
		AmountMicros:     amount,
		BlockTimestampMS: row.BlockTimeMS,
	}, true, nil
}
