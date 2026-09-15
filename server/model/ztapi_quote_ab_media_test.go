package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func quoteABMediaRow(t *testing.T, cell string) ZTAPIABQuotationEntry {
	t.Helper()
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	for _, row := range quote.Entries {
		if row.QuotationCell == cell {
			return row
		}
	}
	t.Fatalf("missing quotation cell %s", cell)
	return ZTAPIABQuotationEntry{}
}

func image2ABProtocol(t *testing.T) types.ZTAPIImageProtocolContract {
	t.Helper()
	contract := types.ZTAPIImageProtocolContract{
		Version: types.ZTAPIImageProtocolContractVersionV2, ProviderModel: "gpt-image-2",
		EndpointType: types.ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		WireProtocol: types.ZTAPIImageWireProtocolOpenAIImages, ProviderPath: "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{
			Sizes: []string{"1024x1024"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"},
			MinCount: 1, MaxCount: 1,
		},
		Response: types.ZTAPIImageResponseContract{
			Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"},
		},
		Usage: types.ZTAPIImageUsageContract{
			UsageField: "usage", TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported",
			Fields: map[string]string{
				"text_input": "input_tokens_details.text_tokens", "image_input": "input_tokens_details.image_tokens",
				"image_output": "output_tokens_details.image_tokens",
			},
		},
		Reservations: []types.ZTAPIImageReservationAuthority{{
			Size: "1024x1024", Quality: "standard", ResponseFormat: "url", N: 1,
			MaximumDimensions: map[string]string{"text_input": "200000", "image_input": "200000", "image_output": "200000"},
		}},
		UpstreamRequestFields: map[string]string{
			"model": "required", "prompt": "required", "n": "required", "size": "required",
			"quality": "optional", "response_format": "omit",
		},
		RequestIDSource: types.ZTAPIResponseIDSourceHeader, RequestIDKey: "X-Request-Id",
		EvidenceVersion: types.ZTAPIImageEvidenceVersion,
	}
	sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	return sealed
}

func TestZTAPIABMediaImage2FrozenEnterpriseBuckets(t *testing.T) {
	row := quoteABMediaRow(t, "D100")
	protocol := image2ABProtocol(t)
	contract, err := BuildZTAPIABMediaPriceContract(row, &protocol, nil)
	require.NoError(t, err)
	require.Equal(t, "image", contract.Modality)
	require.Len(t, contract.Rules, 5)
	wants := map[string][2]string{
		"text_input": {"3.9", "4.875"}, "text_cached_input": {"0.975", "1.21875"},
		"image_input": {"6.24", "7.8"}, "image_cached_input": {"1.56", "1.95"},
		"image_output": {"23.4", "29.25"},
	}
	for _, rule := range contract.Rules {
		require.Equal(t, wants[rule.ID][0], rule.CostUSD[rule.ID])
		require.Equal(t, wants[rule.ID][1], rule.SaleUSD[rule.ID])
		require.Equal(t, "F100", rule.SourceCells[rule.ID])
	}
	bad := protocol.Clone()
	bad.Usage.CacheSemantics = "separate_dimension"
	_, err = BuildZTAPIABMediaPriceContract(row, &bad, nil)
	require.Error(t, err)
	bad = protocol.Clone()
	bad.EvidenceHash = ""
	_, err = BuildZTAPIABMediaPriceContract(row, &bad, nil)
	require.ErrorContains(t, err, "evidence")
	_, err = BuildZTAPIABMediaPriceContract(quoteABMediaRow(t, "D82"), &protocol, nil)
	require.ErrorContains(t, err, "enterprise A")
}

func TestBuildZTAPIABImage2PriceSourceUsesExactEnterpriseQuote(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	old := validZTAPIPriceSourceForTest()
	old.ModelConfigID = 12
	old.SourceModel = "gpt-image-2"
	old.SourceDocumentChecksum = ZTAPIQuotationSHA256
	old.MediaPriceContractJSON = gpImage2ContractForTest(t)
	protocol := image2ABProtocol(t)
	source, err := BuildZTAPIABImage2PriceSource(quote, old, &protocol, 7, 1_789_000_000)
	require.NoError(t, err)
	require.Equal(t, quote.WorkbookSHA256, source.SourceDocumentChecksum)
	require.Equal(t, "A", source.QuotationGrade)
	require.Equal(t, "D100", source.QuotationCell)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise20Margin), source.PricePolicy)
	require.Equal(t, "3.9000000000", source.InputPerMillion)
	require.Equal(t, "23.4000000000", source.OutputPerMillion)
	contract, err := types.ParseZTAPIMediaPriceContract(source.MediaPriceContractJSON)
	require.NoError(t, err)
	require.Len(t, contract.Rules, 5)
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "4.8750000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "29.2500000000", preview.OutputSaleUSDPerMillion)
	require.NoError(t, validateZTAPIABPriceSource(&source))
	tampered := source
	tampered.InputPerMillion = "3.8000000000"
	require.ErrorContains(t, validateZTAPIABPriceSource(&tampered), "input_tokens")
	tampered = source
	tampered.MediaPriceContractJSON = old.MediaPriceContractJSON
	require.ErrorContains(t, validateZTAPIABPriceSource(&tampered), "exact quotation")
	bad := old
	bad.SourceDocumentChecksum = "bad"
	_, err = BuildZTAPIABImage2PriceSource(quote, bad, &protocol, 7, 1_789_000_000)
	require.Error(t, err)
}

func TestZTAPIABMediaSeedanceGateway720pNoVideo(t *testing.T) {
	cases := []struct {
		cell, model, cost, sale string
	}{
		{"D116", "doubao-seedance-2.0", "6.51", "8.1375"},
		{"D117", "doubao-seedance-2-0-fast", "5.208", "6.51"},
		{"D118", "doubao-seedance-2-0-mini", "3.255", "4.06875"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			protocol, _, err := types.BuildZTAPISeedanceProtocolContract(tc.model)
			require.NoError(t, err)
			row := quoteABMediaRow(t, tc.cell)
			_, err = BuildZTAPIABMediaPriceContract(row, nil, &protocol)
			require.ErrorContains(t, err, "unit")
			// The draft calculation is testable, but cannot become a billing contract without unit evidence.
			contract, err := buildZTAPIABSeedanceGatewayContract(row)
			require.NoError(t, err)
			rule, err := types.SelectZTAPIMediaPriceRuleFromContract(contract, types.ZTAPIMediaPriceSelector{
				Modality: "video", Conditions: map[string]string{"contains_video_input": "false", "resolution": "720p"},
			})
			if strings.Contains(tc.model, "fast") || strings.Contains(tc.model, "mini") {
				rule, err = types.SelectZTAPIMediaPriceRuleFromContract(contract, types.ZTAPIMediaPriceSelector{
					Modality: "video", Conditions: map[string]string{"contains_video_input": "false"},
				})
			}
			require.NoError(t, err)
			require.Equal(t, tc.cost, rule.CostUSD["input_tokens"])
			require.Equal(t, tc.sale, rule.SaleUSD["input_tokens"])
			require.Equal(t, "F"+tc.cell[1:], rule.SourceCells["input_tokens"])
		})
	}
}

func TestZTAPIABMediaRejectsIncompleteOrUnverifiedEvidence(t *testing.T) {
	image := image2ABProtocol(t)
	_, err := BuildZTAPIABMediaPriceContract(quoteABMediaRow(t, "D114"), &image, nil)
	require.ErrorContains(t, err, "incomplete")
	require.ErrorContains(t, err, "text/image output")
	_, err = BuildZTAPIABMediaPriceContract(quoteABMediaRow(t, "D115"), nil, nil)
	require.ErrorContains(t, err, "protocol")
	_, err = BuildZTAPIABMediaPriceContract(quoteABMediaRow(t, "D52"), nil, nil)
	require.ErrorContains(t, err, "gateway")
	_, err = BuildZTAPIABMediaPriceContract(quoteABMediaRow(t, "D53"), nil, nil)
	require.ErrorContains(t, err, "gateway")
	fastProtocol, _, err := types.BuildZTAPISeedanceProtocolContract("doubao-seedance-2-0-fast")
	require.NoError(t, err)
	_, err = BuildZTAPIABMediaPriceContract(quoteABMediaRow(t, "D54"), nil, &fastProtocol)
	require.ErrorContains(t, err, "gateway")
	_, err = BuildZTAPIABMediaPriceContract(quoteABMediaRow(t, "D115"), nil, &fastProtocol)
	require.ErrorContains(t, err, "protocol")
	tampered := quoteABMediaRow(t, "D100")
	tampered.QuotedFraction = "0.33"
	_, err = BuildZTAPIABMediaPriceContract(tampered, &image, nil)
	require.ErrorContains(t, err, "exact")
	tampered = quoteABMediaRow(t, "D100")
	tampered.OfficialPriceText = strings.Replace(tampered.OfficialPriceText, "输出=$30", "输出=$31", 1)
	_, err = BuildZTAPIABMediaPriceContract(tampered, &image, nil)
	require.ErrorContains(t, err, "exact")
	video, _, err := types.BuildZTAPISeedanceProtocolContract("doubao-seedance-2.0")
	require.NoError(t, err)
	video.Usage.Fields["input_tokens"] = "total_tokens"
	_, err = BuildZTAPIABMediaPriceContract(quoteABMediaRow(t, "D116"), nil, &video)
	require.Error(t, err)
}
