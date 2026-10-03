import { Image, MessageSquareText, Video } from 'lucide-react';
import { Link, NavLink, Outlet } from 'react-router-dom';
import { useLocale } from '../../i18n/locale';
import { ConsoleHeader } from './ConsoleHeader';

const workbenchNavigation = [
  { to: '/console/workbench/text', label: '文本', icon: MessageSquareText },
  { to: '/console/workbench/image', label: '图像', icon: Image },
  { to: '/console/workbench/video', label: '视频', icon: Video },
] as const;

export function WorkbenchShell() {
  const { t } = useLocale();

  return (
    <div className="console-shell workbench-shell">
      <aside className="console-sidebar workbench-sidebar">
        <Link className="console-brand" to="/console/workbench/text" aria-label="ZTAPI Workbench">
          <img src="/brand/ztapi-mark.png" alt="" width="28" height="28" />
          <span>ZTAPI</span>
        </Link>

        <nav className="console-nav workbench-nav" aria-label={t('工作台导航')}>
          <div className="console-nav-group" role="group" aria-label={t('工作台能力')}>
            <p className="console-nav-group__label">{t('工作台')}</p>
            {workbenchNavigation.map(({ to, label, icon: Icon }) => (
              <NavLink
                className={({ isActive }) =>
                  `console-nav__link${isActive ? ' console-nav__link--active' : ''}`
                }
                key={to}
                to={to}
              >
                <Icon size={18} aria-hidden="true" />
                <span>{t(label)}</span>
              </NavLink>
            ))}
          </div>
        </nav>

        <div className="workbench-sidebar__note">
          <span>{t('当前工作区')}</span>
          <strong>{t('按能力调用模型')}</strong>
        </div>
      </aside>

      <div className="console-workspace">
        <ConsoleHeader />
        <main className="console-content workbench-content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
