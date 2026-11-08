package finance

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func imageSaleCostContract(t *testing.T, cost, sale string) string {
	t.Helper()
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image"}
	for _, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		contract.Rules = append(contract.Rules, types.ZTAPIMediaPriceRule{
			ID: dimension, Conditions: map[string]string{"token_bucket": dimension},
			BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD:     map[string]string{dimension: cost}, SaleUSD: map[string]string{dimension: sale}, SourceCells: map[string]string{dimension: "A1"},
		})
	}
	raw, err := common.Marshal(contract)
	require.NoError(t, err)
	return string(raw)
}

func imageSaleUsage(t *testing.T) string {
	t.Helper()
	raw, err := common.Marshal(map[string]any{
		"upstream_request_id": "native-image-request", "result_count": 2, "result_available": true, "pending": false,
		"dimensions":     map[string]string{"text_input": "2000", "image_input": "4096", "image_output": "8192"},
		"raw_usage_json": `{"input_tokens":6096,"input_tokens_details":{"text_tokens":2000,"image_tokens":4096},"output_tokens":8192,"output_tokens_details":{"image_tokens":8192,"text_tokens":0},"total_tokens":14288}`,
	})
	require.NoError(t, err)
	return string(raw)
}

func imageSaleSnapshot(t *testing.T) Snapshot {
	t.Helper()
	s := fixture()
	s.Settlements[0].ReservedQuota, s.Settlements[0].ChargedQuota = 300000, 300000
	s.Ledgers[1].Delta, s.Ledgers[1].BalanceAfter = -300000, 52200000
	s.Channels = []Channel{{ID: 2, Name: "Xcloud", BaseURL: "https://www.xcloudtoken.com"}}
	quote, err := types.QuoteZTAPIImageSale(types.ZTAPIImageSaleContract20261109JSON(), "2K", 2000, 1, 2)
	require.NoError(t, err)
	price, err := common.Marshal(map[string]any{
		"SourceModel": "gpt-image-2", "Modality": "image", "PriceSourceID": 1873,
		"MediaPriceContractJSON": imageSaleCostContract(t, "0.6", "1"),
		"ImageSaleContractJSON":  types.ZTAPIImageSaleContract20261109JSON(), "ztapi_image_sale_quote": quote,
	})
	require.NoError(t, err)
	// Decode the source projection, including UsageJSON, through the same field
	// names as the Reader. An absent projection must fail this report test.
	row, err := common.Marshal(s.Settlements[0])
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, common.Unmarshal(row, &fields))
	fields["PriceSnapshotJSON"] = string(price)
	fields["ChargeDimensionsJSON"] = `[{"dimension":"images","units":"2","unit_quota":"150000","charged_quota":300000}]`
	fields["MissingDimensionsJSON"] = `[]`
	fields["UsageJSON"] = imageSaleUsage(t)
	row, err = common.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(row, &s.Settlements[0]))
	return s
}

func TestImageSaleSupplierInvoiceQuantitiesAreTokensNeverImages(t *testing.T) {
	for _, supplier := range []string{"Xcloud", "native image supplier"} {
		t.Run(supplier, func(t *testing.T) {
			s := imageSaleSnapshot(t)
			s.Channels[0].Name, s.Channels[0].BaseURL = supplier, "https://supplier.invalid"
			before, err := common.Marshal(s)
			require.NoError(t, err)
			r := calculate(t, s, facts()[:1])
			require.Len(t, r.Estimates, 1)
			require.Equal(t, "gpt-image-2", r.Estimates[0].SourceModel)
			require.Equal(t, int64(300000), r.ChargedQuota)
			require.NotNil(t, r.Revenue)
			require.Equal(t, "0.571429", *r.Revenue)
			require.Equal(t, map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 8192}, r.Estimates[0].Dimensions)
			require.NotContains(t, r.Estimates[0].Dimensions, "images")
			require.True(t, r.Estimates[0].Complete)
			require.True(t, r.Estimates[0].IsEstimate)
			require.Equal(t, "0.008573", r.Estimates[0].CostUSDT)
			require.Nil(t, r.ActualCost, "a frozen supplier quote is not an invoice")
			require.False(t, r.Confirmed)
			after, err := common.Marshal(s)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "reporting cannot rewrite historical evidence")
		})
	}
}

func TestImageSaleSupplierCostUsesFrozenMatrixNotRetailOrCurrentPrices(t *testing.T) {
	s := imageSaleSnapshot(t)
	s.Settlements[0].PriceSnapshotJSON = strings.ReplaceAll(s.Settlements[0].PriceSnapshotJSON, `\"0.6\"`, `\"1.2\"`)
	s.Settlements[0].PriceSnapshotJSON = strings.ReplaceAll(s.Settlements[0].PriceSnapshotJSON, `\"1\"`, `\"2\"`)
	r := calculate(t, s, facts()[:1])
	require.Len(t, r.Estimates, 1)
	require.True(t, r.Estimates[0].Complete)
	require.Equal(t, "0.017146", r.Estimates[0].CostUSDT)
}

func TestImageSaleInvalidNativeUsageCannotBecomeZeroCostOrImages(t *testing.T) {
	for name, usage := range map[string]string{
		"missing": "", "null": "null", "empty": `{}`, "broken": `{"dimensions":`,
		"missing raw receipt":            `{"dimensions":{"text_input":"0","image_input":"0","image_output":"0"}}`,
		"missing bucket":                 `{"dimensions":{"text_input":"2000","image_output":"8192"},"raw_usage_json":"{}"}`,
		"retail unit":                    `{"dimensions":{"images":"2"},"raw_usage_json":"{}"}`,
		"duplicate bucket":               strings.Replace(imageSaleUsage(t), `"text_input":"2000"`, `"text_input":"2000","text_input":"1"`, 1),
		"case ambiguous wrapper":         strings.Replace(imageSaleUsage(t), `"pending":false`, `"pending":false,"Pending":false`, 1),
		"malformed raw receipt":          strings.Replace(imageSaleUsage(t), `"raw_usage_json":"`, `"raw_usage_json":"broken`, 1),
		"negative":                       strings.Replace(imageSaleUsage(t), `"image_input":"4096"`, `"image_input":"-1"`, 1),
		"fractional":                     strings.Replace(imageSaleUsage(t), `"image_input":"4096"`, `"image_input":"1.5"`, 1),
		"overflow":                       strings.Replace(imageSaleUsage(t), `"image_input":"4096"`, `"image_input":"9223372036854775808"`, 1),
		"numeric not native string":      strings.Replace(imageSaleUsage(t), `"image_input":"4096"`, `"image_input":4096`, 1),
		"pending":                        strings.Replace(imageSaleUsage(t), `"pending":false`, `"pending":true`, 1),
		"wrapper output contradicts raw": strings.Replace(imageSaleUsage(t), `"image_output":"8192"`, `"image_output":"1"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			s := imageSaleSnapshot(t)
			raw, err := common.Marshal(map[string]string{"UsageJSON": usage})
			require.NoError(t, err)
			require.NoError(t, common.Unmarshal(raw, &s.Settlements[0]))
			r := calculate(t, s, facts()[:1])
			require.Len(t, r.Estimates, 1)
			require.False(t, r.Estimates[0].Complete)
			require.False(t, r.EstimateComplete)
			require.Empty(t, r.Estimates[0].Dimensions, "invalid evidence must not expose a partial invoice rollup")
			require.Nil(t, r.EstimatedCost)
			require.Nil(t, r.EstimatedGrossProfit)
			require.Empty(t, r.Estimates[0].CostUSDT, "unknown supplier costs cannot be published as zero")
			require.Contains(t, r.EstimateIssues, Issue{Code: "image_sale_usage_missing", Ref: "r1", UserID: 1})
			require.Nil(t, r.ActualCost)
		})
	}
}

func TestImageSaleNativeCachedDimensionsCannotOverlapThreeBucketReceipt(t *testing.T) {
	s := imageSaleSnapshot(t)
	s.Settlements[0].UsageJSON = strings.Replace(imageSaleUsage(t), `"text_input":"2000"`, `"text_input":"2000","text_cached_input":"300","image_cached_input":"200"`, 1)
	r := calculate(t, s, facts()[:1])
	require.Len(t, r.Estimates, 1)
	require.False(t, r.Estimates[0].Complete)
	require.Empty(t, r.Estimates[0].Dimensions)
	require.Empty(t, r.Estimates[0].CostUSDT)
}

func TestImageSaleNativeReceiptAggregatesMustMatchAllThreeBuckets(t *testing.T) {
	const receipt = `{"input_tokens":6096,"input_tokens_details":{"text_tokens":2000,"image_tokens":4096},"output_tokens":8192,"output_tokens_details":{"image_tokens":8192,"text_tokens":0},"total_tokens":14288}`
	for name, raw := range map[string]string{
		"input aggregate":   strings.Replace(receipt, `"input_tokens":6096`, `"input_tokens":6097`, 1),
		"output aggregate":  strings.Replace(receipt, `"output_tokens":8192`, `"output_tokens":8191`, 1),
		"total":             strings.Replace(receipt, `"total_tokens":14288`, `"total_tokens":14289`, 1),
		"output text":       strings.Replace(receipt, `"text_tokens":0`, `"text_tokens":1`, 1),
		"cache member":      strings.Replace(receipt, `"text_tokens":2000`, `"text_tokens":2000,"cached_tokens":1`, 1),
		"zero cache member": strings.Replace(receipt, `"text_tokens":2000`, `"text_tokens":2000,"cached_tokens":0`, 1),
		"negative":          strings.Replace(receipt, `"image_tokens":4096`, `"image_tokens":-1`, 1),
		"fractional":        strings.Replace(receipt, `"image_tokens":4096`, `"image_tokens":4096.0`, 1),
		"duplicate":         strings.Replace(receipt, `"image_tokens":4096`, `"image_tokens":4096,"image_tokens":1`, 1),
		"case alias":        strings.Replace(receipt, `"image_tokens":4096`, `"image_tokens":4096,"Image_Tokens":1`, 1),
		"null bucket":       strings.Replace(receipt, `"image_tokens":4096`, `"image_tokens":null`, 1),
		"unknown object":    `{"unexpected":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := imageSaleSnapshot(t)
			var wrapper map[string]any
			require.NoError(t, common.UnmarshalJsonStr(s.Settlements[0].UsageJSON, &wrapper))
			wrapper["raw_usage_json"] = raw
			encoded, err := common.Marshal(wrapper)
			require.NoError(t, err)
			s.Settlements[0].UsageJSON = string(encoded)
			r := calculate(t, s, facts()[:1])
			require.Len(t, r.Estimates, 1)
			require.False(t, r.Estimates[0].Complete)
			require.Empty(t, r.Estimates[0].Dimensions)
			require.Nil(t, r.EstimatedCost)
		})
	}
}

func TestImageSaleNativeUsageDoesNotReplaceHistoricalChargeDimensions(t *testing.T) {
	s := fixture()
	s.Channels = []Channel{{ID: 2, Name: "Xcloud"}}
	s.Settlements[0].PriceSnapshotJSON = `{"source_model":"claude-fable-5"}`
	s.Settlements[0].ChargeDimensionsJSON = `[{"dimension":"input_tokens","units":"16"},{"dimension":"cache_read","quantity":3}]`
	s.Settlements[0].UsageJSON = imageSaleUsage(t)
	r := calculate(t, s, facts()[:1])
	require.Len(t, r.Estimates, 1)
	require.Equal(t, map[string]int64{"input_tokens": 16, "cache_read": 3}, r.Estimates[0].Dimensions)
	require.False(t, r.Estimates[0].Complete, "legacy routing uncertainty is not repaired by an unrelated native receipt")
	require.Contains(t, r.EstimateIssues, Issue{Code: "xcloud_quote_missing", Ref: "r1", UserID: 1})
}

func TestImageSalePendingNativeCostDoesNotUndoTrustedImageRevenue(t *testing.T) {
	s := imageSaleSnapshot(t)
	known := calculate(t, s, facts()[:1])
	s.Settlements[0].UsageJSON = strings.Replace(imageSaleUsage(t), `"pending":false`, `"pending":true`, 1)
	pending := calculate(t, s, facts()[:1])
	require.Equal(t, known.Revenue, pending.Revenue)
	require.Equal(t, known.ChargedQuota, pending.ChargedQuota)
	require.NotNil(t, pending.Revenue)
	require.False(t, pending.EstimateComplete)
	require.Nil(t, pending.EstimatedCost)
	require.Empty(t, pending.Estimates[0].CostUSDT)
	require.Equal(t, "settled", s.Settlements[0].Status)
}
