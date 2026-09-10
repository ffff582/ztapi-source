package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	ztapiModelPriceStorageScale int32 = 8

	ZTAPIPublicationBlockerDiscoveryStale             = "discovery_missing_or_stale"
	ZTAPIPublicationBlockerIdentityMissing            = "identity_missing"
	ZTAPIPublicationBlockerIdentityInvalid            = "identity_invalid"
	ZTAPIPublicationBlockerRouteUnavailable           = "route_unavailable"
	ZTAPIPublicationBlockerNonStreaming               = "verification_non_streaming_failed"
	ZTAPIPublicationBlockerStreaming                  = "verification_streaming_missing"
	ZTAPIPublicationBlockerUsage                      = "verification_usage_unreconciled"
	ZTAPIPublicationBlockerErrorClassification        = "verification_error_classification_unsafe"
	ZTAPIPublicationBlockerPriceMissing               = "price_source_missing"
	ZTAPIPublicationBlockerPriceIncomplete            = "price_dimension_incomplete"
	ZTAPIPublicationBlockerEnterprisePrice            = "enterprise_price_basis_missing"
	ZTAPIPublicationBlockerAliasConflict              = "alias_conflict"
	ZTAPIPublicationBlockerGroupsMissing              = "groups_missing"
	ZTAPIPublicationBlockerHealthCircuitOpen          = "health_circuit_open"
	ZTAPIPublicationBlockerImageProtocol              = "verification_image_protocol_missing_or_invalid"
	ZTAPIPublicationBlockerVideoProtocol              = "verification_video_protocol_missing_or_invalid"
	ZTAPIPublicationBlockerMediaResult                = "verification_media_result_invalid"
	ZTAPIPublicationBlockerVideoCreate                = "verification_video_create_failed"
	ZTAPIPublicationBlockerVideoFetch                 = "verification_video_fetch_failed"
	ZTAPIPublicationBlockerVideoTerminal              = "verification_video_terminal_failed"
	ZTAPIPublicationBlockerVideoRestartRecovery       = "verification_video_restart_recovery_failed"
	ZTAPIPublicationBlockerVideoSettlementIdempotence = "verification_video_settlement_idempotence_failed"
)

func ztapiStoredPriceMatchesPreview(stored float64, rawPreview string) bool {
	preview, err := decimal.NewFromString(strings.TrimSpace(rawPreview))
	if err != nil {
		return false
	}
	return decimal.NewFromFloat(stored).Round(ztapiModelPriceStorageScale).
		Equal(preview.Round(ztapiModelPriceStorageScale))
}

var ztapiPublicationEvidenceMaxAge = 24 * time.Hour

type ztapiPublicationEvidence struct {
	AllowedChannelIDs         []int
	VerificationIDs           []int64
	PriceSourceID             int64
	IdentityUpdatedAt         int64
	ImageProtocolContractJSON string
	VideoProtocolContractJSON string
}

type ZTAPIPublicationBlockedError struct {
	Blockers []string
}

func (err *ZTAPIPublicationBlockedError) Error() string {
	return fmt.Sprintf("ZTAPI model publication blocked: %v", err.Blockers)
}

func appendZTAPIBlocker(blockers []string, blocker string) []string {
	for _, existing := range blockers {
		if existing == blocker {
			return blockers
		}
	}
	return append(blockers, blocker)
}

func ztapiChannelTypeSupportsProtocol(channelType int, protocol string) bool {
	switch protocol {
	case ZTAPIProtocolOpenAICompatible:
		return channelType == constant.ChannelTypeOpenAI
	case ZTAPIProtocolAnthropic:
		return channelType == constant.ChannelTypeAnthropic
	case ZTAPIProtocolGemini:
		return channelType == constant.ChannelTypeGemini
	default:
		return false
	}
}

func ztapiRouteMatchesEvidence(route ztapiRouteAuthority, config *ZTAPIModelConfig) bool {
	if route.Managed {
		return ztapiChannelTypeSupportsProtocol(route.ChannelType, config.Protocol)
	}
	family := ztapiLegacyFamilyForProvider(config.ProviderFamily)
	if family == "" {
		return false
	}
	routeFamily, ok := ztapiFamilyForTrustedRoute(route.ChannelType, route.BaseURL, false, route.Family)
	return ok && routeFamily == family
}

func ztapiPublicationRouteIDsByGroupTx(
	tx *gorm.DB,
	config *ZTAPIModelConfig,
	freshDiscoveryChannels map[int]struct{},
) (map[string][]int, error) {
	result := make(map[string][]int)
	for _, group := range config.Groups() {
		groupColumn := `abilities."group"`
		if common.UsingMySQL {
			groupColumn = "abilities.`group`"
		}
		var routes []ztapiRouteAuthority
		err := tx.Table("abilities").
			Select("DISTINCT channels.id AS channel_id, channels.type AS channel_type, channels.base_url AS base_url, channels.ztapi_managed AS ztapi_managed, channels.ztapi_family AS ztapi_family").
			Joins("JOIN channels ON channels.id = abilities.channel_id").
			Where("abilities.model = ? AND abilities.enabled = ? AND channels.status = ?", config.SourceModel, true, common.ChannelStatusEnabled).
			Where(groupColumn+" = ?", group).
			Scan(&routes).Error
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			if _, discovered := freshDiscoveryChannels[route.ChannelID]; !discovered {
				continue
			}
			if ztapiRouteMatchesEvidence(route, config) {
				result[group] = append(result[group], route.ChannelID)
			}
		}
		sort.Ints(result[group])
	}
	return result, nil
}

func ztapiPublicationBlockersTx(tx *gorm.DB, config *ZTAPIModelConfig) ([]string, ztapiPublicationEvidence, error) {
	if tx == nil || config == nil {
		return nil, ztapiPublicationEvidence{}, errors.New("publication gate is not initialized")
	}
	blockers := make([]string, 0)
	evidence := ztapiPublicationEvidence{}
	healthOpen, healthErr := ztapiHealthPublicationOpenTx(tx, config.ID)
	if healthErr != nil {
		return nil, evidence, healthErr
	}
	if healthOpen {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerHealthCircuitOpen)
	}
	groups := config.Groups()
	if err := validateZTAPIPublicationGroups(groups); err != nil {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerGroupsMissing)
	}

	var identity ZTAPIModelIdentity
	identityErr := tx.First(&identity, "model_config_id = ?", config.ID).Error
	if errors.Is(identityErr, gorm.ErrRecordNotFound) {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerIdentityMissing)
	} else if identityErr != nil {
		return nil, evidence, identityErr
	} else {
		evidence.IdentityUpdatedAt = identity.UpdatedAt
		if identity.PublicName != config.PublicNameValue() || identity.Protocol != config.Protocol ||
			identity.ProviderFamily != config.ProviderFamily || identity.SourceReference == "" ||
			!IsSupportedZTAPIProtocol(identity.Protocol) ||
			!IsSupportedZTAPIProviderFamily(identity.ProviderFamily) {
			blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerIdentityInvalid)
		}
	}

	if config.PublicNameValue() == "" {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerIdentityInvalid)
	} else {
		var aliasConflicts int64
		if err := tx.Model(&ZTAPIModelConfig{}).
			Where("id <> ? AND source_model = ?", config.ID, config.PublicNameValue()).
			Count(&aliasConflicts).Error; err != nil {
			return nil, evidence, err
		}
		if aliasConflicts > 0 {
			blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerAliasConflict)
		}
	}

	cutoff := time.Now().UTC().Add(-ztapiPublicationEvidenceMaxAge).Unix()
	type discoveryRow struct {
		ChannelID int `gorm:"column:channel_id"`
	}
	var discoveryRows []discoveryRow
	if err := tx.Table("ztapi_discovered_models").
		Select("DISTINCT ztapi_discovered_models.channel_id AS channel_id").
		Joins("JOIN ztapi_discovery_snapshots ON ztapi_discovery_snapshots.id = ztapi_discovered_models.snapshot_id").
		Where("ztapi_discovered_models.source_model = ? AND ztapi_discovery_snapshots.fetched_at >= ?", config.SourceModel, cutoff).
		Scan(&discoveryRows).Error; err != nil {
		return nil, evidence, err
	}
	freshDiscoveryChannels := make(map[int]struct{}, len(discoveryRows))
	for _, row := range discoveryRows {
		freshDiscoveryChannels[row.ChannelID] = struct{}{}
	}
	if len(freshDiscoveryChannels) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerDiscoveryStale)
	}

	routesByGroup, err := ztapiPublicationRouteIDsByGroupTx(tx, config, freshDiscoveryChannels)
	if err != nil {
		return nil, evidence, err
	}
	routeSet := map[int]struct{}{}
	for _, group := range groups {
		if len(routesByGroup[group]) == 0 {
			blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerRouteUnavailable)
		}
		for _, channelID := range routesByGroup[group] {
			routeSet[channelID] = struct{}{}
		}
	}
	routeIDs := make([]int, 0, len(routeSet))
	for channelID := range routeSet {
		routeIDs = append(routeIDs, channelID)
	}
	sort.Ints(routeIDs)
	var priceSource ZTAPIModelPriceSource
	priceErr := tx.Where("model_config_id = ?", config.ID).Order("version DESC, id DESC").First(&priceSource).Error
	modality := ZTAPIModelModality(config.SourceModel)
	imageEvidenceRequired := modality == ZTAPIModalityImage
	videoEvidenceRequired := modality == ZTAPIModalityVideo
	mediaEvidenceRequired := imageEvidenceRequired || videoEvidenceRequired
	var mediaPriceContract ZTAPIMediaPriceContract
	mediaPriceContractValid := false
	if mediaEvidenceRequired && priceErr == nil {
		contract, contractErr := parseZTAPIMediaPriceContract(priceSource.MediaPriceContractJSON)
		canonical, canonicalErr := canonicalizeZTAPIMediaPriceContract(priceSource.MediaPriceContractJSON)
		if contractErr == nil && canonicalErr == nil && canonical == priceSource.MediaPriceContractJSON && contract.Modality == modality {
			mediaPriceContract = contract
			mediaPriceContractValid = true
		}
	}

	var verifications []ZTAPIModelVerification
	if len(routeIDs) > 0 {
		if err := tx.Where("model_config_id = ? AND channel_id IN ? AND verified_at >= ?", config.ID, routeIDs, cutoff).
			Order("verified_at DESC, id DESC").Find(&verifications).Error; err != nil {
			return nil, evidence, err
		}
	}
	latestByChannel := make(map[int]ZTAPIModelVerification)
	for _, verification := range verifications {
		if _, exists := latestByChannel[verification.ChannelID]; !exists {
			latestByChannel[verification.ChannelID] = verification
		}
	}
	nonStreaming := make(map[int]ZTAPIModelVerification)
	streaming := make(map[int]ZTAPIModelVerification)
	usage := make(map[int]ZTAPIModelVerification)
	complete := make(map[int]ZTAPIModelVerification)
	embedding := modality == ZTAPIModalityEmbedding
	imageContracts := make(map[int]string)
	videoContracts := make(map[int]string)
	mediaResults := make(map[int]ZTAPIModelVerification)
	videoCreates := make(map[int]ZTAPIModelVerification)
	videoFetches := make(map[int]ZTAPIModelVerification)
	videoTerminals := make(map[int]ZTAPIModelVerification)
	videoRestartRecoveries := make(map[int]ZTAPIModelVerification)
	videoSettlementIdempotences := make(map[int]ZTAPIModelVerification)
	for channelID, verification := range latestByChannel {
		if verification.Protocol != config.Protocol {
			continue
		}
		if imageEvidenceRequired {
			contract, canonical, contractErr := types.ParseZTAPIImageProtocolContract(verification.ImageProtocolContractJSON)
			if verification.Modality != ZTAPIModalityImage || contractErr != nil || canonical != verification.ImageProtocolContractJSON ||
				contract.ProviderModel != config.SourceModel || verification.StreamingRequired || verification.StreamingPassed ||
				!mediaPriceContractValid || validateZTAPIImagePriceProtocolCompatibility(mediaPriceContract, contract) != nil {
				continue
			}
			imageContracts[channelID] = canonical
		} else if videoEvidenceRequired {
			contract, canonical, contractErr := types.ParseZTAPIVideoProtocolContract(verification.VideoProtocolContractJSON)
			if verification.Modality != ZTAPIModalityVideo || contractErr != nil || canonical != verification.VideoProtocolContractJSON ||
				contract.ProviderModel != config.SourceModel || verification.StreamingRequired || verification.StreamingPassed ||
				!mediaPriceContractValid || types.ValidateZTAPIVideoPriceProtocolCompatibility(mediaPriceContract, contract) != nil {
				continue
			}
			videoContracts[channelID] = canonical
		} else if embedding {
			if verification.Modality != ZTAPIModalityEmbedding || verification.StreamingRequired || verification.StreamingPassed ||
				verification.PromptTokens <= 0 || verification.TotalTokens != verification.PromptTokens || verification.CompletionTokens != 0 {
				continue
			}
		} else if verification.Modality != "" && verification.Modality != ZTAPIModalityText {
			continue
		}
		if !verification.NonStreamingPassed {
			continue
		}
		nonStreaming[channelID] = verification
		if verification.StreamingRequired && !verification.StreamingPassed {
			continue
		}
		streaming[channelID] = verification
		if !verification.UsageReconciled {
			continue
		}
		usage[channelID] = verification
		if mediaEvidenceRequired {
			if !verification.MediaResultValid {
				continue
			}
			mediaResults[channelID] = verification
		}
		if videoEvidenceRequired {
			if !verification.VideoCreatePassed {
				continue
			}
			videoCreates[channelID] = verification
			if !verification.VideoFetchPassed {
				continue
			}
			videoFetches[channelID] = verification
			if !verification.VideoTerminalPassed {
				continue
			}
			videoTerminals[channelID] = verification
			if !verification.VideoRestartRecoveryPassed {
				continue
			}
			videoRestartRecoveries[channelID] = verification
			if !verification.VideoSettlementIdempotencePassed {
				continue
			}
			videoSettlementIdempotences[channelID] = verification
		}
		if !verification.InvalidKeyClassified || !verification.InsufficientBalanceClassified ||
			!verification.RateLimitClassified || !verification.TimeoutClassified {
			continue
		}
		complete[channelID] = verification
	}
	if imageEvidenceRequired && len(imageContracts) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerImageProtocol)
	}
	if videoEvidenceRequired && len(videoContracts) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerVideoProtocol)
	}
	if len(nonStreaming) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerNonStreaming)
	}
	if len(streaming) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerStreaming)
	}
	if len(usage) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerUsage)
	}
	if mediaEvidenceRequired && len(mediaResults) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerMediaResult)
	}
	if videoEvidenceRequired && len(videoCreates) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerVideoCreate)
	}
	if videoEvidenceRequired && len(videoFetches) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerVideoFetch)
	}
	if videoEvidenceRequired && len(videoTerminals) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerVideoTerminal)
	}
	if videoEvidenceRequired && len(videoRestartRecoveries) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerVideoRestartRecovery)
	}
	if videoEvidenceRequired && len(videoSettlementIdempotences) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerVideoSettlementIdempotence)
	}
	if len(complete) == 0 {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerErrorClassification)
	}
	for _, group := range groups {
		covered := false
		for _, channelID := range routesByGroup[group] {
			if verification, ok := complete[channelID]; ok {
				if imageEvidenceRequired && imageContracts[channelID] == "" {
					continue
				}
				if videoEvidenceRequired && videoContracts[channelID] == "" {
					continue
				}
				covered = true
				evidence.AllowedChannelIDs = append(evidence.AllowedChannelIDs, channelID)
				evidence.VerificationIDs = append(evidence.VerificationIDs, verification.ID)
			}
		}
		if !covered && len(routesByGroup[group]) > 0 && len(complete) > 0 {
			blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerErrorClassification)
		}
	}
	evidence.AllowedChannelIDs = uniqueSortedInts(evidence.AllowedChannelIDs)
	evidence.VerificationIDs = uniqueSortedInt64s(evidence.VerificationIDs)
	if imageEvidenceRequired && len(evidence.AllowedChannelIDs) > 0 {
		evidence.ImageProtocolContractJSON = imageContracts[evidence.AllowedChannelIDs[0]]
		for _, channelID := range evidence.AllowedChannelIDs[1:] {
			if imageContracts[channelID] != evidence.ImageProtocolContractJSON {
				blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerImageProtocol)
			}
		}
	}
	if videoEvidenceRequired && len(evidence.AllowedChannelIDs) > 0 {
		evidence.VideoProtocolContractJSON = videoContracts[evidence.AllowedChannelIDs[0]]
		for _, channelID := range evidence.AllowedChannelIDs[1:] {
			if videoContracts[channelID] != evidence.VideoProtocolContractJSON {
				blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerVideoProtocol)
			}
		}
	}

	if errors.Is(priceErr, gorm.ErrRecordNotFound) {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerPriceMissing)
	} else if priceErr != nil {
		return nil, evidence, priceErr
	} else if mediaEvidenceRequired && !mediaPriceContractValid {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerPriceIncomplete)
	} else if !ztapiEnterprisePriceBasis(config.SourceModel, priceSource.ResourceType) {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerEnterprisePrice)
	} else if priceSource.SourceModel != config.SourceModel || ValidateZTAPIModelPriceSource(&priceSource) != nil {
		blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerPriceIncomplete)
	} else {
		preview, previewErr := BuildZTAPIModelPricePreview(&priceSource)
		if previewErr != nil || preview.InputCostUSDPerMillion == "" || preview.OutputCostUSDPerMillion == "" ||
			!config.HasCompletePricing() ||
			!ztapiStoredPriceMatchesPreview(config.InputCostPerMillion, preview.InputCostUSDPerMillion) ||
			!ztapiStoredPriceMatchesPreview(config.OutputCostPerMillion, preview.OutputCostUSDPerMillion) ||
			!ztapiStoredPriceMatchesPreview(config.InputPricePerMillion, preview.InputSaleUSDPerMillion) ||
			!ztapiStoredPriceMatchesPreview(config.OutputPricePerMillion, preview.OutputSaleUSDPerMillion) ||
			!ztapiCacheRatiosMatchPreview(config, preview) {
			blockers = appendZTAPIBlocker(blockers, ZTAPIPublicationBlockerPriceIncomplete)
		} else {
			evidence.PriceSourceID = priceSource.ID
		}
	}
	if err := ValidateZTAPIQuotationIdentity(config.SourceModel, config.PublicNameValue(),
		config.Protocol, config.ProviderFamily, priceSource.SourceDocumentChecksum); err != nil {
		blockers = appendZTAPIBlocker(blockers, err.Error())
	}
	sort.Strings(blockers)
	return blockers, evidence, nil
}

func ztapiCacheRatiosMatchPreview(config *ZTAPIModelConfig, preview *ZTAPIModelPricePreview) bool {
	actual := map[string]float64{
		"cache_read_ratio":        config.CacheReadRatio,
		"cache_creation_ratio":    config.CacheCreationRatio,
		"cache_creation_5m_ratio": config.CacheCreation5mRatio,
		"cache_creation_1h_ratio": config.CacheCreation1hRatio,
	}
	for column, expected := range ztapiQuotedCacheRatios(preview) {
		if !ztapiStoredPriceMatchesPreview(actual[column], decimal.NewFromFloat(expected).String()) {
			return false
		}
	}
	return true
}

func uniqueSortedInts(values []int) []int {
	set := make(map[int]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]int, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func uniqueSortedInt64s(values []int64) []int64 {
	set := make(map[int64]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]int64, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func ZTAPIPublicationBlockers(configID int) ([]string, error) {
	if DB == nil {
		return nil, errors.New("ZTAPI database is not initialized")
	}
	var config ZTAPIModelConfig
	if err := DB.First(&config, configID).Error; err != nil {
		return nil, err
	}
	blockers, _, err := ztapiPublicationBlockersTx(DB, &config)
	return blockers, err
}

func createZTAPIModelPublicationSnapshotTx(
	tx *gorm.DB,
	config *ZTAPIModelConfig,
	nextVersion uint64,
	evidence ztapiPublicationEvidence,
) (*ZTAPIModelPublicationSnapshot, error) {
	channels, _ := json.Marshal(evidence.AllowedChannelIDs)
	verifications, _ := json.Marshal(evidence.VerificationIDs)
	snapshot := &ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: nextVersion,
		SourceModel: config.SourceModel, PublicName: config.PublicNameValue(),
		Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		EnabledGroups: config.EnabledGroups, AllowedChannelIDs: string(channels),
		PriceSourceID: evidence.PriceSourceID, VerificationIDs: string(verifications),
		ImageProtocolContractJSON: evidence.ImageProtocolContractJSON,
		VideoProtocolContractJSON: evidence.VideoProtocolContractJSON,
		InputPricePerMillion:      config.InputPricePerMillion, OutputPricePerMillion: config.OutputPricePerMillion,
		CacheReadRatio: config.CacheReadRatio, CacheCreationRatio: config.CacheCreationRatio,
		CacheCreation5mRatio: config.CacheCreation5mRatio, CacheCreation1hRatio: config.CacheCreation1hRatio,
		ImageRatio: config.ImageRatio, AudioRatio: config.AudioRatio,
		AudioCompletionRatio: config.AudioCompletionRatio,
		IdentityUpdatedAt:    evidence.IdentityUpdatedAt, CreatedAt: common.GetTimestamp(),
	}
	if err := tx.Create(snapshot).Error; err != nil {
		return nil, err
	}
	return snapshot, nil
}
