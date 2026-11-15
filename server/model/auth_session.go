package model

import "time"

type AuthSession struct {
	ID            uint       `json:"id" gorm:"primaryKey"`
	UserID        int        `json:"user_id" gorm:"index;not null"`
	FamilyID      string     `json:"family_id" gorm:"type:varchar(64);index;not null"`
	TokenHash     string     `json:"-" gorm:"type:char(64);uniqueIndex;not null"`
	ExpiresAt     time.Time  `json:"expires_at" gorm:"index;not null"`
	UsedAt        *time.Time `json:"used_at"`
	RevokedAt     *time.Time `json:"revoked_at" gorm:"index"`
	CreatedAt     time.Time  `json:"created_at" gorm:"not null"`
	CreationIP    string     `json:"creation_ip" gorm:"type:varchar(45);not null"`
	UserAgentHash string     `json:"-" gorm:"type:char(64);not null"`
}
