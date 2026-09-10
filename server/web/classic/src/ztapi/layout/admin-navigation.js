import {
  Activity,
  BadgeDollarSign,
  BookOpenCheck,
  ClipboardCheck,
  Boxes,
  FileSearch,
  LayoutDashboard,
  Settings,
  UserCog,
  Users,
  WalletCards,
} from 'lucide-react';
import {
  AdminPermission,
  hasAdminPermission,
} from '../auth/admin-permission-policy';

export const adminNavigation = [
  {
    label: '概览',
    icon: LayoutDashboard,
    route: '/overview',
    permission: AdminPermission.overviewRead,
  },
  {
    label: '上游渠道',
    icon: Boxes,
    route: '/channels',
    permission: AdminPermission.channelRead,
  },
  {
    label: '模型管理',
    icon: Activity,
    route: '/models',
    permission: AdminPermission.modelRead,
  },
  {
    label: '用户管理',
    icon: Users,
    route: '/users',
    permission: AdminPermission.userRead,
  },
  {
    label: '财务管理',
    icon: BadgeDollarSign,
    route: '/finance/orders',
    permission: AdminPermission.financeRead,
  },
  {
    label: '余额账本',
    icon: WalletCards,
    route: '/finance/ledger',
    permission: AdminPermission.financeRead,
  },
  {
    label: '财务对账',
    icon: ClipboardCheck,
    route: '/finance/reconciliation',
    permission: AdminPermission.financeRead,
  },
  {
    label: '操作日志',
    icon: FileSearch,
    route: '/logs',
    permission: AdminPermission.logRead,
  },
  {
    label: '系统设置',
    icon: Settings,
    route: '/settings',
    permission: AdminPermission.systemWrite,
  },
  {
    label: '员工管理',
    icon: UserCog,
    route: '/staff',
    permission: AdminPermission.roleWrite,
  },
  {
    label: '审计日志',
    icon: BookOpenCheck,
    route: '/audit',
    permission: AdminPermission.auditRead,
  },
];
