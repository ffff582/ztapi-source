import { Check, ExternalLink, Link, RefreshCw, Unlink, X } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { supportApi, supportErrorMessage, SupportError, type BindingInput, type BindingView } from '../../api/support';
import { useLocale } from '../../i18n/locale';

function botLink(raw?: string) {
  if (!raw) return undefined;
  try {
    const url = new URL(raw);
    return url.protocol === 'https:' && url.host === 't.me' && !url.username && !url.password &&
      !url.hash && /^\/[a-zA-Z0-9_]{5,32}$/.test(url.pathname) &&
      [...url.searchParams.keys()].length === 1 && /^bind_[a-zA-Z0-9_-]+$/.test(url.searchParams.get('start') || '')
      ? url.href : undefined;
  } catch { return undefined; }
}

function bindingError(cause: unknown) {
  if (cause instanceof SupportError && [409, 410].includes(cause.status))
    return '绑定请求已过期、已使用或在其他会话中变更，请刷新核对。';
  return `${supportErrorMessage(cause)} 绑定结果尚未确认，请刷新核对。`;
}

export function TelegramBinding() {
  const { t } = useLocale();
  const [view, setView] = useState<BindingView | null>(null);
  const [link, setLink] = useState<string>();
  const [reading, setReading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [identityConsent, setIdentityConsent] = useState(false);
  const [notificationConsent, setNotificationConsent] = useState(false);
  const [confirmation, setConfirmation] = useState<'cancel' | 'unbind' | null>(null);
  const [now, setNow] = useState(Date.now);
  const [pollStopped, setPollStopped] = useState(false);
  const alive = useRef(false);
  const lock = useRef(false);
  const identity = useRef('');
  const pending = view?.pending;
  const expires = pending ? Date.parse(pending.expires_at) : 0;
  const expired = !!pending && (!Number.isFinite(expires) || expires <= now);
  const blocked = busy || reading || uncertain;

  const accept = useCallback((value: BindingView, freshCreate = false) => {
    const nextIdentity = JSON.stringify([value.enabled, value.bound, value.telegram_id, value.pending?.id, value.pending?.claimed, value.pending?.telegram_id]);
    const sameIdentity = identity.current === nextIdentity;
    if (!sameIdentity) {
      setIdentityConsent(false);
      setNotificationConsent(false);
      setConfirmation(null);
      identity.current = nextIdentity;
    }
    // Only the fresh create response may expose the capability URL, in memory.
    setLink((previous) => value.enabled && !value.bound && value.pending && !value.pending.claimed && Date.parse(value.pending.expires_at) > Date.now()
      ? freshCreate ? botLink(value.binding_url) : sameIdentity ? previous : undefined
      : undefined);
    setView({ ...value, binding_url: undefined });
    setNow(Date.now());
  }, []);

  const refresh = useCallback(async (manual = false) => {
    if (lock.current) return;
    lock.current = true;
    setReading(true);
    try {
      const value = await supportApi.telegramBinding();
      if (!alive.current) return;
      accept(value);
      if (manual) {
        setUncertain(false);
        setError('');
        setPollStopped(false);
      }
    } catch (cause) {
      if (alive.current) {
        setLink(undefined);
        setIdentityConsent(false);
        setNotificationConsent(false);
        setUncertain(true);
        setPollStopped(true);
        setError(bindingError(cause));
      }
    } finally {
      lock.current = false;
      if (alive.current) setReading(false);
    }
  }, [accept]);

  useEffect(() => {
    alive.current = true;
    void refresh();
    return () => { alive.current = false; };
  }, [refresh]);

  useEffect(() => {
    if (!pending || expired || view?.bound || !view?.enabled) return;
    const timer = window.setInterval(() => {
      setNow(Date.now());
      if (Date.now() >= expires) setLink(undefined);
    }, 1000);
    return () => window.clearInterval(timer);
  }, [pending, expires, expired, view?.bound, view?.enabled]);

  const pendingId = pending?.id;
  useEffect(() => {
    if (!pendingId || expired || !view?.enabled || view.bound || pollStopped) return;
    let reads = 0;
    const timer = window.setInterval(() => {
      if (Date.now() >= expires || document.visibilityState !== 'visible' || lock.current) return;
      if (++reads > 75) { setPollStopped(true); return; }
      void refresh();
    }, 4000);
    return () => window.clearInterval(timer);
  }, [pendingId, expires, expired, view?.enabled, view?.bound, pollStopped, refresh]);

  async function change(action: BindingInput['action']) {
    if (lock.current || uncertain || !view?.enabled) return;
    if (action === 'confirm' && (!pending?.claimed || !Number.isSafeInteger(pending.telegram_id) || pending.telegram_id! <= 0 || Date.now() >= expires || !identityConsent || !notificationConsent)) return;
    if ((action === 'cancel' || action === 'unbind') && confirmation !== action) return;
    lock.current = true;
    setBusy(true);
    setError('');
    setLink(undefined);
    try {
      const value = await supportApi.updateTelegramBinding({
        action,
        idempotency_key: crypto.randomUUID(),
        ...(action === 'confirm' || action === 'cancel' ? { challenge_id: pending?.id } : {}),
      });
      if (!alive.current) return;
      accept(value, action === 'create');
      setConfirmation(null);
      setIdentityConsent(false);
      setNotificationConsent(false);
      setPollStopped(false);
    } catch (cause) {
      if (alive.current) {
        setUncertain(true);
        setIdentityConsent(false);
        setNotificationConsent(false);
        setConfirmation(null);
        setPollStopped(true);
        setError(bindingError(cause));
      }
    } finally {
      lock.current = false;
      if (alive.current) setBusy(false);
    }
  }

  return (
    <section className="support-binding" aria-label={t('Telegram 工单通知')} aria-busy={busy || reading}>
      <div className="support-section-heading">
        <h2>{t('Telegram 工单通知')}</h2>
        <button type="button" title={t('刷新绑定状态')} aria-label={t('刷新绑定状态')} disabled={busy || reading} onClick={() => void refresh(true)}><RefreshCw size={16} /></button>
      </div>
      {error && <p className="console-alert" role="alert">{t(error)}</p>}
      {!view && !error && <p role="status">{t('正在读取绑定状态...')}</p>}
      {view?.enabled === false && <p role="status">{t('Telegram 绑定暂未开放，网站客服不受影响。')}</p>}
      {view?.enabled && <>
        <div className="support-binding-summary">
          <p role="status">{t(view.bound ? '已绑定' : pending && !expired ? '尚未绑定，等待网站确认' : '未绑定')}</p>
          {view.bound && <p className="support-binding-identity">Telegram ID: <strong>{view.telegram_id}</strong>{view.display_name && ` · ${view.display_name}`}{view.username && ` · @${view.username}`}</p>}
          {!view.bound && (!pending || expired) && <button className="console-button console-button--secondary" type="button" disabled={blocked} onClick={() => void change('create')}><Link size={16} />{t('绑定 Telegram')}</button>}
          {view.bound && !confirmation && <button className="console-button console-button--secondary" type="button" disabled={blocked} onClick={() => setConfirmation('unbind')}><Unlink size={16} />{t('解除绑定')}</button>}
        </div>
        {pending && !view.bound && <>
          {expired ? <p role="status">{t('绑定请求已过期，请重新创建。')}</p> : <>
            <p>{t('有效期至')} <time dateTime={pending.expires_at}>{new Date(expires).toLocaleString('zh-CN', { hour12: false })}</time>{t('；不要转发绑定链接。')}</p>
            {!pending.claimed && (link ? <a className="console-button console-button--secondary" href={link} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer"><ExternalLink size={16} />{t('前往小 Z 认领')}</a> : <p>{t('链接仅在创建时显示；已打开机器人可继续认领，否则取消后重新创建。')}</p>)}
            {pending.claimed && <div className="support-binding-consent">
              <p className="support-binding-identity">{t('待确认 Telegram ID')}: <strong>{pending.telegram_id ?? t('未知')}</strong>{pending.display_name && ` · ${pending.display_name}`}{pending.username && ` · @${pending.username}`}</p>
              <p>{t('若不是本人，请取消绑定；用户名不能证明账号归属。')}</p>
              <label><input type="checkbox" checked={identityConsent} disabled={blocked} onChange={(event) => setIdentityConsent(event.target.checked)} />{t('我确认这是我的 Telegram 账号')}</label>
              <label><input type="checkbox" checked={notificationConsent} disabled={blocked} onChange={(event) => setNotificationConsent(event.target.checked)} />{t('同意接收工单状态通知（仅工单编号、状态和网站链接）')}</label>
              <button className="console-button console-button--primary" type="button" disabled={blocked || !identityConsent || !notificationConsent || !Number.isSafeInteger(pending.telegram_id) || pending.telegram_id! <= 0} onClick={() => void change('confirm')}><Check size={16} />{t('确认绑定')}</button>
            </div>}
          </>}
          {!confirmation && <button className="console-button console-button--secondary" type="button" disabled={blocked} onClick={() => setConfirmation('cancel')}><X size={16} />{t('取消绑定')}</button>}
        </>}
        {confirmation && <div className="support-binding-confirm" role="group" aria-label={t('确认绑定变更')}>
          <p>{t(confirmation === 'unbind' ? '解除后将停止 Telegram 私有查询和工单通知。确认解除？' : '取消后当前链接将失效。确认取消？')}</p>
          <button className="console-button console-button--secondary" type="button" disabled={blocked} onClick={() => setConfirmation(null)}><X size={16} />{t('保留当前状态')}</button>
          <button className="console-button console-button--secondary" type="button" disabled={blocked} onClick={() => void change(confirmation)}><Check size={16} />{t(confirmation === 'unbind' ? '确认解除绑定' : '确认取消绑定')}</button>
        </div>}
        {pollStopped && pending && !expired && <p role="status">{t('自动刷新已暂停，请手动刷新绑定状态。')}</p>}
      </>}
    </section>
  );
}
