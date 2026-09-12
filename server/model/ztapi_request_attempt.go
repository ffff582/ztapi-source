package model

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

type ZTAPIRequestAttempt struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	SettlementID      uint      `gorm:"not null;uniqueIndex:ztapi_request_attempt_seq;uniqueIndex:ztapi_request_attempt_channel" json:"settlement_id"`
	Attempt           int       `gorm:"not null;uniqueIndex:ztapi_request_attempt_seq" json:"attempt"`
	ChannelID         int       `gorm:"not null;uniqueIndex:ztapi_request_attempt_channel" json:"channel_id"`
	CredentialVersion string    `gorm:"type:varchar(64);not null" json:"credential_version"`
	Protocol          string    `gorm:"type:varchar(64);not null" json:"protocol"`
	UpstreamRequestID string    `gorm:"type:varchar(200);not null" json:"upstream_request_id"`
	HTTPStatus        int       `json:"http_status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (ZTAPIRequestAttempt) TableName() string { return "ztapi_request_attempts" }

func BeginZTAPIRequestAttempt(op string, channelID int, credentialVersion, protocol string) (*ZTAPIRequestAttempt, error) {
	if channelID <= 0 || credentialVersion == "" || len(credentialVersion) > 64 || protocol == "" || len(protocol) > 64 {
		return nil, ErrZTAPISettlementInvalid
	}
	var attempt ZTAPIRequestAttempt
	_, err := mutateZTAPISettlement(op, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		if row.Status != ZTAPISettlementReserved {
			return ErrZTAPISettlementPending
		}
		var prior []ZTAPIRequestAttempt
		if err := tx.Where("settlement_id = ?", row.ID).Find(&prior).Error; err != nil {
			return err
		}
		if len(prior) >= 2 {
			return ErrZTAPISettlementConflict
		}
		for _, a := range prior {
			if a.ChannelID == channelID {
				return ErrZTAPISettlementConflict
			}
		}
		attempt = ZTAPIRequestAttempt{SettlementID: row.ID, Attempt: len(prior) + 1, ChannelID: channelID, CredentialVersion: credentialVersion, Protocol: protocol}
		if err := tx.Create(&attempt).Error; err != nil {
			return err
		}
		row.Dispatched = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &attempt, nil
}

func RecordZTAPIRequestAttemptResponse(op string, index, channelID, status int, upstreamID string) error {
	// Only bounded opaque metadata is accepted, never raw headers or bodies.
	if status < 0 || status > 599 || len(upstreamID) > 200 || strings.ContainsAny(upstreamID, "\r\n\x00") {
		return ErrZTAPISettlementInvalid
	}
	_, err := mutateZTAPISettlement(op, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		var attempt ZTAPIRequestAttempt
		if err := tx.Where("settlement_id = ? AND attempt = ? AND channel_id = ?", row.ID, index, channelID).Take(&attempt).Error; err != nil {
			return err
		}
		if attempt.UpstreamRequestID != "" && upstreamID != "" && attempt.UpstreamRequestID != upstreamID {
			return ErrZTAPISettlementConflict
		}
		if upstreamID != "" {
			attempt.UpstreamRequestID = upstreamID
		}
		attempt.HTTPStatus = status
		return tx.Save(&attempt).Error
	})
	return err
}
