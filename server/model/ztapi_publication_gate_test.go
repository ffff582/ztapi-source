package model

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type ztapiPublicationGateFixture struct {
	db           *gorm.DB
	config       ZTAPIModelConfig
	channel      Channel
	snapshot     ZTAPIDiscoverySnapshot
	identity     ZTAPIModelIdentity
	price        ZTAPIModelPriceSource
	verification ZTAPIModelVerification
}

func canonicalGPImagePriceContractForPublicationTest(t *testing.T) string {
	t.Helper()
	return mustCanonicalZTAPIMediaPriceContract(t, gpImage2ContractForTest(t))
}

func setupZTAPIPublicationGateFixture(t *testing.T) ztapiPublicationGateFixture {
	t.Helper()
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", "publication-gate-test-master-key-0123456789")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-publication-gate.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB := DB
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(
		&Channel{}, &Ability{}, &ZTAPIModelConfig{}, &ZTAPIAuditEvent{},
		&ZTAPICatalogLock{}, &ZTAPIDiscoverySnapshot{}, &ZTAPIDiscoveredModel{},
		&ZTAPIModelIdentity{}, &ZTAPIModelPriceSource{}, &ZTAPIModelVerification{},
		&ZTAPIModelPublicationSnapshot{},
	))

	weight := uint(100)
	priority := int64(0)
	channel := Channel{
		Name: "managed-openai-compatible", Type: constant.ChannelTypeOpenAI,
		Key: "synthetic-publication-test-key", Status: common.ChannelStatusEnabled,
		Models: "deepseek-v4-flash", Group: "default",
		ZTAPIManaged: true, Weight: &weight, Priority: &priority,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(db))

	alias := "zt-deepseek-v4-flash"
	config := ZTAPIModelConfig{
		SourceModel: "deepseek-v4-flash", PublicName: &alias,
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderDeepSeek,
		InputCostPerMillion: 1, OutputCostPerMillion: 2,
		InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.1, CacheCreationRatio: 1.25,
		CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
		EnabledGroups: `["default"]`, Version: 1,
	}
	require.NoError(t, db.Create(&config).Error)
	now := time.Now().UTC().Unix()
	snapshot := ZTAPIDiscoverySnapshot{
		ChannelID: channel.Id, ModelListHash: strings.Repeat("a", 64),
		ModelCount: 1, FetchedAt: now,
	}
	require.NoError(t, db.Create(&snapshot).Error)
	require.NoError(t, db.Create(&ZTAPIDiscoveredModel{
		SnapshotID: snapshot.ID, ChannelID: channel.Id, SourceModel: config.SourceModel,
	}).Error)
	identity := ZTAPIModelIdentity{
		ModelConfigID: config.ID, PublicName: alias,
		Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		SourceReference: "supplier-confirmation", VerificationState: "mapped",
		OperatorID: 1, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&identity).Error)
	price := validZTAPIPriceSourceForTest()
	price.ModelConfigID = config.ID
	price.SourceModel = config.SourceModel
	price.SourceDocumentChecksum = ZTAPIQuotationSHA256
	price.Version = 1
	require.NoError(t, db.Create(&price).Error)
	verification := ZTAPIModelVerification{
		ModelConfigID: config.ID, ChannelID: channel.Id,
		Protocol: config.Protocol, NonStreamingPassed: true,
		StreamingRequired: true, StreamingPassed: true, UsageReconciled: true,
		InvalidKeyClassified: true, InsufficientBalanceClassified: true,
		RateLimitClassified: true, TimeoutClassified: true,
		OperatorID: 1, VerifiedAt: now,
	}
	require.NoError(t, db.Create(&verification).Error)
	return ztapiPublicationGateFixture{
		db: db, config: config, channel: channel, snapshot: snapshot,
		identity: identity, price: price, verification: verification,
	}
}

func requireZTAPIPublicationBlocker(t *testing.T, mutate func(*ztapiPublicationGateFixture), blocker string) {
	t.Helper()
	fixture := setupZTAPIPublicationGateFixture(t)
	mutate(&fixture)
	blockers, err := ZTAPIPublicationBlockers(fixture.config.ID)
	require.NoError(t, err)
	require.Contains(t, blockers, blocker)
}

func TestZTAPIPublicationGateAllowsCompleteEvidence(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	blockers, err := ZTAPIPublicationBlockers(fixture.config.ID)
	require.NoError(t, err)
	require.Empty(t, blockers)
}

func TestZTAPIRouteMatchesEvidenceAllowsGeminiNativeImageBridge(t *testing.T) {
	route := ztapiRouteAuthority{
		ChannelType: constant.ChannelTypeGemini,
		Managed:     true,
		Family:      ZTAPIModelFamilyGemini,
	}
	config := &ZTAPIModelConfig{
		SourceModel:    "gemini-2.5-flash-image",
		Protocol:       ZTAPIProtocolOpenAICompatible,
		ProviderFamily: ZTAPIProviderGoogle,
	}
	require.True(t, ztapiRouteMatchesEvidence(route, config))

	config.SourceModel = "gemini-2.5-pro"
	require.False(t, ztapiRouteMatchesEvidence(route, config), "the native bridge is image-only")
	config.SourceModel = "gemini-2.5-flash-image"
	config.ProviderFamily = ZTAPIProviderOpenAI
	require.False(t, ztapiRouteMatchesEvidence(route, config), "the bridge must retain Google provider identity")
}

func TestZTAPIPublicationGateBlocksMissingOrStaleDiscovery(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&f.snapshot).Update("fetched_at", time.Now().Add(-48*time.Hour).Unix()).Error)
	}, ZTAPIPublicationBlockerDiscoveryStale)
}

func TestZTAPIPublicationGateBlocksMissingIdentity(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Delete(&f.identity).Error)
	}, ZTAPIPublicationBlockerIdentityMissing)
}

func TestZTAPIPublicationGateBlocksInvalidProtocolOrProvider(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&f.identity).Update("protocol", "invalid").Error)
	}, ZTAPIPublicationBlockerIdentityInvalid)
}

func TestZTAPIPublicationGateBlocksDisabledAbility(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&Ability{}).Where("channel_id = ?", f.channel.Id).Update("enabled", false).Error)
	}, ZTAPIPublicationBlockerRouteUnavailable)
}

func TestZTAPIPublicationGateBlocksFailedNonStreamingVerification(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&f.verification).Update("non_streaming_passed", false).Error)
	}, ZTAPIPublicationBlockerNonStreaming)
}

func TestZTAPIPublicationGateBlocksMissingRequiredStreamingVerification(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&f.verification).Update("streaming_passed", false).Error)
	}, ZTAPIPublicationBlockerStreaming)
}

func TestZTAPIPublicationGateBlocksUnreconciledUsage(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&f.verification).Update("usage_reconciled", false).Error)
	}, ZTAPIPublicationBlockerUsage)
}

func TestZTAPIPublicationGateBlocksUnsafeErrorClassification(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&f.verification).Update("rate_limit_classified", false).Error)
	}, ZTAPIPublicationBlockerErrorClassification)
}

func TestZTAPIImagePublicationRequiresVerifiedProtocolContractBeforeSnapshotCreation(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	authorizeFixtureSourceAsImage(t, &fixture)
	require.NoError(t, fixture.db.Model(&fixture.price).Update("media_price_contract_json", canonicalGPImagePriceContractForPublicationTest(t)).Error)

	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), ZTAPIPublicationBlockerImageProtocol)
	require.Zero(t, committed.PublicationSnapshotID)

	var snapshots int64
	require.NoError(t, fixture.db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&snapshots).Error)
	require.Zero(t, snapshots)
}

func syntheticVerifiedImageProtocolJSON(t *testing.T, providerModel string) string {
	return syntheticVerifiedImageProtocolJSONWithUsage(t, providerModel, map[string]string{
		"text_input": "text_input", "text_cached_input": "text_cached_input",
		"image_input": "image_input", "image_cached_input": "image_cached_input",
		"image_output": "image_output",
	}, "separate_dimension")
}

func syntheticVerifiedImageProtocolJSONWithUsage(t *testing.T, providerModel string, fields map[string]string, cacheSemantics string) string {
	t.Helper()
	contract := types.ZTAPIImageProtocolContract{
		Version: 1, ProviderModel: providerModel, EndpointType: "images_generation",
		Method: "POST", Path: "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{
			Sizes: []string{"512x512", "1024x1024"}, Qualities: []string{"standard"},
			ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 2,
		},
		Response: types.ZTAPIImageResponseContract{
			Schema: "object_results_array", ResultsField: "data", ResultFields: map[string]string{"url": "url"},
		},
		Usage: types.ZTAPIImageUsageContract{
			UsageField: "usage", Fields: fields,
			TotalField: "total_tokens", TotalSemantics: "sum_of_dimensions", CacheSemantics: cacheSemantics,
		},
		RequestIDField: "request_id", EvidenceVersion: 1,
	}
	for _, size := range contract.Capabilities.Sizes {
		for n := contract.Capabilities.MinCount; n <= contract.Capabilities.MaxCount; n++ {
			maximumDimensions := make(map[string]string, len(fields))
			for dimension := range fields {
				maximumDimensions[dimension] = "200000"
			}
			contract.Reservations = append(contract.Reservations, types.ZTAPIImageReservationAuthority{
				Size: size, Quality: "standard", ResponseFormat: "url", N: n, MaximumDimensions: maximumDimensions,
			})
		}
	}
	_, canonical, err := types.SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	return canonical
}

func authorizeFixtureSourceAsImage(t *testing.T, fixture *ztapiPublicationGateFixture) {
	t.Helper()
	original := ztapiQuotation
	entries := append([]ZTAPIQuotationEntry(nil), original.Entries...)
	found := false
	for index := range entries {
		if entries[index].SourceModel == fixture.config.SourceModel {
			entries[index].Modality = ZTAPIModalityImage
			found = true
			break
		}
	}
	require.True(t, found)
	ztapiQuotation.Entries = entries
	t.Cleanup(func() { ztapiQuotation = original })
}

func addVerifiedImageEvidence(t *testing.T, fixture *ztapiPublicationGateFixture, contractJSON string) ZTAPIModelVerification {
	t.Helper()
	verification := fixture.verification
	verification.ID = 0
	verification.Modality = ZTAPIModalityImage
	verification.StreamingRequired = false
	verification.StreamingPassed = false
	verification.MediaResultValid = true
	verification.ImageProtocolContractJSON = contractJSON
	verification.VerifiedAt++
	require.NoError(t, fixture.db.Create(&verification).Error)
	return verification
}

func TestZTAPIImagePublicationRequiresUsableGeneratedResult(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	authorizeFixtureSourceAsImage(t, &fixture)
	require.NoError(t, fixture.db.Model(&fixture.price).Update("media_price_contract_json", canonicalGPImagePriceContractForPublicationTest(t)).Error)
	verification := addVerifiedImageEvidence(t, &fixture, syntheticVerifiedImageProtocolJSON(t, fixture.config.SourceModel))
	require.NoError(t, fixture.db.Model(&verification).Update("media_result_valid", false).Error)

	blockers, err := ZTAPIPublicationBlockers(fixture.config.ID)
	require.NoError(t, err)
	require.Contains(t, blockers, ZTAPIPublicationBlockerMediaResult)
}

func TestZTAPIImagePublicationFreezesVerifiedProtocolContract(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	authorizeFixtureSourceAsImage(t, &fixture)
	contractJSON := syntheticVerifiedImageProtocolJSON(t, fixture.config.SourceModel)
	require.NoError(t, fixture.db.Model(&fixture.price).Update("media_price_contract_json", canonicalGPImagePriceContractForPublicationTest(t)).Error)
	verification := addVerifiedImageEvidence(t, &fixture, contractJSON)
	var persistedVerification ZTAPIModelVerification
	require.NoError(t, fixture.db.First(&persistedVerification, verification.ID).Error)
	require.Equal(t, contractJSON, persistedVerification.ImageProtocolContractJSON)

	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.NoError(t, err)
	require.NotZero(t, committed.PublicationSnapshotID)

	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, fixture.db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.Equal(t, contractJSON, snapshot.ImageProtocolContractJSON)
	require.Equal(t, canonicalGPImagePriceContractForPublicationTest(t), snapshot.MediaPriceContractJSON)
}

func TestZTAPIImagePublicationRequiresExactImageMediaPriceContract(t *testing.T) {
	tests := []struct {
		name     string
		contract string
	}{
		{name: "empty"},
		{name: "malformed", contract: `{"version":1`},
		{name: "non-image", contract: seedanceVariantContractForTest(t, "1", "1.6666666667", "F5", "1", "1.6666666667", "G5")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupZTAPIPublicationGateFixture(t)
			authorizeFixtureSourceAsImage(t, &fixture)
			addVerifiedImageEvidence(t, &fixture, syntheticVerifiedImageProtocolJSON(t, fixture.config.SourceModel))
			require.NoError(t, fixture.db.Model(&fixture.price).Update("media_price_contract_json", tc.contract).Error)

			next := fixture.config
			next.Published = true
			committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
			require.Error(t, err)
			require.Contains(t, err.Error(), ZTAPIPublicationBlockerPriceIncomplete)
			require.Contains(t, err.Error(), ZTAPIPublicationBlockerImageProtocol)
			require.Zero(t, committed.PublicationSnapshotID)
		})
	}
}

func TestZTAPIImagePublicationRejectsPriceUsageDimensionMismatch(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	authorizeFixtureSourceAsImage(t, &fixture)
	require.NoError(t, fixture.db.Model(&fixture.price).Update("media_price_contract_json", canonicalGPImagePriceContractForPublicationTest(t)).Error)
	mismatched := syntheticVerifiedImageProtocolJSONWithUsage(t, fixture.config.SourceModel,
		map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"}, "not_reported")
	addVerifiedImageEvidence(t, &fixture, mismatched)

	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), ZTAPIPublicationBlockerImageProtocol)
	require.Zero(t, committed.PublicationSnapshotID)
}

func TestZTAPIImageProtocolUsageExactlyMatchesFrozenMediaPricing(t *testing.T) {
	price, err := parseZTAPIMediaPriceContract(gpImage2ContractForTest(t))
	require.NoError(t, err)
	base, _, err := types.ParseZTAPIImageProtocolContract(syntheticVerifiedImageProtocolJSON(t, "synthetic-image-v1"))
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func(*types.ZTAPIImageProtocolContract)
	}{
		{"missing dimension", func(c *types.ZTAPIImageProtocolContract) { delete(c.Usage.Fields, "image_output") }},
		{"extra dimension", func(c *types.ZTAPIImageProtocolContract) { c.Usage.Fields["input_tokens"] = "input_tokens" }},
		{"duplicate aliased field bindings", func(c *types.ZTAPIImageProtocolContract) { c.Usage.Fields["image_output"] = "image_input" }},
		{"generic hybrid dimensions", func(c *types.ZTAPIImageProtocolContract) {
			c.Usage.Fields = map[string]string{"text_input": "text_input", "output_tokens": "output_tokens"}
		}},
		{"wrong cache semantics", func(c *types.ZTAPIImageProtocolContract) { c.Usage.CacheSemantics = "included_in_input" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contract := base.Clone()
			tc.mutate(&contract)
			require.Error(t, validateZTAPIImagePriceProtocolCompatibility(price, contract))
		})
	}
	require.NoError(t, validateZTAPIImagePriceProtocolCompatibility(price, base))
}

func TestZTAPIGeminiImageProtocolUsageExactlyMatchesTierPricing(t *testing.T) {
	price, err := parseZTAPIMediaPriceContract(geminiImageContractForTest(t))
	require.NoError(t, err)
	protocolJSON := syntheticVerifiedImageProtocolJSONWithUsage(t, "synthetic-gemini-image-v1",
		map[string]string{"input_tokens": "prompt_token_count", "output_tokens": "candidates_token_count"}, "not_reported")
	protocol, _, err := types.ParseZTAPIImageProtocolContract(protocolJSON)
	require.NoError(t, err)
	require.NoError(t, validateZTAPIImagePriceProtocolCompatibility(price, protocol))

	protocol.Usage.Fields["image_output"] = "image_output_tokens"
	require.Error(t, validateZTAPIImagePriceProtocolCompatibility(price, protocol))
}

func setupPublishedImageFixture(t *testing.T) (ztapiPublicationGateFixture, ZTAPIModelVerification, ZTAPIModelPublicationSnapshot) {
	t.Helper()
	fixture := setupZTAPIPublicationGateFixture(t)
	authorizeFixtureSourceAsImage(t, &fixture)
	require.NoError(t, fixture.db.Model(&fixture.price).Update("media_price_contract_json", canonicalGPImagePriceContractForPublicationTest(t)).Error)
	verification := addVerifiedImageEvidence(t, &fixture, syntheticVerifiedImageProtocolJSON(t, fixture.config.SourceModel))
	next := fixture.config
	next.Published = true
	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.NoError(t, err)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, fixture.db.First(&snapshot, committed.PublicationSnapshotID).Error)
	return fixture, verification, snapshot
}

func changedVerifiedImageProtocolJSON(t *testing.T, canonical string) string {
	t.Helper()
	contract, _, err := types.ParseZTAPIImageProtocolContract(canonical)
	require.NoError(t, err)
	contract.Capabilities.MaxCount++
	for _, size := range contract.Capabilities.Sizes {
		maximumDimensions := make(map[string]string, len(contract.Usage.Fields))
		for dimension := range contract.Usage.Fields {
			maximumDimensions[dimension] = "200000"
		}
		contract.Reservations = append(contract.Reservations, types.ZTAPIImageReservationAuthority{
			Size: size, Quality: "standard", ResponseFormat: "url", N: contract.Capabilities.MaxCount, MaximumDimensions: maximumDimensions,
		})
	}
	_, changed, err := types.SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	require.NotEqual(t, canonical, changed)
	return changed
}

func TestZTAPIImageVerificationEvidenceRejectsOrdinaryGORMContractMutation(t *testing.T) {
	tests := []struct {
		name string
		run  func(*ztapiPublicationGateFixture, ZTAPIModelVerification) error
	}{
		{"loaded Update same snake-case value", func(f *ztapiPublicationGateFixture, v ZTAPIModelVerification) error {
			return f.db.Model(&v).Update("protocol", v.Protocol).Error
		}},
		{"loaded Updates zero Go-field value", func(f *ztapiPublicationGateFixture, v ZTAPIModelVerification) error {
			return f.db.Model(&v).Updates(map[string]any{"ChannelID": 0}).Error
		}},
		{"ID-only Update zero Go-field value", func(f *ztapiPublicationGateFixture, v ZTAPIModelVerification) error {
			return f.db.Model(&ZTAPIModelVerification{ID: v.ID}).Update("ModelConfigID", 0).Error
		}},
		{"ID-only Updates same snake-case value", func(f *ztapiPublicationGateFixture, v ZTAPIModelVerification) error {
			return f.db.Model(&ZTAPIModelVerification{ID: v.ID}).Updates(map[string]any{"modality": v.Modality}).Error
		}},
		{"zero model Update same snake-case value", func(f *ztapiPublicationGateFixture, v ZTAPIModelVerification) error {
			return f.db.Model(&ZTAPIModelVerification{}).Where("id = ?", v.ID).
				Update("image_protocol_contract_json", v.ImageProtocolContractJSON).Error
		}},
		{"zero model Updates zero Go-field value", func(f *ztapiPublicationGateFixture, v ZTAPIModelVerification) error {
			return f.db.Model(&ZTAPIModelVerification{}).Where("id = ?", v.ID).
				Updates(map[string]any{"Protocol": ""}).Error
		}},
		{"selected struct Updates zero protected field", func(f *ztapiPublicationGateFixture, v ZTAPIModelVerification) error {
			return f.db.Model(&v).Select("ChannelID").Updates(ZTAPIModelVerification{}).Error
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, verification, _ := setupPublishedImageFixture(t)
			require.ErrorIs(t, test.run(&fixture, verification), ErrZTAPIImageProtocolEvidenceImmutable)
		})
	}

	t.Run("Save rejects even when protected values are unchanged", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		verification.LatencyMilliseconds = 123
		require.ErrorIs(t, fixture.db.Save(&verification).Error, ErrZTAPIImageProtocolEvidenceImmutable)
	})

	t.Run("unrelated update remains allowed", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		require.NoError(t, fixture.db.Model(&verification).Update("latency_milliseconds", 123).Error)
	})

	t.Run("unrelated update with ID-only model remains allowed", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		require.NoError(t, fixture.db.Model(&ZTAPIModelVerification{ID: verification.ID}).
			Update("latency_milliseconds", 123).Error)
	})

	t.Run("explicit mutable Updates remain allowed", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		require.NoError(t, fixture.db.Model(&ZTAPIModelVerification{}).Where("id = ?", verification.ID).
			Updates(map[string]any{"status_category": "reviewed", "latency_milliseconds": int64(123)}).Error)
	})

	t.Run("selected struct mutable zero remains allowed", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		require.NoError(t, fixture.db.Model(&verification).Update("latency_milliseconds", 123).Error)
		require.NoError(t, fixture.db.Model(&verification).Select("LatencyMilliseconds").
			Updates(ZTAPIModelVerification{}).Error)
	})

	t.Run("omitted protected map field is not assigned", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		require.NoError(t, fixture.db.Model(&verification).Omit("Protocol").Updates(map[string]any{
			"Protocol": "", "latency_milliseconds": int64(123),
		}).Error)
	})

	t.Run("pointer map protected empty value is rejected without mutation", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		updates := map[string]any{"Protocol": ""}
		require.ErrorIs(t, fixture.db.Model(&verification).Updates(&updates).Error, ErrZTAPIImageProtocolEvidenceImmutable)

		var persisted ZTAPIModelVerification
		require.NoError(t, fixture.db.First(&persisted, verification.ID).Error)
		require.Equal(t, verification.Protocol, persisted.Protocol)
	})

	t.Run("pointer map mutable-only zero value remains allowed", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		require.NoError(t, fixture.db.Model(&verification).Update("latency_milliseconds", int64(123)).Error)
		updates := map[string]any{"LatencyMilliseconds": int64(0)}
		require.NoError(t, fixture.db.Model(&verification).Updates(&updates).Error)

		var persisted ZTAPIModelVerification
		require.NoError(t, fixture.db.First(&persisted, verification.ID).Error)
		require.Zero(t, persisted.LatencyMilliseconds)
	})
}

func TestZTAPIImageVerificationRejectsEveryProtectedFieldName(t *testing.T) {
	fields := []struct {
		goName string
		dbName string
	}{
		{"ImageProtocolContractJSON", "image_protocol_contract_json"},
		{"ModelConfigID", "model_config_id"}, {"ChannelID", "channel_id"},
		{"Protocol", "protocol"}, {"Modality", "modality"},
	}
	for _, field := range fields {
		t.Run(field.goName, func(t *testing.T) {
			fixture, verification, _ := setupPublishedImageFixture(t)
			require.ErrorIs(t, fixture.db.Model(&verification).Update(field.goName, 0).Error, ErrZTAPIImageProtocolEvidenceImmutable)
			require.ErrorIs(t, fixture.db.Model(&verification).Update(field.dbName, 0).Error, ErrZTAPIImageProtocolEvidenceImmutable)
		})
	}
}

func TestZTAPIImagePublicationSnapshotRejectsOrdinaryGORMIdentityAndContractMutation(t *testing.T) {
	tests := []struct {
		name string
		run  func(*ztapiPublicationGateFixture, ZTAPIModelPublicationSnapshot) error
	}{
		{"loaded Update same snake-case value", func(f *ztapiPublicationGateFixture, s ZTAPIModelPublicationSnapshot) error {
			return f.db.Model(&s).Update("public_name", s.PublicName).Error
		}},
		{"loaded Updates zero Go-field value", func(f *ztapiPublicationGateFixture, s ZTAPIModelPublicationSnapshot) error {
			return f.db.Model(&s).Updates(map[string]any{"PriceSourceID": int64(0)}).Error
		}},
		{"ID-only Update zero Go-field value", func(f *ztapiPublicationGateFixture, s ZTAPIModelPublicationSnapshot) error {
			return f.db.Model(&ZTAPIModelPublicationSnapshot{ID: s.ID}).Update("ModelVersion", uint64(0)).Error
		}},
		{"ID-only Updates same snake-case value", func(f *ztapiPublicationGateFixture, s ZTAPIModelPublicationSnapshot) error {
			return f.db.Model(&ZTAPIModelPublicationSnapshot{ID: s.ID}).Updates(map[string]any{"protocol": s.Protocol}).Error
		}},
		{"zero model Update same snake-case value", func(f *ztapiPublicationGateFixture, s ZTAPIModelPublicationSnapshot) error {
			return f.db.Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", s.ID).
				Update("image_protocol_contract_json", s.ImageProtocolContractJSON).Error
		}},
		{"zero model Updates zero Go-field value", func(f *ztapiPublicationGateFixture, s ZTAPIModelPublicationSnapshot) error {
			return f.db.Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", s.ID).
				Updates(map[string]any{"SourceModel": ""}).Error
		}},
		{"selected struct Updates zero protected field", func(f *ztapiPublicationGateFixture, s ZTAPIModelPublicationSnapshot) error {
			return f.db.Model(&s).Select("AllowedChannelIDs").Updates(ZTAPIModelPublicationSnapshot{}).Error
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, _, snapshot := setupPublishedImageFixture(t)
			require.ErrorIs(t, test.run(&fixture, snapshot), ErrZTAPIImageProtocolSnapshotImmutable)
		})
	}

	t.Run("Save rejects even when protected values are unchanged", func(t *testing.T) {
		fixture, _, snapshot := setupPublishedImageFixture(t)
		require.ErrorIs(t, fixture.db.Save(&snapshot).Error, ErrZTAPIImageProtocolSnapshotImmutable)
	})

	t.Run("explicit ID update is rejected without changing snapshot identity", func(t *testing.T) {
		fixture, _, snapshot := setupPublishedImageFixture(t)
		newID := snapshot.ID + 1000

		require.ErrorIs(t,
			fixture.db.Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", snapshot.ID).Update("id", newID).Error,
			ErrZTAPIImageProtocolSnapshotImmutable,
		)

		var persisted ZTAPIModelPublicationSnapshot
		require.NoError(t, fixture.db.First(&persisted, snapshot.ID).Error)
		require.Equal(t, snapshot.ID, persisted.ID)
		require.ErrorIs(t, fixture.db.First(&ZTAPIModelPublicationSnapshot{}, newID).Error, gorm.ErrRecordNotFound)
	})

	t.Run("explicit CreatedAt update is rejected without changing snapshot identity", func(t *testing.T) {
		fixture, _, snapshot := setupPublishedImageFixture(t)
		changedCreatedAt := snapshot.CreatedAt + 3600

		require.ErrorIs(t,
			fixture.db.Model(&snapshot).Update("created_at", changedCreatedAt).Error,
			ErrZTAPIImageProtocolSnapshotImmutable,
		)

		var persisted ZTAPIModelPublicationSnapshot
		require.NoError(t, fixture.db.First(&persisted, snapshot.ID).Error)
		require.Equal(t, snapshot.CreatedAt, persisted.CreatedAt)
	})

	t.Run("pointer map alias protected zero value is rejected without mutation", func(t *testing.T) {
		type assignmentMap = map[string]any

		fixture, _, snapshot := setupPublishedImageFixture(t)
		updates := assignmentMap{"price_source_id": int64(0)}
		require.ErrorIs(t, fixture.db.Model(&snapshot).Updates(&updates).Error, ErrZTAPIImageProtocolSnapshotImmutable)

		var persisted ZTAPIModelPublicationSnapshot
		require.NoError(t, fixture.db.First(&persisted, snapshot.ID).Error)
		require.Equal(t, snapshot.PriceSourceID, persisted.PriceSourceID)
	})
}

func TestZTAPIImagePublicationSnapshotRejectsEveryAuthorityField(t *testing.T) {
	fields := []struct {
		goName string
		dbName string
	}{
		{"ModelConfigID", "model_config_id"}, {"ModelVersion", "model_version"},
		{"SourceModel", "source_model"}, {"PublicName", "public_name"}, {"Protocol", "protocol"},
		{"ProviderFamily", "provider_family"}, {"EnabledGroups", "enabled_groups"},
		{"AllowedChannelIDs", "allowed_channel_ids"}, {"PriceSourceID", "price_source_id"},
		{"PricePolicy", "price_policy"}, {"MediaPriceContractJSON", "media_price_contract_json"},
		{"ImageProtocolContractJSON", "image_protocol_contract_json"},
		{"InputPricePerMillion", "input_price_per_million"}, {"OutputPricePerMillion", "output_price_per_million"},
		{"CacheReadRatio", "cache_read_ratio"}, {"CacheCreationRatio", "cache_creation_ratio"},
		{"CacheCreation5mRatio", "cache_creation_5m_ratio"}, {"CacheCreation1hRatio", "cache_creation_1h_ratio"},
		{"ImageRatio", "image_ratio"}, {"AudioRatio", "audio_ratio"},
		{"AudioCompletionRatio", "audio_completion_ratio"}, {"VerificationIDs", "verification_ids"},
		{"IdentityUpdatedAt", "identity_updated_at"},
	}
	for _, field := range fields {
		t.Run(field.goName, func(t *testing.T) {
			fixture, _, snapshot := setupPublishedImageFixture(t)
			require.ErrorIs(t, fixture.db.Model(&snapshot).Update(field.goName, 0).Error, ErrZTAPIImageProtocolSnapshotImmutable)
			require.ErrorIs(t, fixture.db.Model(&snapshot).Update(field.dbName, 0).Error, ErrZTAPIImageProtocolSnapshotImmutable)
		})
	}
}

type ztapiNamedAssignmentMap map[string]any

func invalidZTAPIUpdateDestinations(nilStruct func() any) []struct {
	name        string
	destination func() any
	gormError   error
} {
	return []struct {
		name        string
		destination func() any
		gormError   error
	}{
		{"nil map pointer rejected before hooks", func() any {
			var updates *map[string]any
			return updates
		}, gorm.ErrInvalidValue},
		{"nil struct pointer rejected before hooks", nilStruct, gorm.ErrInvalidValue},
		{"interface pointer chain ending in typed nil", func() any {
			var updates *map[string]any
			var boxed any = updates
			return &boxed
		}, nil},
		{"named map type unsupported by GORM", func() any {
			return ztapiNamedAssignmentMap{"protocol": ""}
		}, nil},
		{"slice", func() any { return []string{"protocol"} }, nil},
		{"array", func() any { return [1]string{"protocol"} }, nil},
		{"scalar", func() any { return 1 }, nil},
		{"channel", func() any { return make(chan struct{}) }, nil},
		{"function", func() any { return func() {} }, nil},
	}
}

func TestZTAPIImageVerificationRejectsInvalidUpdateDestinations(t *testing.T) {
	for _, test := range invalidZTAPIUpdateDestinations(func() any {
		var updates *ZTAPIModelVerification
		return updates
	}) {
		t.Run(test.name, func(t *testing.T) {
			fixture, verification, _ := setupPublishedImageFixture(t)
			var err error
			require.NotPanics(t, func() {
				err = fixture.db.Model(&verification).Updates(test.destination()).Error
			})
			if test.gormError != nil {
				require.ErrorIs(t, err, test.gormError)
			} else {
				require.ErrorIs(t, err, ErrZTAPIInvalidUpdateDestination)
				require.ErrorIs(t, err, gorm.ErrInvalidData)
			}

			var persisted ZTAPIModelVerification
			require.NoError(t, fixture.db.First(&persisted, verification.ID).Error)
			require.Equal(t, verification, persisted)
		})
	}

	t.Run("nil interface is replaced with the model before hooks", func(t *testing.T) {
		fixture, verification, _ := setupPublishedImageFixture(t)
		var err error
		require.NotPanics(t, func() {
			err = fixture.db.Model(&verification).Updates(nil).Error
		})
		require.ErrorIs(t, err, ErrZTAPIImageProtocolEvidenceImmutable)

		var persisted ZTAPIModelVerification
		require.NoError(t, fixture.db.First(&persisted, verification.ID).Error)
		require.Equal(t, verification, persisted)
	})
}

func TestZTAPIImagePublicationSnapshotRejectsInvalidUpdateDestinations(t *testing.T) {
	for _, test := range invalidZTAPIUpdateDestinations(func() any {
		var updates *ZTAPIModelPublicationSnapshot
		return updates
	}) {
		t.Run(test.name, func(t *testing.T) {
			fixture, _, snapshot := setupPublishedImageFixture(t)
			var err error
			require.NotPanics(t, func() {
				err = fixture.db.Model(&snapshot).Updates(test.destination()).Error
			})
			if test.gormError != nil {
				require.ErrorIs(t, err, test.gormError)
			} else {
				require.ErrorIs(t, err, ErrZTAPIInvalidUpdateDestination)
				require.ErrorIs(t, err, gorm.ErrInvalidData)
			}

			var persisted ZTAPIModelPublicationSnapshot
			require.NoError(t, fixture.db.First(&persisted, snapshot.ID).Error)
			require.Equal(t, snapshot, persisted)
		})
	}

	t.Run("nil interface is replaced with the model before hooks", func(t *testing.T) {
		fixture, _, snapshot := setupPublishedImageFixture(t)
		var err error
		require.NotPanics(t, func() {
			err = fixture.db.Model(&snapshot).Updates(nil).Error
		})
		require.ErrorIs(t, err, ErrZTAPIImageProtocolSnapshotImmutable)

		var persisted ZTAPIModelPublicationSnapshot
		require.NoError(t, fixture.db.First(&persisted, snapshot.ID).Error)
		require.Equal(t, snapshot, persisted)
	})
}

func TestZTAPIAssignedColumnsRejectsNilDestinations(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	tx := fixture.db.Session(&gorm.Session{DryRun: true})
	require.NoError(t, tx.Statement.Parse(&ZTAPIModelVerification{}))

	destinations := []any{nil}
	var nilMap *map[string]any
	var nilStruct *ZTAPIModelVerification
	var boxed any = nilMap
	destinations = append(destinations, nilMap, nilStruct, &boxed)

	for _, destination := range destinations {
		tx.Statement.Dest = destination
		assigned, err := ztapiAssignedColumns(tx)
		require.Nil(t, assigned)
		require.ErrorIs(t, err, ErrZTAPIInvalidUpdateDestination)
		require.ErrorIs(t, err, gorm.ErrInvalidData)
	}
}

func TestZTAPIBeforeUpdateDoesNotTreatNilReceiverAsNilDestination(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)

	verificationTx := fixture.db.Session(&gorm.Session{DryRun: true})
	require.NoError(t, verificationTx.Statement.Parse(&ZTAPIModelVerification{}))
	verificationTx.Statement.Dest = map[string]any{"latency_milliseconds": int64(1)}
	var verification *ZTAPIModelVerification
	require.NoError(t, verification.BeforeUpdate(verificationTx))

	snapshotTx := fixture.db.Session(&gorm.Session{DryRun: true})
	require.NoError(t, snapshotTx.Statement.Parse(&ZTAPIModelPublicationSnapshot{}))
	snapshotTx.Statement.Dest = map[string]any{"created_at": int64(1)}
	var snapshot *ZTAPIModelPublicationSnapshot
	err := snapshot.BeforeUpdate(snapshotTx)
	require.ErrorIs(t, err, ErrZTAPIImageProtocolSnapshotImmutable)
	require.NotErrorIs(t, err, ErrZTAPIInvalidUpdateDestination)
}

func TestZTAPIProtectedAssignmentsRejectBeforeQueryOrPredicateEvaluation(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	queryCount := 0
	predicateCallbackRan := false
	queryCallback := "test:ztapi_protected_assignment_query"
	updateCallback := "test:ztapi_protected_assignment_predicate"
	require.NoError(t, fixture.db.Callback().Query().Before("gorm:query").Register(queryCallback, func(*gorm.DB) {
		queryCount++
	}))
	require.NoError(t, fixture.db.Callback().Update().After("gorm:before_update").Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Error == nil {
			predicateCallbackRan = true
		}
	}))
	t.Cleanup(func() {
		fixture.db.Callback().Query().Remove(queryCallback)
		fixture.db.Callback().Update().Remove(updateCallback)
	})

	err := fixture.db.Session(&gorm.Session{SkipDefaultTransaction: true}).
		Model(&ZTAPIModelVerification{}).
		Where("id = ? AND status_category = ?", int64(999999), "changed-later").
		Update("protocol", "").Error

	require.ErrorIs(t, err, ErrZTAPIImageProtocolEvidenceImmutable)
	require.Zero(t, queryCount)
	require.False(t, predicateCallbackRan)
}

func TestZTAPIImageProtocolEvidencePersistenceRejectsNoncanonicalAndMismatchedContracts(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	canonical := syntheticVerifiedImageProtocolJSON(t, fixture.config.SourceModel)
	verification := fixture.verification
	verification.ID = 0
	verification.ImageProtocolContractJSON = " \n" + canonical
	require.Error(t, fixture.db.Create(&verification).Error)

	mismatch := syntheticVerifiedImageProtocolJSON(t, "different-provider-model")
	snapshot := ZTAPIModelPublicationSnapshot{
		ModelConfigID: fixture.config.ID, ModelVersion: fixture.config.Version,
		SourceModel: fixture.config.SourceModel, PublicName: fixture.config.PublicNameValue(),
		Protocol: fixture.config.Protocol, ProviderFamily: fixture.config.ProviderFamily,
		EnabledGroups: fixture.config.EnabledGroups, AllowedChannelIDs: `[]`, PriceSourceID: fixture.price.ID,
		VerificationIDs: `[]`, IdentityUpdatedAt: 1, CreatedAt: 1,
		ImageProtocolContractJSON: mismatch,
	}
	require.Error(t, fixture.db.Create(&snapshot).Error)
}

func TestZTAPIPublicationGateBlocksMissingPriceSource(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Delete(&f.price).Error)
	}, ZTAPIPublicationBlockerPriceMissing)
}

func TestZTAPIPublicationGateBlocksMissingBilledDimension(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&f.price).Update("output_per_million", "0").Error)
	}, ZTAPIPublicationBlockerPriceIncomplete)
}

func TestZTAPIPublicationGateBlocksAliasCollision(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		conflict := ZTAPIModelConfig{SourceModel: f.config.PublicNameValue(), EnabledGroups: "[]", Version: 1}
		require.NoError(t, f.db.Create(&conflict).Error)
	}, ZTAPIPublicationBlockerAliasConflict)
}

func TestZTAPIPublicationGateBlocksMissingGroups(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&f.config).Update("enabled_groups", "[]").Error)
	}, ZTAPIPublicationBlockerGroupsMissing)
}

func TestZTAPIStoredPriceMatchesPreviewAtDatabaseScale(t *testing.T) {
	require.True(t, ztapiStoredPriceMatchesPreview(0.88427755, "0.8842775452"))
	require.True(t, ztapiStoredPriceMatchesPreview(1.14956081, "1.1495608088"))
	require.False(t, ztapiStoredPriceMatchesPreview(1.14956082, "1.1495608088"))
	require.False(t, ztapiStoredPriceMatchesPreview(1.14956081, "not-a-price"))
}

func TestZTAPIPublicationGateRejectsUnmanagedOpenAICompatibleAggregator(t *testing.T) {
	requireZTAPIPublicationBlocker(t, func(f *ztapiPublicationGateFixture) {
		require.NoError(t, f.db.Model(&Channel{}).Where("id = ?", f.channel.Id).
			Update("ztapi_managed", false).Error)
	}, ZTAPIPublicationBlockerRouteUnavailable)
}

func TestZTAPIPublicationSnapshotFreezesEligibleChannelSet(t *testing.T) {
	fixture := setupZTAPIPublicationGateFixture(t)
	next := fixture.config
	next.Published = true

	committed, err := UpdateZTAPIModelConfigAndBilling(&next, fixture.config.Version, nil)
	require.NoError(t, err)
	require.True(t, committed.Published)
	require.NotZero(t, committed.PublicationSnapshotID)

	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, fixture.db.First(&snapshot, committed.PublicationSnapshotID).Error)
	require.Equal(t, []int{fixture.channel.Id}, snapshot.ChannelIDs())
	require.Equal(t, fixture.price.ID, snapshot.PriceSourceID)
	require.Equal(t, fixture.config.InputPricePerMillion, snapshot.InputPricePerMillion)
	require.Equal(t, fixture.config.OutputPricePerMillion, snapshot.OutputPricePerMillion)
	require.Equal(t, fixture.config.CacheReadRatio, snapshot.CacheReadRatio)
	require.Equal(t, fixture.config.CacheCreationRatio, snapshot.CacheCreationRatio)
	require.Equal(t, fixture.config.CacheCreation5mRatio, snapshot.CacheCreation5mRatio)
	require.Equal(t, fixture.config.CacheCreation1hRatio, snapshot.CacheCreation1hRatio)
	require.Equal(t, fixture.config.ImageRatio, snapshot.ImageRatio)
	require.Equal(t, fixture.config.AudioRatio, snapshot.AudioRatio)
	require.Equal(t, fixture.config.AudioCompletionRatio, snapshot.AudioCompletionRatio)

	weight := uint(100)
	priority := int64(0)
	second := Channel{
		Name: "later-managed-channel", Type: constant.ChannelTypeOpenAI,
		Key: "synthetic-later-channel-key", Status: common.ChannelStatusEnabled,
		Models: committed.SourceModel, Group: "default", ZTAPIManaged: true,
		Weight: &weight, Priority: &priority,
	}
	require.NoError(t, fixture.db.Create(&second).Error)
	require.NoError(t, second.AddAbilities(fixture.db))
	laterSnapshot := ZTAPIDiscoverySnapshot{
		ChannelID: second.Id, ModelListHash: strings.Repeat("c", 64),
		ModelCount: 1, FetchedAt: time.Now().Unix(),
	}
	require.NoError(t, fixture.db.Create(&laterSnapshot).Error)
	require.NoError(t, fixture.db.Create(&ZTAPIDiscoveredModel{
		SnapshotID: laterSnapshot.ID, ChannelID: second.Id, SourceModel: committed.SourceModel,
	}).Error)
	require.NoError(t, fixture.db.Create(&ZTAPIModelVerification{
		ModelConfigID: committed.ID, ChannelID: second.Id, Protocol: committed.Protocol,
		NonStreamingPassed: true, StreamingRequired: true, StreamingPassed: true,
		UsageReconciled: true, InvalidKeyClassified: true,
		InsufficientBalanceClassified: true, RateLimitClassified: true,
		TimeoutClassified: true, VerifiedAt: time.Now().Unix(), OperatorID: 1,
	}).Error)

	ids, err := GetZTAPITrustedRouteChannelIDs(committed.SourceModel, committed.Family, committed.Groups())
	require.NoError(t, err)
	require.Equal(t, []int{fixture.channel.Id}, ids)
}
