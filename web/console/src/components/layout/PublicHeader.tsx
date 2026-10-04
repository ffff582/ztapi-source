import { useEffect, useRef, useState } from 'react';
import { Menu, X } from 'lucide-react';
import { Link } from 'react-router-dom';
import { LanguageToggle } from '../../i18n/LanguageToggle';
import { useLocale } from '../../i18n/locale';
import { ThemeToggle } from '../../theme/ThemeToggle';
import './public-header.css';

const links: Array<{ label: string; to: string; external?: boolean }> = [
  { label: '模型市场', to: '/models' },
  { label: 'API 文档', to: '/docs/integration' },
  { label: '快速接入', to: '/#quickstart' },
  { label: '支持', to: 'https://t.me/gan66', external: true },
  { label: '登录', to: '/login' },
];

export function PublicHeader() {
  const { t } = useLocale();
  const [open, setOpen] = useState(false);
  const [scrolled, setScrolled] = useState(false);
  const menuButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    const updateScrollState = () => setScrolled(window.scrollY > 16);
    updateScrollState();
    window.addEventListener('scroll', updateScrollState, { passive: true });
    return () => window.removeEventListener('scroll', updateScrollState);
  }, []);

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || !open) {
        return;
      }

      setOpen(false);
      menuButtonRef.current?.focus();
    };

    window.addEventListener('keydown', closeOnEscape);
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [open]);

  return (
    <header className="public-header" data-scrolled={scrolled}>
      <div className="public-header__inner public-shell">
        <Link className="public-header__brand" to="/" aria-label={t('ZTAPI 首页')}>
          <img className="public-header__mark" src="/brand/ztapi-mark.png" alt="ZTAPI" />
          <span>ZTAPI</span>
        </Link>
        <div className="public-header__controls">
          <LanguageToggle tone="dark" />
          <ThemeToggle />
          <button
          ref={menuButtonRef}
          className="public-header__menu"
          type="button"
          aria-label={open ? t('关闭导航菜单') : t('打开导航菜单')}
          aria-expanded={open}
          aria-controls="public-navigation"
          onClick={() => setOpen((value) => !value)}
          >
            {open ? <X aria-hidden="true" /> : <Menu aria-hidden="true" />}
          </button>
        </div>
        <nav
          id="public-navigation"
          className="public-header__nav"
          aria-label={t('公共导航')}
          data-open={open}
        >
          {links.map((link) =>
            link.external ? (
              <a
                className="public-header__link"
                key={link.label}
                href={link.to}
                target="_blank"
                rel="noreferrer"
                onClick={() => setOpen(false)}
              >
                {t(link.label)}
              </a>
            ) : (
              <Link
                className="public-header__link"
                key={link.label}
                to={link.to}
                onClick={() => setOpen(false)}
              >
                {t(link.label)}
              </Link>
            ),
          )}
          <Link
            className="public-header__link public-header__link--strong"
            to="/register"
            onClick={() => setOpen(false)}
          >
            {t('开始使用')}
          </Link>
        </nav>
      </div>
    </header>
  );
}
