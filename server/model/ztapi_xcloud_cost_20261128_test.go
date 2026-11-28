package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type xcloudCostFixture20261128 struct {
	f     ztapiPublicationGateFixture
	stage *ZTAPIXcloudStaged20261128
	req   ZTAPIXcloudPublish20261128
	proof ZTAPIReferralCostAttestation
	file  string
	usage map[string]ZTAPIXcloudUsageEnvelope20261128
}

func writeXcloudCostProof20261128(t *testing.T, file string, proof ZTAPIReferralCostAttestation) {
	t.Helper()
	body, err := common.Marshal(proof)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, body, 0600))
}

func xcloudCostUsage20261128(t *testing.T, source ZTAPIModelPriceSource, channel int, verification int64) map[string]ZTAPIXcloudUsageEnvelope20261128 {
	t.Helper()
	quotes, _, _, err := ztapiXcloudSourceQuotes20261128(source.SourceModel, source.SpendTier)
	require.NoError(t, err)
	items := map[string]ZTAPIXcloudUsageEnvelope20261128{}
	for i, quote := range quotes {
		for dimension := range quote.CashRates {
			key := fmt.Sprintf("%s/%s/channel:%d/tier:%d/%s", source.SourceModel, ZTAPIProtocolOpenAICompatible, channel, i+1, dimension)
			items[key] = ZTAPIXcloudUsageEnvelope20261128{VerificationID: verification,
				ReceiptReference: "offline-fixture/managed-receipt.json#request-123", UsageUpperBound: "1",
				PlatformFee: "0", Evidence: "synthetic reviewed supplier/customer usage ratio and fee waiver"}
		}
	}
	return items
}

func setupXcloudCost20261128(t *testing.T, model, group string, channel int) xcloudCostFixture20261128 {
	t.Helper()
	f := xcloudStageFixture20261128(t)
	require.NoError(t, f.db.AutoMigrate(&Option{}, &ZTAPIHealthState{}))
	baseline, err := createZTAPIModelPublicationSnapshotTx(f.db, &f.config, f.config.Version,
		ztapiPublicationEvidence{PriceSourceID: f.price.ID, AllowedChannelIDs: []int{f.channel.Id}, IdentityUpdatedAt: f.identity.UpdatedAt})
	require.NoError(t, err)
	require.NoError(t, f.db.Model(&ZTAPIModelConfig{}).Where("id = ?", f.config.ID).
		Updates(map[string]any{"published": true, "publication_snapshot_id": baseline.ID}).Error)
	if channel != 7 {
		url := "https://www.xcloudtoken.com"
		c := f.channel
		c.Id, c.BaseURL, c.Models = channel, &url, "unrelated-model"
		require.NoError(t, f.db.Create(&c).Error)
	}
	stage, err := StageZTAPIXcloud20261128(9, ZTAPIXcloudStage20261128{SourceModel: model, Group: group, ChannelID: channel}, []string{model}, time.Now())
	require.NoError(t, err)
	v := f.verification
	v.ID, v.ModelConfigID, v.ChannelID, v.VerifiedAt = 0, stage.Config.ID, channel, common.GetTimestamp()
	require.NoError(t, f.db.Create(&v).Error)
	fingerprint, keys, err := ZTAPIReferralCatalogIdentity(t.Context(), f.db)
	require.NoError(t, err)
	gate := ZTAPIReferralPriceGateInput{CatalogComplete: true, CatalogEvidence: "isolated existing catalog", RequiredKeys: keys}
	for _, key := range keys {
		item := referralGate().Items[0]
		item.Key = key
		gate.Items = append(gate.Items, item)
	}
	now := time.Now().Unix()
	proof := ZTAPIReferralCostAttestation{Version: 1, CatalogSHA256: fingerprint, VerifiedAt: now - 10, ExpiresAt: now + 3600,
		FinanceCapability: "referral-v1", PlatformFeeKnown: true, PlatformFeeEvidence: "isolated fee evidence", PriceGate: gate}
	file := filepath.Join(t.TempDir(), "protected-cost.json")
	t.Setenv("ZTAPI_REFERRAL_COST_ATTESTATION_FILE", file)
	writeXcloudCostProof20261128(t, file, proof)
	t.Setenv("ZTAPI_REFERRAL_WORKER_ENABLED", "true")
	oldFinance, oldWorker := ZTAPIReferralFinanceCapabilityCheck, ZTAPIReferralWorkerCapabilityCheck
	t.Cleanup(func() {
		ZTAPIReferralFinanceCapabilityCheck, ZTAPIReferralWorkerCapabilityCheck = oldFinance, oldWorker
	})
	ZTAPIReferralFinanceCapabilityCheck = func(context.Context) bool { return true }
	ZTAPIReferralWorkerCapabilityCheck = func(time.Time) bool { return true }
	return xcloudCostFixture20261128{f: f, stage: stage, file: file, proof: proof,
		req:   ZTAPIXcloudPublish20261128{ConfigID: stage.Config.ID, Version: stage.Config.Version, SourceID: stage.Source.ID, VerificationID: v.ID},
		usage: xcloudCostUsage20261128(t, stage.Source, channel, v.ID)}
}

func (f xcloudCostFixture20261128) install(t *testing.T) func(ZTAPIReferralCostAttestation, ZTAPIReferralCostAttestation) error {
	t.Helper()
	return func(current, transition ZTAPIReferralCostAttestation) error {
		actual, err := readZTAPIReferralCostAttestation(f.file, time.Now())
		require.NoError(t, err)
		require.Equal(t, current, actual)
		body, err := common.Marshal(transition)
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(f.file), "transition-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if _, err := file.Write(body); err != nil {
			file.Close()
			return err
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		return os.Rename(file.Name(), f.file)
	}
}

type xcloudCostCommitFailurePool20261128 struct {
	gorm.ConnPool
	armed *bool
	err   error
}

func (p xcloudCostCommitFailurePool20261128) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.ConnPool.(gorm.TxBeginner).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &xcloudCostCommitFailureTx20261128{Tx: tx, armed: p.armed, err: p.err}, nil
}

type xcloudCostCommitFailureTx20261128 struct {
	*sql.Tx
	armed *bool
	err   error
}

func (tx *xcloudCostCommitFailureTx20261128) Commit() error {
	if *tx.armed {
		return tx.err
	}
	return tx.Tx.Commit()
}

func TestZTAPIXcloudCost20261128CommitFailureRetainsOldProof(t *testing.T) {
	f := setupXcloudCost20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
	pool := f.f.db.Statement.ConnPool
	armed := false
	denied := errors.New("injected SQL commit failure")
	f.f.db.Statement.ConnPool = xcloudCostCommitFailurePool20261128{ConnPool: pool, armed: &armed, err: denied}
	defer func() { f.f.db.Statement.ConnPool = pool }()
	install := f.install(t)
	_, err := ApplyZTAPIXcloudPublicationWithCost20261128(t.Context(), 9, f.req, f.usage, func(old, next ZTAPIReferralCostAttestation) error {
		if err := install(old, next); err != nil {
			return err
		}
		armed = true
		return nil
	})
	require.ErrorIs(t, err, denied)
	f.unchanged(t)
	proof, err := readZTAPIReferralCostAttestation(f.file, time.Now())
	require.NoError(t, err)
	require.Equal(t, f.proof.CatalogSHA256, proof.CatalogSHA256)
	require.Equal(t, f.proof.PriceGate, proof.PriceGate)
	require.NotNil(t, proof.TransitionPriceGate, "installed file cannot be rolled back by SQL")
	live, blockers, err := ProductionZTAPIReferralReadiness(t.Context(), f.f.db, time.Now())
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.Equal(t, f.proof.PriceGate, live.PriceGate)
}

func (f xcloudCostFixture20261128) unchanged(t *testing.T) {
	t.Helper()
	var config ZTAPIModelConfig
	require.NoError(t, f.f.db.First(&config, f.req.ConfigID).Error)
	require.Equal(t, f.stage.Config, config)
	var count int64
	require.NoError(t, f.f.db.Model(&ZTAPIModelPublicationSnapshot{}).Where("model_config_id = ?", f.req.ConfigID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, f.f.db.Model(&Ability{}).Where("model = ?", config.SourceModel).Count(&count).Error)
	require.Zero(t, count, "a failed operator check must roll back routes too")
}

func TestZTAPIXcloudCost20261128OperatorAppliesExactTierCosts(t *testing.T) {
	f := setupXcloudCost20261128(t, "gpt-6-astra", "gpt-plus", 7)
	for key, envelope := range f.usage {
		envelope.UsageUpperBound, envelope.PlatformFee = "1.01", "0.000001"
		f.usage[key] = envelope
	}
	result, err := ApplyZTAPIXcloudPublicationWithCost20261128(t.Context(), 9, f.req, f.usage, f.install(t))
	require.NoError(t, err)
	require.Equal(t, []int{7}, result.ChannelIDs())
	proof, err := readZTAPIReferralCostAttestation(f.file, time.Now())
	require.NoError(t, err)
	require.Equal(t, f.proof.PriceGate, proof.PriceGate, "old proof remains valid until SQL commit")
	require.NotNil(t, proof.TransitionPriceGate)
	require.LessOrEqual(t, proof.TransitionExpiresAt, time.Now().Add(10*time.Minute).Unix())
	byKey := map[string]ZTAPIReferralPriceGateItem{}
	for _, item := range proof.TransitionPriceGate.Items {
		byKey[item.Key] = item
	}
	for _, item := range f.proof.PriceGate.Items {
		require.Equal(t, item, byKey[item.Key])
	}
	quotes, _, _, err := ztapiXcloudSourceQuotes20261128(f.stage.Source.SourceModel, f.stage.Source.SpendTier)
	require.NoError(t, err)
	var rules []ztapiABFrozenSaleRule
	require.NoError(t, common.UnmarshalJsonStr(result.TokenPriceRulesJSON, &rules))
	for i, quote := range quotes {
		for dim, cash := range quote.CashRates {
			key := fmt.Sprintf("%s/%s/channel:7/tier:%d/%s", result.SourceModel, result.Protocol, i+1, dim)
			item, ok := byKey[key]
			require.True(t, ok)
			require.True(t, decimal.RequireFromString(item.SupplierCostUpperBound).Equal(cash), key)
			require.Equal(t, rules[i].Sale[dim], item.CustomerRevenue)
			require.Equal(t, "1", item.GroupMultiplier, "cash quote already includes supplier group/recharge factors")
			require.Equal(t, "1", item.CashMultiplier)
			require.Equal(t, "1.01", item.UsageUpperBound, "operator evidence must not be replaced with a guessed one")
			require.Equal(t, "0.000001", item.PlatformFee)
			require.Contains(t, item.Evidence, f.usage[key].ReceiptReference)
			require.Contains(t, item.Evidence, fmt.Sprintf("verification:%d", f.req.VerificationID))
		}
	}
	input, blockers, err := ProductionZTAPIReferralReadiness(t.Context(), f.f.db, time.Now())
	require.NoError(t, err)
	require.Empty(t, blockers)
	require.Equal(t, *proof.TransitionPriceGate, input.PriceGate)
	verdict, err := EvaluateZTAPIReferralPriceGate(input.PriceGate)
	require.NoError(t, err)
	require.True(t, verdict.Allowed, verdict.BlockedReasons)
}

func TestZTAPIXcloudCost20261128ReplacesOnlyTargetRouteItems(t *testing.T) {
	f := setupXcloudCost20261128(t, "gpt-6-astra", "gpt-plus", 7)
	first, err := ApplyZTAPIXcloudPublicationWithCost20261128(t.Context(), 9, f.req, f.usage, f.install(t))
	require.NoError(t, err)
	installed, err := readZTAPIReferralCostAttestation(f.file, time.Now())
	require.NoError(t, err)
	// A separate test-only operator finalization establishes the next baseline.
	baseline := installed
	baseline.CatalogSHA256, baseline.PriceGate = installed.TransitionCatalogSHA256, *installed.TransitionPriceGate
	baseline.TransitionCatalogSHA256, baseline.TransitionPriceGate, baseline.TransitionExpiresAt = "", nil, 0
	writeXcloudCostProof20261128(t, f.file, baseline)
	url := "https://www.xcloudtoken.com"
	c := f.f.channel
	c.Id, c.BaseURL, c.Models = 6, &url, "unrelated-model"
	require.NoError(t, f.f.db.Create(&c).Error)
	stage, err := StageZTAPIXcloud20261128(9, ZTAPIXcloudStage20261128{SourceModel: first.SourceModel, Group: "new-vip", ChannelID: 6,
		ConfigID: first.ModelConfigID, Version: first.ModelVersion, SnapshotID: first.ID, SourceID: first.PriceSourceID}, []string{first.SourceModel}, time.Now())
	require.NoError(t, err)
	hash, _, err := ZTAPIReferralCatalogIdentity(t.Context(), f.f.db)
	require.NoError(t, err)
	require.Equal(t, baseline.CatalogSHA256, hash, "private stage must leave the protected baseline usable")
	v := f.f.verification
	v.ID, v.ModelConfigID, v.ChannelID, v.VerifiedAt = 0, first.ModelConfigID, 6, common.GetTimestamp()
	require.NoError(t, f.f.db.Create(&v).Error)
	req := ZTAPIXcloudPublish20261128{ConfigID: first.ModelConfigID, Version: first.ModelVersion, SnapshotID: first.ID, SourceID: stage.Source.ID, VerificationID: v.ID}
	usage := xcloudCostUsage20261128(t, stage.Source, 6, v.ID)
	second, err := ApplyZTAPIXcloudPublicationWithCost20261128(t.Context(), 9, req, usage, f.install(t))
	require.NoError(t, err)
	require.Equal(t, []int{6}, second.ChannelIDs())
	next, err := readZTAPIReferralCostAttestation(f.file, time.Now())
	require.NoError(t, err)
	require.Equal(t, baseline.PriceGate, next.PriceGate)
	items := map[string]ZTAPIReferralPriceGateItem{}
	for _, item := range next.TransitionPriceGate.Items {
		items[item.Key] = item
	}
	kept := 0
	for _, item := range baseline.PriceGate.Items {
		if strings.HasPrefix(item.Key, "gpt-6-astra/openai_compatible/channel:7/tier:") {
			_, exists := items[item.Key]
			require.False(t, exists, "retired target route cannot remain in the candidate gate")
		} else {
			require.Equal(t, item, items[item.Key], "unrelated costs, fees and evidence must remain byte-for-byte equal")
			kept++
		}
	}
	require.Equal(t, kept+len(usage), len(items))
	for key := range usage {
		require.Contains(t, items, key)
	}
}

func TestZTAPIXcloudCost20261128InstallFailuresAndNoFakeProof(t *testing.T) {
	for _, mode := range []string{"error", "error_after_install", "noop", "different_proof", "mutated_transition", "finance_not_ready", "worker_not_ready", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			f := setupXcloudCost20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
			denied := errors.New("operator install denied")
			install := f.install(t)
			switch mode {
			case "error":
				install = func(ZTAPIReferralCostAttestation, ZTAPIReferralCostAttestation) error { return denied }
			case "error_after_install":
				install = func(_, next ZTAPIReferralCostAttestation) error {
					writeXcloudCostProof20261128(t, f.file, next)
					return denied
				}
			case "noop":
				install = func(ZTAPIReferralCostAttestation, ZTAPIReferralCostAttestation) error { return nil }
			case "different_proof":
				install = func(_, next ZTAPIReferralCostAttestation) error {
					next.PlatformFeeEvidence = "unreviewed replacement"
					writeXcloudCostProof20261128(t, f.file, next)
					return nil
				}
			case "mutated_transition":
				install = func(_, next ZTAPIReferralCostAttestation) error {
					next.TransitionPriceGate.Items[0].Evidence = "callback-invented evidence"
					writeXcloudCostProof20261128(t, f.file, next)
					return nil
				}
			case "finance_not_ready":
				ZTAPIReferralFinanceCapabilityCheck = func(context.Context) bool { return false }
			case "worker_not_ready":
				ZTAPIReferralWorkerCapabilityCheck = func(time.Time) bool { return false }
			case "canceled":
				install = func(current, next ZTAPIReferralCostAttestation) error { return denied }
			}
			ctx := t.Context()
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := ApplyZTAPIXcloudPublicationWithCost20261128(ctx, 9, f.req, f.usage, install)
			require.Error(t, err)
			require.Nil(t, result)
			if mode == "mutated_transition" {
				require.ErrorContains(t, err, "readback failed", "callback mutation cannot rewrite the expected proof")
			}
			if mode == "error" {
				require.ErrorIs(t, err, denied)
			}
			f.unchanged(t)
			live, blockers, readErr := ProductionZTAPIReferralReadiness(t.Context(), f.f.db, time.Now())
			require.NoError(t, readErr)
			require.Empty(t, blockers, "installed dual proof must still match the rolled-back catalog")
			require.Equal(t, f.proof.PriceGate, live.PriceGate)
			proof, readErr := readZTAPIReferralCostAttestation(f.file, time.Now())
			require.NoError(t, readErr)
			require.Equal(t, f.proof.CatalogSHA256, proof.CatalogSHA256)
			require.Equal(t, f.proof.VerifiedAt, proof.VerifiedAt)
			require.Equal(t, f.proof.ExpiresAt, proof.ExpiresAt)
		})
	}
}

func TestZTAPIXcloudCost20261128RejectsUnknownUsageAndUnboundReceipts(t *testing.T) {
	for _, mode := range []string{"missing_dimension", "extra_dimension", "usage_empty", "usage_zero", "usage_exponent", "fee_empty", "fee_high", "evidence_empty", "receipt_empty", "wrong_verification"} {
		t.Run(mode, func(t *testing.T) {
			f := setupXcloudCost20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
			key := "gpt-6.2-sol/openai_compatible/channel:7/tier:1/input_tokens"
			item := f.usage[key]
			switch mode {
			case "missing_dimension":
				delete(f.usage, key)
			case "extra_dimension":
				f.usage["other-source/input_tokens"] = item
			case "usage_empty":
				item.UsageUpperBound = ""
			case "usage_zero":
				item.UsageUpperBound = "0"
			case "usage_exponent":
				item.UsageUpperBound = "1e0"
			case "fee_empty":
				item.PlatformFee = ""
			case "fee_high":
				item.PlatformFee = "1000"
			case "evidence_empty":
				item.Evidence = " "
			case "receipt_empty":
				item.ReceiptReference = " "
			case "wrong_verification":
				item.VerificationID++
			}
			if mode != "missing_dimension" {
				f.usage[key] = item
			}
			called := false
			_, err := ApplyZTAPIXcloudPublicationWithCost20261128(t.Context(), 9, f.req, f.usage,
				func(ZTAPIReferralCostAttestation, ZTAPIReferralCostAttestation) error { called = true; return nil })
			require.Error(t, err)
			require.False(t, called, "unproven usage cannot reach the installer")
			f.unchanged(t)
		})
	}
}

func TestZTAPIXcloudCost20261128RejectsMismatchedOrChangedOldProof(t *testing.T) {
	for _, mode := range []string{"catalog", "required", "expired", "existing_transition", "CAS"} {
		t.Run(mode, func(t *testing.T) {
			f := setupXcloudCost20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
			bad := f.proof
			switch mode {
			case "catalog":
				bad.CatalogSHA256 = strings.Repeat("f", 64)
			case "required":
				bad.PriceGate.RequiredKeys = []string{"wrong-model/input_tokens"}
			case "expired":
				bad.ExpiresAt = time.Now().Unix() - 1
			case "existing_transition":
				bad.TransitionCatalogSHA256 = strings.Repeat("f", 64)
			case "CAS":
				changed := false
				require.NoError(t, f.f.db.Callback().Create().After("gorm:create").Register("test:cost-proof-CAS", func(tx *gorm.DB) {
					if !changed && tx.Statement.Table == (ZTAPIModelPublicationSnapshot{}).TableName() {
						changed = true
						next := f.proof
						next.PlatformFeeEvidence = "concurrent operator review"
						writeXcloudCostProof20261128(t, f.file, next)
					}
				}))
			}
			if mode != "CAS" {
				writeXcloudCostProof20261128(t, f.file, bad)
			}
			called := false
			_, err := ApplyZTAPIXcloudPublicationWithCost20261128(t.Context(), 9, f.req, f.usage,
				func(ZTAPIReferralCostAttestation, ZTAPIReferralCostAttestation) error { called = true; return nil })
			require.Error(t, err)
			require.False(t, called)
			f.unchanged(t)
		})
	}
}

func TestZTAPIXcloudCost20261128KimiZeroPriceBlocker(t *testing.T) {
	f := setupXcloudCost20261128(t, "kimi-k3", "\u56fd\u4ea7\u5206\u7ec4", 8)
	_, err := ApplyZTAPIXcloudPublicationWithCost20261128(t.Context(), 9, f.req, f.usage, f.install(t))
	require.ErrorContains(t, err, "xcloud_zero_retail_dimension_unsupported")
	require.ErrorContains(t, err, "kimi-k3/openai_compatible/channel:8/tier:1/cache_write")
	f.unchanged(t)
}

func TestZTAPIXcloudCost20261128BaselineChangedBeforeMutation(t *testing.T) {
	for _, mode := range []string{"non_target_param_override", "protected_proof"} {
		t.Run(mode, func(t *testing.T) {
			f := setupXcloudCost20261128(t, "gpt-6.2-sol", "gpt-plus", 7)
			injected, changingBaseline, installCalled := false, false, false
			snapshotCreates, routeUpdates := 0, 0
			require.NoError(t, f.f.db.Callback().Query().After("gorm:query").Register("test:xcloud-baseline-window", func(tx *gorm.DB) {
				config, ok := tx.Statement.Dest.(*ZTAPIModelConfig)
				if injected || !ok || config.ID != f.req.ConfigID {
					return
				}
				// The initial complete catalog hash and protected-file read have
				// finished; Apply has not acquired its catalog transaction yet.
				injected, changingBaseline = true, true
				if mode == "non_target_param_override" {
					require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Model(&Channel{}).Where("id = ?", f.f.channel.Id).
						Update("param_override", `{"extra_supplier_context":2048}`).Error)
				} else {
					changed := f.proof
					changed.PlatformFeeEvidence = "another operator's reviewed proof"
					writeXcloudCostProof20261128(t, f.file, changed)
				}
				changingBaseline = false
			}))
			require.NoError(t, f.f.db.Callback().Create().Before("gorm:create").Register("test:xcloud-no-candidate-create", func(tx *gorm.DB) {
				if tx.Statement.Table == (ZTAPIModelPublicationSnapshot{}).TableName() {
					snapshotCreates++
				}
			}))
			require.NoError(t, f.f.db.Callback().Update().Before("gorm:update").Register("test:xcloud-no-route-update", func(tx *gorm.DB) {
				if tx.Statement.Table == "channels" && !changingBaseline {
					routeUpdates++
				}
			}))
			install := f.install(t)
			result, err := ApplyZTAPIXcloudPublicationWithCost20261128(t.Context(), 9, f.req, f.usage, func(old, next ZTAPIReferralCostAttestation) error {
				installCalled = true
				return install(old, next)
			})
			require.True(t, injected, "test must exercise the pre-lock race window")
			require.ErrorContains(t, err, "xcloud_cost_baseline_changed")
			require.Nil(t, result)
			require.False(t, installCalled)
			require.Zero(t, snapshotCreates, "before must fail prior to candidate snapshot creation, not just roll it back")
			require.Zero(t, routeUpdates, "before must fail prior to channel route mutation")
			var channel Channel
			require.NoError(t, f.f.db.Select("id", "models").First(&channel, 7).Error)
			require.Equal(t, "gpt-6-sol", channel.Models)
			f.unchanged(t)
			if mode == "non_target_param_override" {
				channel = Channel{}
				require.NoError(t, f.f.db.Select("id", "param_override").First(&channel, f.f.channel.Id).Error)
				require.NotNil(t, channel.ParamOverride)
				require.Equal(t, `{"extra_supplier_context":2048}`, *channel.ParamOverride, "concurrent non-target change must not be reverted")
			}
		})
	}
}
