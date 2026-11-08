package model

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func imageSaleReadinessCost(t *testing.T) string {
	t.Helper()
	contract := types.ZTAPIMediaPriceContract{Version: 1, Modality: "image"}
	for _, bucket := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		contract.Rules = append(contract.Rules, types.ZTAPIMediaPriceRule{ID: bucket, Conditions: map[string]string{"token_bucket": bucket},
			BillingUnit: types.ZTAPIMediaBillingUnitUSDPerMillionTokens, CostUSD: map[string]string{bucket: "0.6"}, SaleUSD: map[string]string{bucket: "1"}, SourceCells: map[string]string{bucket: "A1"}})
	}
	raw, err := common.Marshal(contract)
	require.NoError(t, err)
	return string(raw)
}

func imageSaleReadinessCatalog(t *testing.T) (*gorm.DB, ZTAPIModelPublicationSnapshot) {
	t.Helper()
	db := referralReadinessCatalog(t)
	name := "zt-gp-image-2"
	config := ZTAPIModelConfig{ID: 85, SourceModel: "gpt-image-2", PublicName: &name, Protocol: ZTAPIProtocolOpenAICompatible,
		ProviderFamily: ZTAPIProviderOpenAI, EnabledGroups: `["default"]`, Published: true, Version: 28}
	require.NoError(t, db.Create(&config).Error)
	source := validZTAPIPriceSourceForTest()
	source.ID, source.ModelConfigID, source.SourceModel = 1873, config.ID, config.SourceModel
	source.MediaPriceContractJSON = imageSaleReadinessCost(t)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&source).Error)
	_, protocol, err := types.SealZTAPIImageProtocolContract(image2ABEditProtocol(t))
	require.NoError(t, err)
	snap := ZTAPIModelPublicationSnapshot{ID: 1632, ModelConfigID: config.ID, ModelVersion: config.Version, SourceModel: config.SourceModel,
		PublicName: name, Protocol: config.Protocol, ProviderFamily: config.ProviderFamily, EnabledGroups: config.EnabledGroups,
		AllowedChannelIDs: `[11,13]`, PriceSourceID: source.ID, MediaPriceContractJSON: source.MediaPriceContractJSON,
		ImageProtocolContractJSON: protocol, ImageSaleContractJSON: types.ZTAPIImageSaleContract20261109JSON(), VerificationIDs: `[1]`, IdentityUpdatedAt: 1, CreatedAt: 2}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&snap).Error)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", snap.ID).Error)
	return db, snap
}

func TestZTAPIImageSaleReadinessLegacyKeysAndFingerprintStayStable(t *testing.T) {
	t.Setenv("ZTAPI_SOURCE_COMMIT", "legacy-catalog-fixture")
	db := referralReadinessCatalog(t)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelConfig{}).Where("published = ?", true).Updates(map[string]any{"created_at": 10, "updated_at": 20}).Error)
	hash, keys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, []string{
		"deepseek-v4-flash/openai_compatible/channel:11/tier:1/input_tokens", "deepseek-v4-flash/openai_compatible/channel:11/tier:1/output_tokens",
		"deepseek-v4-flash/openai_compatible/channel:13/tier:1/input_tokens", "deepseek-v4-flash/openai_compatible/channel:13/tier:1/output_tokens", "hosted-tools/fetch", "hosted-tools/search",
	}, keys)
	require.Equal(t, "9ad8c1ee3bd4abd594cdb5ba9dae397ab320a343e219556d2d04ff8ef93a1df6", hash, "legacy payload and keys must remain byte compatible")
}

func TestZTAPIImageSaleReadinessKeysUseImagesForEveryQuotedTier(t *testing.T) {
	db, _ := imageSaleReadinessCatalog(t)
	_, keys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	var imageKeys []string
	for _, key := range keys {
		if strings.HasPrefix(key, "gpt-image-2/") {
			imageKeys = append(imageKeys, key)
			require.Contains(t, key, "/spec:image_sale_")
			require.True(t, strings.HasSuffix(key, "/images"), key)
			require.NotContains(t, key, "text_input")
			require.NotContains(t, key, "image_output")
		}
	}
	require.Len(t, imageKeys, 20, "two allowed channels, generation/edit, five independent quoted tiers")
	require.Contains(t, imageKeys, "gpt-image-2/openai_compatible/channel:11/spec:image_sale_generation_1k/images")
	require.Contains(t, imageKeys, "gpt-image-2/openai_compatible/channel:13/spec:image_sale_edit_extra_reference_unit/images")
}

func TestZTAPIImageSaleReadinessCannotUseTokenRetailAttestation(t *testing.T) {
	gate := referralGate()
	key := "gpt-image-2/openai_compatible/channel:11/spec:image_sale_generation_1k/images"
	gate.RequiredKeys, gate.Items[0].Key = []string{key}, key
	report, err := EvaluateZTAPIReferralPriceGate(gate)
	require.NoError(t, err)
	require.False(t, report.Allowed, "a profitable legacy Token item is not per-image quote evidence")
}

func imageSaleReadinessGate(t *testing.T, db *gorm.DB) (string, ZTAPIReferralPriceGateInput) {
	t.Helper()
	fingerprint, keys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	quoted, err := ZTAPIReferralImageSaleQuoteItems(t.Context(), db)
	require.NoError(t, err)
	byKey := map[string]ZTAPIReferralPriceGateItem{}
	for _, item := range quoted {
		require.False(t, item.ModelMappingKnown, "operator must review the actual route")
		require.False(t, item.PlatformFeeKnown, "quote builder must not invent a fee waiver")
		require.Empty(t, item.PlatformFee)
		item.ModelMappingKnown, item.PlatformFeeKnown, item.PlatformFee = true, true, "0"
		byKey[item.Key] = item
	}
	gate := ZTAPIReferralPriceGateInput{CatalogComplete: true, CatalogEvidence: "synthetic protected quote review", RequiredKeys: keys}
	for _, key := range keys {
		item, ok := byKey[key]
		if !ok {
			item = referralGate().Items[0]
			item.Key = key
		}
		gate.Items = append(gate.Items, item)
	}
	return fingerprint, gate
}

func imageSaleProtectedProof(t *testing.T, fingerprint string, gate ZTAPIReferralPriceGateInput) (time.Time, func(string, ZTAPIReferralPriceGateInput)) {
	t.Helper()
	now := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)
	file := filepath.Join(t.TempDir(), "synthetic-cost-attestation.json")
	t.Setenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE", file)
	t.Setenv("ZTAPI_REFERRAL_WORKER_ENABLED", "true")
	oldWorker, oldFinance := ZTAPIReferralWorkerCapabilityCheck, ZTAPIReferralFinanceCapabilityCheck
	t.Cleanup(func() {
		ZTAPIReferralWorkerCapabilityCheck, ZTAPIReferralFinanceCapabilityCheck = oldWorker, oldFinance
	})
	ZTAPIReferralWorkerCapabilityCheck = func(time.Time) bool { return true }
	ZTAPIReferralFinanceCapabilityCheck = func(context.Context) bool { return true }
	write := func(fingerprint string, gate ZTAPIReferralPriceGateInput) {
		proof := ZTAPIReferralCostAttestation{Version: 1, CatalogSHA256: fingerprint, VerifiedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
			FinanceCapability: "referral-v1", PlatformFeeKnown: true, PlatformFeeEvidence: "synthetic explicit fee waiver", PriceGate: gate}
		raw, err := common.Marshal(proof)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(file, raw, 0600))
	}
	write(fingerprint, gate)
	return now, write
}

func TestZTAPIImageSaleReadinessAdmitsQuotedAssumptionsWithoutHardCaps(t *testing.T) {
	db, _ := imageSaleReadinessCatalog(t)
	fingerprint, gate := imageSaleReadinessGate(t, db)
	now, _ := imageSaleProtectedProof(t, fingerprint, gate)
	input, blocked, err := ProductionZTAPIReferralReadiness(t.Context(), db, now)
	require.NoError(t, err)
	require.Empty(t, blocked)
	for _, item := range input.PriceGate.Items {
		if strings.Contains(item.Key, "/spec:image_sale_") {
			evidence, err := types.ParseZTAPIImageSaleReferralEvidence(item.Evidence)
			require.NoError(t, err)
			require.False(t, evidence.HardCapKnown)
			require.True(t, evidence.QuoteOnly)
			require.Empty(t, item.SupplierCostUpperBound)
			require.Empty(t, item.UsageUpperBound)
		}
	}
	report, err := CheckZTAPIReferralReadiness(t.Context(), input)
	require.NoError(t, err)
	require.True(t, report.Ready, report.BlockedReasons)
	RegisterZTAPIReferralProductionAdmissionGuard()
	require.NoError(t, ZTAPIReferralBindingAdmissionGuard(db.WithContext(t.Context()), now))
}

func TestZTAPIImageSaleReadinessRejectsForgedOrIncompleteQuoteEvidence(t *testing.T) {
	for _, change := range []string{"fingerprint", "cheaper cost", "assumptions", "hard cap", "operation", "missing tier", "duplicate tier", "legacy upper bound", "token revenue", "fee unknown"} {
		t.Run(change, func(t *testing.T) {
			db, _ := imageSaleReadinessCatalog(t)
			fingerprint, gate := imageSaleReadinessGate(t, db)
			index := -1
			for i, item := range gate.Items {
				if strings.Contains(item.Key, "/spec:image_sale_generation_1k/") {
					index = i
					break
				}
			}
			require.NotEqual(t, -1, index)
			item := &gate.Items[index]
			evidence, err := types.ParseZTAPIImageSaleReferralEvidence(item.Evidence)
			require.NoError(t, err)
			switch change {
			case "fingerprint":
				evidence.QuoteFingerprintSHA256 = strings.Repeat("b", 64)
			case "cheaper cost":
				evidence.Item.QuotedSupplierCostUSD = "0.0001"
			case "assumptions":
				evidence.Item.AssumedTokensPerImage["image_output"] = 1
			case "hard cap":
				evidence.HardCapKnown = true
			case "operation":
				evidence.Operation = "edit"
			case "legacy upper bound":
				item.SupplierCostUpperBound, item.UsageUpperBound = "0.0001", "1"
			case "token revenue":
				item.CustomerRevenue = "100"
			case "fee unknown":
				item.PlatformFeeKnown = false
			}
			raw, err := common.Marshal(evidence)
			require.NoError(t, err)
			item.Evidence = string(raw)
			if change == "missing tier" {
				gate.Items = append(gate.Items[:index], gate.Items[index+1:]...)
			}
			if change == "duplicate tier" {
				gate.Items = append(gate.Items, *item)
			}
			now, _ := imageSaleProtectedProof(t, fingerprint, gate)
			input, blocked, err := ProductionZTAPIReferralReadiness(t.Context(), db, now)
			require.NoError(t, err)
			report, err := CheckZTAPIReferralReadiness(t.Context(), input)
			require.NoError(t, err)
			require.True(t, len(blocked) > 0 || !report.Ready, "untrusted quote inputs cannot admit a new referral")
		})
	}
}

func TestZTAPIImageSaleReadinessSourceQuoteChangeRequiresFreshTierEvidence(t *testing.T) {
	db, snap := imageSaleReadinessCatalog(t)
	fingerprint, gate := imageSaleReadinessGate(t, db)
	now, write := imageSaleProtectedProof(t, fingerprint, gate)
	// Fault injection changes only the local fixture, never a live authority.
	cheaper := strings.ReplaceAll(strings.ReplaceAll(snap.MediaPriceContractJSON, `"0.6"`, `"0.12"`), `"1"`, `"0.2"`)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPriceSource{}).Where("id = ?", snap.PriceSourceID).Update("media_price_contract_json", cheaper).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", snap.ID).Update("media_price_contract_json", cheaper).Error)
	after, keys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	require.NotEqual(t, fingerprint, after)
	require.Equal(t, gate.RequiredKeys, keys)
	write(after, gate)
	_, blocked, err := ProductionZTAPIReferralReadiness(t.Context(), db, now)
	require.NoError(t, err)
	require.Contains(t, blocked, "image_sale_quoted_evidence_missing_or_changed", "refreshing only catalog_sha256 cannot reuse stale per-tier quote evidence")
}

func TestZTAPIImageSaleReadinessQuoteMarginAndExplicitFeeAreBothEnforced(t *testing.T) {
	db, _ := imageSaleReadinessCatalog(t)
	_, gate := imageSaleReadinessGate(t, db)
	for i := range gate.Items {
		if !strings.Contains(gate.Items[i].Key, "/spec:image_sale_") {
			continue
		}
		for _, fee := range []string{"0", "1"} {
			item := gate.Items[i]
			item.PlatformFee = fee
			report, err := EvaluateZTAPIReferralPriceGate(ZTAPIReferralPriceGateInput{CatalogComplete: true, CatalogEvidence: "synthetic quote", RequiredKeys: []string{item.Key}, Items: []ZTAPIReferralPriceGateItem{item}})
			require.NoError(t, err)
			require.Equal(t, fee == "0", report.Allowed, item.Key)
		}
	}
}

func TestZTAPIImageSaleReadinessEditQuoteCannotBorrowGenerationMargin(t *testing.T) {
	db, snap := imageSaleReadinessCatalog(t)
	var base types.ZTAPIMediaPriceContract
	require.NoError(t, common.UnmarshalJsonStr(snap.MediaPriceContractJSON, &base))
	rules := []types.ZTAPIMediaPriceRule{}
	for _, operation := range []string{"generation", "edit"} {
		for _, original := range base.Rules {
			rule := original
			rule.ID = operation + "_" + original.ID
			rule.Conditions = map[string]string{"image_operation": operation, "token_bucket": original.ID}
			if operation == "edit" {
				rule.CostUSD = map[string]string{original.ID: "30"}
				rule.SaleUSD = map[string]string{original.ID: "50"}
			}
			rules = append(rules, rule)
		}
	}
	base.Rules = rules
	raw, err := common.Marshal(base)
	require.NoError(t, err)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPriceSource{}).Where("id = ?", snap.PriceSourceID).Update("media_price_contract_json", string(raw)).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", snap.ID).Update("media_price_contract_json", string(raw)).Error)
	_, gate := imageSaleReadinessGate(t, db)
	for _, item := range gate.Items {
		if !strings.Contains(item.Key, "/spec:image_sale_") {
			continue
		}
		report, err := EvaluateZTAPIReferralPriceGate(ZTAPIReferralPriceGateInput{CatalogComplete: true, CatalogEvidence: "synthetic operation-specific quote", RequiredKeys: []string{item.Key}, Items: []ZTAPIReferralPriceGateItem{item}})
		require.NoError(t, err)
		require.Equal(t, strings.Contains(item.Key, "image_sale_generation_"), report.Allowed, item.Key)
	}
}

func TestZTAPIImageSaleReadinessDoesNotInventEditKeysForGenerationOnly(t *testing.T) {
	db, snap := imageSaleReadinessCatalog(t)
	protocol := image2ABProtocol(t)
	_, raw, err := types.SealZTAPIImageProtocolContract(protocol)
	require.NoError(t, err)
	_, _, err = types.ParseZTAPIImageProtocolContract(raw)
	require.NoError(t, err, "generation-only fixture must be a parseable sealed authority")
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", snap.ID).Update("image_protocol_contract_json", raw).Error)
	items, err := ZTAPIReferralImageSaleQuoteItems(t.Context(), db)
	require.NoError(t, err)
	require.Len(t, items, 10)
	for _, item := range items {
		require.NotContains(t, item.Key, "image_sale_edit_")
	}
}
