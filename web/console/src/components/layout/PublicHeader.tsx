import { useEffect, useRef, useState } from 'react';
import { Menu, X } from 'lucide-react';
import { Link } from 'react-router-dom';
import './public-header.css';

const links = [
  { label: '模型价格', to: '/models' },
  { label: '快速接入', to: '/#quickstart' },
  { label: '网关能力', to: '/#capabilities' },
  { label: '登录', to: '/login' },
] as const;

export function PublicHeader() {
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
        <Link className="public-header__brand" to="/" aria-label="ZTAPI 首页">
          <img className="public-header__mark" src="/brand/ztapi-mark.png" alt="ZTAPI" />
          <span>ZTAPI</span>
        </Link>
        <button
          ref={menuButtonRef}
          className="public-header__menu"
          type="button"
          aria-label={open ? '关闭导航菜单' : '打开导航菜单'}
          aria-expanded={open}
          aria-controls="public-navigation"
          onClick={() => setOpen((value) => !value)}
        >
          {open ? <X aria-hidden="true" /> : <Menu aria-hidden="true" />}
        </button>
        <nav
          id="public-navigation"
          className="public-header__nav"
          aria-label="公共导航"
          data-open={open}
        >
          {links.map((link) => (
            <Link
              className="public-header__link"
              key={link.label}
              to={link.to}
              onClick={() => setOpen(false)}
            >
              {link.label}
            </Link>
          ))}
          <Link
            className="public-header__link public-header__link--strong"
            to="/register"
            onClick={() => setOpen(false)}
          >
            开始使用
          </Link>
        </nav>
      </div>
    </header>
  );
}
