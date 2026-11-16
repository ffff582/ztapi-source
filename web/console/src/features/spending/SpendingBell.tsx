import { Bell, X } from 'lucide-react';
import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { useLocale } from '../../i18n/locale';
import { SpendingInbox } from './SpendingInbox';
import { useSpendingInbox, useSpendingSessionKey } from './useSpendingInbox';
import './spending.css';

export function SpendingBell() {
  const identity = useSpendingSessionKey();
  return <OwnerBell key={identity} />;
}
function OwnerBell() {
  const { t } = useLocale();
  const inbox = useSpendingInbox();
  const [open, setOpen] = useState(false);
  const bell = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const id = useId();
  const location = useLocation();
  function close() { setOpen(false); bell.current?.focus(); }
  useEffect(() => { setOpen(false); }, [location.pathname]);
  useLayoutEffect(() => {
    if (!open) return;
    const fit = () => {
      const element = panel.current;
      if (element) element.style.maxHeight = `${Math.max(0, Math.min(640, window.innerHeight - element.getBoundingClientRect().top - 12))}px`;
    };
    fit();
    window.addEventListener('resize', fit);
    return () => window.removeEventListener('resize', fit);
  }, [open]);
  useEffect(() => {
    if (!open) return;
    panel.current?.focus();
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpen(false);
    };
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [open]);
  return <div className="spending-bell" ref={root}>
    <button ref={bell} className="spending-icon-button" type="button" title={t('消费通知')} aria-label={t('消费通知')} aria-expanded={open} aria-controls={open ? id : undefined} aria-haspopup="dialog" onClick={() => setOpen((value) => !value)}>
      <Bell size={18} aria-hidden="true" />
      {(inbox.data?.unread_count ?? 0) > 0 && <span className="spending-bell__badge" aria-hidden="true">{inbox.data!.unread_count > 99 ? '99+' : inbox.data!.unread_count}</span>}
    </button>
    {open && <div ref={panel} id={id} className="spending-bell__panel" tabIndex={-1} role="dialog" aria-label={t('消费通知')} onKeyDown={(event) => { if (event.key === 'Escape') { event.stopPropagation(); close(); } }}>
      <div className="spending-section-heading"><h2>{t('消费通知')}</h2><button className="spending-icon-button" type="button" title={t('关闭消费通知')} aria-label={t('关闭消费通知')} onClick={close}><X size={18} aria-hidden="true" /></button></div>
      <SpendingInbox />
      <Link className="spending-bell__all" to="/console/spending" onClick={() => setOpen(false)}>{t('全部记录与设置')}</Link>
    </div>}
  </div>;
}
