package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// This hash identifies the retained 2026-10-30 quotation and test evidence.
const ZTAPIXcloudGLM53EvidenceSHA256 = "2bf955196e2b5e56f4f7fde5f49c524800335528d49210079b073c17bb059c67"

func ValidateZTAPIXcloudGLM53Route(source string, channelID int, baseURL, physicalModel string) error {
	if source != "glm-5.3" || channelID != 8 || strings.TrimRight(baseURL, "/") != "https://www.xcloudtoken.com" || physicalModel != "glm-5.3" {
		return errors.New("xcloud_glm53_physical_model_route_mismatch")
	}
	return nil
}

// Record only the verified model; unlike catalog discovery, this never edits
// channel models, abilities, or existing publications.
func RecordZTAPIXcloudGLM53Discovery(visible []string, operatorID int, fetchedAt time.Time) (*ZTAPIDiscoveryResult, error) {
	if operatorID <= 0 || fetchedAt.IsZero() {
		return nil, errors.New("discovery requires operator and time")
	}
	ids, err := normalizeZTAPIDiscoveryModelIDs(visible)
	if err != nil {
		return nil, err
	}
	found := false
	for _, id := range ids {
		if id == "glm-5.3" {
			found = true
		}
	}
	if !found {
		return nil, errors.New("glm-5.3 missing from upstream discovery")
	}
	hash := sha256.Sum256([]byte("glm-5.3"))
	result := &ZTAPIDiscoveryResult{ChannelID: 8, ModelListHash: hex.EncodeToString(hash[:]), ImportedCount: 1, ModelIDs: []string{"glm-5.3"}, FetchedAt: fetchedAt.Unix()}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.First(&channel, 8).Error; err != nil {
			return err
		}
		if !channel.ZTAPIManaged || channel.Status != common.ChannelStatusEnabled || ValidateZTAPIXcloudGLM53Route("glm-5.3", channel.Id, channel.GetBaseURL(), "glm-5.3") != nil {
			return errors.New("xcloud channel changed")
		}
		configured := false
		for _, name := range strings.Split(channel.Models, ",") {
			if strings.TrimSpace(name) == "glm-5.3" {
				configured = true
			}
		}
		if !configured {
			return errors.New("glm-5.3 is not configured")
		}
		var config ZTAPIModelConfig
		if err := tx.Where("source_model = ? AND public_name = ?", "glm-5.3", "zt-glm-5.3").First(&config).Error; err != nil {
			return err
		}
		snapshot := ZTAPIDiscoverySnapshot{ChannelID: 8, ModelListHash: result.ModelListHash, ModelCount: 1, FetchedAt: result.FetchedAt}
		if err := tx.Create(&snapshot).Error; err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIDiscoveredModel{SnapshotID: snapshot.ID, ChannelID: 8, SourceModel: "glm-5.3"}).Error; err != nil {
			return err
		}
		result.SnapshotID = snapshot.ID
		return tx.Create(&ZTAPIAuditEvent{Action: "model.discovery_evidence", ModelConfigID: config.ID, PublicName: config.PublicNameValue(), Version: config.Version, OperatorID: operatorID, Payload: `{"channel_id":8,"source_model":"glm-5.3"}`, CreatedAt: fetchedAt.Unix()}).Error
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func validateZTAPIXcloudGLM53Price(s *ZTAPIModelPriceSource) error {
	if s == nil {
		return errors.New("xcloud glm-5.3 price is missing")
	}
	multiplier, multiplierErr := ztapiNormalizedSaleMultiplier(s.SaleMultiplier)
	if s.SourceModel != "glm-5.3" || s.ResourceType != "pool" ||
		s.PricePolicy != string(ZTAPIPricePolicyXcloudGLM53) || s.SpendTier != "xcloud-cn" ||
		s.Currency != "USD" || s.SourceDocumentChecksum != ZTAPIXcloudGLM53EvidenceSHA256 ||
		s.MediaPriceContractJSON != "" || s.TokenPriceRulesJSON != "" || s.FXMode != "" ||
		!ztapiSameDecimal(s.InputPerMillion, "2.4") || !ztapiSameDecimal(s.OutputPerMillion, "8.4") ||
		!ztapiSameDecimal(s.CacheReadPerMillion, "0.6") || multiplierErr != nil || multiplier.String() != "1" {
		return errors.New("xcloud glm-5.3 price does not match frozen evidence")
	}
	for _, value := range []string{s.CacheWritePerMillion, s.CacheWrite5mPerMillion, s.CacheWrite1hPerMillion, s.ImageUnitCost, s.AudioUnitCost, s.RequestUnitCost} {
		if !ztapiSameDecimal(value, "0") {
			return errors.New("xcloud glm-5.3 contains an unquoted dimension")
		}
	}
	dimensions, err := parseZTAPIBillingDimensions(s.BillingDimensions)
	if err != nil {
		return err
	}
	sort.Strings(dimensions)
	if strings.Join(dimensions, ",") != "cache_read,input_tokens,output_tokens" {
		return errors.New("xcloud glm-5.3 dimensions do not match evidence")
	}
	return nil
}
