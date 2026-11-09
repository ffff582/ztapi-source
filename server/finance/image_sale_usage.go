package finance

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

func isImageSaleSettlement(s Settlement) bool {
	var fields map[string]json.RawMessage
	if common.UnmarshalJsonStr(s.PriceSnapshotJSON, &fields) == nil {
		for key, raw := range fields {
			if strings.EqualFold(key, "ztapi_image_sale_quote") || strings.EqualFold(key, "ImageSaleContractJSON") {
				if string(raw) != `""` && string(raw) != "null" {
					return true
				}
			}
		}
	}
	// A damaged/missing frozen quote must not fall back to pricing image
	// counts as supplier Tokens.
	var charges []struct{ Dimension string }
	if common.UnmarshalJsonStr(s.ChargeDimensionsJSON, &charges) == nil {
		for _, charge := range charges {
			if strings.EqualFold(charge.Dimension, "images") {
				return true
			}
		}
	}
	return false
}

func imageSaleNativeUsage(raw string) ([]UsageDimension, error) {
	invalid := errors.New("native image supplier usage missing or invalid")
	if len(raw) > 1<<20 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil {
		return nil, invalid
	}
	var fields map[string]json.RawMessage
	var dimensions map[string]string
	var receiptJSON string
	var pending bool
	if common.UnmarshalJsonStr(raw, &fields) != nil ||
		common.Unmarshal(fields["dimensions"], &dimensions) != nil || len(dimensions) != 3 ||
		common.Unmarshal(fields["raw_usage_json"], &receiptJSON) != nil ||
		common.GetJsonType(fields["pending"]) != "boolean" || common.Unmarshal(fields["pending"], &pending) != nil || pending ||
		common.RejectDuplicateJsonObjectMembers(strings.NewReader(receiptJSON)) != nil {
		return nil, invalid
	}
	for key := range fields {
		for _, canonical := range []string{"dimensions", "raw_usage_json", "pending"} {
			if key != canonical && strings.EqualFold(key, canonical) {
				return nil, invalid
			}
		}
	}
	receipt, err := types.ParseZTAPIGPTImage2NativeUsage(receiptJSON)
	if err != nil {
		return nil, invalid
	}
	for _, dimension := range []string{"text_input", "image_input", "image_output"} {
		quantity, ok := dimensions[dimension]
		if !ok || quantity != strconv.FormatInt(receipt[dimension], 10) {
			return nil, invalid
		}
	}
	rows := make([]map[string]string, 0, len(dimensions))
	for dimension, units := range dimensions {
		switch dimension {
		case "text_input", "image_input", "image_output":
		default:
			return nil, invalid
		}
		rows = append(rows, map[string]string{"dimension": dimension, "units": units})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["dimension"] < rows[j]["dimension"] })
	encoded, err := common.Marshal(rows)
	if err != nil {
		return nil, invalid
	}
	return parseSettlementUsageDimensions(string(encoded))
}

func imageSaleTokenCost(contractJSON, operation string, dimensions []UsageDimension) (decimal.Decimal, error) {
	quantities := map[string]int64{}
	for _, dimension := range dimensions {
		if _, seen := quantities[dimension.Dimension]; seen || dimension.Quantity < 0 {
			return zero, errors.New("invalid supplier Token quantity")
		}
		quantities[dimension.Dimension] = dimension.Quantity
	}
	return types.CalculateZTAPIImageSaleTokenCost(contractJSON, operation, quantities)
}

func estimateImageSaleUsage(s Settlement, dimensions []UsageDimension) (XcloudUsageEstimate, error) {
	invalid := errors.New("frozen image supplier quote missing or invalid")
	if len(s.PriceSnapshotJSON) > 1<<20 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(s.PriceSnapshotJSON)) != nil {
		return XcloudUsageEstimate{}, invalid
	}
	var frozen struct {
		SourceModel            string
		Modality               string
		PriceSourceID          int64
		MediaPriceContractJSON string
		ImageSaleContractJSON  string
		SelectorJSON           string                    `json:"ztapi_media_selector"`
		Quote                  types.ZTAPIImageSaleQuote `json:"ztapi_image_sale_quote"`
	}
	if common.UnmarshalJsonStr(s.PriceSnapshotJSON, &frozen) != nil || frozen.SourceModel == "" || frozen.Modality != "image" || frozen.PriceSourceID <= 0 ||
		types.ValidateZTAPIImageSaleQuote(frozen.ImageSaleContractJSON, frozen.Quote) != nil {
		return XcloudUsageEstimate{}, invalid
	}
	var selector struct {
		Operation string `json:"image_operation"`
	}
	if frozen.SelectorJSON != "" && (common.RejectDuplicateJsonObjectMembers(strings.NewReader(frozen.SelectorJSON)) != nil || common.UnmarshalJsonStr(frozen.SelectorJSON, &selector) != nil) {
		return XcloudUsageEstimate{}, invalid
	}
	if selector.Operation == "" {
		selector.Operation = "generation"
	}
	cost, err := imageSaleTokenCost(frozen.MediaPriceContractJSON, selector.Operation, dimensions)
	if err != nil {
		return XcloudUsageEstimate{}, err
	}
	return XcloudUsageEstimate{SourceModel: frozen.SourceModel, CostUSDT: cost.String(), IsEstimate: true}, nil
}
