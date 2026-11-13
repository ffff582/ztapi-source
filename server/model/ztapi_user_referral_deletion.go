package model

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrZTAPIReferralAccountClosureNeedsReview = errors.New("该账号仍有推广财务记录或历史邀请关系，请先联系客服人工关闭核对，不能直接删除")

// An account close cannot erase the identity needed to settle earned cash or
// a historical referral promise on either side. No API balance, token or channel is changed.
func deleteZTAPIUserWithReferralGuard(userID int, hard bool) (bool, error) {
	if DB == nil {
		return true, ErrZTAPIReferralNotReady
	}
	if !DB.Migrator().HasTable(&ZTAPIReferralCampaign{}) && !DB.Migrator().HasTable(&ZTAPIReferralLedgerEntry{}) && !DB.Migrator().HasTable(&ZTAPIReferralBinding{}) {
		return false, nil
	}
	err := referralTransaction(context.Background(), func(tx *gorm.DB) error {
		if err := referralLockAccount(tx, userID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&ZTAPIReferralLedgerEntry{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrZTAPIReferralAccountClosureNeedsReview
		}
		if err := tx.Model(&ZTAPIReferralWithdrawal{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrZTAPIReferralAccountClosureNeedsReview
		}
		// Evidence discovery and refunds can arrive after a binding expires.
		// Neither expiry nor a scan cursor proves that all historical obligations
		// are settled; retain both identities for manual account-closure review.
		if err := tx.Model(&ZTAPIReferralBinding{}).Where("inviter_id = ? OR user_id = ?", userID, userID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrZTAPIReferralAccountClosureNeedsReview
		}
		query := tx.Where("id = ?", userID)
		if hard {
			query = query.Unscoped()
		}
		return query.Delete(&User{}).Error
	})
	return true, err
}
