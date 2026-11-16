package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Reuse an existing public invitation code; allocate one for a legacy account
// with a compare-and-set of that column only, never User.Update or a balance.
func EnsureZTAPIReferralInviteCode(ctx context.Context, userID int) (string, error) {
	if userID <= 0 {
		return "", ErrZTAPIReferralInvalid
	}
	var code string
	err := referralTransaction(ctx, func(tx *gorm.DB) error {
		for attempt := 0; attempt < 4; attempt++ {
			var user User
			if err := referralForUpdate(tx).Select("id", "aff_code", "status").First(&user, userID).Error; err != nil {
				return err
			}
			if user.Status != common.UserStatusEnabled {
				return ErrZTAPIReferralInvalid
			}
			if user.AffCode != "" {
				code = user.AffCode
				return nil
			}
			candidate := common.GetRandomString(12)
			result := tx.Model(&User{}).Where("id = ? AND aff_code = ?", userID, "").UpdateColumn("aff_code", candidate)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				code = candidate
				return nil
			}
		}
		return ErrZTAPIReferralConflict
	})
	return code, err
}
