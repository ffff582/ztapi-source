package model

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIFableXcloudOnlyKeepsRetailAndHistory(t *testing.T) {
	db := reductionFixture20261107(t)
	var config ZTAPIModelConfig
	require.NoError(t, db.Where("source_model = ?", "claude-fable-5").First(&config).Error)
	selection := ApprovedZTAPIPriceReductionSelections20261107()
	for _, item := range selection {
		if item.ModelID == config.ID {
			_, err := ApplyZTAPIPriceReduction20261107(7, []ZTAPIPriceReductionSelection20261107{item}, false)
			require.NoError(t, err)
		}
	}
	require.NoError(t, db.First(&config, config.ID).Error)
	var before ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&before, config.PublicationSnapshotID).Error)
	old, err := common.Marshal(before)
	require.NoError(t, err)
	result, err := PublishZTAPIFableXcloudOnly(7, config.Version)
	require.NoError(t, err)
	require.Equal(t, []int{4}, result.ChannelIDs())
	require.Equal(t, before.InputPricePerMillion, result.InputPricePerMillion)
	require.Equal(t, before.OutputPricePerMillion, result.OutputPricePerMillion)
	require.Equal(t, before.PriceSourceID, result.PriceSourceID)
	require.Equal(t, before.TokenPriceRulesJSON, result.TokenPriceRulesJSON)
	require.Equal(t, before.CacheWritePricingMode, result.CacheWritePricingMode)
	var historical ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&historical, before.ID).Error)
	retained, err := common.Marshal(historical)
	require.NoError(t, err)
	require.Equal(t, string(old), string(retained))
	_, err = PublishZTAPIFableXcloudOnly(7, config.Version)
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	again, err := PublishZTAPIFableXcloudOnly(7, result.ModelVersion)
	require.NoError(t, err)
	require.Equal(t, result.ID, again.ID)
}

func TestZTAPIFableRestoreBackupRequiresExactCurrentEvidenceAndPreservesPrice(t *testing.T) {
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", "fable-isolated-test-master-key-0123456789")
	db := reductionFixture20261107(t)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ZTAPIDiscoverySnapshot{}, &ZTAPIDiscoveredModel{}, &ZTAPIModelVerification{}, &ZTAPIModelIdentity{}))
	var config ZTAPIModelConfig
	require.NoError(t, db.Where("source_model = ?", "claude-fable-5").First(&config).Error)
	for _, item := range ApprovedZTAPIPriceReductionSelections20261107() {
		if item.ModelID == config.ID {
			_, err := ApplyZTAPIPriceReduction20261107(7, []ZTAPIPriceReductionSelection20261107{item}, false)
			require.NoError(t, err)
		}
	}
	require.NoError(t, db.First(&config, config.ID).Error)
	before, err := PublishZTAPIFableXcloudOnly(7, config.Version)
	require.NoError(t, err)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&ZTAPIModelIdentity{ModelConfigID: config.ID, PublicName: config.PublicNameValue(), Protocol: config.Protocol, ProviderFamily: config.ProviderFamily, SourceReference: "fixture", UpdatedAt: now}).Error)
	verify := func(channel int, at int64, passed bool) int64 {
		var route Channel
		require.NoError(t, db.First(&route, channel).Error)
		physical := config.SourceModel
		if channel == 2 {
			physical = "claude-fable-5-hc"
		}
		routeHash, err := ZTAPIFableVerificationRouteFingerprint(&route, physical)
		require.NoError(t, err)
		row := ZTAPIModelVerification{ModelConfigID: config.ID, ChannelID: channel, Protocol: config.Protocol,
			FableRouteFingerprint: routeHash,
			NonStreamingPassed:    passed, StreamingRequired: true, StreamingPassed: passed, UsageReconciled: passed,
			InvalidKeyClassified: true, InsufficientBalanceClassified: true, RateLimitClassified: true, TimeoutClassified: true, VerifiedAt: at, OperatorID: 7}
		require.NoError(t, db.Create(&row).Error)
		return row.ID
	}
	for _, route := range []struct {
		id                     int
		url, mapping, physical string
		priority               int64
	}{
		{4, "https://www.xcloudtoken.com", "", "claude-fable-5", 30},
		{2, "https://ai.yunxinapi.com/hub", `{"claude-fable-5":"claude-fable-5-hc"}`, "claude-fable-5-hc", 20},
	} {
		channel := Channel{Id: route.id, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled,
			ZTAPIManaged: true, ZTAPIFamily: ZTAPIModelFamilyOpenAI, Models: config.SourceModel, Group: "default", BaseURL: &route.url,
			ModelMapping: &route.mapping, Priority: &route.priority, Key: "synthetic-test-credential"}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, channel.AddAbilities(db))
		discovery := ZTAPIDiscoverySnapshot{ChannelID: route.id, ModelListHash: "fixture", ModelCount: 1, FetchedAt: now - 1}
		require.NoError(t, db.Create(&discovery).Error)
		require.NoError(t, db.Create(&ZTAPIDiscoveredModel{SnapshotID: discovery.ID, ChannelID: route.id, SourceModel: route.physical}).Error)
	}
	primary := verify(4, now-10, true)
	stale := verify(2, now-86401, true)
	check := func(*gorm.DB) error { return nil }
	_, err = RestoreZTAPIFableYunxinBackup(7, before.ModelVersion, before.ID, primary, stale, check)
	require.Error(t, err)
	backup := verify(2, now-5, true)
	latestFailure := verify(2, now-5, false)
	require.Greater(t, latestFailure, backup)
	_, err = RestoreZTAPIFableYunxinBackup(7, before.ModelVersion, before.ID, primary, backup, check)
	require.Error(t, err)
	backup = verify(2, now-4, true)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", 2).Update("priority", 30).Error)
	_, err = RestoreZTAPIFableYunxinBackup(7, before.ModelVersion, before.ID, primary, backup, check)
	require.Error(t, err)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", 2).Update("priority", 20).Error)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", 2).Update("model_mapping", `{"claude-fable-5":"claude-fable-5-1"}`).Error)
	_, err = RestoreZTAPIFableYunxinBackup(7, before.ModelVersion, before.ID, primary, backup, check)
	require.Error(t, err)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", 2).Update("model_mapping", `{"claude-fable-5":"claude-fable-5-hc"}`).Error)
	require.NoError(t, db.Model(&ZTAPIModelVerification{}).Where("id = ?", backup).Update("fable_route_fingerprint", "").Error)
	_, err = RestoreZTAPIFableYunxinBackup(7, before.ModelVersion, before.ID, primary, backup, check)
	require.Error(t, err)
	backup = verify(2, now-3, true)
	blocked := errors.New("candidate cost proof rejected")
	_, err = RestoreZTAPIFableYunxinBackup(7, before.ModelVersion, before.ID, primary, backup, func(*gorm.DB) error { return blocked })
	require.ErrorIs(t, err, blocked)
	require.NoError(t, db.First(&config, config.ID).Error)
	require.Equal(t, before.ID, config.PublicationSnapshotID)
	require.NoError(t, db.AutoMigrate(&Option{}))
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id <> ?", config.ID).Update("published", false).Error)
	oldHash, oldKeys, err := ZTAPIReferralCatalogIdentity(t.Context(), db)
	require.NoError(t, err)
	oldGate := ZTAPIReferralPriceGateInput{CatalogComplete: true, CatalogEvidence: "isolated fixture", RequiredKeys: oldKeys}
	for _, key := range oldKeys {
		oldGate.Items = append(oldGate.Items, ZTAPIReferralPriceGateItem{Key: key,
			CustomerRevenue: "100", SupplierCostUpperBound: "1", UsageUpperBound: "1", BonusRate: "0.05", GroupMultiplier: "1",
			CashMultiplier: "1", PlatformFee: "0", Evidence: "fixture reviewed", ModelMappingKnown: true, CostKnown: true, UsageKnown: true, PlatformFeeKnown: true})
	}
	proof := ZTAPIReferralCostAttestation{Version: 1, CatalogSHA256: oldHash, PriceGate: oldGate,
		PlatformFeeKnown: true, PlatformFeeEvidence: "fixture fees", FinanceCapability: "referral-v1",
		VerifiedAt: now - 10, ExpiresAt: now + 600}
	file := filepath.Join(t.TempDir(), "protected-cost.json")
	t.Setenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE", file)
	raw, err := common.Marshal(proof)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, raw, 0600))
	live, blockers, err := ProductionZTAPIReferralReadiness(t.Context(), db, time.Unix(now, 0))
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.Equal(t, oldGate, live.PriceGate)
	var transition ZTAPIReferralCostAttestation
	check = func(tx *gorm.DB) error {
		after, required, err := ZTAPIReferralCatalogIdentity(t.Context(), tx)
		require.NoError(t, err)
		require.NotEqual(t, oldHash, after)
		var active ZTAPIModelConfig
		require.NoError(t, tx.First(&active, config.ID).Error)
		var snapshot ZTAPIModelPublicationSnapshot
		require.NoError(t, tx.First(&snapshot, active.PublicationSnapshotID).Error)
		items := map[string]ZTAPIFableBackupUsageEnvelope{}
		for _, key := range required {
			if strings.HasPrefix(key, "claude-fable-5/openai_compatible/channel:2/tier:1/") {
				items[key] = ZTAPIFableBackupUsageEnvelope{UsageUpperBound: "1", PlatformFee: "0", Evidence: "isolated receipt and fee fixture"}
			}
		}
		require.Len(t, items, 5)
		_, oldBlockers, err := ProductionZTAPIReferralReadiness(t.Context(), tx, time.Unix(now, 0))
		require.NoError(t, err)
		require.Contains(t, oldBlockers, "catalog_prices_or_routes_changed_since_cost_review")
		withoutHour := "claude-fable-5/openai_compatible/channel:2/tier:1/cache_write_1h"
		hour := items[withoutHour]
		delete(items, withoutHour)
		_, err = BuildZTAPIFableBackupCostTransition(proof, after, required, &snapshot, items, time.Unix(now, 0))
		require.Error(t, err)
		items[withoutHour] = hour
		underpriced := items[withoutHour]
		underpriced.PlatformFee = "1"
		items[withoutHour] = underpriced
		_, err = BuildZTAPIFableBackupCostTransition(proof, after, required, &snapshot, items, time.Unix(now, 0))
		require.Error(t, err)
		items[withoutHour] = hour
		transition, err = BuildZTAPIFableBackupCostTransition(proof, after, required, &snapshot, items, time.Unix(now, 0))
		require.NoError(t, err)
		require.Equal(t, oldGate, transition.PriceGate)
		require.Len(t, transition.TransitionPriceGate.Items, len(oldGate.Items)+5)
		for _, item := range transition.TransitionPriceGate.Items {
			if item.Key == withoutHour {
				require.Equal(t, "16.2618000000", item.CustomerRevenue)
				require.True(t, decimal.RequireFromString(item.SupplierCostUpperBound).GreaterThan(decimal.RequireFromString("13.16431372549")))
			}
		}
		raw, err := common.Marshal(transition)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(file, raw, 0600))
		ready, blockers, err := ProductionZTAPIReferralReadiness(t.Context(), tx, time.Unix(now, 0))
		require.NoError(t, err)
		require.Empty(t, blockers)
		report, err := EvaluateZTAPIReferralPriceGate(ready.PriceGate)
		require.NoError(t, err)
		require.True(t, report.Allowed)
		return nil
	}
	envelopes := map[string]ZTAPIFableBackupUsageEnvelope{}
	for _, dim := range []string{"input_tokens", "output_tokens", "cache_read", "cache_write_5m", "cache_write_1h"} {
		envelopes["claude-fable-5/openai_compatible/channel:2/tier:1/"+dim] = ZTAPIFableBackupUsageEnvelope{UsageUpperBound: "1", PlatformFee: "0", Evidence: "isolated receipt and fee fixture"}
	}
	// Run the earlier proof checks in a rolled-back transaction, then exercise
	// the operator's actual staging and protected-file readback.
	_, err = RestoreZTAPIFableYunxinBackup(7, before.ModelVersion, before.ID, primary, backup, func(tx *gorm.DB) error {
		require.NoError(t, check(tx))
		return blocked
	})
	require.ErrorIs(t, err, blocked)
	require.NoError(t, os.WriteFile(file, raw, 0600))
	input := func(install func(ZTAPIReferralCostAttestation, ZTAPIReferralCostAttestation) error) (*ZTAPIModelPublicationSnapshot, error) {
		return StageZTAPIFableYunxinBackup(t.Context(), 7, before.ModelVersion, before.ID, primary, backup, envelopes, install)
	}
	_, err = input(func(ZTAPIReferralCostAttestation, ZTAPIReferralCostAttestation) error { return nil })
	require.ErrorContains(t, err, "protected transition")
	require.NoError(t, db.First(&config, config.ID).Error)
	require.Equal(t, before.ID, config.PublicationSnapshotID)
	next, err := input(func(current, staged ZTAPIReferralCostAttestation) error {
		require.Equal(t, proof, current)
		transition = staged
		data, err := common.Marshal(staged)
		require.NoError(t, err)
		return os.WriteFile(file, data, 0600)
	})
	require.NoError(t, err)
	require.Equal(t, []int{2, 4}, next.ChannelIDs())
	require.Equal(t, before.PriceSourceID, next.PriceSourceID)
	require.Equal(t, before.TokenPriceRulesJSON, next.TokenPriceRulesJSON)
	require.Equal(t, before.CacheWritePricingMode, next.CacheWritePricingMode)
	require.Equal(t, before.InputPricePerMillion, next.InputPricePerMillion)
	require.Equal(t, before.OutputPricePerMillion, next.OutputPricePerMillion)
	var historical ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&historical, before.ID).Error)
	require.Equal(t, []int{4}, historical.ChannelIDs())
	live, blockers, err = ProductionZTAPIReferralReadiness(t.Context(), db, time.Unix(now, 0))
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.Equal(t, *transition.TransitionPriceGate, live.PriceGate)
}
