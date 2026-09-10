package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

type ZTAPIDiscoveryResult struct {
	SnapshotID    int64    `json:"snapshot_id"`
	ChannelID     int      `json:"channel_id"`
	ModelListHash string   `json:"model_list_hash"`
	ImportedCount int      `json:"imported_count"`
	ModelIDs      []string `json:"model_ids"`
	FetchedAt     int64    `json:"fetched_at"`
}

func normalizeZTAPIDiscoveryModelIDs(modelIDs []string) ([]string, error) {
	if len(modelIDs) == 0 {
		return nil, errors.New("upstream returned an empty model list")
	}
	normalized := make([]string, 0, len(modelIDs))
	seen := make(map[string]struct{}, len(modelIDs))
	for index, raw := range modelIDs {
		modelID := strings.TrimSpace(raw)
		if modelID == "" {
			return nil, fmt.Errorf("upstream model at index %d is empty", index)
		}
		if !utf8.ValidString(modelID) || len([]byte(modelID)) > 255 {
			return nil, fmt.Errorf("upstream model at index %d exceeds the supported identifier length", index)
		}
		if strings.Contains(modelID, ",") || strings.IndexFunc(modelID, unicode.IsSpace) >= 0 {
			return nil, fmt.Errorf("upstream model %q contains unsupported separators", modelID)
		}
		if _, exists := seen[modelID]; exists {
			return nil, fmt.Errorf("upstream model %q is duplicated", modelID)
		}
		seen[modelID] = struct{}{}
		normalized = append(normalized, modelID)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func ztapiDiscoveryProtocolForChannelType(channelType int) string {
	switch channelType {
	case constant.ChannelTypeOpenAI:
		return ZTAPIProtocolOpenAICompatible
	case constant.ChannelTypeAnthropic:
		return ZTAPIProtocolAnthropic
	case constant.ChannelTypeGemini:
		return ZTAPIProtocolGemini
	default:
		return ""
	}
}

// ImportZTAPIDiscovery atomically records an immutable model-list snapshot,
// private catalog rows, memberships, and disabled/enabled abilities according
// to the managed channel's current status.
func ImportZTAPIDiscovery(channelID int, modelIDs []string, fetchedAt time.Time) (*ZTAPIDiscoveryResult, error) {
	if channelID <= 0 {
		return nil, errors.New("channel ID must be positive")
	}
	normalized, err := normalizeZTAPIDiscoveryModelIDs(modelIDs)
	if err != nil {
		return nil, err
	}
	if fetchedAt.IsZero() {
		return nil, errors.New("discovery fetch time is required")
	}
	fetchedUnix := fetchedAt.UTC().Unix()
	digest := sha256.Sum256([]byte(strings.Join(normalized, "\n")))
	hash := hex.EncodeToString(digest[:])
	result := &ZTAPIDiscoveryResult{
		ChannelID: channelID, ModelListHash: hash, ImportedCount: len(normalized),
		ModelIDs: append([]string(nil), normalized...), FetchedAt: fetchedUnix,
	}

	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.Select(
			"id", "type", "status", "models", "group", "priority", "weight", "tag",
			"settings", "ztapi_managed", "ztapi_family",
		).First(&channel, channelID).Error; err != nil {
			return err
		}
		if !channel.ZTAPIManaged {
			return errors.New("model discovery requires a managed upstream channel")
		}

		snapshot := ZTAPIDiscoverySnapshot{
			ChannelID: channelID, ModelListHash: hash,
			ModelCount: len(normalized), FetchedAt: fetchedUnix,
		}
		if err := tx.Create(&snapshot).Error; err != nil {
			return err
		}
		result.SnapshotID = snapshot.ID

		protocol := ztapiDiscoveryProtocolForChannelType(channel.Type)
		now := common.GetTimestamp()
		for _, sourceModel := range normalized {
			var config ZTAPIModelConfig
			err := tx.Where("source_model = ?", sourceModel).First(&config).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				config = ZTAPIModelConfig{
					SourceModel: sourceModel, Protocol: protocol,
					EnabledGroups: "[]", Published: false, Version: 1,
					CreatedAt: now, UpdatedAt: now,
				}
				if _, ratioErr := applyZTAPIDefaultPricingRatios(&config); ratioErr != nil {
					return ratioErr
				}
				if err := tx.Create(&config).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if !config.Published && strings.TrimSpace(config.Protocol) == "" && protocol != "" {
				result := tx.Model(&ZTAPIModelConfig{}).
					Where("id = ? AND version = ? AND published = ?", config.ID, config.Version, false).
					Updates(map[string]interface{}{
						"protocol":   protocol,
						"version":    config.Version + 1,
						"updated_at": now,
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrZTAPIModelVersionConflict
				}
			}
			if err := tx.Create(&ZTAPIDiscoveredModel{
				SnapshotID: snapshot.ID, ChannelID: channelID, SourceModel: sourceModel,
			}).Error; err != nil {
				return err
			}
		}

		channel.Models = strings.Join(normalized, ",")
		return saveChannelUpstreamModelSettingsTx(tx, &channel, true)
	})
	if err != nil {
		return nil, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}
