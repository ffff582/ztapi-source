package common

import "testing"

func TestZTAPIReferralPermissionsAreIndependent(t *testing.T) {
	for _, role := range []int{RoleCommonUser, RoleSupportUser, RoleFinanceUser, RoleAdminUser, -1, 999} {
		for _, permission := range []AdminPermission{"finance.referrals.read", "finance.referrals.write"} {
			if HasAdminPermission(role, permission) {
				t.Fatalf("role %d unexpectedly grants %s", role, permission)
			}
		}
	}
	for _, role := range []int{RoleRootUser} {
		for _, permission := range []AdminPermission{"finance.referrals.read", "finance.referrals.write"} {
			if !HasAdminPermission(role, permission) {
				t.Fatalf("role %d is missing %s", role, permission)
			}
		}
	}
}
