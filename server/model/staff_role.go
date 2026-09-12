/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package model

import (
	"errors"
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAdminUserStateConflict    = errors.New("user state changed; refresh and retry")
	ErrAdminUserForbidden        = errors.New("operator cannot manage this user")
	ErrAdminUserInvalidStatus    = errors.New("invalid user status")
	ErrAdminUserInvalidRole      = errors.New("invalid staff role")
	ErrLastEnabledRoot           = errors.New("the last enabled root user cannot be changed")
	ErrAdminUserDebtOutstanding  = errors.New("user debt must be repaid before the account can be enabled")
	ErrAdminUserCacheSyncPending = errors.New("user status committed but cache synchronization is pending")

	staffRoleSQLiteMu        sync.Mutex
	ztapiUserStatusCacheSync = func(user User) error { return updateUserCache(user) }
)

func UpdateZTAPIUserStatus(userID, operatorRole, nextStatus, expectedStatus int) (*User, error) {
	if nextStatus != common.UserStatusEnabled && nextStatus != common.UserStatusDisabled {
		return nil, ErrAdminUserInvalidStatus
	}
	if expectedStatus != common.UserStatusEnabled && expectedStatus != common.UserStatusDisabled {
		return nil, ErrAdminUserInvalidStatus
	}
	unlock := lockStaffRoleSQLite()
	defer unlock()

	var updated User
	err := DB.Transaction(func(tx *gorm.DB) error {
		user, err := lockZTAPIUser(tx, userID)
		if err != nil {
			return err
		}
		if user.Status != expectedStatus {
			return ErrAdminUserStateConflict
		}
		if operatorRole != common.RoleRootUser && user.Role != common.RoleCommonUser {
			return ErrAdminUserForbidden
		}
		if nextStatus == common.UserStatusEnabled && user.Quota < 0 {
			return ErrAdminUserDebtOutstanding
		}
		if user.Role == common.RoleRootUser && user.Status == common.UserStatusEnabled && nextStatus == common.UserStatusDisabled {
			if err := ensureAnotherEnabledRoot(tx, user.Id); err != nil {
				return err
			}
		}
		if err := tx.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]any{
			"status":         nextStatus,
			"debt_suspended": false,
		}).Error; err != nil {
			return err
		}
		user.Status = nextStatus
		user.DebtSuspended = false
		updated = *user
		if nextStatus == common.UserStatusDisabled {
			if err := ztapiUserStatusCacheSync(updated); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && nextStatus == common.UserStatusEnabled {
		if cacheErr := ztapiUserStatusCacheSync(updated); cacheErr != nil {
			return &updated, fmt.Errorf("%w: %v", ErrAdminUserCacheSyncPending, cacheErr)
		}
	}
	return &updated, err
}

func UpdateZTAPIUserRole(userID, nextRole, expectedRole int) (*User, error) {
	if !isAssignableZTAPIStaffRole(nextRole) || !common.IsValidateRole(expectedRole) {
		return nil, ErrAdminUserInvalidRole
	}
	unlock := lockStaffRoleSQLite()
	defer unlock()

	var updated User
	err := DB.Transaction(func(tx *gorm.DB) error {
		user, err := lockZTAPIUser(tx, userID)
		if err != nil {
			return err
		}
		if user.Role != expectedRole {
			return ErrAdminUserStateConflict
		}
		if user.Role == common.RoleRootUser && nextRole != common.RoleRootUser && user.Status == common.UserStatusEnabled {
			if err := ensureAnotherEnabledRoot(tx, user.Id); err != nil {
				return err
			}
		}
		if err := tx.Model(&User{}).Where("id = ?", user.Id).Update("role", nextRole).Error; err != nil {
			return err
		}
		user.Role = nextRole
		updated = *user
		return nil
	})
	return &updated, err
}

func lockStaffRoleSQLite() func() {
	if DB != nil && DB.Dialector.Name() == "sqlite" {
		staffRoleSQLiteMu.Lock()
		return staffRoleSQLiteMu.Unlock
	}
	return func() {}
}

func lockZTAPIUser(tx *gorm.DB, userID int) (*User, error) {
	var user User
	query := tx.Where("id = ?", userID)
	if tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func ensureAnotherEnabledRoot(tx *gorm.DB, excludedUserID int) error {
	query := tx.Model(&User{}).Where("role = ?", common.RoleRootUser)
	if tx.Dialector.Name() != "sqlite" {
		var roots []User
		if err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Find(&roots).Error; err != nil {
			return err
		}
	}
	var count int64
	if err := tx.Model(&User{}).
		Where("role = ? AND status = ? AND id <> ?", common.RoleRootUser, common.UserStatusEnabled, excludedUserID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrLastEnabledRoot
	}
	return nil
}

func isAssignableZTAPIStaffRole(role int) bool {
	switch role {
	case common.RoleCommonUser, common.RoleSupportUser, common.RoleFinanceUser, common.RoleAdminUser:
		return true
	default:
		return false
	}
}
