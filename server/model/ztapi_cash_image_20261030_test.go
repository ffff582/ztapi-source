package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func cashImageLegacy20261030ForTest(t *testing.T) ZTAPIModelPriceSource {
	t.Helper()
	row, ok := ztapiMediaPriceRowFromManifest(ztapiQuotation, "gpt-image-2")
	require.True(t, ok)
	raw, err := deriveZTAPIGPTImage2OperationAwarePriceContract(row.MediaPriceContractJSON)
	require.NoError(t, err)
	base := ZTAPIModelPriceSource{ID: 827, ModelConfigID: 85, SourceModel: "gpt-image-2", ResourceType: "pool",
		PricePolicy: "pool_official_80", SaleMultiplier: "1.0000000000", SpendTier: "standard", Currency: "USD",
		BillingDimensions: `["input_tokens","output_tokens"]`, CNYPerUSD: "0", PlatformCNYPerUnit: "0", UpstreamCNYPerUSD: "0",
		MediaPriceContractJSON: raw, SourceDocumentChecksum: ZTAPIQuotationSHA256,
		QuotationEffectiveAt: 1789000000, OperatorID: 1, Version: 24, CreatedAt: 1789000001}
	clearZTAPIPriceSourceCosts(&base)
	base.InputPerMillion, base.OutputPerMillion = "1.6500000000", "9.9000000000"
	return base
}

func cashImageContract20261030ForTest(t *testing.T, raw string) ZTAPIMediaPriceContract {
	t.Helper()
	var contract ZTAPIMediaPriceContract
	require.NoError(t, common.UnmarshalJsonStr(raw, &contract))
	return contract
}

func cashImageFX20261030ForTest() ZTAPIABFXParams {
	return ZTAPIABFXParams{Mode: ZTAPIFXModePlatformV1, PlatformRate: decimal.RequireFromString("6.63"), UpstreamUSDRate: decimal.RequireFromString("6.7138")}
}

func TestZTAPICashImage20261030DerivesFrozenEnterpriseBuckets(t *testing.T) {
	base := cashImageLegacy20261030ForTest(t)
	before, err := common.Marshal(base)
	require.NoError(t, err)
	source, err := deriveZTAPICashImageSource20261030(&base, 7, cashImageFX20261030ForTest())
	require.NoError(t, err)
	after, err := common.Marshal(base)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "the historical parent must remain byte-identical")
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	require.Zero(t, source.ID)
	require.Zero(t, source.Version)
	require.Zero(t, source.CreatedAt)
	require.Equal(t, int64(827), source.CashMarginBaseSourceID)
	require.Equal(t, 85, source.ModelConfigID)
	require.Equal(t, 7, source.OperatorID)
	require.Equal(t, base.QuotationEffectiveAt, source.QuotationEffectiveAt)
	require.Equal(t, "gpt-image-2", source.SourceModel)
	require.Equal(t, "enterprise", source.ResourceType)
	require.Equal(t, "cash_enterprise_20_20261030", source.PricePolicy)
	require.Equal(t, "A", source.SpendTier)
	require.Equal(t, "CNY", source.Currency)
	require.Equal(t, quote.WorkbookSHA256, source.SourceDocumentChecksum)
	require.Equal(t, "A", source.QuotationGrade)
	require.Equal(t, "D100", source.QuotationCell)
	require.Equal(t, "F100", source.OfficialPriceCell)
	require.Equal(t, "zq-g-i-2", source.QuotationModelCode)
	require.Equal(t, `["input_tokens","output_tokens"]`, source.BillingDimensions)
	require.Empty(t, source.TokenPriceRulesJSON)
	require.Equal(t, "26.1838200000", source.InputPerMillion)
	require.Equal(t, "157.1029200000", source.OutputPerMillion)
	require.True(t, decimal.RequireFromString(source.SaleMultiplier).Equal(decimal.NewFromInt(1)))
	for dimension, raw := range ztapiPriceSourceValues(&source) {
		if dimension != "input_tokens" && dimension != "output_tokens" {
			require.True(t, decimal.RequireFromString(raw).IsZero(), dimension)
		}
	}
	require.Equal(t, ZTAPIFXModePlatformV1, source.FXMode)
	require.Equal(t, "6.6300000000", source.CNYPerUSD)
	require.Equal(t, source.CNYPerUSD, source.PlatformCNYPerUnit)
	require.Equal(t, "6.7138000000", source.UpstreamCNYPerUSD)
	contract := cashImageContract20261030ForTest(t, source.MediaPriceContractJSON)
	require.Equal(t, uint64(1), contract.Version)
	require.Equal(t, "image", contract.Modality)
	require.Empty(t, contract.SaleMultiplier)
	require.Len(t, contract.Rules, 10)
	want := map[string][2]string{
		"text_input": {"3.9492941176", "5.1834485300"}, "text_cached_input": {"0.9873235294", "1.2958621400"},
		"image_input": {"6.3188705882", "8.2935176500"}, "image_cached_input": {"1.5797176471", "2.0733794200"},
		"image_output": {"23.6957647059", "31.1006911800"},
	}
	old := cashImageContract20261030ForTest(t, base.MediaPriceContractJSON)
	oldSales := map[string]string{}
	for _, rule := range old.Rules {
		oldSales[rule.ID] = rule.SaleUSD[rule.Conditions["token_bucket"]]
	}
	seen := map[string]bool{}
	for _, rule := range contract.Rules {
		bucket := rule.Conditions["token_bucket"]
		operation := rule.Conditions["image_operation"]
		require.Contains(t, []string{"generation", "edit"}, operation)
		require.Equal(t, operation+"_"+bucket, rule.ID)
		require.Len(t, rule.Conditions, 2)
		require.False(t, seen[rule.ID])
		seen[rule.ID] = true
		require.Equal(t, ZTAPIMediaBillingUnitUSDPerMillionTokens, rule.BillingUnit)
		require.Equal(t, map[string]string{bucket: want[bucket][0]}, rule.CostUSD)
		require.Equal(t, map[string]string{bucket: want[bucket][1]}, rule.SaleUSD)
		require.Equal(t, map[string]string{bucket: "F100"}, rule.SourceCells)
		sale := decimal.RequireFromString(rule.SaleUSD[bucket])
		cost := decimal.RequireFromString(rule.CostUSD[bucket])
		require.True(t, sale.GreaterThanOrEqual(decimal.RequireFromString(oldSales[rule.ID])))
		require.True(t, sale.Mul(decimal.RequireFromString("0.8")).GreaterThanOrEqual(cost.Mul(decimal.RequireFromString("1.05"))))
		require.True(t, sale.Sub(decimal.RequireFromString("0.00000001")).Mul(decimal.RequireFromString("0.8")).LessThan(cost.Mul(decimal.RequireFromString("1.05"))))
	}
	maxima, err := validateZTAPICashImageShape20261030(&source)
	require.NoError(t, err)
	require.Len(t, maxima, 2)
	require.True(t, maxima["input_tokens"].Equal(decimal.RequireFromString("8.29351765")))
	require.True(t, maxima["output_tokens"].Equal(decimal.RequireFromString("31.10069118")))
	maxima["input_tokens"] = decimal.Zero
	again, err := validateZTAPICashImageShape20261030(&source)
	require.NoError(t, err)
	require.True(t, again["input_tokens"].Equal(decimal.RequireFromString("8.29351765")), "returned maps cannot poison future validation")
}

func TestZTAPICashImage20261030AcceptsLegacyMissingQuotationMetadata(t *testing.T) {
	base := cashImageLegacy20261030ForTest(t)
	base.QuotationGrade, base.QuotationCell, base.OfficialPriceCell, base.QuotationModelCode = "", "", "", ""
	base.PlatformCNYPerUnit, base.UpstreamCNYPerUSD = "", ""
	base.SaleMultiplier = ""
	require.NoError(t, validateZTAPICashImageBase20261030(&base), "legacy parent validation must not require current child FX")
	source, err := deriveZTAPICashImageSource20261030(&base, 7, cashImageFX20261030ForTest())
	require.NoError(t, err)
	_, err = validateZTAPICashImageShape20261030(&source)
	require.NoError(t, err)
	source.ID = 828
	_, err = validateZTAPICashImageShape20261030(&source)
	require.NoError(t, err, "persisted sources keep their own row identity")
}

func TestZTAPICashImage20261030FreezesBothFXRatesAndRebuildsFromChild(t *testing.T) {
	base := cashImageLegacy20261030ForTest(t)
	fx := cashImageFX20261030ForTest()
	original, err := deriveZTAPICashImageSource20261030(&base, 7, fx)
	require.NoError(t, err)
	before, err := common.Marshal(original)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, platform, upstream, inputCNY, outputCNY, inputU, inputSale, inputMax, outputMax string
	}{
		{"platform change", "6.7", "6.7138", "26.1838200000", "157.1029200000", "3.9080328358", "5.1292931000", "8.20686896", "30.77575859"},
		{"upstream change", "6.63", "6.9", "26.9100000000", "161.4600000000", "4.0588235294", "5.3272058900", "8.52352942", "31.96323530"},
		{"ten-place rounding", "6.6300000001", "6.7138123456", "26.1838681478", "157.1032088870", "3.9493013797", "5.1834580700", "8.29353290", "31.10074837"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx.PlatformRate, fx.UpstreamUSDRate = decimal.RequireFromString(tc.platform), decimal.RequireFromString(tc.upstream)
			next, err := deriveZTAPICashImageSource20261030(&base, 7, fx)
			require.NoError(t, err)
			require.Equal(t, tc.inputCNY, next.InputPerMillion)
			require.Equal(t, tc.outputCNY, next.OutputPerMillion)
			require.NotEqual(t, original.MediaPriceContractJSON, next.MediaPriceContractJSON)
			contract := cashImageContract20261030ForTest(t, next.MediaPriceContractJSON)
			for _, rule := range contract.Rules {
				if rule.Conditions["token_bucket"] == "text_input" {
					require.Equal(t, tc.inputU, rule.CostUSD["text_input"])
					require.Equal(t, tc.inputSale, rule.SaleUSD["text_input"])
				}
			}
			maximum, err := validateZTAPICashImageShape20261030(&next)
			require.NoError(t, err)
			require.True(t, maximum["input_tokens"].Equal(decimal.RequireFromString(tc.inputMax)))
			require.True(t, maximum["output_tokens"].Equal(decimal.RequireFromString(tc.outputMax)))
			frozen, err := validateZTAPICashImageShape20261030(&original)
			require.NoError(t, err, "changing externally supplied FX cannot invalidate an already frozen child")
			require.True(t, frozen["input_tokens"].Equal(decimal.RequireFromString("8.29351765")))
			require.True(t, frozen["output_tokens"].Equal(decimal.RequireFromString("31.10069118")))
			after, err := common.Marshal(original)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
	require.NoError(t, validateZTAPICashImageBase20261030(&base), "the legacy parent keeps zero FX; derivation must use the explicit child FX")
}

func TestZTAPICashImage20261030RejectsMissingOrUnfreezableFX(t *testing.T) {
	base := cashImageLegacy20261030ForTest(t)
	for name, mutate := range map[string]func(*ZTAPIABFXParams){
		"legacy mode":               func(fx *ZTAPIABFXParams) { fx.Mode = "" },
		"unknown mode":              func(fx *ZTAPIABFXParams) { fx.Mode = "unknown" },
		"zero platform":             func(fx *ZTAPIABFXParams) { fx.PlatformRate = decimal.Zero },
		"negative platform":         func(fx *ZTAPIABFXParams) { fx.PlatformRate = decimal.RequireFromString("-6.63") },
		"low platform":              func(fx *ZTAPIABFXParams) { fx.PlatformRate = decimal.RequireFromString("4.99") },
		"high platform":             func(fx *ZTAPIABFXParams) { fx.PlatformRate = decimal.RequireFromString("9.01") },
		"zero upstream":             func(fx *ZTAPIABFXParams) { fx.UpstreamUSDRate = decimal.Zero },
		"negative upstream":         func(fx *ZTAPIABFXParams) { fx.UpstreamUSDRate = decimal.RequireFromString("-6.7138") },
		"excess platform precision": func(fx *ZTAPIABFXParams) { fx.PlatformRate = decimal.RequireFromString("6.63000000001") },
		"excess upstream precision": func(fx *ZTAPIABFXParams) { fx.UpstreamUSDRate = decimal.RequireFromString("6.71380000001") },
	} {
		t.Run(name, func(t *testing.T) {
			fx := cashImageFX20261030ForTest()
			mutate(&fx)
			before, err := common.Marshal(base)
			require.NoError(t, err)
			source, err := deriveZTAPICashImageSource20261030(&base, 7, fx)
			require.Error(t, err)
			require.Equal(t, ZTAPIModelPriceSource{}, source)
			after, err := common.Marshal(base)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
	_, err := deriveZTAPICashImageSource20261030(&base, 7, ZTAPIABFXParams{})
	require.Error(t, err, "zero-value FX must never fall back to USD=U")
}

func TestZTAPICashImage20261030RetainsOldRetailFloorWhenFXCostFalls(t *testing.T) {
	base := cashImageLegacy20261030ForTest(t)
	fx := ZTAPIABFXParams{Mode: ZTAPIFXModePlatformV1, PlatformRate: decimal.RequireFromString("9"), UpstreamUSDRate: decimal.RequireFromString("5")}
	source, err := deriveZTAPICashImageSource20261030(&base, 7, fx)
	require.NoError(t, err)
	require.Equal(t, "19.5000000000", source.InputPerMillion)
	require.Equal(t, "117.0000000000", source.OutputPerMillion)
	old := cashImageContract20261030ForTest(t, base.MediaPriceContractJSON)
	previous := map[string]decimal.Decimal{}
	for _, rule := range old.Rules {
		previous[rule.ID] = decimal.RequireFromString(rule.SaleUSD[rule.Conditions["token_bucket"]])
	}
	contract := cashImageContract20261030ForTest(t, source.MediaPriceContractJSON)
	require.Len(t, contract.Rules, 10)
	for _, rule := range contract.Rules {
		bucket := rule.Conditions["token_bucket"]
		sale := decimal.RequireFromString(rule.SaleUSD[bucket])
		cost := decimal.RequireFromString(rule.CostUSD[bucket])
		require.True(t, sale.Equal(previous[rule.ID]))
		require.True(t, sale.Mul(decimal.RequireFromString("0.8")).GreaterThanOrEqual(cost.Mul(decimal.RequireFromString("1.05"))))
	}
	maximum, err := validateZTAPICashImageShape20261030(&source)
	require.NoError(t, err)
	require.True(t, maximum["input_tokens"].Equal(decimal.RequireFromString("6.4")))
	require.True(t, maximum["output_tokens"].Equal(decimal.RequireFromString("24")))
}

func TestZTAPICashImage20261030RejectsForgedParentsWithoutMutation(t *testing.T) {
	mutations := map[string]func(*ZTAPIModelPriceSource){
		"missing id":             func(s *ZTAPIModelPriceSource) { s.ID = 0 },
		"missing model config":   func(s *ZTAPIModelPriceSource) { s.ModelConfigID = 0 },
		"wrong model":            func(s *ZTAPIModelPriceSource) { s.SourceModel = "gp-image-2" },
		"wrong resource":         func(s *ZTAPIModelPriceSource) { s.ResourceType = "enterprise" },
		"wrong policy":           func(s *ZTAPIModelPriceSource) { s.PricePolicy = "cash_enterprise_20_20261030" },
		"wrong checksum":         func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = strings.Repeat("a", 64) },
		"wrong currency":         func(s *ZTAPIModelPriceSource) { s.Currency = "CNY" },
		"derived parent":         func(s *ZTAPIModelPriceSource) { s.CashMarginBaseSourceID = 826 },
		"discounted parent":      func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "0.5" },
		"missing effective time": func(s *ZTAPIModelPriceSource) { s.QuotationEffectiveAt = 0 },
		"token rules":            func(s *ZTAPIModelPriceSource) { s.TokenPriceRulesJSON = "[]" },
		"input cost":             func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "3.9" },
		"output cost":            func(s *ZTAPIModelPriceSource) { s.OutputPerMillion = "23.4" },
		"unbilled cost":          func(s *ZTAPIModelPriceSource) { s.RequestUnitCost = "1" },
		"nonzero FX":             func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "7" },
		"active FX mode":         func(s *ZTAPIModelPriceSource) { s.FXMode = "platform_v1" },
		"five bucket matrix": func(s *ZTAPIModelPriceSource) {
			row, ok := ztapiMediaPriceRowFromManifest(ztapiQuotation, "gpt-image-2")
			require.True(t, ok)
			s.MediaPriceContractJSON = row.MediaPriceContractJSON
		},
		"forged price": func(s *ZTAPIModelPriceSource) {
			s.MediaPriceContractJSON = strings.Replace(s.MediaPriceContractJSON, `"24.00"`, `"25.00"`, 1)
		},
		"forged cost": func(s *ZTAPIModelPriceSource) {
			s.MediaPriceContractJSON = strings.Replace(s.MediaPriceContractJSON, `"9.90"`, `"10.00"`, 1)
		},
		"noncanonical matrix": func(s *ZTAPIModelPriceSource) { s.MediaPriceContractJSON += " " },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			base := cashImageLegacy20261030ForTest(t)
			mutate(&base)
			before, err := common.Marshal(base)
			require.NoError(t, err)
			require.Error(t, validateZTAPICashImageBase20261030(&base))
			source, err := deriveZTAPICashImageSource20261030(&base, 7, cashImageFX20261030ForTest())
			require.Error(t, err)
			require.Equal(t, ZTAPIModelPriceSource{}, source)
			after, err := common.Marshal(base)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
	require.Error(t, validateZTAPICashImageBase20261030(nil))
	_, err := deriveZTAPICashImageSource20261030(nil, 7, cashImageFX20261030ForTest())
	require.Error(t, err)
	base := cashImageLegacy20261030ForTest(t)
	_, err = deriveZTAPICashImageSource20261030(&base, 0, cashImageFX20261030ForTest())
	require.Error(t, err)
}

func TestZTAPICashImage20261030RejectsNoncanonicalDerivedShape(t *testing.T) {
	base := cashImageLegacy20261030ForTest(t)
	source, err := deriveZTAPICashImageSource20261030(&base, 7, cashImageFX20261030ForTest())
	require.NoError(t, err)
	mutations := map[string]func(*ZTAPIModelPriceSource){
		"missing parent":         func(s *ZTAPIModelPriceSource) { s.CashMarginBaseSourceID = 0 },
		"negative parent":        func(s *ZTAPIModelPriceSource) { s.CashMarginBaseSourceID = -1 },
		"self parent":            func(s *ZTAPIModelPriceSource) { s.ID = s.CashMarginBaseSourceID },
		"future parent":          func(s *ZTAPIModelPriceSource) { s.ID = s.CashMarginBaseSourceID - 1 },
		"negative row id":        func(s *ZTAPIModelPriceSource) { s.ID = -1 },
		"missing operator":       func(s *ZTAPIModelPriceSource) { s.OperatorID = 0 },
		"missing model config":   func(s *ZTAPIModelPriceSource) { s.ModelConfigID = 0 },
		"wrong source":           func(s *ZTAPIModelPriceSource) { s.SourceModel = "gpt-image-2-oai" },
		"wrong resource":         func(s *ZTAPIModelPriceSource) { s.ResourceType = "pool" },
		"wrong policy":           func(s *ZTAPIModelPriceSource) { s.PricePolicy = "enterprise_20_margin" },
		"wrong grade":            func(s *ZTAPIModelPriceSource) { s.QuotationGrade = "B" },
		"wrong tier":             func(s *ZTAPIModelPriceSource) { s.SpendTier = "B" },
		"wrong cell":             func(s *ZTAPIModelPriceSource) { s.QuotationCell = "D99" },
		"wrong official cell":    func(s *ZTAPIModelPriceSource) { s.OfficialPriceCell = "F99" },
		"wrong model code":       func(s *ZTAPIModelPriceSource) { s.QuotationModelCode = "fake" },
		"wrong workbook":         func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = ZTAPIQuotationSHA256 },
		"wrong currency":         func(s *ZTAPIModelPriceSource) { s.Currency = "USD" },
		"missing effective time": func(s *ZTAPIModelPriceSource) { s.QuotationEffectiveAt = 0 },
		"token rules":            func(s *ZTAPIModelPriceSource) { s.TokenPriceRulesJSON = "[]" },
		"wrong dimensions":       func(s *ZTAPIModelPriceSource) { s.BillingDimensions = `["image"]` },
		"discount":               func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "0.95" },
		"empty multiplier":       func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "" },
		"wrong input cost":       func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "1.65" },
		"wrong output cost":      func(s *ZTAPIModelPriceSource) { s.OutputPerMillion = "9.9" },
		"unbilled cache":         func(s *ZTAPIModelPriceSource) { s.CacheReadPerMillion = "0.01" },
		"unbilled request":       func(s *ZTAPIModelPriceSource) { s.RequestUnitCost = "0.01" },
		"unbilled image":         func(s *ZTAPIModelPriceSource) { s.ImageUnitCost = "0.01" },
		"unbilled audio":         func(s *ZTAPIModelPriceSource) { s.AudioUnitCost = "0.01" },
		"missing FX":             func(s *ZTAPIModelPriceSource) { s.FXMode = "" },
		"unknown FX":             func(s *ZTAPIModelPriceSource) { s.FXMode = "legacy" },
		"mismatched CNY rate":    func(s *ZTAPIModelPriceSource) { s.CNYPerUSD = "7" },
		"zero platform rate":     func(s *ZTAPIModelPriceSource) { s.PlatformCNYPerUnit, s.CNYPerUSD = "0", "0" },
		"zero upstream rate":     func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "0" },
		"missing upstream rate":  func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "" },
		"changed upstream rate":  func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "6.9" },
		"changed platform rate":  func(s *ZTAPIModelPriceSource) { s.PlatformCNYPerUnit, s.CNYPerUSD = "6.7", "6.7" },
		"padded JSON":            func(s *ZTAPIModelPriceSource) { s.MediaPriceContractJSON += " " },
		"malformed JSON":         func(s *ZTAPIModelPriceSource) { s.MediaPriceContractJSON = "{" },
	}
	for name, mutate := range map[string]func(*ZTAPIMediaPriceContract){
		"missing rule":       func(c *ZTAPIMediaPriceContract) { c.Rules = c.Rules[:9] },
		"duplicate rule":     func(c *ZTAPIMediaPriceContract) { c.Rules = append(c.Rules, c.Rules[0]) },
		"matrix operation":   func(c *ZTAPIMediaPriceContract) { c.Rules[0].Conditions["image_operation"] = "generation" },
		"unit":               func(c *ZTAPIMediaPriceContract) { c.Rules[0].BillingUnit = "usd_per_token" },
		"bucket cost":        func(c *ZTAPIMediaPriceContract) { c.Rules[0].CostUSD[c.Rules[0].Conditions["token_bucket"]] = "1" },
		"underpriced bucket": func(c *ZTAPIMediaPriceContract) { c.Rules[0].SaleUSD[c.Rules[0].Conditions["token_bucket"]] = "1" },
		"overpriced bucket":  func(c *ZTAPIMediaPriceContract) { c.Rules[0].SaleUSD[c.Rules[0].Conditions["token_bucket"]] = "99" },
		"wrong source cell": func(c *ZTAPIMediaPriceContract) {
			c.Rules[0].SourceCells[c.Rules[0].Conditions["token_bucket"]] = "G56"
		},
		"inline multiplier":     func(c *ZTAPIMediaPriceContract) { c.SaleMultiplier = "0.5" },
		"noncanonical ordering": func(c *ZTAPIMediaPriceContract) { c.Rules[0], c.Rules[1] = c.Rules[1], c.Rules[0] },
	} {
		mutations[name] = func(s *ZTAPIModelPriceSource) {
			contract := cashImageContract20261030ForTest(t, s.MediaPriceContractJSON)
			mutate(&contract)
			raw, err := common.Marshal(contract)
			require.NoError(t, err)
			s.MediaPriceContractJSON = string(raw)
		}
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := source
			mutate(&copy)
			before, err := common.Marshal(copy)
			require.NoError(t, err)
			maximum, err := validateZTAPICashImageShape20261030(&copy)
			require.Error(t, err)
			require.Nil(t, maximum)
			after, err := common.Marshal(copy)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "shape validation must not repair or mutate submitted data")
		})
	}
	_, err = validateZTAPICashImageShape20261030(nil)
	require.Error(t, err)
}
