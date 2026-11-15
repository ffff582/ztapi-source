package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestZTAPIReferralRegistrationAtomicAndPlainRegistrationCompatible(t *testing.T) {
	oldGuard := ZTAPIReferralBindingAdmissionGuard
	ZTAPIReferralBindingAdmissionGuard = func(*gorm.DB, time.Time) error { return nil }
	t.Cleanup(func() { ZTAPIReferralBindingAdmissionGuard = oldGuard })
	old := DB
	t.Cleanup(func() { DB = old })
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:referral-register-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&User{}))
	_, err = CreateZTAPIUserWithEncodedPasswordAndPendingEmail("plain", "test-hash", "")
	require.NoError(t, err)
	require.NoError(t, MigrateZTAPIReferral(db))
	inviter := User{Username: "inviter", AffCode: "validInvite", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&inviter).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&ZTAPIReferralCampaign{ID: 1, Version: 1, Enabled: true, StartedAt: now.Add(-time.Hour).Unix(), RuleVersion: ZTAPIReferralRuleVersion}).Error)
	user, err := CreateZTAPIUserWithEncodedPasswordAndPendingEmail("invited", "test-hash", "", "validInvite")
	require.NoError(t, err)
	var binding ZTAPIReferralBinding
	require.NoError(t, db.Where("user_id = ?", user.Id).First(&binding).Error)
	require.Equal(t, inviter.Id, binding.InviterID)
	_, err = CreateZTAPIUserWithEncodedPasswordAndPendingEmail("badInvite", "test-hash", "", "not-a-real-code")
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&User{}).Where("username = ?", "badInvite").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&ZTAPIReferralCampaign{}).Where("id = ?", 1).Update("enabled", false).Error)
	closed, err := CreateZTAPIUserWithEncodedPasswordAndPendingEmail("whileClosed", "test-hash", "", "validInvite")
	require.NoError(t, err)
	require.NoError(t, db.Model(&ZTAPIReferralBinding{}).Where("user_id = ?", closed.Id).Count(&count).Error)
	require.Zero(t, count)
}

func TestZTAPIReferralAccountDeletionPreservesFinancialObligations(t *testing.T) {
	old := DB
	t.Cleanup(func() { DB = old })
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:referral-delete-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&User{}))
	require.NoError(t, MigrateZTAPIReferral(db))
	owner := User{Id: 1, Username: "financialOwner", AffCode: "financialOwner", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&ZTAPIReferralLedgerEntry{UserID: 1, Kind: "commission", AmountMicros: 5000000, CreatedAt: time.Now().Unix(), IdempotencyKey: "retained-evidence", Identity: "local-fixture"}).Error)
	require.Error(t, owner.Delete())
	require.Error(t, HardDeleteUserById(owner.Id))
	require.NoError(t, db.First(&User{}, owner.Id).Error)
	promoter := User{Id: 2, Username: "futureOwner", AffCode: "futureOwner", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&promoter).Error)
	require.NoError(t, db.Create(&ZTAPIReferralBinding{UserID: 3, InviterID: 2, ExpiresAt: time.Now().Add(time.Hour).Unix(), RuleVersion: ZTAPIReferralRuleVersion}).Error)
	require.Error(t, promoter.Delete())
	require.NoError(t, db.First(&User{}, promoter.Id).Error)
	var count int64
	require.NoError(t, db.Model(&ZTAPIReferralLedgerEntry{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestZTAPIReferralInviteCodeOnlyWritesPublicIdentifier(t *testing.T) {
	old := DB
	t.Cleanup(func() { DB = old })
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:referral-code-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	DB = db
	require.NoError(t, db.AutoMigrate(&User{}))
	user := User{Id: 1, Username: "legacy", Quota: 12345, UsedQuota: 6789, Password: "untouched-password-hash", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	var updateSQL string
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("capture-referral-identifier", func(tx *gorm.DB) { updateSQL = tx.Statement.SQL.String() }))
	code, err := EnsureZTAPIReferralInviteCode(context.Background(), user.Id)
	require.NoError(t, err)
	require.Len(t, code, 12)
	require.NotContains(t, strings.ToLower(updateSQL), "quota")
	require.NotContains(t, strings.ToLower(updateSQL), "password")
	again, err := EnsureZTAPIReferralInviteCode(context.Background(), user.Id)
	require.NoError(t, err)
	require.Equal(t, code, again)
	var after User
	require.NoError(t, db.First(&after, 1).Error)
	require.Equal(t, user.Quota, after.Quota)
	require.Equal(t, user.UsedQuota, after.UsedQuota)
	require.Equal(t, user.Password, after.Password)
}
