import { AdminPermission, AdminRole, hasAdminPermission } from './admin-permission-policy.js';
import { adminNavigation } from '../layout/admin-navigation.js';

it('denies new referral permissions by default and grants only root', () => {
  expect(AdminPermission.referralsRead).toBe('finance.referrals.read');
  expect(AdminPermission.referralsWrite).toBe('finance.referrals.write');
  for (const role of [1, 2, 3, 10, 999, undefined]) {
    expect(hasAdminPermission(role, AdminPermission.referralsRead)).toBe(false);
    expect(hasAdminPermission(role, AdminPermission.referralsWrite)).toBe(false);
  }
  expect(hasAdminPermission(AdminRole.root, AdminPermission.referralsRead)).toBe(true);
  expect(hasAdminPermission(AdminRole.root, AdminPermission.referralsWrite)).toBe(true);
  expect(adminNavigation.find((item) => item.route === '/finance/referrals')?.permission).toBe(AdminPermission.referralsRead);
});
