import {
  BookOpenText,
  Boxes,
  KeyRound,
  LayoutDashboard,
  ScrollText,
  UserRound,
  WalletCards,
} from 'lucide-react';
import { Link, NavLink, Outlet } from 'react-router-dom';
import { ConsoleHeader } from './ConsoleHeader';
import { useLocale } from '../../i18n/locale';

const consoleNavigation = [
  {
    label: '控制台',
    items: [
      { to: '/console', label: '看板', icon: LayoutDashboard, end: true },
      { to: '/console/models', label: '模型市场', icon: Boxes, end: false },
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
  const { t } = useLocale();

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
        <ConsoleHeader />

        <main className="console-content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
