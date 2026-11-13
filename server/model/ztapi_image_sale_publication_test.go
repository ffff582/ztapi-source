package model

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIImageSalePublicationNullableColumn(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	var columns []struct {
		Name    string
		NotNull int `gorm:"column:notnull"`
	}
	require.NoError(t, db.Raw("PRAGMA table_info(ztapi_model_publication_snapshots)").Scan(&columns).Error)
	found := false
	for _, column := range columns {
		if column.Name == "image_sale_contract_json" {
			found = true
			require.Zero(t, column.NotNull, "legacy snapshots must permit NULL image sale contracts")
		}
	}
	require.True(t, found, "immutable snapshot is missing nullable image sale contract column")
}

func TestZTAPIImageSaleLegacyV1RestoreRemainsCompatible(t *testing.T) {
	db, _ := setupImageSalePublicationForTest(t)
	picture, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	// A historical v1 publication remains a valid restore authority.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(picture).Update("image_sale_contract_json", types.ZTAPIImageSaleContract20261109JSON()).Error)
	restored, err := PublishZTAPIImageSale(7, imageSaleRestoreRequestForTest(t, *picture))
	require.NoError(t, err)
	require.Empty(t, restored.ImageSaleContractJSON)
}

func TestZTAPIImageSalePublicCatalogUsesOnlyPictureRetail(t *testing.T) {
	protocol, _, err := types.ParseZTAPIImageProtocolContract(syntheticVerifiedImageProtocolJSON(t, "gpt-image-2"))
	require.NoError(t, err)
	publication := ZTAPIRuntimePublication{
		ModelConfigID: 85, PriceSourceID: 1873, SnapshotID: 1632, Version: 28,
		Modality: ZTAPIModalityImage, SourceModel: "gpt-image-2", PublicName: "zt-gp-image-2",
		ProviderFamily: ZTAPIProviderOpenAI, Protocol: ZTAPIProtocolOpenAICompatible,
		Groups: []string{"default"}, AllowedChannelIDs: []int{1},
		MediaPriceContractJSON: canonicalGPImagePriceContractForPublicationTest(t), ImageProtocolContract: &protocol,
		InputPriceDisplay: "6.5", OutputPriceDisplay: "39", OfficialUSD: map[string]string{"text_input": "5"},
	}
	publication.ImageSaleContractJSON = types.ZTAPIImageSaleContract20261109JSON()
	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{publication})
	require.Len(t, items, 1)
	item := items[0]
	require.Equal(t, "usd_per_image", item.BillingUnit)
	require.Equal(t, "per_image", item.BillingRule)
	require.Equal(t, []string{"images"}, item.BillingDimensions)
	require.Equal(t, map[string]string{"images": "0.30"}, item.SaleUSD)
	require.Len(t, item.PricingRules, 3)
	for index, resolution := range []string{"1K", "2K", "4K"} {
		rule := item.PricingRules[index]
		require.Equal(t, map[string]string{"resolution": resolution}, rule.Conditions)
		require.Equal(t, "usd_per_image", rule.BillingUnit)
		price := "0.30"
		if resolution == "4K" {
			price = "0.60"
		}
		require.Equal(t, map[string]string{"images": price}, rule.SaleUSD)
		require.Empty(t, rule.OfficialUSD)
	}
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	var public map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &public))
	require.JSONEq(t, types.ZTAPIImageSaleContract20261109JSON(), string(public["image_sale"]))
	for _, hidden := range []string{"official_reference", "official_usd", "input_price_per_million", "output_price_per_million", "token_price_rules", "source_model", "price_source_id", "allowed_channel_ids", "media_price_contract", "image_sale_contract_json"} {
		require.NotContains(t, public, hidden)
	}
	legacy := publication
	legacy.ImageSaleContractJSON = ""
	old := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{legacy})
	require.Len(t, old, 1)
	require.Equal(t, "usd_per_million_tokens", old[0].BillingUnit)
	require.Equal(t, "multi_dimension", old[0].BillingRule)
	legacyRaw, err := json.Marshal(old[0])
	require.NoError(t, err)
	require.NotContains(t, string(legacyRaw), `"image_sale"`)
	require.Contains(t, string(legacyRaw), `"input_price_per_million":""`, "legacy JSON shape must stay compatible")
}

func setupImageSalePublicationForTest(t *testing.T) (*gorm.DB, ZTAPIModelPublicationSnapshot) {
	t.Helper()
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}, &ZTAPIHealthState{}))
	name := "zt-gp-image-2"
	config := ZTAPIModelConfig{ID: 85, SourceModel: "gpt-image-2", PublicName: &name,
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI,
		EnabledGroups: `["default"]`, Published: true, Version: 27}
	require.NoError(t, db.Create(&config).Error)
	source := validZTAPIPriceSourceForTest()
	source.ID, source.ModelConfigID, source.SourceModel = 1873, config.ID, config.SourceModel
	source.ResourceType, source.PricePolicy, source.SpendTier = "pool", string(ZTAPIPricePolicyPoolOfficial80), "B"
	source.InputPerMillion, source.OutputPerMillion = "1.6500000000", "9.9000000000"
	source.SourceDocumentChecksum = ZTAPIQuotationSHA256
	source.MediaPriceContractJSON = mustCanonicalZTAPIMediaPriceContract(t, gpImage2ContractForTest(t))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&source).Error)
	_, protocolJSON, err := types.SealZTAPIImageProtocolContract(image2ABProtocol(t))
	require.NoError(t, err)
	previous := ZTAPIModelPublicationSnapshot{
		ID: 1631, ModelConfigID: config.ID, ModelVersion: config.Version,
		SourceModel: config.SourceModel, PublicName: name, Protocol: config.Protocol,
		ProviderFamily: config.ProviderFamily, EnabledGroups: config.EnabledGroups,
		AllowedChannelIDs: `[1]`, PriceSourceID: source.ID, PricePolicy: source.PricePolicy,
		MediaPriceContractJSON:    source.MediaPriceContractJSON,
		ImageProtocolContractJSON: protocolJSON,
		VerificationIDs:           `[11]`, IdentityUpdatedAt: 1, CreatedAt: 2,
	}
	require.NoError(t, db.Create(&previous).Error)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", previous.ID).Error)
	// Compare persisted authority, including the database's decimal scale.
	require.NoError(t, db.First(&previous, previous.ID).Error)
	return db, previous
}

func approvedImageSalePublicationRequestForTest() ZTAPIImageSalePublicationRequest {
	return ZTAPIImageSalePublicationRequest{Confirm: true, ExpectedModelConfigID: 85, ExpectedPriceSourceID: 1873, ExpectedSnapshotID: 1631, ExpectedVersion: 27}
}

func TestZTAPIImageSalePublicationClonesOnlyReviewedAuthority(t *testing.T) {
	db, previous := setupImageSalePublicationForTest(t)
	oldRuntime, err := GetZTAPIRuntimePublication(previous.PublicName)
	require.NoError(t, err)
	require.Empty(t, oldRuntime.ImageSaleContractJSON)
	var sourceBefore ZTAPIModelPriceSource
	require.NoError(t, db.First(&sourceBefore, previous.PriceSourceID).Error)
	// Mutable draft prices and groups must not become publication authority.
	require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = ?", previous.ModelConfigID).
		Updates(map[string]any{"input_price_per_million": 999, "enabled_groups": `["vip"]`}).Error)
	next, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	require.NotEqual(t, previous.ID, next.ID)
	require.Equal(t, uint64(28), next.ModelVersion)
	require.Equal(t, types.ZTAPIImageSalePartial20261109JSON(), next.ImageSaleContractJSON)
	expected := previous
	expected.ID, expected.ModelVersion, expected.CreatedAt = next.ID, next.ModelVersion, next.CreatedAt
	expected.ImageSaleContractJSON = next.ImageSaleContractJSON
	require.Equal(t, expected, *next, "only the new retail contract, version, ID and timestamp may differ")
	var old ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&old, previous.ID).Error)
	require.Equal(t, previous, old)
	var sourceAfter ZTAPIModelPriceSource
	require.NoError(t, db.First(&sourceAfter, previous.PriceSourceID).Error)
	require.Equal(t, sourceBefore, sourceAfter)
	runtime, err := GetZTAPIRuntimePublication(previous.PublicName)
	require.NoError(t, err)
	require.Equal(t, next.ImageSaleContractJSON, runtime.ImageSaleContractJSON, "cache reload must retain the immutable sale contract")
	require.Equal(t, next.ID, runtime.SnapshotID)
	require.Equal(t, oldRuntime.MediaPriceContractJSON, runtime.MediaPriceContractJSON, "procurement Token matrix must remain unchanged")
	require.Equal(t, oldRuntime.SaleUSD, runtime.SaleUSD, "legacy runtime price plumbing must remain intact")
	require.Empty(t, oldRuntime.ImageSaleContractJSON, "in-flight old authority must not change")
	_, err = PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	var snapshots, events int64
	require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&snapshots).Error)
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&events).Error)
	require.Equal(t, int64(2), snapshots)
	require.Equal(t, int64(1), events)
	var event ZTAPIAuditEvent
	require.NoError(t, db.First(&event).Error)
	require.Equal(t, "model.image_sale_published", event.Action)
	require.Equal(t, 7, event.OperatorID)
	for _, column := range []string{"image_sale_contract_json", "ImageSaleContractJSON"} {
		require.ErrorIs(t, db.Model(next).Update(column, "").Error, ErrZTAPIImageProtocolSnapshotImmutable)
	}
	copy := *next
	copy.ImageSaleContractJSON = ""
	require.ErrorIs(t, db.Save(&copy).Error, ErrZTAPIImageProtocolSnapshotImmutable)
}

func TestZTAPIImageSalePublicationRejectsUnreviewedStateAndRollsBack(t *testing.T) {
	for _, change := range []string{"operator", "confirm", "expected_model", "expected_source", "expected_snapshot", "expected_version", "version", "snapshot", "channels", "source", "identity", "published", "audit"} {
		t.Run(change, func(t *testing.T) {
			db, previous := setupImageSalePublicationForTest(t)
			request, operator := approvedImageSalePublicationRequestForTest(), 7
			switch change {
			case "operator":
				operator = 0
			case "confirm":
				request.Confirm = false
			case "expected_model":
				request.ExpectedModelConfigID++
			case "expected_source":
				request.ExpectedPriceSourceID++
			case "expected_snapshot":
				request.ExpectedSnapshotID++
			case "expected_version":
				request.ExpectedVersion++
			case "version":
				require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = 85").Update("version", 28).Error)
			case "snapshot":
				require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = 85").Update("publication_snapshot_id", 1630).Error)
			case "channels":
				require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&previous).Update("allowed_channel_ids", "[1,2]").Error)
			case "source":
				require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&previous).Update("price_source_id", 1874).Error)
			case "identity":
				require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = 85").Update("source_model", "another-image").Error)
			case "published":
				require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = 85").Update("published", false).Error)
			case "audit":
				require.NoError(t, db.Migrator().DropTable(&ZTAPIAuditEvent{}))
			}
			var before ZTAPIModelConfig
			require.NoError(t, db.First(&before, 85).Error)
			_, err := PublishZTAPIImageSale(operator, request)
			require.Error(t, err)
			var after ZTAPIModelConfig
			require.NoError(t, db.First(&after, 85).Error)
			require.Equal(t, before, after)
			var count int64
			require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&count).Error)
			require.Equal(t, int64(1), count, "failure must not leave an orphan publication")
		})
	}
}

func TestZTAPIImageSalePublicationValidatesCreateAndRuntimeFailClosed(t *testing.T) {
	for _, change := range []string{"malformed", "noncanonical", "price", "duplicate", "config", "model", "alias", "provider", "protocol"} {
		t.Run(change, func(t *testing.T) {
			db, snapshot := setupImageSalePublicationForTest(t)
			snapshot.ImageSaleContractJSON = types.ZTAPIImageSaleContract20261109JSON()
			switch change {
			case "malformed":
				snapshot.ImageSaleContractJSON = "{"
			case "noncanonical":
				snapshot.ImageSaleContractJSON = " " + snapshot.ImageSaleContractJSON
			case "price":
				snapshot.ImageSaleContractJSON = `{"version":1,"billing_unit":"usd_per_image","base_prices":{"1K":"0.01"}}`
			case "duplicate":
				snapshot.ImageSaleContractJSON = `{"version":1,"version":1}`
			case "config":
				snapshot.ModelConfigID = 86
			case "model":
				snapshot.SourceModel = "gp-image-2"
			case "alias":
				snapshot.PublicName = "zt-something-else"
			case "provider":
				snapshot.ProviderFamily = ZTAPIProviderGoogle
			case "protocol":
				snapshot.Protocol = ZTAPIProtocolGemini
			}
			invalid := snapshot
			invalid.ID = 0
			require.Error(t, db.Create(&invalid).Error)
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Save(&snapshot).Error)
			active, err := loadZTAPIActivePublications(db)
			require.NoError(t, err)
			require.Empty(t, active)
		})
	}
}

func TestZTAPIImageSaleLegacyNULLRuntimeAndCache(t *testing.T) {
	db, previous := setupImageSalePublicationForTest(t)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&previous).Update("image_sale_contract_json", nil).Error)
	runtime, err := GetZTAPIRuntimePublication(previous.PublicName)
	require.NoError(t, err)
	require.Empty(t, runtime.ImageSaleContractJSON)
	items, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "usd_per_million_tokens", items[0].BillingUnit)
	require.Nil(t, items[0].ImageSale)
}

func TestZTAPIImageSalePublicationMigratesWithoutRewritingHistory(t *testing.T) {
	db, previous := setupImageSalePublicationForTest(t)
	require.NoError(t, db.Exec("ALTER TABLE ztapi_model_publication_snapshots RENAME COLUMN image_sale_contract_json TO legacy_image_sale_contract_json").Error)
	require.False(t, db.Migrator().HasColumn(&ZTAPIModelPublicationSnapshot{}, "ImageSaleContractJSON"))
	require.NoError(t, db.Migrator().AddColumn(&ZTAPIModelPublicationSnapshot{}, "ImageSaleContractJSON"))
	var after ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&after, previous.ID).Error)
	require.Equal(t, previous, after)
	var nullRows int64
	require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ? AND image_sale_contract_json IS NULL", previous.ID).Count(&nullRows).Error)
	require.Equal(t, int64(1), nullRows, "migration must add a nullable column, not rewrite old contracts")
}

func TestZTAPIImageSalePublicationInheritsOnOrdinaryRepublish(t *testing.T) {
	db, previous := setupImageSalePublicationForTest(t)
	next, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	var config ZTAPIModelConfig
	require.NoError(t, db.First(&config, 85).Error)
	for _, published := range []bool{true, false} {
		config.PublicationSnapshotID, config.Published = 0, published
		copy, err := createZTAPIModelPublicationSnapshotTx(db, &config, next.ModelVersion+1, ztapiPublicationEvidence{
			PriceSourceID: previous.PriceSourceID, AllowedChannelIDs: previous.ChannelIDs(),
			ImageProtocolContractJSON: previous.ImageProtocolContractJSON, VerificationIDs: []int64{11}, PreviousPublicationSnapshotID: next.ID})
		require.NoError(t, err)
		require.Equal(t, next.ImageSaleContractJSON, copy.ImageSaleContractJSON)
	}
}

func TestZTAPIImageSalePublicationConcurrentApprovalCommitsOnce(t *testing.T) {
	db, _ := setupImageSalePublicationForTest(t)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
			results <- err
		}()
	}
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else {
			require.True(t, errors.Is(err, ErrZTAPIModelVersionConflict), err)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	var snapshots, events int64
	require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&snapshots).Error)
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&events).Error)
	require.Equal(t, int64(2), snapshots)
	require.Equal(t, int64(1), events)
}

func imageSaleRestoreRequestForTest(t *testing.T, snapshot ZTAPIModelPublicationSnapshot) ZTAPIImageSalePublicationRequest {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"action": "restore_legacy_token", "confirm": true,
		"expected_model_config_id": snapshot.ModelConfigID, "expected_price_source_id": snapshot.PriceSourceID,
		"expected_snapshot_id": snapshot.ID, "expected_version": snapshot.ModelVersion})
	require.NoError(t, err)
	var request ZTAPIImageSalePublicationRequest
	require.NoError(t, json.Unmarshal(raw, &request))
	return request
}

func TestZTAPIImageSalePublicationRestoreLegacyClonesCurrentAndDoesNotResurrect(t *testing.T) {
	db, legacy := setupImageSalePublicationForTest(t)
	picture, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	restoreRequest := imageSaleRestoreRequestForTest(t, *picture)
	restored, err := PublishZTAPIImageSale(7, restoreRequest)
	require.NoError(t, err, "rollback must restore legacy retail through a new CAS publication")
	require.NotEqual(t, picture.ID, restored.ID)
	require.Equal(t, picture.ModelVersion+1, restored.ModelVersion)
	expected := *picture
	expected.ID, expected.ModelVersion, expected.CreatedAt, expected.ImageSaleContractJSON = restored.ID, restored.ModelVersion, restored.CreatedAt, ""
	require.Equal(t, expected, *restored, "restore may only remove picture retail; current cost/protocol/channels remain frozen")
	for _, historical := range []ZTAPIModelPublicationSnapshot{legacy, *picture} {
		var unchanged ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&unchanged, historical.ID).Error)
		require.Equal(t, historical, unchanged)
	}
	runtime, err := GetZTAPIRuntimePublication(legacy.PublicName)
	require.NoError(t, err)
	require.Empty(t, runtime.ImageSaleContractJSON)
	require.Equal(t, restored.ID, runtime.SnapshotID)
	items, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Nil(t, items[0].ImageSale)
	require.Equal(t, "usd_per_million_tokens", items[0].BillingUnit)
	_, err = PublishZTAPIImageSale(7, restoreRequest)
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	var config ZTAPIModelConfig
	require.NoError(t, db.First(&config, 85).Error)
	// A valid but unpublished future snapshot must never become policy authority.
	orphan := *picture
	orphan.ID, orphan.ModelVersion = 0, restored.ModelVersion+100
	require.NoError(t, db.Create(&orphan).Error)
	for _, published := range []bool{true, false} {
		config.Published, config.PublicationSnapshotID = published, 0
		next, err := createZTAPIModelPublicationSnapshotTx(db, &config, config.Version+1, ztapiPublicationEvidence{
			PriceSourceID: restored.PriceSourceID, AllowedChannelIDs: restored.ChannelIDs(),
			ImageProtocolContractJSON: restored.ImageProtocolContractJSON, VerificationIDs: []int64{11}, PreviousPublicationSnapshotID: restored.ID})
		require.NoError(t, err)
		require.Empty(t, next.ImageSaleContractJSON, "ordinary PUT or re-publish must not resurrect rolled-back retail")
	}
	var event ZTAPIAuditEvent
	require.NoError(t, db.Where("action = ?", "model.image_sale_legacy_token_restored").First(&event).Error)
	require.Equal(t, restored.ModelVersion, event.Version)
	require.Contains(t, event.Payload, `"rollback_only":true`)
}

func TestZTAPIImageSalePublicationRestoreRequiresPictureContractAndAudit(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "already_legacy", true: "audit_failure"}[enabled], func(t *testing.T) {
			db, snapshot := setupImageSalePublicationForTest(t)
			if enabled {
				picture, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
				require.NoError(t, err)
				snapshot = *picture
				require.NoError(t, db.Migrator().DropTable(&ZTAPIAuditEvent{}))
			}
			var before ZTAPIModelConfig
			require.NoError(t, db.First(&before, 85).Error)
			_, err := PublishZTAPIImageSale(7, imageSaleRestoreRequestForTest(t, snapshot))
			require.Error(t, err)
			var after ZTAPIModelConfig
			require.NoError(t, db.First(&after, 85).Error)
			require.Equal(t, before, after)
			var count int64
			require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&count).Error)
			expected := int64(1)
			if enabled {
				expected = 2
			}
			require.Equal(t, expected, count)
		})
	}
}

func TestZTAPIImageSalePublicationUnpublishPreservesExplicitPolicyPointer(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "picture", true: "restored_legacy"}[restore], func(t *testing.T) {
			db, _ := setupImageSalePublicationForTest(t)
			require.NoError(t, db.AutoMigrate(&Ability{}))
			authority, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
			require.NoError(t, err)
			if restore {
				authority, err = PublishZTAPIImageSale(7, imageSaleRestoreRequestForTest(t, *authority))
				require.NoError(t, err)
			}
			var config ZTAPIModelConfig
			require.NoError(t, db.First(&config, 85).Error)
			config.Published, config.PublicationSnapshotID = false, 0
			unpublished, err := UpdateZTAPIModelConfigAndBilling(&config, config.Version, nil)
			require.NoError(t, err)
			require.False(t, unpublished.Published)
			require.Equal(t, authority.ID, unpublished.PublicationSnapshotID)
			next, err := createZTAPIModelPublicationSnapshotTx(db, unpublished, unpublished.Version+1, ztapiPublicationEvidence{
				PriceSourceID: authority.PriceSourceID, AllowedChannelIDs: authority.ChannelIDs(),
				ImageProtocolContractJSON: authority.ImageProtocolContractJSON, VerificationIDs: []int64{11}})
			require.NoError(t, err)
			require.Equal(t, authority.ImageSaleContractJSON, next.ImageSaleContractJSON)
		})
	}
}

func TestZTAPIImageSalePublicationMissingOrStalePointerFailsClosed(t *testing.T) {
	db, legacy := setupImageSalePublicationForTest(t)
	picture, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	var config ZTAPIModelConfig
	require.NoError(t, db.First(&config, 85).Error)
	for _, pointer := range []int64{0, legacy.ID} {
		config.PublicationSnapshotID = pointer
		_, err := createZTAPIModelPublicationSnapshotTx(db, &config, config.Version+1, ztapiPublicationEvidence{
			PriceSourceID: picture.PriceSourceID, AllowedChannelIDs: picture.ChannelIDs(),
			ImageProtocolContractJSON: picture.ImageProtocolContractJSON, VerificationIDs: []int64{11}})
		require.Error(t, err, "omitted/stale policy pointers must not silently select history or legacy retail")
	}
}
