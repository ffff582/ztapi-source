package model

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrZTAPIVerificationPinInvalid = errors.New("ztapi verification route pin is invalid")

// ZTAPIHealthVerificationPin contains only durable routing identity. The
// credential field is a SHA-256 fingerprint and cannot authenticate upstream.
type ZTAPIHealthVerificationPin struct {
	CaseID            string
	LeaseToken        string
	ProbeRequestID    string
	ModelID           int
	PublicModel       string
	ChannelID         int
	EntryProtocol     string
	Protocol          string
	Stream            bool
	CredentialVersion string
	Generation        uint64
}

type ZTAPIHealthVerificationRouteCheck struct {
	CaseID            string
	LeaseToken        string
	ProbeRequestID    string
	ModelID           int
	ChannelID         int
	Protocol          string
	Stream            bool
	CredentialVersion string
	Generation        uint64
}

func validZTAPIHealthVerificationPinInput(caseID, leaseToken, requestID string, now time.Time) bool {
	return strings.TrimSpace(caseID) != "" && strings.TrimSpace(leaseToken) != "" &&
		strings.TrimSpace(requestID) != "" && len(strings.TrimSpace(requestID)) <= 255 && !now.IsZero()
}

func (s *ZTAPIHealthVerificationStore) loadDispatchPin(
	ctx context.Context,
	caseID, leaseToken, requestID string,
	now time.Time,
) (ZTAPIHealthVerificationPin, error) {
	var pin ZTAPIHealthVerificationPin
	if s == nil || s.DB == nil || !validZTAPIHealthVerificationPinInput(caseID, leaseToken, requestID, now) {
		return pin, ErrZTAPIVerificationPinInvalid
	}

	var verificationCase ZTAPIHealthVerificationCase
	err := s.DB.WithContext(ctx).First(&verificationCase, "id = ?", strings.TrimSpace(caseID)).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pin, ErrZTAPIVerificationPinInvalid
		}
		return pin, err
	}
	if verificationCase.State != "dispatching" || verificationCase.LeaseToken != strings.TrimSpace(leaseToken) ||
		verificationCase.ProbeRequestID != strings.TrimSpace(requestID) || verificationCase.LeaseUntil <= now.UTC().UnixMilli() ||
		verificationCase.Generation == 0 || !validZTAPICredentialVersion(verificationCase.CredentialVersion) {
		return pin, ErrZTAPIVerificationPinInvalid
	}

	var state ZTAPIHealthState
	if err := s.DB.WithContext(ctx).First(&state, "model_id = ?", verificationCase.ModelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pin, ErrZTAPIVerificationPinInvalid
		}
		return pin, err
	}
	if state.Generation != verificationCase.Generation {
		return pin, ErrZTAPIVerificationPinInvalid
	}

	var config ZTAPIModelConfig
	if err := s.DB.WithContext(ctx).First(&config, "id = ?", verificationCase.ModelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pin, ErrZTAPIVerificationPinInvalid
		}
		return pin, err
	}
	publicModel := config.PublicNameValue()
	if !config.Published || publicModel == "" || config.PublicationSnapshotID <= 0 {
		return pin, ErrZTAPIVerificationPinInvalid
	}

	var snapshot ZTAPIModelPublicationSnapshot
	if err := s.DB.WithContext(ctx).First(&snapshot, "id = ? AND model_config_id = ?", config.PublicationSnapshotID, config.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pin, ErrZTAPIVerificationPinInvalid
		}
		return pin, err
	}
	var allowedChannelIDs []int
	if err := common.UnmarshalJsonStr(snapshot.AllowedChannelIDs, &allowedChannelIDs); err != nil {
		return pin, ErrZTAPIVerificationPinInvalid
	}
	if snapshot.ModelVersion != config.Version || snapshot.PublicName != publicModel || snapshot.SourceModel != config.SourceModel ||
		!containsZTAPIHealthChannelID(allowedChannelIDs, verificationCase.ChannelID) {
		return pin, ErrZTAPIVerificationPinInvalid
	}

	var channel Channel
	if err := s.DB.WithContext(ctx).First(&channel, "id = ?", verificationCase.ChannelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pin, ErrZTAPIVerificationPinInvalid
		}
		return pin, err
	}
	if channel.Status != common.ChannelStatusEnabled || !channel.ZTAPIManaged || !ztapiChannelHasCredentialVersion(channel, verificationCase.CredentialVersion) {
		return pin, ErrZTAPIVerificationPinInvalid
	}
	modelAllowed := false
	for _, channelModel := range channel.GetModels() {
		if strings.TrimSpace(channelModel) == snapshot.SourceModel || strings.TrimSpace(channelModel) == "*" {
			modelAllowed = true
			break
		}
	}
	if !modelAllowed {
		return pin, ErrZTAPIVerificationPinInvalid
	}

	pin = ZTAPIHealthVerificationPin{
		CaseID: strings.TrimSpace(verificationCase.ID), LeaseToken: verificationCase.LeaseToken,
		ProbeRequestID: verificationCase.ProbeRequestID, ModelID: verificationCase.ModelID,
		PublicModel: publicModel, ChannelID: verificationCase.ChannelID,
		EntryProtocol: strings.TrimSpace(verificationCase.EntryProtocol),
		Protocol:      strings.TrimSpace(verificationCase.Protocol), Stream: verificationCase.Stream,
		CredentialVersion: verificationCase.CredentialVersion, Generation: verificationCase.Generation,
	}
	return pin, nil
}

func containsZTAPIHealthChannelID(ids []int, channelID int) bool {
	for _, id := range ids {
		if id == channelID {
			return true
		}
	}
	return false
}

func (s *ZTAPIHealthVerificationStore) LoadDispatchPin(ctx context.Context, caseID, leaseToken, requestID string, now time.Time) (ZTAPIHealthVerificationPin, error) {
	return s.loadDispatchPin(ctx, caseID, leaseToken, requestID, now)
}

func LoadZTAPIHealthVerificationDispatchPin(ctx context.Context, caseID, leaseToken, requestID string, now time.Time) (ZTAPIHealthVerificationPin, error) {
	return NewZTAPIHealthVerificationStore(DB).LoadDispatchPin(ctx, caseID, leaseToken, requestID, now)
}

func (s *ZTAPIHealthVerificationStore) ValidateDispatchPin(ctx context.Context, check ZTAPIHealthVerificationRouteCheck, now time.Time) error {
	pin, err := s.loadDispatchPin(ctx, check.CaseID, check.LeaseToken, check.ProbeRequestID, now)
	if err != nil {
		return err
	}
	if check.ModelID != pin.ModelID || check.ChannelID != pin.ChannelID ||
		strings.TrimSpace(check.Protocol) != pin.Protocol || check.Stream != pin.Stream ||
		strings.TrimSpace(check.CredentialVersion) != pin.CredentialVersion || check.Generation != pin.Generation {
		return ErrZTAPIVerificationPinInvalid
	}
	return nil
}

func ValidateZTAPIHealthVerificationDispatchPin(ctx context.Context, check ZTAPIHealthVerificationRouteCheck, now time.Time) error {
	return NewZTAPIHealthVerificationStore(DB).ValidateDispatchPin(ctx, check, now)
}
