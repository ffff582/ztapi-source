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

export const AdminRole = Object.freeze({
  support: 2,
  finance: 3,
  administrator: 10,
  root: 100,
});

export const AdminPermission = Object.freeze({
  overviewRead: 'overview.read',
  channelRead: 'channel.read',
  channelWrite: 'channel.write',
  modelRead: 'model.read',
  modelWrite: 'model.write',
  userRead: 'user.read',
  userStatusWrite: 'user.status.write',
  balanceWrite: 'balance.write',
  financeRead: 'finance.read',
  financeWrite: 'finance.write',
  logRead: 'log.read',
  systemWrite: 'system.write',
  roleWrite: 'role.write',
  auditRead: 'audit.read',
});

const permissionsByRole = Object.freeze({
  [AdminRole.support]: [
    AdminPermission.overviewRead,
    AdminPermission.channelRead,
    AdminPermission.modelRead,
    AdminPermission.userRead,
    AdminPermission.userStatusWrite,
    AdminPermission.logRead,
  ],
  [AdminRole.finance]: [
    AdminPermission.overviewRead,
    AdminPermission.financeRead,
    AdminPermission.financeWrite,
  ],
  [AdminRole.administrator]: [
    AdminPermission.overviewRead,
    AdminPermission.channelRead,
    AdminPermission.channelWrite,
    AdminPermission.modelRead,
    AdminPermission.modelWrite,
    AdminPermission.userRead,
    AdminPermission.userStatusWrite,
    AdminPermission.balanceWrite,
    AdminPermission.financeRead,
    AdminPermission.financeWrite,
    AdminPermission.logRead,
    AdminPermission.auditRead,
  ],
  [AdminRole.root]: [
    AdminPermission.overviewRead,
    AdminPermission.channelRead,
    AdminPermission.channelWrite,
    AdminPermission.modelRead,
    AdminPermission.modelWrite,
    AdminPermission.userRead,
    AdminPermission.userStatusWrite,
    AdminPermission.balanceWrite,
    AdminPermission.financeRead,
    AdminPermission.financeWrite,
    AdminPermission.logRead,
    AdminPermission.systemWrite,
    AdminPermission.roleWrite,
    AdminPermission.auditRead,
  ],
});

export function isKnownAdminRole(role) {
  return Number.isInteger(role) && Object.hasOwn(permissionsByRole, role);
}

export function hasAdminPermission(role, permission) {
  return (
    isKnownAdminRole(role) &&
    typeof permission === 'string' &&
    permissionsByRole[role].includes(permission)
  );
}
