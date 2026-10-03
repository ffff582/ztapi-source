import { LogOut } from 'lucide-react';
import { Link, NavLink, useNavigate } from 'react-router-dom';
import { useAuth } from '../../auth/session';
import { LanguageToggle } from '../../i18n/LanguageToggle';
import { useLocale } from '../../i18n/locale';
import { ThemeToggle } from '../../theme/ThemeToggle';

export function ConsoleHeader() {
  const navigate = useNavigate();
  const { user, logout } = useAuth();
  const { t } = useLocale();

  async function handleLogout() {
    await logout();
    navigate('/login', { replace: true });
  }

  return (
    <header className="console-header">
      <div className="console-mode-switch" role="group" aria-label={t('产品模式')}>
        <NavLink
          className={({ isActive }) => isActive ? 'console-mode-switch__link is-active' : 'console-mode-switch__link'}
          end
          to="/console"
        >
          {t('控制台')}
        </NavLink>
        <NavLink
          className={({ isActive }) => isActive ? 'console-mode-switch__link is-active' : 'console-mode-switch__link'}
          to="/console/workbench/text"
        >
          {t('工作台')}
        </NavLink>
      </div>
      <Link className="console-header__market" to="/models">
        {t('查看公开模型市场')} <span aria-hidden="true">↗</span>
      </Link>
      <LanguageToggle />
      <ThemeToggle />
      <span className="console-user">
        <span className="console-user__label">{t('当前账号')}</span>
        <strong>{user?.username}</strong>
      </span>
      <button className="console-logout" type="button" onClick={handleLogout}>
        <LogOut size={17} aria-hidden="true" />
        <span>{t('退出登录')}</span>
      </button>
    </header>
  );
}
