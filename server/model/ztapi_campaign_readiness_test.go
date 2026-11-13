package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func referralReadinessCatalog(t *testing.T) *gorm.DB {
	t.Helper()
	db := referralTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIModelPriceSource{}, &ZTAPIModelPublicationSnapshot{}, &Channel{}, &Option{}))
	seedZTAPIPublicCatalogRecord(t, db, "deepseek-v4-flash", "zt-deepseek-v4-flash", ZTAPIProviderDeepSeek, ZTAPIProtocolOpenAICompatible, []string{"input_tokens", "output_tokens"})
	base := "https://supplier-a.invalid/v1"
	for _, id := range []int{11, 13} {
		require.NoError(t, db.Create(&Channel{Id: id, Type: 1, Key: "synthetic-not-a-live-key", Status: common.ChannelStatusEnabled, BaseURL: &base, Models: "deepseek-v4-flash", Group: "default"}).Error)
	}
	return db
}

func TestZTAPIReferralCatalogPinsActualProcurementIdentity(t *testing.T) {
	for _, change := range []struct {
		column string
		value  any
	}{
		{"base_url", "https://supplier-b.invalid/v1"}, {"type", 14}, {"key", "different-synthetic-account"},
		{"open_ai_organization", "synthetic-other-organization"},
		{"ztapi_key_ciphertext", "synthetic-encrypted-account"}, {"header_override", `{"x-group":"different"}`},
		{"param_override", `{"model":"different"}`}, {"settings", `{"force_format":true}`},
	} {
		t.Run(change.column, func(t *testing.T) {
			db := referralReadinessCatalog(t)
			before, _, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
			require.NoError(t, err)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", 11).Update(change.column, change.value).Error)
			after, _, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
			require.NoError(t, err)
			require.NotEqual(t, before, after, "actual procurement identity changes must invalidate reviewed cost evidence")
		})
	}
}

func TestZTAPIReferralWorkerNotImpliedByEnvironment(t *testing.T) {
	t.Setenv("ZTAPI_REFERRAL_WORKER_ENABLED", "true")
	db := referralReadinessCatalog(t)
	input, _, err := ProductionZTAPIReferralReadiness(context.Background(), db, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, input.WorkerReady, "a configured but absent worker must not admit new financial promises")
}

func TestZTAPIReferralBindingAdmissionRequiresSuccessfulWorker(t *testing.T) {
	db := referralReadinessCatalog(t)
	now := time.Now().UTC()
	fingerprint, keys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	gate := ZTAPIReferralPriceGateInput{CatalogComplete: true, CatalogEvidence: "synthetic local evidence", RequiredKeys: keys}
	for _, key := range keys {
		item := referralGate().Items[0]
		item.Key = key
		gate.Items = append(gate.Items, item)
	}
	proof := ZTAPIReferralCostAttestation{Version: 1, CatalogSHA256: fingerprint, VerifiedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix(), FinanceCapability: "referral-v1", PlatformFeeKnown: true, PlatformFeeEvidence: "synthetic local evidence", PriceGate: gate}
	body, err := common.Marshal(proof)
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "test-cost-proof.json")
	require.NoError(t, os.WriteFile(file, body, 0600))
	t.Setenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE", file)
	t.Setenv("ZTAPI_REFERRAL_WORKER_ENABLED", "true")
	oldWorker, oldFinance := ZTAPIReferralWorkerCapabilityCheck, ZTAPIReferralFinanceCapabilityCheck
	t.Cleanup(func() {
		ZTAPIReferralWorkerCapabilityCheck, ZTAPIReferralFinanceCapabilityCheck = oldWorker, oldFinance
	})
	ZTAPIReferralFinanceCapabilityCheck = func(context.Context) bool { return true }
	RegisterZTAPIReferralProductionAdmissionGuard()
	ZTAPIReferralWorkerCapabilityCheck = nil
	require.ErrorIs(t, ZTAPIReferralBindingAdmissionGuard(db.WithContext(t.Context()), now), ErrZTAPIReferralNotReady)
	ZTAPIReferralWorkerCapabilityCheck = func(time.Time) bool { return false }
	require.ErrorIs(t, ZTAPIReferralBindingAdmissionGuard(db.WithContext(t.Context()), now), ErrZTAPIReferralNotReady)
	ZTAPIReferralWorkerCapabilityCheck = func(time.Time) bool { return true }
	require.NoError(t, ZTAPIReferralBindingAdmissionGuard(db.WithContext(t.Context()), now))
}

func TestZTAPIReferralProtectedCostEvidenceCannotBeInventedOrStale(t *testing.T) {
	now := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	_, err := readZTAPIReferralCostAttestation("", now)
	require.Error(t, err)
	file := filepath.Join(t.TempDir(), "cost-evidence.json")
	proof := ZTAPIReferralCostAttestation{Version: 1, CatalogSHA256: "catalog", VerifiedAt: now.Unix() - 60, ExpiresAt: now.Unix() + 3600, FinanceCapability: "referral-v1", PlatformFeeKnown: true, PlatformFeeEvidence: "verified fee schedule", PriceGate: ZTAPIReferralPriceGateInput{CatalogComplete: true, CatalogEvidence: "quote original cells"}}
	write := func() {
		raw, e := common.Marshal(proof)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(file, raw, 0600))
	}
	write()
	_, err = readZTAPIReferralCostAttestation(file, now)
	require.NoError(t, err)
	proof.ExpiresAt = now.Unix()
	write()
	_, err = readZTAPIReferralCostAttestation(file, now)
	require.Error(t, err)
	proof.ExpiresAt = now.Unix() + 3600
	proof.VerifiedAt = now.Unix() + 1
	write()
	_, err = readZTAPIReferralCostAttestation(file, now)
	require.Error(t, err)
	proof.VerifiedAt = now.Unix() - 60
	proof.Version = 0
	write()
	_, err = readZTAPIReferralCostAttestation(file, now)
	require.Error(t, err)
}

func TestZTAPIReferralRollingCatalogProofIsExactAndExpires(t *testing.T) {
	db := referralReadinessCatalog(t)
	now := time.Now().UTC()
	t.Setenv("ZTAPI_SOURCE_COMMIT", "source-before")
	before, keys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	t.Setenv("ZTAPI_SOURCE_COMMIT", "source-after")
	after, nextKeys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, keys, nextKeys)
	file := filepath.Join(t.TempDir(), "transition-proof.json")
	proof := ZTAPIReferralCostAttestation{Version: 1, CatalogSHA256: before, TransitionCatalogSHA256: after, TransitionExpiresAt: now.Add(time.Minute).Unix(), VerifiedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix(), PriceGate: ZTAPIReferralPriceGateInput{RequiredKeys: keys}}
	raw, err := common.Marshal(proof)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, raw, 0600))
	t.Setenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE", file)
	_, blockers, err := ProductionZTAPIReferralReadiness(t.Context(), db, now)
	require.NoError(t, err)
	require.Empty(t, blockers)
	_, blockers, err = ProductionZTAPIReferralReadiness(t.Context(), db, now.Add(time.Minute))
	require.NoError(t, err)
	require.Contains(t, blockers, "catalog_prices_or_routes_changed_since_cost_review")
	t.Setenv("ZTAPI_SOURCE_COMMIT", "source-before")
	_, blockers, err = ProductionZTAPIReferralReadiness(t.Context(), db, now.Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", 11).Update("base_url", "https://changed.invalid").Error)
	_, blockers, err = ProductionZTAPIReferralReadiness(t.Context(), db, now)
	require.NoError(t, err)
	require.Contains(t, blockers, "catalog_prices_or_routes_changed_since_cost_review")
}

func TestZTAPIReferralRollingCatalogChoosesMatchingCostGate(t *testing.T) {
	db := referralReadinessCatalog(t)
	now := time.Now().UTC()
	t.Setenv("ZTAPI_SOURCE_COMMIT", "xcloud-before")
	before, keys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	t.Setenv("ZTAPI_SOURCE_COMMIT", "xcloud-after")
	after, _, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	gate := ZTAPIReferralPriceGateInput{RequiredKeys: keys, CatalogEvidence: "before"}
	next := ZTAPIReferralPriceGateInput{RequiredKeys: keys, CatalogEvidence: "after"}
	proof := ZTAPIReferralCostAttestation{Version: 1, CatalogSHA256: before, TransitionCatalogSHA256: after, TransitionExpiresAt: now.Add(time.Minute).Unix(),
		VerifiedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix(), PriceGate: gate, TransitionPriceGate: &next}
	file := filepath.Join(t.TempDir(), "cost.json")
	t.Setenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE", file)
	write := func() {
		raw, err := common.Marshal(proof)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(file, raw, 0600))
	}
	write()
	input, blockers, err := ProductionZTAPIReferralReadiness(t.Context(), db, now)
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.Equal(t, "after", input.PriceGate.CatalogEvidence)
	next.RequiredKeys = []string{"wrong-catalog"}
	write()
	_, blockers, err = ProductionZTAPIReferralReadiness(t.Context(), db, now)
	require.NoError(t, err)
	require.Contains(t, blockers, "catalog_prices_or_routes_changed_since_cost_review")
	t.Setenv("ZTAPI_SOURCE_COMMIT", "xcloud-before")
	input, blockers, err = ProductionZTAPIReferralReadiness(t.Context(), db, now)
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.Equal(t, "before", input.PriceGate.CatalogEvidence)
}
