package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

var ErrZTAPIReferralPaymentUnverified = errors.New("referral_payment_unverified")
var referralTxIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ZTAPIReferralVerifiedReceipt struct {
	TxID             string
	LogIndex         int
	Network          string
	ContractAddress  string
	From             string
	To               string
	AmountMicros     int64
	BlockTimestampMS int64
	BlockNumber      int64
	ReceiptSHA256    string
}

// Only solidified successful receipts can settle a manually submitted payout.
// Provider documentation: https://developers.tron.network/reference/transactions-1
func VerifyZTAPIReferralTronPayment(ctx context.Context, apiKey, txID, to string, amountMicros, since int64) (*ZTAPIReferralVerifiedReceipt, error) {
	return verifyZTAPIReferralTronPayment(ctx, &http.Client{Timeout: 12 * time.Second}, apiKey, txID, to, amountMicros, since)
}

func verifyZTAPIReferralTronPayment(ctx context.Context, client *http.Client, apiKey, txID, to string, amountMicros, since int64) (*ZTAPIReferralVerifiedReceipt, error) {
	if strings.TrimSpace(apiKey) == "" || !referralTxIDPattern.MatchString(txID) || model.ValidateTronBase58Address(to) != nil || amountMicros <= 0 || since <= 0 || client == nil {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	body, err := common.Marshal(map[string]string{"value": txID})
	if err != nil {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.trongrid.io/walletsolidity/gettransactioninfobyid", bytes.NewReader(body))
	if err != nil {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("TRON-PRO-API-KEY", apiKey)
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout <= 0 || copyClient.Timeout > 12*time.Second {
		copyClient.Timeout = 12 * time.Second
	}
	response, err := copyClient.Do(request)
	if err != nil {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	return decodeZTAPIReferralReceipt(raw, txID, to, amountMicros, since)
}

func decodeZTAPIReferralReceipt(raw []byte, txID, to string, amountMicros, since int64) (*ZTAPIReferralVerifiedReceipt, error) {
	var receipt struct {
		ID          string `json:"id"`
		Result      string `json:"result"`
		BlockNumber int64  `json:"blockNumber"`
		Timestamp   int64  `json:"blockTimeStamp"`
		Receipt     struct {
			Result string `json:"result"`
		} `json:"receipt"`
		Logs []struct {
			Address string   `json:"address"`
			Topics  []string `json:"topics"`
			Data    string   `json:"data"`
		} `json:"log"`
	}
	if !referralTxIDPattern.MatchString(txID) || amountMicros <= 0 || since <= 0 || since > int64(^uint64(0)>>1)/1000 || common.Unmarshal(raw, &receipt) != nil || receipt.ID != txID || (receipt.Result != "" && receipt.Result != "SUCESS" && receipt.Result != "SUCCESS") || receipt.Receipt.Result != "SUCCESS" || receipt.BlockNumber <= 0 || receipt.Timestamp < since*1000 {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	toHex, err := referralTronAddressHex(to)
	if err != nil {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	contractHex, err := referralTronAddressHex(model.USDTTopUpContractAddress)
	if err != nil {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	const transferTopic = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	var matched *ZTAPIReferralVerifiedReceipt
	for index, event := range receipt.Logs {
		address := strings.ToLower(event.Address)
		if len(address) == 42 && strings.HasPrefix(address, "41") {
			address = address[2:]
		}
		if address != contractHex || len(event.Topics) != 3 || strings.ToLower(event.Topics[0]) != transferTopic {
			continue
		}
		if len(event.Topics[1]) != 64 || len(event.Topics[2]) != 64 || event.Topics[1][:24] != strings.Repeat("0", 24) || event.Topics[2][:24] != strings.Repeat("0", 24) || strings.ToLower(event.Topics[2][24:]) != toHex {
			continue
		}
		if len(event.Data) != 64 {
			return nil, ErrZTAPIReferralPaymentUnverified
		}
		amount, ok := new(big.Int).SetString(event.Data, 16)
		if !ok || !amount.IsInt64() {
			return nil, ErrZTAPIReferralPaymentUnverified
		}
		if amount.Int64() != amountMicros {
			continue
		}
		from, err := referralTronAddressFromHex(event.Topics[1][24:])
		if err != nil {
			return nil, ErrZTAPIReferralPaymentUnverified
		}
		if matched != nil {
			return nil, ErrZTAPIReferralPaymentUnverified
		}
		digest := sha256.Sum256(raw)
		matched = &ZTAPIReferralVerifiedReceipt{TxID: txID, LogIndex: index, Network: model.USDTTopUpNetworkTronMainnet, ContractAddress: model.USDTTopUpContractAddress, From: from, To: to, AmountMicros: amountMicros, BlockTimestampMS: receipt.Timestamp, BlockNumber: receipt.BlockNumber, ReceiptSHA256: hex.EncodeToString(digest[:])}
	}
	if matched == nil {
		return nil, ErrZTAPIReferralPaymentUnverified
	}
	return matched, nil
}

func referralTronAddressHex(address string) (string, error) {
	if err := model.ValidateTronBase58Address(address); err != nil {
		return "", ErrZTAPIReferralPaymentUnverified
	}
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	value := new(big.Int)
	for _, symbol := range address {
		value.Mul(value, big.NewInt(58))
		value.Add(value, big.NewInt(int64(strings.IndexRune(alphabet, symbol))))
	}
	raw := value.Bytes()
	if len(raw) != 25 {
		return "", ErrZTAPIReferralPaymentUnverified
	}
	return hex.EncodeToString(raw[1:21]), nil
}

func referralTronAddressFromHex(address string) (string, error) {
	raw, err := hex.DecodeString(address)
	if err != nil || len(raw) != 20 {
		return "", ErrZTAPIReferralPaymentUnverified
	}
	payload := append([]byte{0x41}, raw...)
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	payload = append(payload, second[:4]...)
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	value := new(big.Int).SetBytes(payload)
	base := big.NewInt(58)
	var encoded []byte
	for value.Sign() > 0 {
		remainder := new(big.Int)
		value.QuoRem(value, base, remainder)
		encoded = append(encoded, alphabet[remainder.Int64()])
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	result := string(encoded)
	if model.ValidateTronBase58Address(result) != nil {
		return "", fmt.Errorf("%w: invalid transfer address", ErrZTAPIReferralPaymentUnverified)
	}
	return result, nil
}
