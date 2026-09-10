import {
  BookOpenText,
  Boxes,
  KeyRound,
  LayoutDashboard,
  LogOut,
  ScrollText,
  WalletCards,
} from 'lucide-react';
import { Link, NavLink, Outlet, useNavigate } from 'react-router-dom';
import { useAuth } from '../../auth/session';

const consoleNavigation = [
  {
    to: '/console',
    label: '概览',
    icon: LayoutDashboard,
    end: true,
  },
  {
    to: '/console/keys',
    label: 'API 密钥',
    icon: KeyRound,
    end: false,
  },
  {
    to: '/console/models',
    label: '模型支持',
    icon: Boxes,
    end: false,
  },
  {
    to: '/console/guide',
    label: '使用说明',
    icon: BookOpenText,
    end: false,
  },
  {
    to: '/console/logs',
    label: '使用日志',
    icon: ScrollText,
    end: false,
  },
  {
    to: '/console/wallet',
    label: '余额充值',
    icon: WalletCards,
    end: false,
  },
];

export function ConsoleShell() {
  const navigate = useNavigate();
  const { user, logout } = useAuth();

  async function handleLogout() {
    await logout();
    navigate('/login', { replace: true });
  }

  return (
    <div className="console-shell">
      <aside className="console-sidebar">
        <Link className="console-brand" to="/console" aria-label="ZTAPI Console">
          <img src="/brand/ztapi-mark.png" alt="" width="28" height="28" />
          <span>ZTAPI</span>
        </Link>

        <nav className="console-nav" aria-label="控制台导航">
          {consoleNavigation.map(({ to, label, icon: Icon, end }) => (
            <NavLink
              className={({ isActive }) =>
                `console-nav__link${isActive ? ' console-nav__link--active' : ''}`
              }
              end={end}
              key={to}
              to={to}
            >
              <Icon size={18} aria-hidden="true" />
              <span>{label}</span>
            </NavLink>
          ))}
        </nav>
      </aside>

      <div className="console-workspace">
        <header className="console-header">
          <span className="console-user">
            当前账号
            <strong>{user?.username}</strong>
          </span>
          <button
            className="console-logout"
            type="button"
            onClick={handleLogout}
          >
            <LogOut size={17} aria-hidden="true" />
            <span>退出登录</span>
          </button>
        </header>

        <main className="console-content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
