package router

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
)

func TestAdminRBACPermissionsCoverStaffBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		role       int
		permission common.AdminPermission
		want       bool
	}{
		{"support users", common.RoleSupportUser, common.PermissionUserRead, true},
		{"support cannot write balance", common.RoleSupportUser, common.PermissionBalanceWrite, false},
		{"finance completes topups", common.RoleFinanceUser, common.PermissionFinanceWrite, true},
		{"finance cannot read channels", common.RoleFinanceUser, common.PermissionChannelRead, false},
		{"admin writes channels", common.RoleAdminUser, common.PermissionChannelWrite, true},
		{"only root assigns roles", common.RoleAdminUser, common.PermissionRoleWrite, false},
		{"root assigns roles", common.RoleRootUser, common.PermissionRoleWrite, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := common.HasAdminPermission(tc.role, tc.permission); got != tc.want {
				t.Fatalf("HasAdminPermission(%d, %q) = %v, want %v", tc.role, tc.permission, got, tc.want)
			}
		})
	}
}
