import {
  BookOpenText,
  Boxes,
  KeyRound,
  LayoutDashboard,
  LogOut,
  PlayCircle,
  ScrollText,
  UserRound,
  WalletCards,
} from 'lucide-react';
import { Link, NavLink, Outlet, useNavigate } from 'react-router-dom';
import { useAuth } from '../../auth/session';
import { LanguageToggle } from '../../i18n/LanguageToggle';
import { useLocale } from '../../i18n/locale';
import { ThemeToggle } from '../../theme/ThemeToggle';

const consoleNavigation = [
  {
    label: '工作台',
    items: [
      { to: '/console', label: '看板', icon: LayoutDashboard, end: true },
      { to: '/console/models', label: '模型市场', icon: Boxes, end: false },
      { to: '/console/test', label: '在线工作台', icon: PlayCircle, end: false },
      { to: '/console/guide', label: 'API 文档', icon: BookOpenText, end: false },
    ],
  },
  {
    label: '账户与数据',
    items: [
      { to: '/console/keys', label: 'API Key', icon: KeyRound, end: false },
      { to: '/console/logs', label: '用量日志', icon: ScrollText, end: false },
      { to: '/console/wallet', label: '账单充值', icon: WalletCards, end: false },
      { to: '/console/account', label: '账号设置', icon: UserRound, end: false },
    ],
  },
];

export function ConsoleShell() {
  const navigate = useNavigate();
  const { user, logout } = useAuth();
  const { t } = useLocale();

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

        <nav className="console-nav" aria-label={t('控制台导航')}>
          {consoleNavigation.map(({ label, items }) => (
            <div className="console-nav-group" key={label} role="group" aria-label={t(label)}>
              <p className="console-nav-group__label">{t(label)}</p>
              {items.map(({ to, label: itemLabel, icon: Icon, end }) => (
                <NavLink
                  className={({ isActive }) =>
                    `console-nav__link${isActive ? ' console-nav__link--active' : ''}`
                  }
                  end={end}
                  key={to}
                  to={to}
                >
                  <Icon size={18} aria-hidden="true" />
                  <span>{t(itemLabel)}</span>
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
      </aside>

      <div className="console-workspace">
        <header className="console-header">
          <Link className="console-header__market" to="/models">
            {t('查看公开模型市场')} <span aria-hidden="true">↗</span>
          </Link>
          <LanguageToggle />
          <ThemeToggle />
          <span className="console-user">
            {t('当前账号')}
            <strong>{user?.username}</strong>
          </span>
          <button
            className="console-logout"
            type="button"
            onClick={handleLogout}
          >
            <LogOut size={17} aria-hidden="true" />
            <span>{t('退出登录')}</span>
          </button>
        </header>

        <main className="console-content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
