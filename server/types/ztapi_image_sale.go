package types

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const ZTAPIImageSaleBillingUnit = "usd_per_image"

// This is a separate retail contract: supplier Token prices are not rewritten.
type ZTAPIImageSaleContract struct {
	Version                int               `json:"version"`
	BillingUnit            string            `json:"billing_unit"`
	BasePrices             map[string]string `json:"base_prices"`
	IncludedPromptTokens   int64             `json:"included_prompt_tokens"`
	PromptBlockTokens      int64             `json:"prompt_block_tokens"`
	PromptBlockPrice       string            `json:"prompt_block_price"`
	IncludedReferenceUnits int64             `json:"included_reference_units"`
	ReferenceUnitPixels    int64             `json:"reference_unit_pixels"`
	ReferenceUnitPrice     string            `json:"reference_unit_price"`
}

type ZTAPIImageSaleQuote struct {
	Version             int    `json:"version"`
	Resolution          string `json:"resolution"`
	PromptTokens        int64  `json:"prompt_tokens"`
	ReferenceUnits      int64  `json:"reference_units"`
	Images              int64  `json:"images"`
	ExtraPromptBlocks   int64  `json:"extra_prompt_blocks"`
	ExtraReferenceUnits int64  `json:"extra_reference_units"`
	UnitPriceUSD        string `json:"unit_price_usd"`
	TotalUSD            string `json:"total_usd"`
}

func ZTAPIImageSaleContract20261109JSON() string {
	raw, _ := json.Marshal(ZTAPIImageSaleContract{Version: 1, BillingUnit: ZTAPIImageSaleBillingUnit,
		BasePrices:           map[string]string{"1K": "0.30", "2K": "0.30", "4K": "0.60"},
		IncludedPromptTokens: 2000, PromptBlockTokens: 2000, PromptBlockPrice: "0.02",
		IncludedReferenceUnits: 1, ReferenceUnitPixels: 4194304, ReferenceUnitPrice: "0.05"})
	return string(raw)
}

func ParseZTAPIImageSaleContract(raw string) (ZTAPIImageSaleContract, error) {
	var value ZTAPIImageSaleContract
	if len(raw) > 4096 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil || common.DecodeJsonStrict(strings.NewReader(raw), &value) != nil {
		return value, errors.New("invalid image retail contract")
	}
	// New prices need a new reviewed policy, not arbitrary caller-provided fees.
	var approved ZTAPIImageSaleContract
	_ = json.Unmarshal([]byte(ZTAPIImageSaleContract20261109JSON()), &approved)
	if !reflect.DeepEqual(value, approved) {
		return value, errors.New("unapproved image retail policy")
	}
	return value, nil
}

func CanonicalizeZTAPIImageSaleContract(raw string) (string, error) {
	value, err := ParseZTAPIImageSaleContract(raw)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	return string(canonical), err
}

func QuoteZTAPIImageSale(raw, resolution string, promptTokens, referenceUnits, images int64) (ZTAPIImageSaleQuote, error) {
	value, err := ParseZTAPIImageSaleContract(raw)
	quote := ZTAPIImageSaleQuote{Version: 1, Resolution: resolution, PromptTokens: promptTokens, ReferenceUnits: referenceUnits, Images: images}
	// Bounds protect integer arithmetic; they do not reintroduce a character limit.
	if err != nil || promptTokens < 0 || promptTokens > math.MaxInt32 || referenceUnits < 0 || referenceUnits > math.MaxInt32 || images < 1 || images > 10 || value.BasePrices[resolution] == "" {
		return quote, errors.New("invalid image retail quote inputs")
	}
	if promptTokens > value.IncludedPromptTokens {
		quote.ExtraPromptBlocks = (promptTokens - value.IncludedPromptTokens + value.PromptBlockTokens - 1) / value.PromptBlockTokens
	}
	if referenceUnits > value.IncludedReferenceUnits {
		quote.ExtraReferenceUnits = referenceUnits - value.IncludedReferenceUnits
	}
	unit := decimal.RequireFromString(value.BasePrices[resolution]).Add(decimal.NewFromInt(quote.ExtraPromptBlocks).Mul(decimal.RequireFromString(value.PromptBlockPrice))).Add(decimal.NewFromInt(quote.ExtraReferenceUnits).Mul(decimal.RequireFromString(value.ReferenceUnitPrice)))
	quote.UnitPriceUSD = unit.String()
	quote.TotalUSD = unit.Mul(decimal.NewFromInt(images)).String()
	return quote, nil
}

func ValidateZTAPIImageSaleQuote(raw string, quote ZTAPIImageSaleQuote) error {
	expected, err := QuoteZTAPIImageSale(raw, quote.Resolution, quote.PromptTokens, quote.ReferenceUnits, quote.Images)
	if err != nil || expected != quote {
		return errors.New("frozen image retail quote mismatch")
	}
	return nil
}

func ZTAPIImageReferenceUnits(width, height int64) (int64, error) {
	if width <= 0 || height <= 0 || width > math.MaxInt64/height {
		return 0, errors.New("invalid reference image dimensions")
	}
	pixels := width * height
	units := (pixels-1)/4194304 + 1
	if units > math.MaxInt32 {
		return 0, errors.New("reference image dimensions exceed billing range")
	}
	return units, nil
}
