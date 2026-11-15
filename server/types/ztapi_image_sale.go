package types

import (
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
	AllowedRequests        []ZTAPIImageSaleAllowedRequest `json:"allowed_requests,omitempty"`
	MaxPromptTokens        int64                          `json:"max_prompt_tokens,omitempty"`
	MaxReferenceImages     int                            `json:"max_reference_images,omitempty"`
	MaxImages              int                            `json:"max_images,omitempty"`
	Version                int                            `json:"version"`
	BillingUnit            string                         `json:"billing_unit"`
	BasePrices             map[string]string              `json:"base_prices"`
	IncludedPromptTokens   int64                          `json:"included_prompt_tokens"`
	PromptBlockTokens      int64                          `json:"prompt_block_tokens"`
	PromptBlockPrice       string                         `json:"prompt_block_price"`
	IncludedReferenceUnits int64                          `json:"included_reference_units"`
	ReferenceUnitPixels    int64                          `json:"reference_unit_pixels"`
	ReferenceUnitPrice     string                         `json:"reference_unit_price"`
}

type ZTAPIImageSaleAllowedRequest struct {
	Operation  string `json:"image_operation"`
	Size       string `json:"size"`
	Quality    string `json:"quality"`
	Resolution string `json:"resolution"`
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
	raw, _ := common.Marshal(ZTAPIImageSaleContract{Version: 1, BillingUnit: ZTAPIImageSaleBillingUnit,
		BasePrices:           map[string]string{"1K": "0.30", "2K": "0.30", "4K": "0.60"},
		IncludedPromptTokens: 2000, PromptBlockTokens: 2000, PromptBlockPrice: "0.02",
		IncludedReferenceUnits: 1, ReferenceUnitPixels: 4194304, ReferenceUnitPrice: "0.05"})
	return string(raw)
}

func ZTAPIImageSalePartial20261109JSON() string {
	var value ZTAPIImageSaleContract
	_ = common.UnmarshalJsonStr(ZTAPIImageSaleContract20261109JSON(), &value)
	value.Version = 2
	value.BasePrices = map[string]string{"1K": "0.30", "2K": "0.30"}
	value.MaxPromptTokens, value.MaxReferenceImages, value.MaxImages = 2000, 1, 1
	value.AllowedRequests = []ZTAPIImageSaleAllowedRequest{
		{Operation: "generation", Size: "1024x1024", Quality: "low", Resolution: "1K"},
		{Operation: "generation", Size: "1152x2048", Quality: "low", Resolution: "2K"},
		{Operation: "edit", Size: "1024x1024", Quality: "low", Resolution: "1K"},
	}
	raw, _ := common.Marshal(value)
	return string(raw)
}

func ParseZTAPIImageSaleContract(raw string) (ZTAPIImageSaleContract, error) {
	var value ZTAPIImageSaleContract
	if len(raw) > 4096 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil || common.DecodeJsonStrict(strings.NewReader(raw), &value) != nil {
		return value, errors.New("invalid image retail contract")
	}
	// New prices need a new reviewed policy, not arbitrary caller-provided fees.
	var approved ZTAPIImageSaleContract
	approvedJSON := ZTAPIImageSaleContract20261109JSON()
	if value.Version == 2 {
		approvedJSON = ZTAPIImageSalePartial20261109JSON()
	}
	_ = common.UnmarshalJsonStr(approvedJSON, &approved)
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
	canonical, err := common.Marshal(value)
	return string(canonical), err
}

func QuoteZTAPIImageSale(raw, resolution string, promptTokens, referenceUnits, images int64) (ZTAPIImageSaleQuote, error) {
	value, err := ParseZTAPIImageSaleContract(raw)
	quote := ZTAPIImageSaleQuote{Version: 1, Resolution: resolution, PromptTokens: promptTokens, ReferenceUnits: referenceUnits, Images: images}
	// Bounds protect integer arithmetic; they do not reintroduce a character limit.
	if err != nil || promptTokens < 0 || promptTokens > math.MaxInt32 || referenceUnits < 0 || referenceUnits > math.MaxInt32 || images < 1 || images > 10 || value.BasePrices[resolution] == "" {
		return quote, errors.New("invalid image retail quote inputs")
	}
	quote.Version = value.Version
	if value.Version == 2 && (promptTokens > value.MaxPromptTokens || referenceUnits > value.IncludedReferenceUnits || images > int64(value.MaxImages)) {
		return quote, errors.New("image retail request exceeds partial release limits")
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

func ValidateZTAPIImageSaleAdmission(raw string, selector ZTAPIImageSelector, promptTokens, referenceUnits int64, referenceImages int) error {
	policy, err := ParseZTAPIImageSaleContract(raw)
	if err != nil {
		return err
	}
	if policy.Version == 1 {
		_, err := QuoteZTAPIImageSale(raw, "1K", promptTokens, referenceUnits, int64(selector.N))
		return err
	}
	operation := selector.Operation
	if operation == "" {
		operation = "generation"
	}
	for _, allowed := range policy.AllowedRequests {
		if operation != allowed.Operation || selector.Size != allowed.Size || selector.Quality != allowed.Quality {
			continue
		}
		if _, err := QuoteZTAPIImageSale(raw, allowed.Resolution, promptTokens, referenceUnits, int64(selector.N)); err != nil {
			return err
		}
		if referenceImages < 0 || referenceImages > policy.MaxReferenceImages ||
			(operation == "generation" && (referenceImages != 0 || referenceUnits != 0)) ||
			(operation == "edit" && (referenceImages != 1 || referenceUnits != 1)) {
			return errors.New("image references are not admitted by partial release")
		}
		return nil
	}
	return errors.New("image selector is not admitted by partial release")
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
