package common

import "testing"

func TestAdminPermissionMatrix(t *testing.T) {
	if !HasAdminPermission(RoleRootUser, PermissionSystemWrite) {
		t.Fatal("root should have system.write")
	}
	if !HasAdminPermission(RoleAdminUser, PermissionChannelWrite) {
		t.Fatal("admin should have channel.write")
	}
	if !HasAdminPermission(RoleFinanceUser, PermissionFinanceWrite) {
		t.Fatal("finance should have finance.write")
	}
	if HasAdminPermission(RoleFinanceUser, PermissionChannelRead) {
		t.Fatal("finance should not have channel.read")
	}
	if !HasAdminPermission(RoleSupportUser, PermissionUserRead) {
		t.Fatal("support should have user.read")
	}
	if HasAdminPermission(RoleSupportUser, PermissionBalanceWrite) {
		t.Fatal("support should not have balance.write")
	}
}

func TestAdminPermissionFailsClosed(t *testing.T) {
	if IsValidateRole(999) {
		t.Fatal("unknown role must be invalid")
	}
	if HasAdminPermission(999, PermissionUserRead) {
		t.Fatal("unknown role must not receive permissions")
	}
	if HasAdminPermission(RoleRootUser, AdminPermission("admin.all")) {
		t.Fatal("unknown permission must not be granted")
	}
}
