import React, { useEffect, useRef, useState } from 'react';
import { ChevronDown, Menu, X } from 'lucide-react';
import { Link, useLocation } from 'react-router-dom';
import ZtapiLogo from '../../assets/logo.tsx';
import { hasAdminPermission } from '../auth/admin-permission-policy';
import { adminNavigation } from './admin-navigation.js';

function Navigation({ role, label, onNavigate }) {
  const location = useLocation();
  const items = adminNavigation.filter((item) =>
    hasAdminPermission(role, item.permission),
  );

  return (
    <nav className='ztapi-admin-navigation' aria-label={label}>
      {items.map((item) => {
        const Icon = item.icon;
        const active = location.pathname === item.route;
        return (
          <Link
            className={`ztapi-admin-nav-link${active ? ' is-active' : ''}`}
            key={item.route}
            to={item.route}
            aria-current={active ? 'page' : undefined}
            onClick={onNavigate}
          >
            <Icon aria-hidden='true' size={18} strokeWidth={1.9} />
            <span>{item.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}

export default function AdminShell({ user, onLogout, children }) {
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);
  const drawerRef = useRef(null);
  const menuTriggerRef = useRef(null);
  const closeButtonRef = useRef(null);
  const restoreMenuFocusRef = useRef(false);
  const environment =
    import.meta.env.MODE === 'production' ? '生产环境' : '开发环境';

  const closeDrawer = () => setDrawerOpen(false);

  const openDrawer = () => {
    restoreMenuFocusRef.current = true;
    setDrawerOpen(true);
  };

  useEffect(() => {
    if (!drawerOpen) {
      if (restoreMenuFocusRef.current) {
        menuTriggerRef.current?.focus();
        restoreMenuFocusRef.current = false;
      }
      return undefined;
    }

    closeButtonRef.current?.focus();
    const trapFocus = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        closeDrawer();
        return;
      }

      if (event.key !== 'Tab') {
        return;
      }

      const focusable = Array.from(
        drawerRef.current?.querySelectorAll(
          'a[href], button:not([disabled])',
        ) || [],
      );
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!first || !last) {
        return;
      }

      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', trapFocus);
    return () => document.removeEventListener('keydown', trapFocus);
  }, [drawerOpen]);

  const logout = async () => {
    setAccountOpen(false);
    await onLogout();
  };

  return (
    <div className='ztapi-admin-app'>
      <aside className='ztapi-admin-sidebar'>
        <div className='ztapi-admin-brand'>
          <span className='ztapi-admin-brand-mark'>
            <ZtapiLogo />
          </span>
          <span>ZTAPI</span>
        </div>
        <Navigation role={user.role} label='主导航' />
      </aside>
      <header className='ztapi-admin-header'>
        <button
          className='ztapi-admin-icon-button ztapi-admin-menu-toggle'
          ref={menuTriggerRef}
          type='button'
          aria-label='打开导航'
          title='打开导航'
          onClick={openDrawer}
        >
          <Menu aria-hidden='true' size={20} />
        </button>
        <span className='ztapi-admin-environment'>{environment}</span>
        <div className='ztapi-admin-header-actions'>
          <div className='ztapi-admin-control'>
            <button
              className='ztapi-admin-account-button'
              type='button'
              aria-label={`${user.username} 账户菜单`}
              aria-expanded={accountOpen}
              onClick={() => setAccountOpen((open) => !open)}
            >
              <span className='ztapi-admin-account-name'>{user.username}</span>
              <ChevronDown aria-hidden='true' size={16} />
            </button>
            {accountOpen ? (
              <div className='ztapi-admin-account-menu'>
                <a
                  href='https://github.com/ffff582/ztapi-source'
                  target='_blank'
                  rel='noreferrer'
                >
                  查看 ZTAPI 对应源码
                </a>
                <a
                  href='https://github.com/QuantumNous/new-api'
                  target='_blank'
                  rel='noreferrer'
                >
                  查看 AGPL-3.0 许可与上游归属
                </a>
                <button type='button' onClick={logout}>
                  退出登录
                </button>
              </div>
            ) : null}
          </div>
        </div>
      </header>
      {drawerOpen ? (
        <div className='ztapi-admin-drawer-layer'>
          <div
            className='ztapi-admin-drawer-backdrop'
            aria-hidden='true'
            onClick={closeDrawer}
          />
          <aside
            className='ztapi-admin-drawer'
            ref={drawerRef}
            role='dialog'
            aria-label='管理导航'
            aria-modal='true'
          >
            <div className='ztapi-admin-drawer-header'>
              <div className='ztapi-admin-brand'>
                <span className='ztapi-admin-brand-mark'>
                  <ZtapiLogo />
                </span>
                <span>ZTAPI</span>
              </div>
              <button
                className='ztapi-admin-icon-button'
                ref={closeButtonRef}
                type='button'
                aria-label='关闭导航'
                title='关闭导航'
                onClick={closeDrawer}
              >
                <X aria-hidden='true' size={20} />
              </button>
            </div>
            <Navigation
              role={user.role}
              label='移动导航'
              onNavigate={closeDrawer}
            />
          </aside>
        </div>
      ) : null}
      <main className='ztapi-admin-content'>{children}</main>
    </div>
  );
}
